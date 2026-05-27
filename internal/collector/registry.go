// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"fmt"
	"sync"
)

// collectors is the internal registry of all known collectors. Guarded by mu
// so concurrent init() registration (or test-time re-registration) is safe.
var (
	mu         sync.RWMutex
	collectors = map[string]Collector{}
)

// Register adds a collector to the registry. Called from init() in each
// collector package. Panics on nil collector, empty name, or duplicate
// registration to catch bugs early.
func Register(c Collector) {
	if c == nil {
		panic("collector: Register called with nil collector")
	}
	name := c.Name()
	if name == "" {
		panic("collector: Register called with empty name")
	}
	mu.Lock()
	defer mu.Unlock()
	if _, exists := collectors[name]; exists {
		panic(fmt.Sprintf("collector: duplicate registration for %q", name))
	}
	collectors[name] = c
}

// Lookup returns a collector by name, or an error if not found.
func Lookup(name string) (Collector, error) {
	mu.RLock()
	defer mu.RUnlock()
	c, ok := collectors[name]
	if !ok {
		return nil, fmt.Errorf("collector: unknown collector %q", name)
	}
	return c, nil
}
