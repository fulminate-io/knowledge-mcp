// SPDX-License-Identifier: Apache-2.0

package pipeline

import "testing"

// TestCircuitBreakerThresholdOrDefault verifies the accessor returns the
// package default (20) for a zero-value Config and the set value otherwise.
func TestCircuitBreakerThresholdOrDefault(t *testing.T) {
	tests := []struct {
		name string
		set  int
		want int
	}{
		{"zero-value -> default 20", 0, DefaultCircuitBreakerThreshold},
		{"negative -> default 20", -5, DefaultCircuitBreakerThreshold},
		{"explicit value honored", 7, 7},
		{"large value honored", 100, 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{CircuitBreakerThreshold: tt.set}
			if got := cfg.CircuitBreakerThresholdOrDefault(); got != tt.want {
				t.Fatalf("CircuitBreakerThresholdOrDefault() = %d, want %d", got, tt.want)
			}
		})
	}
	if DefaultCircuitBreakerThreshold != 20 {
		t.Fatalf("DefaultCircuitBreakerThreshold = %d, want 20", DefaultCircuitBreakerThreshold)
	}
}

// TestLeaseSizes_Derivation pins the derived lease size across the three regimes
// its three terms select between, so a later edit that collapses the expression
// to a literal fails here rather than in production.
//
// The expectations are EXTERNAL to the expression: each one is the value the
// three-term derivation was executed against by hand at that worker count, not a
// second evaluation of the same arithmetic. That is what makes the table a check
// rather than an identity.
func TestLeaseSizes_Derivation(t *testing.T) {
	tests := []struct {
		name        string
		cfg         Config
		wantEmbed   int
		wantSummary int
	}{
		// OPERATIVE term binds on both axes at the shipped defaults: embed gets
		// 10000/20 = 500 (over its 100 floor, under its 1000 ceiling); summary's
		// 10000/25 = 400 is above its 10x20 = 200 ceiling, so summary is CEILED.
		{"shipped defaults", Config{}, 500, 200},
		// CEILING term binds on embed: 10000/4 = 2500 exceeds 10 x 100.
		{"few embed workers -> ceiling", Config{EmbedWorkers: 4}, 1000, 200},
		// FLOOR term binds on embed: 10000/200 = 50 is below one provider call.
		{"many embed workers -> floor", Config{EmbedWorkers: 200}, 100, 200},
		// An explicit override wins outright on the axis that sets it.
		{"explicit override", Config{EmbedLeaseSize: 37, SummaryLeaseSize: 41}, 37, 41},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.EmbedLeaseSizeOrDefault(); got != tt.wantEmbed {
				t.Fatalf("EmbedLeaseSizeOrDefault() = %d, want %d", got, tt.wantEmbed)
			}
			if got := tt.cfg.SummaryLeaseSizeOrDefault(); got != tt.wantSummary {
				t.Fatalf("SummaryLeaseSizeOrDefault() = %d, want %d", got, tt.wantSummary)
			}
		})
	}
	// The lease is a MULTIPLE of the provider-call cap on both axes at the
	// shipped defaults — the property the per-lease stride depends on, and the
	// one a future retune could break without moving any number above.
	var cfg Config
	if cfg.EmbedLeaseSizeOrDefault()%cfg.EmbedBatchSizeOrDefault() != 0 {
		t.Fatalf("embed lease %d is not a whole number of %d-item provider calls",
			cfg.EmbedLeaseSizeOrDefault(), cfg.EmbedBatchSizeOrDefault())
	}
	if cfg.SummaryLeaseSizeOrDefault()%cfg.SummaryBatchSizeOrDefault() != 0 {
		t.Fatalf("summary lease %d is not a whole number of %d-item provider calls",
			cfg.SummaryLeaseSizeOrDefault(), cfg.SummaryBatchSizeOrDefault())
	}
}

// TestNewConstructsCircuitBreaker verifies New() wires BOTH per-axis circuit
// breakers (summary + embed), each non-nil and carrying the configured threshold.
func TestNewConstructsCircuitBreaker(t *testing.T) {
	p := New(Config{CircuitBreakerThreshold: 9}, nil, nil, nil)
	for name, c := range map[string]*circuitBreaker{"summary": p.summaryCircuit, "embed": p.embedCircuit} {
		if c == nil {
			t.Fatalf("Pipeline.%sCircuit is nil after New()", name)
		}
		if c.tripThreshold != 9 {
			t.Fatalf("%sCircuit.tripThreshold = %d, want 9", name, c.tripThreshold)
		}
	}

	// Zero-value Config -> default threshold on BOTH axes.
	pd := New(Config{}, nil, nil, nil)
	for name, c := range map[string]*circuitBreaker{"summary": pd.summaryCircuit, "embed": pd.embedCircuit} {
		if c.tripThreshold != DefaultCircuitBreakerThreshold {
			t.Fatalf("default %sCircuit.tripThreshold = %d, want %d", name, c.tripThreshold, DefaultCircuitBreakerThreshold)
		}
	}
}
