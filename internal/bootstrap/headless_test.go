// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"flag"
	"testing"
)

// TestHeadlessFlag_RegisteredInServeSurface asserts --headless is exposed on the
// serve flag surface (so the downstream `knowledge serve --help | grep -- --headless`
// Dockerfile smoke passes) AND that the three existing --no-* gates keep their names
// and false defaults — headless is purely additive.
func TestHeadlessFlag_RegisteredInServeSurface(t *testing.T) {
	var serveFS *flag.FlagSet
	for _, d := range DocFlagSets() {
		if d.BlockName == "flags-serve" {
			serveFS = d.FlagSet
		}
	}
	if serveFS == nil {
		t.Fatal("DocFlagSets() has no flags-serve entry")
	}
	want := map[string]string{
		"headless":               "false",
		"no-propagation-runtime": "false",
		"skip-llm-precheck":      "false",
		"no-llm-pipeline":        "false",
	}
	for name, def := range want {
		f := serveFS.Lookup(name)
		if f == nil {
			t.Errorf("serve flag surface missing --%s", name)
			continue
		}
		if f.DefValue != def {
			t.Errorf("--%s default = %q, want %q", name, f.DefValue, def)
		}
	}
}

// TestParseFlags_HeadlessParses confirms --headless binds to cfg.Headless and that
// ParseFlags does NOT expand the umbrella (applyHeadless owns that, in runServe).
func TestParseFlags_HeadlessParses(t *testing.T) {
	cfg, err := ParseFlags([]string{"--headless"})
	if err != nil {
		t.Fatalf("ParseFlags(--headless): %v", err)
	}
	if !cfg.Headless {
		t.Error("cfg.Headless = false after --headless")
	}
	if cfg.NoPropagationRuntime {
		t.Error("ParseFlags expanded the umbrella; that is applyHeadless' job, not the parser's")
	}
}

// TestApplyHeadless_ImpliesFullGateSet is the "headless implies the full set"
// guarantee: --headless must expand into all four gate bools so an embedded
// daemon skips every background content + coordination loop.
func TestApplyHeadless_ImpliesFullGateSet(t *testing.T) {
	cfg := Config{Headless: true}
	applyHeadless(&cfg)

	checks := []struct {
		name string
		got  bool
	}{
		{"NoPropagationRuntime", cfg.NoPropagationRuntime},
		{"SkipLLMPrecheck", cfg.SkipLLMPrecheck},
		{"NoLLMPipeline", cfg.NoLLMPipeline},
		{"NoTranscriptUpload", cfg.NoTranscriptUpload},
	}
	for _, c := range checks {
		if !c.got {
			t.Errorf("applyHeadless(Headless:true): %s = false, want true", c.name)
		}
	}
}

// TestApplyHeadless_NoOpWhenDisabled confirms the flag is purely additive: with
// Headless false, applyHeadless leaves every gate bool at its zero value so a
// normal serve is unaffected.
func TestApplyHeadless_NoOpWhenDisabled(t *testing.T) {
	cfg := Config{Headless: false}
	applyHeadless(&cfg)

	if cfg.NoPropagationRuntime || cfg.SkipLLMPrecheck ||
		cfg.NoLLMPipeline || cfg.NoTranscriptUpload {
		t.Errorf("applyHeadless(Headless:false) set a gate bool: %+v", cfg)
	}
}
