// SPDX-License-Identifier: Apache-2.0

package ast

// where_flow.go is the ast package's ENTIRE dependency surface on the
// flow-step registry, and that single-site property is the point rather than a
// tidiness preference.
//
// The registry, the per-language arms and the step computation are owned by the
// flow-fact collection work; this package only READS them. Every other file in
// this package names the two declarations below and never a treesitter flow
// symbol, so if an upstream spelling changes the repair is one edit here rather
// than a sweep across the leaf, its evaluator and its tests.

import (
	"fmt"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// flowStep is the package-local alias for one syntactic observation an arm made
// inside a declaration — a name bound, operands read, a value occupying an
// argument or result position.
//
// It is an ALIAS rather than a wrapper struct deliberately: the steps carry live
// tree-sitter node pointers valid only while the owning parse is alive, so
// copying them into a local type would invite exactly the store-past-the-parse
// mistake the upstream contract forbids.
type flowStep = treesitter.FlowStep

// flowArmFor reports the flow-step arm registered for one language, and whether
// there is one at all.
//
// THE BOOLEAN IS THE LOAD-BEARING RETURN. "Armed" for the flows_to leaf's
// loud-error policy means exactly has an arm read through this accessor — the
// leaf refuses a language that reports false rather than walking it and
// returning an empty result, because an empty result is indistinguishable from
// "this code has no such flow" and that ambiguity is the failure the policy
// exists to remove.
func flowArmFor(lang treesitter.Language) (treesitter.FlowStepResolver, bool) {
	return treesitter.FlowStepsArm(lang)
}

// THE LEAF DECLARATION LIVES HERE RATHER THAN BESIDE ITS SIBLING LEAVES IN
// where.go, and the reason is shape rather than the line budget it also
// relieves: every other flows_to symbol is in this file, and where.go is the
// evaluator wiring. The doc comment below is still THE authoritative
// declaration of the field vocabulary — that is a property of the comment, not
// of the file it sits in.
// FlowsToLeaf asks whether a value REACHES another position inside one
// declaration: does the node bound to From flow to the node bound to To,
// within the declaration bound to Within.
//
// THIS DOC COMMENT IS THE AUTHORITATIVE DECLARATION of the leaf's three field
// names and their required-ness. The consumer-facing surfaces — the tool
// schema, the two description strings and the agent guidance — restate it, and
// must agree with it word for word on from/to/within.
//
// From and To are capture refs resolved through resolveCapture, so the
// `$outer.X` and `$outer.outer.X` chains work exactly as they do for same_node
// and same_text.
//
// WITHIN IS REQUIRED AND AN EMPTY WITHIN IS AN ERROR, NEVER A DEFAULT. Do not
// add a convenience fallback that derives the scope by walking for an enclosing
// declaration-ish ancestor. The reason is a scope boundary rather than a
// preference: the walk is intra-declaration and the flow arm takes a
// declaration node, but this package holds no per-language declaration-kind
// table, and inventing one here would duplicate the per-language registry the
// architecture deliberately placed in the collector. Callers already have two
// ways to name the scope with existing machinery — `$match` when the pattern
// itself matches the declaration, and inside_pattern's `as` binding for every
// other shape.
//
// FlowsToLeaf carries NO nested where, which is why validateKindLeaves needs no
// case for it: that walk descends only the composers and the two sub-pattern
// leaves' Where fields. A future revision that adds a nested where here MUST
// add a case there, or kind leaves buried under a flows_to would stop being
// validated.
type FlowsToLeaf struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Within string `json:"within"`
}

// flowArmedVocabulary names every language a flows_to leaf can ACTUALLY run on,
// sorted: those carrying a flow-step arm AND registered with this package.
//
// IT IS DERIVED, NEVER LISTED, and that is what keeps php out of it without a
// hardcoded exclusion. php carries a flow arm upstream but sits in this
// package's deniedLanguages over the placeholder-sigil collision, so it is
// absent from langRegistry and can never appear here. A hardcoded "except php"
// would be a second place to keep in sync, and it would go stale the moment the
// deny list or the armed set moved — the derived set stays honest by itself as
// more languages are armed.
//
// The enumeration walks THIS package's registry rather than the upstream one,
// which is why the leaf needs no enumeration accessor from the flow registry —
// only the per-language read accessor flowArmFor already wraps.
func flowArmedVocabulary() []string {
	langRegistryMu.RLock()
	out := make([]string, 0, len(langRegistry))
	for lang := range langRegistry {
		if _, armed := flowArmFor(lang); armed {
			out = append(out, string(lang))
		}
	}
	langRegistryMu.RUnlock()
	sort.Strings(out)
	return out
}

