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

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
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
		Source:            "test-gh-tree",
		SeedURLs:          []string{seed},
		MaxDepth:          0,
		MaxPages:          1,
		PolitenessMs:      0,
		MaterializeGithub: true, // seed-time materialization is caller-requested
		MaxDownloadBytes:  50 << 20,
	}
	ctx := WithCrawlOptions(context.Background(), opts)

	_, err := collector.Collect(ctx, "web", "test-gh-tree", collector.CollectOptions{Force: true})
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
		Source:            "test-gh-blob",
		SeedURLs:          []string{seed},
		MaxDepth:          0,
		MaxPages:          1,
		PolitenessMs:      0,
		MaterializeGithub: true, // seed-time materialization is caller-requested
		MaxDownloadBytes:  50 << 20,
	}
	ctx := WithCrawlOptions(context.Background(), opts)

	_, err := collector.Collect(ctx, "web", "test-gh-blob", collector.CollectOptions{Force: true})
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

	_, err := collector.Collect(ctx, "web", "test-regression", collector.CollectOptions{Force: true})
	require.NoError(t, err)

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
		Source:            "test-gh-sizecap",
		SeedURLs:          []string{seed},
		MaxDepth:          0,
		MaxPages:          1,
		PolitenessMs:      0,
		MaterializeGithub: true,    // seed-time materialization is caller-requested
		MaxDownloadBytes:  1 << 20, // 1 MiB cap, fixture is 200 MiB
	}
	ctx := WithCrawlOptions(context.Background(), opts)

	_, err := collector.Collect(ctx, "web", "test-gh-sizecap", collector.CollectOptions{Force: true})
	require.NoError(t, err)

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

// TestCollect_URICensus_MaterializedNodesCarryURI is the materializer half of
// the ticket's "stamp uri on EVERY emitted node" clause. The materializer's
// nodes land in the SAME web graph as the HTML page nodes, so a census that
// only walked emitFromPage's output would satisfy the clause for one producer
// and silently miss the other.
//
// THE CONTROL THAT MATTERS: enrichForRecipes' loop opens with a NodeFile type
// gate, so a uri stamp written below that gate would reach NodeFile rows only
// and skip every chunk node and language hub — a majority of the slice, and
// invisible to a test that checked one file node. The floor below therefore
// requires a file node AND a chunk node to be present in the checked set.
func TestCollect_URICensus_MaterializedNodesCarryURI(t *testing.T) {
	sink := initWebTestSink(t)

	tarball := generateFixtureTarball(t)
	srv := fixtureCodeloadServer(t, tarball)
	t.Cleanup(srv.Close)
	withCodeloadBaseURL(t, srv.URL)

	seed := "https://github.com/owner/repo/tree/main"

	opts := CrawlOptions{
		Source:            "test-gh-uri-census",
		SeedURLs:          []string{seed},
		MaxDepth:          0,
		MaxPages:          1,
		PolitenessMs:      0,
		MaterializeGithub: true, // seed-time materialization is caller-requested
		MaxDownloadBytes:  50 << 20,
	}
	ctx := WithCrawlOptions(context.Background(), opts)

	_, err := collector.Collect(ctx, "web", "test-gh-uri-census", collector.CollectOptions{Force: true})
	require.NoError(t, err)

	batch := sink.last()
	require.NotNil(t, batch, "collector must hand a CollectResult to the sink")

	// NON-VACUITY FLOOR, drawn from what this fixture is already known to
	// produce (TestCollect_GitHubTreeURL_EndToEnd asserts the same numbers).
	counts := countCapturedNodeTypes(batch.Nodes)
	require.Equal(t, 1, counts["github_repo"], "the census needs the gh-root present")
	require.GreaterOrEqual(t, counts["file"], 3, "the census needs NodeFile rows present")
	chunkTypes := 0
	for typ, n := range counts {
		switch typ {
		case "github_repo", "file", "language":
			continue
		default:
			chunkTypes += n
		}
	}
	require.Positive(t, chunkTypes,
		"the census needs chunk nodes present — they are the ones a stamp below the NodeFile gate would skip")

	// THE CENSUS — every node, reported per-node so a regression names the
	// node kind that lost its stamp.
	missing := 0
	sawFile, sawChunk := false, false
	for _, n := range batch.Nodes {
		got := n.Metadata["uri"]
		if got == "" {
			t.Errorf("materialized node id=%s type=%s carries no uri (metadata=%v)", n.Id, n.Type, n.Metadata)
			missing++
			continue
		}
		assert.Equal(t, seed, got,
			"materialized node id=%s type=%s: uri = %q, want the seed github URL %q", n.Id, n.Type, got, seed)
		switch n.Type {
		case "file":
			sawFile = true
		case "github_repo", "language":
		default:
			sawChunk = true
		}
	}
	assert.Equal(t, 0, missing, "%d of %d materialized nodes carry no uri", missing, len(batch.Nodes))

	// KNOWN-POSITIVE: the census actually reached past the gh-root. A run
	// where only the gh-root was checked is precisely the failure mode where
	// the stamp was written below the NodeFile gate.
	assert.True(t, sawFile, "the census never checked a file node")
	assert.True(t, sawChunk, "the census never checked a chunk node")
}

