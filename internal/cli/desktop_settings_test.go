// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
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
			t.Fatal("invalid edits accepted")
		}
		after, _ := os.ReadFile(p)
		if !bytes.Equal(before, after) {
			t.Fatal("invalid edit changed file")
		}
	}
	for _, key := range []string{"voyage_api_key", "linear_api_key", "anthropic_api_key", "openai_api_key", "gemini_api_key", "cohere_api_key"} {
		for _, secret := range []string{"quote\"slash\\\nfixture", "replacement", ""} {
			body := settingsJSON(t, map[string]any{"edits": map[string]any{"credentials": map[string]string{key: secret}}})
			r = run("save", string(body))
			if !r.Saved {
				t.Fatal("credential save failed")
			}
			encoded := settingsJSON(t, r)
			if secret != "" && bytes.Contains(encoded, []byte(secret)) {
				t.Fatal("credential leaked")
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
			t.Fatal("supported provider failed")
		}
	}
	for _, key := range settingsCredentials {
		t.Setenv(strings.ToUpper(key), "environment-fixture")
		for _, value := range []string{"first-fixture", "quote\"slash\\\nfixture", ""} {
			body := settingsJSON(t, map[string]any{"edits": map[string]any{"credentials": map[string]string{key: value}}})
			r := runDesktopSettings(p, "save", bytes.NewReader(body))
			if !r.Saved {
				t.Fatal("credential transition failed")
			}
			want := "saved"
			if value == "" {
				want = "environment"
			}
			if r.Credentials[key] != want {
				t.Fatal("credential precedence differs")
			}
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err = toml.Unmarshal(data, &doc); err != nil {
				t.Fatal("invalid saved TOML")
			}
			if doc["credentials"].(map[string]any)[key] != value {
				t.Fatal("credential round trip differs")
			}
			public := settingsJSON(t, r)
			encoded := settingsJSON(t, value)
			if value != "" && (bytes.Contains(public, []byte(value)) || bytes.Contains(public, encoded)) {
				t.Fatal("public credential disclosure")
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

func settingsJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal("fixture JSON encoding failed")
	}
	return data
}
