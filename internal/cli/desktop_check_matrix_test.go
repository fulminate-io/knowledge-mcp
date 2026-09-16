// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// THE AXIS-BY-OUTCOME MATRIX, complete as a table.
//
// Each check axis re-expresses the four-word outcome vocabulary in its own
// mapper — the summarizer through providercheck's HTTP arm, the embedder
// through OutcomeForError over the embed arm's *llm.LLMError, the tracker
// through trackerOutcome over the Linear transport's *backends.Error — so a
// taxonomy asserted once on one axis says nothing about the other two. Two
// cells shipped unasserted for exactly that reason: the tracker's
// rate-limited arm and the embedder's unreachable arm.
//
// Every axis times every outcome is a ROW here. A cell nobody wrote is now a
// missing row rather than something a later reader discovers.

// axisStub is one axis's provider stand-in: the base URL the check is
// pointed at. There is no hit counter, deliberately — every row below
// asserts the outcome AND a substring of the provider's own sentence, and
// neither can be produced without the call having happened, so a counter
// would be a second weaker witness of the same fact.
type axisStub struct{ baseURL string }

// summarizerStub answers a list-models read with status.
func summarizerStub(t *testing.T, status int, body string) axisStub {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write stub body: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return axisStub{baseURL: srv.URL}
}

// closedListener returns the URL of a listener that has been shut down, so a
// check against it fails in transport rather than with a status. It is the
// only way to reach the unreachable cell: an HTTP status, however bad, is a
// provider that answered.
func closedListener(t *testing.T) axisStub {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Errorf("the closed listener received a request; it must be shut before the check runs")
	}))
	url := srv.URL
	srv.Close()
	return axisStub{baseURL: url}
}

// embedAxisStub answers an embeddings request with status.
func embedAxisStub(t *testing.T, status int) axisStub {
	t.Helper()
	srv, _, _ := embedStub(t, status)
	return axisStub{baseURL: srv.URL}
}

// trackerAxisStub answers the viewer query with status.
func trackerAxisStub(t *testing.T, status int, viewerID string) axisStub {
	t.Helper()
	srv, _, _ := linearStub(t, status, viewerID)
	return axisStub{baseURL: srv.URL}
}

func TestDesktopCheckAxisOutcomeMatrix(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("VOYAGE_API_KEY", "")
	t.Setenv("LINEAR_API_KEY", "")
	t.Cleanup(config.SetForTest(nil))
	for _, tc := range []struct {
		name     string
		axis     string
		provider string
		stub     func(t *testing.T) axisStub
		want     string
		// wantReason is a substring the user-facing sentence must carry, so a
		// cell cannot pass with the right word and the wrong explanation.
		wantReason string
		// echoesKey marks the row whose stub puts the credential in its own
		// error body. THE KEY-ABSENCE ASSERTION BELOW RUNS ON EVERY ROW, and
		// on every other row it cannot fail, because a reason that never
		// carried the key cannot stop carrying it. This row is that
		// assertion's same-run known positive, through the same instrument,
		// the same field and the same loop: it asserts the redaction marker
		// is present as well as the value absent, so a redaction that stopped
		// working turns it red and the eleven silent copies are readable as
		// the cheap restatement they are.
		echoesKey bool
	}{
		// SUMMARIZER: the list-models read.
		{name: "SummarizerAccepted", axis: checkAxisSummarizer, provider: "openai", want: "accepted", stub: func(t *testing.T) axisStub { return summarizerStub(t, http.StatusOK, `{"data":[{"id":"gpt-5"}]}`) }},
		{name: "SummarizerRejected", axis: checkAxisSummarizer, provider: "openai", want: "rejected", wantReason: "rejected the key", echoesKey: true, stub: func(t *testing.T) axisStub {
			return summarizerStub(t, http.StatusUnauthorized, `{"error":"key `+checkFixtureKey+` is not valid"}`)
		}},
		{name: "SummarizerUnreachable", axis: checkAxisSummarizer, provider: "openai", want: "unreachable", wantReason: "could not be reached", stub: closedListener},
		{name: "SummarizerRateLimited", axis: checkAxisSummarizer, provider: "openai", want: "rate_limited", wantReason: "rate-limiting", stub: func(t *testing.T) axisStub { return summarizerStub(t, http.StatusTooManyRequests, `slow down`) }},

		// EMBEDDER: one live embed through the startup axis check.
		{name: "EmbedderAccepted", axis: checkAxisEmbedder, provider: "voyage", want: "accepted", stub: func(t *testing.T) axisStub { return embedAxisStub(t, http.StatusOK) }},
		{name: "EmbedderRejected", axis: checkAxisEmbedder, provider: "voyage", want: "rejected", wantReason: "rejected the key", stub: func(t *testing.T) axisStub { return embedAxisStub(t, http.StatusUnauthorized) }},
		{name: "EmbedderUnreachable", axis: checkAxisEmbedder, provider: "voyage", want: "unreachable", stub: closedListener},
		{name: "EmbedderRateLimited", axis: checkAxisEmbedder, provider: "voyage", want: "rate_limited", wantReason: "rate-limiting", stub: func(t *testing.T) axisStub { return embedAxisStub(t, http.StatusTooManyRequests) }},

		// TRACKER: one viewer query on the Linear transport.
		{name: "TrackerAccepted", axis: checkAxisTracker, want: "accepted", stub: func(t *testing.T) axisStub { return trackerAxisStub(t, http.StatusOK, "viewer-fixture") }},
		{name: "TrackerRejected", axis: checkAxisTracker, want: "rejected", wantReason: "rejected the key", stub: func(t *testing.T) axisStub { return trackerAxisStub(t, http.StatusUnauthorized, "") }},
		{name: "TrackerUnreachable", axis: checkAxisTracker, want: "unreachable", wantReason: "could not be reached", stub: closedListener},
		{name: "TrackerRateLimited", axis: checkAxisTracker, want: "rate_limited", wantReason: "rate-limiting", stub: func(t *testing.T) axisStub { return trackerAxisStub(t, http.StatusTooManyRequests, "") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := tc.stub(t)
			got := runCheck(t.Context(), t, tc.axis, filepath.Join(t.TempDir(), "config"), desktopCheckRequest{Provider: tc.provider, Key: checkFixtureKey, BaseURL: stub.baseURL})
			if got.Outcome != tc.want {
				t.Fatalf("%s check outcome = %q (%s), want %q", tc.axis, got.Outcome, got.Reason, tc.want)
			}
			if tc.wantReason != "" && !strings.Contains(got.Reason, tc.wantReason) {
				t.Errorf("%s check reason = %q, want it to carry %q — the right word with the wrong explanation is still wrong", tc.axis, got.Reason, tc.wantReason)
			}
			if strings.Contains(got.Reason, checkFixtureKey) {
				t.Errorf("%s check reason carried the pasted credential; it is %d bytes and must carry none of it", tc.axis, len(got.Reason))
			}
			if tc.echoesKey && !strings.Contains(got.Reason, "[redacted]") {
				t.Errorf("%s check reason = %q, want the echoed credential replaced by the redaction marker — this row is the control for the absence assertion above", tc.axis, got.Reason)
			}
		})
	}
}

