// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// namespaceSanitizer strips the two characters that carry structural meaning in
// an edge ID: '.' separates namespace from symbol in parser/edges.go, and ':'
// separates the language prefix from the directory below.
var namespaceSanitizer = strings.NewReplacer(".", "_", ":", "_")

// NamespaceToken builds the declared-namespace token from a namespace as the
// SOURCE spells it — a PHP `Foo\Bar`, a C# `App.Models`, a java `com.acme.foo`.
//
// IT IS THE ONE PLACE THE TOKEN IS BUILT, and that is the whole point of
// exporting it. Three producers must agree on the byte string or the scope they
// name silently matches nothing: declaredFileNamespace, which stamps a file's
// own scope; declaredNamespaceBinds, which stamps an import's target scope; and
// the parser's qualifierScope, which derives a fully-qualified reference's target
// scope from the qualifier. A well-formed token assembled a second way is the
// worst failure shape available here — every gate stays green while the rung
// that reads it does nothing at all.
//
// The sanitiser is load-bearing on this input: edge resolution reads everything
// before the FIRST '.' as the namespace token, so a C# "App.Models" assembled
// any other way is split in half.
func NamespaceToken(lang Language, namespace string) string {
	return string(lang) + ":" + namespaceSanitizer.Replace(namespace)
}

// fileNamespace derives the per-file symbol namespace from the file's
// parent directory. Non-Go namespaces carry a language prefix so a Go
// package name can never collide with a directory basename; the ':' is
// the language separator and is stripped from the basename so exactly
// one colon means "not Go". Dots are replaced because parser/edges.go
// splits namespace from symbol on '.'.
//
// The namespace is not persisted: parser/populate.go writes no namespace field
// on a node, and parser/resolveEdges rewrites every surviving edge endpoint to
// a graph node ID. It exists only between chunker emission and edge resolution.
func fileNamespace(filePath string, lang Language) string {
	base := namespaceSanitizer.Replace(filepath.Base(filepath.Dir(filePath)))
	if lang == LangGo {
		return base
	}
	return string(lang) + ":" + base
}

// functionLikeTypes are AST node types that represent function/method scopes
// across all supported tree-sitter grammars. Used to find the enclosing
// function for nested declarations so their node IDs are unique.
var functionLikeTypes = map[string]bool{
	"function_declaration": true, // Go, JS, TS, Swift, Kotlin
	"method_declaration":   true, // Go, C#, Java, PHP
	"method_definition":    true, // JS/TS class methods
	"function_definition":  true, // Python, C, C++, Rust
	"arrow_function":       true, // JS/TS
	"method":               true, // Ruby
	"singleton_method":     true, // Ruby
}

