// SPDX-License-Identifier: Apache-2.0

// deps_reflection.go — the REFLECTION / THOUGHT-GRAPH consumer seams: the forced
// reflection pass and the derived views the reflect intercepts read from it
// (blind spots, clusters, tensions), plus the topic-similarity forcer. Relocated
// verbatim from deps.go.
//
// They belong together because they share one lifecycle: each is satisfied by the
// same client-side reflection engine, and each exists so a reflect arm can demand a
// fresh computation rather than serve whatever the background pass last left
// behind.

package tools

import (
	"context"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// ReflectionForcer is the narrow seam the manual propagate tool uses to drive an
// on-demand full-corpus reflection backstop pass (thoughts(propagate,
// force_full:true)). *clientthought.PropagationLoop satisfies it (ForceFullPass).
// Declared here as an interface — not the concrete loop type — so the tools layer
// reaches the lever without importing the loop's full surface, and so tests inject
// a fake recording the force call. ForceFullPass claims the per-account reflection
// single-flight guard, bypasses the cadence + quiet-skip + incremental scoping, and
// resets the backstop clock on completion; it returns
// clientthought.ErrReflectionInFlight (a benign coalesce, not a failure) when
// another pass already holds the guard.
type ReflectionForcer interface {
	ForceFullPass(ctx context.Context) (clientthought.PropagationResult, error)
}

// BlindSpotProvider is the narrow READ seam the on-demand query(mode:blind_spots)
// handler serves the loop's cached faceted report through. *clientthought.
// PropagationLoop satisfies it (GetBlindSpots). Declared as an interface — not the
// concrete loop type — so the tools layer reads the cache without importing the
// loop's full surface, and so tests inject a fake returning a constructed report.
// GetBlindSpots is O(1) (a p.mu-guarded field read): the handler serves the report
// the background tick already computed and NEVER recomputes on the call path. A
// zero-value report (Computed=false) is the cold sentinel before the first tick —
// the handler renders a not-yet-computed message rather than a synchronous
// recompute.
type BlindSpotProvider interface {
	GetBlindSpots() clientthought.BlindSpotReport
}

// ClusterProvider is the narrow READ seam the on-demand query(mode:personality)
// and query(mode:summary) handlers serve the loop's cached clusters + personality
// profile through. *clientthought.PropagationLoop satisfies it (GetClustersCached).
// Declared as an interface — not the concrete loop type — so the tools layer reads
// the cache without importing the loop's full surface, and so tests inject a fake
// returning constructed clusters. GetClustersCached is O(1) (a p.mu-guarded field
// read): the handler serves the clusters the background tick already detected and
// NEVER recomputes on the call path. The bool is the cold sentinel (false before
// the first tick) — the handler renders a not-yet-computed message rather than a
// synchronous cluster detect.
type ClusterProvider interface {
	GetClustersCached() ([]clientthought.ThoughtCluster, *clientthought.PersonalityProfile, bool)
}

// TensionsProvider is the narrow READ seam the on-demand query(mode:tensions)
// handler serves the loop's cached tension reports through. *clientthought.
// PropagationLoop satisfies it (GetTensions). Declared as an interface — not the
// concrete loop type — so the tools layer reads the cache without importing the
// loop's full surface, and so tests inject a fake returning constructed reports.
// GetTensions is O(1) (a p.mu-guarded field read): the handler serves the reports
// the background tick already computed and NEVER recomputes on the call path. The
// bool is the cold sentinel (false before the first tick) — the handler renders a
// not-yet-computed message rather than a synchronous tension detect.
type TensionsProvider interface {
	GetTensions() ([]clientthought.TensionReport, bool)
}

// SimilarityForcer is the narrow seam the manual propagate tool uses to drive the
// now-ASYNC topic-similarity lever (thoughts(propagate, similarity:true)).
// *clientthought.PropagationLoop satisfies it. Declared as an interface (mirroring
// ReflectionForcer) so the tools layer reaches the lever without the loop's full
// surface and tests inject a fake.
//
// The lever is async: StartSimilarityPass acquires the SAME per-account reflection
// single-flight guard in the trigger path (coalescing onto an in-flight tick →
// started=false, no second concurrent recompute), then runs the whole topic layer
// (drain → centroids → reconcile → merge cascade → summaries → drift → links) on a
// daemon-lifetime goroutine and invokes onComplete with the report — it does NOT
// return the rendered report to the caller. The event seam persists one status
// record per pass: BeginSimilarityEvent creates the status=running event at trigger
// time and FinishSimilarityEvent REPLACES it at completion (re-supplying the FULL
// metadata map — upsert is a whole-node REPLACE). The read methods back the
// similarity_report fetch op. RunSimilarityPass stays on the interface as the worker
// body StartSimilarityPass calls internally.
type SimilarityForcer interface {
	RunSimilarityPass(ctx context.Context, linkThreshold, mergeThreshold float64, densify clientthought.DensifyParams) (clientthought.SimilarityReport, error)
	StartSimilarityPass(linkThreshold, mergeThreshold float64, densify clientthought.DensifyParams, onStarted func(), onComplete clientthought.SimilarityComplete) (started bool)
	BeginSimilarityEvent(ctx context.Context, link, merge float64) (id string, startedAt time.Time, err error)
	FinishSimilarityEvent(ctx context.Context, id string, startedAt time.Time, link, merge float64, status string, durationMs int64, rendered string, headline map[string]string) error
	LatestSimilarityEvent(ctx context.Context) (*knowledgev1.Node, bool)
	LatestCompletedSimilarityEvent(ctx context.Context) (*knowledgev1.Node, bool)
	SimilarityEventByID(ctx context.Context, id string) (*knowledgev1.Node, bool)
}
