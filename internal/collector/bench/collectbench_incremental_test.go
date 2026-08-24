// SPDX-License-Identifier: Apache-2.0

//go:build collectbench

package bench

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collectbench_incremental_test.go — RUNs 2 and 3: the fixed point, the write
// bound and the read band.
//
// THESE TESTS ASSERT; THEY DO NOT COLLECT. The conductor drives each collect
// through TestCollectBench_Run and samples psql AROUND it, because the
// server-side numbers can only be read over psql and this module must not grow a
// Postgres driver. Splitting measurement from assertion is also what lets the
// conductor sample BETWEEN two consecutive collects, which one long test could
// not allow.

// The per-run record names the conductor writes. Named here rather than at each
// use so a conductor rename fails in ONE place instead of drifting silently.
const (
	run2aFacts, run2aSample = "run2a.json", "run2a.psql.json"
	run2bFacts, run2bSample = "run2b.json", "run2b.psql.json"
	run3Facts, run3Sample   = "run3.json", "run3.psql.json"
	run3bFacts, run3bSample = "run3b.json", "run3b.psql.json"

	mf2a, mf2b = "mf2a.json", "mf2b.json"
	mf3, mf3b  = "mf3.json", "mf3b.json"
)

// quiescentPairs is the (facts, sample) pair for every run required to be a
// fixed point: the two consecutive K=0 collects, and the K=0 run that must
// RE-ENTER the fixed point after RUN 3's mutation.
var quiescentPairs = []struct {
	label   string
	facts   string
	sample  string
	rejoins bool // true for the run that must re-enter after the mutation
}{
	{"RUN 2a (K=0)", run2aFacts, run2aSample, false},
	{"RUN 2b (K=0)", run2bFacts, run2bSample, false},
	{"RUN 3b (K=0, return to quiescence)", run3bFacts, run3bSample, true},
}

// TestCollectBench_QuiescentAcrossRepeatedCollects asserts the collect is a
// FIXED POINT: repeated collects of an unchanged tree change nothing.
//
// THE SCOPE IS FILE-OWNED ROWS, AND THE REASON IS NOT THE HISTORICAL ONE. File-
// owned rows are the manifest population — exactly the set the diff decides
// about — so a quiescence test FOR THE DIFF asserting over rows the diff never
// reasons about would be a broader claim than its own subject. The fileless half
// has its own gate in the work whose enforcement makes it true
// (TestCollectBench_FilelessResidueIsZero, on this same conductor); widening this
// scope would put one property in two places that must agree, with nothing
// saying which is authoritative when they disagree.
//
// THREE CLAUSES, AND CLAUSE (c) IS TWO ASSERTIONS BECAUSE THE THREE DIVERGENCE
// CLASSES SPLIT ACROSS TWO WIRE SURFACES:
//
//	(a) zero xmin churn on file-owned rows, over a NON-EMPTY population;
//	(b) byte-identical CollectManifest responses run over run;
//	(c) the armed-path divergence equivalent —
//	      · ZERO file-owned files in the upload set, which covers the
//	        hash_mismatch and discovered_only classes (both enter changedFiles
//	        and therefore surface as an UPLOAD); and
//	      · an EMPTY deletion set on the FinalizeRequest, which covers the
//	        manifest_only class (it enters the deletion set, never the upload).
//
// An upload-only form of (c) is the obvious way to write this and it would cover
// TWO OF THREE, leaving uncovered the one class whose failure is DESTRUCTIVE: a
// spurious manifest_only at K=0 means the diff asked the server to delete files
// that exist.
func TestCollectBench_QuiescentAcrossRepeatedCollects(t *testing.T) {
	for _, p := range quiescentPairs {
		facts, sample := readFacts(t, p.facts), readSample(t, p.sample)

		// (a) — with its known-positive control first. A zero churn over an
		// EMPTY population is not quiescence, it is an empty graph, and the two
		// are indistinguishable without this.
		require.Positive(t, sample.FileOwnedRows,
			"%s: the file-owned population is EMPTY, so its zero churn proves nothing — "+
				"the graph never landed, or the sampler matched no rows", p.label)
		assert.Zero(t, sample.FileOwnedXminChurn,
			"%s: %d file-owned rows took a new row version on an unchanged tree, out of %d — "+
				"a collect of an unchanged tree must write no file-owned row at all",
			p.label, sample.FileOwnedXminChurn, sample.FileOwnedRows)

		// (c) first half — hash_mismatch and discovered_only.
		assert.Zero(t, facts.UploadedFileOwnedFiles,
			"%s: the upload set carried %d file-owned files on an unchanged tree — "+
				"the diff classified unchanged files as changed (hash_mismatch or discovered_only)",
			p.label, facts.UploadedFileOwnedFiles)

		// (c) second half — manifest_only, the DESTRUCTIVE class. The finalize
		// count is what stops an empty deletion set read off a run that never
		// finalized from passing as a genuinely empty one.
		require.Equal(t, 1, facts.Finalizes,
			"%s: expected exactly one Finalize, saw %d — an empty deletion set read off a run "+
				"that never finalized is not evidence of anything", p.label, facts.Finalizes)
		assert.Zero(t, facts.DeletedFilesOnFinalize,
			"%s: the FinalizeRequest carried %d deletions on an unchanged tree — "+
				"the diff asked the server to delete files that are present (manifest_only)",
			p.label, facts.DeletedFilesOnFinalize)
	}

	// (b) — byte-identical manifests run over run. The pairs are the two
	// consecutive quiescent runs, and then the mutation's own before/after pair:
	// RUN 3 CHANGED THE TREE, so its manifest legitimately differs from RUN 2's,
	// and comparing across the mutation would be red against correct work. What
	// must hold is that each side of the mutation is stable against ITS OWN
	// successor.
	assertManifestIdentical(t, mf2a, mf2b, "the two consecutive K=0 collects")
	assertManifestIdentical(t, mf3, mf3b, "the K=25 collect and the K=0 run that follows it")
}

