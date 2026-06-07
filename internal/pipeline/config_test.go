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

// TestNewConstructsCircuitBreaker verifies New() wires a non-nil circuit
// breaker carrying the configured threshold.
func TestNewConstructsCircuitBreaker(t *testing.T) {
	p := New(Config{CircuitBreakerThreshold: 9}, nil, nil, nil)
	if p.circuit == nil {
		t.Fatalf("Pipeline.circuit is nil after New()")
	}
	if p.circuit.tripThreshold != 9 {
		t.Fatalf("circuit.tripThreshold = %d, want 9", p.circuit.tripThreshold)
	}

	// Zero-value Config -> default threshold.
	pd := New(Config{}, nil, nil, nil)
	if pd.circuit.tripThreshold != DefaultCircuitBreakerThreshold {
		t.Fatalf("default circuit.tripThreshold = %d, want %d", pd.circuit.tripThreshold, DefaultCircuitBreakerThreshold)
	}
}