// classLikeTypes are AST node types that represent a named container whose
// members another query pattern chunks separately — a class, interface, trait,
// protocol, module or namespace. Membership is a census of the 32 queries_*.go
// TopLevel queries: a kind belongs here when that language's query chunks a
// member declaration nested inside it and containerName can resolve the
// container's own name from one of its three sources.
//
// NO GO NODE KIND APPEARS HERE. Go's containers are type_declaration,
// type_spec, struct_type and interface_type; their absence is what makes Go's
// behavior unchanged by construction rather than by measurement. Note that
// method_definition and method_declaration are function-like, not class-like,
// and stay in functionLikeTypes.
//
// A kind admitted for one language is admitted for every language that uses
// it: "class" is Ruby's class declaration and also the kind of a
// TypeScript/JavaScript class expression, which is correct here — a member of
// a named class expression takes the class's name, and a member of an
// anonymous one takes nothing. ADDING A KIND HERE THEREFORE REQUIRES A CENSUS
// OF EVERY GRAMMAR THAT USES IT, derived by reading the queries_*.go TopLevel
// patterns rather than by probing one language and inferring the rest: a
// passing test suite cannot reveal that an added kind also appears in a
// grammar nobody wrote a fixture for.
//
// Containment is SINGLE-ANCESTOR: a member takes the name of its nearest named
// container, never a dotted chain of every container above it. A C++ member of
// `namespace outer { namespace inner { ... } }` therefore carries "inner"
// alone. Where a grammar itself hands over a qualified spelling as one name
// node — C#'s `namespace App.Models`, C++17's `namespace a::b` — that full
// path is kept, because it arrives as the container's own name.
//
// Deliberately excluded, with the grammar reason: Elixir's container and
// member are both the call kind, so no kind-based rule can tell defmodule from
// def; C#'s file_scoped_namespace_declaration and PHP's semicolon-form
// namespace are SIBLINGS of the declarations they name rather than ancestors,
// so no upward walk reaches them and they are resolved from the file's own
// declaration instead.
var classLikeTypes = map[string]bool{
	"class_definition":      true, // Python, Scala
	"class_declaration":     true, // TypeScript/TSX, Java, C#, PHP, Swift; Kotlin (see below)
	"class_specifier":       true, // C++
	"struct_specifier":      true, // C++
	"struct_declaration":    true, // C#
	"interface_declaration": true, // Java, C#, PHP
	"enum_declaration":      true, // Java, C#
	"trait_declaration":     true, // PHP
	"trait_definition":      true, // Scala
	"object_definition":     true, // Scala
	"protocol_declaration":  true, // Swift
	"class":                 true, // Ruby; also JS/TS class expressions
	"module":                true, // Ruby
	// Kotlin's class_declaration and object_declaration bind their name
	// POSITIONALLY — ChildByFieldName("name") returns nil on both, so their
	// names come from containerName's third source, the scan of direct named
	// children.
	"object_declaration": true, // Kotlin (no name field — see containerName)
	// Namespace-style containers. Each is admitted because a probe measured it
	// as a true named ancestor of the declarations its language's query chunks.
	"mod_item":              true, // Rust module — binds name:, true ancestor
	"impl_item":             true, // Rust impl — binds type:, see containerName
	"namespace_definition":  true, // C++; PHP braced form only
	"namespace_declaration": true, // C#, block form only
	"module_binding":        true, // OCaml — module_definition has no fields
}

// containerName resolves a class-like container's name. Grammars disagree
// about where they put it, so three sources are tried in order:
//
//  1. the name: field — every kind in classLikeTypes except the two below,
//     proven because each language's own TopLevel query compiles a name:
//     pattern against that kind;
//  2. the type: field, accepted ONLY when it binds a type_identifier —
//     Rust's impl_item carries type: and no name:, and queries_rust.go
//     chunks an impl only as (impl_item type: (type_identifier) @name), so
//     rejecting any other kind keeps a member's ParentName in agreement
//     with the container chunk that actually exists;
//  3. the first direct named child of an identifier-like kind — Kotlin's
//     grammar attaches NO field name to ANY node, so its container names
//     are reachable only positionally.
//
// Returns "" when none applies, which makes findEnclosingScope keep
// walking — an anonymous C++ namespace, an anonymous class expression and
// a generic Rust impl all take that path.
func containerName(p *sitter.Node, src []byte) string {
	if n := p.ChildByFieldName("name"); n != nil {
		return n.Content(src)
	}
	if n := p.ChildByFieldName("type"); n != nil {
		// A type: field binding anything else — Rust's `impl<T> Gen<T>` binds
		// type: generic_type — returns early rather than falling through to
		// the scan, which would otherwise pick some unrelated identifier out
		// of the node and parent members to a container chunk that does not
		// exist.
		if n.Type() == "type_identifier" {
			return n.Content(src)
		}
		return ""
	}
	// The scan MUST NOT stop at the first named child. Kotlin puts `modifiers`
	// at index 0 of both `data class Point` and `private class Dog : ...`, so a
	// first-child rule resolves neither and their members lose their parent
	// entirely. A supertype cannot win the scan instead: it sits inside a
	// delegation_specifier, never as a direct child.
	for i := range int(p.NamedChildCount()) {
		switch c := p.NamedChild(i); c.Type() {
		case "type_identifier", "simple_identifier":
			return c.Content(src)
		}
	}
	return ""
}

