package font

// export_test.go exposes package-internal symbols needed by tests in
// other test files (and per-T3-8 by the resolver_test.go caching
// regression). The file uses the `_test.go` suffix so it's compiled
// only into the test binary; production callers cannot reach these.

// ParseCMapCalls returns the cumulative parseCMap invocation count.
// Phase 9's TestFontResolver_DocScopeCaching uses this to assert the
// document-scoped resolver caches font decoders by content
// (BaseFont + sha256(ToUnicodeBytes)) rather than by *PageObject
// identity — iterating pages[i%3] for i=0..99 should produce
// exactly ONE parseCMap call.
func ParseCMapCalls() uint64 {
	return parseCMapCalls.Load()
}

// ResetParseCMapCalls zeroes the counter. Call at the top of any
// test that asserts a specific count so previous tests don't leak.
func ResetParseCMapCalls() {
	parseCMapCalls.Store(0)
}
