// SPDX-License-Identifier: Apache-2.0

package bitbucket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func testClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewClient("user", "pass")
	c.baseURL = srv.URL
	return c
}

func TestRepos_Collect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/ws1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"values":[
			{"uuid":"{aaa}","slug":"repo-a","full_name":"ws1/repo-a","is_private":true,"scm":"git","language":"go","mainbranch":{"name":"main"}},
			{"uuid":"{bbb}","slug":"repo-b","full_name":"ws1/repo-b","is_private":false,"scm":"git","language":"","mainbranch":{"name":"master"}}
		]}`))
	})

	c := testClient(t, mux)
	sub := newReposCollector(c, "ws1")
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)

	// 1 workspace + 2 repos = 3 resources.
	assert.Len(t, result.Resources, 3)
	assert.Equal(t, "workspace", result.Resources[0].ResourceType)
	assert.Equal(t, "repository", result.Resources[1].ResourceType)
	assert.Equal(t, "repository", result.Resources[2].ResourceType)

	// 2 BELONGS_TO edges (repo → workspace).
	assert.Len(t, result.Edges, 2)
	for _, e := range result.Edges {
		assert.Equal(t, kgtypes.EdgeBelongsTo, e.Relationship)
		assert.Contains(t, e.TargetID, "Workspace")
	}

	// Verify metadata.
	repoA := result.Resources[1]
	assert.Equal(t, "true", repoA.Metadata["is_private"])
	assert.Equal(t, "main", repoA.Metadata["mainbranch"])
	assert.Equal(t, "go", repoA.Metadata["language"])
}

func TestRunners_Collect(t *testing.T) {
	mux := http.NewServeMux()
	// Workspace runners.
	mux.HandleFunc("/workspaces/ws1/pipelines-config/runners", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"values":[
			{"uuid":"r1","name":"runner-1","labels":[{"name":"linux"},{"name":"self-hosted"}],"state":{"status":"ONLINE"}}
		]}`))
	})
	// Repo runners — same UUID r1 to test dedup, plus new r2.
	mux.HandleFunc("/repositories/ws1/repo-a/pipelines-config/runners", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"values":[
			{"uuid":"r1","name":"runner-1","labels":[{"name":"linux"}],"state":{"status":"ONLINE"}},
			{"uuid":"r2","name":"runner-2","labels":[],"state":{"status":"OFFLINE"}}
		]}`))
	})

	c := testClient(t, mux)
	repos := []repoInfo{{Slug: "repo-a"}}
	sub := newRunnersCollector(c, "ws1", repos)
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)

	// r1 (deduped) + r2 = 2 runners + 2 labels (linux, self-hosted) = 4 resources.
	runnerCount := 0
	labelCount := 0
	for _, res := range result.Resources {
		switch res.ResourceType {
		case "runner":
			runnerCount++
		case "label":
			labelCount++
		}
	}
	assert.Equal(t, 2, runnerCount, "should have 2 unique runners")
	assert.Equal(t, 2, labelCount, "should have 2 unique labels")

	// Verify edges: BELONGS_TO + HAS_LABEL.
	belongsCount := 0
	labelEdgeCount := 0
	for _, e := range result.Edges {
		switch e.Relationship {
		case kgtypes.EdgeBelongsTo:
			belongsCount++
		case kgtypes.EdgeHasLabel:
			labelEdgeCount++
		}
	}
	assert.Equal(t, 2, belongsCount)
	assert.Equal(t, 2, labelEdgeCount)
}

func TestEnvironments_Collect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repositories/ws1/repo-a/environments", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"values":[
			{"uuid":"{e1}","name":"staging","slug":"staging","rank":1,"environment_type":{"name":"Staging"},"lock":{"type":"none"},"restrictions":{"admin_only":[]}},
			{"uuid":"{e2}","name":"production","slug":"production","rank":2,"environment_type":{"name":"Production"},"lock":{"type":"lock"},"restrictions":{"admin_only":["reviewer1"]}}
		]}`))
	})

	c := testClient(t, mux)
	repos := []repoInfo{{Slug: "repo-a"}}
	sub := newEnvironmentsCollector(c, "ws1", repos)
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)

	// 2 envs + 1 approval gate for production = 3 resources.
	assert.Len(t, result.Resources, 3)

	// Verify approval gate for production.
	var gateFound bool
	for _, res := range result.Resources {
		if res.ResourceType == "approval_gate" {
			gateFound = true
			assert.Equal(t, "production approval", res.Name)
		}
	}
	assert.True(t, gateFound, "should emit approval gate for production")

	// Verify edges: 2 BELONGS_TO + 1 REQUIRES_APPROVAL.
	edgeTypes := make(map[kgtypes.EdgeType]int)
	for _, e := range result.Edges {
		edgeTypes[e.Relationship]++
	}
	assert.Equal(t, 2, edgeTypes[kgtypes.EdgeBelongsTo])
	assert.Equal(t, 1, edgeTypes[kgtypes.EdgeRequiresApproval])
}

func TestVariables_Collect_NoValues(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/workspaces/ws1/pipelines-config/variables", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"values":[
			{"uuid":"v1","key":"API_KEY","secured":true,"system":false,"value":"super-secret-123"},
			{"uuid":"v2","key":"DEBUG","secured":false,"system":false,"value":"true"}
		]}`))
	})
	mux.HandleFunc("/repositories/ws1/repo-a/pipelines-config/variables", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"values":[{"uuid":"v3","key":"REPO_VAR","secured":false}]}`))
	})
	// Environment list for deployment vars.
	mux.HandleFunc("/repositories/ws1/repo-a/environments", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"values":[]}`))
	})

	c := testClient(t, mux)
	repos := []repoInfo{{Slug: "repo-a"}}
	sub := newVariablesCollector(c, "ws1", repos)
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)

	// 2 workspace vars + 1 repo var = 3 resources.
	assert.Len(t, result.Resources, 3)

	// CRITICAL: no resource should contain "super-secret-123" or "value" field
	// with actual secret data.
	for _, res := range result.Resources {
		content := string(res.Content)
		assert.NotContains(t, content, "super-secret-123",
			"variable value must NOT be stored for %s", res.Name)
		assert.NotContains(t, content, `"value"`,
			"value field must NOT appear in content for %s", res.Name)
	}

	// All edges should be BELONGS_TO.
	assert.Len(t, result.Edges, 3)
	for _, e := range result.Edges {
		assert.Equal(t, kgtypes.EdgeBelongsTo, e.Relationship)
	}
}
