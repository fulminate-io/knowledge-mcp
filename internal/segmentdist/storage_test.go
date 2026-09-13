// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"path/filepath"
	"testing"

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
