// SPDX-License-Identifier: Apache-2.0

package transcriptsync

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// updateRollupCapture regenerates the checked-in artifact pair instead of asserting
// against it. The default run ASSERTS, which is what makes the artifact a gate: a producer
// change that silently alters the emitted payload reddens here rather than in the
// consuming repo.
var updateRollupCapture = flag.Bool("update-rollup-capture", false,
	"rewrite testdata/rollup_v2_payload.json and its sibling PROVENANCE from this run's captured payload")

const (
	captureArtifactPath   = "testdata/rollup_v2_payload.json"
	captureProvenancePath = "testdata/rollup_v2_payload.PROVENANCE"
)

// canonicalArrays names every top-level rollup array the aggregation MATERIALIZES FROM MAP
// ITERATION, which is what makes their element order nondeterministic between runs. That
// rule — not this enumeration — is what a future row kind inherits: if finish() grows
// another array built by walking a map, it belongs here too. All four qualify today: facts
// from the fact accumulators, latency_hist from the histogram counts, slow_calls from the
// per-tool candidate lists (the per-tool sort orders WITHIN a tool, while the cross-tool
// concatenation walks the map and is unordered), and duplicate_commands from the fine
// duplicate accumulators.
var canonicalArrays = []string{"facts", "latency_hist", "slow_calls", "duplicate_commands"}

// captureRollupParse is a ParseFunc that ignores the file bytes and returns a synthetic row
// set exercising every v2 active case in ONE payload, because the consuming repo's wire test
// reads one file. Cases, in order below: an agent with three same-day instants (a clean
// non-zero on both fields); an agent whose two instants straddle midnight (a measured zero
// per day against a non-zero whole-life total); an agent whose two same-day instants sit in
// different fact grains (the denormalized value repeated, never split); an agent with a
// single record (a measured zero on both); and a main-lane row carrying no agent (both keys
// present and null). Three distinct tools and two duplicate groups keep every array the
// capture canonicalizes genuinely multi-element. Every value is synthetic.
func captureRollupParse(source string, _ io.Reader) ([]transcripts.Row, error) {
	day := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	midnight := time.Date(2026, 6, 20, 23, 59, 59, 0, time.UTC)
	row := func(agent, tool, hash string, ts time.Time, dur int64, sidechain bool) transcripts.Row {
		return transcripts.Row{
			Source: transcripts.Source(source), SessionID: "cap-s", Project: "/cap", Model: "cap-model",
			ToolName: tool, SubagentType: "cap-type", AgentID: agent, IsSidechain: sidechain,
			ToolInputHash: hash, ToolInputPreview: "cap-input-" + tool,
			RecordTS: ts, DurationMs: dur, InputTokens: 10, OutputTokens: 5,
		}
	}
	return []transcripts.Row{
		row("cap-a-multi", "Bash", "cap-h1", day, 100, true),
		row("cap-a-multi", "Bash", "cap-h1", day.Add(30*time.Second), 200, true),
		row("cap-a-multi", "Bash", "cap-h1", day.Add(90*time.Second), 300, true),

		row("cap-a-mid", "Read", "cap-h2", midnight, 150, true),
		row("cap-a-mid", "Read", "cap-h2", midnight.Add(2*time.Second+999500*time.Microsecond), 250, true),

		row("cap-a-split", "Bash", "", day, 120, true),
		row("cap-a-split", "Read", "", day.Add(90*time.Second), 130, true),

		row("cap-a-one", "Grep", "", day, 140, true),

		row("", "Bash", "", day, 160, false),
	}, nil
}

// TestCaptureRollupV2Payload captures the v2 payload from the producer's REAL emission path
// — the raw marshaled confirm-batch body the fake backend recorded — canonicalizes the
// order-nondeterministic arrays, and asserts it against the checked-in artifact. It decodes
// into generic values only: routing the bytes back through the wire structs would re-derive
// the very tag spellings the artifact exists to prove, which is the failure the captured
// handoff is meant to prevent.
func TestCaptureRollupV2Payload(t *testing.T) {
	dir := t.TempDir()
	path := writeOffsetsFile(t, dir, "cap.jsonl", 1, 2)
	wm, _ := newTempWatermarkStore(t)
	backend := newFakeTranscriptBackend(t)
	backend.consentEnabledFlag = true

	summary, err := Run(context.Background(), Config{
		Transport:  backend,
		Enumerator: fixedEnumerator{files: []TranscriptFile{{Path: path, Source: "claude", Session: "cap-sess"}}},
		Parse:      captureRollupParse,
		Watermarks: wm,
	})
	require.NoError(t, err)
	require.Equal(t, 1, summary.FilesUploaded)

	raw := backend.lastConfirmBody()
	require.NotEmpty(t, raw, "the fake backend recorded the emitted confirm-batch body")

	got := renderCanonicalRollup(t, raw)
	assertOrderIndependentRender(t, raw, got)

	if *updateRollupCapture {
		require.NoError(t, os.WriteFile(captureArtifactPath, got, 0o600))
		writeCaptureProvenance(t)
		t.Logf("regenerated %s and %s", captureArtifactPath, captureProvenancePath)
		return
	}

	want, err := os.ReadFile(captureArtifactPath)
	require.NoError(t, err,
		"the captured artifact must exist; regenerate with: go test ./internal/transcriptsync/ -run '^TestCaptureRollupV2Payload$' -update-rollup-capture")
	require.Equal(t, string(want), string(got),
		"the checked-in capture and the producer's emitted payload disagree; if the producer change is intended, regenerate with -update-rollup-capture and hand the new artifact to the consuming repo")
}