// errUnarmedFlowLanguage is the ONE constructor for the unarmed-language
// refusal, so the pre-walk validator and the leaf evaluator cannot drift into
// two different messages for one condition. Both enforce the same rule, at
// different times, for the reason the validator's doc records.
func errUnarmedFlowLanguage(lang treesitter.Language) error {
	return fmt.Errorf(
		"ast/where: flows_to is not available for language %q — it has no flow-step arm, "+
			"so a flow question cannot be answered for it at all. Flow-armed languages: %s",
		lang, strings.Join(flowArmedVocabulary(), ", "))
}

// errFlowLeafMissingField is the ONE constructor for a malformed flows_to leaf,
// shared by the validator and the evaluator for the same anti-drift reason.
func errFlowLeafMissingField(field string) error {
	return fmt.Errorf(
		"ast/where: flows_to leaf is missing %q — from, to and within are all required; "+
			"within names the declaration the walk is scoped to (use $match when the pattern "+
			"matches the declaration, or an inside_pattern `as` binding otherwise)", field)
}

// ValidateWhereFlowArms rejects, BEFORE the walk starts, a flows_to leaf that
// can never be answered: one on a language with no flow-step arm, and one
// missing any of its three required fields.
//
// WHY PRE-WALK RATHER THAN ONLY IN THE EVALUATOR, which is the whole point of
// this function. A check living only in the leaf evaluator fires only when some
// node matches the pattern. So a flows_to leaf on an unarmed language, run over
// a corpus the pattern happens to miss, would return a clean zero and no error
// — byte-identical to the zero a correct search that genuinely found nothing
// returns. The caller cannot tell "no such flow" from "this language cannot
// answer flow questions", and that is exactly the silent-zero class
// where_kind_validate.go was built to remove.
//
// It is deliberately a TWO-POINT rule: the evaluator repeats both checks,
// because ast.Match is callable directly without going through the tool
// handlers, and a caller on that path deserves the same refusal.
//
// Structural sibling of ValidateWhereKinds, including its nil semantics: a nil
// tree is no filter and returns nil.
func ValidateWhereFlowArms(where *WhereNode, lang treesitter.Language) error {
	if where == nil {
		return nil
	}
	if where.FlowsTo != nil {
		if err := validateFlowLeaf(where.FlowsTo, lang); err != nil {
			return err
		}
	}
	for _, child := range where.All {
		if err := ValidateWhereFlowArms(child, lang); err != nil {
			return err
		}
	}
	for _, child := range where.Any {
		if err := ValidateWhereFlowArms(child, lang); err != nil {
			return err
		}
	}
	if err := ValidateWhereFlowArms(where.Not, lang); err != nil {
		return err
	}
	// A flows_to nested inside a sub-pattern's where is as undecidable during
	// the walk as one at the top, so the recursion follows those too.
	for _, sub := range []*SubPatternLeaf{where.InsidePattern, where.ContainsPattern} {
		if sub == nil {
			continue
		}
		if err := ValidateWhereFlowArms(sub.Where, lang); err != nil {
			return err
		}
	}
	return nil
}

// validateFlowLeaf checks one leaf: every required field present, and the
// language armed.
//
// THE FIELD CHECKS COME FIRST AND COST NOTHING EXTRA — the leaf is already in
// hand at the point the arm is probed. They belong here for this function's own
// rationale applied to a second input class: a leaf missing `within` is
// malformed regardless of corpus, so leaving it to the evaluator would let a
// malformed leaf over a pattern that matches nothing return a clean zero.
func validateFlowLeaf(leaf *FlowsToLeaf, lang treesitter.Language) error {
	for _, req := range []struct {
		name  string
		value string
	}{
		{"from", leaf.From},
		{"to", leaf.To},
		{"within", leaf.Within},
	} {
		if strings.TrimSpace(req.value) == "" {
			return errFlowLeafMissingField(req.name)
		}
	}
	if _, armed := flowArmFor(lang); !armed {
		return errUnarmedFlowLanguage(lang)
	}
	return nil
}

