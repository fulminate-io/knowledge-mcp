// SPDX-License-Identifier: Apache-2.0

package cicd

import (
	"fmt"
	"strings"
	"sync"
)

// summaryMaxLen caps deterministic Summary strings emitted by registered
// helpers and the generic fallback. Mirrors collector/cloud/summarize.go.
const summaryMaxLen = 500

// SummarizeFunc returns a deterministic, human-readable Summary string for
// a ResourceSpec. Same input must always produce the same output -- no time,
// no randomness. Read from spec.Metadata, never from spec.Content JSON.
type SummarizeFunc func(spec ResourceSpec) string

var (
	summarizers   = make(map[string]SummarizeFunc)
	summarizersMu sync.RWMutex
)

// registryKey returns the composite "<provider>:<resourceType>" key used by
// the registry. CI/CD providers (github, gitlab, bitbucket) all use bare
// strings like "deployment", "environment", "repository", "runner" so the
// process-global registry would collide without provider scoping. Empty
// provider falls back to the bare resourceType so existing tests and
// pure-resourceType-key call sites still work.
func registryKey(provider, resourceType string) string {
	if provider == "" {
		return resourceType
	}
	return provider + ":" + resourceType
}

// Register associates a (provider, resourceType) pair with a deterministic
// summarizer. Called from each provider package's init(). Panics on duplicate
// keys to match the in-tree convention at collector/logs/registry.go:22 and
// domains/transformer/registry.go:27.
func Register(provider, resourceType string, fn SummarizeFunc) {
	summarizersMu.Lock()
	defer summarizersMu.Unlock()
	key := registryKey(provider, resourceType)
	if _, dup := summarizers[key]; dup {
		panic(fmt.Sprintf("cicd: duplicate summarizer registration: %q", key))
	}
	summarizers[key] = fn
}

// Summarize returns the deterministic Summary for a ResourceSpec. Looks up
// by (spec.Provider, spec.ResourceType). Falls back to a generic
// "<resourceType> <name>" string when no helper is registered (substitutes
// "<unknown>" for spec.ResourceType when empty so downstream search has
// something to match). Trailing whitespace from any empty-field combination
// is trimmed. Output is truncated to summaryMaxLen bytes.
func Summarize(spec ResourceSpec) string {
	summarizersMu.RLock()
	fn := summarizers[registryKey(spec.Provider, spec.ResourceType)]
	if fn == nil {
		// Allow pure-resourceType lookup as a fallback for legacy tests
		// and any ad hoc registration that pre-dates Provider scoping.
		fn = summarizers[spec.ResourceType]
	}
	summarizersMu.RUnlock()

	var s string
	if fn != nil {
		s = fn(spec)
	}
	if s == "" {
		rt := spec.ResourceType
		if rt == "" {
			rt = "<unknown>"
		}
		s = fmt.Sprintf("%s %s", rt, spec.Name)
		s = strings.TrimSpace(s)
	}
	return truncateSummary(s, summaryMaxLen)
}

// truncateSummary caps a string to max bytes. Byte-length truncation keeps
// the cap deterministic and matches len() semantics; a UTF-8 corner case
// losing one rune at the boundary is acceptable for embed input.
func truncateSummary(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
