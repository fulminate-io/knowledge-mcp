// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/pelletier/go-toml/v2"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

func TestDesktopSettingsReadSave(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config")
	run := func(op, body string) desktopSettingsResult {
		t.Helper()
		r := runDesktopSettings(p, op, strings.NewReader(body))
		return r
	}
	if r := run("read", ""); r.State != "missing" {
		t.Fatal("missing file not reported")
	}
	if _, e := os.Stat(p); !os.IsNotExist(e) {
		t.Fatal("read created file")
	}
	r := run("save", `{"edits":{"default":{"provider":"openai","model":"unicode 雪 model","base_url":"http://127.0.0.1:1"}}}`)
	if r.Code != "" || !r.Saved {
		t.Fatalf("save failed: %s", r.Code)
	}
	if r = run("read", ""); r.Sections["summarizer"].Effective["model"] != "unicode 雪 model" {
		t.Fatal("inheritance missing")
	}
	before, _ := os.ReadFile(p)
	for _, body := range []string{`{"edits":{"default":{"provider":"wrong"}}}`, `{"edits":{"default":{"model":""}}}`, `{"edits":{"default":{"provider":"codex-cli","cli_bin":"/missing"}}}`, `{"path":"/other","edits":{}}`, `{"edits":{"other":{"model":"x"}}}`} {
		if run("save", body).Code == "" {
			t.Errorf("invalid edits accepted: %s", body)
			continue
		}
		after, _ := os.ReadFile(p)
		if !bytes.Equal(before, after) {
			t.Errorf("refused edit changed the file: %s", body)
		}
	}
	for _, key := range []string{"voyage_api_key", "linear_api_key", "anthropic_api_key", "openai_api_key", "gemini_api_key", "cohere_api_key"} {
		for _, secret := range []string{"quote\"slash\\\nfixture", "replacement", ""} {
			body := settingsJSON(t, map[string]any{"edits": map[string]any{"credentials": map[string]string{key: secret}}})
			r = run("save", string(body))
			if !r.Saved {
				t.Errorf("credential save failed for %s: %s", key, r.Code)
				continue
			}
			encoded := settingsJSON(t, r)
			if secret != "" && bytes.Contains(encoded, []byte(secret)) {
				t.Errorf("save response for %s disclosed the credential", key)
			}
		}
	}
	if err := os.WriteFile(p, []byte("[credentials]\nopenai_api_key = \"unterminated-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if r = run("read", ""); r.Code != "config_invalid" {
		t.Fatal("malformed file not reported")
	}
	raw := settingsJSON(t, r)
	if bytes.Contains(raw, []byte("unterminated")) {
		t.Fatal("parser leaked input")
	}
}

func TestDesktopSettingsProvidersAndSources(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config")
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"anthropic", "openai", "gemini", "claude-cli", "codex-cli"} {
		body := settingsJSON(t, map[string]any{"edits": map[string]any{"default": map[string]string{"provider": provider, "model": "free model 雪", "base_url": "http://127.0.0.1:1", "cli_bin": bin}}})
		if r := runDesktopSettings(p, "save", bytes.NewReader(body)); !r.Saved {
			t.Errorf("supported provider %s failed: %s", provider, r.Code)
		}
	}
	for _, key := range settingsCredentials {
		t.Setenv(strings.ToUpper(key), "environment-fixture")
		for _, value := range []string{"first-fixture", "quote\"slash\\\nfixture", ""} {
			body := settingsJSON(t, map[string]any{"edits": map[string]any{"credentials": map[string]string{key: value}}})
			r := runDesktopSettings(p, "save", bytes.NewReader(body))
			if !r.Saved {
				t.Errorf("credential transition failed for %s: %s", key, r.Code)
				continue
			}
			want := "saved"
			if value == "" {
				want = "environment"
			}
			if r.Credentials[key] != want {
				t.Errorf("%s source=%s want %s", key, r.Credentials[key], want)
			}
			data, err := os.ReadFile(p)
			if err != nil {
				t.Error(err)
				continue
			}
			var doc map[string]any
			if err = toml.Unmarshal(data, &doc); err != nil {
				t.Errorf("saved file for %s is not valid TOML: %v", key, err)
				continue
			}
			if doc["credentials"].(map[string]any)[key] != value {
				t.Errorf("%s did not round trip through the saved file", key)
			}
			public := settingsJSON(t, r)
			encoded := settingsJSON(t, value)
			if value != "" && (bytes.Contains(public, []byte(value)) || bytes.Contains(public, encoded)) {
				t.Errorf("public response disclosed the %s value", key)
			}
		}
	}
	body := `{"edits":{"topics":{"model":"override"}}}`
	if !runDesktopSettings(p, "save", strings.NewReader(body)).Saved {
		t.Fatal("override save")
	}
	if !runDesktopSettings(p, "save", strings.NewReader(`{"edits":{"topics":{"model":""}}}`)).Saved {
		t.Fatal("override clear")
	}
	if r := runDesktopSettings(p, "read", nil); r.Sections["topics"].Effective["model"] != "free model 雪" {
		t.Fatal("clear did not restore inheritance")
	}
	if r := runDesktopSettings(filepath.Dir(p), "read", nil); r.Code != "config_unreadable" {
		t.Fatal("directory read did not fail")
	}
}

