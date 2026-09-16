// SPDX-License-Identifier: Apache-2.0

package providercheck

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// fixtureKey is the pasted credential every test uses. It is a fixture
// string, never a real credential, and no assertion below prints it except
// the redaction tests, which assert it is ABSENT.
const fixtureKey = "providercheck-fixture-key"

// listServer answers a list-models read with status and body, recording the
// request it saw so a test can prove the call reached THIS endpoint with
// THESE headers rather than a vendor default.
func listServer(t *testing.T, hits *atomic.Int32, status int, body string) (*httptest.Server, *http.Header, *string) {
	t.Helper()
	var header http.Header
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		header = r.Header.Clone()
		query = r.URL.RequestURI()
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write stub body: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &header, &query
}

func TestCheckAPIAcceptsAndListsModels(t *testing.T) {
	for _, tc := range []struct {
		name      string
		provider  config.Provider
		body      string
		wantFirst string
		assert    func(t *testing.T, header http.Header, uri string)
	}{
		{
			name:      "OpenAI",
			provider:  config.ProviderOpenAI,
			body:      `{"data":[{"id":"gpt-5-mini"},{"id":"gpt-5"}]}`,
			wantFirst: "gpt-5-mini",
			assert: func(t *testing.T, header http.Header, uri string) {
				if got := header.Get("Authorization"); got != "Bearer "+fixtureKey {
					t.Errorf("Authorization header carried a %d-byte value; want the bearer form of the pasted key", len(got))
				}
				if !strings.HasPrefix(uri, "/v1/models") {
					t.Errorf("request URI = %q, want the /v1/models read", uri)
				}
			},
		},
		{
			name:      "Anthropic",
			provider:  config.ProviderAnthropic,
			body:      `{"data":[{"id":"claude-fable-5-1"}]}`,
			wantFirst: "claude-fable-5-1",
			assert: func(t *testing.T, header http.Header, _ string) {
				if got := header.Get("X-Api-Key"); got != fixtureKey {
					t.Errorf("X-Api-Key carried a %d-byte value; want the pasted key verbatim", len(got))
				}
				if got := header.Get("Anthropic-Version"); got != anthropicVersion {
					t.Errorf("Anthropic-Version = %q, want %q", got, anthropicVersion)
				}
				if header.Get("Authorization") != "" {
					t.Errorf("anthropic must not receive an Authorization header; it authenticates with X-Api-Key")
				}
			},
		},
		{
			name:      "Gemini",
			provider:  config.ProviderGemini,
			body:      `{"models":[{"name":"models/gemini-3-flash"},{"name":"models/gemini-3-pro"}]}`,
			wantFirst: "gemini-3-flash",
			assert: func(t *testing.T, header http.Header, uri string) {
				if !strings.Contains(uri, "key="+fixtureKey) {
					t.Errorf("request URI did not carry the key query parameter; got %d bytes of URI", len(uri))
				}
				if header.Get("Authorization") != "" || header.Get("X-Api-Key") != "" {
					t.Errorf("gemini must authenticate by query parameter alone, not by header")
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv, header, uri := listServer(t, &hits, http.StatusOK, tc.body)
			got, err := Check(t.Context(), Request{Provider: tc.provider, Key: fixtureKey, BaseURL: srv.URL})
			if err != nil {
				t.Fatalf("Check(%s) returned an error: %v", tc.provider, err)
			}
			if got.Outcome != Accepted {
				t.Fatalf("Check(%s).Outcome = %q (%s), want %q", tc.provider, got.Outcome, got.Reason, Accepted)
			}
			if len(got.Models) == 0 || got.Models[0] != tc.wantFirst {
				t.Errorf("Check(%s).Models = %v, want it to begin with %q", tc.provider, got.Models, tc.wantFirst)
			}
			if hits.Load() != 1 {
				t.Errorf("the stub saw %d requests; want exactly 1", hits.Load())
			}
			tc.assert(t, *header, *uri)
		})
	}
}

func TestCheckAPIClassifiesTheProvidersAnswer(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		want       Outcome
		wantReason string
		wantModels int
	}{
		{name: "Rejected401", status: http.StatusUnauthorized, body: `{"error":"invalid api key"}`, want: Rejected, wantReason: "invalid api key"},
		{name: "Rejected403", status: http.StatusForbidden, body: `{"error":"forbidden"}`, want: Rejected, wantReason: "forbidden"},
		{name: "RateLimited429", status: http.StatusTooManyRequests, body: `slow down`, want: RateLimited, wantReason: "rate-limiting"},
		{name: "Unreachable500", status: http.StatusInternalServerError, body: `boom`, want: Unreachable, wantReason: "HTTP 500"},
		{name: "Unreachable404", status: http.StatusNotFound, body: `no such route`, want: Unreachable, wantReason: "HTTP 404"},
		{name: "UnreadableListStillAccepted", status: http.StatusOK, body: `not json at all`, want: Accepted, wantReason: "model list could not be read"},
		{name: "WrongKindListStillAccepted", status: http.StatusOK, body: `{"data":["gpt-5","gpt-5-mini"]}`, want: Accepted, wantReason: "model list could not be read"},
		{name: "EmptyListAccepted", status: http.StatusOK, body: `{"data":[]}`, want: Accepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv, _, _ := listServer(t, &hits, tc.status, tc.body)
			got, err := Check(t.Context(), Request{Provider: config.ProviderOpenAI, Key: fixtureKey, BaseURL: srv.URL})
			if err != nil {
				t.Fatalf("Check returned an error: %v", err)
			}
			if got.Outcome != tc.want {
				t.Errorf("Check().Outcome = %q (%s), want %q", got.Outcome, got.Reason, tc.want)
			}
			if tc.wantReason != "" && !strings.Contains(got.Reason, tc.wantReason) {
				t.Errorf("Check().Reason = %q, want it to contain %q", got.Reason, tc.wantReason)
			}
			if len(got.Models) != tc.wantModels {
				t.Errorf("Check().Models = %v, want %d entries", got.Models, tc.wantModels)
			}
			if hits.Load() != 1 {
				t.Errorf("the stub saw %d requests; want exactly 1", hits.Load())
			}
		})
	}
}

