// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// TestRegisteredMemberShipsToTheManagerItsSearchReads is the keyless-BM25 fix's pool-identity invariant:
// for every member the pipeline registers, the ShipManager the registration
// captures is the SAME *segmentdist.Manager a search bound to that member's
// destination resolves. The producer pool IS the consumer pool.
//
// WHY IT DID NOT EXIST BEFORE. #164 gave the CONSUMER side per-destination
// routing and added four tests for it — TestStoragePoolsKeepDuplicateDocuments
// Separate, TestStorageNudgesReachRootWithIdentity, TestStorageFederatedSearch
// KeepsDuplicateIDs, TestStorageFederatedCodeOverlayKeepsBothCopies — and every
// one of them pins per-destination INDEPENDENCE. None pins producer/consumer
// IDENTITY, which is the half #164 did not thread and the defect this ticket
// repairs: the search-path admitter recorded a destination-less Ref, so the
// registration bound nothing and captured the ROOT manager while the search read
// the child at storage-<sha256(storage\0account)>.
//
// NOTHING ON THE SEAM IS A DOUBLE. The Manager is the real one the PRODUCTION
// construction site builds (c.ensureSegmentManager, the same method
// wireRuntimesBackground calls), the admission is the real one a real
// Manager.Search performs through the real admitter closure, the working set is
// the real workingset.Set, the registration is the real Pipeline.RefreshOnceFor
// Boot → refreshOnce → RegisterGraph, and the wire client is the production
// routedWireClient pointed at an unreachable local URL. The ONE thing the test
// supplies is the ctx recorder wrapped around the production resolver expression
// (c.segmentMgr.ForDestination), because the captured manager is otherwise
// unreachable from outside package pipeline — and the resolution on BOTH sides
// of the comparison is still done by that production expression.
//
// BOTH DESTINATIONS THE TICKET NAMES, and neither needs a login: ForDestination
// derives its child directory from sha256(d.Storage + "\x00" + d.AccountID), so a
// signed-in destination is a fixture VALUE. Pinning the identity for local alone
// would leave the signed-in cell — where the defect is MASKED rather than absent,
// because a restart's shutdown drain writes the child — unpinned.
func TestRegisteredMemberShipsToTheManagerItsSearchReads(t *testing.T) {
	for _, tc := range []struct {
		name        string
		destination graphclient.Destination
	}{
		{name: "local logged out", destination: graphclient.Destination{Storage: "local"}},
		{name: "signed in cloud account", destination: graphclient.Destination{Storage: "cloud", AccountID: "acct-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A DESTINATION CARRYING AN ACCOUNT NEEDS THAT ACCOUNT SELECTED, and the
			// selection is NOT a credential: it is the id written in the config, which
			// is all Manager.checkAccountBinding (manager_account_guard.go:51-61) and
			// segmentCacheDirFor consult. Without it the fail-closed account guard
			// refuses the search — correctly, since serving account A's segments to a
			// session bound to B is the thing it exists to prevent — and this row
			// would red on the guard rather than on the pool identity it is about.
			selectAccountForTest(t, tc.destination.AccountID)
			c := newPoolIdentityClient(t)
			searchCtx := graphclient.WithDestination(t.Context(), tc.destination)

			// THE CONSUMER TOUCH, through the real search path: it resolves the
			// destination pool AND records the admission through the production
			// admitter the Manager was constructed with.
			if _, err := c.segmentMgr.Search(searchCtx, kgtypes.GraphKnowledge, "default", "anything", nil, 5); err != nil {
				t.Fatalf("Manager.Search(%+v) returned error: %v", tc.destination, err)
			}
			consumerPool := c.segmentMgr.ForDestination(searchCtx)
			if consumerPool == c.segmentMgr {
				t.Fatalf("fixture check: ForDestination(%+v) resolved the ROOT manager, so the identity "+
					"asserted below would hold vacuously", tc.destination)
			}

			members := c.workingSet.Members()
			if len(members) != 1 {
				t.Fatalf("the search recorded %d working-set members, want exactly 1 — got %+v", len(members), members)
			}
			if got, want := (graphclient.Destination{Storage: members[0].Storage, AccountID: members[0].Account}), tc.destination; got != want {
				t.Fatalf("the search recorded member destination %+v, want %+v — a destination-less Ref is what "+
					"makes the registration bind nothing and capture the root pool", got, want)
			}

			// THE PRODUCER SIDE, through the real registration pass.
			capturedPtr, p := registerFromWorkingSet(t, c)
			captured := *capturedPtr
			if len(captured) != 1 {
				t.Fatalf("the registration pass resolved %d ship managers, want exactly 1 per (graph, destination)", len(captured))
			}
			if captured[0] != pipeline.ShipManager(consumerPool) {
				shipsToRoot := captured[0] == pipeline.ShipManager(c.segmentMgr)
				t.Errorf("the member admitted by a search bound to %+v captured a ship pool that is NOT the pool "+
					"its own search reads (captured_is_the_root_pool=%t) — the producer must ship to the pool the "+
					"same-destination consumer reads", tc.destination, shipsToRoot)
			}

			// AND EXACTLY ONE MEMBER PER (graph, destination): a second refresh pass
			// over the same working set must register nothing new. Two members for one
			// destination is the paired-collector-tick shape the ticket observed.
			p.RefreshOnceForBoot(t.Context())
			if got := len(*capturedPtr); got != 1 {
				t.Errorf("a second registration pass resolved %d ship managers in total, want 1 — "+
					"one destination must own exactly one member", got)
			}
			stopPipelineOrFail(t, p)
		})
	}
}