// TestDesktopSettingsConsumerGateAppliesToBothBodies pins ONE rule across both
// request bodies. The consumer gate is the only thing standing between a form
// submission and a configuration whose summarizer can no longer resolve, and
// the legacy section edits and the typed patches reach it through different
// code, so a gate wired to one body refuses a change through that body and
// saves the same change through the other.
//
// The rule is a comparison, not a test of the candidate: a save is refused only
// when the candidate cannot resolve a consumer the PRIOR document could. Every
// row therefore names BOTH sets, so a row cannot pass on the wrong mechanism —
// the refusal rows are refusals because the set GREW, and the accepted rows are
// accepted even though the candidate set is not empty.
func TestDesktopSettingsConsumerGateAppliesToBothBodies(t *testing.T) {
	// allResolve passes consumer validation as it stands: an API provider needs
	// a key or a base_url, and every consumer inherits a model from default.
	// Clearing default.model leaves summarizer with a provider and no model, so
	// nothing resolves — a genuine clean-to-broken transition.
	const allResolve = `[default]
provider = 'openai'
model = 'm'
base_url = 'http://127.0.0.1:1'

[summarizer]
provider = 'anthropic'
base_url = 'http://127.0.0.1:2'
`
	// topicsBroken starts with ONE consumer already failing: topics names a CLI
	// provider and no cli_bin is set anywhere. Clearing default.model breaks the
	// other two on top of it, which is the case a set-emptiness test cannot
	// tell apart from the row below it.
	const topicsBroken = allResolve + `
[topics]
provider = 'codex-cli'
`
	// keyOnly resolves only because the credentials table carries the key its
	// default provider needs. Clearing that key breaks all three consumers, so
	// the credential rows are a live exemption rather than a vacuous one.
	const keyOnly = `[default]
provider = 'openai'
model = 'm'

[credentials]
openai_api_key = 'stored-fixture'
`
	every := []string{"summarizer", "supervisor", "topics"}
	for _, tc := range []struct {
		name string
		file string
		body string
		// wantPrior and wantCandidate are the consumers that fail validation
		// before and after the change. The gate's verdict is a function of the
		// two, so a row that does not name both proves nothing about which
		// mechanism produced its code.
		wantPrior     []string
		wantCandidate []string
		wantCode      string
		wantSaved     bool
	}{
		{name: "edits break a resolved consumer", file: allResolve, body: `{"edits":{"default":{"model":""}}}`, wantCandidate: every, wantCode: "invalid_settings"},
		{name: "patches break a resolved consumer", file: allResolve, body: `{"patches":[{"path":["default","model"],"remove":true}]}`, wantCandidate: every, wantCode: "invalid_settings"},
		{name: "edits break a consumer beside one already broken", file: topicsBroken, body: `{"edits":{"default":{"model":""}}}`, wantPrior: []string{"topics"}, wantCandidate: every, wantCode: "invalid_settings"},
		{name: "patches break a consumer beside one already broken", file: topicsBroken, body: `{"patches":[{"path":["default","model"],"remove":true}]}`, wantPrior: []string{"topics"}, wantCandidate: every, wantCode: "invalid_settings"},
		{name: "edits keep every consumer resolvable", file: allResolve, body: `{"edits":{"topics":{"model":"override"}}}`, wantSaved: true},
		{name: "patches keep every consumer resolvable", file: allResolve, body: `{"patches":[{"path":["topics","model"],"value":"override"}]}`, wantSaved: true},
		{name: "edits clear the key every consumer needs", file: keyOnly, body: `{"edits":{"credentials":{"openai_api_key":""}}}`, wantCandidate: every, wantSaved: true},
		{name: "patches clear the key every consumer needs", file: keyOnly, body: `{"patches":[{"path":["credentials","openai_api_key"],"remove":true}]}`, wantCandidate: every, wantSaved: true},
		// A first-run configuration resolves NO consumer, so these rows are the
		// whole reason the rule compares against the prior document: the user
		// has not reached summarizer yet and is not told about it.
		{name: "edits on an unconfigured file", file: "", body: `{"edits":{"topics":{"model":"override"}}}`, wantPrior: every, wantCandidate: every, wantSaved: true},
		{name: "patches on an unconfigured file", file: "", body: `{"patches":[{"path":["auto_update"],"value":false}]}`, wantPrior: every, wantCandidate: every, wantSaved: true},
		{name: "edits configure one consumer on an unconfigured file", file: "", body: `{"edits":{"summarizer":{"provider":"anthropic","model":"m","base_url":"http://127.0.0.1:2"}}}`, wantPrior: every, wantCandidate: []string{"supervisor", "topics"}, wantSaved: true},
		{name: "patches configure one consumer on an unconfigured file", file: "", body: `{"patches":[{"path":["summarizer","provider"],"value":"anthropic"},{"path":["summarizer","model"],"value":"m"},{"path":["summarizer","base_url"],"value":"http://127.0.0.1:2"}]}`, wantPrior: every, wantCandidate: []string{"supervisor", "topics"}, wantSaved: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The fixtures that turn on a missing key must not read one from
			// the machine running the test.
			for _, key := range settingsCredentials {
				t.Setenv(strings.ToUpper(key), "")
			}
			p := filepath.Join(t.TempDir(), "config")
			if err := os.WriteFile(p, []byte(tc.file), 0600); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.wantPrior, runDesktopSettings(p, "read", nil).Validation); diff != "" {
				t.Fatalf("the fixture is not in the prior state this row tests (-want +got):\n%s", diff)
			}
			got := runDesktopSettings(p, "save", strings.NewReader(tc.body))
			if got.Code != tc.wantCode || got.Saved != tc.wantSaved {
				t.Fatalf("code=%q saved=%v want code=%q saved=%v", got.Code, got.Saved, tc.wantCode, tc.wantSaved)
			}
			if diff := cmp.Diff(tc.wantCandidate, got.Validation); diff != "" {
				t.Fatalf("candidate validation (-want +got):\n%s", diff)
			}
			after, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.wantSaved && string(after) != tc.file {
				t.Fatalf("refused save rewrote the file: %q", string(after))
			}
			if tc.wantSaved && string(after) == tc.file {
				t.Fatal("accepted save left the file unchanged, so it never reached the writer")
			}
		})
	}
}

func settingsJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal("fixture JSON encoding failed")
	}
	return data
}

func TestDesktopSettingsTypedPatches(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config")
	// A missing file, which is the first-run case: the consumer gate compares
	// against the prior document, so a typed patch that reaches no consumer is
	// not refused for the consumers an empty file was never going to resolve
	// (TestDesktopSettingsConsumerGateAppliesToBothBodies).
	body := `{"patches":[{"path":["auto_update"],"value":false},{"path":["embedder","provider"],"value":"fake"},{"path":["embedder","dimension"],"value":512},{"path":["credentials","linear_api_key"],"value":"private-linear"}]}`
	result := runDesktopSettings(p, "save", strings.NewReader(body))
	if !result.Saved || result.SettingsDocument == nil || len(result.Schema.Fields) != len(config.MainSettingsMetadata().Fields) {
		t.Fatalf("typed save failed: %s", result.Code)
	}
	if result.Configuration["auto_update"] != false {
		t.Fatal("false became unset")
	}
	public := settingsJSON(t, result)
	if bytes.Contains(public, []byte("private-linear")) {
		t.Fatal("save response leaks secret")
	}
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{"patches":[{"path":["auto_update"],"value":"false"}]}`,
		`{"patches":[{"path":["embedder","dimension"],"value":-1}]}`,
		`{"patches":[{"path":["auto_update"],"value":false,"remove":true}]}`,
		`{"patches":[{"path":["auto_update"],"value":false,"unknown":true}]}`,
		`{"patches":[{"path":["auto_update"],"value":false}],"edits":{"default":{"model":"x"}}}`,
		body + strings.Repeat(" ", 65536),
	} {
		if got := runDesktopSettings(p, "save", strings.NewReader(bad)); got.Code == "" {
			t.Errorf("invalid typed save succeeded: %s", bad)
		}
		after, err := os.ReadFile(p)
		if err != nil {
			t.Error(err)
			continue
		}
		if !bytes.Equal(before, after) {
			t.Errorf("refused typed save changed the file: %s", bad)
		}
	}
	removed := runDesktopSettings(p, "save", strings.NewReader(`{"patches":[{"path":["auto_update"],"remove":true}]}`))
	if !removed.Saved || removed.Effective["auto_update"] != true {
		t.Fatal("unset did not restore update default")
	}
	if _, exists := removed.Configuration["auto_update"]; exists {
		t.Fatal("removed value still configured")
	}
}
