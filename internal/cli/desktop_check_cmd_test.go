// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// recordingReader reports whether anything read from it, so a test can
// assert that a refused argv never touched the request body.
type recordingReader struct{ read bool }

func (r *recordingReader) Read(p []byte) (int, error) {
	r.read = true
	return 0, io.EOF
}

func TestDesktopCheckCmdRejectsBadArguments(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		wantRead bool
	}{
		{name: "NoAxis", args: nil},
		{name: "UnknownAxis", args: []string{"reranker", "--config-file", "/tmp/config"}},
		{name: "RelativeConfigPath", args: []string{"summarizer", "--config-file", "config"}},
		{name: "NoConfigPath", args: []string{"summarizer"}},
		{name: "PositionalArgument", args: []string{"summarizer", "--config-file", "/tmp/config", "extra"}},
		{name: "UnknownFlag", args: []string{"summarizer", "--key", "x", "--config-file", "/tmp/config"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := &recordingReader{}
			var out strings.Builder
			if err := runDesktopCheckCmd(t.Context(), tc.args, input, &out); err == nil {
				t.Errorf("runDesktopCheckCmd(%v) returned no error; bad input always errors", tc.args)
			}
			if input.read != tc.wantRead {
				t.Errorf("runDesktopCheckCmd(%v) read the body = %v, want %v — an argv refused before the body must not wait on a pipe", tc.args, input.read, tc.wantRead)
			}
			if out.Len() != 0 {
				t.Errorf("runDesktopCheckCmd(%v) wrote %d bytes to stdout; a refused command writes no result object", tc.args, out.Len())
			}
		})
	}
}

// TestDesktopCheckCmdNeverWritesTheConfiguration is the check-before-write
// order asserted at the layer the Desktop actually invokes — the whole
// command, argv and stdin and stdout — rather than at the dispatch
// underneath it. It is the outer half of the pair: a write introduced
// anywhere in the command, not just in one axis, turns this red.
//
// The accepted arm is the control. A command that wrote nothing because it
// never reached a provider would pass the rejected arm on its own.
func TestDesktopCheckCmdNeverWritesTheConfiguration(t *testing.T) {
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
		{name: "UnreachableProvider", status: http.StatusInternalServerError, listBody: `boom`, wantOutcome: "unreachable"},
		{name: "AcceptedKeyControl", status: http.StatusOK, listBody: `{"data":[{"id":"gpt-5"}]}`, wantOutcome: "accepted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := scratchConfig(t, body)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read scratch config: %v", err)
			}
			srv, _, _ := modelListStub(t, tc.status, tc.listBody)
			request := `{"provider":"openai","key":"` + checkFixtureKey + `","base_url":"` + srv.URL + `"}`
			var out strings.Builder
			if err := runDesktopCheckCmd(t.Context(), []string{"summarizer", "--config-file", path}, strings.NewReader(request), &out); err != nil {
				t.Fatalf("runDesktopCheckCmd returned an error: %v", err)
			}
			var got desktopCheckResult
			if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
				t.Fatalf("stdout was not one JSON object: %v", err)
			}
			if got.Outcome != tc.wantOutcome {
				t.Errorf("outcome = %q (%s), want %q", got.Outcome, got.Reason, tc.wantOutcome)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("re-read scratch config: %v", err)
			}
			if string(before) != string(after) {
				t.Errorf("the command changed the configuration: %d bytes before, %d after — the check must write nothing", len(before), len(after))
			}
			if strings.Contains(string(after), checkFixtureKey) {
				t.Errorf("the configuration carries the pasted credential after a check; it is %d bytes and must carry none of it", len(after))
			}
		})
	}
}

// TestDesktopCheckCmdWritesOneJSONObject pins the wire contract the
// Desktop's response validator parses: one object on stdout, an outcome
// from the four-word vocabulary, and a models array.
func TestDesktopCheckCmdWritesOneJSONObject(t *testing.T) {
	srv, _, _ := modelListStub(t, http.StatusOK, `{"data":[{"id":"gpt-5"}]}`)
	path := scratchConfig(t, "[default]\nprovider = \"openai\"\nmodel = \"gpt-5\"\n")
	body := `{"provider":"openai","key":"` + checkFixtureKey + `","base_url":"` + srv.URL + `"}`
	var out strings.Builder
	if err := runDesktopCheckCmd(t.Context(), []string{"summarizer", "--config-file", path}, strings.NewReader(body), &out); err != nil {
		t.Fatalf("runDesktopCheckCmd returned an error: %v", err)
	}
	var got desktopCheckResult
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("stdout was not one JSON object (%d bytes): %v", out.Len(), err)
	}
	if got.Outcome != "accepted" || len(got.Models) != 1 || got.Models[0] != "gpt-5" {
		t.Errorf("stdout object = %+v, want accepted with the stub's one model", got)
	}
	if strings.Contains(out.String(), checkFixtureKey) {
		t.Errorf("the result object echoed the pasted credential; it is %d bytes and must carry none of it", out.Len())
	}
}
