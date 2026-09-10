// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"fmt"
	"sort"
	"strings"
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
//
// THE REFUSAL NAMES THE CUSTOM-COLLECTOR ROUTE, and that is the whole point of
// the sentence. Most names that reach this miss are not typos: aws, gcp, azure,
// k8s, cloudwatch, loki, stackdriver, github, gitlab and bitbucket were
// compiled-in collector names one release ago and are contrib collectors now, so
// an operator whose call stopped working needs the route rather than the
// observation that the name is unknown. The registered names are listed too,
// because a refusal that names the offending value owes the vocabulary that would
// have worked.
func Lookup(name string) (Collector, error) {
	mu.RLock()
	defer mu.RUnlock()
	c, ok := collectors[name]
	if !ok {
		return nil, fmt.Errorf(
			"collector: unknown collector %q — the built-in collectors are %s; every other collector is a "+
				"CONTRIB collector, registered in the collector config file as its own graph type and run as its "+
				"own binary (see the custom collector guide). aws, gcp, azure, k8s, cloudwatch, loki, "+
				"stackdriver, github, gitlab and bitbucket were built in until this release and are contrib "+
				"collectors now",
			name, strings.Join(registeredNames(), ", "))
	}
	return c, nil
}

// registeredNames lists the compiled-in collector names, sorted. Caller holds
// the read lock. Derived from the registry rather than restated at the error
// site: a hand-written vocabulary in an error string rots without failing a
// test, because nothing compares the two.
func registeredNames() []string {
	out := make([]string, 0, len(collectors))
	for n := range collectors {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
