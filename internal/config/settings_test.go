// SPDX-License-Identifier: Apache-2.0
package config

import (
	"strings"
	"testing"
)

func TestSettingsCandidateCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Cleanup(SetForTest(&Config{Credentials: &Credentials{OpenAIAPIKey: "stale"}}))
	c := &Config{Default: Section{Provider: ProviderOpenAI, Model: "model"}}
	if c.Validate([]Consumer{ConsumerSummarizer}) == nil {
		t.Fatal("candidate used stale credentials")
	}
	c.Credentials = &Credentials{OpenAIAPIKey: "candidate"}
	if err := c.Validate([]Consumer{ConsumerSummarizer}); err != nil {
		t.Fatal("candidate credential rejected")
	}
}
func TestSettingsPreservingEdits(t *testing.T) {
	original := "# retained\nfulminate_account_id = 'acct_fixture'\nauto_update = false\n[default]\nprovider = 'openai' # keep\nmodel = '''old\nmodel''' # model comment\nbase_url = 'http://127.0.0.1:1'\n[[summarizer.fallback]]\nprovider='openai'\nmodel='fallback'\n[embedder]\nmodel='embed'\n[embedder.profile.named]\nmodel='profile'\n[reranker]\nmodel='rerank'\n[collector]\nkeep='unchanged'\n"
	value := "long 雪 model"
	out, err := EditSettings([]byte(original), map[string]map[string]string{"default": {"model": value}, "topics": {"model": "topic"}, "credentials": {"cohere_api_key": "quote\"slash\\\nline"}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(out)
	if err != nil {
		t.Fatal("edited TOML invalid")
	}
	if c.Default.Model != value || c.Topics.Model != "topic" || c.Credentials.CohereAPIKey != "quote\"slash\\\nline" {
		t.Fatal("edit round trip differs")
	}
	for _, keep := range []string{"# retained", "# keep", "# model comment", "fulminate_account_id = 'acct_fixture'", "auto_update = false", "[[summarizer.fallback]]\nprovider='openai'\nmodel='fallback'", "[embedder]\nmodel='embed'", "[embedder.profile.named]\nmodel='profile'", "[reranker]\nmodel='rerank'", "[collector]\nkeep='unchanged'"} {
		if !strings.Contains(string(out), keep) {
			t.Fatal("unrelated bytes lost")
		}
	}
}

func TestSettingsInlineAndDotted(t *testing.T) {
	for _, input := range []string{"default = { provider='openai', model='old', base_url='http://127.0.0.1:1' }\n", "default.provider='openai'\ndefault.model='old'\ndefault.base_url='http://127.0.0.1:1'\n", "['default']\n'provider'='openai'\n'model'='old'\n'base_url'='http://127.0.0.1:1'\n"} {
		out, err := EditSettings([]byte(input), map[string]map[string]string{"default": {"model": "new", "cli_bin": "/fixture"}})
		if err != nil {
			t.Error("supported TOML layout could not be edited")
			continue
		}
		c, err := Parse(out)
		if err != nil || c.Default.Model != "new" || c.Default.CLIBin != "/fixture" {
			t.Error("layout edit differs")
		}
	}
}
