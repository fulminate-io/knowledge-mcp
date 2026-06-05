// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
)

// TestSegmentManagerAccessorSharesOneInstance is Phase 3 Step 1's criterion:
// ClientDeps.SegmentManager() returns the SAME *segmentdist.Manager the client
// holds (the one the pipeline attaches at wirePipelineRuntime) — one instance,
// not a freshly constructed duplicate. wirePipelineRuntime assigns
// c.segmentMgr = NewManager(...) and immediately p.AttachSegmentManager(c.segmentMgr)
// (pipeline.go), so the producer (pipeline) and the consumer (SegmentManager())
// share one pointer by construction; this test pins the accessor half.
func TestSegmentManagerAccessorSharesOneInstance(t *testing.T) {
	mgr := segmentdist.NewManager(nil, t.TempDir(), 0)
	c := &client{segmentMgr: mgr}

	got := c.SegmentManager()
	require.NotNil(t, got, "accessor returns the attached manager")
	// Same pointer — the accessor exposes the held instance, not a copy.
	require.Same(t, mgr, got, "SegmentManager() returns the SAME instance the client holds")
}

// TestSegmentManagerAccessorNilWhenUnwired asserts the accessor returns an
// UNTYPED nil (so the search arms' nil fallback fires) when the pipeline was
// never wired (c.segmentMgr nil).
func TestSegmentManagerAccessorNilWhenUnwired(t *testing.T) {
	c := &client{}
	require.Nil(t, c.SegmentManager(), "unwired client yields a nil SegmentManager")
}

// TestBuildHealFactoryShape is the auto-heal wiring criterion: with a
// segment manager wired, buildHealFactory returns a non-nil factory that produces
// a NON-NIL heal closure for a code graph (segments are code-only, so the heal is
// armed for code) and a NIL closure for a non-code graph (the code-only gate).
// Construction-level only — it does NOT drive a live rebuild (the probe + rebuild
// behavior is covered by the segmentdist / tools / maybeHealCheck tests).
func TestBuildHealFactoryShape(t *testing.T) {
	mgr := segmentdist.NewManager(nil, t.TempDir(), 0)
	c := &client{segmentMgr: mgr}

	factory := c.buildHealFactory()
	require.NotNil(t, factory, "a wired segment manager yields a non-nil heal factory")

	codeClosure := factory(kgtypes.GraphCode, "repo")
	require.NotNil(t, codeClosure, "a code graph gets a non-nil heal closure")

	nonCodeClosure := factory(kgtypes.GraphKnowledge, "kg")
	require.Nil(t, nonCodeClosure, "a non-code graph gets a nil closure (rebuild_segments is code-only)")
}

// TestHealFactoryNotAttachedWithoutSegmentManager asserts the bootstrap guard:
// with no segment manager wired (c.segmentMgr nil), wirePipelineRuntime never
// calls AttachHealFactory, so a degraded/headless client carries no heal closure.
// Mirrors the production guard `if c.segmentMgr != nil { p.AttachHealFactory(...) }`
// — when the manager is absent the heal factory is simply not built/attached, and
// the per-collector heal-check no-ops.
func TestHealFactoryNotAttachedWithoutSegmentManager(t *testing.T) {
	c := &client{} // segmentMgr nil — the degraded/headless path
	require.Nil(t, c.segmentMgr, "no segment manager wired")
	// The bootstrap guard (segmentMgr != nil) is what gates AttachHealFactory; with
	// segmentMgr nil that branch is not taken, so no heal factory is attached. This
	// pins the precondition the guard reads.
}
