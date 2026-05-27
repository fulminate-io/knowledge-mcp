// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestCollect_GitHubTreeURL_EndToEnd points the seed URL at a github tree
// URL and asserts the resulting CollectResult batch contains:
//   - one gh-root NodeGithubRepo
//   - NodeFile children for every regular file in the fixture tarball
//   - chunk nodes for the .go files
//   - CONTAINS edges from the gh-root to each NodeFile
func TestCollect_GitHubTreeURL_EndToEnd(t *testing.T) {
	sink := initWebTestSink(t)

	// Serve the fixture tarball at the codeload override.
	tarball := generateFixtureTarball(t)
	srv := fixtureCodeloadServer(t, tarball)
	t.Cleanup(srv.Close)
	withCodeloadBaseURL(t, srv.URL)

	// The seed URL is a github.com tree URL — the materializer
	// translates it to a codeload fetch against the override.
	seed := "https://github.com/owner/repo/tree/main"

	opts := CrawlOptions{
		Source:           "test-gh-tree",
		SeedURLs:         []string{seed},
		MaxDepth:         0,
		MaxPages:         1,
		PolitenessMs:     0,
		MaxDownloadBytes: 50 << 20,
	}
	ctx := WithCrawlOptions(context.Background(), opts)

	err := collector.Collect(ctx, "web", "test-gh-tree", collector.CollectOptions{Force: true})
	require.NoError(t, err)

	batch := sink.last()
	require.NotNil(t, batch, "collector must hand a CollectResult to the sink")

	counts := countCapturedNodeTypes(batch.Nodes)
	assert.Equal(t, 1, counts["github_repo"], "exactly one gh-root expected")
	assert.GreaterOrEqual(t, counts["file"], 3, "at least 3 NodeFile entries expected (README + 3 .go)")
	// At minimum one chunk node from the parser. Tree-sitter chunk
	// types are raw grammar node names (e.g. "function_declaration"),
	// so we count anything that isn't file/language/github_repo.
	totalChunks := 0
	for typ, n := range counts {
		switch typ {
		case "github_repo", "file", "language":
			continue
		default:
			totalChunks += n
		}
	}
	assert.Positive(t, totalChunks, "expected at least one chunk node from the parser")

	// Verify gh-root → NodeFile CONTAINS edges exist in the batch.
	var ghRootID string
	for _, n := range batch.Nodes {
		if kgtypes.NodeType(n.Type) == kgtypes.NodeGithubRepo {
			ghRootID = n.Id
		}
	}
	require.NotEmpty(t, ghRootID, "gh-root ID not found")

	containsCount := 0
	for _, e := range batch.Edges {
		if e.FromID == ghRootID && e.Type == kgtypes.EdgeKGContains {
			containsCount++
		}
	}
	assert.Positive(t, containsCount,
		"gh-root must hold at least one EdgeKGContains link to a NodeFile (lowercase 'contains' is the recipe DSL idiom)")
}

// TestCollect_GitHubBlobURL_EndToEnd points the seed URL at a github blob
// URL. Whole-repo materialization downloads the entire tarball; the blob
// URL's per-URL link target is the specific NodeFile for the file the URL
// points at (verified by inspecting the namespaced node ID).
func TestCollect_GitHubBlobURL_EndToEnd(t *testing.T) {
	sink := initWebTestSink(t)

	tarball := generateFixtureTarball(t)
	srv := fixtureCodeloadServer(t, tarball)
	t.Cleanup(srv.Close)
	withCodeloadBaseURL(t, srv.URL)

	seed := "https://github.com/owner/repo/blob/main/pkg/foo.go"

	opts := CrawlOptions{
		Source:           "test-gh-blob",
		SeedURLs:         []string{seed},
		MaxDepth:         0,
		MaxPages:         1,
		PolitenessMs:     0,
		MaxDownloadBytes: 50 << 20,
	}
	ctx := WithCrawlOptions(context.Background(), opts)

	err := collector.Collect(ctx, "web", "test-gh-blob", collector.CollectOptions{Force: true})
	require.NoError(t, err)

	batch := sink.last()
	require.NotNil(t, batch)
	counts := countCapturedNodeTypes(batch.Nodes)
	assert.Equal(t, 1, counts["github_repo"], "exactly one gh-root for the repo")
	// Whole repo materialized — at least the README + 3 .go files.
	assert.GreaterOrEqual(t, counts["file"], 3, "expected NodeFile entries for every file in the tarball")

	// The blob URL's specific NodeFile must exist in the batch.
	wantFileID := "owner/repo@main/pkg/foo.go"
	var fooFound bool
	for _, n := range batch.Nodes {
		if kgtypes.NodeType(n.Type) == kgtypes.NodeFile && n.Id == wantFileID {
			fooFound = true
		}
	}
	assert.True(t, fooFound, "expected NodeFile %q (the blob URL's per-URL target)", wantFileID)
}

// TestCollect_NonGithubRegression confirms the existing HTML path is
// unchanged for a non-github URL.
func TestCollect_NonGithubRegression(t *testing.T) {
	sink := initWebTestSink(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(fixtureHTML))
	}))
	t.Cleanup(srv.Close)

	opts := CrawlOptions{
		Source:       "test-regression",
		SeedURLs:     []string{srv.URL},
		MaxDepth:     0,
		MaxPages:     1,
		PolitenessMs: 0,
	}
	ctx := WithCrawlOptions(context.Background(), opts)

	require.NoError(t, collector.Collect(ctx, "web", "test-regression", collector.CollectOptions{Force: true}))

	batch := sink.last()
	require.NotNil(t, batch)
	counts := countCapturedNodeTypes(batch.Nodes)
	assert.Equal(t, 1, counts["page"], "exactly one HTML page node")
	assert.Equal(t, 0, counts["github_repo"], "no gh-root nodes from non-github URL")
	assert.Equal(t, 0, counts["file"], "no NodeFile entries from non-github URL")
}

// TestCollect_GitHubSizeCap_EmitsWarning confirms an oversized fixture
// produces a warning NodeDocument and does not panic.
func TestCollect_GitHubSizeCap_EmitsWarning(t *testing.T) {
	sink := initWebTestSink(t)

	tarball := generateOversizeFixtureTarball(t)
	srv := fixtureCodeloadServer(t, tarball)
	t.Cleanup(srv.Close)
	withCodeloadBaseURL(t, srv.URL)

	seed := "https://github.com/owner/repo/tree/main"

	opts := CrawlOptions{
		Source:           "test-gh-sizecap",
		SeedURLs:         []string{seed},
		MaxDepth:         0,
		MaxPages:         1,
		PolitenessMs:     0,
		MaxDownloadBytes: 1 << 20, // 1 MiB cap, fixture is 200 MiB
	}
	ctx := WithCrawlOptions(context.Background(), opts)

	require.NoError(t, collector.Collect(ctx, "web", "test-gh-sizecap", collector.CollectOptions{Force: true}))

	batch := sink.last()
	require.NotNil(t, batch)
	counts := countCapturedNodeTypes(batch.Nodes)
	assert.Zero(t, counts["github_repo"], "no gh-root on size-cap reject")
	assert.Zero(t, counts["file"], "no NodeFile on size-cap reject")

	// Warning is a NodeDocument with metadata materialization_skipped.
	hasWarning := false
	for _, n := range batch.Nodes {
		if kgtypes.NodeType(n.Type) == kgtypes.NodeDocument && strings.HasPrefix(n.Id, "gh-warn:") {
			hasWarning = true
		}
	}
	assert.True(t, hasWarning, "expected a gh-warn warning NodeDocument")
}
