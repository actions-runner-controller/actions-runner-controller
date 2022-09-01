package scalesetlistener

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/actions-runner-controller/actions-runner-controller/github"
	"github.com/actions-runner-controller/actions-runner-controller/pkg/github/scalesetclient"
	"github.com/go-logr/logr"
)

type Listener struct {
	ghConfig *github.Config
	logger   logr.Logger
	message  chan struct{}
}

func New(ghConfig *github.Config, logger logr.Logger, message chan struct{}) *Listener {
	return &Listener{
		ghConfig: ghConfig,
		logger:   logger,
		message:  message,
	}
}

func (l *Listener) Validate() error {
	c := l.ghConfig
	if anyEmpty(c.RunnerRepository, c.RunnerOrg, c.Token, c.RunnerScaleSetName) {
		return fmt.Errorf("GitHub config is not provided: %q, %q, %q, %q", c.RunnerRepository, c.RunnerOrg, c.Token, c.RunnerScaleSetName)
	}

	hasToken := len(c.Token) > 0
	hasPrivateKeyConfig := c.AppID > 0 && c.AppInstallationID > 0 && c.AppPrivateKey != ""
	hasBasicAuth := len(c.BasicauthUsername) > 0 && len(c.BasicauthPassword) > 0

	if !hasToken && !hasPrivateKeyConfig && !hasBasicAuth {
		return fmt.Errorf("GitHub client cannot initialize. Must provide any of token or private key or basic auth creds.")
	}
	return nil
}

func (l *Listener) Run(ctx context.Context) error {
	ghClient, err := l.ghConfig.NewClient()
	if err != nil {
		return fmt.Errorf("Client creation failed: %v", err)
	}

	builder := &builder{
		ctx:                ctx,
		runnerEnterprise:   l.ghConfig.RunnerEnterprise,
		runnerOrg:          l.ghConfig.RunnerOrg,
		runnerRepository:   l.ghConfig.RunnerRepository,
		runnerScaleSetName: l.ghConfig.RunnerScaleSetName,
		ghClient:           ghClient,
		logger:             l.logger,
	}
	err = builder.createAdminConn().
		createServiceClient().
		createRunnerScaleSet().
		createSession()
	if err != nil {
		return err
	}
	defer builder.destroy()

	messageLoop := &messageLoop{
		logger: l.logger,
		b:      builder,
	}

	return messageLoop.runAndNotify(ctx, l.message)
}

type builder struct {
	// fields that should be passed by the caller

	ctx                context.Context
	runnerEnterprise   string
	runnerOrg          string
	runnerRepository   string
	runnerScaleSetName string
	ghClient           *github.Client
	logger             logr.Logger

	// fields built by the builder

	actionsAdminConnection *github.ActionsServiceAdminConnection
	actionsServiceClient   *github.ActionsClient
	runnerScaleSet         *github.RunnerScaleSet
	session                *github.RunnerScaleSetSession

	// err is the first error encountered during building steps
	err error
}

func (b *builder) createAdminConn() *builder {
	var err error
	b.actionsAdminConnection, err = b.ghClient.GetActionsServiceAdminConnection(b.ctx, b.runnerEnterprise, b.runnerOrg, b.runnerRepository)
	if err != nil {
		b.err = fmt.Errorf("Could not create an Actions Service admin connection: %v", err)
	}
	return b
}

func (b *builder) createServiceClient() *builder {
	if b.err != nil {
		return b
	}
	b.actionsServiceClient = newActionsClient(b.actionsAdminConnection)
	return b
}

func (b *builder) createRunnerScaleSet() *builder {
	if b.err != nil {
		return b
	}
	b.runnerScaleSet, b.err = createRunnerScaleSet(b.ctx, b.logger, b.actionsServiceClient, b.runnerScaleSetName)
	return b
}

func (b *builder) createSession() error {
	if b.err != nil {
		return b.err
	}

	hostName, err := os.Hostname()
	if err != nil {
		b.err = fmt.Errorf("Get host name failed: %v", err)
	}

	b.session, b.err = createRunnerScaleSetSession(b.ctx, b.actionsServiceClient, b.runnerScaleSet.Id, hostName)
	return b.err
}

func (b *builder) destroy() error {
	return b.actionsServiceClient.DeleteMessageSession(b.ctx, b.session.RunnerScaleSet.Id, b.session.SessionId)
}

type messageLoop struct {
	logger logr.Logger
	b      *builder
}

