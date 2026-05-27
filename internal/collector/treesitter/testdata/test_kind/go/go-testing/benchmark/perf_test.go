// SPDX-License-Identifier: Apache-2.0

package fixture

import "testing"

func BenchmarkParseURL(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = parseURL("https://example.com")
	}
}

func parseURL(s string) string { return s }
