// SPDX-License-Identifier: Apache-2.0

package web

import (
	"slices"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// markerLengthRatio is the calibration constant of the THIRD heading arm: a
// promoted marker's text may be at most this fraction of the median text
// length of its parent's other block children. HTML carries no font metric,
// so the calibrated analog of the PDF classifier's "larger than body size"
// is "much shorter than the sibling content it labels".
const markerLengthRatio = 0.5

// authHeading is one AUTHORITATIVE heading — native h1-h6 or an element
// carrying role="heading" with an explicit aria-level — recorded in document
// order so a marker group can be placed one level below the one preceding it.
type authHeading struct {
	level, order int
}

// markerCandidate is one element that passed the candidacy test: classed,
// block-level, inline-only, not already a native heading, non-empty text.
type markerCandidate struct {
	node, parent *html.Node
	class        string
	textLen      int
	order        int
	depth        int
}

// markerGroup is a set of candidates sharing a parent and a normalized class.
// depth and order are taken from the group's first member.
//
// median is the CALIBRATION BASELINE this group was measured against — the
// median text length of its parent's other block children. It is set by
// calibrateMarkerGroups on the groups that survive, and it is retained rather
// than discarded because it is half of the comparison that promoted the group:
// a reader who sees only the assigned level cannot tell a marker that cleared
// the gate by a wide margin from one that scraped it.
type markerGroup struct {
	members      []markerCandidate
	depth, order int
	median       float64
}

// headingSignal is the level the heuristic pre-pass assigned a presentation
// marker, TOGETHER WITH THE MEASUREMENTS THAT PRODUCED IT.
//
// The pre-pass already computes every field here and previously threw all but
// the level away, which left the promotion unauditable downstream: a section
// node said "I am a level 3 heading" and nothing said why. Carrying the inputs
// makes the verdict checkable by whoever consumes the graph — the group key it
// was repeated under, its own text length, the sibling baseline it was measured
// against, and how many members the repetition gate actually saw.
type headingSignal struct {
	level         int
	classGroup    string
	textLen       int
	siblingMedian float64
	groupSize     int
}

// isSectionBoundary reports whether a ends the scope of every heading opened
// inside it. The set is exactly the HTML spec's own two lists — SECTIONING
// CONTENT and SECTIONING ROOTS — written as two case arms so a reader can
// check each against the spec rather than against this comment. Those lists
// ARE the outline algorithm's definition of where a heading's scope ends;
// nothing here is a heuristic about page furniture.
//
// header, main and div are deliberately NOT in the set: none of the three is
// sectioning content or a sectioning root, and adding them turns
// <header><h1>Post Title</h1></header><p>body</p> into an orphaned body.
// TestParsePage_NonBoundaryContainers is the gate that reddens if the set is
// widened that way. footer is likewise absent — adding it changes no outcome
// on any fixture, so it would be an addition with no evidence behind it.
// table and th are absent because a DATA table is already terminal in
// handleTable and a LAYOUT table's content cell is reached as atom.Td, which
// IS in the set.
func isSectionBoundary(a atom.Atom) bool {
	switch a {
	// Sectioning content.
	case atom.Article, atom.Aside, atom.Nav, atom.Section:
		return true
	// Sectioning roots.
	case atom.Blockquote, atom.Body, atom.Details, atom.Dialog,
		atom.Fieldset, atom.Figure, atom.Td:
		return true
	}
	return false
}

// closeSectionsOpenedSince pops every section whose push-sequence number is
// greater than mark, leaving the sections that were already open when the
// boundary was entered.
//
// KEYING ON THE SEQUENCE NUMBER IS LOAD-BEARING, and the two obvious
// alternatives are measurably wrong on
// <h1>Outer</h1><h2>Mid</h2><article><h2>Sibling</h2><p>i</p></article><p>after</p>,
// where "after" must land on /Outer:
//
//   - A stack-LENGTH mark taken at entry UNDER-POPS. An inner heading that
//     popped an outer one leaves the stack no deeper than it found it, so the
//     length mark sees nothing to unwind and "after" stays on /Outer/Sibling.
//   - A remembered POINTER to the section that was on top at entry OVER-POPS.
//     That section was itself popped from inside the boundary, so the loop
//     never finds it again and runs to the root, leaving "after" on "/".
//
// boundary_closes_only_sections_opened_inside_it is the only subtest that
// separates the three, and under the pointer form every other subtest in the
// package passes.
func (w *walker) closeSectionsOpenedSince(mark int) {
	for len(w.stack) > 1 && w.stack[len(w.stack)-1].seq > mark {
		w.stack = w.stack[:len(w.stack)-1]
	}
}

// isNativeHeading reports whether a is one of the six native heading tags.
// This is the FIRST and most authoritative of the three section-opening
// arms: a native h1-h6 never consults its own aria-level and is never a
// candidate for the presentation heuristic.
func isNativeHeading(a atom.Atom) bool {
	switch a {
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		return true
	}
	return false
}

// ariaHeadingLevel reads the SECOND arm's signal: role="heading" carrying an
// explicit aria-level. It returns the declared level and true only when the
// role token list contains "heading" and aria-level parses to 1..6.
//
// WAI-ARIA 1.2 defines a DEFAULT level of 2 for role="heading" with no
// aria-level, and this arm deliberately does not implement it. A missing,
// unparseable or out-of-range level means NOT a heading, and the element
// falls through to its existing treatment — the presentation heuristic below
// now covers the case a defaulted level would have caught, so the loose
// reading buys nothing and costs a width.
//
// The len(n.Attr) == 0 early return keeps the common attribute-free element
// off the attribute scan entirely.
func ariaHeadingLevel(n *html.Node) (int, bool) {
	if n == nil || len(n.Attr) == 0 {
		return 0, false
	}
	if !hasToken(getAttr(n, "role"), "heading") {
		return 0, false
	}
	level, err := strconv.Atoi(strings.TrimSpace(getAttr(n, "aria-level")))
	if err != nil || level < 1 || level > 6 {
		return 0, false
	}
	return level, true
}

// hasToken reports whether the space-separated token list contains want,
// compared case-insensitively. HTML's role and rel attributes are both token
// lists, so each token is tested individually and a substring such as
// "nofollowup" or "headingless" does not match.
func hasToken(list, want string) bool {
	for tok := range strings.FieldsSeq(list) {
		if strings.EqualFold(tok, want) {
			return true
		}
	}
	return false
}

// heuristicHeadingLevels is the whole-document pre-pass behind the THIRD
// arm. It runs ONCE per page, from parsePage, and returns the level assigned
// to each presentation marker it promotes; handleStructural consults the
// result only after both authoritative arms have declined.
//
// THE SIGNAL IS A REPEATED, SHORT, INLINE-ONLY CLASSED SIBLING SERIES, and
// both gates below are load-bearing:
//
//   - REPETITION: at least two candidates share a parent AND a normalized
//     class. Without it a lone <p class="byline">by Gregor Hohpe</p> and a
//     one-off nav strip both become sections.
//   - CALIBRATION: every member is at most markerLengthRatio times the median
//     text length of that parent's OTHER block children, and a group with no
//     sibling baseline at all is rejected — a group with nothing to be
//     shorter than has no evidence behind it. Without it a page of repeated
//     prose divs turns every paragraph into a heading.
//
// It must run AFTER pruneHiddenNodes, or a hidden repeated marker series
// would be admitted into a group and inflate it.
func (w *walker) heuristicHeadingLevels(doc *html.Node) map[*html.Node]headingSignal {
	auth, cands := w.scanForMarkers(doc)
	return assignMarkerLevels(w.calibrateMarkerGroups(groupMarkers(cands)), auth)
}

// hasBlockChild reports whether n holds block-level content. It is
// containsBlockLevel expressed over the walker's shared isBlockLevel memo:
// the per-child answers it needs are the same ones the walk that follows
// will ask for, so each element is partitioned once per page rather than
// once here and once again in walkChildren.
func (w *walker) hasBlockChild(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if w.isBlockLevel(c) {
			return true
		}
	}
	return false
}

