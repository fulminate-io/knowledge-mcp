// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// SegmentManager returns the SAME *segmentdist.Manager the client pipeline
// attached at wirePipelineRuntime — the one per-graph BM25+HNSW segment owner
// the producer ships into and the search intercepts consume.
// Returns an UNTYPED nil interface (not a typed nil *Manager wrapped in the
// interface) when the pipeline was not wired OR during the bind-first wiring
// window (bind-first startup), before wirePipelineRuntime has run. The search arms do NOT
// fall back to a server search (that path is retired); instead they gate on
// PipelineReady() and return a "daemon still starting" not-ready error during
// the window, so a nil here is never dereferenced.
func (c *client) SegmentManager() tools.SegmentSearcher {
	if c.segmentMgr == nil {
		return nil
	}
	return c.segmentMgr
}

// SegmentVectorResolver returns the SAME *segmentdist.Manager as the by-id
// stored-vector read seam the mode:"similar" search claim resolves its query
// vector through (Manager.VectorByID). Returns an UNTYPED nil interface (not a
// typed nil *Manager) when the pipeline was not wired, so the similar-mode claim's
// nil-guard fires correctly and loud-errors instead of a silent empty result.
func (c *client) SegmentVectorResolver() tools.SegmentVectorResolver {
	if c.segmentMgr == nil {
		return nil
	}
	return c.segmentMgr
}

// SegmentCoverage returns the SAME *segmentdist.Manager as the read seam the
// manage(status) segment-coverage column reads segment-covered doc counts through
// (Manager.ShippedSegmentDocCount). Returns an UNTYPED nil interface (not a typed
// nil *Manager) when the pipeline was not wired, so the column's nil-guard fires
// and renders a placeholder instead of dereferencing.
func (c *client) SegmentCoverage() tools.SegmentCoverageReader {
	if c.segmentMgr == nil {
		return nil
	}
	return c.segmentMgr
}

// SegmentShipper returns the SAME *segmentdist.Manager as the build-concurrent/
// ship-once SHIP surface the rebuild_segments driver drives (AddDeterministic /
// AddFields / FlushDeterministic / InvalidateLocal). Returns an UNTYPED nil
// interface (not a typed nil *Manager) when the pipeline was not wired, so the
// driver's nil-guard fires correctly.
func (c *client) SegmentShipper() tools.SegmentShipper {
	if c.segmentMgr == nil {
		return nil
	}
	return c.segmentMgr
}

// PipelineScanner returns the login-routed PipelineScan+Execute wire seam the
// rebuild_segments driver pages the segment_rebuild scan through. It reuses
// routedWireClient (the same per-call cloud-when-logged-in / local-otherwise
// adapter the client pipeline scans through). Returns an UNTYPED nil interface
// when no router is wired (degraded headless mode) so the driver's nil-guard
// fires correctly.
func (c *client) PipelineScanner() tools.PipelineScanner {
	if c.router == nil {
		return nil
	}
	return routedWireClient{router: c.router}
}

// Coverage-heal gate constants. The auto-heal arm no longer triggers only on a
// ZERO-segment pool — it also heals a DEGENERATE-but-nonzero pool (segments
// present, but covering far fewer docs than the graph has embedded). Two
// thresholds keep it from flapping:
//
//   - segmentCoverageFloor: the absolute embedded-count MAGNITUDE below which the
//     ratio probe is NEVER consulted — a small graph (e.g. a handful of embedded
//     nodes) can legitimately sit in one small segment, so the ratio is noisy
//     there. Below the floor only the zero-segments probe heals (never the ratio),
//     so a tiny healthy graph never churns.
//   - coverageRatioThreshold: covered/embedded BELOW this fraction marks the pool
//     degenerate (the live incident was ~6-of-60 shards covering a fraction of the
//     embedded corpus). At/above it the pool is healthy and the arm disarms.
const (
	segmentCoverageFloor   = 64
	coverageRatioThreshold = 0.5
)