// assertManifestIdentical compares two captured manifest digests.
func assertManifestIdentical(t *testing.T, a, b, what string) {
	t.Helper()
	ma, mb := readManifestCapture(t, a), readManifestCapture(t, b)
	require.Positive(t, ma.Entries,
		"%s: the manifest captured in %s is EMPTY, so an equality against it proves nothing", what, a)
	assert.Equal(t, ma.Entries, mb.Entries,
		"%s: the manifest entry count moved (%d -> %d) across collects of an unchanged tree",
		what, ma.Entries, mb.Entries)
	assert.Equal(t, ma.Digest, mb.Digest,
		"%s: the CollectManifest response is not byte-identical across collects of an unchanged tree", what)
}

// TestCollectBench_MutatedKScalesWithK asserts the WRITE side collapses to O(K).
//
// THE BOUND, PINNED:
//
//	write_rows(K=25) <= floor(K=0) + 3x (mutated files' actual node+edge count)
//
// THE DENOMINATOR IS THE MUTATED FILES' ACTUAL NODE+EDGE COUNT, not their share
// of the file count: the harness partitions the collected result by owning file,
// so it knows the real figure and does not have to approximate it from 25/N.
//
// floor(K=0) IS MEASURED BY RUN 2, NEVER PREDICTED, and under the rail-neutral
// skip clause it is expected AT OR NEAR ZERO — which TIGHTENS this bound rather
// than loosening it, because the 3x slack now carries it alone. If this test goes
// red, READ THE FLOOR FIRST: a floor at or near zero is the CONFIRMING signal
// that enforcement works, not the cause of an overshoot, and widening the 3x to
// absorb a correctly-behaving floor would undo the evidence.
//
// THE 3x IS PINNED SLACK for chunk boundaries and whole-file edge-set
// replacement — exactly the term that survives rail-neutrality.
func TestCollectBench_MutatedKScalesWithK(t *testing.T) {
	const slack = 3

	floorSample := readSample(t, run2bSample)
	mutatedFacts, mutatedSample := readFacts(t, run3Facts), readSample(t, run3Sample)

	require.Positive(t, mutatedFacts.MutatedFiles,
		"RUN 3 recorded no mutated files, so there is no K to scale with — the mutation never ran")
	share := int64(mutatedFacts.MutatedNodes + mutatedFacts.MutatedEdges)
	require.Positive(t, share,
		"the %d mutated files own no nodes or edges, so the bound's denominator is zero — "+
			"the mutation edited files the collector does not chunk", mutatedFacts.MutatedFiles)

	bound := floorSample.WriteRows + slack*share
	t.Logf("K-scaling: floor(K=0)=%d write rows, mutated share=%d (nodes %d + edges %d over %d files), "+
		"bound 3x => %d, measured write_rows(K=25)=%d",
		floorSample.WriteRows, share, mutatedFacts.MutatedNodes, mutatedFacts.MutatedEdges,
		mutatedFacts.MutatedFiles, bound, mutatedSample.WriteRows)

	// THE KNOWN-POSITIVE: a K=25 run that wrote NOTHING would satisfy the bound
	// while proving the payoff never happened — the mutation would not have
	// landed at all.
	require.Positive(t, mutatedSample.WriteRows,
		"the K=25 run wrote ZERO rows, so the bound is satisfied vacuously — the mutation never landed")
	assert.LessOrEqual(t, mutatedSample.WriteRows, bound,
		"write_rows(K=25)=%d exceeds floor(K=0)=%d + 3x(mutated share=%d) = %d. "+
			"READ THE FLOOR FIRST: at or near zero it confirms enforcement works and is not the cause. "+
			"A K=25 write profile resembling a full collect means a payoff conversion regressed — "+
			"carry-forward, the collector edge-set replace, or per-node writes back to O(uploaded)",
		mutatedSample.WriteRows, floorSample.WriteRows, share, bound)
}

