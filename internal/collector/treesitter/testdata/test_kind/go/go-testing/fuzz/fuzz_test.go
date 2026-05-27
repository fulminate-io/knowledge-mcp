// SPDX-License-Identifier: Apache-2.0

package fixture

import "testing"

func FuzzParseURL(f *testing.F) {
	f.Add("https://example.com")
	f.Fuzz(func(t *testing.T, s string) {
		_ = s
	})
}
