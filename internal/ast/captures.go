// SPDX-License-Identifier: Apache-2.0

// captures.go — the per-match Captures accumulator: the name-to-Capture binding
// map and the helpers the walker binds through.
//
// Split out of walker.go, which keeps the match decisions themselves. The two
// halves answer different questions: walker.go decides WHETHER a pattern node and
// a target node correspond, and this file records WHAT a successful
// correspondence bound. The other three per-match records — the token alignment,
// the dropped spans and the skipped comments — hang off this same struct and live
// in alignment.go, which documents their speculation-rollback discipline.

package ast

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// Captures is the per-match name → Capture binding accumulator. Sequence
// captures populate Children + StartByte + EndByte; single captures leave
// Children nil. aligns is the ordered literal-token alignment record for the
// same match attempt and dropped holds the pattern spans its promotions threw
// away — see alignment.go.
type Captures struct {
	byName   map[string]Capture
	aligns   []TokenAlign
	dropped  []byteRange
	comments []byteRange
}

// newCaptures returns an empty Captures binding accumulator.
func newCaptures() *Captures {
	return &Captures{
		byName:   make(map[string]Capture),
		aligns:   make([]TokenAlign, 0, alignInitialCap),
		dropped:  make([]byteRange, 0, dropInitialCap),
		comments: make([]byteRange, 0, commentInitialCap),
	}
}

// reset clears all bindings so the Captures can be reused for a new
// match attempt without allocating a new map. All three side records — the two
// pattern-side ones (aligns, dropped) and the target-side comment spans — are
// truncated rather than dropped: reset runs once per candidate node, and any
// record leaked from a rejected candidate would corrupt the next match.
func (c *Captures) reset() {
	for k := range c.byName {
		delete(c.byName, k)
	}
	c.aligns = c.aligns[:0]
	c.dropped = c.dropped[:0]
	c.comments = c.comments[:0]
}

// clearNodeMap removes all entries from a node map for reuse.
func clearNodeMap(m map[string]*sitter.Node) {
	for k := range m {
		delete(m, k)
	}
}

// bindNode records a single-node capture under name (or appends a fresh
// per-occurrence binding when the name is empty / wildcard). Multiple
// captures with the same name OVERWRITE — the v2 design rejects implicit
// binding equality, so named-collision is a callsite concern (B.4
// same_node leaf is the explicit identity check).
func (c *Captures) bindNode(name string, n *sitter.Node, src []byte) {
	if name == "" || n == nil {
		return
	}
	c.byName[name] = nodeToCapture(n, src)
}

// bindSeq records a sequence capture: name → Capture with Children
// populated, Text spanning [first.StartByte, last.EndByte). Empty seq
// (no siblings) records {Text: "", Children: []}.
//
// THE SEPARATOR RULE. A seq shadow consumes whole sibling spans, anonymous
// tokens included — it has to, because the pattern siblings that follow it
// align against the target's own anonymous tokens. So the commas between
// parameters and the semicolons between statements arrive here. Text KEEPS
// them: it is the verbatim source span, which is what makes a seq capture
// re-interpolate as valid source. Children DROPS them: it carries semantic
// siblings only, so a two-parameter capture reads as two parameters rather
// than two parameters and a comma.
func (c *Captures) bindSeq(name string, siblings []*sitter.Node, src []byte) {
	if name == "" {
		return
	}
	cap := Capture{
		Children: make([]Capture, 0, len(siblings)),
	}
	if len(siblings) > 0 {
		first := siblings[0]
		last := siblings[len(siblings)-1]
		cap.StartByte = first.StartByte()
		cap.EndByte = last.EndByte()
		cap.Line = int(first.StartPoint().Row) + 1
		cap.Text = string(src[first.StartByte():last.EndByte()])
		for _, s := range siblings {
			if !s.IsNamed() {
				continue
			}
			cap.Children = append(cap.Children, nodeToCapture(s, src))
		}
		// Sequence captures don't carry a Kind — the children carry kinds.
	}
	c.byName[name] = cap
}

// nodeToCapture builds a Capture from a single tree-sitter node: text,
// kind, line, byte range. Sequence captures call bindSeq, which builds
// the outer-span Capture itself and uses nodeToCapture only for child
// entries.
func nodeToCapture(n *sitter.Node, src []byte) Capture {
	return Capture{
		Text:      n.Content(src),
		Kind:      n.Type(),
		Line:      int(n.StartPoint().Row) + 1,
		StartByte: n.StartByte(),
		EndByte:   n.EndByte(),
	}
}
