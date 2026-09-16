// SPDX-License-Identifier: Apache-2.0
package config

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/pelletier/go-toml/v2"
)

func settingPatch(t *testing.T, path []string, value any) SettingsPatch {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return SettingsPatch{Path: path, Value: raw}
}
func TestSettingsDocumentFieldCoverage(t *testing.T) {
	schema := MainSettingsMetadata()
	// The wanted map below pins the supported set two-sidedly: a field the
	// schema grew that is not listed is an error, and a listed field the schema
	// dropped is an error. A separate total would only add a literal to retype.
	wanted := map[string]string{"health_probe_interval": "string", "auto_update": "boolean", "schema_version": "integer"}
	for _, section := range []string{"default", "summarizer", "supervisor", "topics"} {
		for _, key := range []string{"provider", "model", "cli_bin", "base_url"} {
			wanted[section+"."+key] = "string"
		}
		if section != "default" {
			wanted[section+".fallback"] = "array"
		}
	}
	for _, key := range []string{"voyage_api_key", "linear_api_key", "anthropic_api_key", "openai_api_key", "gemini_api_key", "cohere_api_key"} {
		wanted["credentials."+key] = "string"
	}
	for _, section := range []string{"embedder", "embedder.profile.*", "reranker"} {
		for _, key := range []string{"provider", "model", "base_url", "key"} {
			wanted[section+"."+key] = "string"
		}
		if section != "reranker" {
			wanted[section+".dimension"] = "integer"
			wanted[section+".dtype"] = "string"
		}
	}
	wanted["embedder.family.*.profile"] = "string"
	for _, field := range schema.Fields {
		key := strings.Join(field.Path, ".")
		if wanted[key] != field.Type {
			t.Errorf("unexpected or mistyped schema field %s:%s", key, field.Type)
		}
		delete(wanted, key)
		if strings.HasPrefix(key, "credentials.") || strings.HasSuffix(key, ".key") {
			if !field.Secret {
				t.Errorf("unmarked secret %s", key)
			}
		}
	}
	if len(wanted) != 0 {
		t.Errorf("missing fields: %v", wanted)
	}
}
func TestSettingsDocumentTypedRoundTrip(t *testing.T) {
	original := []byte(`schema_version = 1
fulminate_account_id = "account-kept"
unknown = [1, 2]
[default]
provider = "openai"
model = "primary"
base_url = "http://127.0.0.1:1"
custom_extension = "kept"
[credentials]
openai_api_key = "stored-secret"
[unrelated]
secret_extension = "unknown-secret"
`)
	patches := []SettingsPatch{
		settingPatch(t, []string{"auto_update"}, false), settingPatch(t, []string{"health_probe_interval"}, "7m"),
		settingPatch(t, []string{"summarizer", "fallback"}, []any{map[string]any{"provider": "gemini", "model": "first"}, map[string]any{"provider": "openai", "model": "second"}}),
		settingPatch(t, []string{"embedder", "provider"}, "cohere"), settingPatch(t, []string{"embedder", "dimension"}, 1024), settingPatch(t, []string{"embedder", "dtype"}, "float32"), settingPatch(t, []string{"embedder", "key"}, "embedding-secret"),
		settingPatch(t, []string{"embedder", "profile", "local.test", "provider"}, "openai-compatible"), settingPatch(t, []string{"embedder", "profile", "local.test", "base_url"}, "http://127.0.0.1:2"), settingPatch(t, []string{"embedder", "profile", "local.test", "model"}, "local"), settingPatch(t, []string{"embedder", "profile", "local.test", "key"}, "profile-secret"),
		settingPatch(t, []string{"embedder", "family", "code", "profile"}, "local.test"), settingPatch(t, []string{"reranker", "provider"}, "voyage"), settingPatch(t, []string{"reranker", "model"}, "rank"), settingPatch(t, []string{"reranker", "key"}, "ranking-secret"),
	}
	edited, err := EditSettingsPatches(original, patches)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse(edited)
	if err != nil {
		t.Fatal("candidate not parseable")
	}
	chain, err := cfg.ResolveChain(ConsumerSummarizer)
	if err != nil || len(chain) != 3 || chain[1].Model != "first" || chain[2].Model != "second" {
		t.Fatal("ordered fallbacks lost")
	}
	if cfg.AutoUpdate == nil || *cfg.AutoUpdate || cfg.HealthProbeInterval.String() != "7m0s" {
		t.Fatal("typed top level lost")
	}
	prof, err := cfg.ResolveEmbedProfileForFamily("code")
	if err != nil || prof.Name != "local.test" || prof.Dimension != 256 {
		t.Fatal("profile incorrectly inherited default dimension")
	}
	var oldTree, newTree map[string]any
	if toml.Unmarshal(original, &oldTree) != nil || toml.Unmarshal(edited, &newTree) != nil {
		t.Fatal("tree decode failed")
	}
	for _, key := range []string{"schema_version", "fulminate_account_id", "unknown", "credentials", "unrelated"} {
		if diff := cmp.Diff(oldTree[key], newTree[key]); diff != "" {
			t.Errorf("unrelated %s changed (-original +edited):\n%s", key, diff)
		}
	}
	if newTree["default"].(map[string]any)["custom_extension"] != "kept" {
		t.Fatal("unknown section field lost")
	}
	view, err := ReadSettingsDocument(edited)
	if err != nil {
		t.Fatal(err)
	}
	public, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"stored-secret", "embedding-secret", "profile-secret", "ranking-secret", "unknown-secret"} {
		if bytes.Contains(public, []byte(secret)) {
			t.Error("secret disclosed")
		}
	}
	if _, ok := view.Configuration["unrelated"]; ok {
		t.Fatal("unknown table leaked")
	}
	effective := view.Effective["summarizer"].(map[string]any)
	if effective["model"] != "primary" || len(effective["fallback"].([]any)) != 2 {
		t.Fatal("effective inheritance lost")
	}
	if _, ok := view.Configuration["supervisor"]; ok {
		t.Fatal("unset became configured")
	}
}
func TestSettingsDocumentSecretsRemovalAndValidation(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "environment-secret")
	data := []byte("[embedder]\nprovider='openai-compatible'\nkey='section-secret'\n[credentials]\nopenai_api_key='credential-secret'\n")
	for _, tc := range []struct {
		patch  SettingsPatch
		source string
	}{
		{patch: SettingsPatch{Path: []string{"embedder", "key"}, Remove: true}, source: "credentials"},
		{patch: SettingsPatch{Path: []string{"credentials", "openai_api_key"}, Remove: true}, source: "environment"},
	} {
		edited, err := EditSettingsPatches(data, []SettingsPatch{tc.patch})
		if err != nil {
			t.Error(err)
			continue
		}
		data = edited
		view, err := ReadSettingsDocument(data)
		if err != nil {
			t.Error(err)
			continue
		}
		found := false
		for _, secret := range view.SecretStatus {
			if cmp.Equal(secret.Path, []string{"embedder", "key"}) {
				found = true
				if secret.Configured || !secret.Present || secret.Source != tc.source {
					t.Errorf("secret provenance wrong: %+v", secret)
				}
			}
		}
		if !found {
			t.Errorf("removing %v left no status for embedder.key", tc.patch.Path)
		}
	}
	for _, patch := range []SettingsPatch{
		settingPatch(t, []string{"auto_update"}, "false"), settingPatch(t, []string{"embedder", "dimension"}, 512.5), settingPatch(t, []string{"embedder", "dimension"}, 100), settingPatch(t, []string{"embedder", "dtype"}, "bad"), settingPatch(t, []string{"health_probe_interval"}, "secret-invalid-duration"),
		settingPatch(t, []string{"schema_version"}, 1), settingPatch(t, []string{"fulminate_account_id"}, "other"), settingPatch(t, []string{"default", "fallback"}, []any{}), settingPatch(t, []string{"embedder", "family", "code", "profile"}, "missing"), settingPatch(t, []string{"embedder", "profile", "default", "provider"}, "voyage"),
		settingPatch(t, []string{"summarizer", "fallback"}, []any{map[string]any{"fallback": []any{}}}),
	} {
		if _, err := EditSettingsPatches(data, []SettingsPatch{patch}); err == nil {
			t.Errorf("invalid path/value accepted: %v", patch.Path)
		} else if strings.Contains(err.Error(), "secret-invalid") {
			t.Error("validation echoed value")
		}
	}
}

func TestSettingsDocumentProfileRemoval(t *testing.T) {
	data := []byte("[embedder.profile.local]\nprovider='fake'\nkey='profile-secret'\n[embedder.family.code]\nprofile='local'\n")
	remove := SettingsPatch{Path: []string{"embedder", "profile", "local"}, Remove: true}
	if _, err := EditSettingsPatches(data, []SettingsPatch{remove}); err == nil {
		t.Fatal("removed a referenced profile")
	}
	edited, err := EditSettingsPatches(data, []SettingsPatch{{Path: []string{"embedder", "family", "code"}, Remove: true}, remove})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse(edited)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := cfg.ResolveEmbedProfileForFamily("code")
	if err != nil || profile.Name != "default" || len(cfg.EmbedProfiles) != 0 {
		t.Fatal("profile removal failed to restore default assignment")
	}
	if bytes.Contains(edited, []byte("profile-secret")) {
		t.Fatal("removed profile key retained")
	}
	empty, err := EditSettingsPatches(nil, []SettingsPatch{{Path: []string{"embedder", "family", "code", "profile"}, Remove: true}})
	if err != nil || len(bytes.TrimSpace(empty)) != 0 {
		t.Fatal("removing an unset value created a table")
	}
}
