package azurekeyvault

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
)

type AzureKeyVault struct {
	client *azsecrets.Client
}

type Config struct {
	ClientID string `json:"client_id"`
	TenantID string `json:"tenant_id"`
	JWT      string `json:"jwt"`
	URL      string `json:"url"`
}

func (c *Config) getAssertion(ctx context.Context) (string, error) {
	return c.JWT, nil
}

func New(cfg Config) (*AzureKeyVault, error) {
	cred, err := azidentity.NewClientAssertionCredential(
		cfg.TenantID,
		cfg.ClientID,
		cfg.getAssertion,
		&azidentity.ClientAssertionCredentialOptions{
			ClientOptions: azcore.ClientOptions{
				Transport: nil,
			},
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create client assertion credential: %w", err)
	}

	client, err := azsecrets.NewClient(cfg.URL, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize keyvault client: %w", err)
	}

	return &AzureKeyVault{client: client}, nil
}

func (v *AzureKeyVault) GetSecret(ctx context.Context, name, version string) (string, error) {
	secret, err := v.client.GetSecret(ctx, name, version, nil)
	if err != nil {
		return "", fmt.Errorf("failed to get secret: %w", err)
	}
	if secret.Value == nil {
		return "", fmt.Errorf("secret value is nil")
	}

	return *secret.Value, nil
}
