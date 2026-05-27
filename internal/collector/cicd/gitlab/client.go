// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"os"

	gl "gitlab.com/gitlab-org/api/client-go"
)

const defaultGitLabURL = "https://gitlab.com/"

// newClient creates a go-gitlab client configured for the given token.
// If GITLAB_URL is set, it overrides the default GitLab base URL to support
// self-hosted instances. Returns the client and the base URL for OIDC issuer
// matching.
func newClient(token string) (*gl.Client, string, error) {
	baseURL := os.Getenv("GITLAB_URL")
	if baseURL == "" {
		baseURL = defaultGitLabURL
	}

	var opts []gl.ClientOptionFunc
	if baseURL != defaultGitLabURL {
		opts = append(opts, gl.WithBaseURL(baseURL))
	}

	client, err := gl.NewClient(token, opts...)
	if err != nil {
		return nil, "", err
	}

	return client, baseURL, nil
}
