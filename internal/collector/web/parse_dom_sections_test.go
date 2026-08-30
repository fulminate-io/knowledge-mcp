// SPDX-License-Identifier: Apache-2.0

package web

import (
	"strings"
	"testing"
)

// recordOwner walks rec's section tree in document order and returns the
// "/"-joined heading path of the section OWNING the first record want
// matches, plus whether any record matched at all.
//
// collectRecords deliberately flattens ownership away — it answers "does
// this record exist", and both defects this file reproduces leave every
// record present. The property under test is which section a record hangs
// off, so these assertions need the path and not the set.
//
// The synthetic depth-0 root contributes an empty heading, so a record
// attached directly to it reads "/".
func recordOwner(rec *pageRecord, want func(contentRecord) bool) (string, bool) {
	var walk func(*sectionRecord, []string) (string, bool)
	walk = func(s *sectionRecord, path []string) (string, bool) {
		if s == nil {
			return "", false
		}
		here := make([]string, len(path), len(path)+1)
		copy(here, path)
		here = append(here, s.Heading)
		for _, child := range s.Children {
			if nested, ok := child.(nestedSectionRecord); ok {
				if got, found := walk(nested.Section, here); found {
					return got, true
				}
				continue
			}
			if want(child) {
				return "/" + strings.Join(here, "/"), true
			}
		}
		return "", false
	}
	for _, s := range rec.TopSections {
		if got, found := walk(s, nil); found {
			return got, true
		}
	}
	return "", false
}

// paragraphContaining matches a paragraphRecord whose Text contains sub.
func paragraphContaining(sub string) func(contentRecord) bool {
	return func(r contentRecord) bool {
		p, ok := r.(paragraphRecord)
		return ok && strings.Contains(p.Text, sub)
	}
}

// listContaining matches a listRecord one of whose item texts contains sub.
func listContaining(sub string) func(contentRecord) bool {
	return func(r contentRecord) bool {
		l, ok := r.(listRecord)
		if !ok {
			return false
		}
		for _, item := range l.Items {
			if strings.Contains(item.Text, sub) {
				return true
			}
		}
		return false
	}
}

// ownerMustBe fails when no record matches want, and separately when the
// section owning the first match is not wantPath. The two failures are
// reported differently on purpose: "record absent" means the fixture or the
// predicate is wrong, "attributed to X" means the walker is.
func ownerMustBe(t *testing.T, rec *pageRecord, want func(contentRecord) bool, label, wantPath string) {
	t.Helper()
	got, found := recordOwner(rec, want)
	if !found {
		t.Fatalf("%s: no matching record anywhere in the parsed page", label)
	}
	if got != wantPath {
		t.Fatalf("%s: attributed to %q, want %q", label, got, wantPath)
	}
}

// TestParsePage_SectionAttribution pins WHERE a record lands. Every record
// named here is emitted by both the unfixed and the fixed walker, so a count
// assertion is satisfied by either; only the owning section separates them.
func TestParsePage_SectionAttribution(t *testing.T) {
	t.Parallel()

	t.Run("hohpe_footer_leaves_content_section", func(t *testing.T) {
		t.Parallel()
		rec := parseFixture(t, "hohpe_sample.html")
		ownerMustBe(t, rec, paragraphContaining("Copyright 2003-2024"),
			"site footer copyright", "/")
	})

	t.Run("go101_footer_leaves_content_section", func(t *testing.T) {
		t.Parallel()
		rec := parseFixture(t, "go101_sample.html")
		ownerMustBe(t, rec, paragraphContaining("2016-2024 Go 101"),
			"site footer copyright", "/")
	})

	t.Run("cwe_footer_menu_leaves_entry_section", func(t *testing.T) {
		t.Parallel()
		rec := parseFixture(t, "cwe_table_layout.html")
		ownerMustBe(t, rec, listContaining("Terms of Use"),
			"#FooterMenu list", "/Shell Argument Assembly")
	})

	t.Run("sibling_after_layout_table_leaves_in_cell_section", func(t *testing.T) {
		t.Parallel()
		rec := parseInline(t, "", `<table role="presentation"><tr><td>`+
			`<h1>InCell</h1><p>inside the cell</p>`+
			`</td></tr></table><p>after the table</p>`)
		ownerMustBe(t, rec, paragraphContaining("inside the cell"),
			"body inside the layout cell", "/InCell")
		ownerMustBe(t, rec, paragraphContaining("after the table"),
			"sibling after the layout table", "/")
	})

	// The three-implementation discriminator. A heading INSIDE the boundary
	// pops one from OUTSIDE it, which is the only shape that separates the
	// prescribed push-sequence keying from a stack-length mark (under-pops,
	// leaving "after" on /Outer/Sibling) and from a remembered-pointer mark
	// (over-pops to the root, leaving "after" on "/"). An <h3> under an <h2>
	// discriminates nothing — all three forms agree on it.
	t.Run("boundary_closes_only_sections_opened_inside_it", func(t *testing.T) {
		t.Parallel()
		rec := parseInline(t, "", `<h1>Outer</h1><h2>Mid</h2>`+
			`<article><h2>Sibling</h2><p>i</p></article><p>after</p>`)
		ownerMustBe(t, rec, paragraphContaining("i"),
			"body inside the boundary", "/Outer/Sibling")
		ownerMustBe(t, rec, paragraphContaining("after"),
			"sibling after the boundary", "/Outer")
	})
}

