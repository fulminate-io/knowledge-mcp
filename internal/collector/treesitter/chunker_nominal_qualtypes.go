// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// The roles a declaration's descendant can play in the nominal qualifier walk.
//
// ROLE ZERO IS DELIBERATELY UNNAMED AND UNASSIGNABLE. It is the role of every
// class code a language's table does not name, which is what makes an
// unclassified node behave like a node no rule matches rather than like a wrong
// one — and the counting starts at one so no spec can assign that meaning to a
// kind on purpose.
const (
	// nominalRoleName — a node whose Content is the NAME a qualifier is bound
	// under. It is the spelling a reference inside the declaration would use,
	// so PHP's variable_name carries its "$" and nothing normalizes it away.
	nominalRoleName uint8 = iota + 1
	// nominalRoleType — a node the language's renderer may turn into type text.
	// It is assigned ONLY by the languages whose bind sites put the type AFTER
	// the name, because those are the ones that locate the type by class rather
	// than by position.
	nominalRoleType
	// nominalRoleDeclarator — a wrapper holding the name one level down, so
	// `Store a, b;` binds BOTH names to one type rather than only the first.
	nominalRoleDeclarator
	// nominalRoleIgnored — a leading child that is neither a name nor a type:
	// a modifier list, a visibility keyword, a val/var binding marker. It is
	// skipped BEFORE the positional rule runs, so a modifier cannot be mistaken
	// for the type in the languages that locate the type by position.
	nominalRoleIgnored
	// nominalRoleScopeBreak — a nested declaration with a scope of its own. The
	// walk neither binds inside it nor descends into it: a nested class's
	// fields and a method's parameters are not the enclosing declaration's
	// locals, and each is walked again under its own declaration chunk.
	nominalRoleScopeBreak
)

// The bind-site kinds. A bind site is a node that introduces one or more
// name-to-type bindings; the two kinds differ only in where the type sits.
const (
	nominalSiteNone uint8 = iota
	// nominalSiteTypeFirst — the TYPE precedes the NAME (java, csharp, php,
	// groovy). The type is the FIRST non-ignored named child, taken
	// POSITIONALLY, because in csharp and groovy the type and the name are both
	// plain identifiers and no class distinguishes them.
	nominalSiteTypeFirst
	// nominalSiteNameFirst — the NAME precedes the TYPE (kotlin, scala). The
	// type is the first child carrying nominalRoleType, located by CLASS rather
	// than by position, because these sites may carry a trailing initializer
	// expression that a last-child rule would read as the type.
	nominalSiteNameFirst
)

// nominalSpec is everything the shared walk needs that differs per grammar.
//
// EVERY MEMBERSHIP FIELD IS INDEXED BY THAT LANGUAGE'S OWN CLASS CODE, never by
// a kind NAME. Reading a node's kind name converts a cgo C string into a fresh
// Go string on every call, so a recursive walk that named every node at every
// depth would allocate once per node visited; the symbol is a scalar the
// binding already holds, and the class table turns it into one bounds-checked
// array index.
type nominalSpec struct {
	// kinds is the language's symbol class table, from its <l>Kinds().
	kinds symbolClasses

	// selfToken is the spelling a member reference uses for the enclosing
	// instance — "this" in java, kotlin, scala, csharp and groovy, "$this" in
	// php. Bound to the enclosing class-like container's NAME, which is what
	// lets `this.f.go()` reach the field hop.
	selfToken string

	// roles classifies a node by its language's class code. Indexed directly,
	// so a class code the language's table does not name reads role zero and
	// matches no rule.
	roles [256]uint8

	// sites marks the class codes that introduce bindings, and with which of
	// the two layouts.
	sites [256]uint8

	// containers marks the class-like declaration kinds the self ascent stops
	// at. It is SEPARATE from nominalRoleScopeBreak, which also covers methods:
	// ascending to a method and reading its name would bind the self token to
	// the method rather than to the type that declares it.
	containers [256]bool

	// selfSuppressors are ANONYMOUS keyword spellings that make the self token
	// UNAVAILABLE inside the declaration carrying them — `static` in every
	// language of this group that has static members.
	//
	// A STATIC DECLARATION HAS NO ENCLOSING INSTANCE, so binding the self token
	// there would state that `this.f` names something the language forbids
	// writing. Each spelling is looked for on the declaration itself and on its
	// modifier children, because the grammars disagree about whether a modifier
	// is a direct keyword or a wrapper node holding one.
	selfSuppressors []string

	// untypedMarkers are ANONYMOUS keyword spellings that mark a bind site as
	// declaring no type at all. A site carrying one declines whole.
	//
	// IT EXISTS FOR A MEASURED MIS-BIND, not for tidiness. Groovy spells an
	// untyped local `def z = other`, whose named children are the NAME and the
	// initializer with no type node between them — so the positional rule would
	// read the name as the type and bind the initializer's spelling to it. The
	// languages that locate the type by class need no such marker, because a
	// site with no type node simply binds nothing.
	untypedMarkers []string

	// typeText renders a type node as the text the qualifier's type is recorded
	// under, or "" to decline it.
	//
	// IT IS A CLOSED ALLOWLIST IN EVERY LANGUAGE, and declining by default is
	// the point: a permissive descent binds a container's ELEMENT type and
	// hands the qualifier methods its value does not have.
	typeText func(*sitter.Node, []byte) string
}

