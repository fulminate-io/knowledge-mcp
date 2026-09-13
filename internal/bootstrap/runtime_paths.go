// SPDX-License-Identifier: Apache-2.0
package bootstrap

import (
	"fmt"
	"path/filepath"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/remote"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/filecrypt/machineid"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

func loadExplicitBootConfig(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("config-file must be absolute")
	}
	if _, err := config.Load(path); err != nil {
		return fmt.Errorf("load explicit config: %w", err)
	}
	return auth.ConfigureAccountPath(path)
}

// prepareRuntimePaths runs before any constructor or background goroutine.
// Supervised installations own process lifecycle and must supply every path.
func prepareRuntimePaths(f Config) error {
	if f.ConfigFile != "" {
		if err := loadExplicitBootConfig(f.ConfigFile); err != nil {
			return err
		}
	}
	if f.StateDir == "" {
		return nil
	}
	if !filepath.IsAbs(f.StateDir) || !filepath.IsAbs(f.GraphStorage) || !filepath.IsAbs(f.RootDir) || !filepath.IsAbs(f.LogFile) || f.ConfigFile == "" {
		return fmt.Errorf("state-dir requires absolute state, graph-storage, root, log-file and config-file paths")
	}
	if err := machineid.ConfigureCache(filepath.Join(f.StateDir, "machine-id")); err != nil {
		return err
	}
	if err := remote.ConfigureDiscoveryState(f.StateDir); err != nil {
		return err
	}
	return tools.ConfigureRuntimeState(f.StateDir)
}
