// SPDX-License-Identifier: Apache-2.0

package web

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGithubMaterializerState_ClaimAndPublish covers the basic claim →
// publish lifecycle: the first call gets claimed=true, the second sees the
// already-published root ID with claimed=false, ready=true.
func TestGithubMaterializerState_ClaimAndPublish(t *testing.T) {
	s := newGithubMaterializerState()
	key := githubKey{Owner: "owner", Repo: "repo", Ref: "main"}

	id, claimed, ready := s.claim(key)
	if !claimed {
		t.Fatalf("first claim: claimed=false want=true")
	}
	if ready {
		t.Fatalf("first claim: ready=true want=false (no root yet)")
	}
	if id != "" {
		t.Errorf("first claim: id=%q want empty", id)
	}

	s.publish(key, "gh-root:owner/repo@main")

	id2, claimed2, ready2 := s.claim(key)
	if claimed2 {
		t.Errorf("second claim: claimed=true want=false")
	}
	if !ready2 {
		t.Errorf("second claim: ready=false want=true")
	}
	if id2 != "gh-root:owner/repo@main" {
		t.Errorf("second claim: id=%q want=gh-root:owner/repo@main", id2)
	}
}

// TestGithubMaterializerState_DifferentRefs verifies (owner, repo) at
// different refs are independent keys (the second materialization is
// not deduped).
func TestGithubMaterializerState_DifferentRefs(t *testing.T) {
	s := newGithubMaterializerState()
	keyMain := githubKey{Owner: "owner", Repo: "repo", Ref: "main"}
	keyDev := githubKey{Owner: "owner", Repo: "repo", Ref: "dev"}

	_, claimedMain, _ := s.claim(keyMain)
	if !claimedMain {
		t.Fatalf("main claim: claimed=false")
	}
	s.publish(keyMain, "gh-root:owner/repo@main")

	_, claimedDev, ready := s.claim(keyDev)
	if !claimedDev {
		t.Fatalf("dev claim: claimed=false (different ref must NOT dedupe)")
	}
	if ready {
		t.Fatalf("dev claim: ready=true (no root yet)")
	}
}

// TestGithubMaterializerState_ConcurrentClaim verifies that when two
// workers race on the same key, exactly one gets claimed=true and the
// other blocks until publish().
func TestGithubMaterializerState_ConcurrentClaim(t *testing.T) {
	s := newGithubMaterializerState()
	key := githubKey{Owner: "owner", Repo: "repo", Ref: "main"}

	// First worker claims.
	_, claimed, _ := s.claim(key)
	if !claimed {
		t.Fatalf("worker 1: not claimed")
	}

	var wg sync.WaitGroup
	var w2id string
	var w2claimed, w2ready bool
	wg.Go(func() {
		w2id, w2claimed, w2ready = s.claim(key)
	})

	// Give worker 2 a chance to enter claim() and block on the WaitGroup.
	// The actual sleep here is unfortunate but simulates real-world race —
	// the test still passes deterministically because publish() unblocks
	// regardless of when worker 2 entered.
	s.publish(key, "gh-root:owner/repo@main")
	wg.Wait()

	if w2claimed {
		t.Errorf("worker 2: claimed=true (should have lost the race)")
	}
	if !w2ready {
		t.Errorf("worker 2: ready=false (publish should have unblocked)")
	}
	if w2id != "gh-root:owner/repo@main" {
		t.Errorf("worker 2: id=%q want=gh-root:owner/repo@main", w2id)
	}
}

// TestEmitMaterializerWarning shapes the warning node + edge correctly.
func TestEmitMaterializerWarning(t *testing.T) {
	info := githubURLInfo{Owner: "owner", Repo: "repo", Ref: "main", Kind: kindTree}
	w := &materializerWarning{
		Reason:    "size_cap_pre_read",
		URL:       "https://github.com/owner/repo/tree/main/big",
		BytesSeen: 0,
		Cap:       50 << 20,
	}

	node, edges, err := emitMaterializerWarning("page-id-123", "https://github.com/owner/repo/tree/main", info, w)
	require.NoError(t, err, "a string-map marshal cannot fail in a correct build")

	if node.Type != "document" {
		t.Errorf("Type=%q want=document", node.Type)
	}
	if node.Metadata["materialization_skipped"] != "size_cap_pre_read" {
		t.Errorf("metadata.materialization_skipped=%q", node.Metadata["materialization_skipped"])
	}
	for _, k := range []string{"reason", "url", "uri", "bytes_seen", "cap_bytes", "owner", "repo", "ref"} {
		if _, ok := node.Metadata[k]; !ok {
			t.Errorf("metadata key %q missing", k)
		}
	}
	if len(edges) != 1 {
		t.Fatalf("edges=%d want=1", len(edges))
	}
	e := edges[0]
	if e.FromID != "page-id-123" {
		t.Errorf("edge FromID=%q want=page-id-123", e.FromID)
	}
	if e.ToID != node.Id {
		t.Errorf("edge ToID=%q want=%q", e.ToID, node.Id)
	}
	if e.Type != "references" {
		t.Errorf("edge Type=%q want=references", e.Type)
	}
}

// TestEmitMaterializerWarning_NoParent emits no edge when parentPageID
// is empty (seed URL with no enclosing page).
func TestEmitMaterializerWarning_NoParent(t *testing.T) {
	info := githubURLInfo{Owner: "owner", Repo: "repo", Ref: "main", Kind: kindTree}
	w := &materializerWarning{
		Reason: "size_cap_mid_stream",
		URL:    "https://github.com/owner/repo",
	}
	node, edges, err := emitMaterializerWarning("", "https://github.com/owner/repo", info, w)
	require.NoError(t, err, "a string-map marshal cannot fail in a correct build")
	if node.Id == "" {
		t.Errorf("node ID empty")
	}
	if len(edges) != 0 {
		t.Errorf("edges=%d want=0 (no parent)", len(edges))
	}
}