func TestCheckAPIUnreachableEndpointIsNotARejectedKey(t *testing.T) {
	var hits atomic.Int32
	srv, _, _ := listServer(t, &hits, http.StatusOK, `{"data":[{"id":"gpt-5"}]}`)
	url := srv.URL
	srv.Close()
	got, err := Check(t.Context(), Request{Provider: config.ProviderOpenAI, Key: fixtureKey, BaseURL: url})
	if err != nil {
		t.Fatalf("Check returned an error: %v", err)
	}
	if got.Outcome != Unreachable {
		t.Errorf("Check() against a closed listener = %q (%s), want %q", got.Outcome, got.Reason, Unreachable)
	}
	if hits.Load() != 0 {
		t.Errorf("the closed stub saw %d requests; want 0", hits.Load())
	}
}

// TestCheckAPIWithNoKeyFailsClosed is the fail-closed guard. The control is
// the second subtest: the SAME endpoint is reached when a key is present,
// which is what proves the zero above is a call that never happened rather
// than a stub that never counts.
//
// HAZARD FOR ANYONE MUTATING THE GUARD: the keyless request names no base
// URL, because "no key and no endpoint" is the cell under test. With the
// guard in place nothing is called. With the guard REMOVED the request
// resolves to the provider's real host, so a mutation run of this test
// issues one unauthenticated GET to api.openai.com — no credential, but a
// third-party contact. Restore the guard before re-running the package.
func TestCheckAPIWithNoKeyFailsClosed(t *testing.T) {
	var hits atomic.Int32
	srv, _, _ := listServer(t, &hits, http.StatusOK, `{"data":[{"id":"gpt-5"}]}`)

	got, err := Check(t.Context(), Request{Provider: config.ProviderOpenAI})
	if err != nil {
		t.Fatalf("Check returned an error: %v", err)
	}
	if got.Outcome != Rejected {
		t.Errorf("Check() with no key and no base_url = %q (%s), want %q", got.Outcome, got.Reason, Rejected)
	}
	if !strings.Contains(got.Reason, "no API key was provided") {
		t.Errorf("Check().Reason = %q, want it to name the missing key", got.Reason)
	}
	if hits.Load() != 0 {
		t.Errorf("the keyless check issued %d requests; want 0 — it must refuse before calling", hits.Load())
	}

	t.Run("ControlKeyPresentReachesEndpoint", func(t *testing.T) {
		got, err := Check(t.Context(), Request{Provider: config.ProviderOpenAI, Key: fixtureKey, BaseURL: srv.URL})
		if err != nil {
			t.Fatalf("Check returned an error: %v", err)
		}
		if got.Outcome != Accepted || hits.Load() != 1 {
			t.Errorf("control: Check() = %q with %d hits, want %q with 1 hit", got.Outcome, hits.Load(), Accepted)
		}
	})
}

