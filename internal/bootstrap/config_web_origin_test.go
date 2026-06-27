// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"reflect"
	"testing"
)

// TestParseFlagsWebOrigin locks the --web-origin flag contract: the default
// allow-list is the two canonical Fulminate hosts, an explicit flag REPLACES
// the default (CSV-split), and '*' is never produced as a default.
func TestParseFlagsWebOrigin(t *testing.T) {
	canonical := []string{"https://fulminate.io", "https://dev.fulminate.io"}

	t.Run("default is the two canonical origins", func(t *testing.T) {
		cfg, err := ParseFlags(nil)
		if err != nil {
			t.Fatalf("ParseFlags(nil): %v", err)
		}
		if !reflect.DeepEqual(cfg.AllowedWebOrigins, canonical) {
			t.Fatalf("default AllowedWebOrigins = %v, want %v", cfg.AllowedWebOrigins, canonical)
		}
		for _, o := range cfg.AllowedWebOrigins {
			if o == "*" {
				t.Fatal("'*' must never appear in the default allow-list")
			}
		}
	})

	t.Run("explicit single --web-origin replaces the default", func(t *testing.T) {
		cfg, err := ParseFlags([]string{"--web-origin", "https://example.test"})
		if err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}
		want := []string{"https://example.test"}
		if !reflect.DeepEqual(cfg.AllowedWebOrigins, want) {
			t.Fatalf("AllowedWebOrigins = %v, want %v", cfg.AllowedWebOrigins, want)
		}
	})

	t.Run("CSV --web-origin splits and trims", func(t *testing.T) {
		cfg, err := ParseFlags([]string{"--web-origin", "https://a.test, https://b.test ,"})
		if err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}
		want := []string{"https://a.test", "https://b.test"}
		if !reflect.DeepEqual(cfg.AllowedWebOrigins, want) {
			t.Fatalf("AllowedWebOrigins = %v, want %v", cfg.AllowedWebOrigins, want)
		}
	})

	t.Run("repeated --web-origin appends after the first replaces", func(t *testing.T) {
		cfg, err := ParseFlags([]string{"--web-origin", "https://a.test", "--web-origin", "https://b.test"})
		if err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}
		want := []string{"https://a.test", "https://b.test"}
		if !reflect.DeepEqual(cfg.AllowedWebOrigins, want) {
			t.Fatalf("AllowedWebOrigins = %v, want %v", cfg.AllowedWebOrigins, want)
		}
	})
}
