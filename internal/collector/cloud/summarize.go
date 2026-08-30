// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"fmt"
	"strings"
	"sync"
)

// summaryMaxLen caps deterministic Summary strings emitted by registered
// helpers and the generic fallback. The cap exists so downstream embedders
// see bounded input regardless of metadata bloat.
const summaryMaxLen = 500

// SummarizeFunc returns a deterministic, human-readable Summary string for
// a ResourceSpec. Same input must always produce the same output -- no time,
// no randomness. Read from spec.Metadata, never from spec.Content JSON.
type SummarizeFunc func(spec ResourceSpec) string

var (
	summarizers   = make(map[string]SummarizeFunc)
	summarizersMu sync.RWMutex
)

// Register associates a ResourceType string with a deterministic summarizer.
// Called from each provider package's init(). Panics on duplicate keys to
// match the in-tree convention at internal/logwire/registry.go.
func Register(resourceType string, fn SummarizeFunc) {
	summarizersMu.Lock()
	defer summarizersMu.Unlock()
	if _, dup := summarizers[resourceType]; dup {
		panic(fmt.Sprintf("cloud: duplicate summarizer registration: %q", resourceType))
	}
	summarizers[resourceType] = fn
}

// Summarize returns the deterministic Summary for a ResourceSpec.
// Looks up by spec.ResourceType. Falls back to a generic
// "<resourceType> <name> in <region>" string when no helper is registered
// (drops " in <region>" when spec.Region is empty; substitutes
// "<unknown>" for spec.ResourceType when empty so downstream search has
// something to match). Trailing whitespace from any empty-field combination
// is trimmed. Output is truncated to summaryMaxLen bytes.
func Summarize(spec ResourceSpec) string {
	summarizersMu.RLock()
	fn := summarizers[spec.ResourceType]
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
		if spec.Region == "" {
			s = fmt.Sprintf("%s %s", rt, spec.Name)
		} else {
			s = fmt.Sprintf("%s %s in %s", rt, spec.Name, spec.Region)
		}
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
