package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/actions-runner-controller/actions-runner-controller/github/metrics"
	"github.com/actions-runner-controller/actions-runner-controller/logging"
	"github.com/bradleyfalzon/ghinstallation"
	"github.com/go-logr/logr"
	"github.com/google/go-github/v39/github"
	"github.com/gregjones/httpcache"
	"golang.org/x/oauth2"
)

// Config contains configuration for Github client
type Config struct {
	EnterpriseURL     string `split_words:"true"`
	AppID             int64  `split_words:"true"`
	AppInstallationID int64  `split_words:"true"`
	AppPrivateKey     string `split_words:"true"`
	Token             string
	URL               string `split_words:"true"`
	UploadURL         string `split_words:"true"`
	BasicauthUsername string `split_words:"true"`
	BasicauthPassword string `split_words:"true"`
	RunnerGitHubURL   string `split_words:"true"`

	Log *logr.Logger
}

// Client wraps GitHub client with some additional
type Client struct {
	*github.Client
	regTokens map[string]*github.RegistrationToken
	mu        sync.Mutex
	// GithubBaseURL to Github without API suffix.
	GithubBaseURL string
}

type BasicAuthTransport struct {
	Username string
	Password string
}

func (p BasicAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.SetBasicAuth(p.Username, p.Password)
	return http.DefaultTransport.RoundTrip(req)
}

// NewClient creates a Github Client
func (c *Config) NewClient() (*Client, error) {
	var transport http.RoundTripper
	if len(c.BasicauthUsername) > 0 && len(c.BasicauthPassword) > 0 {
		transport = BasicAuthTransport{Username: c.BasicauthUsername, Password: c.BasicauthPassword}
	} else if len(c.Token) > 0 {
		transport = oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: c.Token})).Transport
	} else {
		var tr *ghinstallation.Transport

		if _, err := os.Stat(c.AppPrivateKey); err == nil {
			tr, err = ghinstallation.NewKeyFromFile(http.DefaultTransport, c.AppID, c.AppInstallationID, c.AppPrivateKey)
			if err != nil {
				return nil, fmt.Errorf("authentication failed: using private key at %s: %v", c.AppPrivateKey, err)
			}
		} else {
			tr, err = ghinstallation.New(http.DefaultTransport, c.AppID, c.AppInstallationID, []byte(c.AppPrivateKey))
			if err != nil {
				return nil, fmt.Errorf("authentication failed: using private key of size %d (%s...): %v", len(c.AppPrivateKey), strings.Split(c.AppPrivateKey, "\n")[0], err)
			}
		}

		if len(c.EnterpriseURL) > 0 {
			githubAPIURL, err := getEnterpriseApiUrl(c.EnterpriseURL)
			if err != nil {
				return nil, fmt.Errorf("enterprise url incorrect: %v", err)
			}
			tr.BaseURL = githubAPIURL
		}
		transport = tr
	}

	cached := httpcache.NewTransport(httpcache.NewMemoryCache())
	cached.Transport = transport
	loggingTransport := logging.Transport{Transport: cached, Log: c.Log}
	metricsTransport := metrics.Transport{Transport: loggingTransport}
	httpClient := &http.Client{Transport: metricsTransport}

	var client *github.Client
	var githubBaseURL string
	if len(c.EnterpriseURL) > 0 {
		var err error
		client, err = github.NewEnterpriseClient(c.EnterpriseURL, c.EnterpriseURL, httpClient)
		if err != nil {
			return nil, fmt.Errorf("enterprise client creation failed: %v", err)
		}
		githubBaseURL = fmt.Sprintf("%s://%s%s", client.BaseURL.Scheme, client.BaseURL.Host, strings.TrimSuffix(client.BaseURL.Path, "api/v3/"))
	} else {
		client = github.NewClient(httpClient)
		githubBaseURL = "https://github.com/"

		if len(c.URL) > 0 {
			baseUrl, err := url.Parse(c.URL)
			if err != nil {
				return nil, fmt.Errorf("github client creation failed: %v", err)
			}
			if !strings.HasSuffix(baseUrl.Path, "/") {
				baseUrl.Path += "/"
			}
			client.BaseURL = baseUrl
		}

		if len(c.UploadURL) > 0 {
			uploadUrl, err := url.Parse(c.UploadURL)
			if err != nil {
				return nil, fmt.Errorf("github client creation failed: %v", err)
			}
			if !strings.HasSuffix(uploadUrl.Path, "/") {
				uploadUrl.Path += "/"
			}
			client.UploadURL = uploadUrl
		}

		if len(c.RunnerGitHubURL) > 0 {
			githubBaseURL = c.RunnerGitHubURL
			if !strings.HasSuffix(githubBaseURL, "/") {
				githubBaseURL += "/"
			}
		}
	}

	client.UserAgent = "actions-runner-controller"

	return &Client{
		Client:        client,
		regTokens:     map[string]*github.RegistrationToken{},
		mu:            sync.Mutex{},
		GithubBaseURL: githubBaseURL,
	}, nil
}