// TestCollectBench_ReadSideIsKInvariant asserts the READ side does NOT scale
// with K.
//
// PINNED BAND: 0.8 <= read_side_buffers(K=25) / read_side_buffers(K=0) <= 1.5.
//
// Reads are O(repo) + O(K) with the manifest render's floor DOMINANT, so the
// ratio should sit near 1. THE UPPER BOUND catches reads growing with K. THE
// LOWER BOUND IS NOT SYMMETRY: a large DROP would mean the render stopped
// rendering, and a manifest returning few entries collapses reads AND makes the
// diff wrongly believe everything changed — a failure that would otherwise look
// like an improvement.
//
// THE RATIO IS READ-SIDE ONLY, NEVER BLENDED WITH THE WRITE SIDE. One number
// mixing a term designed to stay constant with a term required to collapse
// false-passes a regressed write path hiding behind a dominant read floor, and
// false-fails a correct O(K) write path under a legitimately constant floor.
func TestCollectBench_ReadSideIsKInvariant(t *testing.T) {
	const lo, hi = 0.8, 1.5

	base := readSample(t, run2bSample)
	mutated := readSample(t, run3Sample)

	require.Positive(t, base.ReadSideBuffers,
		"the K=0 run read ZERO buffers, so the ratio has no denominator — the census captured nothing")
	ratio := float64(mutated.ReadSideBuffers) / float64(base.ReadSideBuffers)
	t.Logf("read side: K=0 buffers=%d, K=25 buffers=%d, ratio=%.3f, band %.1f..%.1f",
		base.ReadSideBuffers, mutated.ReadSideBuffers, ratio, lo, hi)

	assert.GreaterOrEqual(t, ratio, lo,
		"read_side_buffers collapsed from %d to %d (ratio %.3f, floor %.1f) — the manifest render "+
			"stopped rendering; a manifest returning few entries collapses reads AND makes the diff "+
			"wrongly believe everything changed", base.ReadSideBuffers, mutated.ReadSideBuffers, ratio, lo)
	assert.LessOrEqual(t, ratio, hi,
		"read_side_buffers grew from %d to %d (ratio %.3f, ceiling %.1f) — reads are scaling with K, "+
			"but the render is meant to be an O(repo) floor that K does not move",
		base.ReadSideBuffers, mutated.ReadSideBuffers, ratio, hi)
}
