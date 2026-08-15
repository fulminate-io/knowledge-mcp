// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// emitTestBlockCallEdges runs the EXISTING Calls query over a LEAF test block's
// @decl node and appends the resulting edges as EdgeTestCalls.
//
// Nothing here is new extraction machinery. It is the same three-step shape
// emitDeclarationEdges uses at chunker_emit.go:138-143 — derive the reference
// site for this source's parent, extract, stamp the carriers — and the edges
// ride the unchanged resolution ladder afterwards. Only the type differs, and
// an unresolvable test reference terminates exactly as a production one does.
//
// LEAF-ONLY, AND THE RULE IS ABOUT TRUTH RATHER THAN VOLUME. An edge asserts
// that THIS node calls THAT node; a describe block does not call what its its
// call, it CONTAINS them, and containment is already carried by the CONTAINS
// edge. Emitting an inner call from every enclosing block would also inflate
// the callee's inbound count by the nesting depth of whichever test happened to
// exercise it — reintroducing at a new site the arbitrary style-dependent
// distortion this work exists to remove.
func (c *Chunker) emitTestBlockCallEdges(
	site *testBlockSite,
	src []byte,
	fileCtx ChunkContext,
	symbolName string,
	cqs *compiledQuerySet,
	slot int,
	ref *RefSite,
	result *Result,
) {
	if !site.leaf || symbolName == "" {
		return
	}
	edges := c.extractCallEdges(site.declNode, src, fileCtx.PackageName, symbolName, cqs)
	if len(edges) == 0 {
		return
	}
	for i := range edges {
		edges[i].Type = EdgeTestCalls
	}
	declRef := refForParent(ref, site.captures.ParentName)
	result.Edges = append(result.Edges, attachRefSite(edges, declRef, site.declNode, slot)...)
}

// markLeafTestBlocks sets leaf on every site that STRICTLY contains no other
// surviving site.
//
// Containment is decided on the ranges this pass already holds — no re-parse
// and no second query — and equal ranges do not nest: two query patterns
// matching one node would otherwise each disqualify the other and leave the
// block emitting nothing.
func markLeafTestBlocks(sites []testBlockSite) {
	for i := range sites {
		sites[i].leaf = true
		for j := range sites {
			if i == j {
				continue
			}
			if rangeStrictlyInside(sites[j].declRange, sites[i].declRange) {
				sites[i].leaf = false
				break
			}
		}
	}
}

// rangeStrictlyInside reports whether inner sits within outer AND is not the
// same extent.
func rangeStrictlyInside(inner, outer byteRange) bool {
	if inner.start < outer.start || inner.end > outer.end {
		return false
	}
	return inner.start != outer.start || inner.end != outer.end
}

// testBlockCoverRange returns the extent a test block contributes to
// coveredRanges, which is the OUTERMOST ancestor starting at the same byte as
// @decl — not @decl itself.
//
// collectOrphans (chunker_edges.go:229-235) tests a whole root-level child for
// `start >= r.start && end <= r.end`, and the node it tests is not the node the
// TestBlocks query captures: in JavaScript and TypeScript @decl binds the
// call_expression while the root child is the expression_statement wrapping it,
// which is one byte longer whenever the call is terminated with a semicolon.
// Measured on a TSX fixture: expression_statement [35,191) against test_block
// [35,190). Contributing the @decl extent would leave `end <= r.end` false and
// suppress nothing while looking exactly like a working suppression.
//
// The ascent is bounded by the same-start test, so it climbs only through
// wrappers that begin where the call begins — a statement terminator, an
// `await`, a parenthesization — and stops at the first ancestor that starts
// earlier. It never reaches the root, whose start is byte 0 for every file
// whose first byte is not the test block itself, and cannot when the file
// begins with the block because the loop takes the LAST same-start ancestor
// below the node whose parent is nil.
func testBlockCoverRange(declNode *sitter.Node) byteRange {
	cover := byteRange{start: declNode.StartByte(), end: declNode.EndByte()}
	for n := declNode; ; {
		parent := n.Parent()
		if parent == nil || parent.Parent() == nil || parent.StartByte() != declNode.StartByte() {
			return cover
		}
		cover.end = parent.EndByte()
		n = parent
	}
}

// testBlockRangeContains reports whether any surviving test block's @decl
// extent contains the given range — the identifiability rule for a declaration
// that leaked its call edges out of a test body.
func testBlockRangeContains(sites []testBlockSite, r byteRange) bool {
	for i := range sites {
		if r.start >= sites[i].declRange.start && r.end <= sites[i].declRange.end {
			return true
		}
	}
	return false
}
