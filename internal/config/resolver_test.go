// SPDX-License-Identifier: Apache-2.0

package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestResolve_DefaultProvidesProvider(t *testing.T) {
	cfg := &Config{
		Default:    Section{Provider: ProviderAnthropic, Model: "claude-haiku-5"},
		Summarizer: &Section{Model: "claude-opus-4-7"},
	}
	got, err := cfg.Resolve(ConsumerSummarizer)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Provider != ProviderAnthropic {
		t.Errorf("Provider = %q; want inherited %q", got.Provider, ProviderAnthropic)
	}
	if got.Model != "claude-opus-4-7" {
		t.Errorf("Model = %q; want overridden", got.Model)
	}
}

func TestResolve_DefaultProvidesBaseURL(t *testing.T) {
	cfg := &Config{
		Default:    Section{Provider: ProviderOpenAI, Model: "gpt-5-mini", BaseURL: "http://d"},
		Summarizer: &Section{Model: "gpt-5"},
	}
	got, err := cfg.Resolve(ConsumerSummarizer)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.BaseURL != "http://d" {
		t.Errorf("BaseURL = %q; want inherited %q", got.BaseURL, "http://d")
	}
	if got.Model != "gpt-5" {
		t.Errorf("Model = %q; want overridden", got.Model)
	}
}

func TestResolve_PerConsumerOverridesBaseURL(t *testing.T) {
	cfg := &Config{
		Default:    Section{Provider: ProviderOpenAI, Model: "gpt-5-mini", BaseURL: "http://d"},
		Summarizer: &Section{BaseURL: "http://s"},
	}
	got, err := cfg.Resolve(ConsumerSummarizer)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.BaseURL != "http://s" {
		t.Errorf("BaseURL = %q; want overridden %q", got.BaseURL, "http://s")
	}
}

func TestResolve_DefaultProvidesModel(t *testing.T) {
	cfg := &Config{
		Default:    Section{Provider: ProviderAnthropic, Model: "claude-haiku-5"},
		Summarizer: &Section{Provider: ProviderOpenAI},
	}
	got, err := cfg.Resolve(ConsumerSummarizer)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Provider != ProviderOpenAI {
		t.Errorf("Provider = %q; want overridden %q", got.Provider, ProviderOpenAI)
	}
	if got.Model != "claude-haiku-5" {
		t.Errorf("Model = %q; want inherited", got.Model)
	}
}

func TestResolve_PerConsumerOverridesAll(t *testing.T) {
	cfg := &Config{
		Default:    Section{Provider: ProviderAnthropic, Model: "claude-haiku-5"},
		Summarizer: &Section{Provider: ProviderOpenAI, Model: "gpt-5-mini"},
	}
	got, err := cfg.Resolve(ConsumerSummarizer)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Provider != ProviderOpenAI || got.Model != "gpt-5-mini" {
		t.Errorf("got %+v; want fully-overridden", got)
	}
}

func TestResolve_NoPerConsumerSection(t *testing.T) {
	cfg := &Config{
		Default: Section{Provider: ProviderAnthropic, Model: "claude-haiku-5"},
	}
	got, err := cfg.Resolve(ConsumerSummarizer)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !reflect.DeepEqual(got, cfg.Default) {
		t.Errorf("got %+v; want %+v", got, cfg.Default)
	}
}

func TestResolve_NeitherProvider(t *testing.T) {
	cfg := &Config{
		Default:    Section{Model: "claude-haiku-5"},
		Summarizer: &Section{Model: "claude-opus-4-7"},
	}
	_, err := cfg.Resolve(ConsumerSummarizer)
	if err == nil {
		t.Fatal("Resolve: want error, got nil")
	}
	if !strings.Contains(err.Error(), "no provider") {
		t.Errorf("error does not mention provider: %v", err)
	}
}

func TestResolve_NeitherModel(t *testing.T) {
	cfg := &Config{
		Default:    Section{Provider: ProviderAnthropic},
		Summarizer: &Section{Provider: ProviderOpenAI},
	}
	_, err := cfg.Resolve(ConsumerSummarizer)
	if err == nil {
		t.Fatal("Resolve: want error, got nil")
	}
	if !strings.Contains(err.Error(), "no model") {
		t.Errorf("error does not mention model: %v", err)
	}
}

func TestResolve_DreamSection(t *testing.T) {
	cfg := &Config{
		Default: Section{Provider: ProviderAnthropic, Model: "claude-haiku-5"},
		Dream:   &Section{Provider: ProviderOpenAI, Model: "gpt-5-mini"},
	}
	got, err := cfg.Resolve(ConsumerDream)
	if err != nil {
		t.Fatalf("Resolve(dream): %v", err)
	}
	if got.Provider != ProviderOpenAI || got.Model != "gpt-5-mini" {
		t.Errorf("dream resolve = %+v; want override", got)
	}
}

func TestResolve_SupervisorSection(t *testing.T) {
	cfg := &Config{
		Default:    Section{Provider: ProviderAnthropic, Model: "claude-haiku-5"},
		Supervisor: &Section{Model: "claude-opus-4-7"},
	}
	got, err := cfg.Resolve(ConsumerHiveSupervisor)
	if err != nil {
		t.Fatalf("Resolve(supervisor): %v", err)
	}
	if got.Provider != ProviderAnthropic {
		t.Errorf("Provider = %q; want inherited %q", got.Provider, ProviderAnthropic)
	}
	if got.Model != "claude-opus-4-7" {
		t.Errorf("Model = %q; want per-field override", got.Model)
	}
}

