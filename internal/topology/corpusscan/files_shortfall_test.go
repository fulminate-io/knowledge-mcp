// SPDX-License-Identifier: Apache-2.0

// files_shortfall_test.go — the post-walk accounting backstop, as a unit.

package corpusscan

import (
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/ast"
)

// TestFileScopeShortfall is the backstop's own unit, driven with synthetic walk
// stats because the end-to-end trigger for a file that resolves and is then not
// opened depends on the filesystem and on .gitignore, neither of which a test in
// this repository may construct.
func TestFileScopeShortfall(t *testing.T) {
	scope := &fileScope{named: []string{"a.go", "b.go"}, langFiles: []string{"a.go", "b.go"}}
	if err := fileScopeShortfall(scope, scope.expectedUnder(nil), "chk-1", walkStatsScanning(2)); err != nil {
		t.Fatalf("control: every named file opened is no shortfall, got %v", err)
	}
	if err := fileScopeShortfall(scope, scope.expectedUnder(nil), "chk-1", walkStatsScanning(3)); err != nil {
		t.Fatalf("control: a directory in the list can only ADD files, so a surplus is not a shortfall, got %v", err)
	}
	// THE EXPECTATION FOLLOWS A CHECK'S OWN NARROWING: scoped to one of the two
	// named files, opening one file is the whole of what that check owed.
	if err := fileScopeShortfall(scope, scope.expectedUnder([]string{"a.go"}), "chk-1", walkStatsScanning(1)); err != nil {
		t.Fatalf("control: a check scoped to one of the two named files owes one file, got %v", err)
	}
	err := fileScopeShortfall(scope, scope.expectedUnder(nil), "chk-1", walkStatsScanning(1))
	if err == nil {
		t.Fatal("one file of two opened is a silent drop unless it is refused")
	}
	for _, want := range []string{"chk-1", "2", "1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must carry the check and both counts; %q missing from %q", want, err)
		}
	}
}

// walkStatsScanning is a walk that opened n files and skipped nothing — the
// synthetic stats the backstop reads, so its arithmetic is driven directly
// rather than through a filesystem that would have to be coaxed into producing
// each count.
func walkStatsScanning(n int) ast.WalkStats {
	return ast.WalkStats{FilesScanned: n}
}