func (ml *messageLoop) runAndNotify(ctx context.Context, notify chan struct{}) error {
	var (
		actionsAdminConnection = ml.b.actionsAdminConnection
		actionsServiceClient   = ml.b.actionsServiceClient
		session                = ml.b.session
		ghClient               = ml.b.ghClient
	)

	var (
		runnerEnterprise = ml.b.runnerEnterprise
		runnerOrg        = ml.b.runnerOrg
		runnerRepository = ml.b.runnerRepository
	)

	var lastMessageId int64 = 0
	for {
		ml.logger.Info("Waiting for message...")

		select {
		case <-ctx.Done():
			ml.logger.Info("Message queue listener is stopped.")
			return nil
		default:
		}

		message, err := getMessage(ctx, actionsServiceClient, ml.logger, session.MessageQueueUrl, session.MessageQueueAccessToken, lastMessageId)
		if err != nil {
			var tokenExpiredErr *github.MessageQueueTokenExpiredError
			if !errors.As(err, &tokenExpiredErr) {
				ml.logger.Error(err, "Error: Get message failed.")
				continue
			}

			ml.logger.Info("Message queue token is expired, refreshing...")
			ml.b.actionsAdminConnection, err = ghClient.GetActionsServiceAdminConnection(ctx, runnerEnterprise, runnerOrg, runnerRepository)
			if err != nil {
				ml.logger.Error(err, "Error: Get Actions service admin connection failed during message session refresh.")
				continue
			}

			actionsServiceClient.ActionsServiceAdminToken = actionsAdminConnection.AdminToken

			session, err = ml.b.actionsServiceClient.RefreshMessageSession(ctx, session.RunnerScaleSet.Id, session.SessionId)
			if err != nil {
				ml.logger.Error(err, "Error: Refresh message session failed.")
				continue
			}
		}

		if message == nil {
			continue
		}

		lastMessageId = message.MessageId

		switch message.MessageType {
		case "RunnerScaleSetJobAvailable":
			scalesetclient.MaybeAcquireJob(ctx, ml.logger, ml.b.actionsServiceClient, ml.b.session, message)
		case "RunnerScaleSetJobAssigned":
			scalesetclient.HandleJobAssignment(ctx, ml.logger, ml.b.actionsServiceClient, ml.b.runnerScaleSet, message)
		case "RunnerScaleSetJobCompleted":
			scalesetclient.NoopHandleJobCompletion(ml.logger, message)
		default:
			ml.logger.Info("Unknown message type received.", "messageType", message.MessageType)
		}
	}
}

func newActionsClient(actionsAdminConnection *github.ActionsServiceAdminConnection) *github.ActionsClient {
	return &github.ActionsClient{
		Client:                   http.DefaultClient,
		ActionsServiceURL:        actionsAdminConnection.ActionsServiceUrl,
		ActionsServiceAdminToken: actionsAdminConnection.AdminToken,
		UserAgent:                "actions-runner-controller-message-queue-listener",
	}
}

func newRunnerScaleSet(scaleSetName string) *github.RunnerScaleSet {
	return &github.RunnerScaleSet{
		Name:          scaleSetName,
		RunnerGroupId: 1,
		Labels: []github.Label{
			{Name: scaleSetName, Type: "User"},
		},
		RunnerSetting: github.RunnerSetting{
			Ephemeral:     true,
			DisableUpdate: true,
		},
	}
}

func createRunnerScaleSet(ctx context.Context, logger logr.Logger, actionsServiceClient *github.ActionsClient, name string) (*github.RunnerScaleSet, error) {
	runnerScaleSet, err := actionsServiceClient.GetRunnerScaleSet(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("Can not found runner scale set: %v", err)
	}

	if runnerScaleSet != nil {
		logger.Info("Get runner scale set.", "id", runnerScaleSet.Id, "name", runnerScaleSet.Name)

		replaceRunnerScaleSet := newRunnerScaleSet(name)

		runnerScaleSet, err = actionsServiceClient.ReplaceRunnerScaleSet(ctx, runnerScaleSet.Id, replaceRunnerScaleSet)
		if err != nil {
			return nil, fmt.Errorf("Create runner scale set failed: %v", err)
		}
	} else {
		logger.Info("Runner scale set is not found, creating a new one.")

		newRunnerScaleSet := newRunnerScaleSet(name)

		runnerScaleSet, err = actionsServiceClient.CreateRunnerScaleSet(ctx, newRunnerScaleSet)
		if err != nil {
			return nil, fmt.Errorf("Create runner scale set failed: %v", err)
		}
	}

	return runnerScaleSet, nil
}

func createRunnerScaleSetSession(ctx context.Context, actionsServiceClient *github.ActionsClient, scaleSetId int, name string) (*github.RunnerScaleSetSession, error) {
	retries := 3
	var (
		runnerScaleSetSession *github.RunnerScaleSetSession
		err                   error
	)
	for i := 0; i < retries; i++ {
		runnerScaleSetSession, err = actionsServiceClient.CreateMessageSession(ctx, scaleSetId, name)
		if err == nil {
			return runnerScaleSetSession, nil
		}
		log.Println("Unable to create message session. Will try again in 30 seconds", "error", err.Error())
		retries--
		time.Sleep(30 * time.Second)
	}

	return nil, fmt.Errorf("Unable to create session in %d attempts", retries)
}

func getMessage(ctx context.Context, c *github.ActionsClient, logger logr.Logger, messageQueueUrl, messageQueueToken string, lastMessageId int64) (*github.RunnerScaleSetMessage, error) {
	message, err := c.GetMessage(ctx, messageQueueUrl, messageQueueToken, lastMessageId)
	if err != nil {
		return nil, err
	}

	if message == nil {
		return nil, nil
	}

	logger.Info("Get message.", "messageId", message.MessageId, "messageType", message.MessageType, "body", message.Body)

	defer deleteMessage(ctx, c, logger, messageQueueUrl, messageQueueToken, message.MessageId)

	return message, nil
}

func deleteMessage(ctx context.Context, c *github.ActionsClient, logger logr.Logger, messageQueueUrl, messageQueueToken string, messageId int64) {
	if err := c.DeleteMessage(ctx, messageQueueUrl, messageQueueToken, messageId); err != nil {
		logger.Error(err, "Error: Delete message failed.")
	}

	logger.Info("Deleted message.", "messageId", messageId)
}

func anyEmpty(first string, others ...string) bool {
	values := append(others, first)
	for _, v := range values {
		if len(v) == 0 {
			return true
		}
	}
	return false
}
