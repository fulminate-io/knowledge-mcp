// SPDX-License-Identifier: Apache-2.0

package collector_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/codesync"         // registers the "code" collector
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/pdfcollector" // registers the "pdf" collector
	"github.com/fulminate-io/knowledge-mcp/internal/collector/web"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// collect_gate_identity_test.go pins the ONE equality the gap-scan collect-gate
// depends on and that no other test can see: the graph name a collect RECORDS
// must equal the graph name the collector actually EMITS.
//
// WHY THIS TEST HAS TO EXIST. Every other test of the gate supplies both sides of
// that comparison itself — a fake predicate, or a hand-written graph name — so all
// of them stay green under a recorded identity that could never match a real
// collector's name. A gate that never fires is invisible: it has no error, no log,
// and a completely green board. This test is the only thing standing between that
// and production.
//
// THE RULE IT OBEYS, AND WHY THE FILE LIVES HERE. Neither side of the comparison
// may be written by the test. The collector-name side comes from running the REAL
// code collector and reading the CollectResult it hands its sink; the recorded side
// comes from calling the production derivation. The test computes no name of its
// own — no filepath.Base, no literal naming the temp directory — because a test
// that computes the name it expects is comparing production against itself, which
// is precisely the failure it is here to catch.
//
// It is an EXTERNAL test package (collector_test, not collector) because it must
// import tools, and tools imports collector: an in-package test would be an import
// cycle. The capturing sink below is local for the same reason — the in-package
// one is unexported — and rides the exported Sink seam via CollectOptions.Sink.

// gateCaptureSink records the CollectResult the pipeline hands its terminal sink,
// which is where the collector's own graph name is observable without recomputing
// it.
type gateCaptureSink struct {
	results []*collectorwire.CollectResult
}

func (s *gateCaptureSink) WriteResult(_ context.Context, _ string, r *collectorwire.CollectResult) error {
	s.results = append(s.results, r)
	return nil
}

// TestCollectGate_RecordedIdentityMatchesRegisteredCollectorName runs the real code
// collector over a throwaway repository and asserts the collect runtime's in-flight
// query answers TRUE for the graph name that collector produced.
func TestCollectGate_RecordedIdentityMatchesRegisteredCollectorName(t *testing.T) {
	repoDir := newThrowawayRepo(t)

	// SIDE 1 — the collector's own name for this graph, taken from the real run.
	sink := &gateCaptureSink{}
	_, err := collector.Collect(context.Background(), "code", repoDir,
		collector.CollectOptions{Sink: sink})
	require.NoError(t, err)
	require.Len(t, sink.results, 1, "the collect must reach the sink exactly once")
	collectorGraphName := sink.results[0].GraphName
	require.NotEmpty(t, collectorGraphName, "the collector must name the graph it produced")

	// SIDE 2 — the identity a collect records, via the production derivation.
	recordedName, err := tools.CollectGateGraphName("code", repoDir, nil)
	require.NoError(t, err, "the code derivation must not refuse a real repo directory")
	rt := tools.NewCollectRuntime()
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	_, started, _ := rt.Start("code\x00"+repoDir, "code "+repoDir,
		kgtypes.GraphCode, recordedName,
		func() (string, string, error) {
			<-block // hold the run open so the gate stays up for the assertion
			return "", "", nil
		})
	require.True(t, started)

	require.True(t, rt.CollectInFlightForGraph(kgtypes.GraphCode, collectorGraphName),
		"the recorded collect identity does not match the name the collector emits (%q), "+
			"so the gate can never fire against a real collector and is inert in production",
		collectorGraphName)

	// Known-negative control: the assertion above must be capable of being false.
	// Without this, a CollectInFlightForGraph that returned true unconditionally
	// would satisfy it just as well.
	require.False(t, rt.CollectInFlightForGraph(kgtypes.GraphCode, collectorGraphName+"-not-this-graph"),
		"the in-flight query must discriminate between graph names")

	// DIRECTION CHECK — the specific way this could go wrong, demonstrated rather
	// than asserted. Branch-qualifying the recorded identity is the tempting change
	// (the collect knows its branch; overlays exist), and it would be silent: the
	// qualified name matches no registered collector, so the gate simply never fires
	// again. Here a second runtime records exactly that qualified form, and the
	// collector's real name stops matching.
	qualified := tools.NewCollectRuntime()
	_, startedQualified, _ := qualified.Start("code\x00"+repoDir, "code "+repoDir,
		kgtypes.GraphCode, recordedName+"@some-branch",
		func() (string, string, error) {
			<-block
			return "", "", nil
		})
	require.True(t, startedQualified)
	require.False(t, qualified.CollectInFlightForGraph(kgtypes.GraphCode, collectorGraphName),
		"a branch-qualified recorded identity must NOT match a collector name — "+
			"this is the inert-gate failure the assertion above is guarding against")
}

