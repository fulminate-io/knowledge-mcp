// SPDX-License-Identifier: Apache-2.0
package tools

import (
	"fmt"
	"path/filepath"
)

var collectorRuntimeBoundary string

// ConfigureRuntimeState selects installation-local metadata before serving.
// It must run before any tool dispatch or background goroutine.
func ConfigureRuntimeState(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("runtime state directory must be absolute")
	}
	collectorRuntimeBoundary = filepath.Clean(dir)
	defaultSyncWatermarkStore = &syncWatermarkStore{path: filepath.Join(dir, "sync_watermarks.json")}
	defaultRepoManifest = &repoManifest{path: filepath.Join(dir, "repos.json")}
	collectorUserConfigPath = filepath.Join(dir, "collectors.json")
	collectorUserConfigErr = nil
	return nil
}
