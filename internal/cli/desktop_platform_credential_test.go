// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// platformVerbatimBody is what platformEmittedText writes in place of a body
// this command carried exactly as the platform served it. It carries no
// character json.Marshal escapes, so the rendered text contains it literally.
const platformVerbatimBody = "(the platform's body, verbatim)"

// platformArmJSON and platformArmBinary label the two success arms in the arm
// census TestDesktopPlatformNoCredentialEmitted keeps, beside the members of
// platformOutcomeCodes.
const (
	platformArmJSON   = "the JSON arm"
	platformArmBinary = "the binary arm"
)

// platformOutcomeArm names the arm a result took, in the vocabulary the arm
// census is kept in: a failure is its code, and a success is the arm its
// builder wrote — platformBinaryResult sets bodyBase64, platformJSONResult sets
// body.
func platformOutcomeArm(out desktopPlatformResult) string {
	switch {
	case out.Code != "":
		return out.Code
	case out.BodyBase64 != nil:
		return platformArmBinary
	default:
		return platformArmJSON
	}
}

// platformEmittedText renders the document this command wrote for one outcome
// — the same json.Marshal DesktopPlatformCmd encodes to stdout — with the
// platform's own body removed where it was carried VERBATIM: the JSON arm's
// body when it equals the bytes the platform served, and the binary arm's
// bodyBase64 when it decodes to them. A body the command altered is no longer
// the platform's words, so it stays in the text, decoded on the binary arm so
// that a needle inside it is a substring the caller can find rather than a
// base64 fragment. It returns the text rather than failing the test so the Test
// function owns every assertion made over it.
func platformEmittedText(out desktopPlatformResult, platformBody string) (string, error) {
	emitted := out
	if string(emitted.Body) == platformBody {
		emitted.Body = json.RawMessage(`"` + platformVerbatimBody + `"`)
	}
	if emitted.BodyBase64 != nil {
		decoded, err := base64.StdEncoding.DecodeString(*emitted.BodyBase64)
		if err != nil {
			return "", err
		}
		text := string(decoded)
		if text == platformBody {
			text = platformVerbatimBody
		}
		emitted.BodyBase64 = &text
	}
	raw, err := json.Marshal(emitted)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// TestDesktopPlatformNoCredentialEmitted pins that no document this command can
// write carries the bearer or the refresh token, on any encoding, on EVERY arm
// it can send — the JSON arm, the binary arm and each member of
// platformOutcomeCodes — and that a failure code is from the declared
// vocabulary rather than a passthrough of what the platform said.
//
// The arm census is derived, never hand-listed: every outcome driven below is
// classified by platformOutcomeArm, and the test fails naming any member of
// platformOutcomeCodes, or either success arm, that no outcome reached. A code
// added to the vocabulary without a row here is a red, which is what keeps this
// row from silently covering a subset of what the command can send.
//
// The one place a credential-shaped string may appear is the platform's own
// body carried verbatim (a 401 echoing the bearer, a CSV whose cells hold
// token-shaped text); platformEmittedText exempts that body by EQUALITY with
// what the fixture served, so a body the command altered is searched in full.
func TestDesktopPlatformNoCredentialEmitted(t *testing.T) {
	f := newPlatformFixture(t, "account-A")
	const export = `{"method":"GET","path":"/v1/accounts/{account_id}/audit-logs/export","params":{"account_id":"account-A"}}`
	const identity = `{"method":"GET","path":"/auth/me","params":{}}`
	type outcome struct {
		name         string
		platformBody string
		result       desktopPlatformResult
	}
	var outcomes []outcome
	drive := func(name, request string) {
		outcomes = append(outcomes, outcome{name: name, platformBody: f.body, result: f.run(t, request)})
	}
	f.body = `{"name":"Ada","email":"ada@example.com"}`
	drive("a JSON answer on the identity read", identity)
	drive("a JSON answer on the account list", `{"method":"GET","path":"/v1/accounts","params":{}}`)
	drive("a request carrying a token field", `{"method":"GET","path":"/auth/me","params":{},"token":"x"}`)
	drive("a path outside the structural wall", `{"method":"GET","path":"/auth/logout","params":{}}`)
	f.status, f.body = http.StatusUnauthorized, `{"error":"Bearer test-native rejected"}`
	drive("a 401 whose body echoes the bearer", identity)
	f.status, f.contentType, f.body = http.StatusOK, "text/csv", "actor,token\nada,private-refresh test-native\n"
	drive("a CSV answer whose cells carry token-shaped text", export)
	f.body = strings.Repeat("c", platformBodyCap+1)
	drive("an answer over the body cap", export)
	f.contentType, f.body = "application/json", "<html>gateway</html>"
	drive("a JSON-typed answer that is not JSON", identity)
	// The two arms that need the fixture broken come last: a platform that
	// cannot be reached, and a selection the gateway has rejected.
	down := httptest.NewServer(http.NotFoundHandler())
	unreachable := down.URL
	down.Close()
	desktopPlatformTransport = func(_ auth.Store, _ string) *auth.Transport {
		return auth.NewSyncTransport(unreachable, auth.StaticTokenSource{AccessToken: "test-native"}, auth.WithAccountSelection(f.selection))
	}
	drive("a platform that cannot be reached", identity)
	f.selection.MarkInvalid("account-A", "not a member")
	drive("an account-scoped route on a rejected selection", export)

	reached := map[string]bool{}
	for _, out := range outcomes {
		reached[platformOutcomeArm(out.result)] = true
		text, err := platformEmittedText(out.result, out.platformBody)
		if err != nil {
			t.Errorf("%s: the emitted document does not render: %v", out.name, err)
			continue
		}
		for _, forbidden := range []string{"private-refresh", "test-native"} {
			if strings.Contains(text, forbidden) {
				t.Errorf("%s emitted a credential-shaped value: %s", out.name, text)
			}
		}
		if strings.Contains(text, "Authorization") {
			t.Errorf("%s carries an authorization field: %s", out.name, text)
		}
		if out.result.Code != "" && out.result.State != "error" {
			t.Errorf("%s carries a code without the error state: %s", out.name, text)
		}
		// The vocabulary is a SET, not a string to search. Written as a
		// concatenation this assertion passed for any substring of it —
		// "auth", "error", "path_not" — so a leaked code shaped like a
		// fragment of a legitimate one was admitted.
		if out.result.Code != "" && !slices.Contains(platformOutcomeCodes, out.result.Code) {
			t.Errorf("%s: outcome code outside the vocabulary %v: %q", out.name, platformOutcomeCodes, out.result.Code)
		}
	}
	// THE ARM CENSUS: every arm the command can send was driven above, read from
	// the declared vocabulary and the two arm builders rather than from a list
	// kept beside the rows.
	for _, arm := range append(slices.Clone(platformOutcomeCodes), platformArmJSON, platformArmBinary) {
		if !reached[arm] {
			t.Errorf("no outcome drove %q, so the credential assertion never read that arm", arm)
		}
	}
	// THE KNOWN POSITIVE for the instrument, in the same run: a needle the
	// command wrote into its own field, or into a body it altered, is in the
	// text platformEmittedText renders, and the platform's verbatim body is not.
	leaked := base64.StdEncoding.EncodeToString([]byte("a,b\nprivate-refresh\n"))
	verbatim := base64.StdEncoding.EncodeToString([]byte("a,b\ntest-native\n"))
	for _, control := range []struct {
		name string
		out  desktopPlatformResult
		body string
		want string
	}{
		{name: "a needle in contentType", out: desktopPlatformResult{Status: 200, ContentType: "text/csv leak=private-refresh", BodyBase64: &verbatim}, body: "a,b\ntest-native\n", want: "private-refresh"},
		{name: "a needle in an altered binary body", out: desktopPlatformResult{Status: 200, ContentType: "text/csv", BodyBase64: &leaked}, body: "a,b\n", want: "private-refresh"},
		{name: "a needle in an altered JSON body", out: desktopPlatformResult{Status: 200, Body: json.RawMessage(`{"leak":"private-refresh"}`)}, body: `{}`, want: "private-refresh"},
		{name: "the platform's verbatim binary body", out: desktopPlatformResult{Status: 200, ContentType: "text/csv", BodyBase64: &verbatim}, body: "a,b\ntest-native\n", want: platformVerbatimBody},
	} {
		text, err := platformEmittedText(control.out, control.body)
		if err != nil {
			t.Errorf("platformEmittedText(%s) errored: %v", control.name, err)
			continue
		}
		if !strings.Contains(text, control.want) {
			t.Errorf("platformEmittedText(%s) = %s, want it to carry %q", control.name, text, control.want)
		}
		if control.want == platformVerbatimBody && strings.Contains(text, "test-native") {
			t.Errorf("platformEmittedText(%s) = %s, want the verbatim body exempted", control.name, text)
		}
	}
	// The bearer DID reach the platform on the admitted calls: the credential is
	// attached there and nowhere else, and this is the positive that proves the
	// zero above is not a zero because nothing was ever sent.
	if len(f.bearers) == 0 || f.bearers[0] == "" {
		t.Fatalf("no bearer was attached to the outbound request (bearers %d)", len(f.bearers))
	}
}
