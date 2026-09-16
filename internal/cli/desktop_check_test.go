// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// checkFixtureKey is the pasted credential every check test uses. No
// assertion below prints it; the ones that care report whether it was
// PRESENT, or assert that it is absent.
const checkFixtureKey = "desktop-check-fixture-key"

// scratchConfig writes body to a file under the test's own directory and
// returns the path. Nothing here touches the operator's ~/.knowledge.
func scratchConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write scratch config: %v", err)
	}
	return path
}

// modelListStub answers a list-models read and records whether the key it
// saw was the fixture one — a boolean, never the value.
func modelListStub(t *testing.T, status int, body string) (*httptest.Server, *atomic.Bool, *atomic.Int32) {
	t.Helper()
	var sawFixtureKey atomic.Bool
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		sawFixtureKey.Store(r.Header.Get("Authorization") == "Bearer "+checkFixtureKey)
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write stub body: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &sawFixtureKey, &hits
}

// embedStub answers one embeddings request, recording whether the pasted
// key arrived and how many requests it saw.
func embedStub(t *testing.T, status int) (*httptest.Server, *atomic.Bool, *atomic.Int32) {
	t.Helper()
	var sawFixtureKey atomic.Bool
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		sawFixtureKey.Store(r.Header.Get("Authorization") == "Bearer "+checkFixtureKey)
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"embedding": make([]int, 32)}}}); err != nil {
			t.Errorf("encode embed response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &sawFixtureKey, &hits
}

// linearStub answers the viewer query, recording whether Authorization
// carried the RAW key with no Bearer prefix — the convention the Linear
// transport documents — and the query text it received.
func linearStub(t *testing.T, status int, viewerID string) (*httptest.Server, *atomic.Bool, *atomic.Bool) {
	t.Helper()
	var rawKey, askedViewer atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawKey.Store(r.Header.Get("Authorization") == checkFixtureKey)
		// Both wire fields are declared and an unknown one is refused, so
		// the stub asserts the envelope shape rather than skimming it.
		var body struct {
			Query     string `json:"query"`
			Variables any    `json:"variables"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			t.Errorf("decode graphql body: %v", err)
		}
		askedViewer.Store(strings.Contains(body.Query, "viewer"))
		if status != http.StatusOK {
			w.WriteHeader(status)
			if _, err := w.Write([]byte(`{"errors":[{"message":"authentication failed"}]}`)); err != nil {
				t.Errorf("write stub body: %v", err)
			}
			return
		}
		payload := `{"data":{"viewer":{"id":"` + viewerID + `"}}}`
		if viewerID == "" {
			payload = `{"data":{"viewer":{}}}`
		}
		if _, err := w.Write([]byte(payload)); err != nil {
			t.Errorf("write stub body: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &rawKey, &askedViewer
}

// runCheck drives the command's own dispatch with a body on a reader, the
// way the Desktop drives it over stdin.
func runCheck(ctx context.Context, t *testing.T, axis, configPath string, body desktopCheckRequest) desktopCheckResult {
	t.Helper()
	credentials, err := desktopCheckCredentials(configPath)
	if err != nil {
		t.Fatalf("desktopCheckCredentials(%q) = %v", configPath, err)
	}
	result, err := runDesktopCheck(ctx, axis, body, credentials)
	if err != nil {
		t.Fatalf("runDesktopCheck(%q) = %v", axis, err)
	}
	return result
}

func TestDesktopCheckSummarizerListsModelsForAnAcceptedKey(t *testing.T) {
	srv, sawKey, hits := modelListStub(t, http.StatusOK, `{"data":[{"id":"gpt-5"},{"id":"gpt-5-mini"}]}`)
	path := filepath.Join(t.TempDir(), "config")
	got := runCheck(t.Context(), t, checkAxisSummarizer, path, desktopCheckRequest{Provider: "openai", Key: checkFixtureKey, BaseURL: srv.URL})
	if got.Outcome != "accepted" {
		t.Fatalf("summarizer check outcome = %q (%s), want accepted", got.Outcome, got.Reason)
	}
	if len(got.Models) != 2 || got.Models[0] != "gpt-5" {
		t.Errorf("summarizer check models = %v, want the stub's two ids in order", got.Models)
	}
	if !sawKey.Load() {
		t.Errorf("the stub did not receive the pasted key; it must reach the provider, and only the provider")
	}
	if hits.Load() != 1 {
		t.Errorf("the stub saw %d requests; want exactly 1", hits.Load())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("os.Stat(%q) = %v; a check must create no configuration file", path, err)
	}
}

// TestDesktopCheckNeverWritesTheConfiguration is the check-before-write
// order's own test: whatever the provider answers, the file on disk is
// byte-identical afterwards. The accepted arm is the control — a check that
// never wrote anything because it never ran would pass the rejected arm
// alone.
func TestDesktopCheckNeverWritesTheConfiguration(t *testing.T) {
	const body = `[default]
provider = "openai"
model = "gpt-5"
`
	for _, tc := range []struct {
		name        string
		status      int
		listBody    string
		wantOutcome string
	}{
		{name: "RejectedKey", status: http.StatusUnauthorized, listBody: `{"error":"nope"}`, wantOutcome: "rejected"},
		{name: "RateLimitedProvider", status: http.StatusTooManyRequests, listBody: `slow down`, wantOutcome: "rate_limited"},
		{name: "AcceptedKeyControl", status: http.StatusOK, listBody: `{"data":[{"id":"gpt-5"}]}`, wantOutcome: "accepted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := scratchConfig(t, body)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read scratch config: %v", err)
			}
			srv, _, _ := modelListStub(t, tc.status, tc.listBody)
			got := runCheck(t.Context(), t, checkAxisSummarizer, path, desktopCheckRequest{Provider: "openai", Key: checkFixtureKey, BaseURL: srv.URL})
			if got.Outcome != tc.wantOutcome {
				t.Errorf("outcome = %q (%s), want %q", got.Outcome, got.Reason, tc.wantOutcome)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("re-read scratch config: %v", err)
			}
			if string(before) != string(after) {
				t.Errorf("the configuration changed across a check: %d bytes before, %d after", len(before), len(after))
			}
			if strings.Contains(string(after), checkFixtureKey) {
				t.Errorf("the configuration carries the pasted credential after a check; it is %d bytes and must carry none of it", len(after))
			}
		})
	}
}

// TestDesktopCheckUsesTheStoredKeyWhenNothingIsPasted is the revisited-step
// cell: a step reached again on resume with a key already saved and nothing
// re-typed must still be checkable, or it can never complete on its second
// visit.
func TestDesktopCheckUsesTheStoredKeyWhenNothingIsPasted(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	srv, sawKey, hits := modelListStub(t, http.StatusOK, `{"data":[{"id":"gpt-5"}]}`)
	path := scratchConfig(t, "[credentials]\nopenai_api_key = \""+checkFixtureKey+"\"\n")
	got := runCheck(t.Context(), t, checkAxisSummarizer, path, desktopCheckRequest{Provider: "openai", BaseURL: srv.URL})
	if got.Outcome != "accepted" {
		t.Fatalf("outcome = %q (%s), want accepted from the stored key", got.Outcome, got.Reason)
	}
	if !sawKey.Load() {
		t.Errorf("the stub did not receive the stored key; the fallback did not resolve it")
	}
	if hits.Load() != 1 {
		t.Errorf("the stub saw %d requests; want exactly 1", hits.Load())
	}
}

// TestDesktopCheckWithNoKeyAnywhereFailsClosed is the cell where the step's
// non-credential fields are set and no key exists in either place.
func TestDesktopCheckWithNoKeyAnywhereFailsClosed(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	srv, _, hits := modelListStub(t, http.StatusOK, `{"data":[{"id":"gpt-5"}]}`)
	path := scratchConfig(t, "[default]\nprovider = \"openai\"\nmodel = \"gpt-5\"\n")
	got := runCheck(t.Context(), t, checkAxisSummarizer, path, desktopCheckRequest{Provider: "openai"})
	if got.Outcome != "rejected" {
		t.Errorf("outcome = %q (%s), want rejected — a key that does not exist is not an accepted one", got.Outcome, got.Reason)
	}
	if !strings.Contains(got.Reason, "no API key was provided") {
		t.Errorf("reason = %q, want it to name the missing key", got.Reason)
	}
	if hits.Load() != 0 {
		t.Errorf("the keyless check issued %d requests; want 0", hits.Load())
	}
	_ = srv
}

func TestDesktopCheckEmbedder(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	t.Cleanup(config.SetForTest(nil))
	t.Run("AcceptedKey", func(t *testing.T) {
		srv, sawKey, hits := embedStub(t, http.StatusOK)
		got := runCheck(t.Context(), t, checkAxisEmbedder, filepath.Join(t.TempDir(), "config"), desktopCheckRequest{Provider: "voyage", Key: checkFixtureKey, BaseURL: srv.URL})
		if got.Outcome != "accepted" {
			t.Fatalf("outcome = %q (%s), want accepted", got.Outcome, got.Reason)
		}
		if !sawKey.Load() || hits.Load() != 1 {
			t.Errorf("the stub saw the pasted key=%v after %d requests; want true after exactly 1", sawKey.Load(), hits.Load())
		}
		if len(got.Models) != 0 {
			t.Errorf("the embedder step offers no model list; got %v", got.Models)
		}
	})
	t.Run("RejectedKey", func(t *testing.T) {
		srv, _, hits := embedStub(t, http.StatusUnauthorized)
		got := runCheck(t.Context(), t, checkAxisEmbedder, filepath.Join(t.TempDir(), "config"), desktopCheckRequest{Provider: "voyage", Key: checkFixtureKey, BaseURL: srv.URL})
		if got.Outcome != "rejected" {
			t.Errorf("outcome = %q (%s), want rejected", got.Outcome, got.Reason)
		}
		if hits.Load() == 0 {
			t.Errorf("the stub saw no request; the rejection must come from a call that happened")
		}
	})
	t.Run("RateLimitedNotRejected", func(t *testing.T) {
		srv, _, _ := embedStub(t, http.StatusTooManyRequests)
		got := runCheck(t.Context(), t, checkAxisEmbedder, filepath.Join(t.TempDir(), "config"), desktopCheckRequest{Provider: "voyage", Key: checkFixtureKey, BaseURL: srv.URL})
		if got.Outcome != "rate_limited" {
			t.Errorf("outcome = %q (%s), want rate_limited", got.Outcome, got.Reason)
		}
	})
	t.Run("NoKeyNoEndpointFailsClosed", func(t *testing.T) {
		srv, _, hits := embedStub(t, http.StatusOK)
		got := runCheck(t.Context(), t, checkAxisEmbedder, filepath.Join(t.TempDir(), "config"), desktopCheckRequest{Provider: "voyage"})
		if got.Outcome != "rejected" {
			t.Errorf("outcome = %q (%s), want rejected — the startup check's BM25-only opt-out must not read as an accepted key here", got.Outcome, got.Reason)
		}
		if hits.Load() != 0 {
			t.Errorf("the unconfigured check issued %d requests; want 0", hits.Load())
		}
		_ = srv
	})
	t.Run("DeterministicFake", func(t *testing.T) {
		got := runCheck(t.Context(), t, checkAxisEmbedder, filepath.Join(t.TempDir(), "config"), desktopCheckRequest{Provider: "fake"})
		if got.Outcome != "accepted" || !strings.Contains(got.Reason, "makes no provider call") {
			t.Errorf("outcome = %q (%s), want accepted with a reason saying nothing was called", got.Outcome, got.Reason)
		}
	})
}

func TestDesktopCheckTracker(t *testing.T) {
	t.Setenv("LINEAR_API_KEY", "")
	t.Cleanup(config.SetForTest(nil))
	t.Run("AcceptedKeyRawAuthHeader", func(t *testing.T) {
		srv, rawKey, askedViewer := linearStub(t, http.StatusOK, "viewer-fixture-id")
		got := runCheck(t.Context(), t, checkAxisTracker, filepath.Join(t.TempDir(), "config"), desktopCheckRequest{Key: checkFixtureKey, BaseURL: srv.URL})
		if got.Outcome != "accepted" {
			t.Fatalf("outcome = %q (%s), want accepted", got.Outcome, got.Reason)
		}
		if !rawKey.Load() {
			t.Errorf("the stub did not see the key in the raw Authorization form; Linear personal keys carry no Bearer prefix")
		}
		if !askedViewer.Load() {
			t.Errorf("the stub did not receive a viewer query")
		}
	})
	t.Run("RejectedKey", func(t *testing.T) {
		srv, _, _ := linearStub(t, http.StatusUnauthorized, "")
		got := runCheck(t.Context(), t, checkAxisTracker, filepath.Join(t.TempDir(), "config"), desktopCheckRequest{Key: checkFixtureKey, BaseURL: srv.URL})
		if got.Outcome != "rejected" {
			t.Errorf("outcome = %q (%s), want rejected", got.Outcome, got.Reason)
		}
	})
	t.Run("NoViewerNotAccepted", func(t *testing.T) {
		srv, _, _ := linearStub(t, http.StatusOK, "")
		got := runCheck(t.Context(), t, checkAxisTracker, filepath.Join(t.TempDir(), "config"), desktopCheckRequest{Key: checkFixtureKey, BaseURL: srv.URL})
		if got.Outcome == "accepted" {
			t.Errorf("outcome = accepted for an endpoint that identified no viewer; want a non-accepted outcome")
		}
	})
	t.Run("NoKeyRejectedAndOptional", func(t *testing.T) {
		got := runCheck(t.Context(), t, checkAxisTracker, filepath.Join(t.TempDir(), "config"), desktopCheckRequest{})
		if got.Outcome != "rejected" || !strings.Contains(got.Reason, "optional") {
			t.Errorf("outcome = %q (%s), want rejected with a reason saying the step may be skipped", got.Outcome, got.Reason)
		}
	})
	t.Run("StoredKeyUsed", func(t *testing.T) {
		srv, rawKey, _ := linearStub(t, http.StatusOK, "viewer-fixture-id")
		path := scratchConfig(t, "[credentials]\nlinear_api_key = \""+checkFixtureKey+"\"\n")
		got := runCheck(t.Context(), t, checkAxisTracker, path, desktopCheckRequest{BaseURL: srv.URL})
		if got.Outcome != "accepted" || !rawKey.Load() {
			t.Errorf("outcome = %q (%s) with the fixture key seen=%v, want accepted from the stored key", got.Outcome, got.Reason, rawKey.Load())
		}
	})
}

func TestDesktopCheckRefusesAnUnanswerableRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	for _, tc := range []struct {
		name    string
		axis    string
		request desktopCheckRequest
	}{
		{name: "UnrecognizedAxis", axis: "reranker", request: desktopCheckRequest{Provider: "voyage", Key: checkFixtureKey}},
		{name: "EmptyAxis", axis: "", request: desktopCheckRequest{Provider: "openai", Key: checkFixtureKey}},
		{name: "SummarizerProviderNotLLM", axis: checkAxisSummarizer, request: desktopCheckRequest{Provider: "voyage", Key: checkFixtureKey}},
		{name: "SummarizerProviderEmpty", axis: checkAxisSummarizer, request: desktopCheckRequest{Key: checkFixtureKey}},
		{name: "EmbedderProviderNotEmbed", axis: checkAxisEmbedder, request: desktopCheckRequest{Provider: "anthropic", Key: checkFixtureKey}},
		{name: "TrackerProviderNotLinear", axis: checkAxisTracker, request: desktopCheckRequest{Provider: "jira", Key: checkFixtureKey}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			credentials, err := desktopCheckCredentials(path)
			if err != nil {
				t.Fatalf("desktopCheckCredentials: %v", err)
			}
			if _, err := runDesktopCheck(t.Context(), tc.axis, tc.request, credentials); err == nil {
				t.Errorf("runDesktopCheck(%q, provider=%q) returned no error; an unanswerable request is refused, never defaulted", tc.axis, tc.request.Provider)
			}
		})
	}
}

func TestReadDesktopCheckRequest(t *testing.T) {
	t.Run("WellFormedBody", func(t *testing.T) {
		got, err := readDesktopCheckRequest(strings.NewReader(`{"provider":"openai","key":"x","model":"gpt-5","base_url":"","cli_bin":""}`))
		if err != nil {
			t.Fatalf("readDesktopCheckRequest returned an error: %v", err)
		}
		if got.Provider != "openai" || got.Model != "gpt-5" || len(got.Key) != 1 {
			t.Errorf("decoded provider=%q model=%q key length=%d, want openai/gpt-5 and a one-byte key", got.Provider, got.Model, len(got.Key))
		}
	})
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "UnknownField", body: `{"provider":"openai","token":"x"}`},
		{name: "TrailingContent", body: `{"provider":"openai"}{"provider":"openai"}`},
		{name: "NotAnObject", body: `["openai"]`},
		{name: "ValueOverFieldBound", body: `{"provider":"openai","key":"` + strings.Repeat("k", 16385) + `"}`},
		{name: "BodyOverRequestBound", body: `{"provider":"` + strings.Repeat("p", 65537) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := readDesktopCheckRequest(strings.NewReader(tc.body)); err == nil {
				t.Errorf("readDesktopCheckRequest(%s) returned no error; want a refusal", tc.name)
			}
		})
	}
}

