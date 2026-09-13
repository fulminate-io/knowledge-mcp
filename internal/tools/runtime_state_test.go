// SPDX-License-Identifier: Apache-2.0
package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collectorconfig"
)

func TestRuntimeStateOwnsWatermarks(t *testing.T) {
	oldBoundary := collectorRuntimeBoundary
	t.Cleanup(func() { collectorRuntimeBoundary = oldBoundary })
	oldManifest, oldWatermarks := defaultRepoManifest, defaultSyncWatermarkStore
	oldPath, oldErr := collectorUserConfigPath, collectorUserConfigErr
	t.Cleanup(func() {
		defaultRepoManifest = oldManifest
		defaultSyncWatermarkStore = oldWatermarks
		collectorUserConfigPath = oldPath
		collectorUserConfigErr = oldErr
	})
	canary := filepath.Join(t.TempDir(), "watermarks.json")
	if err := os.WriteFile(canary, []byte(`{"knowledge/example":"canary"}`), 0600); err != nil {
		t.Fatal(err)
	}
	defaultSyncWatermarkStore = &syncWatermarkStore{path: canary}
	root := t.TempDir()
	if err := ConfigureRuntimeState(root); err != nil {
		t.Fatal(err)
	}
	if defaultSyncWatermarkStore.path != filepath.Join(root, "sync_watermarks.json") {
		t.Fatal("runtime retained another installation watermark path")
	}
	if got := defaultSyncWatermarkStore.Load("knowledge", "example"); got != "" {
		t.Fatalf("read another installation: %q", got)
	}
	if err := defaultSyncWatermarkStore.Save("knowledge", "example", "desktop"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(canary)
	if err != nil || string(raw) != `{"knowledge/example":"canary"}` {
		t.Fatalf("canary changed: %s %v", raw, err)
	}
}

type runtimePathDeps struct {
	ClientDeps
	root string
}

func (d runtimePathDeps) RootDir() string { return d.root }
func TestRuntimeCollectorBoundary(t *testing.T) {
	oldBoundary := collectorRuntimeBoundary
	oldPath, oldErr := collectorUserConfigPath, collectorUserConfigErr
	t.Cleanup(func() {
		collectorRuntimeBoundary = oldBoundary
		collectorUserConfigPath = oldPath
		collectorUserConfigErr = oldErr
	})
	parent := t.TempDir()
	root := filepath.Join(parent, "desktop")
	cwd := filepath.Join(root, "workspace")
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	outside := collectorconfig.ProjectPathIn(parent)
	if err := os.MkdirAll(filepath.Dir(outside), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	collectorRuntimeBoundary = root
	collectorUserConfigPath = filepath.Join(root, "collectors.json")
	collectorUserConfigErr = nil
	loader, err := collectorLoader(context.Background(), runtimePathDeps{root: cwd})
	if err != nil || loader.ProjectPath != "" {
		t.Fatalf("default workspace escaped: %+v %v", loader, err)
	}
	loader, err = collectorLoader(context.Background(), runtimePathDeps{root: parent})
	if err != nil || loader.ProjectPath != outside {
		t.Fatalf("explicit external repository lost scope: %+v %v", loader, err)
	}
}