// assertOrderIndependentRender proves the canonicalization is COMPLETE rather than merely
// declared. It decodes a second copy of the same recorded bytes, REVERSES every top-level
// array the payload carries, canonicalizes and re-renders: any array left out of the
// canonicalization renders differently once reversed, so this fails deterministically on
// every run rather than only when map iteration happens to reorder it.
//
// The reversal walks EVERY array present rather than only the ones canonicalArrays names,
// which is a superset of the named ones and is what lets this see an array DROPPED from
// that list. A reversal driven by the list itself can only catch a canonicalizer that
// fails to sort something the list names, never a name missing from it.
func assertOrderIndependentRender(t *testing.T, raw []byte, want []byte) {
	t.Helper()
	rollup := decodeCapturedRollup(t, raw)
	reversed := 0
	for _, name := range canonicalArrays {
		require.Contains(t, rollup, name, "the %q array must be present before it can be reversed", name)
	}
	for _, v := range rollup {
		arr, ok := v.([]any)
		if !ok {
			continue
		}
		require.GreaterOrEqual(t, len(arr), 2,
			"every payload array needs at least two elements or reversing it is a no-op and this check is vacuous")
		slices.Reverse(arr)
		reversed++
	}
	require.GreaterOrEqual(t, reversed, len(canonicalArrays),
		"the perturbation must reach at least every array canonicalArrays names")

	canonicalizeRollupArrays(t, rollup)
	require.Equal(t, string(want), string(renderRollup(t, rollup)),
		"the render depends on the input order, so at least one map-materialized array is not being canonicalized")
}

// decodeCapturedRollup decodes the recorded confirm-batch body into generic values and
// returns the single chunk's rollup object. UseNumber is REQUIRED: without it every integer
// decodes to a float64 and can re-render in exponent form, silently corrupting the artifact
// the consuming repo parses.
func decodeCapturedRollup(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var envelope map[string]any
	require.NoError(t, dec.Decode(&envelope), "the recorded confirm body decodes as generic JSON")

	chunks, ok := envelope["chunks"].([]any)
	require.True(t, ok, "the confirm body carries a chunks array")
	require.Len(t, chunks, 1, "one enumerated file yields one confirm chunk")
	chunk, ok := chunks[0].(map[string]any)
	require.True(t, ok, "the chunk is an object")
	rollup, ok := chunk["rollup"].(map[string]any)
	require.True(t, ok, "the chunk carries a rollup object")
	return rollup
}

// canonicalizeRollupArrays sorts each map-materialized array by its elements' own marshaled
// bytes. It reorders elements only — nothing is added, removed or rewritten — and marshaling
// a generic object emits its keys in sorted order, so the whole render is deterministic.
func canonicalizeRollupArrays(t *testing.T, rollup map[string]any) {
	t.Helper()
	for _, name := range canonicalArrays {
		arr, ok := rollup[name].([]any)
		require.True(t, ok, "the %q array must be present in the captured payload", name)
		require.GreaterOrEqual(t, len(arr), 2,
			"the capture fixture must drive the %q array to at least two elements, or its ordering is untested", name)
		keyed := make([]struct {
			key string
			val any
		}, len(arr))
		for i, el := range arr {
			b, err := json.Marshal(el)
			require.NoError(t, err)
			keyed[i].key, keyed[i].val = string(b), el
		}
		sort.SliceStable(keyed, func(i, j int) bool { return keyed[i].key < keyed[j].key })
		for i := range keyed {
			arr[i] = keyed[i].val
		}
	}
}

// renderRollup renders the canonicalized rollup object as the artifact's bytes.
func renderRollup(t *testing.T, rollup map[string]any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(rollup, "", "  ")
	require.NoError(t, err)
	return append(b, '\n')
}

// renderCanonicalRollup is the whole capture path: decode the recorded bytes, canonicalize
// the map-materialized arrays, render.
func renderCanonicalRollup(t *testing.T, raw []byte) []byte {
	t.Helper()
	rollup := decodeCapturedRollup(t, raw)
	canonicalizeRollupArrays(t, rollup)
	return renderRollup(t, rollup)
}

// writeCaptureProvenance records where the sibling artifact came from, so a reader can tell
// a real capture from a hand-authored payload. It is written only under the update flag and
// is never byte-asserted by the default run: it carries a commit SHA that legitimately moves
// on every commit.
func writeCaptureProvenance(t *testing.T) {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	require.NoError(t, err, "the producing commit SHA is part of the provenance")
	lines := []string{
		"repository: knowledge",
		"package: cmd/knowledge/internal/transcriptsync",
		"commit: " + strings.TrimSpace(string(out)),
		"command: go test ./internal/transcriptsync/ -run '^TestCaptureRollupV2Payload$' -update-rollup-capture",
		fmt.Sprintf("rollup_schema_version: %d", rollupSchemaVersion),
		"fixture: one synthetic session holding an agent with three same-day instants, an agent straddling midnight, an agent split across two fact grains, an agent with a single record, and a main-lane row carrying no agent",
		"provenance: every KEY and VALUE below came from the producer's own marshaler via the recorded confirm-batch body; the test canonicalized only the ORDER of the four arrays materialized from map iteration — facts, latency_hist, slow_calls and duplicate_commands — and rewrote nothing else",
	}
	require.NoError(t, os.WriteFile(captureProvenancePath, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
}
