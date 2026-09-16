// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"bytes"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// The local store is OPTIONAL beside a cloud account: a user runs one to keep
// graphs they do not want to share off the cloud, the way a repository can stay
// unpushed. Not running one is a choice, so a signed-in search reads the cloud
// store alone and says nothing about the local one. The cloud store is the
// required half — unreachable or unauthenticated, it still fails the search.
// These tests pin both directions, at the router and over the real MCP endpoint.

func signedInRouter(t *testing.T, localURL, cloudURL string) *Router {
	t.Helper()
	r := NewRouterWithMachineAuth(
		closeIdleOnCleanup(t, NewGraphClientForURL(localURL)),
		cloudURL, auth.StaticTokenSource{}, nil, true,
	)
	t.Cleanup(r.Close)
	return r
}

// TestStorageSearchBindsCloudAloneWhenLocalStoreIsNotRunning is requirement
// 7(a): with the local leg off the air and a live cloud engine, BindSearch
// binds the cloud destination only, returns no error, and the fan-out reads
// cloud alone.
func TestStorageSearchBindsCloudAloneWhenLocalStoreIsNotRunning(t *testing.T) {
	selectCloudAccountForTest(t, "a")
	localURL, local, stopLocal := startHealthyCountingEngine(t)
	cloudURL, cloud, _ := startHealthyCountingEngine(t)
	stopLocal()
	r := signedInRouter(t, localURL, cloudURL)

	ctx, err := r.BindSearch(WithOperation(t.Context(), OperationForTool("search")))
	if err != nil {
		t.Fatalf("BindSearch() error = %v, want nil: the local store not running is a choice, not a fault", err)
	}
	want := []Destination{{Storage: "cloud", AccountID: "a"}}
	if got := SearchDestinations(ctx); !slices.Equal(got, want) {
		t.Fatalf("SearchDestinations() = %+v, want %+v", got, want)
	}
	if _, err := r.Execute(ctx, graphNamesQuery()); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if local.execute.Load() != 0 || cloud.execute.Load() != 1 {
		t.Fatalf("executes: local=%d cloud=%d, want local=0 cloud=1",
			local.execute.Load(), cloud.execute.Load())
	}
}

// TestStorageSearchBindsBothStoresWhenLocalStoreIsRunning is requirement 7(e),
// the control: nothing about the both-stores path changes when the local store
// IS running.
func TestStorageSearchBindsBothStoresWhenLocalStoreIsRunning(t *testing.T) {
	selectCloudAccountForTest(t, "a")
	localURL, local, _ := startHealthyCountingEngine(t)
	cloudURL, cloud, _ := startHealthyCountingEngine(t)
	r := signedInRouter(t, localURL, cloudURL)

	ctx, err := r.BindSearch(WithOperation(t.Context(), OperationForTool("search")))
	if err != nil {
		t.Fatalf("BindSearch() error = %v, want nil", err)
	}
	want := []Destination{{Storage: "local"}, {Storage: "cloud", AccountID: "a"}}
	if got := SearchDestinations(ctx); !slices.Equal(got, want) {
		t.Fatalf("SearchDestinations() = %+v, want %+v", got, want)
	}
	if _, err := r.Execute(ctx, graphNamesQuery()); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if local.execute.Load() != 1 || cloud.execute.Load() != 1 {
		t.Fatalf("executes: local=%d cloud=%d, want local=1 cloud=1",
			local.execute.Load(), cloud.execute.Load())
	}
}

// TestStorageSearchStillRequiresCloudWhenOnlyLocalIsHealthy is requirement 5's
// unambiguous row. TestStorageFederationRequiresCloudEvenWithoutHits points
// BOTH legs at a dead address, so it cannot say which leg refused; this one
// keeps the local leg healthy, leaving the cloud leg as the only possible
// source of the error.
func TestStorageSearchStillRequiresCloudWhenOnlyLocalIsHealthy(t *testing.T) {
	selectCloudAccountForTest(t, "a")
	localURL, _, _ := startHealthyCountingEngine(t)
	cloudURL, _, stopCloud := startHealthyCountingEngine(t)
	stopCloud()
	r := signedInRouter(t, localURL, cloudURL)

	_, err := r.BindSearch(WithOperation(t.Context(), OperationForTool("search")))
	if err == nil {
		t.Fatal("BindSearch() error = nil, want an error: the cloud store is required when signed in")
	}
	if !strings.Contains(err.Error(), "cloud storage unavailable; search cannot be complete") {
		t.Fatalf("BindSearch() error = %v, want it to name the unavailable cloud store", err)
	}
}

