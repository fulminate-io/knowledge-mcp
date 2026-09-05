// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
)

// refusalVocabulary is the reason table this client must latch. The four
// originals plus the two the gateway adds for unproven states — AND a reason
// this repo has never heard of, which is the load-bearing row: the vocabulary
// lives in another repo with its own release cadence, so a client that dropped
// an unrecognized reason would report a refusal it could not explain and leave
// a blocked user with no remedy.
var refusalVocabulary = []struct {
	name   string
	reason string
}{
	{name: "below_minimum", reason: clientver.ReasonBelowMinimum},
	{name: "version_header_absent", reason: clientver.ReasonHeaderAbsent},
	{name: "version_unparseable", reason: clientver.ReasonUnparseable},
	{name: "dev_stamp_not_allowlisted", reason: clientver.ReasonDevStampNotAllowlisted},
	{name: "version_unverified", reason: clientver.ReasonUnverified},
	{name: "version_unprovable", reason: clientver.ReasonUnprovable},
	{name: "a_reason_from_a_future_gateway_release", reason: "quantum_flux_not_allowlisted"},
}

// refusalBodyJSON builds the gateway's refusal body for a given reason.
func refusalBodyJSON(t *testing.T, reason string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]string{
		"minimum":         "2.0.0",
		"client_version":  "1.0.0",
		"platform":        "linux-amd64",
		"upgrade_command": "knowledge install",
		"reason":          reason,
	})
	if err != nil {
		t.Fatalf("marshal refusal body: %v", err)
	}
	return raw
}

// TestSyncTransport_LatchesMinimumVersionRefusal drives the HTTP cloud
// transport against a gateway returning 426 with the refusal body.
func TestSyncTransport_LatchesMinimumVersionRefusal(t *testing.T) {
	for _, tc := range refusalVocabulary {
		t.Run(tc.name, func(t *testing.T) {
			clientver.ClearRefusal()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUpgradeRequired)
				_, _ = w.Write(refusalBodyJSON(t, tc.reason))
			}))
			t.Cleanup(srv.Close)

			tr := accountTestTransport(t, srv, "acct_01REFUSEREFUSEREFUSERE")
			_, err := tr.SyncControlJSON(context.Background(), "presign", []byte(`{}`))

			// The call FAILS. It never succeeds and never continues on a
			// degraded path.
			if err == nil {
				t.Fatalf("a 426 refusal must fail the call, got nil error")
			}
			if _, ok := errors.AsType[*VersionRefusalError](err); !ok {
				t.Fatalf("a 426 must surface as *VersionRefusalError, got %T: %v", err, err)
			}
			// The error text carries the remedy: minimum, this client's
			// version, and the upgrade command.
			for _, want := range []string{"2.0.0", "1.0.0", "knowledge install"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal error text omits %q, so a refused user has no remedy: %v", want, err)
				}
			}

			got, ok := clientver.CurrentRefusal()
			if !ok {
				t.Fatalf("the refusal was not latched, so no status surface can report it")
			}
			// VERBATIM, recognized or not. Coercing an unknown reason to a
			// known member is the defect this row exists to catch.
			if got.Reason != tc.reason {
				t.Errorf("latched reason = %q, want the gateway's own %q verbatim", got.Reason, tc.reason)
			}
			if got.Minimum != "2.0.0" || got.ClientVersion != "1.0.0" || got.UpgradeCommand != "knowledge install" {
				t.Errorf("latched refusal lost gateway fields: %+v", got)
			}
			if got.At.IsZero() {
				t.Errorf("latched refusal carries no timestamp, so a renderer cannot say when it happened")
			}

			clientver.ClearRefusal()
			if _, stillSet := clientver.CurrentRefusal(); stillSet {
				t.Errorf("ClearRefusal left the latch set")
			}
		})
	}
}

// TestSyncTransport_UnparseableRefusalBodyStillLatches proves a 426 whose body
// does not parse is STILL a refusal rather than a generic transport error. A
// client that swallowed it would leave the user blocked with no signal at all.
func TestSyncTransport_UnparseableRefusalBodyStillLatches(t *testing.T) {
	clientver.ClearRefusal()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = w.Write([]byte(`<html>gateway said no</html>`))
	}))
	t.Cleanup(srv.Close)

	tr := accountTestTransport(t, srv, "acct_01REFUSEREFUSEREFUSERE")
	_, err := tr.SyncControlJSON(context.Background(), "presign", []byte(`{}`))
	if err == nil {
		t.Fatalf("an unparseable 426 must still fail the call")
	}
	got, ok := clientver.CurrentRefusal()
	if !ok {
		t.Fatalf("an unparseable 426 must still latch — it is a refusal, not an admission")
	}
	// THE LOCAL REASON, not the gateway's version_unparseable. The two mean
	// opposite things — this one says the fault is on this side — and while they
	// shared a spelling a reader with the right evidence in front of them still
	// went looking in the gateway's comparator.
	if got.Reason != clientver.ReasonRefusalBodyUnparseable {
		t.Errorf("latched reason = %q, want the LOCAL %q; the gateway's %q means something else entirely",
			got.Reason, clientver.ReasonRefusalBodyUnparseable, clientver.ReasonUnparseable)
	}
	if !strings.Contains(err.Error(), "could not read the refusal") {
		t.Errorf("the error should say this client could not read the body rather than report an empty minimum as an answer: %v", err)
	}
	// The diagnostic names the transport and quotes what arrived, which is what
	// turns "unreadable" into a one-line diagnosis.
	for _, want := range []string{"sync", "gateway said no"} {
		if !strings.Contains(got.Diagnostic, want) {
			t.Errorf("diagnostic omits %q, so a reader cannot tell which transport lost the body or what it held: %q", want, got.Diagnostic)
		}
	}
	// The client falls back to its OWN identity when the gateway's body carries
	// none, so the message still names a version rather than an empty string.
	if got.ClientVersion != clientver.Version {
		t.Errorf("client version = %q, want this client's own %q", got.ClientVersion, clientver.Version)
	}
	clientver.ClearRefusal()
}