// GetRegistrationToken returns a registration token tied with the name of repository and runner.
func (c *Client) GetRegistrationToken(ctx context.Context, enterprise, org, repo, name string) (*github.RegistrationToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := getRegistrationKey(org, repo, enterprise)
	rt, ok := c.regTokens[key]

	// We'd like to allow the runner just starting up to miss the expiration date by a bit.
	// Note that this means that we're going to cache Creation Registraion Token API response longer than the
	// recommended cache duration.
	//
	// https://docs.github.com/en/rest/reference/actions#create-a-registration-token-for-a-repository
	// https://docs.github.com/en/rest/reference/actions#create-a-registration-token-for-an-organization
	// https://docs.github.com/en/rest/reference/actions#create-a-registration-token-for-an-enterprise
	// https://docs.github.com/en/rest/overview/resources-in-the-rest-api#conditional-requests
	//
	// This is currently set to 30 minutes as the result of the discussion took place at the following issue:
	// https://github.com/actions-runner-controller/actions-runner-controller/issues/1295
	runnerStartupTimeout := 30 * time.Minute

	if ok && rt.GetExpiresAt().After(time.Now().Add(runnerStartupTimeout)) {
		return rt, nil
	}

	enterprise, owner, repo, err := getEnterpriseOrganizationAndRepo(enterprise, org, repo)

	if err != nil {
		return rt, err
	}

	rt, res, err := c.createRegistrationToken(ctx, enterprise, owner, repo)

	if err != nil {
		return nil, fmt.Errorf("failed to create registration token: %v", err)
	}

	if res.StatusCode != 201 {
		return nil, fmt.Errorf("unexpected status: %d", res.StatusCode)
	}

	c.regTokens[key] = rt
	go func() {
		c.cleanup()
	}()

	return rt, nil
}

// RemoveRunner removes a runner with specified runner ID from repository.
func (c *Client) RemoveRunner(ctx context.Context, enterprise, org, repo string, runnerID int64) error {
	enterprise, owner, repo, err := getEnterpriseOrganizationAndRepo(enterprise, org, repo)

	if err != nil {
		return err
	}

	res, err := c.removeRunner(ctx, enterprise, owner, repo, runnerID)

	if err != nil {
		return fmt.Errorf("failed to remove runner: %w", err)
	}

	if res.StatusCode != 204 {
		return fmt.Errorf("unexpected status: %d", res.StatusCode)
	}

	return nil
}

// ListRunners returns a list of runners of specified owner/repository name.
func (c *Client) ListRunners(ctx context.Context, enterprise, org, repo string) ([]*github.Runner, error) {
	enterprise, owner, repo, err := getEnterpriseOrganizationAndRepo(enterprise, org, repo)

	if err != nil {
		return nil, err
	}

	var runners []*github.Runner

	opts := github.ListOptions{PerPage: 100}
	for {
		list, res, err := c.listRunners(ctx, enterprise, owner, repo, &opts)

		if err != nil {
			return runners, fmt.Errorf("failed to list runners: %w", err)
		}

		runners = append(runners, list.Runners...)
		if res.NextPage == 0 {
			break
		}
		opts.Page = res.NextPage
	}

	return runners, nil
}

// ListOrganizationRunnerGroups returns all the runner groups defined in the organization and
// inherited to the organization from an enterprise.
func (c *Client) ListOrganizationRunnerGroups(ctx context.Context, org string) ([]*github.RunnerGroup, error) {
	var runnerGroups []*github.RunnerGroup

	opts := github.ListOptions{PerPage: 100}
	for {
		list, res, err := c.Client.Actions.ListOrganizationRunnerGroups(ctx, org, &opts)
		if err != nil {
			return runnerGroups, fmt.Errorf("failed to list organization runner groups: %w", err)
		}

		runnerGroups = append(runnerGroups, list.RunnerGroups...)
		if res.NextPage == 0 {
			break
		}
		opts.Page = res.NextPage
	}

	return runnerGroups, nil
}

