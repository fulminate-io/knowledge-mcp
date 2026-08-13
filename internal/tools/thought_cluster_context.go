// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"

	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// corpusSourceFromDeps returns the resident thought-corpus cache when the
// reflection loop is running in-process, else nil so the on-demand consumer drains.
// It REUSES the existing ClusterProvider seam rather than adding a ClientDeps method
// (which every test fake would have to implement): in production ClusterProvider()
// returns the *clientthought.PropagationLoop, which ALSO implements
// clientthought.CorpusSource (CorpusSnapshot). A test/degraded fake returns a
// non-loop provider (or nil), the type assertion fails, and the consumer drains —
// behavior-equivalent to the pre-cache on-demand path.
func corpusSourceFromDeps(deps ClientDeps) clientthought.CorpusSource {
	if cp := deps.ClusterProvider(); cp != nil {
		if cs, ok := cp.(clientthought.CorpusSource); ok {
			return cs
		}
	}
	return nil
}

// fetchClusterContext reads the loop-persisted cluster state
// (DetectPersistedClusters) + computes personality scalars in one synchronous
// pass. It does NOT recompute clusters live (the loop owns the adjacency+Leiden
// compute) — this keeps the live reflective surface within the tool ceiling.
// Used by the influence + evolution handlers, which need clusters or profile state
// but tolerate an empty/cold result (they render their own "no data" message).
// Returns empty values on failure so the format helpers can still render an
// "empty" report. The cold-state sentinel that personality/summary needed is no
// longer carried here — those modes are served from the propagation-loop cache via
// ClusterProvider (with their own cold message); the two remaining callers never
// consumed the cold flag, so it was dropped.
//
// The third return is the READ MEMO this call built, handed back so the caller's own
// follow-on reads join the SAME pinned snapshot instead of resolving a second
// source. It is returned on EVERY path, including the early-return error paths: a
// caller that got clusters but no profile still has reads worth sharing.
func fetchClusterContext(ctx context.Context, deps ClientDeps) ([]clientthought.ThoughtCluster, *clientthought.PersonalityProfile, clientthought.CorpusSource) {
	gc := deps.GraphCaller()
	if gc == nil {
		return nil, nil, nil
	}
	// ONE memo for the whole call. The three compositions below each resolved the
	// corpus and re-composed the thought->charges map independently, so a single
	// handler read it three times; sharing the memo makes them one read of one
	// pinned snapshot, which is both cheaper and the only thing that guarantees the
	// three stages agree with each other rather than merely tending to.
	src := clientthought.NewReadMemo(corpusSourceFromDeps(deps))
	clusters, err := clientthought.DetectPersistedClusters(ctx, gc, src)
	if err != nil || len(clusters) == 0 {
		return clusters, nil, src
	}
	// Feed the charge→evidence adjacency so the cross-cluster attribution leg
	// participates in trust differentiation (nil here left every scalar at 1.000).
	evidenceAdj := clientthought.BuildEvidenceAdj(ctx, gc, clusters, src)
	profile, err := clientthought.ComputePersonalityScalars(ctx, gc, clusters, evidenceAdj, src)
	if err != nil {
		return clusters, nil, src
	}
	return clusters, &profile, src
}
