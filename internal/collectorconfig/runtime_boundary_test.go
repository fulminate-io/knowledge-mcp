// SPDX-License-Identifier: Apache-2.0
package collectorconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectPathBoundary(t *testing.T) {
	parent := t.TempDir()
	boundary := filepath.Join(parent, "desktop")
	cwd := filepath.Join(boundary, "workspace", "nested")
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	outside := ProjectPathIn(parent)
	if err := os.MkdirAll(filepath.Dir(outside), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := FindProjectPath(cwd); got != outside {
		t.Fatalf("positive ancestor control: %s", got)
	}
	if got := FindProjectPathWithin(cwd, boundary); got != "" {
		t.Fatalf("escaped boundary: %s", got)
	}
	inside := ProjectPathIn(boundary)
	if err := os.MkdirAll(filepath.Dir(inside), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := FindProjectPathWithin(cwd, boundary); got != inside {
		t.Fatalf("missing boundary config: %s", got)
	}
	if got := FindProjectPathWithin(parent, boundary); got != "" {
		t.Fatalf("accepted outside cwd: %s", got)
	}
	if got := FindProjectPathWithin("", boundary); got != "" {
		t.Fatalf("accepted empty cwd: %s", got)
	}
}
