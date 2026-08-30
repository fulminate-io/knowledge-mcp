// SPDX-License-Identifier: Apache-2.0

package web

import "testing"

// longProse is the sibling baseline the calibration gate measures against. A
// marker group is admitted only when every member is at most
// markerLengthRatio times the median text length of its parent's OTHER block
// children, so the surrounding prose has to be genuinely long for the ratio
// to mean anything.
const longProse = "A shell reads its argument string before running anything, and every " +
	"character it treats as punctuation is a chance for a caller to end one command " +
	"and begin another, which is the whole of the weakness."

// sectionHeadings returns the heading of every section in rec, in the order
// collectRecords walks them.
func sectionHeadings(rec *pageRecord) []string {
	got := collectRecords(rec)
	out := make([]string, 0, len(got.sections))
	for _, s := range got.sections {
		out = append(out, s.Heading)
	}
	return out
}

// noSectionNamed fails when any section's heading equals unwanted. It is the
// OVER-PROMOTION assertion: the heuristic's failure mode is turning ordinary
// page furniture into a section, and recordOwner cannot see that, because the
// furniture is emitted either way — as a paragraph or as a heading.
func noSectionNamed(t *testing.T, rec *pageRecord, unwanted string) {
	t.Helper()
	headings := sectionHeadings(rec)
	for _, h := range headings {
		if h == unwanted {
			t.Fatalf("%q was promoted to a section heading; all headings: %q", unwanted, headings)
		}
	}
}

// sectionDepthsByHeading indexes rec's sections by heading text. A heading
// that is absent reads back as depth 0, which no real section carries except
// the synthetic root, so a missing section fails a depth assertion rather
// than passing it vacuously.
func sectionDepthsByHeading(rec *pageRecord) map[string]int {
	out := map[string]int{}
	for _, s := range collectRecords(rec).sections {
		out[s.Heading] = s.Depth
	}
	return out
}

