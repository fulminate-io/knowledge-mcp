// SPDX-License-Identifier: Apache-2.0

//go:build collectbench

package bench

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/remote"
)

// collectbench_enforcement_test.go — RUN 5, the adversarial full blast.
//
// EVERY OTHER RUN IN THIS BENCH MEASURES A COOPERATIVE CLIENT: one that computes
// a diff and uploads only what changed. This one measures the opposite, because
// the server's guarantee is not "a good client is cheap" but "no client can make
// the server rewrite the repository". The upload is a full-tree blast of an
// UNCHANGED tree against a graph that already holds it, and the assertion is on
// what the server LANDED, not on what the client sent.

// The per-run record names the conductor writes for this phase. Named here
// rather than at each use so a conductor rename fails in ONE place.
const (
	run5SeedFacts, run5SeedSample = "run5seed.json", "run5seed.psql.json"
	run5Facts, run5Sample         = "run5.json", "run5.psql.json"
)

// TestCollectBench_FullBlastRun is the DRIVER the conductor invokes for RUN 5,
// deliberately NOT one of the locked assertion names: it measures, and the two
// tests below compare.
//
// It reuses the tag-gated non-diff constructor the convergence arm uses. That is
// the adversarial client exactly: no CollectManifest fetch, no diff computed,
// every file uploaded on every collect — the pre-incremental client's behavior.
func TestCollectBench_FullBlastRun(t *testing.T) {
	facts := runOneCollect(t, benchTreeRoot(t), benchGraphFor(), 0,
		func(c knowledgev1connect.IngestServiceClient) collector.Sink {
			return remote.NewUploadSinkForBenchFullPath(
				func(context.Context) (knowledgev1connect.IngestServiceClient, error) { return c, nil })
		})
	writeFacts(t, facts)
}

// TestCollectBench_FullBlastAgainstExistingGraphIsODiff asserts that a full
// upload of an unchanged tree against a populated graph lands O(ACTUAL CHANGE)
// rows, not O(repo), on BOTH rails.
//
// THE GAP BETWEEN UPLOADED AND LANDED IS THE MEASUREMENT. A run where both are
// small proves nothing — it could be a diff collect mislabelled — so the uploaded
// side is checked to be at FULL-TREE scale first, by requiring that nothing was
// narrowed and that no manifest was ever fetched. Only then does a zero on the
// landed side mean what it says.
//
// THE CHANGE SIZE HERE IS ZERO, because the tree the blast uploads is byte-for-
// byte the tree the seed landed. So "collapses to the change size" is "writes no
// row at all", and that is what is asserted. A materially non-zero reading is a
// finding about the skip clause rather than a bound to widen.
//
// THE RAILS ARE ASSERTED SEPARATELY, never as a total: a run where nodes collapse
// but edges do not is precisely the pre-enforcement state, and a summed assertion
// would report it as bounded.
func TestCollectBench_FullBlastAgainstExistingGraphIsODiff(t *testing.T) {
	seedFacts, seedSample := readFacts(t, run5SeedFacts), readSample(t, run5SeedSample)
	facts, sample := readFacts(t, run5Facts), readSample(t, run5Sample)

	t.Logf("full blast: uploaded nodes %d/%d edges %d/%d, manifest RPCs %d; "+
		"landed totals nodes %d edges %d; WROTE nodes %d edges %d "+
		"(seed control wrote nodes %d edges %d)",
		facts.NodesUploaded, facts.TotalNodes, facts.EdgesUploaded, facts.TotalEdges,
		facts.ManifestRPCs, sample.NodeRows, sample.EdgeRows,
		sample.NodeWriteRows, sample.EdgeWriteRows,
		seedSample.NodeWriteRows, seedSample.EdgeWriteRows)

	// THE UPLOAD REALLY WAS A FULL BLAST — nothing narrowed it. Read off the
	// client's own result: the diff narrows result.Nodes/Edges IN PLACE, so an
	// uploaded count equal to the pre-upload total is proof that no narrowing
	// happened, and a zero manifest RPC count is proof the diff was never even
	// consulted.
	require.Equal(t, facts.TotalNodes, facts.NodesUploaded,
		"the blast uploaded %d of %d nodes — something narrowed the upload, so this is not the "+
			"adversarial case", facts.NodesUploaded, facts.TotalNodes)
	require.Equal(t, facts.TotalEdges, facts.EdgesUploaded,
		"the blast uploaded %d of %d edges — something narrowed the upload, so this is not the "+
			"adversarial case", facts.EdgesUploaded, facts.TotalEdges)
	require.Zero(t, facts.ManifestRPCs,
		"the blast issued %d CollectManifest RPCs — it computed a diff, so a small landed count "+
			"below would be the diff working rather than the server's enforcement", facts.ManifestRPCs)
	require.Equal(t, 1, facts.Finalizes,
		"the blast issued %d Finalize calls, expected 1 — rows read off a run that never finalized "+
			"are not evidence of anything", facts.Finalizes)

	// THE GRAPH REALLY WAS POPULATED before the blast, so "wrote nothing" is a
	// skip rather than an empty target.
	require.Positive(t, sample.NodeRows, "the graph holds no node rows — the seed collect never landed")
	require.Positive(t, sample.EdgeRows, "the graph holds no edge rows — the seed collect never landed")

	// THE KNOWN-POSITIVE CONTROL FOR A PAIR OF ZEROES. The seed ran through the
	// SAME xmin instrument and wrote its whole tree; without this, an instrument
	// that never fires and a server that never wrote are indistinguishable.
	require.Positive(t, seedSample.NodeWriteRows,
		"the seed collect recorded ZERO node writes, so the instrument never fired and the zero "+
			"asserted below would prove nothing")
	require.Positive(t, seedSample.EdgeWriteRows,
		"the seed collect recorded ZERO edge writes, so the instrument never fired and the zero "+
			"asserted below would prove nothing")
	require.Positive(t, seedFacts.NodesUploaded, "the seed uploaded nothing — there is no populated graph to blast")

	assert.Zero(t, sample.NodeWriteRows,
		"the full blast of an UNCHANGED tree took %d new node row versions against a graph that "+
			"already held it (%d node rows). Actual change is zero, so server-side writes must be "+
			"zero: the skip clause did not engage on the node rail",
		sample.NodeWriteRows, sample.NodeRows)
	assert.Zero(t, sample.EdgeWriteRows,
		"the full blast of an UNCHANGED tree took %d new edge row versions against a graph that "+
			"already held it (%d edge rows). Actual change is zero, so server-side writes must be "+
			"zero: the skip clause did not engage on the edge rail — the rail carrying the majority "+
			"of the write volume",
		sample.EdgeWriteRows, sample.EdgeRows)
}

