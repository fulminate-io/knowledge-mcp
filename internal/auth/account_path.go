// SPDX-License-Identifier: Apache-2.0
package auth

import (
	"fmt"
	"path/filepath"
)

// ConfigureAccountPath binds all account consumers to the explicit client
// configuration. Call during startup before constructing transports.
func ConfigureAccountPath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("account configuration path must be absolute")
	}
	selectedAccountMu.Lock()
	defer selectedAccountMu.Unlock()
	selectedAccountInst = NewAccountSelection(path, DefaultAccountCheckTTL)
	return nil
}