// TestStorageSearchFailsWhenCloudRejectsTheCredential pins the owner's rule
// that "auth failures should always be fatal and never fail open". The cloud
// leg here is REACHABLE and refuses the credential (CodeUnauthenticated), which
// is a different input class from the unreachable leg every other row drives —
// and it is fatal whether or not the optional local store is running, so
// neither subtest can fall through to a local-only search.
func TestStorageSearchFailsWhenCloudRejectsTheCredential(t *testing.T) {
	for _, arm := range []struct {
		name      string
		localDown bool
	}{
		{name: "local store running", localDown: false},
		{name: "local store not running", localDown: true},
	} {
		t.Run(arm.name, func(t *testing.T) {
			selectCloudAccountForTest(t, "a")
			localURL, _, stopLocal := startHealthyCountingEngine(t)
			cloudURL, _, _ := startUnauthenticatedCountingEngine(t)
			if arm.localDown {
				stopLocal()
			}
			r := signedInRouter(t, localURL, cloudURL)

			_, err := r.BindSearch(WithOperation(t.Context(), OperationForTool("search")))
			if err == nil {
				t.Fatal("BindSearch() error = nil, want an error: a rejected cloud credential must never fail open")
			}
			if !strings.Contains(err.Error(), "cloud storage unavailable; search cannot be complete") {
				t.Fatalf("BindSearch() error = %v, want it to name the cloud store it could not authenticate to", err)
			}
			if code := connect.CodeOf(err); code != connect.CodeUnauthenticated {
				t.Fatalf("connect.CodeOf(BindSearch() error) = %v, want %v: the auth code must survive the wrap",
					code, connect.CodeUnauthenticated)
			}
		})
	}
}

// TestStorageSearchOverMCPMatchesCloudEnvelopeWhenLocalIsDown is requirements
// 7(b) and 3: over the real MCP endpoint, with the local store down, an
// unqualified search returns the SAME envelope, byte for byte, that an explicit
// storage:"cloud" search returns — no note, no warning, isError false — for the
// text arm and the hybrid arm alike.
func TestStorageSearchOverMCPMatchesCloudEnvelopeWhenLocalIsDown(t *testing.T) {
	for _, arm := range []struct {
		name string
		args map[string]any
	}{
		{name: "text", args: map[string]any{"query": "anything", "graph": "knowledge"}},
		{name: "hybrid", args: map[string]any{"query": "anything", "graph": "knowledge", "mode": "hybrid"}},
	} {
		t.Run(arm.name, func(t *testing.T) {
			s := newSignedInMCPSearchStack(t)
			s.stopLocal()

			unqualified := s.callResult(t, "search", arm.args)
			qualified := s.callResult(t, "search", withStorage(arm.args, "cloud"))
			if !bytes.Equal(unqualified, qualified) {
				t.Fatalf("search %s: unqualified result = %s, want the storage:\"cloud\" result %s",
					arm.name, unqualified, qualified)
			}
			text, isError := s.callText(t, "search", arm.args)
			if isError {
				t.Fatalf("search %s: isError = true, want false; text = %q", arm.name, text)
			}
			if !strings.Contains(text, "search_destinations=[{Storage:cloud AccountID:probe-account}]") {
				t.Fatalf("search %s: text = %q, want the cloud destination alone", arm.name, text)
			}
			if s.local.execute.Load() != 0 {
				t.Fatalf("search %s: local.execute = %d, want 0", arm.name, s.local.execute.Load())
			}
			if s.cloud.execute.Load() == 0 {
				t.Fatalf("search %s: cloud.execute = 0, want the cloud store read", arm.name)
			}
		})
	}
}