// selectAccountForTest installs a process-wide account selection reading a
// scratch config, and restores the prior selection on cleanup. An empty id
// installs a selection over an empty config, which is the logged-out state.
func selectAccountForTest(t *testing.T, accountID string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if accountID != "" {
		if err := config.WriteSelectedAccountID(path, accountID); err != nil {
			t.Fatalf("WriteSelectedAccountID(%q, %q) returned error: %v", path, accountID, err)
		}
	}
	t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(path, 0)))
}

// newPoolIdentityClient builds a client whose segment Manager comes from the
// PRODUCTION construction site. A non-nil router is ensureSegmentManager's only
// precondition and NewManager makes no RPC, so a logged-out router pointed at an
// unreachable URL is sufficient and reaches nothing.
func newPoolIdentityClient(t *testing.T) *client {
	c, _ := newPoolIdentityClientWithRoot(t)
	return c
}

// newPoolIdentityClientWithRoot is newPoolIdentityClient plus the L2 cache ROOT
// the Manager was built over, which a row inspecting the pools on disk needs and
// which the Manager exposes no accessor for. It is derived through the production
// helper (segmentCacheDirFor) rather than re-spelled, so a change to where the
// cache roots moves this row with it instead of past it.
func newPoolIdentityClientWithRoot(t *testing.T) (*client, string) {
	t.Helper()
	authState := auth.NewAuthState(newFakeAuthStore(), time.Minute)
	local := graphclient.NewGraphClientForURL("http://local.invalid")
	t.Cleanup(local.Close)
	router := graphclient.NewRouter(local, "http://local.invalid", staticTokenSource{tok: "tok"}, authState)

	c := &client{local: local, router: router, authState: authState, workingSet: workingset.New()}
	graphStorage := t.TempDir()
	c.ensureSegmentManager(graphStorage, 0)
	// Only Manager.Close stops the per-engine merger goroutines the Manager spawns.
	t.Cleanup(c.segmentMgr.Close)
	return c, segmentCacheDirFor(graphStorage)
}

// registerFromWorkingSet drives the real registration pass and returns every
// ship pool it resolved, in order. The returned slice is appended to by the
// resolver the pass calls, so the caller reads it after the pass returns.
//
// The resolver is the production expression from wireStoragePipelineOwners with a
// capture around it; nothing about WHICH pool is resolved is decided here.
func registerFromWorkingSet(t *testing.T, c *client) (*[]pipeline.ShipManager, *pipeline.Pipeline) {
	t.Helper()
	captured := &[]pipeline.ShipManager{}
	p := pipeline.New(pipeline.Config{Tick: time.Hour}, routedWireClient{router: c.router}, nil, nil)
	p.AttachSegmentManager(c.segmentMgr)
	p.AttachWorkingSet(c.workingSet)
	p.AttachStorageOwners(func(ctx context.Context) pipeline.ShipManager {
		mgr := c.segmentMgr.ForDestination(ctx)
		*captured = append(*captured, mgr)
		return mgr
	}, nil)
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), pipelineStopBudget)
		defer cancel()
		_ = p.Stop(stopCtx)
	})
	p.RefreshOnceForBoot(t.Context())
	return captured, p
}