// evalFlowsTo answers the leaf: does the value bound to From reach the position
// bound to To, inside the declaration bound to Within.
//
// EVERY FAILURE MODE HERE IS AN ERROR RATHER THAN A FALSE, and that is the
// leaf's whole point. A false says "this code has no such flow"; an unarmed
// language, a malformed leaf and a mis-scoped capture say "this question could
// not be asked". Collapsing them would put the caller back where the ticket
// found them — unable to tell an answer from an inability to answer.
//
// It is the SECOND enforcement point of two two-point rules (required fields,
// armed language), not a redundant copy of the pre-walk validator: ast.Match is
// callable directly as a library, so a caller who never goes through the tool
// handlers must still get the refusal.
func evalFlowsTo(l *FlowsToLeaf, scope *evalScope) (bool, error) {
	// (1) Required fields. resolveCapture("") errors on its own, so omitting
	// this would degrade the MESSAGE rather than the answer — which is exactly
	// why it is here: "leaf is missing within" is actionable, "capture
	// unresolved" sends the caller looking for a capture they never wrote.
	for _, req := range []struct{ name, value string }{
		{"from", l.From}, {"to", l.To}, {"within", l.Within},
	} {
		if strings.TrimSpace(req.value) == "" {
			return false, errFlowLeafMissingField(req.name)
		}
	}

	// (2) Resolve all three. An unresolved ref is propagated, never converted
	// into a no-match.
	fromCap, fromNode, err := resolveCapture(scope, l.From)
	if err != nil {
		return false, err
	}
	toCap, toNode, err := resolveCapture(scope, l.To)
	if err != nil {
		return false, err
	}
	withinCap, withinNode, err := resolveCapture(scope, l.Within)
	if err != nil {
		return false, err
	}

	// (3) The armed check, repeated from the pre-walk validator for the
	// library-path reason above.
	arm, armed := flowArmFor(scope.lang)
	if !armed {
		return false, errUnarmedFlowLanguage(scope.lang)
	}

	// (4) Both endpoints must lie inside the named scope. Returning false here
	// would be indistinguishable from an honest "no flow", when the truth is
	// that the caller named the wrong declaration.
	if !spanWithin(withinCap, fromCap) {
		return false, errFlowEndpointOutsideWithin("from", l.From, l.Within)
	}
	if !spanWithin(withinCap, toCap) {
		return false, errFlowEndpointOutsideWithin("to", l.To, l.Within)
	}
	if withinNode == nil {
		return false, fmt.Errorf(
			"ast/where: flows_to `within` (%q) is not bound to a single node, so there is no "+
				"declaration to scope the walk to — name the declaration itself", l.Within)
	}

	// (5) One arm call for this declaration, memoized per scope node.
	steps := scope.flowStepsFor(withinNode, arm)
	return flowReaches(steps, scope.src, fromCap, fromNode, toCap, toNode), nil
}

// errFlowEndpointOutsideWithin names WHICH endpoint escaped the scope, because
// "outside within" without the side is a bug report the caller cannot act on.
func errFlowEndpointOutsideWithin(side, ref, within string) error {
	return fmt.Errorf(
		"ast/where: flows_to %s capture %q lies OUTSIDE the declaration named by within (%q) — "+
			"the walk is intra-declaration, so this leaf can never be true as written; "+
			"name the declaration that actually encloses both endpoints", side, ref, within)
}

// spanWithin reports whether inner's byte span lies inside outer's.
func spanWithin(outer, inner Capture) bool {
	return inner.StartByte >= outer.StartByte && inner.EndByte <= outer.EndByte
}

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
// THE BOUND IS STRUCTURAL, not a ceiling constant: the visited set is keyed by
// endpoint span, each endpoint is enqueued at most once, and the step set for
// one declaration is finite — so the walk is O(V+E) and terminates on a cyclic
// step set with no truncation signal to report, because nothing accumulates
// without bound.
func flowReaches(
	steps []flowStep, src []byte,
	fromCap Capture, fromNode *sitter.Node,
	toCap Capture, toNode *sitter.Node,
) bool {
	if len(steps) == 0 {
		return false
	}
	type spanKey struct{ start, end uint32 }
	key := func(n *sitter.Node) spanKey { return spanKey{n.StartByte(), n.EndByte()} }

	// Index every endpoint once: its node, and the adjacency the two edge
	// classes imply.
	byText := map[string][]*sitter.Node{}
	adj := map[spanKey][]*sitter.Node{}
	var all []*sitter.Node
	note := func(n *sitter.Node) {
		if n == nil {
			return
		}
		all = append(all, n)
	}
	for _, st := range steps {
		note(st.Target)
		for _, s := range st.Sources {
			note(s)
		}
	}
	for _, n := range all {
		t := n.Content(src)
		byText[t] = append(byText[t], n)
	}
	for _, st := range steps {
		if st.Target == nil {
			continue
		}
		for _, s := range st.Sources {
			if s != nil {
				adj[key(s)] = append(adj[key(s)], st.Target)
			}
		}
	}

	// Seed from every endpoint the From capture binds.
	var queue []*sitter.Node
	seen := map[spanKey]bool{}
	for _, n := range all {
		if endpointBinds(fromCap, fromNode, n) && !seen[key(n)] {
			seen[key(n)] = true
			queue = append(queue, n)
		}
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if endpointBinds(toCap, toNode, cur) {
			return true
		}
		next := adj[key(cur)]
		// Edge class (2): same-binding occurrences elsewhere in this declaration.
		next = append(next, byText[cur.Content(src)]...)
		for _, n := range next {
			if n == nil || seen[key(n)] {
				continue
			}
			seen[key(n)] = true
			queue = append(queue, n)
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