// TestCollectGateGraphName_MatchesTheRawCollectorsOwnNames extends the equality
// above to the two RAW families. It obeys the same rule as the file header: the
// collector's side is read off a real run's CollectResult, and the predicted side
// comes from the production derivation. Neither is written by the test.
//
// The failure it exists to catch is the same one, in a family that has no gate
// yet: a predicted name matching no collector produces no error, no log and a
// green board, because every other test supplies both sides itself.
func TestCollectGateGraphName_MatchesTheRawCollectorsOwnNames(t *testing.T) {
	t.Run("pdf", func(t *testing.T) {
		abs, err := filepath.Abs("../collector/pdf/testdata/t4_paragraph_simple.pdf")
		require.NoError(t, err)

		sink := &gateCaptureSink{}
		_, err = collector.Collect(context.Background(), "pdf", abs,
			collector.CollectOptions{Sink: sink})
		require.NoError(t, err)
		require.Len(t, sink.results, 1, "the collect must reach the sink exactly once")

		predicted, err := tools.CollectGateGraphName("pdf", abs, nil)
		require.NoError(t, err)
		require.Equal(t, sink.results[0].GraphName, predicted,
			"the predicted pdf graph name does not match the name the real collector emits, "+
				"so anything gating on the prediction can never fire")

		// Known-negative: a relative id must REFUSE rather than name a graph. The
		// pdf collector rejects a relative path too, so a name derived from one
		// would name a graph that can never exist.
		_, relErr := tools.CollectGateGraphName("pdf", "testdata/t4_paragraph_simple.pdf", nil)
		require.Error(t, relErr, "a relative pdf id must be refused, not named")
	})

	t.Run("web", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<html><head><title>Gate</title></head><body>" +
				"<h1>Gate</h1><p>One paragraph of prose for the emitter to keep.</p></body></html>"))
		}))
		defer srv.Close()

		// PRODUCTION ORDER: the dispatch names the graph FIRST, from a request
		// carrying no id, and the crawl then runs under that name.
		predicted, err := tools.CollectGateGraphName("web", "", []string{srv.URL})
		require.NoError(t, err)
		require.NotEmpty(t, predicted)

		ctx := web.WithCrawlOptions(context.Background(), web.CrawlOptions{
			Source:   predicted,
			SeedURLs: []string{srv.URL},
			MaxPages: 1,
		}.ApplyDefaults())
		sink := &gateCaptureSink{}
		_, err = collector.Collect(ctx, "web", predicted, collector.CollectOptions{Sink: sink})
		require.NoError(t, err)
		require.Len(t, sink.results, 1, "the collect must reach the sink exactly once")
		require.Equal(t, sink.results[0].GraphName, predicted,
			"the predicted web graph name does not match the name the real collector emits")

		// Known-negative: neither an id nor a seed URL must REFUSE rather than
		// invent a name nobody asked for.
		_, noneErr := tools.CollectGateGraphName("web", "", nil)
		require.Error(t, noneErr, "a web collect with no id and no seed must be refused, not named")
	})

	// SCOPE CONTROL. The families that derive nothing must keep deriving nothing:
	// a switch widened one case too far would start naming graphs for collectors
	// that have none, and no other assertion in this package would notice.
	//
	// gcp, k8s AND github LEFT THIS LIST because each of them now HAS a
	// derivation — each names its graph from the collect id it was handed — so
	// asserting they derive nothing would pin the very hole the collect-gate
	// families change removes. Their coverage did not disappear, it MOVED to the
	// two identity tests in collect_gate_identity_families_test.go, which assert
	// what each one derives rather than that it derives nothing.
	//
	// aws STAYS because its collector discards the collect id entirely and names
	// its graph from the STS caller identity read during the walk, so no name
	// derivable from the request could be right. logs STAYS because it produces no
	// collector graph at all.
	for _, ct := range []string{"aws", "logs"} {
		name, err := tools.CollectGateGraphName(ct, "some-id", []string{"https://example.com/"})
		require.NoError(t, err, "%s must not error", ct)
		require.Empty(t, name, "%s must derive no graph name", ct)
	}
}

