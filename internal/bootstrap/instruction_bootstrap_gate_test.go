// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// gatedBootstrapClient returns a *client holding a real, EMPTY working set and
// the fake graph caller the deferred instruction bootstrap will use. The fake
// records every Execute it receives — including the idempotency pre-flight, which
// on an already-seeded graph is the ONLY call the bootstrap makes — so an empty
// call slice means no read was issued rather than that nothing was wired.
func gatedBootstrapClient(t *testing.T) (*client, *fakeBootstrapGC, string) {
	t.Helper()
	dir := t.TempDir()
	makeBootstrapDirs(t, dir, 1, 1)
	return &client{workingSet: workingset.New()}, &fakeBootstrapGC{queryNodeCount: 0}, dir
}

// waitForCalls polls until the fake has recorded at least n calls or the deadline
// passes, returning what it saw. The deferred bootstrap runs on its own
// goroutine, so a bare read right after the admission is racy.
func waitForCalls(fc *fakeBootstrapGC, n int) []bootstrapCall {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(fc.recorded()) >= n {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fc.recorded()
}

// TestBackgroundSurfaces_SkipWhenKnowledgeNotAdmitted is the bootstrap-package
// half of the pair. The propagation surfaces live in package thought and cannot
// be reached from here, so the same assertion is made once in each package; this
// copy covers the instruction bootstrap.
func TestBackgroundSurfaces_SkipWhenKnowledgeNotAdmitted(t *testing.T) {
	t.Parallel()

	c, fc, dir := gatedBootstrapClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go c.deferInstructionBootstrapUntilAdmitted(ctx, fc, dir)
	// Long enough that a bootstrap which ran at boot would have landed its calls.
	time.Sleep(100 * time.Millisecond)

	assert.Equal(t, []bootstrapCall(nil), fc.recorded(),
		"an unadmitted knowledge graph must receive ZERO requests: the instruction "+
			"bootstrap is a query plus a create_batch, and neither may run at boot")
}

// TestBackgroundSurfaces_RunAfterKnowledgeAdmitted is the known-positive control
// for the zero above, in the same package and over the same fixture.
func TestBackgroundSurfaces_RunAfterKnowledgeAdmitted(t *testing.T) {
	t.Parallel()

	c, fc, dir := gatedBootstrapClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go c.deferInstructionBootstrapUntilAdmitted(ctx, fc, dir)
	require.Empty(t, fc.recorded(), "precondition: nothing runs before admission")

	c.AdmitGraph(kgtypes.GraphKnowledge, "", "query")

	calls := waitForCalls(fc, 2)
	require.Len(t, calls, 2,
		"once admitted the bootstrap must run: the idempotency pre-flight then the seed")
	assert.Equal(t, "query", calls[0].tool)
	assert.Equal(t, "mutate", calls[1].tool)
}

// TestInstructionBootstrap_WaitsForAdmissionThenSeeds pins the one-shot: the
// deferred bootstrap fires on the WAKE rather than at boot, ignores a wake for a
// graph that is not the knowledge graph, and seeds exactly once.
func TestInstructionBootstrap_WaitsForAdmissionThenSeeds(t *testing.T) {
	t.Parallel()

	c, fc, dir := gatedBootstrapClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go c.deferInstructionBootstrapUntilAdmitted(ctx, fc, dir)

	// A wake for a DIFFERENT graph must not fire it: the bootstrap targets
	// knowledge/default and nothing else.
	c.AdmitGraph(kgtypes.GraphCode, "repoA", "collect")
	time.Sleep(100 * time.Millisecond)
	require.Empty(t, fc.recorded(), "a code-graph admission must not fire the knowledge bootstrap")

	c.AdmitGraph(kgtypes.GraphKnowledge, "default", "search")
	calls := waitForCalls(fc, 2)
	require.Len(t, calls, 2, "the knowledge admission must fire the bootstrap")

	// One-shot: a further admission cannot re-run it, because the goroutine has
	// returned. A repeat admit is a no-op on the set, so drive a fresh graph too.
	c.AdmitGraph(kgtypes.GraphKnowledge, "default", "search")
	c.AdmitGraph(kgtypes.GraphCloud, "acct-1", "collect")
	time.Sleep(100 * time.Millisecond)
	assert.Len(t, fc.recorded(), 2, "the bootstrap must seed exactly once per process")
}