// TestCheckAPIKeylessBaseURLIsAllowed covers the one legitimate keyless
// shape: a local compatible server that needs no credential, which the
// configuration validator admits under its key-OR-base_url rule.
func TestCheckAPIKeylessBaseURLIsAllowed(t *testing.T) {
	var hits atomic.Int32
	srv, header, _ := listServer(t, &hits, http.StatusOK, `{"data":[{"id":"local-model"}]}`)
	got, err := Check(t.Context(), Request{Provider: config.ProviderOpenAI, BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("Check returned an error: %v", err)
	}
	if got.Outcome != Accepted || len(got.Models) != 1 {
		t.Fatalf("Check() against a keyless endpoint = %q (%s) with models %v, want %q with one model", got.Outcome, got.Reason, got.Models, Accepted)
	}
	if (*header).Get("Authorization") != "" {
		t.Errorf("a keyless check must send no Authorization header")
	}
}

// TestCheckAPIReasonNeverCarriesTheKey is the credential-handling guard: a
// provider that echoes the offending key in its error body must not have
// that value rendered back to the user.
func TestCheckAPIReasonNeverCarriesTheKey(t *testing.T) {
	var hits atomic.Int32
	srv, _, _ := listServer(t, &hits, http.StatusUnauthorized, `{"error":"key `+fixtureKey+` is not valid"}`)
	got, err := Check(t.Context(), Request{Provider: config.ProviderOpenAI, Key: fixtureKey, BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("Check returned an error: %v", err)
	}
	if got.Outcome != Rejected {
		t.Fatalf("Check().Outcome = %q, want %q", got.Outcome, Rejected)
	}
	if strings.Contains(got.Reason, fixtureKey) {
		t.Errorf("Check().Reason echoed the pasted credential back; the reason is %d bytes and must carry none of it", len(got.Reason))
	}
	if !strings.Contains(got.Reason, "[redacted]") {
		t.Errorf("Check().Reason = %q, want the echoed credential replaced by the redaction marker", got.Reason)
	}
}

func TestSanitizeReason(t *testing.T) {
	t.Run("RemovesEveryOccurrence", func(t *testing.T) {
		got := SanitizeReason("bad "+fixtureKey+" and again "+fixtureKey, fixtureKey)
		if strings.Contains(got, fixtureKey) {
			t.Errorf("SanitizeReason left the credential in a %d-byte result", len(got))
		}
		if strings.Count(got, "[redacted]") != 2 {
			t.Errorf("SanitizeReason(...) = %q, want both occurrences replaced", got)
		}
	})
	t.Run("BoundsProviderText", func(t *testing.T) {
		got := SanitizeReason(strings.Repeat("x", 4096), "")
		if len(got) > reasonMaxBytes+len("…") {
			t.Errorf("SanitizeReason returned %d bytes, want at most %d plus the ellipsis", len(got), reasonMaxBytes)
		}
		if !strings.HasSuffix(got, "…") {
			t.Errorf("a bounded reason must say it was cut; got %q", got[max(0, len(got)-8):])
		}
	})
	t.Run("EmptyKeyRemovesNothing", func(t *testing.T) {
		if got := SanitizeReason("  plain text  ", ""); got != "plain text" {
			t.Errorf("SanitizeReason(%q, \"\") = %q, want the trimmed text", "  plain text  ", got)
		}
	})
}

func TestCheckRefusesAnUnanswerableProvider(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider config.Provider
	}{
		{name: "EmptyProvider", provider: ""},
		{name: "UnimplementedProvider", provider: "bedrock"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Check(t.Context(), Request{Provider: tc.provider, Key: fixtureKey}); err == nil {
				t.Errorf("Check(provider=%q) returned no error; an unanswerable request is the caller's duty to fix, not an outcome", tc.provider)
			}
		})
	}
}

// cliFixture writes an executable script that exits with code and prints
// out on stdout, standing in for `claude auth status`.
func cliFixture(t *testing.T, code int, out string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cli-fixture")
	script := "#!/bin/sh\nprintf '%s' " + quoteForShell(out) + "\nexit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatalf("write CLI fixture: %v", err)
	}
	return path
}

