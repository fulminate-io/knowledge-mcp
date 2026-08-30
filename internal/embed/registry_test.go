// SPDX-License-Identifier: Apache-2.0

package embed

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubArm is a minimal BinaryEmbedder used to prove registry dispatch. It
// is NOT the deterministic fake arm — this one exists only to be a
// distinguishable factory return value.
type stubArm struct{}

func (stubArm) Available() bool                                     { return true }
func (stubArm) EmbedBinary(context.Context, string) ([]byte, error) { return []byte{1}, nil }
func (stubArm) EmbedBinaryBatch(context.Context, []string) ([][]byte, error) {
	return [][]byte{{1}}, nil
}

// TestEmbedRegistry_ValidatesBeforeLookup pins the ORDER: a config that is
// invalid must be reported as ErrInvalidConfig even when no factory is
// registered for its provider, and a VALID config naming an unregistered
// provider must be reported as ErrProviderNotRegistered naming it.
//
// Order matters because the two errors send an operator to different
// places: one says "fix your config", the other says "this build has no
// such arm".
func TestEmbedRegistry_ValidatesBeforeLookup(t *testing.T) {
	t.Cleanup(SnapshotRegistryForTest())
	resetRegistryForTest()

	// Invalid config (off width) on a provider with NO factory: the
	// validation error wins over the not-registered one.
	_, err := NewEmbedder(context.Background(), &Config{Provider: ProviderVoyage, APIKey: "k", Dimension: 128, Dtype: "ubinary"})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewEmbedder(invalid cfg) = %v; want ErrInvalidConfig", err)
	}
	if errors.Is(err, ErrProviderNotRegistered) {
		t.Errorf("validation must run BEFORE lookup; got a not-registered error: %v", err)
	}

	// Valid config, still no factory: now the miss is reported, and it
	// names the provider.
	_, err = NewEmbedder(context.Background(), &Config{Provider: ProviderVoyage, APIKey: "k", Dimension: 256, Dtype: "ubinary"})
	if !errors.Is(err, ErrProviderNotRegistered) {
		t.Fatalf("NewEmbedder(unregistered) = %v; want ErrProviderNotRegistered", err)
	}
	if got := err.Error(); !strings.Contains(got, string(ProviderVoyage)) {
		t.Errorf("miss error %q does not name the provider", got)
	}

	// Register and dispatch — the known-positive that keeps the two
	// failures above from being satisfied by a NewEmbedder that always
	// errors.
	RegisterProvider(ProviderVoyage, func(context.Context, *Config) (BinaryEmbedder, error) {
		return stubArm{}, nil
	})
	got, err := NewEmbedder(context.Background(), &Config{Provider: ProviderVoyage, APIKey: "k", Dimension: 256, Dtype: "ubinary"})
	if err != nil {
		t.Fatalf("NewEmbedder(registered) = %v; want the stub arm", err)
	}
	if _, ok := got.(stubArm); !ok {
		t.Errorf("NewEmbedder returned %T; want stubArm", got)
	}

	// A nil factory is a no-op, not a registration.
	RegisterProvider(ProviderCohere, nil)
	if HasProvider(ProviderCohere) {
		t.Errorf("a nil factory must not register")
	}
}

// TestEmbedRegistry_SnapshotRestores proves the test seam actually
// restores: the mutation is visible while it is in force and gone after
// the restore closure runs.
func TestEmbedRegistry_SnapshotRestores(t *testing.T) {
	before := ListProviders()

	restore := SnapshotRegistryForTest()
	RegisterProvider(Provider("scratch-provider"), func(context.Context, *Config) (BinaryEmbedder, error) {
		return stubArm{}, nil
	})
	if !HasProvider(Provider("scratch-provider")) {
		t.Fatal("the mutation must be visible before the restore")
	}
	if len(ListProviders()) != len(before)+1 {
		t.Fatalf("ListProviders() = %v; want one more than %v", ListProviders(), before)
	}

	restore()
	if HasProvider(Provider("scratch-provider")) {
		t.Error("the restore closure did not remove the injected factory")
	}
	after := ListProviders()
	if len(after) != len(before) {
		t.Fatalf("ListProviders() after restore = %v; want %v", after, before)
	}
	for i := range after {
		if after[i] != before[i] {
			t.Fatalf("ListProviders() after restore = %v; want %v", after, before)
		}
	}
}
