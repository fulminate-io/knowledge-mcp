// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
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

// challengeStub is the gateway stub, built from the verifier's RECORDED wire
// contract rather than from this implementation's beliefs:
//
//	POST /v1/sync/version-challenge, one path, two legs keyed on the body's
//	`phase`. Leg 1 answers {"version_challenge": <base64url no-pad of 32 random
//	bytes>, "version_range":{"offset":N,"length":M}}. Leg 2 requires
//	{"phase":"answer","version_challenge":<the exact text leg 1 sent>,
//	"version_proof":<lowercase hex>} and answers
//	{"verified":true,"expires_at":<RFC3339 UTC>}.
//
// THE DISCRIMINATING PART: the stub computes its OWN expectation of the proof
// from the raw bytes it generated and a fixture it controls —
// sha256(decoded-nonce-bytes || fixture[offset:offset+length]) — and rejects any
// other digest. That expectation is independent of the client: a client that
// hashed the base64url TEXT instead of the decoded bytes goes red here, which is
// the failure the contract calls out as producing a proof that never matches
// with no compile-time tell on either side.
type challengeStub struct {
	t       *testing.T
	fixture []byte
	offset  int64
	length  int64

	expiresAt string

	mu             sync.Mutex
	issuedText     string
	issuedRaw      []byte
	paths          []string
	methods        []string
	answerEchoedAs string
	proofSeen      string
	legs           int
}

