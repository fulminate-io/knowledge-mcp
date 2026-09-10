// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// linkerTriggerCaller is an Execute-capable GraphCaller that counts how many
// times the Dockerfile pass exercised the wire. runPostCollectLinker delegates
// DIRECTLY to clientlinker.LinkDockerfilesInGraph, whose first act is a node read
// over the Execute seam — so "the pass ran" is observable as a non-zero Execute
// (or Call) count. The fake returns benign empty carriers so the pass completes
// with zero links and no panic.
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
// runs the Dockerfile pass (observable as wire activity) for the CODE collector
// type alone, and silently skips for every other type.
//
// THE TRIGGER MOVED, AND THAT IS WHAT THIS TEST NOW PINS. It used to be the
// cloud provider names, then the CI/CD ones — an allowlist inherited from a
// server-side gate rather than derived from what the pass reads. The one
// surviving pass reads CODE graphs on both sides, so a cicd collect fired it over
// data it had not touched while the collect that DID change that data fired
// nothing. `code` triggers; the former trigger names are in the skip list below,
// which is the direction that would go green again if the old allowlist came
// back.
func TestRunPostCollectLinker_GatedByCollectorType(t *testing.T) {
	const collectedGraph = "some-repo"

	t.Run("triggers/code", func(t *testing.T) {
		gc := &linkerTriggerCaller{}
		runPostCollectLinker(context.Background(), newLinkerTriggerDeps(gc), "code", collectedGraph, false)
		assert.True(t, gc.wireTouched(), "a code collect must run the linker (wire exercised)")
	})

	// EVERY FORMER TRIGGER IS IN THE SKIP LIST, deliberately: with them absent, an
	// implementation that kept the old allowlist alongside the new one would pass
	// the row above and leave the correction half-made.
	skipTypes := []string{
		"web", "pdf", "knowledge",
		"github", "gitlab", "bitbucket", "cicd",
		"aws", "gcp", "azure", "k8s",
	}
	for _, ct := range skipTypes {
		t.Run("skips/"+ct, func(t *testing.T) {
			gc := &linkerTriggerCaller{}
			runPostCollectLinker(context.Background(), newLinkerTriggerDeps(gc), ct, collectedGraph, false)
			assert.False(t, gc.wireTouched(), "collector type %q must not run the linker", ct)
		})
	}
}

// TestRunPostCollectLinker_NoCollectedGraphSkipsAudiblyAndNeverEnumerates is the
// KILL TEST for the empty-name arm, and it is the leg an implementer skips.
//
// THE TWO WRONG ANSWERS ARE BOTH SILENT. Falling back to the all-graphs sweep
// restores exactly the cross-graph fan-out the bound removed, and it does so at
// the moment the wiring is broken; failing the collect reports a successful
// upload as a failure. The tail is best-effort, so it WARNS and does nothing —
// and "does nothing" is asserted on the WIRE, because a fallback would touch it.
func TestRunPostCollectLinker_NoCollectedGraphSkipsAudiblyAndNeverEnumerates(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	gc := &linkerTriggerCaller{}
	runPostCollectLinker(context.Background(), newLinkerTriggerDeps(gc), "code", "", false)

	assert.False(t, gc.wireTouched(),
		"a scoped tail with no graph name must touch the wire ZERO times — an enumeration here is the silent fallback")
	logged := buf.String()
	assert.Contains(t, logged, "level=WARN", "the skip is AUDIBLE: a wiring defect that logs nothing is invisible")
	assert.Contains(t, logged, "no collected graph name", "and the warning names the condition")
	assert.Contains(t, logged, "no all-graphs fallback", "and states what it refused to do")

	// KNOWN POSITIVE, same helper and same fake: with a name, the wire IS touched.
	// Without it the assertion above is satisfied by a helper that never runs.
	gc2 := &linkerTriggerCaller{}
	runPostCollectLinker(context.Background(), newLinkerTriggerDeps(gc2), "code", "some-repo", false)
	assert.True(t, gc2.wireTouched(), "control: the same call WITH a graph name reaches the pass")
}

// TestRunPostCollectLinker_NilGraphCaller_DoesNotPanic asserts the degraded-mode
// path: when deps.GraphCaller() returns nil, the helper slog.Warns and returns
// cleanly without touching anything.
func TestRunPostCollectLinker_NilGraphCaller_DoesNotPanic(t *testing.T) {
	runPostCollectLinker(context.Background(), newLinkerTriggerDeps(nil), "code", "some-repo", false)
}

// TestRunPostCollectLinker_RunError_BestEffort asserts that an error from the
// in-process linker run does not surface to the caller (the helper is
// fire-and-forget — the collect's user-facing result is unchanged).
func TestRunPostCollectLinker_RunError_BestEffort(t *testing.T) {
	gc := &linkerTriggerCaller{execErr: assert.AnError}
	// Must not panic / must return cleanly despite the linker's Execute failing.
	runPostCollectLinker(context.Background(), newLinkerTriggerDeps(gc), "code", "some-repo", false)
	assert.Positive(t, gc.execs.Load(), "the linker attempted the wire before failing")
}
