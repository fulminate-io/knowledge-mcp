// SPDX-License-Identifier: Apache-2.0

// where_flow_reach.go — the bounded intra-declaration reachability walk behind
// the flows_to leaf, plus the endpoint-identity and memo helpers it needs.
// where_flow.go keeps the leaf declaration, the two pre-walk validators and the
// evaluator that calls into this file.
//
// THIS FILE IS NOT A ZERO-BEHAVIOR SPLIT, and reading it as one would skip the
// change it exists to carry. It is a split PLUS the value-scoped seeding. The
// three groups below are named per declaration, and every claim was checked by
// extracting each function body from `git show <base>:where_flow.go` and from
// this file and comparing them, rather than by recalling what was edited:
//
//	MOVED, byte-identical to their bodies in where_flow.go:
//	  endpointBinds, evalScope.flowStepsFor
//
//	REWRITTEN, and this is the function that carries the change:
//	  flowReaches — its signature dropped the now-unused fromNode parameter;
//	  its inline endpoint indexing and its inline seeding loop moved out to
//	  indexFlowGraph and seedFlowQueue; it gained an enqueue closure; and it
//	  gained an answer path the base has nowhere, the `satisfied` early
//	  return, which reports a reach WITHOUT traversing for the case a
//	  composite `from` already lies inside the destination and so can never be
//	  dequeued. Its own doc comment, immediately above it, carries the
//	  mechanism.
//
//	NEW, and together they ARE the value scoping:
//	  valueSpan, fromValueSpans   — what the `from` capture names as values
//	  flowSpanKey, flowKey        — endpoint identity by span
//	  indexFlowGraph, flowGraph   — the two edge classes, indexed once
//	  spanConsumers               — a composite value's outgoing edges
//	  seedFlowQueue               — the seeding rule itself
//
// The split was forced by the file-size threshold; the seeding was forced by a
// measurement.

package ast

