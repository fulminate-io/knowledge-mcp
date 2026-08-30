// SPDX-License-Identifier: Apache-2.0

package collector_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/codesync" // registers the "code" collector
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
	rt := tools.NewCollectRuntime()
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	_, started, _ := rt.Start("code\x00"+repoDir, "code "+repoDir,
		tools.CollectGateGraphName("code", repoDir),
		func() (string, error) {
			<-block // hold the run open so the gate stays up for the assertion
			return "", nil
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
		tools.CollectGateGraphName("code", repoDir)+"@some-branch",
		func() (string, error) {
			<-block
			return "", nil
		})
	require.True(t, startedQualified)
	require.False(t, qualified.CollectInFlightForGraph(kgtypes.GraphCode, collectorGraphName),
		"a branch-qualified recorded identity must NOT match a collector name — "+
			"this is the inert-gate failure the assertion above is guarding against")
}

// newThrowawayRepo builds a minimal git repository with one source file and
// returns its absolute path. Git-initialized because the code collector detects
// the current branch as part of a normal run.
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
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	return dir
}