// TestDesktopCheckStoredKeyFallbackPerAxis is the resume cell, once per axis.
//
// Continue is gated on an accepted check, so a user who saved a key, left the
// guide and came back with nothing re-pasted depends on this fallback to
// complete the step at all. It shipped observed on two axes and unobserved on
// the embedder, whose key the guide most expects to already be there.
func TestDesktopCheckStoredKeyFallbackPerAxis(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("VOYAGE_API_KEY", "")
	t.Setenv("LINEAR_API_KEY", "")
	t.Cleanup(config.SetForTest(nil))
	for _, tc := range []struct {
		name       string
		axis       string
		provider   string
		configBody string
	}{
		{name: "Summarizer", axis: checkAxisSummarizer, provider: "openai", configBody: "[credentials]\nopenai_api_key = \"" + checkFixtureKey + "\"\n"},
		{name: "Embedder", axis: checkAxisEmbedder, provider: "voyage", configBody: "[credentials]\nvoyage_api_key = \"" + checkFixtureKey + "\"\n"},
		{name: "Tracker", axis: checkAxisTracker, configBody: "[credentials]\nlinear_api_key = \"" + checkFixtureKey + "\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Each axis's stub records whether the key it saw was the stored
			// fixture one — a boolean, never the value.
			var sawKey *atomic.Bool
			var baseURL string
			switch tc.axis {
			case checkAxisSummarizer:
				srv, seen, _ := modelListStub(t, http.StatusOK, `{"data":[{"id":"gpt-5"}]}`)
				sawKey, baseURL = seen, srv.URL
			case checkAxisEmbedder:
				srv, seen, _ := embedStub(t, http.StatusOK)
				sawKey, baseURL = seen, srv.URL
			case checkAxisTracker:
				srv, seen, _ := linearStub(t, http.StatusOK, "viewer-fixture")
				sawKey, baseURL = seen, srv.URL
			}
			path := scratchConfig(t, tc.configBody)
			got := runCheck(t.Context(), t, tc.axis, path, desktopCheckRequest{Provider: tc.provider, BaseURL: baseURL})
			if got.Outcome != "accepted" {
				t.Fatalf("%s check with a stored key and nothing pasted = %q (%s), want accepted", tc.axis, got.Outcome, got.Reason)
			}
			if !sawKey.Load() {
				t.Errorf("%s: the stub did not receive the stored key, so the fallback did not resolve it", tc.axis)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("re-read scratch config: %v", err)
			}
			if string(after) != tc.configBody {
				t.Errorf("%s: the check rewrote the configuration; %d bytes before, %d after", tc.axis, len(tc.configBody), len(after))
			}
		})
	}
}
