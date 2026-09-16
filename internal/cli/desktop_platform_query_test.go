// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// platformWire marshals one outcome and returns its KEYS and its values as the
// Desktop wall receives them. The seam is a JSON document over stdout, so the
// arm assertions read that document rather than the Go struct: a field the
// struct carries and the encoder omits would otherwise pass here and be refused
// at the other wall.
func platformWire(t *testing.T, out desktopPlatformResult) map[string]any {
	t.Helper()
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("json.Marshal(%+v) errored: %v", out, err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("json.Unmarshal(%s) errored: %v", raw, err)
	}
	return wire
}

func platformWireKeys(wire map[string]any) []string {
	keys := make([]string, 0, len(wire))
	for key := range wire {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// TestDesktopPlatformQueryCarried pins requirement 1's success arm: a query
// whose keys are structurally admissible reaches the platform encoded by THIS
// command, with every pair intact.
//
// The two hand-written callers of the website are the rows: the audit-log export
// with all six of its filters, and the invoice PDF with download=true. The
// assertion reads the query off the request the stub platform recorded, so it is
// a statement about the URL that was sent rather than about the request that was
// built.
func TestDesktopPlatformQueryCarried(t *testing.T) {
	f := newPlatformFixture(t, "account-A")
	for _, row := range []struct {
		name    string
		request string
		want    map[string]string
	}{
		{
			name:    "the audit export with all six filters",
			request: `{"method":"GET","path":"/v1/accounts/{account_id}/audit-logs/export","params":{"account_id":"account-A"},"query":{"action":"member.invited","resource_type":"member","severity":"info","actor_email":"ada@example.com","start_time":"2026-09-01T00:00:00Z","end_time":"2026-09-13T23:59:59Z"}}`,
			want: map[string]string{
				"action":        "member.invited",
				"resource_type": "member",
				"severity":      "info",
				"actor_email":   "ada@example.com",
				"start_time":    "2026-09-01T00:00:00Z",
				"end_time":      "2026-09-13T23:59:59Z",
			},
		},
		{
			name:    "the invoice pdf download flag",
			request: `{"method":"GET","path":"/v1/accounts/{account_id}/billing/invoices/{invoice_id}/pdf","params":{"account_id":"account-A","invoice_id":"in_1"},"query":{"download":"true"}}`,
			want:    map[string]string{"download": "true"},
		},
		{
			name:    "a required key whose value is the empty string",
			request: `{"method":"GET","path":"/v1/accounts/{account_id}/usage/slow-tool-calls","params":{"account_id":"account-A"},"query":{"tool":""}}`,
			want:    map[string]string{"tool": ""},
		},
		{
			name:    "an absent query sends none",
			request: `{"method":"GET","path":"/v1/accounts/{account_id}/usage","params":{"account_id":"account-A"}}`,
			want:    map[string]string{},
		},
		{
			name:    "an empty query object sends none",
			request: `{"method":"GET","path":"/v1/accounts/{account_id}/usage","params":{"account_id":"account-A"},"query":{}}`,
			want:    map[string]string{},
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			before := len(f.calls)
			got := f.run(t, row.request)
			if got.Code != "" || got.Status != http.StatusOK {
				t.Fatalf("runDesktopPlatform(%s) = %+v, want the platform's 200", row.name, got)
			}
			if len(f.calls) != before+1 {
				t.Fatalf("runDesktopPlatform(%s) issued %d requests, want 1", row.name, len(f.calls)-before)
			}
			issued := f.calls[len(f.calls)-1]
			values := issued.URL.Query()
			if len(values) != len(row.want) {
				t.Errorf("the platform received %d query keys, want %d (raw %q)", len(values), len(row.want), f.queries[len(f.queries)-1])
			}
			for key, want := range row.want {
				if !values.Has(key) {
					t.Errorf("the platform received no %q key (raw %q)", key, f.queries[len(f.queries)-1])
					continue
				}
				if got := values.Get(key); got != want {
					t.Errorf("the platform received %s=%q, want %q", key, got, want)
				}
			}
		})
	}
}

// TestDesktopPlatformQueryValueVocabulary pins the value vocabulary as a
// matrix. The empty string and the two-dot value are the cells that fail if the
// query is bounded by a path-segment charset, and they are cells rather than
// prose because the website legitimately sends both: an actor_email filter is
// free user text and a timestamp carries colons.
func TestDesktopPlatformQueryValueVocabulary(t *testing.T) {
	f := newPlatformFixture(t, "account-A")
	admitted := []struct {
		name  string
		value string
	}{
		{name: "the empty string", value: ""},
		{name: "an RFC3339 timestamp with colons", value: "2026-09-13T23:59:59Z"},
		{name: "an email address", value: "ada@example.com"},
		{name: "a value carrying two dots", value: "../etc/passwd"},
		{name: "a value carrying a double slash", value: "a//b"},
		{name: "a value carrying a question mark", value: "a?b=c"},
		{name: "a value carrying an ampersand", value: "a&b=c"},
		{name: "a value carrying a hash", value: "a#b"},
		{name: "a value carrying a space", value: "two words"},
		{name: "a value carrying non-ASCII text", value: "Ada Lovelace éè"},
		{name: "a value at the byte bound", value: strings.Repeat("v", platformQueryValueLimit)},
	}
	for _, row := range admitted {
		t.Run("admitted: "+row.name, func(t *testing.T) {
			value, err := json.Marshal(row.value)
			if err != nil {
				t.Fatalf("json.Marshal(%q) errored: %v", row.value, err)
			}
			before := len(f.calls)
			got := f.run(t, `{"method":"GET","path":"/v1/accounts/{account_id}/audit-logs/export","params":{"account_id":"account-A"},"query":{"actor_email":`+string(value)+`}}`)
			if got.Code != "" {
				t.Fatalf("runDesktopPlatform(actor_email=%q) = %+v, want an admitted request", row.value, got)
			}
			if len(f.calls) != before+1 {
				t.Fatalf("runDesktopPlatform(actor_email=%q) issued %d requests, want 1", row.value, len(f.calls)-before)
			}
			// The DECODED value is what the platform's own handler reads, so
			// that is what the assertion compares: an encoder that escapes the
			// dots, the slashes or the space passes this row, and one that drops
			// or mangles the value does not.
			if delivered := f.calls[len(f.calls)-1].URL.Query().Get("actor_email"); delivered != row.value {
				t.Errorf("the platform received actor_email=%q, want %q (raw %q)", delivered, row.value, f.queries[len(f.queries)-1])
			}
		})
	}
	refused := []struct {
		name    string
		request string
	}{
		{name: "a value one byte over the bound", request: `{"method":"GET","path":"/v1/accounts/{account_id}/audit-logs/export","params":{"account_id":"account-A"},"query":{"actor_email":"` + strings.Repeat("v", platformQueryValueLimit+1) + `"}}`},
		{name: "a numeric value", request: `{"method":"GET","path":"/v1/accounts/{account_id}/audit-logs/export","params":{"account_id":"account-A"},"query":{"severity":1}}`},
		{name: "a boolean value", request: `{"method":"GET","path":"/v1/accounts/{account_id}/billing/invoices/{invoice_id}/pdf","params":{"account_id":"account-A","invoice_id":"in_1"},"query":{"download":true}}`},
		{name: "a null value", request: `{"method":"GET","path":"/v1/accounts/{account_id}/audit-logs/export","params":{"account_id":"account-A"},"query":{"severity":null}}`},
		{name: "an object value", request: `{"method":"GET","path":"/v1/accounts/{account_id}/audit-logs/export","params":{"account_id":"account-A"},"query":{"severity":{"eq":"info"}}}`},
		{name: "an array value", request: `{"method":"GET","path":"/v1/accounts/{account_id}/audit-logs/export","params":{"account_id":"account-A"},"query":{"severity":["info"]}}`},
		{name: "a key naming a path segment", request: `{"method":"GET","path":"/v1/accounts/{account_id}/audit-logs/export","params":{"account_id":"account-A"},"query":{"a/b":"c"}}`},
		{name: "a key carrying an equals sign", request: `{"method":"GET","path":"/v1/accounts/{account_id}/audit-logs/export","params":{"account_id":"account-A"},"query":{"a=b":"c"}}`},
		{name: "a key carrying an ampersand", request: `{"method":"GET","path":"/v1/accounts/{account_id}/audit-logs/export","params":{"account_id":"account-A"},"query":{"a&b":"c"}}`},
		{name: "an empty key", request: `{"method":"GET","path":"/v1/accounts/{account_id}/audit-logs/export","params":{"account_id":"account-A"},"query":{"":"c"}}`},
		{name: "a key one byte over the name bound", request: `{"method":"GET","path":"/v1/accounts/{account_id}/audit-logs/export","params":{"account_id":"account-A"},"query":{"k` + strings.Repeat("e", platformQueryKeyLimit) + `":"c"}}`},
		// THE FIRST CHARACTER CLASS IS NARROWER THAN THE REST, and these are the
		// two spellings that says: a name may carry a digit or an underscore, and
		// may not START with one.
		{name: "a key with a leading digit", request: `{"method":"GET","path":"/v1/accounts/{account_id}/audit-logs/export","params":{"account_id":"account-A"},"query":{"1severity":"c"}}`},
		{name: "a key with a leading underscore", request: `{"method":"GET","path":"/v1/accounts/{account_id}/audit-logs/export","params":{"account_id":"account-A"},"query":{"_severity":"c"}}`},
		{name: "a key carrying a question mark", request: `{"method":"GET","path":"/v1/accounts/{account_id}/audit-logs/export","params":{"account_id":"account-A"},"query":{"a?b":"c"}}`},
		{name: "a key carrying a hash", request: `{"method":"GET","path":"/v1/accounts/{account_id}/audit-logs/export","params":{"account_id":"account-A"},"query":{"a#b":"c"}}`},
		{name: "a query string instead of an object", request: `{"method":"GET","path":"/v1/accounts/{account_id}/audit-logs/export","params":{"account_id":"account-A"},"query":"severity=info"}`},
	}
	for _, row := range refused {
		t.Run("refused: "+row.name, func(t *testing.T) {
			before := len(f.calls)
			got := f.run(t, row.request)
			if got.Code != platformCodeInvalidRequest {
				t.Errorf("runDesktopPlatform(%s) = %+v, want %q", row.name, got, platformCodeInvalidRequest)
			}
			if len(f.calls) != before {
				t.Errorf("runDesktopPlatform(%s) reached the platform (raw %q)", row.name, f.queries[len(f.queries)-1])
			}
		})
	}
	// THE KEY BOUND IS OBSERVED ON BOTH SIDES, as the value bound above is: the
	// refused table drives one byte over, and this drives the bound itself.
	// Both fixtures are computed from the declaration, so a repeat count off by
	// one moves the one-over refused row or this at-bound row rather than
	// neither.
	atBound := "k" + strings.Repeat("e", platformQueryKeyLimit-1)
	if len(atBound) != platformQueryKeyLimit {
		t.Fatalf("the at-bound key fixture is %d bytes, not the %d the declaration admits", len(atBound), platformQueryKeyLimit)
	}
	for _, key := range []string{atBound, "k", "a.b-c_1"} {
		issued := len(f.calls)
		got := f.run(t, `{"method":"GET","path":"/v1/accounts/{account_id}/audit-logs/export","params":{"account_id":"account-A"},"query":{"`+key+`":"kept"}}`)
		if got.Code != "" || len(f.calls) != issued+1 {
			t.Errorf("runDesktopPlatform(a %d-byte key %.20q) = %+v, want an admitted request (calls %d)", len(key), key, got, len(f.calls)-issued)
			continue
		}
		if delivered := f.calls[len(f.calls)-1].URL.Query().Get(key); delivered != "kept" {
			t.Errorf("the platform received %.20q=%q, want %q (raw %q)", key, delivered, "kept", f.queries[len(f.queries)-1])
		}
	}
	// The same-run known positive: the instrument that recorded zero calls for
	// every refusal records one for an admitted request.
	before := len(f.calls)
	if got := f.run(t, `{"method":"GET","path":"/v1/accounts/{account_id}/audit-logs/export","params":{"account_id":"account-A"},"query":{"severity":"info"}}`); got.Code != "" || len(f.calls) != before+1 {
		t.Fatalf("the known positive did not reach the platform: %+v (calls %d)", got, len(f.calls)-before)
	}
}
