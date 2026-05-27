// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// functionLikeTypes are AST node types that represent function/method scopes
// across all supported tree-sitter grammars. Used to find the enclosing
// function for nested declarations so their node IDs are unique.
var functionLikeTypes = map[string]bool{
	"function_declaration": true, // Go, JS, TS, Swift, Kotlin
	"method_declaration":   true, // Go, C#, Java, PHP
	"method_definition":    true, // JS/TS class methods, Ruby
	"function_definition":  true, // Python, C, C++, Rust
	"arrow_function":       true, // JS/TS
}

// findEnclosingFunction walks up the AST parent chain and returns the name of
// the enclosing function or method. For Go methods, returns "Receiver.Method".
// For anonymous functions assigned to variables (e.g., const handler = () => {}),
// uses the variable name. Returns "" if the node is at top-level scope.
func findEnclosingFunction(node *sitter.Node, src []byte) string {
	for p := node.Parent(); p != nil; p = p.Parent() {
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
	for i := 0; i < int(args.NamedChildCount()); i++ {
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
			for i := 0; i < int(p.NamedChildCount()); i++ {
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