// TestCollectBench_FilelessResidueIsZero asserts the fileless population — the
// language hub nodes, the package nodes and the repo root, which carry no file
// path and so were never covered by the client's per-file diff — re-lands
// NOTHING on a collect of an unchanged tree.
//
// THE COUNT IS RE-DERIVED, NEVER PINNED. An earlier bench ledger recorded roughly
// 441 such nodes, but that is a tree-derived observation and the corpus moves, so
// the test measures the population itself and asserts this run landed none of it.
//
// IT IS ASSERTED ON THE FULL-BLAST RUN ON PURPOSE. The fileless set uploads on
// EVERY collect by design, so the diff never protects it; only the server-side
// skip clause can. The blast is the run where that is most exposed.
func TestCollectBench_FilelessResidueIsZero(t *testing.T) {
	seed := readSample(t, run5SeedSample)
	sample := readSample(t, run5Sample)

	t.Logf("fileless: population %d rows, re-landed %d on the full blast "+
		"(seed control landed %d of %d)",
		sample.FilelessRows, sample.FilelessRelands, seed.FilelessRelands, seed.FilelessRows)

	require.Positive(t, sample.FilelessRows,
		"the fileless population is EMPTY, so a zero residue over it proves nothing — "+
			"the graph never landed, or the sampler's class predicate matched no rows")
	require.Positive(t, seed.FilelessRelands,
		"the seed collect landed ZERO fileless rows, so the instrument never fired and the zero "+
			"asserted below would be indistinguishable from a sampler that cannot see this class")

	assert.Zero(t, sample.FilelessRelands,
		"%d of the %d fileless node rows took a new row version on a full-blast upload of an "+
			"unchanged tree. The fileless set ships on every collect by design, so the client diff "+
			"cannot protect it — a non-zero here means the server-side skip clause is not covering "+
			"the rows the per-file diff was never able to",
		sample.FilelessRelands, sample.FilelessRows)
}