// scanForMarkers walks doc once, collecting the authoritative headings and
// the marker candidates in a single document-order pass. order is a shared
// counter over both, so "the nearest authoritative heading preceding this
// group" is a plain comparison later.
func (w *walker) scanForMarkers(doc *html.Node) ([]authHeading, []markerCandidate) {
	var (
		auth  []authHeading
		cands []markerCandidate
		order int
	)
	var visit func(*html.Node, int)
	visit = func(n *html.Node, depth int) {
		if n.Type == html.ElementNode {
			if isNonRenderable(n.DataAtom) || n.DataAtom == atom.Head {
				return
			}
			order++
			switch level, ok := ariaHeadingLevel(n); {
			case isNativeHeading(n.DataAtom):
				auth = append(auth, authHeading{level: headingDepth(n.DataAtom), order: order})
			case ok:
				auth = append(auth, authHeading{level: level, order: order})
			}
			if c, isCand := w.markerCandidateAt(n, order, depth); isCand {
				cands = append(cands, c)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c, depth+1)
		}
	}
	visit(doc, 0)
	return auth, cands
}

// markerCandidateAt applies the candidacy test to a single element. It is a
// function rather than a closure inside scanForMarkers's walk so the branches
// stay flat, and it is where text is materialized — only classed inline-only
// blocks ever pay for a collectProseText.
//
// A NATIVE HEADING IS NEVER A CANDIDATE. That exclusion is this walker's form
// of applyHeadingLevels preserving an upstream authoritative level: the
// heuristic map can therefore never hold an h1-h6, whatever class it carries.
// An element carrying role="heading" IS still a candidate, deliberately — the
// ARIA arm outranks the heuristic at dispatch, and keeping such an element in
// the map is what makes that ordering observable rather than assumed.
func (w *walker) markerCandidateAt(n *html.Node, order, depth int) (markerCandidate, bool) {
	if isNativeHeading(n.DataAtom) {
		return markerCandidate{}, false
	}
	fields := strings.Fields(strings.ToLower(getAttr(n, "class")))
	if len(fields) == 0 {
		return markerCandidate{}, false
	}
	if !w.isBlockLevel(n) || w.hasBlockChild(n) {
		return markerCandidate{}, false
	}
	text := strings.TrimSpace(collectProseText(n))
	if text == "" {
		return markerCandidate{}, false
	}
	slices.Sort(fields)
	return markerCandidate{
		node:    n,
		parent:  n.Parent,
		class:   strings.Join(fields, " "),
		textLen: len([]rune(text)),
		order:   order,
		depth:   depth,
	}, true
}

