// SPDX-License-Identifier: Apache-2.0

package config

import (
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
	if got != cfg.Default {
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
	if got != cfg.Default {
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

func TestResolve_NilConfig(t *testing.T) {
	var cfg *Config
	_, err := cfg.Resolve(ConsumerSummarizer)
	if err == nil {
		t.Fatal("Resolve(nil cfg): want error, got nil")
	}
}
