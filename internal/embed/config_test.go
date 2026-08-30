// SPDX-License-Identifier: Apache-2.0

package embed

import (
	"fmt"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// accepted returns a Config that Validate accepts, so each case below
// varies exactly one field away from a KNOWN-GOOD baseline. Without the
// baseline a refusal test cannot distinguish "the gate refused this field"
// from "the gate refuses everything".
func accepted() *Config {
	return &Config{Provider: ProviderVoyage, APIKey: "k", Dimension: 256, Dtype: "ubinary"}
}

// TestEmbedConfigValidate_KeyOrBaseURL drives the credential half: an API
// provider with NEITHER a key nor a base_url is refused, base_url alone is
// accepted (a keyless local endpoint), and the fake is exempt from both.
func TestEmbedConfigValidate_KeyOrBaseURL(t *testing.T) {
	if err := accepted().Validate(); err != nil {
		t.Fatalf("baseline config must validate: %v", err)
	}

	cases := []struct {
		name    string
		cfg     *Config
		wantErr bool
	}{
		{"nil config", nil, true},
		{"api provider with neither", &Config{Provider: ProviderVoyage, Dimension: 256, Dtype: "ubinary"}, true},
		{"api provider with base_url alone", &Config{Provider: ProviderVoyage, BaseURL: "http://127.0.0.1:8000", Dimension: 256, Dtype: "ubinary"}, false},
		{"api provider with key alone", accepted(), false},
		{"fake is exempt", &Config{Provider: ProviderFake, Dimension: 256, Dtype: "ubinary"}, false},
		{"unknown provider", &Config{Provider: Provider("anthropic"), APIKey: "k", Dimension: 256, Dtype: "ubinary"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() = %v; wantErr=%v", err, tc.wantErr)
			}
		})
	}

	// An empty InputRole DEFAULTS to document — the behavior that
	// reproduces the single hardcoded role this package used to have.
	cfg := accepted()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.InputRole != InputRoleDocument {
		t.Errorf("empty InputRole defaulted to %q; want %q", cfg.InputRole, InputRoleDocument)
	}
	// An explicitly-set role is NOT overwritten — the known-positive that
	// proves the default above is a default and not an assignment.
	q := accepted()
	q.InputRole = InputRoleQuery
	if err := q.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if q.InputRole != InputRoleQuery {
		t.Errorf("explicit InputRole was overwritten to %q", q.InputRole)
	}
}

// TestEmbedConfigValidate_RefusesOffWidth exercises HAND-BUILT Configs
// that never pass through TOML — the enforcement layer the parser cannot
// reach. Each refusal must NAME the offending value: an error that does
// not name it leaves the operator guessing.
//
// The zero-Dimension fake case is the load-bearing one. Without this
// refusal that config yields width 0/8 = 0, an empty vector for every
// text, and a pipeline that indexes nothing while every gate stays green.
func TestEmbedConfigValidate_RefusesOffWidth(t *testing.T) {
	cases := []struct {
		name     string
		cfg      *Config
		wantText string
	}{
		{"fake at zero dimension", &Config{Provider: ProviderFake, Dtype: "ubinary"}, "dimension 0"},
		// 128 stays an off-set width and 3072 is added because it is the plausible
		// one — a real width other embedders offer that this build does not serve.
		{"voyage at 128 bits", &Config{Provider: ProviderVoyage, APIKey: "k", Dimension: 128, Dtype: "ubinary"}, "dimension 128"},
		{"voyage at 3072 bits", &Config{Provider: ProviderVoyage, APIKey: "k", Dimension: 3072, Dtype: "ubinary"}, "dimension 3072"},
		{"voyage at float dtype", &Config{Provider: ProviderVoyage, APIKey: "k", Dimension: 256, Dtype: "float"}, `dtype "float"`},
		{"voyage at empty dtype", &Config{Provider: ProviderVoyage, APIKey: "k", Dimension: 256}, `dtype ""`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %s; want a refusal", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("refusal %q does not name the offending value (want substring %q)", err, tc.wantText)
			}
			// THE RESTRICTION IS NO LONGER TEMPORARY, so the refusal no longer
			// promises a future release. The float-native index that was named as
			// what would lift it has shipped: the accepted set widened, and what
			// remains outside it is outside it permanently. What an operator needs
			// now is the VOCABULARY, which every refusal still carries.
			if !strings.Contains(err.Error(), "accepted:") {
				t.Errorf("refusal %q does not list the accepted vocabulary", err)
			}
			if strings.Contains(err.Error(), "float-native vector index") {
				t.Errorf("refusal %q still promises a release that has shipped", err)
			}
		})
	}

	// The accepted pair is NOT refused — the known-positive that keeps the
	// four reds above from being satisfied by a gate that refuses
	// everything.
	if err := (&Config{Provider: ProviderVoyage, APIKey: "k", Dimension: 256, Dtype: "ubinary"}).Validate(); err != nil {
		t.Fatalf("the accepted (256, ubinary) pair must validate: %v", err)
	}
}

// TestEmbedConfigValidate_AcceptsEveryAcceptedPair is the positive half the
// off-width table cannot supply. Every case there is refused under BOTH the
// old single-value rule and the widened set rule, so the table alone cannot
// tell them apart; these cases separate them, because each is accepted by the
// set rule and refused by the single-value one.
//
// THE CONTRADICTION THIS CLOSES was live: Validate compared against the single
// accepted default while its own refusal message listed the whole accepted set,
// so a config at 1024/float32 was rejected with the words "dimension 1024 is
// not supported (accepted: 256, 512, 1024, 2048)".
func TestEmbedConfigValidate_AcceptsEveryAcceptedPair(t *testing.T) {
	for _, dim := range config.AcceptedEmbedDimensions {
		for _, dtype := range config.AcceptedEmbedDtypes {
			name := fmt.Sprintf("%d/%s", dim, dtype)
			t.Run(name, func(t *testing.T) {
				cfg := &Config{Provider: ProviderVoyage, APIKey: "k", Dimension: dim, Dtype: dtype}
				if err := cfg.Validate(); err != nil {
					t.Fatalf("Validate refused the accepted pair %s: %v", name, err)
				}
			})
		}
	}
	// KNOWN-POSITIVE FOR THE REFUSAL SIDE, same run: a pair just outside each
	// set is still refused, so "accepts every accepted pair" is not satisfied
	// by a Validate that accepts everything.
	if err := (&Config{Provider: ProviderVoyage, APIKey: "k", Dimension: 384, Dtype: "ubinary"}).Validate(); err == nil {
		t.Error("Validate accepted an off-set dimension")
	}
	if err := (&Config{Provider: ProviderVoyage, APIKey: "k", Dimension: 256, Dtype: "float64"}).Validate(); err == nil {
		t.Error("Validate accepted an off-set dtype")
	}
}
