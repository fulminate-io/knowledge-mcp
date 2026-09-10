// SPDX-License-Identifier: Apache-2.0

// where_capture_validate.go — compile-time validation of every capture
// REFERENCE in a where-tree against the names the pattern and the tree's own
// sub-pattern leaves actually declare.
//
// WHY THIS EXISTS, and it is a silence rather than a wrong answer. Capture
// references resolve inside evalWhere, which runs only once some node matches
// the outer pattern. A where-tree naming a capture nobody declares therefore
// errors LOUDLY over a corpus the pattern happens to hit and returns a CLEAN
// ZERO over one it misses — byte-identical to the zero a correct search that
// found nothing returns. On a stored corpus check, whose whole job is to report
// an absence, that zero is the answer the caller reads, and the run walks the
// whole tree to produce it.
//
// It is the third member of the pre-walk family, beside ValidateWhereKinds (a
// kind the grammar lacks) and ValidateWhereFlowArms (a flow question the
// language cannot answer). Same placement, same reason, same nil semantics.
//
// WHAT IT DOES NOT DECIDE: ORDER. The evaluator binds an `as` handle when its
// leaf runs, so a reference to a handle declared by a LATER sibling is
// unresolved at run time even though the name is declared somewhere in the
// scope. This validator refuses NAMES NOBODY DECLARES; a name declared out of
// order stays the evaluator's error, with its own message. Refusing on order
// here would mean re-deciding evaluation order in a second place, and the two
// would drift.

package ast

import (
	"fmt"
	"sort"
	"strings"
)

// matchCaptureName is the implicit binding matchTreeWithNodes injects for the
// outermost matched node of every match, outer and sub-pattern alike. It is
// declared in every scope and needs no placeholder.
const matchCaptureName = "$match"

// outerRefPrefix is the one-level scope-walk prefix a capture reference uses to
// reach the enclosing scope. resolveCapture strips it one occurrence at a time;
// so does this validator, against the same chain.
const outerRefPrefix = "$outer."

// errCaptureUndeclared is the sentinel for a reference no pattern and no `as`
// declaration in the tree can ever bind. It is a sibling of
// errCaptureUnresolved (the run-time form) rather than the same error, because
// the two report different facts: this one says the name exists nowhere in the
// call, that one says it was not bound at the moment it was read.
var errCaptureUndeclared = fmt.Errorf("ast/where: capture reference names a capture nothing declares")

// captureScope is one level of the declared-name chain, mirroring the
// evalScope chain resolveCapture walks at run time. Root holds the outer
// pattern's placeholder names; a sub-pattern leaf's nested where gets a child
// whose parent is the level that hosts the leaf.
type captureScope struct {
	declared map[string]struct{}
	parent   *captureScope
}

func newCaptureScope(parent *captureScope) *captureScope {
	return &captureScope{declared: map[string]struct{}{matchCaptureName: {}}, parent: parent}
}

// resolves reports whether ref names something this chain declares, applying
// the same `$outer.` walk resolveCapture applies.
func (s *captureScope) resolves(ref string) bool {
	cur := s
	name := ref
	for strings.HasPrefix(name, outerRefPrefix) {
		name = name[len(outerRefPrefix):]
		if cur == nil {
			return false
		}
		cur = cur.parent
	}
	if cur == nil {
		return false
	}
	_, ok := cur.declared[name]
	return ok
}

// vocabulary renders every name the chain can resolve, innermost first and each
// level spelled with the `$outer.` depth a caller would have to write. The
// message is the discovery surface for the namespaced export, so it must show
// the composite keys rather than only the bare handles.
func (s *captureScope) vocabulary() []string {
	var out []string
	prefix := ""
	for cur := s; cur != nil; cur = cur.parent {
		level := make([]string, 0, len(cur.declared))
		for name := range cur.declared {
			level = append(level, prefix+name)
		}
		sort.Strings(level)
		out = append(out, level...)
		prefix += outerRefPrefix
	}
	return out
}