// nominalQualifierTypes walks ONE declaration's subtree and returns the
// qualifier names it makes visible mapped to their declared types.
//
// WHAT A DECLARATION BINDS IS ITS OWN SCOPE, AND ONLY ITS OWN. A class-like
// declaration binds its fields; a method binds its parameters and its typed
// locals; and a method's map does NOT carry its enclosing class's fields. The
// walk stops at every nested declaration that has a scope of its own, so each
// declaration's cost is proportional to its own source rather than to its
// container's, and no node is visited twice under one chunking of a file.
//
// A FIELD IS STILL REACHABLE FROM INSIDE A METHOD, through the self token
// rather than through a rescan: `this.f.go()` binds `this` to the enclosing
// type's name and the field hop reads that type's recorded field types. A BARE
// `f.go()` — a field referenced with no self qualifier — DECLINES. A decline is
// correct under this rung's bind-only bar; a wrong bind would not be.
//
// It returns nil when the declaration establishes nothing, and nil is a
// meaningful answer rather than an empty one: a declaration that binds nothing
// carries the exact reference site it carried before this rung existed.
func nominalQualifierTypes(spec *nominalSpec, declNode *sitter.Node, src []byte) map[string]QualType {
	if spec == nil || declNode == nil {
		return nil
	}
	b := &qualBinder{classes: spec.kinds}

	// The self token binds ONCE, off the enclosing class-like container, before
	// the walk — so a local of the same spelling can never be shadowed by it
	// under the first-binding-wins rule.
	if spec.selfToken != "" && !nominalSelfSuppressed(spec, declNode) {
		if name := nominalSelfName(spec, declNode, src); name != "" {
			b.bind(spec.selfToken, QualType{Text: name})
		}
	}

	nominalWalk(spec, b, declNode, src)

	if len(b.types) == 0 {
		return nil
	}
	return b.types
}

// nominalSelfSuppressed reports whether the declaration carries a modifier that
// takes the enclosing instance away.
//
// It reads the declaration's OWN children and its modifier wrappers — one level
// each, ONCE PER DECLARATION — because the grammars split on where the keyword
// sits: one puts `static` directly under a modifiers list, another wraps each
// modifier in its own node.
func nominalSelfSuppressed(spec *nominalSpec, declNode *sitter.Node) bool {
	for _, kw := range spec.selfSuppressors {
		if hasAnonymousChild(declNode, kw) {
			return true
		}
		for i := range int(declNode.NamedChildCount()) {
			child := declNode.NamedChild(i)
			if spec.roles[spec.kinds.class(child.Symbol())] != nominalRoleIgnored {
				continue
			}
			if hasAnonymousChild(child, kw) {
				return true
			}
		}
	}
	return false
}