// capturedRefusalBody is the EXACT 109-byte body the dev gateway returned to a
// v0.8.3-stamped client on 2026-09-04, byte-identical on the sync route and the
// connect route. Quoted verbatim rather than composed from a map so this test
// fails if the wire shape it was written against ever changes.
const capturedRefusalBody = `{"minimum":"v0.8.4","client_version":"v0.8.3","upgrade_command":"knowledge install","reason":"below_minimum"}`

// TestSyncTransport_CapturedRefusalCarriesTheRemedy drives the SYNC transport
// against the exact bytes the gateway sent, as the other half of the pair whose
// connect twin lives in graphclient.
//
// IT IS THE CONTROL THAT SAYS WHICH TRANSPORT WAS BROKEN. Both routes returned
// the identical body, yet only the connect one lost it — because only that one
// sets Accept-Encoding and takes the bytes as they arrive. Asserting the sync
// route was fine all along is what keeps the fix pointed at the transport that
// actually had the defect.
func TestSyncTransport_CapturedRefusalCarriesTheRemedy(t *testing.T) {
	clientver.ClearRefusal()
	t.Cleanup(clientver.ClearRefusal)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = w.Write([]byte(capturedRefusalBody))
	}))
	t.Cleanup(srv.Close)

	tr := accountTestTransport(t, srv, "acct_01REFUSEREFUSEREFUSERE")
	_, err := tr.SyncControlJSON(context.Background(), "presign", []byte(`{}`))
	if err == nil {
		t.Fatalf("a 426 refusal must fail the call, got nil error")
	}
	got, ok := clientver.CurrentRefusal()
	if !ok {
		t.Fatalf("the refusal was not latched, so no status surface can report it")
	}
	if got.Reason != clientver.ReasonBelowMinimum {
		t.Errorf("latched reason = %q, want the gateway's %q", got.Reason, clientver.ReasonBelowMinimum)
	}
	if got.Minimum != "v0.8.4" || got.ClientVersion != "v0.8.3" || got.UpgradeCommand != "knowledge install" {
		t.Errorf("latched refusal lost the gateway's fields: %+v", got)
	}
	if got.Diagnostic != "" {
		t.Errorf("a refusal the gateway explained must carry no local diagnostic, got %q", got.Diagnostic)
	}
	for _, want := range []string{"v0.8.4", "v0.8.3", "knowledge install"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal error text omits %q, so a refused user has no remedy: %v", want, err)
		}
	}
}

// TestLatchVersionRefusal_IgnoresNon426 is the discriminating control for the
// whole classifier: the same instrument that latches a 426 must stay silent on
// every other status, or a green latch assertion elsewhere would be consistent
// with a classifier that latches unconditionally.
func TestLatchVersionRefusal_IgnoresNon426(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusForbidden, http.StatusBadRequest, http.StatusInternalServerError} {
		clientver.ClearRefusal()
		if _, ok := LatchVersionRefusal(RefusalObservation{
			Status: status, Body: refusalBodyJSON(t, clientver.ReasonBelowMinimum),
			Transport: "sync", Path: "/v1/sync/presign",
		}); ok {
			t.Errorf("HTTP %d classified as a version refusal", status)
		}
		if _, latched := clientver.CurrentRefusal(); latched {
			t.Errorf("HTTP %d latched a refusal", status)
		}
	}
	// KNOWN-POSITIVE, same run, same instrument: a 426 through the identical
	// call path DOES latch, so the silence above is a property of the status
	// rather than of a classifier that never fires.
	clientver.ClearRefusal()
	if _, ok := LatchVersionRefusal(RefusalObservation{
		Status: http.StatusUpgradeRequired, Body: refusalBodyJSON(t, clientver.ReasonBelowMinimum),
		Transport: "sync", Path: "/v1/sync/presign",
	}); !ok {
		t.Fatalf("the control failed: a 426 did not classify, so the negatives above prove nothing")
	}
	if _, latched := clientver.CurrentRefusal(); !latched {
		t.Fatalf("the control failed: a 426 did not latch")
	}
	clientver.ClearRefusal()
}