// ValidateWhereCaptureRefs rejects, BEFORE the walk starts, any capture
// reference in the where-tree that no pattern placeholder and no sub-pattern
// leaf's `as` declaration can bind.
//
// pats is the pattern set the where-tree will be evaluated against — one entry
// for the singular `pattern` form, one per member for a `patterns[]`
// alternation. EVERY member is validated, because the same where-tree is
// evaluated against every member's matches, so a reference undeclared in one
// member is a run-time error the moment that member matches. Validating only
// the first would leave exactly the silence this function removes.
//
// A nil tree is no filter and returns nil, as ValidateWhereKinds does.
func ValidateWhereCaptureRefs(where *WhereNode, pats ...Pattern) error {
	if where == nil {
		return nil
	}
	for _, pat := range pats {
		root := newCaptureScope(nil)
		for _, ph := range pat.Placeholders {
			if ph.Name != "" {
				root.declared[ph.Name] = struct{}{}
			}
		}
		if err := validateCaptureRefs(where, root); err != nil {
			return err
		}
	}
	return nil
}

// validateCaptureRefs walks one scope level: declare every `as` handle the
// level's leaves carry, then check every reference at that level, then descend
// into each sub-pattern leaf's nested where with a child scope.
//
// DECLARATION IS A PRE-PASS OVER THE WHOLE LEVEL, not a running accumulation,
// for the ordering reason in this file's header: order is the evaluator's to
// enforce and this function's job is the vocabulary.
func validateCaptureRefs(where *WhereNode, scope *captureScope) error {
	declareHandles(where, scope)
	if err := checkRefsAtLevel(where, scope); err != nil {
		return err
	}
	return descendSubPatterns(where, scope)
}

// declareHandles adds every sub-pattern leaf's `as` handle at this level —
// plus the composite keys that leaf's own pattern exports under it — to the
// scope. It descends the composers but NOT a sub-pattern's nested where, whose
// handles belong to the child scope.
func declareHandles(where *WhereNode, scope *captureScope) {
	if where == nil {
		return
	}
	for _, child := range where.All {
		declareHandles(child, scope)
	}
	for _, child := range where.Any {
		declareHandles(child, scope)
	}
	declareHandles(where.Not, scope)
	for _, sub := range []*SubPatternLeaf{where.InsidePattern, where.ContainsPattern} {
		if sub == nil || sub.As == "" {
			continue
		}
		scope.declared[sub.As] = struct{}{}
		// The namespaced export: every capture the leaf's own pattern binds,
		// plus the implicit $match, is reachable as `<as>.<name>`. A
		// sub-pattern whose source does not parse declares only the handle —
		// the parse failure is compilePattern's error to report, with its own
		// message, and duplicating it here would give the caller two.
		scope.declared[sub.As+subPatternNamespaceSep+matchCaptureName] = struct{}{}
		pat, err := Parse(sub.Pattern)
		if err != nil {
			continue
		}
		for _, ph := range pat.Placeholders {
			if ph.Name != "" {
				scope.declared[sub.As+subPatternNamespaceSep+ph.Name] = struct{}{}
			}
		}
	}
}

// checkRefsAtLevel validates every capture reference the leaves at this level
// carry. It descends the composers only; a nested where's refs are checked
// against the child scope in descendSubPatterns.
func checkRefsAtLevel(where *WhereNode, scope *captureScope) error {
	if where == nil {
		return nil
	}
	for _, child := range where.All {
		if err := checkRefsAtLevel(child, scope); err != nil {
			return err
		}
	}
	for _, child := range where.Any {
		if err := checkRefsAtLevel(child, scope); err != nil {
			return err
		}
	}
	if err := checkRefsAtLevel(where.Not, scope); err != nil {
		return err
	}
	for _, r := range refsOf(where) {
		if err := checkRef(r, scope); err != nil {
			return err
		}
	}
	return nil
}

// captureRef is one reference and the leaf field it came from, so a refusal can
// name where the caller wrote it.
type captureRef struct {
	leaf  string
	field string
	ref   string
}

