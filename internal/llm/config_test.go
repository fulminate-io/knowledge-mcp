package llm

import (
	"errors"
	"testing"
)

func TestConfig_Validate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *Config
		wantErr error // nil = no error; ErrInvalidConfig = expect that sentinel
	}{
		{"nil", nil, ErrInvalidConfig},
		{"empty provider", &Config{}, ErrInvalidConfig},
		{"unknown provider", &Config{Provider: "fake-provider"}, ErrInvalidConfig},
		{"openai missing key", &Config{Provider: ProviderOpenAI}, ErrInvalidConfig},
		{"anthropic missing key", &Config{Provider: ProviderAnthropic}, ErrInvalidConfig},
		{"gemini missing key", &Config{Provider: ProviderGemini}, ErrInvalidConfig},
		{"openai with key", &Config{Provider: ProviderOpenAI, APIKey: "sk-..."}, nil},
		{"anthropic with key", &Config{Provider: ProviderAnthropic, APIKey: "sk-..."}, nil},
		{"gemini with key", &Config{Provider: ProviderGemini, APIKey: "..."}, nil},
		{"claude-cli no fields", &Config{Provider: ProviderClaudeCLI}, nil},
		{"codex-cli no fields", &Config{Provider: ProviderCodexCLI}, nil},
		{"claude-cli with override bin", &Config{Provider: ProviderClaudeCLI, CLIBin: "/tmp/claude"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want %v", tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() = %v, want errors.Is(%v)", err, tc.wantErr)
			}
		})
	}
}
