// SPDX-License-Identifier: Apache-2.0

package rerank

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
)

// ProviderFactory constructs a Reranker for a given Config.
//
// Each arm registers its factory from init() in its own file in this
// package, the same files-in-package shape the embed axis uses.
//
// THE TWO AXES SHARE A PATTERN, NOT A MAP. This registry is deliberately a
// second instance of the embed registry's shape rather than a generalized
// third package: the repo's architecture invariant denies new shared
// packages, and the two registries carry different value types. A provider
// that embeds does not necessarily rerank, so the maps must be able to
// differ in membership — an absent entry here is how NewReranker reports
// "this provider publishes no rerank API".
type ProviderFactory func(ctx context.Context, cfg *Config) (Reranker, error)

var (
	registryMu sync.RWMutex
	registry   = make(map[Provider]ProviderFactory)
)

// RegisterProvider registers factory under provider. A nil factory is a
// no-op; re-registration overwrites silently and is idempotent.
func RegisterProvider(provider Provider, factory ProviderFactory) {
	if factory == nil {
		return
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[provider] = factory
}

// NewReranker constructs a Reranker for cfg.Provider.
//
// VALIDATE FIRST, THEN LOOK UP — the same order the embed registry uses,
// and it matters: a config naming an unknown provider must be reported as
// a bad config, not as an unregistered arm.
func NewReranker(ctx context.Context, cfg *Config) (Reranker, error) {
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

// ListProviders returns the sorted list of currently registered rerank
// providers.
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

// resetRegistryForTest clears the registry. Test-only, package-private.
func resetRegistryForTest() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = make(map[Provider]ProviderFactory)
}

// SnapshotRegistryForTest captures the current registry and returns a
// closure that restores it, so a cross-package test that injects a stub
// arm can undo its mutation in t.Cleanup.
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
