// SPDX-License-Identifier: Apache-2.0

package logs

import "sync"

// engineRegistry keeps QueryEngine instances alive between Pipeline.Collect
// (which builds them) and downstream tool handlers (which look them up by
// queryID). Storing them process-wide lets the MCP log_* tools answer
// follow-up queries ("severity>=WARN for service=api", "templates for this
// stream", etc.) without re-running the provider.
//
// The registry is intentionally simple: the engine holds only indexes and
// pointers into immutable collection artifacts, so lifetime is bounded by
// how long callers care about a given query. A future eviction policy can
// land here without touching call sites.
var (
	engineRegistryMu sync.RWMutex
	engineRegistry   = make(map[string]*QueryEngine)
)

// RegisterEngine stores engine under queryID. Overwrites any previous entry
// for the same key — collection for the same query is expected to replace
// the prior result. A nil engine removes the entry.
func RegisterEngine(queryID string, engine *QueryEngine) {
	if queryID == "" {
		return
	}
	engineRegistryMu.Lock()
	defer engineRegistryMu.Unlock()
	if engine == nil {
		delete(engineRegistry, queryID)
		return
	}
	engineRegistry[queryID] = engine
}

// LookupEngine returns the QueryEngine previously registered for queryID.
// Returns (nil, false) when no engine exists for the key.
func LookupEngine(queryID string) (*QueryEngine, bool) {
	if queryID == "" {
		return nil, false
	}
	engineRegistryMu.RLock()
	defer engineRegistryMu.RUnlock()
	e, ok := engineRegistry[queryID]
	return e, ok
}

// UnregisterEngine drops the engine for queryID. Safe to call with an
// unknown key. Exposed so tools/ can clean up after a user discards the
// result of a query.
func UnregisterEngine(queryID string) {
	if queryID == "" {
		return
	}
	engineRegistryMu.Lock()
	delete(engineRegistry, queryID)
	engineRegistryMu.Unlock()
}
