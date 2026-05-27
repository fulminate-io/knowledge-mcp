// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"os"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
)

func TestBuildSubCollectors_ReturnsSevenSubcollectors(t *testing.T) {
	// Set a dummy token so the client doesn't fail.
	t.Setenv("GITLAB_TOKEN", "test-token")

	subs := buildSubCollectors("test-token", "mygroup")
	if subs == nil {
		t.Fatal("expected non-nil subcollectors slice")
	}
	if len(subs) != 7 {
		t.Fatalf("expected 7 subcollectors, got %d", len(subs))
	}

	// OIDC federation is no longer a subcollector — it runs as the "gitlab"
	// PostPopulate hook (register_postpopulate.go), reading cloud graphs over
	// the wire after upload instead of via the (nil) client store engine.
	expected := []string{
		"gitlab-projects",
		"gitlab-pipelines",
		"gitlab-pipeline-runs",
		"gitlab-runners",
		"gitlab-environments",
		"gitlab-deployments",
		"gitlab-variables",
	}

	for i, name := range expected {
		if subs[i].Name() != name {
			t.Errorf("subcollector %d: expected name %q, got %q", i, name, subs[i].Name())
		}
	}
}

func TestBuildSubCollectors_SubcollectorNames(t *testing.T) {
	subs := buildSubCollectors("token", "group")
	if subs == nil {
		t.Fatal("expected non-nil subcollectors")
	}

	names := make(map[string]bool)
	for _, s := range subs {
		if names[s.Name()] {
			t.Errorf("duplicate subcollector name: %s", s.Name())
		}
		names[s.Name()] = true
	}
}

func TestCollector_MissingToken(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "")
	t.Setenv("GITLAB_PRIVATE_TOKEN", "")

	c := &GitLabCollector{}
	_, err := c.Collect(t.Context(), "mygroup", collector.CollectOptions{})
	if err == nil {
		t.Fatal("expected error for missing token")
	}
}

func TestNewClient_DefaultURL(t *testing.T) {
	os.Unsetenv("GITLAB_URL")

	client, baseURL, err := newClient("test-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if baseURL != defaultGitLabURL {
		t.Errorf("expected base URL %q, got %q", defaultGitLabURL, baseURL)
	}
}

func TestNewClient_CustomURL(t *testing.T) {
	t.Setenv("GITLAB_URL", "https://gitlab.example.com/")

	client, baseURL, err := newClient("test-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if baseURL != "https://gitlab.example.com/" {
		t.Errorf("expected custom URL, got %q", baseURL)
	}
}
