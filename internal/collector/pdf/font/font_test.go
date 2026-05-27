package font

import "testing"

// TestFont_PackageCompiles is the T1 placeholder smoke. The font
// sub-package (T3) declares no types in T1; this test exists so
// `go test ./collector/pdf/font/` doesn't report "no test files",
// keeping the test invocation consistent across all sub-packages.
// T3 replaces this with real CMap / Encoding / Resolver coverage.
func TestFont_PackageCompiles(t *testing.T) {
	t.Parallel()
	// Package-compilation is the assertion. No types yet to exercise.
}