// TestNewThrowawayRepo_IgnoresAmbientGitEnv is the known-positive control for the
// clean environment newThrowawayRepo builds below. With the helper inheriting
// os.Environ(), an exported GIT_DIR redirects every git command it runs at the
// AMBIENT repository — git's environment overrides cmd.Dir — so the seed commit
// lands in the caller's checkout instead of the temp directory. This subtest
// exports GIT_DIR at a second throwaway repo and requires the opposite of that:
// the helper's own temp repo gets the commit, and the GIT_DIR repo is untouched.
func TestNewThrowawayRepo_IgnoresAmbientGitEnv(t *testing.T) {
	control := newThrowawayRepo(t)
	controlHead := resolveHead(t, control)
	require.NotEmpty(t, controlHead, "the control repo must have a HEAD to be a control")

	t.Setenv("GIT_DIR", filepath.Join(control, ".git"))

	victim := newThrowawayRepo(t)
	require.NotEmpty(t, resolveHead(t, victim),
		"the helper's commit must land in the helper's own temp repo, not in GIT_DIR's")
	require.Equal(t, controlHead, resolveHead(t, control),
		"the ambient GIT_DIR repository must be untouched by the helper")
}

// resolveHead reads a repository's current HEAD commit straight off disk, with no
// git process. It exists so this control needs no SECOND git-spawning helper: the
// one below is the only test in this package approved to run git.
func resolveHead(t *testing.T, dir string) string {
	t.Helper()
	head, err := os.ReadFile(filepath.Join(dir, ".git", "HEAD"))
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(head))
	ref, isSymbolic := strings.CutPrefix(line, "ref: ")
	if !isSymbolic {
		return line // detached HEAD holds the sha directly
	}
	sha, err := os.ReadFile(filepath.Join(dir, ".git", filepath.FromSlash(ref))) //nolint:gosec // G703: a ref path inside a repository this test built under t.TempDir()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(sha))
}

// hermeticGitEnv returns os.Environ() with every GIT_* entry stripped, then
// re-adds GIT_TERMINAL_PROMPT=0. Test fixtures that spawn git subprocesses MUST
// use this instead of raw os.Environ(): inside a worktree or a git hook, git
// exports GIT_DIR / GIT_INDEX_FILE / GIT_WORK_TREE / etc. into child processes,
// and those override `git -C <dir>` and cmd.Dir — so a fixture's `git init`
// would re-init the host worktree gitdir (flipping core.bare=true) and its
// commits would land on the host branch. Scrubbing GIT_* makes the fixture
// operate only in its own temp dir regardless of the ambient env. Intentionally
// duplicated from the coderun package: the no-shared-packages-outside-gen-proto
// invariant (AGENTS.md) forbids a hand-written shared test-helper package
// between these internal packages.
func hermeticGitEnv() []string {
	base := os.Environ()
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if strings.HasPrefix(kv, "GIT_") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GIT_TERMINAL_PROMPT=0")
}

// newThrowawayRepo builds a minimal git repository with one source file and
// returns its absolute path.
//
// REASON THIS TEST SPAWNS GIT (approved site, see the git-in-tests
// allowlist under scripts/testdata/): its subject is the
// code collector's own git-repo discovery and branch detection, which shells out
// to git — a fake repository directory would exercise the filesystem-walk
// fallback instead of the path a real collect takes, so the gate this file pins
// would be proven over the wrong code path. The repository is a t.TempDir, every
// command runs under hermeticGitEnv, and the committer identity is passed
// per-command with -c so nothing is ever written into any repository's config.
func newThrowawayRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package main\n\nfunc main() {}\n"), 0o600))
	for _, args := range [][]string{
		{"init"},
		{"add", "."},
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-m", "seed"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = hermeticGitEnv()
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	return dir
}