import (
	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// endpointBinds reports whether one flow-step endpoint node belongs to a
// capture, using the package's own identity rule: spans equal, OR the endpoint
// contained within the capture.
//
// THE CONTAINMENT HALF IS LOAD-BEARING AND WAS CONFIRMED AGAINST THE LANDED
// ARM, not assumed. The Go reference arm reports endpoints at IDENTIFIER
// granularity — a parameter's name node, an assignment's target — which is
// finer than a whole expression, so a sequence capture like $$$ARGS
// legitimately spans several endpoints and an equality-only rule would bind
// none of them.
func endpointBinds(cap Capture, capNode, endpoint *sitter.Node) bool {
	if endpoint == nil {
		return false
	}
	s, e := endpoint.StartByte(), endpoint.EndByte()
	if capNode != nil && len(cap.Children) == 0 && capNode.StartByte() == s && capNode.EndByte() == e {
		return true
	}
	return s >= cap.StartByte && e <= cap.EndByte
}

// valueSpan is one byte range the `from` capture names as a VALUE in its own
// right, in the flow graph's own coordinate space.
type valueSpan struct{ start, end uint32 }

// fromValueSpans splits a `from` capture into the values it names.
//
// A SEQUENCE CAPTURE NAMES ONE VALUE PER ELEMENT, not one value spanning the
// list: `$$$ARGS` over `f(a, b)` asks whether a OR b reaches the destination,
// and each element is a complete value the caller wrote. Its children carry
// their own spans, so the split is a read rather than a re-parse.
//
// EVERY OTHER CAPTURE NAMES EXACTLY ONE VALUE: its own span. Whether that span
// happens to coincide with a flow endpoint is what flowReaches decides next —
// a bare identifier does, a call or arithmetic expression does not, and the two
// are seeded differently for the reason flowReaches documents.
func fromValueSpans(cap Capture) []valueSpan {
	if len(cap.Children) == 0 {
		return []valueSpan{{cap.StartByte, cap.EndByte}}
	}
	out := make([]valueSpan, 0, len(cap.Children))
	for _, c := range cap.Children {
		out = append(out, valueSpan{c.StartByte, c.EndByte})
	}
	return out
}

// flowSpanKey identifies one endpoint by its byte span. Endpoints are compared
// and deduped by span rather than by pointer because the same source position
// is reported by several steps as distinct wrapper values.
type flowSpanKey struct{ start, end uint32 }

func flowKey(n *sitter.Node) flowSpanKey { return flowSpanKey{n.StartByte(), n.EndByte()} }

// flowGraph is the indexed form of one declaration's steps: every endpoint
// once, the same-text buckets edge class (2) reads, and the declared adjacency
// edge class (1) reads.
type flowGraph struct {
	all    []*sitter.Node
	byText map[string][]*sitter.Node
	adj    map[flowSpanKey][]*sitter.Node
}

// indexFlowGraph builds the graph both edge classes walk. It is separated from
// the traversal so the traversal reads as the two-class walk its doc comment
// describes rather than as an index build with a walk at the end.
func indexFlowGraph(steps []flowStep, src []byte) flowGraph {
	g := flowGraph{byText: map[string][]*sitter.Node{}, adj: map[flowSpanKey][]*sitter.Node{}}
	note := func(n *sitter.Node) {
		if n != nil {
			g.all = append(g.all, n)
		}
	}
	for _, st := range steps {
		note(st.Target)
		for _, s := range st.Sources {
			note(s)
		}
	}
	for _, n := range g.all {
		g.byText[n.Content(src)] = append(g.byText[n.Content(src)], n)
	}
	for _, st := range steps {
		if st.Target == nil {
			continue
		}
		for _, s := range st.Sources {
			if s != nil {
				g.adj[flowKey(s)] = append(g.adj[flowKey(s)], st.Target)
			}
		}
	}
	return g
}

// spanConsumers returns the Targets of every declared step that consumed an
// operand lying inside span — the outgoing edges of a COMPOSITE value, which is
// not itself an endpoint and therefore has none of its own.
//
// THIS IS THE VALUE-SCOPING RULE IN ONE FUNCTION. It deliberately does NOT
// consult the same-text buckets: the operands inside a composite expression are
// reads that PRODUCED the value, not other occurrences OF it, and closing over
// them is what made every accessor on one receiver interchangeable.
func spanConsumers(steps []flowStep, span valueSpan) []*sitter.Node {
	var out []*sitter.Node
	for _, st := range steps {
		if st.Target == nil {
			continue
		}
		for _, s := range st.Sources {
			if s != nil && s.StartByte() >= span.start && s.EndByte() <= span.end {
				out = append(out, st.Target)
				break
			}
		}
	}
	return out
}

// seedFlowQueue returns the endpoints the walk starts from, and reports whether
// the From capture ALREADY satisfies the destination — the from-equals-to case,
// which a composite value cannot answer by traversal because it is not an
// endpoint and can never be dequeued.
func seedFlowQueue(g flowGraph, steps []flowStep, fromCap, toCap Capture) (queue []*sitter.Node, satisfied bool) {
	for _, v := range fromValueSpans(fromCap) {
		bound := false
		for _, n := range g.all {
			if n.StartByte() == v.start && n.EndByte() == v.end {
				queue = append(queue, n)
				bound = true
			}
		}
		if bound {
			continue
		}
		if v.start >= toCap.StartByte && v.end <= toCap.EndByte {
			return nil, true
		}
		queue = append(queue, spanConsumers(steps, v)...)
	}
	return queue, false
}

// flowReaches is the bounded intra-declaration reachability walk: breadth-first
// from every endpoint bound to From, returning true the moment any endpoint
// bound to To is reached.
//
// THE GRAPH HAS TWO EDGE CLASSES, and the second is a design decision this
// function owns rather than one the arm supplies. Stating it plainly because a
// reader will otherwise assume the steps alone define the graph:
//
//  1. DECLARED STEPS. Every step with a Target contributes Sources -> Target,
//     the direction the arm declared. This is the arm's fact.
//  2. BINDING OCCURRENCES. Two endpoints with the same identifier TEXT inside
//     one declaration are the same local binding, so they are linked. Without
//     this the leaf could not answer its own headline question: a parameter is
//     an endpoint at the signature and its use is a DIFFERENT node at the call
//     site, and no step joins them — the arm reports grammar shape and leaves
//     alias closure to its consumer, which here is this function.
//
// THE LIMIT OF (2), stated rather than discovered later: it does not model
// SHADOWING. A declaration that rebinds a name in an inner block is treated as
// one binding, so this can report a flow that a shadow-aware analysis would
// deny. That is the conservative direction for a search filter — it over-reports
// rather than hiding a real flow — but it is a real limit and a shadow-aware
// closure belongs with the closure engine, not in a where-leaf.
//
// THE SEED IS VALUE-SCOPED, AND THAT IS WHAT MAKES THE LEAF ANSWER THE QUESTION
// ITS CALLER ASKED. `from` names a VALUE. When that value is a composite
// expression — `spec.GetCommand()`, `a + b`, a call whose result is what the
// caller means — the endpoints INSIDE it are operand reads, not the value.
// Seeding them directly and then applying edge class (2) to them makes the leaf
// answer a different and much weaker question: `spec.GetCommand()` and
// `spec.GetEnv()` share the operand `spec`, so every value derived from ANY
// method on that receiver becomes reachable from every other, and a `from` that
// demonstrably ends up in an error string reports as reaching a spawn.
// Measured on this repository before the fix: a leaf asking whether
// `spec.GetEnv()` reaches a transport's Command field answered YES, through the
// single shared identifier.
//
// So a composite `from` is seeded by seedFlowQueue instead: the DECLARED steps that
// consume one of its operands, which is the arm's own statement about where the
// expression's value went, with no same-text expansion at the seed. From those
// targets outward both edge classes apply unchanged, because a step's Target IS
// a binding occurrence and class (2) is exactly right for one. A `from` that
// binds an endpoint directly — a bare identifier capture, or each element of a
// sequence capture — is seeded as it always was, so the ordinary shapes are
// untouched.
//
// THE BOUND IS STRUCTURAL, not a ceiling constant: the visited set is keyed by
// endpoint span, each endpoint is enqueued at most once, and the step set for
// one declaration is finite — so the walk is O(V+E) and terminates on a cyclic
// step set with no truncation signal to report, because nothing accumulates
// without bound.
func flowReaches(
	steps []flowStep, src []byte,
	fromCap Capture,
	toCap Capture, toNode *sitter.Node,
) bool {
	if len(steps) == 0 {
		return false
	}
	g := indexFlowGraph(steps, src)

	seeds, satisfied := seedFlowQueue(g, steps, fromCap, toCap)
	if satisfied {
		return true
	}

	var queue []*sitter.Node
	seen := map[flowSpanKey]bool{}
	enqueue := func(n *sitter.Node) {
		if n == nil || seen[flowKey(n)] {
			return
		}
		seen[flowKey(n)] = true
		queue = append(queue, n)
	}
	for _, n := range seeds {
		enqueue(n)
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if endpointBinds(toCap, toNode, cur) {
			return true
		}
		// Edge class (1) then edge class (2): the arm's declared direction, then
		// the same-binding occurrences elsewhere in this declaration.
		for _, n := range g.adj[flowKey(cur)] {
			enqueue(n)
		}
		for _, n := range g.byText[cur.Content(src)] {
			enqueue(n)
		}
	}
	return false
}

// flowStepsFor returns the flow steps for one declaration, calling the arm at
// most ONCE per declaration node per match worker.
//
// WITHOUT THE MEMO the cost is quadratic in the wrong variable: a pattern
// matching N sites inside one function would re-walk that whole declaration
// subtree N times, because each match is evaluated in its own scope. The memo
// is keyed by the declaration's byte span rather than by pointer identity, so
// it survives the scope chain the evaluator builds per match.
//
// A NIL RESULT IS A REAL ANSWER AND IS CACHED AS ONE. An arm returns nil for a
// declaration that shows no flow at all, and re-asking would re-walk the
// subtree every time to learn the same nothing — so presence in the map, not
// non-emptiness of the value, is what decides a hit.
func (s *evalScope) flowStepsFor(decl *sitter.Node, arm treesitter.FlowStepResolver) []flowStep {
	if decl == nil || arm == nil {
		return nil
	}
	if s.flowSteps == nil {
		// A scope built without a memo still works; it just pays per call.
		return arm(decl, s.src)
	}
	k := [2]uint32{decl.StartByte(), decl.EndByte()}
	if steps, ok := s.flowSteps[k]; ok {
		return steps
	}
	steps := arm(decl, s.src)
	s.flowSteps[k] = steps
	return steps
}
