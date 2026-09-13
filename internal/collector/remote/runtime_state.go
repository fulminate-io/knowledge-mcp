// SPDX-License-Identifier: Apache-2.0
package remote

import (
	"fmt"
	"path/filepath"
)

// ConfigureDiscoveryState selects installation metadata before collection starts.
func ConfigureDiscoveryState(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("discovery state directory must be absolute")
	}
	defaultDiscoveryStore = &discoveryStore{path: filepath.Join(dir, "collect-discovery.json")}
	return nil
}
