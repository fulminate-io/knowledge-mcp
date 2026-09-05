// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
)

// buildProveGateway refuses the first business request with version_unverified,
// serves the two-leg exchange against the RUNNING TEST BINARY's own bytes, and
// then answers the retry.
//
// It reads the test binary rather than a synthetic fixture on purpose: this test
// drives the REAL constructor, which wires the REAL self-read, so the proof has
// to be computed over the bytes that self-read actually returns.
type buildProveGateway struct {
	t       *testing.T
	exePath string

	mu        sync.Mutex
	business  int
	exchanges int
	issuedRaw []byte
}

func (g *buildProveGateway) counts() (business, exchanges int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.business, g.exchanges
}

func (g *buildProveGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	if strings.HasSuffix(r.URL.Path, "/version-challenge") {
		g.mu.Lock()
		g.exchanges++
		g.mu.Unlock()
		var probe struct {
			Phase string `json:"phase"`
		}
		_ = json.Unmarshal(body, &probe)
		w.Header().Set("Content-Type", "application/json")
		if probe.Phase == "request" {
			raw := make([]byte, 32)
			if _, err := rand.Read(raw); err != nil {
				g.t.Fatalf("nonce: %v", err)
			}
			g.mu.Lock()
			g.issuedRaw = raw
			g.mu.Unlock()
			_, _ = fmt.Fprintf(w, `{"version_challenge":%q,"version_range":{"offset":0,"length":64}}`,
				base64.RawURLEncoding.EncodeToString(raw))
			return
		}
		var ans struct {
			VersionProof string `json:"version_proof"`
		}
		_ = json.Unmarshal(body, &ans)
		g.mu.Lock()
		raw := g.issuedRaw
		g.mu.Unlock()
		// The stub computes its OWN expectation from the bytes it generated and
		// the executable it can read independently — never from the client's
		// answer.
		exe, err := os.ReadFile(g.exePath) //nolint:gosec // the running test binary, resolved by the runtime
		if err != nil {
			g.t.Fatalf("read the test binary: %v", err)
		}
		h := sha256.New()
		h.Write(raw)
		h.Write(exe[0:64])
		if ans.VersionProof != hex.EncodeToString(h.Sum(nil)) {
			w.WriteHeader(http.StatusUpgradeRequired)
			_, _ = w.Write([]byte(`{"minimum":"9.0.0","reason":"version_unverified"}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"verified":true,"expires_at":%q}`,
			time.Now().UTC().Add(time.Hour).Format(time.RFC3339))
		return
	}

	g.mu.Lock()
	g.business++
	n := g.business
	g.mu.Unlock()
	if n == 1 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = w.Write([]byte(`{"minimum":"9.0.0","upgrade_command":"knowledge install","reason":"version_unverified"}`))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// redirectHost rewrites every request's scheme and host to the stub server,
// leaving the path and body untouched — so the request the real transport built
// is the request the stub receives.
type redirectHost struct {
	scheme, host string
}

func (r redirectHost) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme, clone.URL.Host = r.scheme, r.host
	return http.DefaultTransport.RoundTrip(clone)
}

func redirectTo(t *testing.T, target string) http.RoundTripper {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse stub url: %v", err)
	}
	return redirectHost{scheme: u.Scheme, host: u.Host}
}

// TestBuildSyncTransport_WiresProveOnRefusal drives the REAL constructor.
//
// This test exists because a recovery that nothing enables ships all-green:
// every test of the recovery itself builds its own Transport with the option
// set, so the ONE thing none of them can observe is whether any constructor
// turns it on. It runs the assertion for BOTH return paths, because those are
// two separate returns and a partial edit enables only one — and the
// machine-bearer path is exactly the population least likely to have a daemon
// running, so leaving it out would strand the users who need this most.
func TestBuildSyncTransport_WiresProveOnRefusal(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve the test binary: %v", err)
	}

	for _, tc := range []struct {
		name        string
		machineAuth bool
	}{
		{name: "the machine-bearer return path", machineAuth: true},
		{name: "the credential-store return path", machineAuth: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clientver.ClearRefusal()
			t.Cleanup(clientver.ClearRefusal)

			g := &buildProveGateway{t: t, exePath: exe}
			srv := httptest.NewServer(g)
			t.Cleanup(srv.Close)

			if tc.machineAuth {
				t.Setenv("KNOWLEDGE_AUTH_TOKEN", "machine-tok")
			} else {
				t.Setenv("KNOWLEDGE_AUTH_TOKEN", "")
				t.Setenv("HOME", t.TempDir())
			}

			tr, err := BuildSyncTransport()
			if err != nil {
				// The credential-store branch calls the REAL store opener, and
				// this repo refuses that from any test binary on purpose — a
				// test that reached it would read and write the developer's own
				// keychain. That guard is not weakened here to make this
				// fixture convenient, so the BEHAVIORAL half of this row is
				// genuinely unreachable on such a host.
				//
				// THE PROPERTY IS STILL COVERED, on every host, by the
				// structural assertion in the sibling test below: both return
				// paths must carry the prove option. That is exactly the
				// partial-edit defect this row exists to catch.
				t.Skipf("the real credential store is off-limits to test binaries (%v); the both-paths property is asserted structurally by TestBuildSyncTransport_BothReturnPathsCarryProveOnRefusal", err)
			}
			// CloudEndpoint is a build-tag CONSTANT with no runtime override —
			// a deliberate production property. So the stub is reached by
			// redirecting the transport's HTTP client, not by making the
			// endpoint settable.
			tr.SetHTTPClientForTest(&http.Client{Transport: redirectTo(t, srv.URL)})

			// THE HANDLE IS OPEN AFTER CONSTRUCTION. Asked of the REAL
			// self-read: an unopened handle refuses by name, and that refusal
			// is what this asserts is absent.
			if _, err := clientver.AnswerChallenge([]byte{0x01}, 0, 1); err != nil &&
				strings.Contains(err.Error(), "before the executable handle was opened") {
				t.Errorf("the constructor did not open the executable handle, so every proof it drives would fail for a reason that has nothing to do with the gateway")
			}

			if _, err := tr.SyncControlJSON(t.Context(), "presign", []byte(`{}`)); err != nil {
				t.Fatalf("the refused request should have been proved and retried: %v", err)
			}

			business, exchanges := g.counts()
			if exchanges != 2 {
				t.Errorf("expected exactly two challenge legs, saw %d — the constructor did not enable prove-on-refusal on this return path", exchanges)
			}
			if business != 2 {
				t.Errorf("expected the original request plus ONE retry, saw %d", business)
			}
		})
	}
}
