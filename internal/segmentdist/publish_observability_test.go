// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// infosContaining is the INFO counterpart of warnsContaining: the ship/publish/swap
// lines are steady-state operational facts rather than faults, so they are emitted
// at Info and a WARN-only reader would find none of them.
func (h *capturingSlogHandler) infosContaining(substr string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, r := range h.records {
		if r.Level != slog.LevelInfo || !strings.Contains(r.Message, substr) {
			continue
		}
		var b strings.Builder
		b.WriteString(r.Message)
		r.Attrs(func(a slog.Attr) bool {
			b.WriteString(" " + a.Key + "=" + a.Value.String())
			return true
		})
		out = append(out, b.String())
	}
	return out
}

// TestSegmentPublishEmitsShipSkipCounts is observability gate 4a, asserted in
// segmentdist over a REAL distManager because this is the only layer where the two
// numbers exist. The tools interceptor never sees a ship diff, so a gate placed
// there could only be satisfied by a value the interceptor synthesized.
//
// WHICH PRODUCTION CALL WOULD HAVE TO BE WRONG: the ship/publish emit sites in
// shipAndPublish and publishResident. The capture is installed over the real slog
// default, so nothing here passes against a test logger.
//
// WHY SHIPPED-VERSUS-SKIPPED IS THE LOAD-BEARING PAIR: the ship diff suppresses
// every content-hash-unchanged blob, and only shipped blobs reach the L2 cache. A
// line reporting the upload count alone describes a rebuild that uploaded 4 of 16
// buckets as if that were the whole live set — which is the reading that made the
// incident invisible. 12 skipped against 4 shipped is a sentence a human parses
// correctly on sight.
//
// DELIBERATELY NOT PARALLEL. Shared resource: the process-global default slog
// logger, which this test swaps for a capturing handler to assert over the
// records the path emits. Concurrent peers would both install and restore that
// one global, so the handler this test reads could be a peer's, and a peer's
// unrelated records would land in this test's capture.
func TestSegmentPublishEmitsShipSkipCounts(t *testing.T) {
	gt, name := kgtypes.GraphCode, "publish-observability"
	base := t.TempDir()
	svc, _ := newSegmentHarness(t)
	target := graphSelector(gt, name)

	corpus := probeDocs(probeCorpusN)
	groups, buckets := groupByBucket(corpus, searchengine.BucketCountFor(len(corpus)))

	newMgr := func() *Manager {
		view := svc.viewFor(target, "")
		view.listFromManifest = true
		view.verifies = true
		return NewManager(loginStateStub{loggedIn: true}, base, 0, withSegmentSource(view))
	}

	// Prior state: the server already holds 12 of the 16 buckets, so the rebuild
	// below reproduces those byte-identically and ships only the remaining 4.
	driveRebuild(t, newMgr(), name, groups, buckets[:priorBuckets])
	quarantineL2(t, base, name)

	logs := installCapturingSlog(t)
	built, _, swapped := driveRebuild(t, newMgr(), name, groups, buckets)
	if !swapped || built != probeBuckets {
		t.Fatalf("fixture: built=%d swapped=%v, want %d/true", built, swapped, probeBuckets)
	}

	shipLines := logs.infosContaining("ship diff resolved")
	if len(shipLines) == 0 {
		t.Fatalf("no ship-diff line emitted — a rebuild that uploads a fraction of the live set must say so; got %d records",
			len(logs.records))
	}
	var sawHNSWShipCounts bool
	for _, l := range shipLines {
		if !containsAll(l, []string{"format=", "shipped=", "skipped_as_present=", "resident=", "name="}) {
			t.Errorf("ship-diff line is missing the shipped/skipped pair or the graph identity: %q", l)
			continue
		}
		if containsAll(l, []string{"format=hnsw", "shipped=4", "skipped_as_present=12", "resident=16"}) {
			sawHNSWShipCounts = true
		}
	}
	if !sawHNSWShipCounts {
		t.Errorf("the hnsw ship-diff line did not carry shipped=%d against skipped_as_present=%d over resident=%d; lines: %v",
			newBuckets, priorBuckets, probeBuckets, shipLines)
	}

	swapLines := logs.infosContaining("manifest swap COMPLETED")
	if len(swapLines) == 0 {
		t.Fatalf("no manifest-swap line emitted — a publish that LANDED logged nothing while both skip paths log a WARN, "+
			"so the log could not distinguish a landed publish from a refused one; got %d records", len(logs.records))
	}
	var sawHNSWSwap bool
	for _, l := range swapLines {
		if !containsAll(l, []string{"format=", "published=", "name="}) {
			t.Errorf("manifest-swap line is missing the published cardinality or the graph identity: %q", l)
			continue
		}
		if containsAll(l, []string{"format=hnsw", "published=16"}) {
			sawHNSWSwap = true
		}
	}
	if !sawHNSWSwap {
		t.Errorf("the hnsw swap line did not carry the full published cardinality %d — that number is what a truncation "+
			"claim is argued against; lines: %v", probeBuckets, swapLines)
	}
}