func TestResolve_NilSupervisorInheritsDefault(t *testing.T) {
	cfg := &Config{
		Default: Section{Provider: ProviderAnthropic, Model: "claude-haiku-5"},
	}
	got, err := cfg.Resolve(ConsumerHiveSupervisor)
	if err != nil {
		t.Fatalf("Resolve(supervisor): %v", err)
	}
	if !reflect.DeepEqual(got, cfg.Default) {
		t.Errorf("got %+v; want %+v (full inheritance from Default)", got, cfg.Default)
	}
}

func TestResolve_UnknownConsumer(t *testing.T) {
	cfg := &Config{
		Default: Section{Provider: ProviderAnthropic, Model: "claude-haiku-5"},
	}
	_, err := cfg.Resolve(Consumer("bogus"))
	if err == nil {
		t.Fatal("Resolve(bogus): want error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown consumer") {
		t.Errorf("error does not mention unknown consumer: %v", err)
	}
}

// TestResolveChain asserts the ordered primary+fallback chain: ResolveChain
// returns [primary, fb0, fb1] with the SAME per-field [default] inheritance
// applied to every entry (the primary's model override stays, each fallback
// inherits cli_bin from [default]), and a fallback missing both its own and
// [default]'s model trips the existing required-field error.
func TestResolveChain(t *testing.T) {
	cfg := &Config{
		Default: Section{Provider: ProviderClaudeCLI, Model: "claude-haiku-5", CLIBin: "/bin/claude"},
		Summarizer: &Section{
			Model: "claude-opus-5",
			Fallback: []Section{
				{Provider: ProviderOpenAI, Model: "gpt-5-mini"},
				{Provider: ProviderGemini}, // model inherited from [default]
			},
		},
	}
	chain, err := cfg.ResolveChain(ConsumerSummarizer)
	if err != nil {
		t.Fatalf("ResolveChain: %v", err)
	}
	if len(chain) != 3 {
		t.Fatalf("chain len = %d; want 3", len(chain))
	}
	// Primary: provider+cli_bin inherited from [default], model overridden.
	if chain[0].Provider != ProviderClaudeCLI || chain[0].Model != "claude-opus-5" || chain[0].CLIBin != "/bin/claude" {
		t.Errorf("chain[0] = %+v; want primary {claude-cli, claude-opus-5, /bin/claude}", chain[0])
	}
	// Fallback 0: its own provider+model, cli_bin inherited from [default].
	if chain[1].Provider != ProviderOpenAI || chain[1].Model != "gpt-5-mini" || chain[1].CLIBin != "/bin/claude" {
		t.Errorf("chain[1] = %+v; want {openai, gpt-5-mini, /bin/claude inherited}", chain[1])
	}
	// Fallback 1: its own provider, model inherited from [default].
	if chain[2].Provider != ProviderGemini || chain[2].Model != "claude-haiku-5" {
		t.Errorf("chain[2] = %+v; want {gemini, claude-haiku-5 inherited}", chain[2])
	}
}

// TestResolveChain_FallbackMissingModel asserts a fallback that supplies neither
// its own model nor inherits one from [default] returns the existing 'no model'
// required-field error.
func TestResolveChain_FallbackMissingModel(t *testing.T) {
	cfg := &Config{
		Default:    Section{Provider: ProviderAnthropic}, // no default model
		Summarizer: &Section{Model: "claude-opus-5", Fallback: []Section{{Provider: ProviderOpenAI}}},
	}
	_, err := cfg.ResolveChain(ConsumerSummarizer)
	if err == nil {
		t.Fatal("ResolveChain: want 'no model' error for model-less fallback, got nil")
	}
	if !strings.Contains(err.Error(), "no model") {
		t.Errorf("error does not mention model: %v", err)
	}
}

// TestResolveChain_NoFallbackIsSingleton proves the change is a strict superset:
// a config with no fallback yields a len-1 chain identical to the single-section
// Resolve path.
func TestResolveChain_NoFallbackIsSingleton(t *testing.T) {
	cfg := &Config{
		Default:    Section{Provider: ProviderAnthropic, Model: "claude-haiku-5"},
		Summarizer: &Section{Model: "claude-opus-5"},
	}
	chain, err := cfg.ResolveChain(ConsumerSummarizer)
	if err != nil {
		t.Fatalf("ResolveChain: %v", err)
	}
	primary, err := cfg.Resolve(ConsumerSummarizer)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(chain) != 1 {
		t.Fatalf("chain len = %d; want 1 (no fallback)", len(chain))
	}
	if !reflect.DeepEqual(chain[0], primary) {
		t.Errorf("chain[0] = %+v; want identical to Resolve %+v", chain[0], primary)
	}
}

func TestResolve_NilConfig(t *testing.T) {
	var cfg *Config
	_, err := cfg.Resolve(ConsumerSummarizer)
	if err == nil {
		t.Fatal("Resolve(nil cfg): want error, got nil")
	}
}