// ListOrganizationRunnerGroupsForRepository returns all the runner groups defined in the organization and
// inherited to the organization from an enterprise.
// We can remove this when google/go-github library is updated to support this.
func (c *Client) ListOrganizationRunnerGroupsForRepository(ctx context.Context, org, repo string) ([]*github.RunnerGroup, error) {
	var runnerGroups []*github.RunnerGroup

	opts := github.ListOptions{PerPage: 100}
	for {
		list, res, err := c.listOrganizationRunnerGroupsVisibleToRepo(ctx, org, repo, &opts)
		if err != nil {
			return runnerGroups, fmt.Errorf("failed to list organization runner groups: %w", err)
		}

		runnerGroups = append(runnerGroups, list.RunnerGroups...)
		if res.NextPage == 0 {
			break
		}
		opts.Page = res.NextPage
	}

	return runnerGroups, nil
}

func (c *Client) ListRunnerGroupRepositoryAccesses(ctx context.Context, org string, runnerGroupId int64) ([]*github.Repository, error) {
	var repos []*github.Repository

	opts := github.ListOptions{PerPage: 100}
	for {
		list, res, err := c.Client.Actions.ListRepositoryAccessRunnerGroup(ctx, org, runnerGroupId, &opts)
		if err != nil {
			return nil, fmt.Errorf("failed to list repository access for runner group: %w", err)
		}

		repos = append(repos, list.Repositories...)
		if res.NextPage == 0 {
			break
		}

		opts.Page = res.NextPage
	}

	return repos, nil
}

// listOrganizationRunnerGroupsVisibleToRepo lists all self-hosted runner groups configured in an organization which can be used by the repository.
//
// GitHub API docs: https://docs.github.com/en/rest/reference/actions#list-self-hosted-runner-groups-for-an-organization
func (c *Client) listOrganizationRunnerGroupsVisibleToRepo(ctx context.Context, org, repo string, opts *github.ListOptions) (*github.RunnerGroups, *github.Response, error) {
	u := fmt.Sprintf("orgs/%v/actions/runner-groups?visible_to_repository=%v", org, repo)

	if opts != nil {
		if opts.PerPage > 0 {
			u = fmt.Sprintf("%v&per_page=%v", u, opts.PerPage)
		}

		if opts.Page > 0 {
			u = fmt.Sprintf("%v&page=%v", u, opts.Page)
		}
	}

	req, err := c.Client.NewRequest("GET", u, nil)
	if err != nil {
		return nil, nil, err
	}

	groups := &github.RunnerGroups{}
	resp, err := c.Client.Do(ctx, req, &groups)
	if err != nil {
		return nil, resp, err
	}

	return groups, resp, nil
}

// cleanup removes expired registration tokens.
func (c *Client) cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key, rt := range c.regTokens {
		if rt.GetExpiresAt().Before(time.Now()) {
			delete(c.regTokens, key)
		}
	}
}

// wrappers for github functions (switch between enterprise/organization/repository mode)
// so the calling functions don't need to switch and their code is a bit cleaner

func (c *Client) createRegistrationToken(ctx context.Context, enterprise, org, repo string) (*github.RegistrationToken, *github.Response, error) {
	if len(repo) > 0 {
		return c.Client.Actions.CreateRegistrationToken(ctx, org, repo)
	}
	if len(org) > 0 {
		return c.Client.Actions.CreateOrganizationRegistrationToken(ctx, org)
	}
	return c.Client.Enterprise.CreateRegistrationToken(ctx, enterprise)
}

func (c *Client) removeRunner(ctx context.Context, enterprise, org, repo string, runnerID int64) (*github.Response, error) {
	if len(repo) > 0 {
		return c.Client.Actions.RemoveRunner(ctx, org, repo, runnerID)
	}
	if len(org) > 0 {
		return c.Client.Actions.RemoveOrganizationRunner(ctx, org, runnerID)
	}
	return c.Client.Enterprise.RemoveRunner(ctx, enterprise, runnerID)
}

