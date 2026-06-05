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
// interface) when the pipeline was not wired, so the search arms' `mgr == nil`
// fallback to the server search path fires correctly.
func (c *client) SegmentManager() tools.SegmentSearcher {
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

// buildHealFactory constructs the auto-heal closure factory the pipeline
// injects into each collector (Pipeline.AttachHealFactory). It is the ONLY layer
// where the pipeline, the segmentdist probe, and the tools rebuild driver are all
// visible — keeping the rebuild body OUT of the pipeline package avoids a
// pipeline→tools import cycle (tools already imports pipeline).
//
// The returned factory yields, per (gt, name), a per-collector heal closure (or
// nil for a non-code graph — rebuild_segments is code-only). The closure runs on
// the armed embed drain edge: a CHEAP zero-shipped-segments probe and, ONLY on
// zero, the rebuild driver (single-flight, shared with the manual
// rebuild_segments op). A healthy graph (segments present) is a probe + disarm,
// never a churn.
func (c *client) buildHealFactory() func(kgtypes.GraphType, string) func(context.Context) error {
	return func(gt kgtypes.GraphType, name string) func(context.Context) error {
		// Code-only gate FIRST — segments (and rebuild_segments) are code-graph only.
		if gt != kgtypes.GraphCode {
			return nil
		}
		return func(ctx context.Context) error {
			has, err := c.segmentMgr.HasShippedSegments(ctx, gt, name)
			if err != nil {
				return err
			}
			if has {
				// Healthy: the cheap probe found segments — disarm, no rebuild.
				return nil
			}
			// Zero shipped segments: heal by rebuilding from the already-embedded
			// nodes. Reuse the SAME login-routed scanner seam the manual op uses (the
			// accessor carries the c.router==nil guard) and the SAME segment manager
			// as shipper. RebuildSegments owns the single-flight shared with the
			// manual op.
			scanner := c.PipelineScanner()
			ran, scanned, built, pruned, err := tools.RebuildSegments(ctx, scanner, c.segmentMgr, name)
			if err != nil {
				return err
			}
			slog.Info("bootstrap: auto-heal rebuilt missing segments for code graph",
				"name", name, "ran", ran, "scanned", scanned, "built", built, "pruned", len(pruned))
			return nil
		}
	}
}
