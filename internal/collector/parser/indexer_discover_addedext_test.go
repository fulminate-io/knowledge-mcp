// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"path/filepath"
	"slices"
	"testing"
)

// TestDiscoverAddedExtensions is the collector-side half of the extension
// routing change, and it measures the consumer rather than the table: an
// extension DetectLanguage now knows is worth nothing unless the discovery gate
// stops declining the file under skip_unknown_lang, because a declined file
// never reaches a chunker at all.
//
// The ast tool is the other consumer and is covered by its own test in that
// package. Two independent measurements that must agree — one alone proves
// neither consumer changed behavior.
func TestDiscoverAddedExtensions(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "keep.go"), "package main")
	writeFile(t, filepath.Join(dir, "bundle.cjs"), "module.exports = function f() { return 1; };\n")
	writeFile(t, filepath.Join(dir, "Rakefile"), "task :build do\n  puts 'x'\nend\n")
	// KNOWN-NEGATIVE CONTROL in the same walk. Without a file that is STILL
	// declined under this rule, a build where skip_unknown_lang stopped firing
	// altogether would satisfy the admission assertions below.
	writeFile(t, filepath.Join(dir, "styles.less"), "@c: #fff;\n.a { color: @c; }\n")
	writeFile(t, filepath.Join(dir, "data.csv"), "a,b,c\n")

	files, rep, err := DiscoverFilesReporting(t.Context(), dir, DiscoveryOptions{})
	if err != nil {
		t.Fatalf("DiscoverFilesReporting: %v", err)
	}

	for _, want := range []string{"bundle.cjs", "Rakefile"} {
		if !slices.Contains(files, want) {
			t.Errorf("%s must be admitted, got %v", want, files)
		}
	}
	// The control file is still declined, and by this exact rule.
	for _, unwanted := range []string{"styles.less", "data.csv"} {
		if slices.Contains(files, unwanted) {
			t.Errorf("%s must still be declined, got %v", unwanted, files)
		}
	}
	if got := rep.ExcludedByRule[RuleUnknownLang]; got != 2 {
		t.Errorf("%s = %d, want 2 (styles.less and data.csv) — a zero here would mean the rule stopped firing rather than that the new extensions are admitted",
			RuleUnknownLang, got)
	}
	// Known-positive control that the walk ran at all.
	if !slices.Contains(files, "keep.go") {
		t.Fatalf("keep.go missing — the walk found nothing, so every assertion above is vacuous: %v", files)
	}
}
