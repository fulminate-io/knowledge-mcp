// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/config"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

func TestStoragePoolsKeepDuplicateDocumentsSeparate(t *testing.T) {
	t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(filepath.Join(t.TempDir(), "config"), 0)))
	m := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	local := graphclient.WithDestination(t.Context(), graphclient.Destination{Storage: "local"})
	cloud := graphclient.WithDestination(t.Context(), graphclient.Destination{Storage: "cloud"})
	if err := m.ReplaceBucketFields(local, kgtypes.GraphKnowledge, "default", nil, []searchengine.Document{{ID: "same", Fields: map[string]string{searchengine.FieldContent: "localword"}}}); err != nil {
		t.Fatal(err)
	}
	if err := m.ReplaceBucketFields(cloud, kgtypes.GraphKnowledge, "default", nil, []searchengine.Document{{ID: "same", Fields: map[string]string{searchengine.FieldContent: "cloudword"}}}); err != nil {
		t.Fatal(err)
	}
	hits, err := m.Search(local, kgtypes.GraphKnowledge, "default", "localword", nil, 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("local search hits=%v err=%v; want one", hits, err)
	}
	hits, err = m.Search(local, kgtypes.GraphKnowledge, "default", "cloudword", nil, 10)
	if err != nil || len(hits) != 0 {
		t.Fatalf("local search leaked cloud: hits=%v err=%v", hits, err)
	}
}

// TestStorageManagersAreCapped is the segment half of the destination bound: a
// request names its own account, so the set of per-destination children — each
// with its own hashed cache directory on disk — is caller-driven. The map is
// capped on the same constant the Router's client cache uses, and an evicted
// child's directory goes with it.
func TestStorageManagersAreCapped(t *testing.T) {
	const selected = "11111111-1111-4111-8111-111111111111"
	live := stubAccountSelection(t, selected)
	cacheDir := t.TempDir()
	root := closeOnCleanup(t, NewManager(cacheDir, 0))

	keep := graphclient.Destination{Storage: "cloud", AccountID: *live}
	kept := root.ForDestination(graphclient.WithDestination(t.Context(), keep))

	for n := range graphclient.MaxBoundDestinations + 8 {
		d := burstAccount(n)
		// Touch the child's cache directory so the eviction has something on
		// disk to release — an empty child would make the directory count
		// vacuous.
		child := root.ForDestination(graphclient.WithDestination(t.Context(), d))
		if err := os.MkdirAll(child.cacheDir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	root.mu.Lock()
	size := len(root.storageManagers)
	survivor := root.storageManagers[keep]
	root.mu.Unlock()

	if size > graphclient.MaxBoundDestinations {
		t.Fatalf("storageManagers = %d children after a burst, want at most %d", size, graphclient.MaxBoundDestinations)
	}
	if survivor != kept {
		t.Fatal("the SELECTED account's child must survive a burst of foreign accounts")
	}

	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	dirs := 0
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "storage-") {
			dirs++
		}
	}
	if dirs == 0 {
		t.Fatal("no per-destination cache directory was created at all — the count below would be vacuous")
	}
	if dirs > graphclient.MaxBoundDestinations {
		t.Fatalf("per-destination cache directories = %d, want at most %d", dirs, graphclient.MaxBoundDestinations)
	}
}

// burstAccount spells the nth distinct, well-formed account id of a burst —
// what a caller sending the /mcp account header N times produces.
func burstAccount(n int) graphclient.Destination {
	return graphclient.Destination{Storage: "cloud", AccountID: fmt.Sprintf("%08d-0000-4000-8000-000000000000", n)}
}