// TestStorageQueryOverMCPMatchesCloudEnvelopeWhenLocalIsDown is requirement
// 7(c): the text-shaped query arms are the only other tool that reaches the
// preflight, and they answer exactly as the search arms do.
func TestStorageQueryOverMCPMatchesCloudEnvelopeWhenLocalIsDown(t *testing.T) {
	for _, arm := range []struct {
		name string
		args map[string]any
	}{
		{name: "text", args: map[string]any{"text": "anything"}},
		{name: "queries", args: map[string]any{"queries": []any{"a", "b"}}},
	} {
		t.Run(arm.name, func(t *testing.T) {
			s := newSignedInMCPSearchStack(t)
			s.stopLocal()

			unqualified := s.callResult(t, "query", arm.args)
			qualified := s.callResult(t, "query", withStorage(arm.args, "cloud"))
			if !bytes.Equal(unqualified, qualified) {
				t.Fatalf("query %s: unqualified result = %s, want the storage:\"cloud\" result %s",
					arm.name, unqualified, qualified)
			}
			text, isError := s.callText(t, "query", arm.args)
			if isError {
				t.Fatalf("query %s: isError = true, want false; text = %q", arm.name, text)
			}
			if s.local.execute.Load() != 0 {
				t.Fatalf("query %s: local.execute = %d, want 0", arm.name, s.local.execute.Load())
			}
		})
	}
}

// TestStorageExplicitLocalSearchStillErrorsWhenLocalIsDown is requirement 4: a
// user who ASKS for the local store with it down is told so, and an explicit
// cloud search is unaffected.
func TestStorageExplicitLocalSearchStillErrorsWhenLocalIsDown(t *testing.T) {
	s := newSignedInMCPSearchStack(t)
	s.stopLocal()

	text, isError := s.callText(t, "search", map[string]any{"query": "anything", "storage": "local"})
	if !isError {
		t.Fatalf("search storage:\"local\": isError = false, want true; text = %q", text)
	}
	want := "knowledge-server not reachable on port " + strconv.Itoa(mcpSearchStackPort)
	if !strings.Contains(text, want) {
		t.Fatalf("search storage:\"local\": text = %q, want it to contain %q", text, want)
	}
	if text, isError := s.callText(t, "search", map[string]any{"query": "anything", "storage": "cloud"}); isError {
		t.Fatalf("search storage:\"cloud\": isError = true, want false; text = %q", text)
	}
}

// TestStorageLoggedOutSearchUnchangedWhenLocalIsDown is requirement 6: a
// logged-out (OSS) client has no cloud leg, so BindSearch returns its context
// untouched and the local store stays REQUIRED — up it answers, down it is the
// EnsureServer error, exactly as before.
func TestStorageLoggedOutSearchUnchangedWhenLocalIsDown(t *testing.T) {
	s := newLoggedOutMCPSearchStack(t)

	text, isError := s.callText(t, "search", map[string]any{"query": "anything", "graph": "knowledge"})
	if isError {
		t.Fatalf("logged-out search with the local store up: isError = true, want false; text = %q", text)
	}
	if s.local.execute.Load() != 1 {
		t.Fatalf("logged-out search with the local store up: local.execute = %d, want 1", s.local.execute.Load())
	}

	s.stopLocal()
	text, isError = s.callText(t, "search", map[string]any{"query": "anything", "graph": "knowledge"})
	if !isError {
		t.Fatalf("logged-out search with the local store down: isError = false, want true; text = %q", text)
	}
	want := "knowledge-server not reachable on port " + strconv.Itoa(mcpSearchStackPort)
	if !strings.Contains(text, want) {
		t.Fatalf("logged-out search with the local store down: text = %q, want it to contain %q", text, want)
	}
}

// withStorage copies args with an explicit storage argument added, so the two
// halves of an envelope comparison differ in exactly that one argument.
func withStorage(args map[string]any, storage string) map[string]any {
	qualified := make(map[string]any, len(args)+1)
	maps.Copy(qualified, args)
	qualified["storage"] = storage
	return qualified
}