// TestCollect_URICensus_MaterializerWarningCarriesURI pins the two addresses a
// size-cap warning node holds as DISTINCT FACTS.
//
// md["uri"] is the SEED GITHUB URL the crawl was given — the node's own
// address. md["url"] is the codeload tarball URL that was being downloaded
// when the cap fired. materializerWarning.URL holds the latter at every
// assignment site (github_materializer_fetch.go:131, :200, :336, all fed from
// the tarURL built at :183), so a uri stamped from w.URL would record an
// ephemeral 127.0.0.1 codeload address here and a codeload endpoint in
// production. Asserting equality against the seed — rather than merely
// non-emptiness — is what makes that wrong reading unable to pass.
func TestCollect_URICensus_MaterializerWarningCarriesURI(t *testing.T) {
	sink := initWebTestSink(t)

	tarball := generateOversizeFixtureTarball(t)
	srv := fixtureCodeloadServer(t, tarball)
	t.Cleanup(srv.Close)
	withCodeloadBaseURL(t, srv.URL)

	seed := "https://github.com/owner/repo/tree/main"

	opts := CrawlOptions{
		Source:            "test-gh-warn-uri",
		SeedURLs:          []string{seed},
		MaxDepth:          0,
		MaxPages:          1,
		PolitenessMs:      0,
		MaterializeGithub: true,    // seed-time materialization is caller-requested
		MaxDownloadBytes:  1 << 20, // 1 MiB cap, fixture is 200 MiB
	}
	ctx := WithCrawlOptions(context.Background(), opts)

	_, err := collector.Collect(ctx, "web", "test-gh-warn-uri", collector.CollectOptions{Force: true})
	require.NoError(t, err)

	batch := sink.last()
	require.NotNil(t, batch)

	var warning *knowledgev1.Node
	for _, n := range batch.Nodes {
		if kgtypes.NodeType(n.Type) == kgtypes.NodeDocument && strings.HasPrefix(n.Id, "gh-warn:") {
			warning = n
		}
	}
	require.NotNil(t, warning, "the size-cap path must emit a gh-warn warning node for this assertion to mean anything")

	assert.Equal(t, seed, warning.Metadata["uri"],
		"the warning node's uri must be the seed github URL the crawl was given, not the codeload tarball URL")

	// The OTHER fact, pinned so neither key can be quietly dropped in favor
	// of the other: url still names what was actually being downloaded.
	gotURL := warning.Metadata["url"]
	assert.True(t, strings.HasPrefix(gotURL, srv.URL+"/owner/repo/tar.gz/"),
		"the warning node's url must still hold the codeload tarball URL, got %q", gotURL)
	assert.NotEqual(t, warning.Metadata["uri"], gotURL,
		"uri and url are two different facts and must not collapse into one")
}