func quoteForShell(value string) string { return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'" }

func TestCheckCLIReadsTheSignInState(t *testing.T) {
	for _, tc := range []struct {
		name       string
		code       int
		output     string
		want       Outcome
		wantReason string
	}{
		{name: "ExitZeroSignedIn", code: 0, output: "Logged in as fixture", want: Accepted},
		{name: "NonZeroExitRejected", code: 1, output: "Not logged in. Run claude login.", want: Rejected, wantReason: "Not logged in"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Check(t.Context(), Request{Provider: config.ProviderClaudeCLI, CLIBin: cliFixture(t, tc.code, tc.output)})
			if err != nil {
				t.Fatalf("Check returned an error: %v", err)
			}
			if got.Outcome != tc.want {
				t.Errorf("Check(claude-cli).Outcome = %q (%s), want %q", got.Outcome, got.Reason, tc.want)
			}
			if tc.wantReason != "" && !strings.Contains(got.Reason, tc.wantReason) {
				t.Errorf("Check(claude-cli).Reason = %q, want it to carry the CLI's own text %q", got.Reason, tc.wantReason)
			}
			if len(got.Models) != 0 {
				t.Errorf("a CLI provider lists no models; got %v", got.Models)
			}
		})
	}
}

func TestCheckCLIMissingBinaryAndMissingPath(t *testing.T) {
	t.Run("UnsetPathRejected", func(t *testing.T) {
		got, err := Check(t.Context(), Request{Provider: config.ProviderCodexCLI})
		if err != nil {
			t.Fatalf("Check returned an error: %v", err)
		}
		if got.Outcome != Rejected || !strings.Contains(got.Reason, "no CLI executable path") {
			t.Errorf("Check(codex-cli) with no cli_bin = %q (%s), want %q naming the missing path", got.Outcome, got.Reason, Rejected)
		}
	})
	t.Run("AbsentBinaryUnreachable", func(t *testing.T) {
		got, err := Check(t.Context(), Request{Provider: config.ProviderCodexCLI, CLIBin: filepath.Join(t.TempDir(), "not-installed")})
		if err != nil {
			t.Fatalf("Check returned an error: %v", err)
		}
		if got.Outcome != Unreachable {
			t.Errorf("Check(codex-cli) with an absent binary = %q (%s), want %q", got.Outcome, got.Reason, Unreachable)
		}
	})
}

// TestCheckCLIStripsTheAPIKeyFromTheChild is the guard that keeps a CLI
// provider's check honest about WHAT it verified. A CLI provider exists to
// use the user's own subscription; a stray API key in the environment would
// let the CLI report itself signed in on API-key billing instead.
func TestCheckCLIStripsTheAPIKeyFromTheChild(t *testing.T) {
	const sentinel = "environment-key-that-must-not-reach-the-cli"
	t.Setenv("ANTHROPIC_API_KEY", sentinel)
	path := filepath.Join(t.TempDir(), "cli-echo")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'saw [%s]' \"$ANTHROPIC_API_KEY\"\nexit 1\n"), 0o700); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatalf("write CLI fixture: %v", err)
	}
	got, err := Check(t.Context(), Request{Provider: config.ProviderClaudeCLI, CLIBin: path})
	if err != nil {
		t.Fatalf("Check returned an error: %v", err)
	}
	if strings.Contains(got.Reason, sentinel) {
		t.Fatalf("the CLI child saw ANTHROPIC_API_KEY; the reason is %d bytes and the variable must be stripped", len(got.Reason))
	}
	if !strings.Contains(got.Reason, "saw []") {
		t.Errorf("Check(claude-cli).Reason = %q, want the fixture to report an empty ANTHROPIC_API_KEY", got.Reason)
	}
}

func TestOutcomeForError(t *testing.T) {
	if got, _ := OutcomeForError(nil); got != Accepted {
		t.Errorf("OutcomeForError(nil) = %q, want %q", got, Accepted)
	}
	for _, tc := range []struct {
		reason string
		want   Outcome
	}{
		{reason: "http_401", want: Rejected},
		{reason: "http_403", want: Rejected},
		{reason: "http_429", want: RateLimited},
		{reason: "http_500", want: Unreachable},
		{reason: "network", want: Unreachable},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			wrapped := fmt.Errorf("embed precheck: %w", &llm.LLMError{Reason: tc.reason, Cause: errors.New("stub")})
			if got, _ := OutcomeForError(wrapped); got != tc.want {
				t.Errorf("OutcomeForError(%s) = %q, want %q", tc.reason, got, tc.want)
			}
		})
	}
	t.Run("UnclassifiedErrorUnreachable", func(t *testing.T) {
		if got, _ := OutcomeForError(errors.New("dial tcp: connection refused")); got != Unreachable {
			t.Errorf("OutcomeForError(a plain error) = %q, want %q", got, Unreachable)
		}
	})
}