// nominalSelfName returns the name of the class-like container the declaration
// belongs to: its OWN name when the declaration is itself class-like, and the
// nearest enclosing one otherwise.
//
// The ascent runs ONCE PER DECLARATION, not per node, which is what makes
// containerName's kind-name comparisons affordable here.
func nominalSelfName(spec *nominalSpec, declNode *sitter.Node, src []byte) string {
	if spec.containers[spec.kinds.class(declNode.Symbol())] {
		return containerName(declNode, src)
	}
	for p := declNode.Parent(); p != nil; p = p.Parent() {
		if spec.containers[spec.kinds.class(p.Symbol())] {
			return containerName(p, src)
		}
	}
	return ""
}

// nominalWalk descends one declaration binding every bind site it meets, and
// stops at every nested declaration carrying a scope of its own.
func nominalWalk(spec *nominalSpec, b *qualBinder, node *sitter.Node, src []byte) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		class := spec.kinds.class(child.Symbol())
		if spec.roles[class] == nominalRoleScopeBreak {
			continue
		}
		if site := spec.sites[class]; site != nominalSiteNone {
			nominalBind(spec, b, child, site, src)
		}
		nominalWalk(spec, b, child, src)
	}
}

// nominalBind binds every name one bind site introduces to the one type it
// declares.
func nominalBind(spec *nominalSpec, b *qualBinder, site *sitter.Node, layout uint8, src []byte) {
	for _, marker := range spec.untypedMarkers {
		if hasAnonymousChild(site, marker) {
			return
		}
	}

	// The site's own named children, with the leading modifiers dropped so the
	// positional rule below reads the type rather than a modifier list.
	kids := make([]*sitter.Node, 0, int(site.NamedChildCount()))
	for i := range int(site.NamedChildCount()) {
		child := site.NamedChild(i)
		if spec.roles[spec.kinds.class(child.Symbol())] == nominalRoleIgnored {
			continue
		}
		kids = append(kids, child)
	}
	if len(kids) < 2 {
		return
	}

	var typeNode *sitter.Node
	var names []*sitter.Node
	switch layout {
	case nominalSiteTypeFirst:
		typeNode = kids[0]
		names = nominalNamesAfterType(spec, kids[1:])
	case nominalSiteNameFirst:
		for i, child := range kids {
			if spec.roles[spec.kinds.class(child.Symbol())] == nominalRoleType {
				typeNode = child
				names = nominalNamesAfterType(spec, kids[:i])
				break
			}
		}
	}
	if typeNode == nil || len(names) == 0 {
		return
	}

	text := spec.typeText(typeNode, src)
	if text == "" {
		return
	}
	for _, name := range names {
		b.bind(name.Content(src), QualType{Text: text})
	}
}

// nominalNamesAfterType selects the name nodes among a bind site's remaining
// children.
//
// DECLARATORS WIN OVER BARE NAMES WHERE BOTH COULD MATCH, and where there are
// no declarators exactly ONE bare name is taken. Both halves guard against the
// same over-binding: a site may carry a trailing DEFAULT VALUE or INITIALIZER
// whose spelling is a bare name of the same kind as the declared one, and
// taking every matching child would bind the initializer's name to the
// declared type.
func nominalNamesAfterType(spec *nominalSpec, kids []*sitter.Node) []*sitter.Node {
	var out []*sitter.Node
	for _, child := range kids {
		if spec.roles[spec.kinds.class(child.Symbol())] != nominalRoleDeclarator {
			continue
		}
		for i := range int(child.NamedChildCount()) {
			inner := child.NamedChild(i)
			if spec.roles[spec.kinds.class(inner.Symbol())] == nominalRoleName {
				out = append(out, inner)
				break
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	if len(kids) > 0 && spec.roles[spec.kinds.class(kids[0].Symbol())] == nominalRoleName {
		return kids[:1]
	}
	return nil
}
