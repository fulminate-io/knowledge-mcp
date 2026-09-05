// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
)

// proveGateway is a stub gateway built from the frozen wire contract: it
// refuses the FIRST business request with a configurable reason, serves the
// two-leg challenge exchange, and then answers the retried business request.
type proveGateway struct {
	t       *testing.T
	fixture []byte
	reason  string

	mu           sync.Mutex
	businessHits int
	exchanges    int
	seenBodies   [][]byte
	seenPaths    []string
	seenMethods  []string
	issuedRaw    []byte
	issuedText   string
}

func (g *proveGateway) counts() (business, exchanges int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.businessHits, g.exchanges
}

func (g *proveGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	if strings.HasSuffix(r.URL.Path, "/version-challenge") {
		g.mu.Lock()
		g.exchanges++
		g.mu.Unlock()
		g.serveExchange(w, body)
		return
	}

	g.mu.Lock()
	g.businessHits++
	n := g.businessHits
	g.seenBodies = append(g.seenBodies, body)
	g.seenPaths = append(g.seenPaths, r.URL.Path)
	g.seenMethods = append(g.seenMethods, r.Method)
	g.mu.Unlock()

	if n == 1 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = fmt.Fprintf(w,
			`{"minimum":"9.0.0","client_version":"1.0.0","upgrade_command":"knowledge install","reason":%q}`, g.reason)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (g *proveGateway) serveExchange(w http.ResponseWriter, body []byte) {
	var probe struct {
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		g.t.Errorf("exchange body is not JSON: %q", body)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch probe.Phase {
	case "request":
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			g.t.Fatalf("nonce: %v", err)
		}
		g.mu.Lock()
		g.issuedRaw, g.issuedText = raw, base64.RawURLEncoding.EncodeToString(raw)
		text := g.issuedText
		g.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"version_challenge":%q,"version_range":{"offset":0,"length":64}}`, text)
	case "answer":
		var ans struct {
			VersionProof string `json:"version_proof"`
		}
		_ = json.Unmarshal(body, &ans)
		g.mu.Lock()
		raw := g.issuedRaw
		g.mu.Unlock()
		// The stub's OWN expectation, from its own bytes and its own fixture.
		h := sha256.New()
		h.Write(raw)
		h.Write(g.fixture[0:64])
		if ans.VersionProof != hex.EncodeToString(h.Sum(nil)) {
			w.WriteHeader(http.StatusUpgradeRequired)
			_, _ = w.Write([]byte(`{"minimum":"9.0.0","reason":"version_unverified"}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"verified":true,"expires_at":%q}`, time.Now().UTC().Add(time.Hour).Format(time.RFC3339))
	default:
		g.t.Errorf("unknown phase %q", probe.Phase)
		w.WriteHeader(http.StatusBadRequest)
	}
}

// proveAnswerOver builds an answer function over a fixture the test controls.
func proveAnswerOver(fixture []byte) func([]byte, int64, int64) (string, error) {
	return func(nonce []byte, offset, length int64) (string, error) {
		h := sha256.New()
		h.Write(nonce)
		h.Write(fixture[offset : offset+length])
		return hex.EncodeToString(h.Sum(nil)), nil
	}
}

// TestSyncTransport_ProvesOnUnverifiedRefusalAndRetriesOnce drives the whole
// decision table.
func TestSyncTransport_ProvesOnUnverifiedRefusalAndRetriesOnce(t *testing.T) {
	fixture := make([]byte, 4096)
	if _, err := rand.Read(fixture); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	newTransport := func(t *testing.T, srv *httptest.Server, answer func([]byte, int64, int64) (string, error)) *Transport {
		t.Helper()
		sel := NewAccountSelection(seedSelection(t, t.TempDir(), "acct_01PROVEPROVEPROVEPROVE"), time.Second)
		src := StaticTokenSource{AccessToken: "tok"}
		return NewSyncTransport(srv.URL, src, WithAccountSelection(sel), WithProveOnRefusal(answer))
	}

	t.Run("version_unverified proves once and retries the ORIGINAL request", func(t *testing.T) {
		clientver.ClearRefusal()
		t.Cleanup(clientver.ClearRefusal)

		g := &proveGateway{t: t, fixture: fixture, reason: clientver.ReasonUnverified}
		srv := httptest.NewServer(g)
		t.Cleanup(srv.Close)
		tr := newTransport(t, srv, proveAnswerOver(fixture))

		out, err := tr.SyncControlJSON(t.Context(), "presign", []byte(`{"payload":"original"}`))
		if err != nil {
			t.Fatalf("the retried request should have succeeded: %v", err)
		}
		if string(out) != `{"ok":true}` {
			t.Errorf("the caller received %q, not the retry's success body", out)
		}

		business, exchanges := g.counts()
		if exchanges != 2 {
			t.Errorf("expected exactly two challenge legs, saw %d", exchanges)
		}
		if business != 2 {
			t.Errorf("expected the original request plus ONE retry, saw %d business requests", business)
		}

		// The retry re-sends the IDENTICAL request.
		g.mu.Lock()
		defer g.mu.Unlock()
		if string(g.seenBodies[0]) != string(g.seenBodies[1]) {
			t.Errorf("the retry changed the body: %q then %q", g.seenBodies[0], g.seenBodies[1])
		}
		if g.seenPaths[0] != g.seenPaths[1] || g.seenMethods[0] != g.seenMethods[1] {
			t.Errorf("the retry changed the route: %s %s then %s %s",
				g.seenMethods[0], g.seenPaths[0], g.seenMethods[1], g.seenPaths[1])
		}
		if _, latched := clientver.CurrentRefusal(); latched {
			t.Errorf("a successful proof must clear the latch")
		}
	})

	// EVERY OTHER REASON PASSES THROUGH VERBATIM. The body-restoration property
	// is asserted right here: if the peeked body were consumed and not put
	// back, the error would name no minimum and read as unparseable.
	for _, reason := range []string{
		clientver.ReasonBelowMinimum,
		clientver.ReasonUnprovable,
		"a_reason_this_repo_has_never_heard_of",
	} {
		t.Run("reason "+reason+" triggers NO exchange and keeps its remedy", func(t *testing.T) {
			clientver.ClearRefusal()
			t.Cleanup(clientver.ClearRefusal)

			g := &proveGateway{t: t, fixture: fixture, reason: reason}
			srv := httptest.NewServer(g)
			t.Cleanup(srv.Close)
			tr := newTransport(t, srv, proveAnswerOver(fixture))

			_, err := tr.SyncControlJSON(t.Context(), "presign", []byte(`{}`))
			if err == nil {
				t.Fatalf("a refusal this client cannot repair must surface")
			}
			business, exchanges := g.counts()
			if exchanges != 0 {
				t.Errorf("reason %q triggered %d challenge legs; proving on it burns a round trip and still fails", reason, exchanges)
			}
			if business != 1 {
				t.Errorf("reason %q was retried (%d business requests)", reason, business)
			}
			// THE BODY-RESTORATION ASSERTION.
			for _, want := range []string{"9.0.0", "knowledge install"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the surfaced refusal omits %q — the peeked body was consumed and not put back, so the real reason and minimum were lost: %v", want, err)
				}
			}
			got, latched := clientver.CurrentRefusal()
			if !latched {
				t.Fatalf("the refusal must still latch")
			}
			if got.Reason != reason {
				t.Errorf("latched reason = %q, want %q verbatim", got.Reason, reason)
			}
		})
	}

	t.Run("a FAILED proof surfaces the refusal rather than retrying again", func(t *testing.T) {
		clientver.ClearRefusal()
		t.Cleanup(clientver.ClearRefusal)

		g := &proveGateway{t: t, fixture: fixture, reason: clientver.ReasonUnverified}
		srv := httptest.NewServer(g)
		t.Cleanup(srv.Close)
		// An answer function that cannot produce a valid proof.
		tr := newTransport(t, srv, func([]byte, int64, int64) (string, error) {
			return "", errors.New("no executable handle")
		})

		_, err := tr.SyncControlJSON(t.Context(), "presign", []byte(`{}`))
		if err == nil {
			t.Fatalf("a failed proof must surface the gateway's refusal")
		}
		business, _ := g.counts()
		if business != 1 {
			t.Errorf("a failed proof retried anyway (%d business requests); the honest end state is the refusal, not a loop", business)
		}
		p, ok := clientver.LastProof()
		if !ok || p.OK {
			t.Errorf("a failed proof must be recorded as failed: ok=%v %+v", ok, p)
		}
	})

	t.Run("a SECOND unverified refusal on the same Transport triggers no second attempt", func(t *testing.T) {
		clientver.ClearRefusal()
		t.Cleanup(clientver.ClearRefusal)

		// This gateway refuses EVERY business request with version_unverified,
		// so only the single-attempt guard can stop a loop.
		var mu sync.Mutex
		business, exchanges := 0, 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/version-challenge") {
				mu.Lock()
				exchanges++
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				// Refuse the exchange too: this is the recursion probe. The
				// challenge legs ride the SAME transport, so a guard consumed
				// AFTER the exchange would re-enter here.
				w.WriteHeader(http.StatusUpgradeRequired)
				_, _ = w.Write([]byte(`{"minimum":"9.0.0","reason":"version_unverified"}`))
				return
			}
			mu.Lock()
			business++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUpgradeRequired)
			_, _ = w.Write([]byte(`{"minimum":"9.0.0","upgrade_command":"knowledge install","reason":"version_unverified"}`))
		}))
		t.Cleanup(srv.Close)
		tr := newTransport(t, srv, proveAnswerOver(fixture))

		for range 3 {
			_, _ = tr.SyncControlJSON(t.Context(), "presign", []byte(`{}`))
		}

		mu.Lock()
		defer mu.Unlock()
		if exchanges > 1 {
			t.Errorf("the challenge ran %d times; at most ONE attempt per Transport, and the challenge legs must not re-enter the recovery", exchanges)
		}
		if business > 4 {
			t.Errorf("%d business requests across three calls — the recovery is looping", business)
		}
	})
}