// TestSearchAdmittedMemberShipsVectorsToTheDestinationChild is requirement 5's
// keyed-client cell, and it pins the MOVE rather than denying it.
//
// THE MOVE IS REAL AND MUST BE STATED. One ShipManager serves BOTH arms of a
// member, so binding the search-admitted member to its destination — which is what
// makes the BM25 arm ship where the search reads — moves that member's hnswv3
// writes from the ROOT pool to the destination child too. A row asserting "no
// hnswv3 file moved" would be false, which is exactly why this one asserts the
// opposite and then asserts what genuinely is unchanged.
//
// WHAT IS UNCHANGED, and what this row therefore pins beside the move: the L2
// LAYOUT. Segments still sit at <pool>/hnswv3/<graphtype>/<name>/, the same
// relative shape under whichever pool root; only WHICH root holds them moves. A
// change to that relative layout would orphan an operator's pools silently, and
// nothing else in the tree pins it.
func TestSearchAdmittedMemberShipsVectorsToTheDestinationChild(t *testing.T) {
	selectAccountForTest(t, "")
	c, poolRoot := newPoolIdentityClientWithRoot(t)
	destination := graphclient.Destination{Storage: "local"}
	searchCtx := graphclient.WithDestination(t.Context(), destination)

	if _, err := c.segmentMgr.Search(searchCtx, kgtypes.GraphKnowledge, "default", "anything", nil, 5); err != nil {
		t.Fatalf("Manager.Search returned error: %v", err)
	}
	capturedPtr, p := registerFromWorkingSet(t, c)
	captured := *capturedPtr
	if len(captured) != 1 {
		t.Fatalf("the registration pass resolved %d ship managers, want exactly 1", len(captured))
	}
	t.Cleanup(func() { stopPipelineOrFail(t, p) })

	// THE VECTOR SHIP, through the manager the registration captured — the same
	// ShipManager the BM25 arm ships through, because a member has only one.
	//
	// IT SHIPS UNDER AN UNBOUND ctx, AND THAT IS WHAT MAKES THE ROW DISCRIMINATE.
	// AddAndMarkDirty re-resolves ForDestination from the ctx it is handed
	// (manager_bucket_backlog.go), so shipping under the SEARCH's bound ctx would
	// land in the child however the registration resolved — the row would pass
	// against a producer still capturing the root, which is the defect it exists to
	// observe. Measured: with the admitter reverted to its destination-less form,
	// the bound-ctx version of this row stayed GREEN. Under an unbound ctx the
	// captured manager alone decides, which is also the production shape: a
	// collector's ctx carries a destination only when its member was bound, and a
	// bound member's captured manager is already the child.
	shipCtx := t.Context()
	doc := searchengine.Document{ID: "vec-1", Vector: make([]byte, 32)}
	if err := captured[0].AddAndMarkDirty(shipCtx, kgtypes.GraphKnowledge, "default", []searchengine.Document{doc}); err != nil {
		t.Fatalf("AddAndMarkDirty returned error: %v", err)
	}
	if err := captured[0].Flush(shipCtx, kgtypes.GraphKnowledge, "default"); err != nil {
		t.Fatalf("Flush returned error: %v", err)
	}

	// THE MOVE: the hnswv3 segments are under the DESTINATION child, and the root
	// pool holds none for this graph.
	const layout = "hnswv3/knowledge/default"
	childSegs := segFilesUnder(t, filepath.Join(poolRoot, childPoolName(t, poolRoot), layout))
	rootSegs := segFilesUnder(t, filepath.Join(poolRoot, layout))
	if childSegs == 0 {
		t.Errorf("the registered member's vector ship wrote NO segment under the destination child at %s — "+
			"one ShipManager serves both arms, so binding the member moves its hnswv3 writes here too", layout)
	}
	if rootSegs != 0 {
		t.Errorf("the root pool still holds %d hnswv3 segment(s) at %s for a member bound to %+v — the "+
			"vector writes move WITH the member, and a root-pool write is one no same-destination reader sees",
			rootSegs, layout, destination)
	}
}

// childPoolName returns the single per-destination pool directory name under root.
// It matches the `storage-` prefix rather than the sha256 literal: the derivation
// is an undeclared on-disk contract and hard-coding the digest here would pin it
// by accident in a row that is about something else.
func childPoolName(t *testing.T, root string) string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading the pool root %s: %v", root, err)
	}
	var found []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "storage-") {
			found = append(found, e.Name())
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one per-destination pool under %s, found %v", root, found)
	}
	return found[0]
}

// segFilesUnder counts the sealed segment blobs directly under dir. A missing
// directory counts zero, which is the honest answer for a pool that was never
// written.
func segFilesUnder(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".seg") {
			n++
		}
	}
	return n
}
