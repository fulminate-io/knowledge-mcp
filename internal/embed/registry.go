// SPDX-License-Identifier: Apache-2.0

package embed

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
)

// ProviderFactory constructs a BinaryEmbedder for a given Config.
//
// Each arm registers its factory from init() in its own file; callers
// reach the factory indirectly through NewEmbedder. The factory may dial
// network resources, so it takes a context.
//
// The arms are FILES IN THIS PACKAGE rather than sub-packages (the shape
// the LLM registry uses): each embed arm is one request shape and one
// response decode, and same-package init() registration needs no blank
// imports, so there is no forgot-to-import-the-arm failure mode.
type ProviderFactory func(ctx context.Context, cfg *Config) (BinaryEmbedder, error)

var (
	registryMu sync.RWMutex
	registry   = make(map[Provider]ProviderFactory)
)

// RegisterProvider registers factory under provider. Typically called from
// the arm's init(). If a factory is already registered for provider it is
// overwritten silently — registration is idempotent so test init order can
// re-register a provider without panicking.
func RegisterProvider(provider Provider, factory ProviderFactory) {
	if factory == nil {
		return
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[provider] = factory
}

// NewEmbedder constructs a BinaryEmbedder for cfg.Provider.
//
// Validates cfg, looks up the registered factory, and invokes it. Returns
// ErrInvalidConfig (wrapped with detail) if cfg fails validation, or
// ErrProviderNotRegistered (wrapped with the provider name) if no factory
// is registered for cfg.Provider. VALIDATE FIRST, THEN LOOK UP: a config
// naming an unknown provider must be reported as a bad config, not as an
// unregistered arm.
func NewEmbedder(ctx context.Context, cfg *Config) (BinaryEmbedder, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	registryMu.RLock()
	factory, ok := registry[cfg.Provider]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrProviderNotRegistered, cfg.Provider)
	}
	return factory(ctx, cfg)
}

// ListProviders returns the sorted list of currently registered providers.
// Useful for diagnostics ("which arms did this binary compile in?").
func ListProviders() []Provider {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]Provider, 0, len(registry))
	for p := range registry {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// HasProvider reports whether a factory is registered for provider.
func HasProvider(provider Provider) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()
	_, ok := registry[provider]
	return ok
}

// resetRegistryForTest clears the registry. Test-only helper kept package-
// private; cross-package tests should use the deterministic fake arm plus
// SnapshotRegistryForTest rather than wholesale clearing.
func resetRegistryForTest() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = make(map[Provider]ProviderFactory)
}

// SnapshotRegistryForTest captures the current registry and returns a
// closure that restores it. Test-only seam exported so cross-package tests
// that must inject a stub arm via RegisterProvider can undo their mutation
// in t.Cleanup. In-package tests that wholesale clear the registry pair
// this with resetRegistryForTest.
//
// Typical use:
//
//	t.Cleanup(embed.SnapshotRegistryForTest())
//	embed.RegisterProvider(embed.ProviderVoyage, fakeFactory)
//	// ... test that depends on the swapped factory
func SnapshotRegistryForTest() func() {
	registryMu.RLock()
	saved := make(map[Provider]ProviderFactory, len(registry))
	maps.Copy(saved, registry)
	registryMu.RUnlock()
	return func() {
		registryMu.Lock()
		defer registryMu.Unlock()
		registry = saved
	}
}