// refsOf enumerates every capture reference the leaves ON THIS NODE carry.
//
// IT IS EXHAUSTIVE OVER THE EIGHT LEAVES BY CONSTRUCTION, and a ninth leaf that
// carries a capture ref and is not added here reintroduces exactly the silence
// this file removes. The nested `where` of a sub-pattern leaf is deliberately
// absent: those refs resolve in the CHILD scope and are checked there.
func refsOf(w *WhereNode) []captureRef {
	var out []captureRef
	if w.Kind != nil {
		out = append(out, captureRef{"kind", "of", w.Kind.Of})
	}
	if w.Matches != nil {
		out = append(out, captureRef{"matches", "of", w.Matches.Of})
	}
	if w.Equals != nil {
		out = append(out, captureRef{"equals", "of", w.Equals.Of})
	}
	if w.SameNode != nil {
		for _, c := range w.SameNode.Captures {
			out = append(out, captureRef{"same_node", "captures", c})
		}
	}
	if w.SameText != nil {
		for _, c := range w.SameText.Captures {
			out = append(out, captureRef{"same_text", "captures", c})
		}
	}
	if w.InsidePattern != nil {
		out = append(out, captureRef{"inside_pattern", "of", w.InsidePattern.Of})
	}
	if w.ContainsPattern != nil {
		out = append(out, captureRef{"contains_pattern", "of", w.ContainsPattern.Of})
	}
	if w.FlowsTo != nil {
		out = append(out,
			captureRef{"flows_to", "from", w.FlowsTo.From},
			captureRef{"flows_to", "to", w.FlowsTo.To},
			captureRef{"flows_to", "within", w.FlowsTo.Within},
		)
	}
	return out
}

// checkRef refuses one reference the chain cannot resolve.
//
// AN EMPTY REFERENCE IS REFUSED TOO, and it is refused HERE as well as by the
// leaf-specific validators that reach some of the fields, because an omitted
// `of` is unresolvable for exactly the same reason a misspelled one is: it
// names nothing. The leaf-specific message is better where one exists, which is
// why ValidateWhereFlowArms runs first at every call site that has both.
func checkRef(r captureRef, scope *captureScope) error {
	if strings.TrimSpace(r.ref) == "" {
		return fmt.Errorf(
			"ast/where: %s leaf has an empty %q — every capture reference must name a capture. Declared here: %s",
			r.leaf, r.field, strings.Join(scope.vocabulary(), ", "))
	}
	if scope.resolves(r.ref) {
		return nil
	}
	return fmt.Errorf(
		"%w: %s leaf's %q names %q, which no pattern placeholder and no sub-pattern `as` declaration binds. "+
			"A sub-pattern leaf's own captures are exported under its `as` name as \"<as>.<capture>\", and a leaf "+
			"with no `as` exports nothing. Declared here: %s",
		errCaptureUndeclared, r.leaf, r.field, r.ref, strings.Join(scope.vocabulary(), ", "))
}

// descendSubPatterns validates each sub-pattern leaf's nested where against a
// CHILD scope holding that sub-pattern's own placeholders, whose parent is the
// level hosting the leaf — the same chain pushSubPattern builds at run time, so
// `$outer.` resolves identically here and there.
func descendSubPatterns(where *WhereNode, scope *captureScope) error {
	if where == nil {
		return nil
	}
	for _, child := range where.All {
		if err := descendSubPatterns(child, scope); err != nil {
			return err
		}
	}
	for _, child := range where.Any {
		if err := descendSubPatterns(child, scope); err != nil {
			return err
		}
	}
	if err := descendSubPatterns(where.Not, scope); err != nil {
		return err
	}
	for _, sub := range []*SubPatternLeaf{where.InsidePattern, where.ContainsPattern} {
		if sub == nil || sub.Where == nil {
			continue
		}
		child := newCaptureScope(scope)
		pat, err := Parse(sub.Pattern)
		if err != nil {
			// Unparseable sub-pattern: compilePattern reports it with its own
			// message. Validating its nested where against an empty vocabulary
			// would bury that error under a misleading second one.
			continue
		}
		for _, ph := range pat.Placeholders {
			if ph.Name != "" {
				child.declared[ph.Name] = struct{}{}
			}
		}
		if err := validateCaptureRefs(sub.Where, child); err != nil {
			return err
		}
	}
	return nil
}
