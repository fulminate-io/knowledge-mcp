// SPDX-License-Identifier: Apache-2.0

package config

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// updateGoldens, when -update is passed to `go test`, rewrites every
// testdata/starter_*.golden file with the current Render output. Use it
// when intentionally changing the starter template; review the diff
// before committing.
var updateGoldens = flag.Bool("update", false, "rewrite testdata/starter_*.golden files")

// renderCases drive both the round-trip test and -update regeneration.
// The Model values are the defaults the auto-detector seeds into a
// freshly-rendered config (see autodetect.go's defaultModels). Keeping
// the canonical mapping here keeps render_test independent of
// autodetect.go's internals while still exercising the full provider
// matrix end-to-end.
var renderCases = []struct {
	name     string
	provider Provider
	model    Model
}{
	{name: "anthropic", provider: ProviderAnthropic, model: "claude-haiku-4-5-20251001"},
	{name: "openai", provider: ProviderOpenAI, model: "gpt-4o-mini"},
	{name: "gemini", provider: ProviderGemini, model: "gemini-2.5-flash"},
	{name: "claude-cli", provider: ProviderClaudeCLI, model: "claude-haiku-4-5"},
	{name: "codex-cli", provider: ProviderCodexCLI, model: "gpt-5.3-codex-spark"},
}

func TestRender_Goldens(t *testing.T) {
	for _, tc := range renderCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Render(DetectedProvider{Provider: tc.provider, Model: tc.model})
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			goldenPath := filepath.Join("testdata", "starter_"+tc.name+".golden")
			if *updateGoldens {
				if err := os.MkdirAll(filepath.Dir(goldenPath), 0o750); err != nil {
					t.Fatalf("mkdir testdata: %v", err)
				}
				if err := os.WriteFile(goldenPath, []byte(got), 0o600); err != nil {
					t.Fatalf("write %s: %v", goldenPath, err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden %s: %v (run `go test -run TestRender -update ./domains/config/` to seed)", goldenPath, err)
			}
			if got != string(want) {
				t.Errorf("starter mismatch for %s:\n--- got ---\n%s\n--- want ---\n%s", tc.name, got, want)
			}
		})
	}
}

// TestRenderStarter_Credentials proves the credential-writing path:
// a set voyage key produces a REAL uncommented voyage_api_key line,
// unset keys are omitted, and the output round-trips through Parse
// back to the same Credentials value.
func TestRenderStarter_Credentials(t *testing.T) {
	body, err := RenderStarter(
		DetectedProvider{Provider: ProviderAnthropic, Model: "claude-haiku-4-5-20251001"},
		Credentials{VoyageAPIKey: "voy-secret-123"},
	)
	if err != nil {
		t.Fatalf("RenderStarter: %v", err)
	}
	if !strings.Contains(body, `voyage_api_key    = "voy-secret-123"`) {
		t.Fatalf("missing uncommented voyage_api_key line:\n%s", body)
	}
	// A real (uncommented) [credentials] table — not the commented block.
	if !strings.Contains(body, "\n[credentials]\n") {
		t.Fatalf("expected a real [credentials] table:\n%s", body)
	}
	// Unset credential keys are omitted entirely (they only appear
	// lowercase inside the [credentials] table, never in the header
	// comments which use the uppercase env-var names).
	for _, omitted := range []string{"linear_api_key", "anthropic_api_key", "openai_api_key", "gemini_api_key"} {
		if strings.Contains(body, omitted) {
			t.Fatalf("unset credential %q must be omitted:\n%s", omitted, body)
		}
	}
	// Round-trips through Parse to the same value.
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse rendered starter: %v", err)
	}
	if cfg.Credentials == nil || cfg.Credentials.VoyageAPIKey != "voy-secret-123" {
		t.Fatalf("VoyageAPIKey did not round-trip: %+v", cfg.Credentials)
	}
}

func TestRender_InvalidProvider(t *testing.T) {
	_, err := Render(DetectedProvider{Provider: Provider("bedrock"), Model: "claude-3"})
	if err == nil {
		t.Fatal("Render(invalid provider): want error, got nil")
	}
}

func TestRender_EmptyModel(t *testing.T) {
	_, err := Render(DetectedProvider{Provider: ProviderAnthropic})
	if err == nil {
		t.Fatal("Render(empty model): want error, got nil")
	}
}
