// SPDX-License-Identifier: Apache-2.0

package logwire

import (
	"fmt"
	"sort"
	"sync"
)

// registryMu protects the global provider factory registry. An RWMutex
// allows concurrent New/Available reads while serializing Register writes
// from init() functions.
var (
	registryMu sync.RWMutex
	factories  = make(map[string]func() Provider)
)

// Register adds a provider factory to the global registry. Backend packages
// call this from init() to self-register. Panics on duplicate names — a
// duplicate is a programmer error, not a runtime condition.
func Register(name string, factory func() Provider) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := factories[name]; dup {
		panic(fmt.Sprintf("logs: duplicate provider registration: %q", name))
	}
	factories[name] = factory
}

// New creates a fresh Provider instance by calling the registered factory.
// Each call returns a new instance so callers get independent state.
func New(name string) (Provider, error) {
	registryMu.RLock()
	f, ok := factories[name]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("logs: unknown provider %q (registered: %v)", name, Available())
	}
	return f(), nil
}

// Available returns the sorted list of registered provider names. Used by
// configure_log_backend's pre-write validation so an unknown-provider
// configuration errors at config time instead of at first-use; also
// folded into the New() not-found error so callers see what's valid.
func Available() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(factories))
	for name := range factories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// IsRegistered reports whether name corresponds to a registered provider.
// Cheap O(1) check used by configure_log_backend to validate at write
// time rather than deferring discovery to first use.
func IsRegistered(name string) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()
	_, ok := factories[name]
	return ok
}