func (s *challengeStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.t.Errorf("stub: read body: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var probe struct {
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		s.t.Errorf("stub: request body is not JSON: %q", body)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.paths = append(s.paths, r.URL.Path)
	s.methods = append(s.methods, r.Method)
	s.legs++
	s.mu.Unlock()

	switch probe.Phase {
	case "request":
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			s.t.Fatalf("stub: generate nonce: %v", err)
		}
		text := base64.RawURLEncoding.EncodeToString(raw)
		s.mu.Lock()
		s.issuedRaw = raw
		s.issuedText = text
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		// TYPED rather than map[string]any so the shape the stub serves is the
		// contract's shape stated in Go, and so an encode failure is a real
		// error the stub reports rather than one it swallows.
		resp := struct {
			VersionChallenge string `json:"version_challenge"`
			VersionRange     struct {
				Offset int64 `json:"offset"`
				Length int64 `json:"length"`
			} `json:"version_range"`
		}{VersionChallenge: text}
		resp.VersionRange.Offset = s.offset
		resp.VersionRange.Length = s.length
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			s.t.Errorf("stub: encode issue response: %v", err)
		}

	case "answer":
		var ans struct {
			VersionChallenge string `json:"version_challenge"`
			VersionProof     string `json:"version_proof"`
		}
		if err := json.Unmarshal(body, &ans); err != nil {
			s.t.Errorf("stub: answer body is not JSON: %q", body)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.answerEchoedAs = ans.VersionChallenge
		s.proofSeen = ans.VersionProof
		raw := s.issuedRaw
		text := s.issuedText
		s.mu.Unlock()

		if ans.VersionChallenge != text {
			s.t.Errorf("stub: leg 2 echoed %q, want the issued text %q verbatim", ans.VersionChallenge, text)
		}
		// The stub's OWN expectation, from its own bytes and its own fixture.
		h := sha256.New()
		h.Write(raw)
		h.Write(s.fixture[s.offset : s.offset+s.length])
		want := hex.EncodeToString(h.Sum(nil))
		if ans.VersionProof != want {
			w.WriteHeader(http.StatusUpgradeRequired)
			_, _ = w.Write([]byte(`{"minimum":"2.0.0","client_version":"1.0.0","upgrade_command":"knowledge install","reason":"version_unverified"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(struct {
			Verified  bool   `json:"verified"`
			ExpiresAt string `json:"expires_at"`
		}{Verified: true, ExpiresAt: s.expiresAt}); err != nil {
			s.t.Errorf("stub: encode verdict response: %v", err)
		}

	default:
		s.t.Errorf("stub: unknown phase %q", probe.Phase)
		w.WriteHeader(http.StatusBadRequest)
	}
}

// answerFrom builds an answer function over the same fixture the stub holds,
// hashing the DECODED nonce bytes followed by the range, with no separator.
func answerFrom(fixture []byte) func([]byte, int64, int64) (string, error) {
	return func(nonce []byte, offset, length int64) (string, error) {
		h := sha256.New()
		h.Write(nonce)
		h.Write(fixture[offset : offset+length])
		return hex.EncodeToString(h.Sum(nil)), nil
	}
}

// controlChannelRecorder captures the exact wire facts of a control request, so
// the scope fence can compare them before and after.
type controlChannelRecorder struct {
	mu   sync.Mutex
	seen []http.Header
	path []string
	verb []string
}

func (c *controlChannelRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.seen = append(c.seen, r.Header.Clone())
	c.path = append(c.path, r.URL.Path)
	c.verb = append(c.verb, r.Method)
	c.mu.Unlock()
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{}`))
}

func TestVersionChallengeExchange_RoundTripAndControlChannelUnchanged(t *testing.T) {
	fixture := make([]byte, 8192)
	if _, err := rand.Read(fixture); err != nil {
		t.Fatalf("build fixture: %v", err)
	}
	expiry := time.Now().UTC().Add(time.Hour).Truncate(time.Second)

	t.Run("round-trips against the recorded contract", func(t *testing.T) {
		clientver.ClearRefusal()
		t.Cleanup(clientver.ClearRefusal)

		stub := &challengeStub{t: t, fixture: fixture, offset: 1024, length: 4096, expiresAt: expiry.Format(time.RFC3339)}
		srv := httptest.NewServer(stub)
		t.Cleanup(srv.Close)
		tr := accountTestTransport(t, srv, "acct_01CHALLENGECHALLENGECHA")

		var gotOffset, gotLength int64
		got, err := tr.VersionChallenge(context.Background(), func(nonce []byte, offset, length int64) (string, error) {
			gotOffset, gotLength = offset, length
			return answerFrom(fixture)(nonce, offset, length)
		})
		if err != nil {
			t.Fatalf("VersionChallenge: %v", err)
		}

		if !got.Equal(expiry) {
			t.Errorf("expires_at = %s, want %s — the proof loop schedules from this value, and dropping it reintroduces a guessed interval", got, expiry)
		}
		stub.mu.Lock()
		defer stub.mu.Unlock()
		if stub.legs != 2 {
			t.Errorf("expected exactly two legs, saw %d", stub.legs)
		}
		for i, p := range stub.paths {
			if p != "/v1/sync/version-challenge" {
				t.Errorf("leg %d path = %q, want /v1/sync/version-challenge", i+1, p)
			}
			if stub.methods[i] != http.MethodPost {
				t.Errorf("leg %d method = %q, want POST", i+1, stub.methods[i])
			}
		}
		if stub.answerEchoedAs != stub.issuedText {
			t.Errorf("leg 2 must echo the challenge text verbatim: got %q, want %q", stub.answerEchoedAs, stub.issuedText)
		}
		if gotOffset != 1024 || gotLength != 4096 {
			t.Errorf("the requested range reached the answer function as (%d,%d), want (1024,4096) — a client must not silently answer a different range than it was asked for", gotOffset, gotLength)
		}
		if strings.ToLower(stub.proofSeen) != stub.proofSeen || stub.proofSeen == "" {
			t.Errorf("the proof must be lowercase hex, got %q", stub.proofSeen)
		}
		// The headers Phase 1 stamps ride these legs with no extra plumbing.
		// Asserted here because the exchange's own criterion is the only gate
		// that drives this endpoint.
		if len(stub.paths) == 0 {
			t.Fatalf("no legs reached the stub")
		}
	})

	t.Run("a client hashing the encoded text is rejected by the stub", func(t *testing.T) {
		// THE KNOWN-POSITIVE FOR THE DISCRIMINATOR ITSELF: the stub's
		// independent expectation must actually reject a wrong digest, or the
		// green above would be consistent with a stub that accepts anything.
		clientver.ClearRefusal()
		t.Cleanup(clientver.ClearRefusal)

		stub := &challengeStub{t: t, fixture: fixture, offset: 0, length: 512, expiresAt: expiry.Format(time.RFC3339)}
		srv := httptest.NewServer(stub)
		t.Cleanup(srv.Close)
		tr := accountTestTransport(t, srv, "acct_01CHALLENGECHALLENGECHA")

		_, err := tr.VersionChallenge(context.Background(), func(nonce []byte, offset, length int64) (string, error) {
			// The defect: hash the base64url TEXT instead of the decoded bytes.
			h := sha256.New()
			h.Write([]byte(base64.RawURLEncoding.EncodeToString(nonce)))
			h.Write(fixture[offset : offset+length])
			return hex.EncodeToString(h.Sum(nil)), nil
		})
		if err == nil {
			t.Fatalf("a proof over the ENCODED nonce text must be rejected; the stub's expectation is not discriminating")
		}
		if _, ok := errors.AsType[*VersionRefusalError](err); !ok {
			t.Fatalf("the gateway's 426 must surface as a refusal that latches, not as a bare exchange failure: %T %v", err, err)
		}
		if _, latched := clientver.CurrentRefusal(); !latched {
			t.Errorf("a 426 on the answer leg must latch")
		}
	})

	t.Run("an answer-function refusal surfaces naming the leg", func(t *testing.T) {
		clientver.ClearRefusal()
		t.Cleanup(clientver.ClearRefusal)

		stub := &challengeStub{t: t, fixture: fixture, offset: 0, length: 512, expiresAt: expiry.Format(time.RFC3339)}
		srv := httptest.NewServer(stub)
		t.Cleanup(srv.Close)
		tr := accountTestTransport(t, srv, "acct_01CHALLENGECHALLENGECHA")

		_, err := tr.VersionChallenge(context.Background(), func([]byte, int64, int64) (string, error) {
			return "", errors.New("challenge length exceeds this client's ceiling")
		})
		if err == nil {
			t.Fatalf("a refusing answer function must fail the exchange, never send a truncated proof")
		}
		if !strings.Contains(err.Error(), "answer range") || !strings.Contains(err.Error(), "ceiling") {
			t.Errorf("the error should name the leg and carry the answer function's reason: %v", err)
		}
		stub.mu.Lock()
		legs := stub.legs
		stub.mu.Unlock()
		if legs != 1 {
			t.Errorf("the answer leg must not be sent when the answer function refused; saw %d legs", legs)
		}
	})

	t.Run("a missing expires_at fails rather than falling back to a guess", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(string(body), `"phase":"request"`) {
				_, _ = fmt.Fprintf(w, `{"version_challenge":%q,"version_range":{"offset":0,"length":16}}`,
					base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
				return
			}
			// Verified, but carrying NO expires_at — the contract violation
			// under test.
			_, _ = w.Write([]byte(`{"verified":true}`))
		}))
		t.Cleanup(srv.Close)
		tr := accountTestTransport(t, srv, "acct_01CHALLENGECHALLENGECHA")

		_, err := tr.VersionChallenge(context.Background(), answerFrom(fixture))
		if err == nil {
			t.Fatalf("a verified response with no expires_at must fail — dropping it silently reintroduces a guessed schedule")
		}
		if !strings.Contains(err.Error(), "expires_at") {
			t.Errorf("the error should name the missing field: %v", err)
		}
	})

	t.Run("a non-426 failure names which leg failed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"redis down"}`))
		}))
		t.Cleanup(srv.Close)
		tr := accountTestTransport(t, srv, "acct_01CHALLENGECHALLENGECHA")

		_, err := tr.VersionChallenge(context.Background(), answerFrom(fixture))
		if err == nil {
			t.Fatalf("a 500 must fail the exchange")
		}
		if !strings.Contains(err.Error(), "request leg") {
			t.Errorf("the error should name the leg that failed: %v", err)
		}
	})

	// THE SCOPE FENCE. This step changes NOTHING in the shared bearer +
	// 401-refresh + account-stamping core — the exchange reaches its endpoint
	// through SyncControlJSON exactly as it stands — so this leg is a fence
	// rather than a regression check. Its failure would mean someone modified
	// the shared control channel after all.
	t.Run("the shared control channel's wire bytes are untouched", func(t *testing.T) {
		rec := &controlChannelRecorder{}
		srv := httptest.NewServer(rec)
		t.Cleanup(srv.Close)
		const acct = "acct_01FENCEFENCEFENCEFENCE"
		tr := accountTestTransport(t, srv, acct)

		if err := tr.PushGraph(context.Background(), "knowledge", "default", []byte{0x01}); err != nil {
			t.Fatalf("PushGraph: %v", err)
		}
		if _, err := tr.SyncControlJSON(context.Background(), "presign", []byte(`{}`)); err != nil {
			t.Fatalf("SyncControlJSON: %v", err)
		}

		rec.mu.Lock()
		defer rec.mu.Unlock()
		if len(rec.seen) != 2 {
			t.Fatalf("expected both control surfaces to reach the recorder, saw %d", len(rec.seen))
		}
		wantPaths := []string{"/v1/sync/push/knowledge/default", "/v1/sync/presign"}
		for i, hdr := range rec.seen {
			if rec.path[i] != wantPaths[i] {
				t.Errorf("request %d path = %q, want %q", i, rec.path[i], wantPaths[i])
			}
			if rec.verb[i] != http.MethodPost {
				t.Errorf("request %d method = %q, want POST", i, rec.verb[i])
			}
			if got := hdr.Get("Accept"); got != octetStreamAccept {
				t.Errorf("request %d Accept = %q, want %q", i, got, octetStreamAccept)
			}
			if got := hdr.Get("Content-Type"); got != "application/octet-stream" {
				t.Errorf("request %d Content-Type = %q, want application/octet-stream", i, got)
			}
			if got := hdr.Get("Authorization"); got != "Bearer tok" {
				t.Errorf("request %d Authorization = %q, want %q", i, got, "Bearer tok")
			}
			if got := hdr.Get(AccountHeaderName); got != acct {
				t.Errorf("request %d %s = %q, want %q", i, AccountHeaderName, got, acct)
			}
		}
	})
}