// TestParsePage_NonBoundaryContainers is the OVER-REACH control on the
// boundary set. header, main and div are not sectioning content and not
// sectioning roots, so a heading wrapped in one of them keeps owning the
// body that follows as a sibling. Adding any of the three to
// isSectionBoundary reddens all three subtests here while leaving the five
// attribution assertions above green.
func TestParsePage_NonBoundaryContainers(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, tag string }{
		{"header_wrapped_heading_keeps_following_body", "header"},
		{"div_wrapped_heading_keeps_following_body", "div"},
		{"main_wrapped_heading_keeps_following_body", "main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := parseInline(t, "", "<"+tc.tag+"><h1>Post Title</h1></"+tc.tag+">"+
				"<p>body prose</p>")
			ownerMustBe(t, rec, paragraphContaining("body prose"),
				"body after a <"+tc.tag+">-wrapped heading", "/Post Title")
		})
	}
}

// TestParsePage_AriaHeading covers the SECOND arm: role="heading" carrying an
// explicit aria-level in 1..6. Two subtests are red-first; the other two are
// the arm's width controls.
func TestParsePage_AriaHeading(t *testing.T) {
	t.Parallel()

	t.Run("opens_section_at_declared_level", func(t *testing.T) {
		t.Parallel()
		rec := parseInline(t, "", `<div role="heading" aria-level="2">Alpha</div>`+
			`<p>alpha body</p>`)
		ownerMustBe(t, rec, paragraphContaining("alpha body"),
			"prose under an aria heading", "/Alpha")
	})

	t.Run("role_token_list_matches_case_insensitively", func(t *testing.T) {
		t.Parallel()
		rec := parseInline(t, "", `<div role="banner HEADING" aria-level="2">TokenList</div>`+
			`<p>token body</p>`)
		ownerMustBe(t, rec, paragraphContaining("token body"),
			"prose under a token-list aria heading", "/TokenList")
	})

	// The native guard sits ABOVE the ARIA guard, so a native h1-h6 never
	// consults its own aria-level.
	//
	// THE SECOND CASE IS THE ONE THAT GATES THE ORDERING, and the first cannot.
	// ariaHeadingLevel requires role="heading" before it reads aria-level at
	// all, so a bare <h2 aria-level="5"> falls to the native arm under EITHER
	// guard order and opens at 2 either way — measured by hoisting the ARIA
	// guard above the native one, which left the whole package green. Only an
	// element carrying BOTH signals separates the orders: native 2 with the
	// guards as written, the declared 5 with them inverted.
	t.Run("native_heading_wins_over_aria_level", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct{ name, attrs string }{
			{"aria-level alone", `aria-level="5"`},
			{"role and aria-level together", `role="heading" aria-level="5"`},
		} {
			rec := parseInline(t, "", `<h2 `+tc.attrs+`>Native</h2><p>native body</p>`)
			ownerMustBe(t, rec, paragraphContaining("native body"),
				"prose under a native heading ("+tc.name+")", "/Native")
			depth := 0
			for _, s := range collectRecords(rec).sections {
				if s.Heading == "Native" {
					depth = s.Depth
				}
			}
			if depth != 2 {
				t.Fatalf("<h2 %s> opened at depth %d, want its native 2 — the ARIA guard must sit BELOW the native one", tc.attrs, depth)
			}
		}
	})

	// WAI-ARIA 1.2 defines a DEFAULT level of 2 for role="heading" with no
	// aria-level. This arm deliberately does not implement it: a missing,
	// out-of-range or unparseable level means NOT a heading, and the element
	// falls through to its existing treatment. The known-positive contrast is
	// opens_section_at_declared_level above, which promotes the same shape the
	// moment a valid level is present.
	t.Run("missing_or_out_of_range_level_is_not_a_heading", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct{ name, attrs string }{
			{"missing", `role="heading"`},
			{"zero", `role="heading" aria-level="0"`},
			{"seven", `role="heading" aria-level="7"`},
			{"unparseable", `role="heading" aria-level="two"`},
		} {
			rec := parseInline(t, "", `<h1>Base</h1><div `+tc.attrs+`>Candidate</div>`+
				`<p>trailing prose</p>`)
			ownerMustBe(t, rec, paragraphContaining("trailing prose"),
				"trailing prose ("+tc.name+" aria-level)", "/Base")
		}
	})
}

// TestParsePage_ZeroHeadingPage_Unchanged holds the walker's behaviour on a
// page with no heading of any kind: one section with an empty name owning
// everything. divprose_blocks.html is thirteen repeated <div class="tmd-usual">
// prose blocks, so it is also the calibration gate's most direct victim — with
// the length calibration dropped, twelve of those become sections and both
// numbers below move. The 13 is measured off the fixture, not chosen.
func TestParsePage_ZeroHeadingPage_Unchanged(t *testing.T) {
	t.Parallel()

	rec := parseFixture(t, "divprose_blocks.html")
	got := collectRecords(rec)
	if len(got.sections) != 1 {
		t.Fatalf("divprose_blocks.html emitted %d sections, want 1: %q",
			len(got.sections), sectionHeadings(rec))
	}
	if got.sections[0].Heading != "" {
		t.Fatalf("sole section is named %q, want the empty synthetic root heading",
			got.sections[0].Heading)
	}
	if len(got.paragraphs) != 13 {
		t.Fatalf("divprose_blocks.html emitted %d paragraphs, want 13", len(got.paragraphs))
	}
	ownerMustBe(t, rec, paragraphContaining("A channel is a typed conduit"),
		"first prose block", "/")
}