func TestDesktopCheckCredentials(t *testing.T) {
	t.Run("MissingFileIsFirstRun", func(t *testing.T) {
		got, err := desktopCheckCredentials(filepath.Join(t.TempDir(), "config"))
		if err != nil {
			t.Fatalf("desktopCheckCredentials on a missing file = %v, want no error", err)
		}
		if got != nil {
			t.Errorf("desktopCheckCredentials on a missing file returned a credentials table; want none")
		}
	})
	t.Run("MalformedFileReported", func(t *testing.T) {
		path := scratchConfig(t, "[default\n")
		if _, err := desktopCheckCredentials(path); err == nil {
			t.Errorf("desktopCheckCredentials on a malformed file returned no error; a broken file must not read as no stored key")
		}
	})
	t.Run("StoredKeyResolved", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "")
		path := scratchConfig(t, "[credentials]\nanthropic_api_key = \""+checkFixtureKey+"\"\n")
		got, err := desktopCheckCredentials(path)
		if err != nil {
			t.Fatalf("desktopCheckCredentials: %v", err)
		}
		if length := len(got.APIKeyFor(config.ProviderAnthropic)); length != len(checkFixtureKey) {
			t.Errorf("resolved key length = %d, want %d", length, len(checkFixtureKey))
		}
	})
}
