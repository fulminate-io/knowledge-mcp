// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
)

// TestSyncTransport_StampsClientVersionAndPlatform proves the /v1/sync/*
// stamping chokepoint carries the client's build identity on BOTH of its wire
// surfaces — the graph push and the control-JSON channel.
//
// Both are asserted separately for the same reason the account-header tests
// are: PushGraph and SyncControlJSON are distinct Transport methods, and a
// stamp applied at one call site rather than at the shared issueBytes would
// cover one and miss the other. The control-JSON leg is also what puts the
// headers on the version-challenge exchange, which rides that same channel.
func TestSyncTransport_StampsClientVersionAndPlatform(t *testing.T) {
	oldVer := clientver.Version
	t.Cleanup(func() { clientver.Version = oldVer })
	clientver.Version = "7.7.7-stamp-test"

	var mu sync.Mutex
	seen := map[string]http.Header{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	tr := accountTestTransport(t, srv, "acct_01STAMPSTAMPSTAMPSTAMPST")

	if err := tr.PushGraph(context.Background(), "knowledge", "default", []byte{0x01}); err != nil {
		t.Fatalf("PushGraph: %v", err)
	}
	if _, err := tr.SyncControlJSON(context.Background(), "presign", []byte(`{}`)); err != nil {
		t.Fatalf("SyncControlJSON: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// KNOWN-POSITIVE CONTROL: without it, a handler that never observed a
	// request (a route change, a transport failure swallowed upstream) would
	// leave the loop below iterating over nothing and reporting a clean green.
	if len(seen) != 2 {
		t.Fatalf("expected both wire surfaces to reach the server, saw paths %v", keysOf(seen))
	}
	for path, hdr := range seen {
		if got := hdr.Get(clientver.HeaderVersion); got != clientver.Version {
			t.Errorf("%s: %s = %q, want %q", path, clientver.HeaderVersion, got, clientver.Version)
		}
		if got := hdr.Get(clientver.HeaderPlatform); got != clientver.Platform() {
			t.Errorf("%s: %s = %q, want %q", path, clientver.HeaderPlatform, got, clientver.Platform())
		}
		// The stamp sits beside the credential; it must not have displaced it.
		if got := hdr.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("%s: Authorization = %q, want %q", path, got, "Bearer tok")
		}
	}
}

func keysOf(m map[string]http.Header) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
