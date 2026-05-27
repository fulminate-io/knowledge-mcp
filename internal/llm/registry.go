package llm

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
)

// ProviderFactory constructs a Client for a given Config.
//
// Each provider sub-package registers its factory from init() via
// RegisterProvider; callers reach the factory indirectly through NewClient.
// The factory may dial network resources or shell out to a CLI binary, so
// it takes a context.
type ProviderFactory func(ctx context.Context, cfg *Config) (Client, error)

var (
	registryMu sync.RWMutex
	registry   = make(map[Provider]ProviderFactory)
)

// RegisterProvider registers factory under provider. Typically called from
// the provider sub-package's init(). If a factory is already registered for
// provider it is overwritten silently — registration is idempotent so test
// init order can re-import a provider without panicking.
func RegisterProvider(provider Provider, factory ProviderFactory) {
	if factory == nil {
		return
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[provider] = factory
}

// NewClient constructs a Client for cfg.Provider.
//
// Validates cfg, looks up the registered factory, and invokes it. Returns
// ErrInvalidConfig (wrapped with detail) if cfg fails validation, or
// ErrProviderNotRegistered (wrapped with the provider name) if no factory
// is registered for cfg.Provider.
func NewClient(ctx context.Context, cfg *Config) (Client, error) {
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
// Useful for diagnostics ("which providers did this binary compile in?").
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
// private; cross-package tests should use FakeClient + SnapshotRegistryForTest
// rather than wholesale clearing.
func resetRegistryForTest() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = make(map[Provider]ProviderFactory)
}

// SnapshotRegistryForTest captures the current registry and returns a closure
// that restores it. Test-only seam exported so cross-package tests (e.g.
// cmd/knowledge-server smoke tests) that must inject a FakeClient via
// RegisterProvider can undo their mutation in t.Cleanup. In-package tests
// that wholesale clear the registry pair this with resetRegistryForTest.
//
// Typical use:
//
//	t.Cleanup(llm.SnapshotRegistryForTest())
//	llm.RegisterProvider(llm.ProviderAnthropic, fakeFactory)
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