// findEnclosingScope walks up the AST parent chain and returns the name of the
// enclosing scope — a class-like container or a function/method. For Go
// methods, returns "Receiver.Method". For anonymous functions assigned to
// variables (e.g., const handler = () => {}), uses the variable name. Returns
// "" if the node is at top-level scope.
func findEnclosingScope(node *sitter.Node, src []byte) string {
	for p := node.Parent(); p != nil; p = p.Parent() {
		// Class-like containers get their own branch and CONTINUE when
		// unnamed, so a nameless container never reaches the
		// function-oriented fallbacks below — anonymousFuncName would
		// otherwise name it after the variable it is assigned to and parent
		// every member to a thing that is not its class.
		if classLikeTypes[p.Type()] {
			if nm := containerName(p, src); nm != "" {
				return nm
			}
			continue
		}
		if !functionLikeTypes[p.Type()] {
			continue
		}
		// Go methods: include receiver type in the name.
		if p.Type() == "method_declaration" {
			receiver := extractGoReceiver(p, src)
			if nameNode := p.ChildByFieldName("name"); nameNode != nil {
				if receiver != "" {
					return receiver + "." + nameNode.Content(src)
				}
				return nameNode.Content(src)
			}
			continue
		}
		// Named function — use its name.
		if nameNode := p.ChildByFieldName("name"); nameNode != nil {
			return nameNode.Content(src)
		}
		// Anonymous function — check if assigned to a variable.
		// e.g., const handler = () => { ... }
		// AST: variable_declarator { name: "handler", value: arrow_function }
		if name := anonymousFuncName(p, src); name != "" {
			return name
		}
		// Anonymous and not assigned to a variable — keep walking up
		// to find a named parent scope.
	}
	return ""
}

// declParentName returns the qualification prefix for a declaration — the Go
// receiver for a Go method, otherwise the enclosing scope. One call serves both
// the chunk's ParentName and its edge endpoints, so parser/populate's symbolMap
// key and the emitted edge IDs are the same string by construction.
func declParentName(declNode *sitter.Node, src []byte, lang Language, chunkType string) string {
	if lang == LangGo && chunkType == "method_declaration" {
		if r := extractGoReceiver(declNode, src); r != "" {
			return r
		}
	}
	return findEnclosingScope(declNode, src)
}

// anonymousFuncName resolves the name for an anonymous function node by checking
// three patterns, in order:
//
//  1. Variable assignment: const foo = () => {} → "foo"
//  2. Assigned call result: const foo = useCallback(() => {}) → "foo"
//  3. Call argument: it('should work', () => {}) → "it(should work)"
//
// Returns "" if the function has no discoverable identity.
func anonymousFuncName(funcNode *sitter.Node, src []byte) string {
	parent := funcNode.Parent()
	if parent == nil {
		return ""
	}
	// Pattern 1: direct variable assignment.
	// AST: variable_declarator { name: identifier, value: arrow_function }
	if parent.Type() == "variable_declarator" {
		if name := varDeclaratorName(parent, src); name != "" {
			return name
		}
	}
	// Pattern 2 & 3: callback argument to a call expression.
	// AST: call_expression { function: identifier, arguments: [..., arrow_function] }
	callNode := parent
	if callNode.Type() == "arguments" {
		callNode = callNode.Parent()
	}
	if callNode == nil || callNode.Type() != "call_expression" {
		return ""
	}
	// Check if the call result is assigned to a variable.
	// e.g., const loadTimeline = useCallback(async () => { ... })
	// AST: variable_declarator { name: "loadTimeline", value: call_expression }
	if callParent := callNode.Parent(); callParent != nil && callParent.Type() == "variable_declarator" {
		if name := varDeclaratorName(callParent, src); name != "" {
			return name
		}
	}
	// Fall back to callee name + first string argument.
	callee := callExpressionName(callNode, src)
	if callee == "" {
		return ""
	}
	if desc := firstStringArg(callNode, src); desc != "" {
		return callee + "(" + desc + ")"
	}
	return callee
}