// TestParsePage_HeuristicHeading covers the THIRD arm: a repeated, short,
// inline-only classed sibling series, promoted one level below the nearest
// authoritative heading that precedes it. The arm runs only where neither
// authoritative signal exists, which is this walker's form of
// applyHeadingLevels' "if blocks[i].HeadingLevel != 0 { continue }".
//
// Both of its gates are load-bearing and each has its own catcher here.
// Dropping REPETITION promotes hohpe's byline and go101's nav strip; dropping
// CALIBRATION turns divprose's thirteen prose blocks into sections. In both
// cases the two CWE recovery subtests stay green, so a suite asserting only
// CWE recovery would ship either defect silently.
func TestParsePage_HeuristicHeading(t *testing.T) {
	t.Parallel()

	t.Run("cwe_markers_become_sections_owning_their_own_content", func(t *testing.T) {
		t.Parallel()
		rec := parseFixture(t, "cwe_table_layout.html")
		const under = "/Shell Argument Assembly/Entry 78: Shell Argument Assembly/"
		ownerMustBe(t, rec, paragraphContaining("A shell reads its argument string"),
			"Description prose", under+"Description")
		ownerMustBe(t, rec, paragraphContaining("Recovery is expensive"),
			"Common Consequences prose", under+"Common Consequences")
		ownerMustBe(t, rec, paragraphContaining("Entries in this catalog describe mistakes"),
			"Notes prose", under+"Notes")
	})

	// The counts are the ground-truth control from research: the same fixture
	// with its seven marker divs replaced by <h3> and nothing else touched
	// parses to 10 sections with these seven names at depth 3.
	t.Run("cwe_marker_sections_nest_under_the_native_heading", func(t *testing.T) {
		t.Parallel()
		rec := parseFixture(t, "cwe_table_layout.html")
		want := []string{
			"Description", "Common Consequences", "Demonstrative Examples",
			"Applicable Platforms", "Related Weaknesses",
			"Vulnerability Mapping Notes", "Notes",
		}
		depths := sectionDepthsByHeading(rec)
		found := 0
		for _, name := range want {
			depth, ok := depths[name]
			if !ok {
				continue
			}
			found++
			if depth != 3 {
				t.Fatalf("marker section %q is at depth %d, want 3 — one below the native <h2>", name, depth)
			}
		}
		if found != len(want) {
			t.Fatalf("recovered %d of %d marker sections; headings present: %q",
				found, len(want), sectionHeadings(rec))
		}
		if n := len(collectRecords(rec).sections); n != 10 {
			t.Fatalf("cwe_table_layout.html emitted %d sections, want 10 (the original 3 plus the 7 markers): %q",
				n, sectionHeadings(rec))
		}
	})

	// KNOWN-POSITIVE IN THE SAME DOCUMENT: the repeated class IS promoted, so
	// "the solo label is absent" cannot be satisfied by an arm that never
	// fires. That control is what makes this subtest red-first rather than a
	// characterization guard.
	t.Run("unrepeated_short_labels_are_not_promoted", func(t *testing.T) {
		t.Parallel()
		rec := parseInline(t, "", `<h1>Base</h1><div>`+
			`<div class="solo">Solo Label</div>`+
			`<div class="pair">Pair One</div><p>`+longProse+`</p>`+
			`<div class="pair">Pair Two</div><p>`+longProse+`</p></div>`)
		depths := sectionDepthsByHeading(rec)
		for _, name := range []string{"Pair One", "Pair Two"} {
			if depths[name] != 2 {
				t.Fatalf("control: repeated marker %q is at depth %d, want 2 — with no promotion at all the assertion below is vacuous: %q",
					name, depths[name], sectionHeadings(rec))
			}
		}
		noSectionNamed(t, rec, "Solo Label")
	})

	// A CHARACTERIZATION GUARD: green against the unfixed tree AND against the
	// fixed one, which is what proves it is a guard rather than a
	// reproduction. Its non-vacuity comes from the two CWE recovery subtests
	// above, which drive the same measurement non-zero in the same run — an
	// in-subtest positive control would make this red-first and destroy the
	// green-before-and-after property.
	t.Run("repeated_long_prose_siblings_are_not_promoted", func(t *testing.T) {
		t.Parallel()
		rejected := parseInline(t, "", `<h1>Base</h1><div>`+
			`<div class="m">`+longProse+`</div><p>`+longProse+`</p>`+
			`<div class="m">`+longProse+`</div><p>`+longProse+`</p></div>`)
		if n := len(collectRecords(rejected).sections); n != 1 {
			t.Fatalf("repeated LONG classed siblings produced %d sections, want 1: %q",
				n, sectionHeadings(rejected))
		}
		rec := parseFixture(t, "divprose_blocks.html")
		if n := len(collectRecords(rec).sections); n != 1 {
			t.Fatalf("divprose_blocks.html emitted %d sections, want 1: %q", n, sectionHeadings(rec))
		}
	})

	// A group with nothing to be shorter than has no evidence behind it, so it
	// is rejected. Another CHARACTERIZATION GUARD — green before and after —
	// whose non-vacuity comes from the CWE recovery subtests in the same run.
	// It is the second catcher for the calibration gate: dropping calibration
	// removes the baseline requirement along with the ratio, and both markers
	// here are promoted.
	t.Run("no_sibling_baseline_means_no_promotion", func(t *testing.T) {
		t.Parallel()
		rec := parseInline(t, "", `<h1>Base</h1><div>`+
			`<div class="m">Alpha</div><div class="m">Beta</div></div>`+
			`<p>`+longProse+`</p>`)
		noSectionNamed(t, rec, "Alpha")
		noSectionNamed(t, rec, "Beta")
	})

	t.Run("hohpe_byline_is_not_promoted", func(t *testing.T) {
		t.Parallel()
		rec := parseFixture(t, "hohpe_sample.html")
		noSectionNamed(t, rec, "by Gregor Hohpe")
		if n := len(collectRecords(rec).sections); n != 6 {
			t.Fatalf("hohpe_sample.html emitted %d sections, want 6 (unchanged): %q",
				n, sectionHeadings(rec))
		}
	})

	t.Run("go101_nav_strip_is_not_promoted", func(t *testing.T) {
		t.Parallel()
		rec := parseFixture(t, "go101_sample.html")
		noSectionNamed(t, rec, "Home | Articles | Book")
		if n := len(collectRecords(rec).sections); n != 4 {
			t.Fatalf("go101_sample.html emitted %d sections, want 4 (unchanged): %q",
				n, sectionHeadings(rec))
		}
	})

	// ARIA beats the heuristic. The markers are repeated, classed, short and
	// calibrated, so the heuristic map genuinely holds an entry for each —
	// proven in this subtest by parsing the same document with the ARIA
	// attributes stripped and observing the computed level 2. Both guard
	// orders put the prose under /Base/Alpha, so only the DEPTH separates
	// them: declared 4 with the correct order, computed 2 with the heuristic
	// guard placed above the ARIA guard.
	t.Run("aria_level_wins_over_the_heuristic", func(t *testing.T) {
		t.Parallel()
		plain := parseInline(t, "", `<h1>Base</h1><div>`+
			`<div class="marker">Alpha</div><p>`+longProse+`</p>`+
			`<div class="marker">Beta</div><p>`+longProse+`</p></div>`)
		if d := sectionDepthsByHeading(plain)["Alpha"]; d != 2 {
			t.Fatalf("control: without the ARIA attributes the heuristic must compute depth 2 for %q, got %d (%q)",
				"Alpha", d, sectionHeadings(plain))
		}
		rec := parseInline(t, "", `<h1>Base</h1><div>`+
			`<div class="marker" role="heading" aria-level="4">Alpha</div><p>`+longProse+`</p>`+
			`<div class="marker" role="heading" aria-level="4">Beta</div><p>`+longProse+`</p></div>`)
		depths := sectionDepthsByHeading(rec)
		found := 0
		for _, name := range []string{"Alpha", "Beta"} {
			depth, ok := depths[name]
			if !ok {
				continue
			}
			found++
			if depth != 4 {
				t.Fatalf("%q opened at depth %d, want the declared 4 — the ARIA guard must sit ABOVE the heuristic guard, which computes 2 here",
					name, depth)
			}
		}
		if found != 2 {
			t.Fatalf("found %d of 2 aria-declared marker sections: %q", found, sectionHeadings(rec))
		}
	})

	// Native beats the heuristic. A CHARACTERIZATION GUARD, green before and
	// after: the <h2> carries the marker group's own class and sits under an
	// <h3>, so the heuristic's computed level for that series is 4 while the
	// native level is 2. It reddens if candidacy stops excluding native
	// headings AND the guards are inverted — the walker's form of
	// applyHeadingLevels preserving an upstream authoritative level. The
	// markers' own placement one level below the native heading is asserted on
	// the real fixture by cwe_marker_sections_nest_under_the_native_heading.
	t.Run("native_heading_wins_over_the_heuristic", func(t *testing.T) {
		t.Parallel()
		rec := parseInline(t, "", `<h1>Base</h1><h3>Sub</h3><div>`+
			`<h2 class="m">Native</h2><p>`+longProse+`</p>`+
			`<div class="m">Marker One</div><p>`+longProse+`</p>`+
			`<div class="m">Marker Two</div><p>`+longProse+`</p></div>`)
		if depth := sectionDepthsByHeading(rec)["Native"]; depth != 2 {
			t.Fatalf(`<h2 class="m">Native</h2> opened at depth %d, want its native 2: %q`,
				depth, sectionHeadings(rec))
		}
	})
}
