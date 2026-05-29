// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"path/filepath"
	"testing"
)

// criterion d8a4dfda: hintCodexAssetsIfStale (via codexAssetDrift) walks
// codexassets.Files vs split roots: counts missing/drift, and is a no-op
// (0/0) when everything is in sync. AGENTS.md is NOT part of the walk.
func TestCodexAssetDrift_MissingThenInSync(t *testing.T) {
	skills := t.TempDir()
	agents := t.TempDir()
	dest := codexDest{skills: skills, agents: agents}

	// Empty roots: every embedded asset is missing, none drifted.
	missing, drift, ok := codexAssetDrift(dest)
	if !ok {
		t.Fatal("codexAssetDrift ok=false on empty roots")
	}
	if missing == 0 {
		t.Error("missing = 0 on empty roots, want > 0")
	}
	if drift != 0 {
		t.Errorf("drift = %d on empty roots, want 0", drift)
	}

	// Install the embedded assets into those roots, then drift is 0/0.
	if err := runInstallCodexAssets([]string{
		"--skills-dest", skills,
		"--agents-dest", agents,
		"--agents-md-dest", filepath.Join(t.TempDir(), "AGENTS.md"),
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	missing2, drift2, ok2 := codexAssetDrift(dest)
	if !ok2 {
		t.Fatal("codexAssetDrift ok=false after install")
	}
	if missing2 != 0 || drift2 != 0 {
		t.Errorf("after install missing=%d drift=%d, want 0/0 (in sync)", missing2, drift2)
	}
}
