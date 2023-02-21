package actions_test

import (
	"testing"

	"github.com/actions/actions-runner-controller/github/actions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_Identifier(t *testing.T) {
	t.Run("configURL changes", func(t *testing.T) {
		scenarios := []struct {
			name string
			url  string
		}{
			{
				name: "url of a different repo",
				url:  "https://github.com/org/repo2",
			},
			{
				name: "url of an org",
				url:  "https://github.com/org",
			},
			{
				name: "url of an enterprise",
				url:  "https://github.com/enterprises/my-enterprise",
			},
			{
				name: "url of a self-hosted github",
				url:  "https://selfhosted.com/org/repo",
			},
		}

		configURL := "https://github.com/org/repo"
		defaultCreds := &actions.ActionsAuth{
			Token: "token",
		}
		oldClient, err := actions.NewClient(configURL, defaultCreds)
		require.NoError(t, err)

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				newClient, err := actions.NewClient(scenario.url, defaultCreds)
				require.NoError(t, err)
				assert.NotEqual(t, oldClient.Identifier(), newClient.Identifier())
			})
		}
	})

	t.Run("credentials change", func(t *testing.T) {
		defaultTokenCreds := &actions.ActionsAuth{
			Token: "token",
		}
		defaultAppCreds := &actions.ActionsAuth{
			AppCreds: &actions.GitHubAppAuth{
				AppID:             123,
				AppInstallationID: 123,
				AppPrivateKey:     "private key",
			},
		}

		scenarios := []struct {
			name string
			old  *actions.ActionsAuth
			new  *actions.ActionsAuth
		}{
			{
				name: "different token",
				old:  defaultTokenCreds,
				new: &actions.ActionsAuth{
					Token: "new token",
				},
			},
			{
				name: "changing from token to github app",
				old:  defaultTokenCreds,
				new:  defaultAppCreds,
			},
			{
				name: "changing from github app to token",
				old:  defaultAppCreds,
				new:  defaultTokenCreds,
			},
			{
				name: "different github app",
				old:  defaultAppCreds,
				new: &actions.ActionsAuth{
					AppCreds: &actions.GitHubAppAuth{
						AppID:             456,
						AppInstallationID: 456,
						AppPrivateKey:     "new private key",
					},
				},
			},
		}

		defaultConfigURL := "https://github.com/org/repo"

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				oldClient, err := actions.NewClient(defaultConfigURL, scenario.old)
				require.NoError(t, err)

				newClient, err := actions.NewClient(defaultConfigURL, scenario.new)
				require.NoError(t, err)
				assert.NotEqual(t, oldClient.Identifier(), newClient.Identifier())
			})
		}
	})
}