// TestStorageManagersKeepTheDaemonsOwnChildren is the eviction keep-rule: the
// cap exists to bound CALLER-DRIVEN key space, so the children that are not
// caller-driven — the local store and the accountless cloud binding the
// daemon's own machine-auth work uses — are never eviction candidates, and
// neither is the selected account's.
//
// Written from the reviewer's probe: a burst of 40 distinct header accounts
// evicted the LOCAL child, Close()d it and os.RemoveAll'd its cache directory,
// and the next local search — through the fresh child the daemon's next local
// op gets — returned 0 hits and a nil error. A read that lost its content and
// reports success is a completeness failure, not a cache miss.
func TestStorageManagersKeepTheDaemonsOwnChildren(t *testing.T) {
	const selected = "11111111-1111-4111-8111-111111111111"
	stubAccountSelection(t, selected)
	cacheDir := t.TempDir()
	root := closeOnCleanup(t, NewManager(cacheDir, 0))

	local := graphclient.WithDestination(t.Context(), graphclient.Destination{Storage: "local"})
	accountless := graphclient.WithDestination(t.Context(), graphclient.Destination{Storage: "cloud"})
	selectedCtx := graphclient.WithDestination(t.Context(), graphclient.Destination{Storage: "cloud", AccountID: selected})

	// Content in the local child, and a CONTROL read proving it is there
	// before the burst — so a zero afterwards is the burst's doing.
	require.NoError(t, root.ReplaceBucketFields(local, kgtypes.GraphKnowledge, "default", nil,
		[]searchengine.Document{{ID: "same", Fields: map[string]string{searchengine.FieldContent: "localword"}}}))
	hits, err := root.Search(local, kgtypes.GraphKnowledge, "default", "localword", nil, 10)
	require.NoError(t, err)
	require.Len(t, hits, 1, "CONTROL: the local child holds the document before the burst")

	localChild := root.ForDestination(local)
	accountlessChild := root.ForDestination(accountless)
	selectedChild := root.ForDestination(selectedCtx)
	// Materialize each keep child's cache directory BEFORE the burst, so the
	// stat below distinguishes "still there" from "never created" — a lazily
	// created directory would make the assertion vacuous.
	for _, child := range []*Manager{localChild, accountlessChild, selectedChild} {
		require.NoError(t, os.MkdirAll(child.cacheDir, 0o700))
	}

	for n := range graphclient.MaxBoundDestinations + 8 {
		root.ForDestination(graphclient.WithDestination(t.Context(), burstAccount(n)))
	}

	for _, keep := range []struct {
		name  string
		dest  graphclient.Destination
		child *Manager
	}{
		{name: "local", dest: graphclient.Destination{Storage: "local"}, child: localChild},
		{name: "accountless cloud", dest: graphclient.Destination{Storage: "cloud"}, child: accountlessChild},
		{name: "selected account", dest: graphclient.Destination{Storage: "cloud", AccountID: selected}, child: selectedChild},
	} {
		assert.Same(t, keep.child, root.ForDestination(graphclient.WithDestination(t.Context(), keep.dest)),
			"the %s child is not caller-driven key space and must survive a burst of header accounts", keep.name)
		if keep.child.cacheDir != "" {
			_, statErr := os.Stat(keep.child.cacheDir)
			require.NoError(t, statErr, "the %s child's cache directory must survive the burst", keep.name)
		}
	}

	// THE READ THE PROBE BROKE: the same search, after the burst, through
	// whatever child the daemon's next local op gets.
	hits, err = root.Search(local, kgtypes.GraphKnowledge, "default", "localword", nil, 10)
	require.NoError(t, err)
	assert.Len(t, hits, 1, "a local read must not silently lose its content to a burst of foreign accounts")
}

// TestStorageManagersKeepAChildWithARequestInFlight is the in-flight axis:
// eviction Close()s a child and deletes its cache directory, so a child a
// request is still using is never a victim. forRequest hands the caller a
// reference for the duration of that request; only IDLE caller-driven children
// are candidates.
func TestStorageManagersKeepAChildWithARequestInFlight(t *testing.T) {
	stubAccountSelection(t, "11111111-1111-4111-8111-111111111111")
	root := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	inFlight := burstAccount(999)
	child, release := root.forRequest(graphclient.WithDestination(t.Context(), inFlight))
	require.NotNil(t, child)
	// Materialize the directory before the burst so its survival below is a
	// statement about eviction rather than about lazy creation.
	require.NoError(t, os.MkdirAll(child.cacheDir, 0o700))

	for n := range graphclient.MaxBoundDestinations + 8 {
		root.ForDestination(graphclient.WithDestination(t.Context(), burstAccount(n)))
	}

	root.mu.Lock()
	held := root.storageManagers[inFlight]
	root.mu.Unlock()
	assert.Same(t, child, held, "a child with a request in flight must never be evicted")
	if child.cacheDir != "" {
		_, statErr := os.Stat(child.cacheDir)
		require.NoError(t, statErr, "an in-flight child's cache directory must not be removed under it")
	}

	// KNOWN POSITIVE: once the request releases it, the same child IS an
	// eviction candidate — so the survival above is the reference's doing and
	// not a destination that could never be evicted.
	release()
	for n := range graphclient.MaxBoundDestinations + 8 {
		root.ForDestination(graphclient.WithDestination(t.Context(), burstAccount(1000+n)))
	}
	root.mu.Lock()
	after := root.storageManagers[inFlight]
	root.mu.Unlock()
	assert.Nil(t, after, "a released caller-driven child is evictable again")
}

