// SPDX-License-Identifier: Apache-2.0

// client_segment_dropcache.go — the production wiring for the tools
// SegmentCacheDropper seam: manage(drop_graph)'s local L2 teardown, bridged onto
// the SAME *segmentdist.Manager the client already holds.

package bootstrap

import (
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// SegmentCacheDropper returns the whole-graph L2 teardown seam wrapping the SAME
// *segmentdist.Manager the client holds. Returns an UNTYPED nil interface (not a
// typed-nil adapter) when the segment manager was not constructed (router-less /
// headless client), so the handler's nil-guard fires: a typed nil would satisfy a
// naive != nil check and dereference later. Mirrors SegmentPruner's guard at
// client_segment.go:181.
func (c *client) SegmentCacheDropper() tools.SegmentCacheDropper {
	if c.segmentMgr == nil {
		return nil
	}
	return segmentCacheDropperAdapter{mgr: c.segmentMgr}
}

// segmentCacheDropperAdapter bridges the tools.SegmentCacheDropper seam to
// segmentdist.Manager.DropGraphCache, translating the native report into the
// tools-local mirror. It lives in bootstrap because this is the layer that
// legitimately imports BOTH tools and segmentdist.
type segmentCacheDropperAdapter struct {
	mgr *segmentdist.Manager
}

var _ tools.SegmentCacheDropper = segmentCacheDropperAdapter{}

// DropGraphCache forwards to the Manager and copies the native report back
// field-for-field into the tools-local report.
func (a segmentCacheDropperAdapter) DropGraphCache(
	gt kgtypes.GraphType, name string,
) (tools.DropGraphCacheReport, error) {
	report, err := a.mgr.DropGraphCache(gt, name)
	return tools.DropGraphCacheReport{
		Formats: report.Formats,
		Files:   report.Files,
		Bytes:   report.Bytes,
	}, err
}