// buildHealFactory constructs the auto-heal closure factory the pipeline
// injects into each collector (Pipeline.AttachHealFactory). It is the ONLY layer
// where the pipeline, the segmentdist probe, and the tools rebuild driver are all
// visible — keeping the rebuild body OUT of the pipeline package avoids a
// pipeline→tools import cycle (tools already imports pipeline).
//
// The returned factory yields, per (gt, name), a per-collector heal closure (or
// nil for any graph other than the two builtins that self-heal — code and the
// builtin knowledge graph; the manual rebuild_segments op also serves registered
// custom graphs, but auto-heal stays scoped to code + knowledge by design). The
// closure runs on the armed embed drain edge: a CHEAP presence + coverage probe
// and, when the pool is missing OR degenerate, the rebuild driver (single-flight,
// shared with the manual rebuild_segments op). A healthy graph (segments present
// AND covering enough of the embedded corpus) is a probe + disarm, never a churn.
//
// The probe heals on TWO conditions, not just zero segments:
//  1. zero shipped segments (the never-shipped case), OR
//  2. a degenerate-but-nonzero pool: segment-covered docs (summed HNSW doc_count)
//     below coverageRatioThreshold × the graph's embedded-node count, once the
//     embedded count clears segmentCoverageFloor.
//
// CONSERVATIVE-UNKNOWN guard: a segment whose doc_count is 0 predates the
// doc_count wire plumbing, so its real coverage is UNKNOWN. When ANY shipped HNSW
// segment reports doc_count==0 (ShippedSegmentDocCount's anyUnknown), the ratio
// probe is DISARMED and the arm falls back to the zero-only trigger — without this
// a fleet mid-migration (every shipped meta still 0) would read covered=0 on every
// healthy graph and trigger a fleet-wide rebuild storm. The guard self-retires per
// graph: the first heal/rebuild re-ships segments carrying real doc_count.
func (c *client) buildHealFactory() func(kgtypes.GraphType, string) func(context.Context) error {
	return func(gt kgtypes.GraphType, name string) func(context.Context) error {
		// Builtin-graph gate FIRST — the auto-heal closure is built only for the
		// code graph and the builtin knowledge graph (the manual rebuild_segments
		// op also serves registered custom graphs).
		if gt != kgtypes.GraphCode && gt != kgtypes.GraphKnowledge {
			return nil
		}
		return func(ctx context.Context) error {
			has, err := c.segmentMgr.HasShippedSegments(ctx, gt, name)
			if err != nil {
				return err
			}
			if has {
				degenerate, derr := c.segmentPoolDegenerate(ctx, gt, name)
				if derr != nil {
					return derr
				}
				if !degenerate {
					// Healthy: segments present AND covering enough of the embedded
					// corpus (or the coverage signal is unknown/below-floor — disarm
					// conservatively rather than churn). No rebuild.
					return nil
				}
				// Degenerate-but-nonzero pool: fall through to the rebuild.
			}
			// Zero shipped segments OR a degenerate pool: heal by rebuilding from the
			// already-embedded nodes. Reuse the SAME login-routed scanner seam the
			// manual op uses (the accessor carries the c.router==nil guard) and the
			// SAME segment manager as shipper. RebuildSegments owns the single-flight
			// shared with the manual op.
			scanner := c.PipelineScanner()
			ran, scanned, built, partial, pruned, err := tools.RebuildSegments(ctx, scanner, c.segmentMgr, gt, name)
			if err != nil {
				return err
			}
			slog.Info("bootstrap: auto-heal rebuilt missing or degenerate segments for builtin graph",
				"graph_type", gt, "name", name, "ran", ran, "scanned", scanned, "built", built, "partial", partial, "pruned", len(pruned))
			return nil
		}
	}
}

// segmentPoolDegenerate reports whether a graph's shipped segment pool is present
// but DEGENERATE — covering far fewer docs than the graph has embedded — and so
// should be rebuilt. It is consulted only when HasShippedSegments already found
// segments (the zero case heals unconditionally upstream).
//
// It disarms (returns false) conservatively in three cases so a healthy or
// ambiguous graph never churns: (1) anyUnknown — at least one shipped HNSW segment
// has doc_count==0, so coverage is unknowable (migration-storm guard); (2) embedded
// below segmentCoverageFloor — too small for the ratio to be meaningful; (3)
// covered at/above coverageRatioThreshold × embedded — the pool is healthy.
func (c *client) segmentPoolDegenerate(ctx context.Context, gt kgtypes.GraphType, name string) (bool, error) {
	covered, anyUnknown, err := c.segmentMgr.ShippedSegmentDocCount(ctx, gt, name)
	if err != nil {
		return false, err
	}
	if anyUnknown {
		// Conservative-unknown: an old pre-doc_count segment is present, so the
		// ratio is not trustworthy — disarm and leave it to the zero-only trigger.
		return false, nil
	}
	embedded, err := tools.GraphEmbeddedCount(ctx, c.GraphCaller(), gt, name)
	if err != nil {
		return false, err
	}
	if embedded < segmentCoverageFloor {
		// Small-graph no-flap: too few embedded nodes for the ratio to be meaningful.
		return false, nil
	}
	return float64(covered) < coverageRatioThreshold*float64(embedded), nil
}