func TestStorageNudgesReachRootWithIdentity(t *testing.T) {
	m := NewManager(t.TempDir(), 0)
	t.Cleanup(func() { m.Close() })
	local := graphclient.Destination{Storage: "local"}
	cloud := graphclient.Destination{Storage: "cloud", AccountID: "a"}
	wake := m.ReconcileNudge()
	for _, d := range []graphclient.Destination{local, cloud} {
		m.ForDestination(graphclient.WithDestination(t.Context(), d)).NudgeSegmentDelta(kgtypes.GraphKnowledge, "default")
	}
	select {
	case <-wake:
	default:
		t.Fatal("destination nudges did not wake root reconciler")
	}
	nudges := m.TakeReconcileNudges()
	if len(nudges) != 2 {
		t.Fatalf("nudges=%+v", nudges)
	}
}

func TestStorageFederatedSearchKeepsDuplicateIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := config.WriteSelectedAccountID(path, "account-a"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(path, 0)))
	m := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	destinations := []graphclient.Destination{{Storage: "local"}, {Storage: "cloud", AccountID: "account-a"}}
	for _, d := range destinations {
		ctx := graphclient.WithDestination(t.Context(), d)
		if err := m.ReplaceBucketFields(ctx, kgtypes.GraphKnowledge, "default", nil, []searchengine.Document{{ID: "same", Fields: map[string]string{searchengine.FieldContent: "needle"}}}); err != nil {
			t.Fatal(err)
		}
	}
	ctx := graphclient.WithSearchDestinations(t.Context(), destinations)
	hits, err := m.Search(ctx, kgtypes.GraphKnowledge, "default", "needle", nil, 10)
	if err != nil || len(hits) != 2 {
		t.Fatalf("federation hits=%v err=%v; want two", hits, err)
	}
	seen := map[string]bool{}
	for _, hit := range hits {
		ref, qualified, err := graphclient.ParseReference(hit.ID)
		if err != nil || !qualified || ref.ID != "same" {
			t.Fatalf("reference=%+v qualified=%v err=%v", ref, qualified, err)
		}
		seen[ref.Storage] = true
	}
	if !seen["local"] || !seen["cloud"] {
		t.Fatalf("destinations=%v", seen)
	}
}

func TestStorageFederatedCodeOverlayKeepsBothCopies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := config.WriteSelectedAccountID(path, "a"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(path, 0)))
	m := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	destinations := []graphclient.Destination{{Storage: "local"}, {Storage: "cloud", AccountID: "a"}}
	for _, d := range destinations {
		ctx := graphclient.WithDestination(t.Context(), d)
		if err := m.ReplaceBucketFields(ctx, kgtypes.GraphCode, "repo@topic", nil, []searchengine.Document{{ID: "same", Fields: map[string]string{searchengine.FieldContent: "needle"}}}); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := m.SearchOverlay(graphclient.WithSearchDestinations(t.Context(), destinations), kgtypes.GraphCode, "repo", "repo@topic", "needle", nil, 10)
	if err != nil || len(hits) != 2 {
		t.Fatalf("hits=%v err=%v", hits, err)
	}
	for _, hit := range hits {
		ref, ok, err := graphclient.ParseReference(hit.ID)
		if err != nil || !ok || ref.Repo != "repo" || ref.Branch != "topic" {
			t.Fatalf("ref=%+v err=%v", ref, err)
		}
	}
}