// varDeclaratorName returns the identifier name from a variable_declarator node,
// or "" if the name is a destructuring pattern.
func varDeclaratorName(vd *sitter.Node, src []byte) string {
	nameNode := vd.ChildByFieldName("name")
	if nameNode != nil && nameNode.Type() == "identifier" {
		return nameNode.Content(src)
	}
	return ""
}

// callExpressionName extracts the callee name from a call_expression.
// Handles:
//   - identifier         — the common case (JS/TS, Scala, Java, etc.).
//   - simple_identifier  — Kotlin and Swift grammars name the callee leaf this way.
//   - name               — PHP grammar names the callee leaf this way.
//   - member_expression  — JS/TS dot access (e.g. page.goto).
//
// Returns "" for any other shape (lambda invocations, parenthesized, call
// expressions whose function is itself a call_expression — see Bucket B's
// JS Pattern C three-line outer-call unwrap for `.each` parameterized form).
//
// Field-name fallback: Kotlin's tree-sitter grammar does NOT name the
// `function` field on call_expression — children are positional. When
// ChildByFieldName("function") returns nil, this helper inspects the first
// named child and accepts the same identifier-like types listed above.
// This keeps the helper usable from Kotlin (Phase 4) without forking a
// per-language helper, and preserves return-"" semantics for nodes whose
// first named child is not an identifier (lambda invocations, parenthesized
// expressions, member_expression-only nested forms).
//
// Reuse precedent for Ruby and Elixir: those grammars use different field
// names (`method` and `target`) for the callee leaf, so per-language helpers
// are justified there. Bucket B's Phases 4/5/6/9 (Kotlin/Scala/Swift/PHP) all
// use this helper directly post-T2-A consolidation.
func callExpressionName(callNode *sitter.Node, src []byte) string {
	fn := callNode.ChildByFieldName("function")
	if fn == nil {
		// Field-less fallback: Kotlin grammar uses positional children.
		// First named child is the callee leaf or a nested call_expression.
		if callNode.NamedChildCount() > 0 {
			fn = callNode.NamedChild(0)
		}
	}
	if fn == nil {
		return ""
	}
	switch fn.Type() {
	case "identifier", "simple_identifier", "name":
		return fn.Content(src)
	case "member_expression":
		return fn.Content(src)
	}
	return ""
}

// firstStringArg finds the first string literal argument in a call_expression's arguments.
// Returns the string content without quotes, or "" if none found.
func firstStringArg(callNode *sitter.Node, src []byte) string {
	args := callNode.ChildByFieldName("arguments")
	if args == nil {
		return ""
	}
	for i := range int(args.NamedChildCount()) {
		arg := args.NamedChild(i)
		if arg.Type() == "string" || arg.Type() == "template_string" {
			s := arg.Content(src)
			// Strip outer quotes.
			if len(s) >= 2 {
				return s[1 : len(s)-1]
			}
			return s
		}
	}
	return ""
}

// nonTypeContainerKinds are the classLikeTypes members a type reference never
// names: an implementation, module, namespace or companion-object block that
// shares its name with the type it accompanies. Every entry is a member of the
// classLikeTypes census above, so this is a FILTER over that list rather than a
// second census — derived by asking of each admitted kind "is this a type, or a
// block that implements or scopes one?", and checkable by diffing the two.
var nonTypeContainerKinds = map[string]bool{
	"impl_item":             true, // Rust impl
	"mod_item":              true, // Rust module
	"namespace_definition":  true, // C++; PHP braced form
	"namespace_declaration": true, // C# block form
	"module_binding":        true, // OCaml
	"module":                true, // Ruby
	"object_definition":     true, // Scala companion object
	"object_declaration":    true, // Kotlin object
}

