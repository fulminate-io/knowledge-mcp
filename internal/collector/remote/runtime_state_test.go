// SPDX-License-Identifier: Apache-2.0
package remote

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeDiscoveryState(t *testing.T) {
	old := defaultDiscoveryStore
	t.Cleanup(func() { defaultDiscoveryStore = old })
	canary := filepath.Join(t.TempDir(), "discovery.json")
	if err := os.WriteFile(canary, []byte(`{"fixture":"other"}`), 0600); err != nil {
		t.Fatal(err)
	}
	defaultDiscoveryStore = &discoveryStore{path: canary}
	root := t.TempDir()
	if err := ConfigureDiscoveryState(root); err != nil {
		t.Fatal(err)
	}
	changed, err := defaultDiscoveryStore.changed("fixture", "other")
	if err != nil || !changed {
		t.Fatalf("read external state: %v %v", changed, err)
	}
	if err := defaultDiscoveryStore.record(baselineCommit{key: "fixture", sig: "desktop"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "collect-discovery.json")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(canary)
	if err != nil || string(raw) != `{"fixture":"other"}` {
		t.Fatalf("canary changed: %s %v", raw, err)
	}
	if err := ConfigureDiscoveryState("relative"); err == nil {
		t.Fatal("relative state accepted")
	}
}
