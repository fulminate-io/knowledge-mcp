// SPDX-License-Identifier: Apache-2.0

package syncgcs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// knowledgeHeaderPrefix is the vendor prefix every client-identity header this
// repo stamps begins with. The fence is stated as a PREFIX rather than as the
// two header names in use today, so a third one added later is inside the
// invariant instead of outside the test.
const knowledgeHeaderPrefix = "X-Knowledge-"

// TestGCSRequests_CarryNoKnowledgeClientHeaders is the scope fence for the
// direct-to-GCS transfers.
//
// These requests go to Google Cloud Storage against a V4 presigned URL and
// never reach the gateway that reads the client-identity headers, so a version
// header here identifies the caller to nobody. They also sit beside a signature
// this file's own doc comment records as broken by an Authorization header,
// which is why the auth transport cannot be reused for them at all. The fence
// keeps a future request constructor in this package from acquiring the stamp
// by habit.
func TestGCSRequests_CarryNoKnowledgeClientHeaders(t *testing.T) {
	var mu sync.Mutex
	var seen []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("payload"))
	}))
	t.Cleanup(srv.Close)

	if err := PutObject(context.Background(), srv.URL+"/obj?X-Goog-Signature=abc", []byte("bytes"), "application/octet-stream"); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if _, err := GetObject(context.Background(), srv.URL+"/obj?X-Goog-Signature=abc"); err != nil {
		t.Fatalf("GetObject: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// KNOWN-POSITIVE CONTROL for an absence assertion: a run in which neither
	// request reached the server would satisfy the "no such header" loop
	// vacuously. Both constructors must have been observed.
	if len(seen) != 2 {
		t.Fatalf("expected PutObject and GetObject to both reach the server, saw %d request(s)", len(seen))
	}
	// SECOND CONTROL, on the MEASUREMENT rather than the traffic: the same
	// instrument that reports the absence must be shown reporting a presence,
	// so a header map that arrived empty for an unrelated reason is
	// distinguishable from a genuinely unstamped request. PutObject sets
	// Content-Type unconditionally and its doc comment records that doing so is
	// REQUIRED, which makes it the right positive.
	if got := seen[0].Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("the header instrument read no Content-Type on the PUT (got %q), so its report of an absent client header proves nothing", got)
	}

	for i, hdr := range seen {
		for name := range hdr {
			if strings.HasPrefix(http.CanonicalHeaderKey(name), knowledgeHeaderPrefix) {
				t.Errorf("request %d to the presigned URL carries %q; these transfers never meet the gateway that reads it", i, name)
			}
		}
		if hdr.Get("Authorization") != "" {
			t.Errorf("request %d carries an Authorization header, which breaks a V4 signed URL", i)
		}
	}
}