// collisionNames is one file's resolution of every colliding declaration name,
// computed once and serving both edge rewrites: the parent-to-member CONTAINS
// FromID and the USES_TYPE ToID.
type collisionNames struct {
	// final is each pendingDecl's emitted name, positionally.
	final []string
	// typeRefAlias maps a collided base name to the suffixed name of the one
	// declaration a type reference to it may mean. A base name is absent when
	// the answer is ambiguous.
	typeRefAlias map[string]string
}

// resolveCollisionNames computes every declaration's final name and, for each
// collided base name, the single declaration a type reference to it may mean.
//
// The candidate pool is EVERY top-level declaration, not only containers: a
// Rust `pub fn Thing()` beside `pub struct Thing {}` collides, and the function
// belongs to no container deny set, so a deny-set-only rule would hand it the
// alias. The rule is therefore ambiguity abstention — claim the alias only when
// EXACTLY ONE colliding declaration survives nonTypeContainerKinds, and claim
// nothing when two or more survive or none does.
//
// ABSTAINING NO LONGER DROPS THE REFERENCE. It reaches the resolution walk
// unsuffixed, finds the whole collided set under its base name, and becomes a
// multi-candidate edge group: one edge per candidate, each saying "exactly one
// of these". Abstention now means declining to NARROW, not declining to emit.
func resolveCollisionNames(pending []pendingDecl, counts map[[2]string]int) collisionNames {
	c := collisionNames{final: make([]string, len(pending))}
	survivors := make(map[string]int)
	winner := make(map[string]string)

	for i, p := range pending {
		c.final[i] = p.name
		if p.name == "" || counts[[2]string{p.parentName, p.name}] < 2 {
			continue
		}
		suffixed := p.name + "#" + astPathHash(p.declNode)
		c.final[i] = suffixed
		// chunkType rather than the raw node kind: resolveChunkType unwraps an
		// export_statement, so an exported TypeScript declaration is weighed as
		// the interface or function it declares.
		if !nonTypeContainerKinds[p.chunkType] {
			survivors[p.name]++
			winner[p.name] = suffixed
		}
	}

	for base, n := range survivors {
		if n != 1 {
			continue
		}
		if c.typeRefAlias == nil {
			c.typeRefAlias = make(map[string]string)
		}
		c.typeRefAlias[base] = winner[base]
	}
	return c
}

// aliasTypeRefTargets repoints every USES_TYPE edge whose target is a collided
// base name onto the declaration the alias table picked. A type-reference edge
// carries the bare, unqualified name as its ToID, so this rewrite is what lets
// it resolve against the suffixed key the winning chunk actually carries.
func aliasTypeRefTargets(edges []Edge, alias map[string]string) []Edge {
	if len(alias) == 0 {
		return edges
	}
	for i := range edges {
		if to, ok := alias[edges[i].ToID]; ok {
			edges[i].ToID = to
		}
	}
	return edges
}

// astPathHash computes a short hash of the AST path from a node to the tree root.
// The path encodes the structural position of the node (parent types + child indices),
// producing a deterministic fingerprint that uniquely identifies the node's location
// in the AST. Used as a collision breaker when node IDs would otherwise collide.
func astPathHash(node *sitter.Node) string {
	var path strings.Builder
	for n := node; n != nil; n = n.Parent() {
		if path.Len() > 0 {
			path.WriteByte('/')
		}
		path.WriteString(n.Type())
		// Include child index among siblings for uniqueness.
		if p := n.Parent(); p != nil {
			for i := range int(p.NamedChildCount()) {
				if p.NamedChild(i).StartByte() == n.StartByte() && p.NamedChild(i).EndByte() == n.EndByte() {
					fmt.Fprintf(&path, "[%d]", i)
					break
				}
			}
		}
	}
	h := sha256.Sum256([]byte(path.String()))
	return hex.EncodeToString(h[:4]) // 8 hex chars
}
