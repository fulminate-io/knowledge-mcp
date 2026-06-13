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