func (c *Client) listRunners(ctx context.Context, enterprise, org, repo string, opts *github.ListOptions) (*github.Runners, *github.Response, error) {
	if len(repo) > 0 {
		return c.Client.Actions.ListRunners(ctx, org, repo, opts)
	}
	if len(org) > 0 {
		return c.Client.Actions.ListOrganizationRunners(ctx, org, opts)
	}
	return c.Client.Enterprise.ListRunners(ctx, enterprise, opts)
}

func (c *Client) ListRepositoryWorkflowRuns(ctx context.Context, user string, repoName string) ([]*github.WorkflowRun, error) {
	queued, err := c.listRepositoryWorkflowRuns(ctx, user, repoName, "queued")
	if err != nil {
		return nil, fmt.Errorf("listing queued workflow runs: %w", err)
	}

	inProgress, err := c.listRepositoryWorkflowRuns(ctx, user, repoName, "in_progress")
	if err != nil {
		return nil, fmt.Errorf("listing in_progress workflow runs: %w", err)
	}

	var workflowRuns []*github.WorkflowRun

	workflowRuns = append(workflowRuns, queued...)
	workflowRuns = append(workflowRuns, inProgress...)

	return workflowRuns, nil
}

func (c *Client) listRepositoryWorkflowRuns(ctx context.Context, user string, repoName, status string) ([]*github.WorkflowRun, error) {
	var workflowRuns []*github.WorkflowRun

	opts := github.ListWorkflowRunsOptions{
		ListOptions: github.ListOptions{
			PerPage: 100,
		},
		Status: status,
	}

	for {
		list, res, err := c.Client.Actions.ListRepositoryWorkflowRuns(ctx, user, repoName, &opts)

		if err != nil {
			return workflowRuns, fmt.Errorf("failed to list workflow runs: %v", err)
		}

		workflowRuns = append(workflowRuns, list.WorkflowRuns...)
		if res.NextPage == 0 {
			break
		}
		opts.Page = res.NextPage
	}

	return workflowRuns, nil
}

// Validates enterprise, organization and repo arguments. Both are optional, but at least one should be specified
func getEnterpriseOrganizationAndRepo(enterprise, org, repo string) (string, string, string, error) {
	if len(repo) > 0 {
		owner, repository, err := splitOwnerAndRepo(repo)
		return "", owner, repository, err
	}
	if len(org) > 0 {
		return "", org, "", nil
	}
	if len(enterprise) > 0 {
		return enterprise, "", "", nil
	}
	return "", "", "", fmt.Errorf("enterprise, organization and repository are all empty")
}

func getRegistrationKey(org, repo, enterprise string) string {
	return fmt.Sprintf("org=%s,repo=%s,enterprise=%s", org, repo, enterprise)
}

func splitOwnerAndRepo(repo string) (string, string, error) {
	chunk := strings.Split(repo, "/")
	if len(chunk) != 2 {
		return "", "", fmt.Errorf("invalid repository name: '%s'", repo)
	}
	return chunk[0], chunk[1], nil
}

func getEnterpriseApiUrl(baseURL string) (string, error) {
	baseEndpoint, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(baseEndpoint.Path, "/") {
		baseEndpoint.Path += "/"
	}
	if !strings.HasSuffix(baseEndpoint.Path, "/api/v3/") &&
		!strings.HasPrefix(baseEndpoint.Host, "api.") &&
		!strings.Contains(baseEndpoint.Host, ".api.") {
		baseEndpoint.Path += "api/v3/"
	}

	// Trim trailing slash, otherwise there's double slash added to token endpoint
	return fmt.Sprintf("%s://%s%s", baseEndpoint.Scheme, baseEndpoint.Host, strings.TrimSuffix(baseEndpoint.Path, "/")), nil
}

type RunnerNotFound struct {
	runnerName string
}

func (e *RunnerNotFound) Error() string {
	return fmt.Sprintf("runner %q not found", e.runnerName)
}

type RunnerOffline struct {
	runnerName string
}

func (e *RunnerOffline) Error() string {
	return fmt.Sprintf("runner %q offline", e.runnerName)
}

func (r *Client) IsRunnerBusy(ctx context.Context, enterprise, org, repo, name string) (bool, error) {
	runners, err := r.ListRunners(ctx, enterprise, org, repo)
	if err != nil {
		return false, err
	}

	for _, runner := range runners {
		if runner.GetName() == name {
			if runner.GetStatus() == "offline" {
				return runner.GetBusy(), &RunnerOffline{runnerName: name}
			}
			return runner.GetBusy(), nil
		}
	}

	return false, &RunnerNotFound{runnerName: name}
}
