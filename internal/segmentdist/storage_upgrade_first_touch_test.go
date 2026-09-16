// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestBothPoolsPopulatedFirstTouchReadsOnlyTheDestination is requirement 5's
// both-pools-populated cell, and it pins the alternative that was CHOSEN.
//
// THE CHOICE, stated so a reader does not have to infer it from an absence: the
// keyless BM25 fix reconciles NOTHING. No code reads the root pool into the
// destination pool, lazily or otherwise. The requirement allows either a lazy
// next-touch reconciliation or leaving the root pool alone, and leaving it alone
// is what this change does, for two reasons. First, "convergence is always lazy"
// is satisfied trivially by doing no convergence at all, while a reconciliation is
// a new operational surface that would have to be bounded, and the operator shape
// it would run against is 16,110 root files for one graph — every one of them an
// append through the same publish path a separate in-flight fix is addressing.
// Second, the destination pool refills from the server's own CorpusDelta feed on
// the first search, which is the mechanism this whole change is about; the cost is
// one re-drain from a zero cursor, paid once per graph per upgrade.
//
// SO THE CLAIM IS AN ISOLATION CLAIM, and it is the one an operator's upgrade
// actually depends on: a document sitting in the ROOT pool is not served, not
// double-counted, and not merged in. The destination pool is the whole answer.
//
// THE FIXTURE IS THE UPGRADE STATE ITSELF: the root pool holds a document under
// the same (graph, name) key as the child, which is exactly what an operator who
// ran a pre-split release has on disk.
func TestBothPoolsPopulatedFirstTouchReadsOnlyTheDestination(t *testing.T) {
	t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(filepath.Join(t.TempDir(), "config"), 0)))
	cacheDir := t.TempDir()
	m := closeOnCleanup(t, NewManager(cacheDir, 0))

	// THE PRE-UPGRADE POOL: an UNBOUND write, which is what the producer did before
	// the fix — it lands in the root pool, the one no destination-bound search reads.
	rootDoc := searchengine.Document{ID: "same", Fields: map[string]string{searchengine.FieldContent: "rootonly"}}
	if err := m.ReplaceBucketFields(t.Context(), kgtypes.GraphKnowledge, "default", nil, []searchengine.Document{rootDoc}); err != nil {
		t.Fatalf("seeding the root pool: %v", err)
	}
	// THE POST-UPGRADE POOL: the same id, a different term, written through a bound
	// ctx — what the producer does now.
	local := graphclient.WithDestination(t.Context(), graphclient.Destination{Storage: "local"})
	childDoc := searchengine.Document{ID: "same", Fields: map[string]string{searchengine.FieldContent: "childonly"}}
	if err := m.ReplaceBucketFields(local, kgtypes.GraphKnowledge, "default", nil, []searchengine.Document{childDoc}); err != nil {
		t.Fatalf("seeding the destination pool: %v", err)
	}

	// FIXTURE CHECK: the two pools are really two directories on disk. Without this
	// every assertion below would hold vacuously on a fixture that wrote one pool
	// twice.
	if child := childPoolDir(t, cacheDir); child == "" {
		t.Fatal("fixture check: no per-destination pool directory was created, so this row compares one pool with itself")
	}

	// THE FIRST TOUCH. A destination-bound search returns the CHILD's document and
	// not the root's.
	hits, err := m.Search(local, kgtypes.GraphKnowledge, "default", "childonly", nil, 10)
	if err != nil {
		t.Fatalf("Search(childonly) returned error: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("Search(childonly) returned %d hits, want exactly 1 — the destination pool holds it", len(hits))
	}
	rootHits, err := m.Search(local, kgtypes.GraphKnowledge, "default", "rootonly", nil, 10)
	if err != nil {
		t.Fatalf("Search(rootonly) returned error: %v", err)
	}
	if len(rootHits) != 0 {
		t.Errorf("a destination-bound search returned %d hits for a term only the ROOT pool holds — the "+
			"chosen shape reconciles nothing, so the root pool must be neither read nor merged in", len(rootHits))
	}

	// AND THE DOUBLY-RESIDENT ID APPEARS ONCE, WITH ONE SCORE. Both pools hold id
	// "same"; a reader that merged them would return it twice.
	if hits[0].ID != searchengine.ExternalID("same") {
		t.Errorf("hit id = %q, want \"same\"", hits[0].ID)
	}

	// AND THE TOUCH IS IDEMPOTENT: a second search does no further work and changes
	// no result. With no reconciliation there is nothing to converge, and this is
	// what says so rather than leaving it implied.
	again, err := m.Search(local, kgtypes.GraphKnowledge, "default", "childonly", nil, 10)
	if err != nil {
		t.Fatalf("the second Search returned error: %v", err)
	}
	if len(again) != len(hits) || again[0].ID != hits[0].ID {
		t.Errorf("the second search returned %d hits (%v), want the same one the first did (%v) — the "+
			"first touch must not have moved anything", len(again), again, hits)
	}
	if after, err := m.Search(local, kgtypes.GraphKnowledge, "default", "rootonly", nil, 10); err != nil || len(after) != 0 {
		t.Errorf("the root pool became visible after a touch: hits=%v err=%v — nothing reconciles it in", after, err)
	}
}

// childPoolDir returns the per-destination pool directory under cacheDir, or ""
// when none exists. It matches the `storage-` prefix rather than the sha256
// literal for the reason the derivation itself is not pinned anywhere: hard-coding
// the digest would make this fixture check pass for the wrong reason.
func childPoolDir(t *testing.T, cacheDir string) string {
	t.Helper()
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "storage-") {
			return filepath.Join(cacheDir, e.Name())
		}
	}
	return ""
}
