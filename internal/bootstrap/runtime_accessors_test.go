// SPDX-License-Identifier: Apache-2.0
package bootstrap

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestRuntimeAccessorsStayInInstallation(t *testing.T) {
	root := t.TempDir()
	c := &client{runtimeStateDir: root, version: "fixture-version"}
	if got, ok := c.DaemonVersion(); !ok || got != "fixture-version" {
		t.Fatalf("runtime status did not use in-process version: %q %v", got, ok)
	}
	analyzer := c.UsageAnalyzer()
	if analyzer == nil {
		t.Fatal("analyzer unavailable")
	}
	got := reflect.ValueOf(analyzer).Elem().FieldByName("cacheRoot").String()
	if got != filepath.Join(root, "transcripts-cache") {
		t.Fatalf("analyzer escaped state: %s", got)
	}
	if c.UsageAnalyzer() != analyzer {
		t.Fatal("analyzer not cached")
	}
}
