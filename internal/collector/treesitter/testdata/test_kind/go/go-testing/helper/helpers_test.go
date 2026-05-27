// SPDX-License-Identifier: Apache-2.0

package fixture

import "testing"

// newFixture is a test helper — not a TestX function (lowercase after "new").
func newFixture(t *testing.T) string {
	t.Helper()
	return "fixture"
}

// TestifyMe is a helper despite the Test prefix — `TestifyMe` has lowercase
// 'i' after `Test`, so the Go testing package would NOT pick it up. Per
// classifyTestKindGo (chunker_go.go:178), this falls through to TestKindHelper.
func TestifyMe(s string) string {
	return s
}
