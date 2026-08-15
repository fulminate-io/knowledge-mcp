// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// TestWantedGraphs_SkipsAbsentCodeRepo is the pipeline half of the rule that a
// client does not work on code graphs whose codebase this machine lacks. What is
// denied is REGISTRATION, which is the resource: an unregistered graph gets no
// collector goroutine pair, no gen-poll entry, no scan and no writeback.
//
// The zero is measured against TWO controls in the same run, and both are needed.
// The present code graph shows the gate discriminates on presence rather than
// excluding the code family outright; the knowledge graph shows it discriminates
// on family rather than dropping everything a repo-presence question has no
// answer for. Against a gate stuck at false, both controls fail.
func TestWantedGraphs_SkipsAbsentCodeRepo(t *testing.T) {
	const (
		presentRepo = "repo-this-machine-has"
		absentRepo  = "repo-this-machine-lacks"
	)

	ctx := context.Background()
	fake := newFakeWireClient()
	// The backend holds both repos: the absent one is absent from this MACHINE,
	// not from the account. That is the case the gate exists for.
	fake.seedGraphNames(kgtypes.GraphCode, presentRepo, absentRepo)
	fake.seedSummaryScan(&knowledgev1.PipelineScanItem{NodeId: "n1", SummarizeText: `{"name":"n1"}`})

	cfg := Config{
		SummaryChannelSize: 4, SummaryBatchSize: 1, SummaryWorkers: 1,
		EmbedChannelSize: 4, EmbedBatchSize: 1, EmbedWorkers: 1,
		Tick: 5 * time.Millisecond,
	}
	p := New(cfg, fake, (&fakeSummarizer{}).call, (&fakeEmbedder{}).call)

	// BOTH code graphs are admitted, so the drop below is presence acting and not
	// an interaction that never happened.
	ws := workingset.New()
	require.True(t, ws.Admit(kgtypes.GraphCode, presentRepo, "search"))
	require.True(t, ws.Admit(kgtypes.GraphCode, absentRepo, "search"))
	require.True(t, ws.Admit(kgtypes.GraphKnowledge, "default", "search"))
	p.AttachWorkingSet(ws)

	// Presence is stated directly: this package cannot read the repo manifest (it
	// lives in tools, which imports this package), which is exactly why the real
	// predicate is injected from bootstrap.
	p.AttachLocalPresence(func(gt kgtypes.GraphType, name string) bool {
		return gt != kgtypes.GraphCode || name == presentRepo
	})

	require.NoError(t, p.Start(ctx))
	t.Cleanup(func() { require.NoError(t, p.Stop(ctx)) })
	p.RefreshOnceForBoot(ctx)

	have := registeredKeys(p)
	require.Contains(t, have, graphKey{GraphType: kgtypes.GraphCode, GraphName: presentRepo},
		"CONTROL: an admitted code graph WITH a local checkout is registered")
	require.Contains(t, have, graphKey{GraphType: kgtypes.GraphKnowledge, GraphName: "default"},
		"CONTROL: a non-code graph is unaffected — it has no checkout to be present or absent")
	assert.NotContains(t, have, graphKey{GraphType: kgtypes.GraphCode, GraphName: absentRepo},
		"an admitted code graph whose repo this machine does not hold gets NO collector: "+
			"registration is the resource, so denying it denies the scan, the gen-poll entry "+
			"and the writeback with it")
}

// TestWantedGraphs_NilPresenceNarrowsNothing pins the deliberate asymmetry
// against the working set: an unwired presence predicate must leave behavior
// exactly as it was. The direction matters because a default-deny here would be
// invisible — every fixture that never wires a predicate would drain nothing,
// and a suite full of green tests would be asserting over an empty pipeline.
func TestWantedGraphs_NilPresenceNarrowsNothing(t *testing.T) {
	ws := workingset.New()
	require.True(t, ws.Admit(kgtypes.GraphCode, "some-repo", "search"))

	p := New(Config{}, newFakeWireClient(), nil, nil)
	p.AttachWorkingSet(ws)
	// No AttachLocalPresence call at all — the unwired case.

	assert.Equal(t,
		[]GraphRef{{GraphType: kgtypes.GraphCode, GraphName: "some-repo"}},
		p.wantedGraphs(),
		"with no presence predicate wired, the wanted set is the admitted set unchanged")
}
