// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// linkerTriggerCaller is an Execute-capable GraphCaller that counts how many
// times the cross-graph linker exercised the wire. runPostCollectLinker now
// delegates DIRECTLY to clientlinker.RunAll (no manage gc.Call self-bounce), and
// RunAll's first act is the per-type graph enumeration over the Execute seam — so
// "the linker ran" is observable as a non-zero Execute (or Call) count. The fake
// returns benign empty carriers so RunAll completes with zero links + no panic.
type linkerTriggerCaller struct {
	calls   atomic.Int64
	execs   atomic.Int64
	execErr error
}

func (c *linkerTriggerCaller) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	c.calls.Add(1)
	return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: `{}`}}}, nil
}

func (c *linkerTriggerCaller) Execute(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	c.execs.Add(1)
	if c.execErr != nil {
		return nil, c.execErr
	}
	// Empty graph-names carrier → RunAll's enumeration finds no foreign graphs →
	// zero links, clean completion.
	return &knowledgev1.ExecuteResponse{GraphNames: nil}, nil
}

func (c *linkerTriggerCaller) wireTouched() bool { return c.calls.Load()+c.execs.Load() > 0 }

// linkerTriggerDeps is the minimal ClientDeps needed for the
// runPostCollectLinker test surface.
type linkerTriggerDeps struct {
	interceptTestDeps
}

func newLinkerTriggerDeps(gc GraphCaller) linkerTriggerDeps {
	return linkerTriggerDeps{interceptTestDeps: interceptTestDeps{gc: gc}}
}

// TestRunPostCollectLinker_GatedByCollectorType asserts that runPostCollectLinker
// runs the client linker (clientlinker.RunAll, observable as wire activity) ONLY
// for cloud/cicd-shaped collector types (aws/gcp/azure/k8s/github/gitlab/
// bitbucket/cicd) and silently skips for the types that never triggered the
// post-collect linker (code/web/pdf/logs/knowledge). The reduction is a DELEGATE:
// no manage(link) self-bounce — RunAll runs in-process over the Execute seam.
func TestRunPostCollectLinker_GatedByCollectorType(t *testing.T) {
	triggerTypes := []string{"aws", "gcp", "azure", "k8s", "github", "gitlab", "bitbucket", "cicd"}
	for _, ct := range triggerTypes {
		t.Run("triggers/"+ct, func(t *testing.T) {
			gc := &linkerTriggerCaller{}
			runPostCollectLinker(context.Background(), newLinkerTriggerDeps(gc), ct)
			assert.True(t, gc.wireTouched(), "collector type %q must run the linker (wire exercised)", ct)
		})
	}

	skipTypes := []string{"code", "web", "pdf", "logs", "knowledge"}
	for _, ct := range skipTypes {
		t.Run("skips/"+ct, func(t *testing.T) {
			gc := &linkerTriggerCaller{}
			runPostCollectLinker(context.Background(), newLinkerTriggerDeps(gc), ct)
			assert.False(t, gc.wireTouched(), "collector type %q must not run the linker", ct)
		})
	}
}

// TestRunPostCollectLinker_NilGraphCaller_DoesNotPanic asserts the degraded-mode
// path: when deps.GraphCaller() returns nil, the helper slog.Warns and returns
// cleanly without touching anything.
func TestRunPostCollectLinker_NilGraphCaller_DoesNotPanic(t *testing.T) {
	runPostCollectLinker(context.Background(), newLinkerTriggerDeps(nil), "aws")
}

// TestRunPostCollectLinker_RunError_BestEffort asserts that an error from the
// in-process linker run does not surface to the caller (the helper is
// fire-and-forget — the collect's user-facing result is unchanged).
func TestRunPostCollectLinker_RunError_BestEffort(t *testing.T) {
	gc := &linkerTriggerCaller{execErr: assert.AnError}
	// Must not panic / must return cleanly despite the linker's Execute failing.
	runPostCollectLinker(context.Background(), newLinkerTriggerDeps(gc), "aws")
	assert.Positive(t, gc.execs.Load(), "the linker attempted the wire before failing")
}
