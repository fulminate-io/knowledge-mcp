// SPDX-License-Identifier: Apache-2.0

package pipeline

// bm25_wake_test.go is the keyless-BM25 fix's wake row (the in-process half). A signed-in
// daemon's BM25 arm must be reachable by the central bulk gen-poll, so a write
// followed by continuing activity drains within the wake path rather than waiting
// out the arm's idle backoff — which on a logged-in client doubles 5s → 10 → 20 →
// … to the ONE HOUR ceiling and is shortened by nothing short of a restart.
//
// BOTH HALVES ARE ASSERTED SEPARATELY, and that is the whole design of the row:
// pokeAxisWake is a SILENT NO-OP past the end of the slice
// (`if !ok || wakeIdx >= len(wakes) { return }`), so a registration without a
// poker and a poker without a registration are BOTH inert, neither breaks the
// build, and neither turns any other test red. Half a landing would otherwise
// ship as a change that does nothing.
//
// THE END-TO-END SIGNED-IN OBSERVATION IS NOT HERE and cannot be: the spawn
// harness carries --no-auth as a mandatory floor and the CI lane excludes cloud
// scenarios by design, so no lane in this repository can hold a login. That
// observation belongs to the live confirmation on dev.

import (
	"context"
	"testing"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// bm25WakeSlot is the slice index the arm's wake channel must occupy, spelled
// LOCALLY rather than as the production constant bm25WakeIdx, for one reason: this
// row must COMPILE on the unfixed tree so its red is an assertion a reviewer can
// read, not a build failure. The production index is nevertheless pinned, and more
// strongly than a constant reference would: the poke half below drives the real
// applyStorageGenPollResponse and the real pokeAxisWake, so the wake it delivers
// lands on whatever index PRODUCTION chose, and it is read back from this slot.
const bm25WakeSlot = 2

func TestBM25ArmIsPokedOnCorpusStampMovement(t *testing.T) {
	const graphName = "default"
	fb := newFlippableBackend()
	// Logged in: the configuration whose idle interval climbs to an hour, which is
	// the cost R9 removes.
	fb.loggedIn.Store(true)
	p := New(Config{CloudTick: time.Hour, IdleTickMax: time.Hour}, fb, nil, nil)
	p.AttachSegmentManager(&fakeShipManager{})
	ws := workingset.New()
	if !ws.Admit(kgtypes.GraphKnowledge, graphName, "search") {
		t.Fatal("fixture check: the working set must accept the member the registration pass reads")
	}
	p.AttachWorkingSet(ws)
	ctx := context.Background()
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = p.Stop(stopCtx)
	})
	p.refreshOnce(ctx)

	key := graphKey{GraphType: kgtypes.GraphKnowledge, GraphName: graphName}

	// HALF ONE — THE REGISTRATION. The arm's channel must be in the slice, at the
	// index the poker uses. Asserting the length alone would pass on a slice that
	// grew a nil third entry, so the channel identity is asserted too.
	p.collectorMu.Lock()
	wakes := append([]chan struct{}(nil), p.collectorWakes[key]...)
	p.collectorMu.Unlock()
	if len(wakes) <= bm25WakeSlot {
		t.Fatalf("collectorWakes[%+v] has length %d, want more than bm25WakeSlot=%d — pokeAxisWake is a "+
			"silent no-op past the end of the slice, so an unregistered arm channel makes every poke inert",
			key, len(wakes), bm25WakeSlot)
	}
	if wakes[bm25WakeSlot] == nil {
		t.Fatalf("collectorWakes[%+v][%d] is nil, want the arm's own wake channel", key, bm25WakeSlot)
	}
	// KNOWN POSITIVE for the indexing: the two LLM axes are still where their
	// constants say they are, so a third index that happened to be right for the
	// wrong reason is caught.
	if wakes[summaryWakeIdx] == nil || wakes[embedWakeIdx] == nil {
		t.Fatalf("the summary/embed wake channels moved: collectorWakes[%+v] = %d entries with nil at "+
			"summaryWakeIdx=%t embedWakeIdx=%t", key, len(wakes),
			wakes[summaryWakeIdx] == nil, wakes[embedWakeIdx] == nil)
	}
	if summaryWakeIdx == bm25WakeSlot || embedWakeIdx == bm25WakeSlot {
		t.Fatalf("bm25WakeSlot=%d collides with summaryWakeIdx=%d / embedWakeIdx=%d",
			bm25WakeSlot, summaryWakeIdx, embedWakeIdx)
	}

	// HALF TWO — THE POKE. A gen-poll response whose CORPUS stamp advances must
	// deliver a wake on that channel. The corpus stamp is the arm's own axis: it is
	// neither the summary gen nor the embed gen nor the segment stamp (which folds
	// vector-write and erasure-append times, so keying the arm on it would wake it
	// on embedding activity and still miss deletes).
	drain(wakes[bm25WakeSlot])
	pokes, _, _ := p.applyStorageGenPollResponse(key.Destination, &knowledgev1.PipelineGenPollResponse{
		Entries: []*knowledgev1.PipelineGenPollEntry{{
			GraphType:             string(kgtypes.GraphKnowledge),
			GraphName:             graphName,
			Axis:                  "segment",
			CorpusDeltaStampNanos: 4_000_000_000,
		}},
	})
	var bm25Pokes int
	for _, pk := range pokes {
		if pk.wakeIdx == bm25WakeSlot && pk.key == key {
			bm25Pokes++
		}
	}
	if bm25Pokes != 1 {
		t.Fatalf("a corpus-stamp advance produced %d BM25 pokes (%d pokes in total), want exactly 1 — "+
			"without it the arm waits out its idle interval, which on a logged-in client climbs to an hour",
			bm25Pokes, len(pokes))
	}
	for _, pk := range pokes {
		p.pokeAxisWake(pk.key, pk.wakeIdx)
	}
	if !woken(wakes[bm25WakeSlot]) {
		t.Error("the poke did not reach the arm's wake channel — pokeAxisWake indexes collectorWakes and " +
			"returns silently when the index is past the end, so the registration and the poke must agree")
	}

	// AND IT DEBOUNCES AGAINST ITS OWN POKE HISTORY, exactly as the two gens and
	// the segment stamp do: the SAME stamp served again is not movement and must
	// produce no second poke. Without this a client polling a quiet graph would
	// drain on every poll.
	drain(wakes[bm25WakeSlot])
	repeat, _, _ := p.applyStorageGenPollResponse(key.Destination, &knowledgev1.PipelineGenPollResponse{
		Entries: []*knowledgev1.PipelineGenPollEntry{{
			GraphType:             string(kgtypes.GraphKnowledge),
			GraphName:             graphName,
			Axis:                  "segment",
			CorpusDeltaStampNanos: 4_000_000_000,
		}},
	})
	for _, pk := range repeat {
		if pk.wakeIdx == bm25WakeSlot {
			t.Error("an unchanged corpus stamp produced a BM25 poke — the poke must debounce against this " +
				"loop's own poke watermark, or a quiet graph drains on every poll")
		}
	}

	// AND A ZERO STAMP IS NOT MOVEMENT. Zero is what the server serves for a graph
	// whose stamp has never been recorded.
	zeroKey := graphKey{GraphType: kgtypes.GraphKnowledge, GraphName: "never-stamped"}
	zeroPokes, _, _ := p.applyStorageGenPollResponse(zeroKey.Destination, &knowledgev1.PipelineGenPollResponse{
		Entries: []*knowledgev1.PipelineGenPollEntry{{
			GraphType:             string(kgtypes.GraphKnowledge),
			GraphName:             "never-stamped",
			Axis:                  "segment",
			CorpusDeltaStampNanos: 0,
		}},
	})
	for _, pk := range zeroPokes {
		if pk.wakeIdx == bm25WakeSlot {
			t.Error("a zero corpus stamp produced a BM25 poke — zero is the server's never-recorded value")
		}
	}
}

// drain empties a buffered(1) wake channel so a later read means a NEW signal.
func drain(ch <-chan struct{}) {
	select {
	case <-ch:
	default:
	}
}

// woken reports whether a signal is sitting on a buffered(1) wake channel.
func woken(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	case <-time.After(time.Second):
		return false
	}
}
