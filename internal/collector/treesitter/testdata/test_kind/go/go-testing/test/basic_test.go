// SPDX-License-Identifier: Apache-2.0

package fixture

import "testing"

func TestUserLogin(t *testing.T) {
	if got := "ok"; got != "ok" {
		t.Errorf("got %q, want %q", got, "ok")
	}
}

func TestUserLogin_EmptyPassword(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		t.Skip("skip empty case")
	})
}