// groupMarkers partitions candidates by (parent, normalized class) and keeps
// only the groups with at least two members — the REPETITION gate. Groups
// are returned in the document order of their first member.
func groupMarkers(cands []markerCandidate) []markerGroup {
	type key struct {
		parent *html.Node
		class  string
	}
	byKey := map[key][]markerCandidate{}
	var order []key
	for _, c := range cands {
		k := key{parent: c.parent, class: c.class}
		if _, seen := byKey[k]; !seen {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], c)
	}
	var out []markerGroup
	for _, k := range order {
		members := byKey[k]
		if len(members) < 2 {
			continue
		}
		out = append(out, markerGroup{members: members, depth: members[0].depth, order: members[0].order})
	}
	return out
}

// calibrateMarkerGroups applies the CALIBRATION gate: every member must be at
// most markerLengthRatio times the median text length of its parent's other
// block-level element children. The baseline is computed lazily, per
// surviving group, so no text is materialized for a page with no repeated
// classed series at all. A group whose parent has no other block children
// supplies no baseline and is rejected.
//
// The baseline is RETAINED on each surviving group rather than being consumed
// by the comparison and dropped, so the measurement travels with the verdict
// all the way to the emitted node.
func (w *walker) calibrateMarkerGroups(groups []markerGroup) []markerGroup {
	out := make([]markerGroup, 0, len(groups))
	for _, g := range groups {
		member := make(map[*html.Node]bool, len(g.members))
		for _, m := range g.members {
			member[m.node] = true
		}
		var others []int
		for c := g.members[0].parent.FirstChild; c != nil; c = c.NextSibling {
			if member[c] || !w.isBlockLevel(c) || isNonRenderable(c.DataAtom) {
				continue
			}
			others = append(others, len([]rune(strings.TrimSpace(collectProseText(c)))))
		}
		if len(others) == 0 {
			continue
		}
		median := medianLength(others)
		if limit := markerLengthRatio * median; !overLimit(g.members, limit) {
			g.median = median
			out = append(out, g)
		}
	}
	return out
}

// medianLength returns the median of lengths, averaging the two middle
// values on an even count. It sorts in place; the caller owns the slice.
func medianLength(lengths []int) float64 {
	slices.Sort(lengths)
	mid := len(lengths) / 2
	if len(lengths)%2 == 1 {
		return float64(lengths[mid])
	}
	return float64(lengths[mid-1]+lengths[mid]) / 2
}

// overLimit reports whether ANY member exceeds limit. The gate is universal,
// not average: one long member disqualifies the whole group, because a series
// that mixes labels and prose is not a label series.
func overLimit(members []markerCandidate, limit float64) bool {
	for _, m := range members {
		if float64(m.textLen) > limit {
			return true
		}
	}
	return false
}

// assignMarkerLevels places each surviving group RELATIVE TO THE
// AUTHORITATIVE STRUCTURE, never from 1: a group takes the level of the
// nearest authoritative heading preceding its first member, plus one, capped
// at 6. Groups sharing a base heading are ranked by DOM depth ascending and
// take successive levels. A group with no authoritative heading before it
// starts at 1.
func assignMarkerLevels(groups []markerGroup, auth []authHeading) map[*html.Node]headingSignal {
	out := map[*html.Node]headingSignal{}
	byBase := map[int][]markerGroup{}
	var bases []int
	for _, g := range groups {
		base := -1
		for i, a := range auth {
			if a.order >= g.order {
				break
			}
			base = i
		}
		if _, seen := byBase[base]; !seen {
			bases = append(bases, base)
		}
		byBase[base] = append(byBase[base], g)
	}
	for _, base := range bases {
		peers := byBase[base]
		slices.SortStableFunc(peers, func(a, b markerGroup) int { return a.depth - b.depth })
		level := 1
		if base >= 0 {
			level = auth[base].level + 1
		}
		for i, g := range peers {
			for _, m := range g.members {
				out[m.node] = headingSignal{
					level:         min(level+i, 6),
					classGroup:    m.class,
					textLen:       m.textLen,
					siblingMedian: g.median,
					groupSize:     len(g.members),
				}
			}
		}
	}
	return out
}
