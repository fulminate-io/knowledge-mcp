// SPDX-License-Identifier: Apache-2.0

package fixture

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// setup
	code := m.Run()
	// teardown
	os.Exit(code)
}
