// SPDX-License-Identifier: Apache-2.0

package segmentdist

// manager_reconcile_performat_test.go covers PER-ARM ERROR ISOLATION on the resident
// observation probe.
//
// WHAT THIS FILE USED TO BE, and why almost all of it is gone. It held five
// TestPerFormatVerdict_* tests built on ArmVerdict and a per-format probe that both
// OBSERVED each format arm and DECIDED whether the pool was degenerate. The decision was keyed on
// the shipped doc count, which the cloud rail deletion removed, so the method was
// split: the observation half survives as Manager.ResidentObservationsByFormat, and
// the DECISION half moved out to the caller that holds the embedded denominator.
//
// THE DECISION TESTS DID NOT DIE UNNAMED. Their successor is
// bootstrap/per_format_degeneracy_test.go's TestPerFormatDegeneracyUsesTheEmbedded
// Denominator, which covers the embedded denominator swinging a verdict, HNSW and
// BM25 DISAGREEING, the consumers acting on that split, and an evicted pool yielding
// no verdict. TestPerFormatVerdict_RecoveredAboveFloorBelowRatio has no successor and
// needs none: it was a ratio test, and residentBackstopRatio "went with the shipped
// denominator it scaled" (manager_reconcile_arms.go:26) — there is no proportion left
// to assert. TestPerFormatVerdict_DebugFieldsPerFormat observed the deleted verdict's
// debug line and goes with it.
//
// ONE PROPERTY SURVIVED WITH NO COVERAGE ANYWHERE, which is why this file still
// exists rather than being deleted whole: per-arm error isolation. It is a stated
// contract on ResidentObservationsByFormat — "A top-level error is returned ONLY when
// EVERY arm errored" — and searching the tree found nothing asserting it. A
// documented contract with no test is the shape a later refactor breaks silently.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// observationForFormat picks one arm's observation out of the returned set.
func observationForFormat(t *testing.T, obs []ArmObservation, format string) ArmObservation {
	t.Helper()
	for _, o := range obs {
		if o.Format == format {
			return o
		}
	}
	require.FailNowf(t, "no observation for format", "format %s not among %d observations", format, len(obs))
	return ArmObservation{}
}

// corruptArm plants an unreadable .seg blob in one format arm's L2 root so that
// arm's cache-first load FAILS.
//
// THE FAULT IS INJECTED AT THE REAL SEAM rather than through a double: each format's
// cache is rooted separately by graphCacheDirFor, which is exactly the structural
// fact that makes per-arm isolation both possible and necessary. A corrupt blob in
// one root cannot be seen from the other.
//
// IT MUST BE A CORRUPT BLOB, NOT AN ABSENT ROOT. An empty or missing root reads as
// errL2CacheCold, which the probe treats as a legitimate cold arm rather than a
// failure — so an absent-root fixture would assert nothing about error isolation.
func corruptArm(t *testing.T, cacheDir, name, format string) {
	t.Helper()
	dir := graphCacheDirFor(cacheDir, kgtypes.GraphCode, name, format)
	require.NoError(t, os.MkdirAll(dir, 0o750))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "corrupt-blob.seg"), []byte("not a serialized segment"), 0o600))
}

// TestResidentObservationsIsolatePerArmErrors pins the contract that a failure on one
// format arm does not destroy the other arm's observation.
//
// WHY IT MATTERS STRUCTURALLY: each format's L2 cache is rooted separately, so one
// arm can be cold or damaged in exactly the processes where the other is warm.
// Propagating the broken arm's error would take out the healthy arm's measurement,
// and the consumer — which pairs each arm's resident count against the embedded
// denominator — would lose the good number along with the bad one.
func TestResidentObservationsIsolatePerArmErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	hnswName, bm25Name := hnsw.New().Name(), bm25.New().Name()

	t.Run("one arm errors and the other stays usable", func(t *testing.T) {
		t.Parallel()
		const repo = "isolatedRepo"
		cacheDir := t.TempDir()

		// A genuinely populated HNSW arm.
		producer := closeOnCleanup(t, NewManager(cacheDir, 0))
		require.NoError(t, producer.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, hnswVecDocs(searchCorpusN)))
		require.NoError(t, producer.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, repo))
		require.NotEmpty(t, l2HNSWIDs(cacheDir, repo),
			"fixture: the HNSW arm must hold real content, or 'the other arm stays usable' proves nothing")

		// Break ONLY the BM25 arm.
		corruptArm(t, cacheDir, repo, bm25Name)

		consumer := closeOnCleanup(t, NewManager(cacheDir, 0))
		obs, err := consumer.ResidentObservationsByFormat(ctx, kgtypes.GraphCode, repo)
		require.NoError(t, err,
			"a top-level error is returned ONLY when EVERY arm errored — one broken arm must not surface here")
		require.Len(t, obs, 2, "every arm is observed, including the one that failed")

		broken := observationForFormat(t, obs, bm25Name)
		require.Error(t, broken.Err,
			"the broken arm's failure is RECORDED on its own observation rather than discarded")

		healthy := observationForFormat(t, obs, hnswName)
		require.NoError(t, healthy.Err, "the healthy arm must not inherit the other arm's failure")
		require.Positive(t, healthy.ResidentAfterLoad,
			"and it must still report a real measurement — an isolated error that zeroed the good arm's "+
				"count would be the same data loss with a different shape")
	})

	t.Run("EVERY arm erroring DOES surface a top-level error", func(t *testing.T) {
		t.Parallel()
		// THE OTHER DIRECTION, and it is not optional. Without it the assertion above
		// is equally satisfied by a probe that never returns a top-level error at all —
		// which would report a healthy-looking all-zero observation set for a graph
		// nothing could be measured on.
		const repo = "bothBrokenRepo"
		cacheDir := t.TempDir()
		corruptArm(t, cacheDir, repo, hnswName)
		corruptArm(t, cacheDir, repo, bm25Name)

		consumer := closeOnCleanup(t, NewManager(cacheDir, 0))
		obs, err := consumer.ResidentObservationsByFormat(ctx, kgtypes.GraphCode, repo)
		require.Error(t, err,
			"when NOTHING was measurable the failure must surface rather than reading as a healthy zero")
		require.Len(t, obs, 2, "and the per-arm observations are still returned alongside it")
		for _, o := range obs {
			require.Error(t, o.Err, "arm %s must carry its own error", o.Format)
		}
	})
}
