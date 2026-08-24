// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterGoQualifierTypes()
}

// RegisterGoQualifierTypes installs the Go qualifier-type arm. It is EXPORTED
// for the reason RegisterGoBindsResolver is: a test that takes the arm OUT to
// measure the unarmed baseline must RESTORE the production registration on
// cleanup, and UnregisterQualifierTypes DELETES the entry rather than parking
// it. A cleanup that only unregisters would silently disarm the typed-qualifier
// rung for every later test in the same binary, and the symptom would not be a
// missing arm — it would be Go method calls quietly resolving through R3 in
// tests that happen to run afterwards.
func RegisterGoQualifierTypes() {
	RegisterQualifierTypes(LangGo, goQualifierTypes)
}

// qualBinder accumulates one declaration's qualifier bindings under the
// FIRST-BINDING-WINS rule, with a conflicting rebind DELETING the entry.
//
// The deletion is not an optimisation. The simulation this rung reproduces
// binds the first occurrence, marks a name later bound to textually different
// type text as conflicted, and then DECLINES the conflicted qualifier at
// resolution time. Dropping the rule manufactures wrong targets in exactly the
// population the zero-wrong-targets gate covers — shadowed names are 32
// fan-out groups on the knowledge corpus alone.
// BOTH MAPS ARE LAZILY ALLOCATED and both are therefore NIL-TOLERANT on every
// read. A nil map read yields the zero value and len(nil) is 0, which is what
// lets bind's lookup and goQualifierTypes' len==0 return work unchanged before
// the first successful bind.
type qualBinder struct {
	types      map[string]QualType
	conflicted map[string]bool

	// classes is the Go symbol class table, HOISTED HERE ON PURPOSE. It is
	// assigned once where the binder is constructed, so the recursive walk and
	// every binder-taking helper classify a node with a struct-field read plus
	// one array index — never a per-node sync.Once fast path, and never a
	// per-node cgo string conversion.
	classes symbolClasses
}

// bind records one name-to-type binding.
//
// CONFLICT IS JUDGED ON TYPE TEXT, matching the simulation that produced the
// measured coverage figures: a rebind to the same text is not a conflict, so a
// name reassigned to its own type keeps its binding. The conflicted set is
// remembered separately from the map so a THIRD binding cannot re-add a name a
// second binding already knocked out — without it, `x := A{}; x := B{}; x := A{}`
// would end up bound to A again.
func (b *qualBinder) bind(name string, qt QualType) {
	// The blank identifier names no qualifier: nothing can be called on it.
	if name == "" || name == "_" || qt.Text == "" {
		return
	}
	if b.conflicted[name] {
		return
	}
	if existing, ok := b.types[name]; ok {
		if existing.Text != qt.Text {
			delete(b.types, name)
			if b.conflicted == nil {
				// Allocated on the first conflict only. Conflicts are rare —
				// 32 fan-out groups across the whole knowledge corpus — so the
				// overwhelmingly common declaration pays no second map.
				b.conflicted = map[string]bool{}
			}
			b.conflicted[name] = true
		}
		return
	}
	if b.types == nil {
		// Allocated on the FIRST SUCCESSFUL BIND only, the same deferral the
		// conflicted map above documents and for the same reason: a
		// declaration that binds nothing used to allocate a map purely to
		// discard it at the len==0 return. Measured on the benchmark input, 2
		// of its 13 declarations bind nothing.
		b.types = map[string]QualType{}
	}
	b.types[name] = qt
}

// goQualifierTypes is the Go arm: one walk of a declaration's subtree,
// returning the qualifier names it makes visible mapped to their declared
// types.
//
// It is a plain recursive node walk rather than a tree-sitter query. A
// QueryCursor is a cgo handle that must be closed on every path, and this walk
// needs no pattern matching that NamedChild cannot express — so not creating
// one REMOVES that failure mode instead of guarding it.
func goQualifierTypes(declNode *sitter.Node, src []byte) map[string]QualType {
	if declNode == nil {
		return nil
	}
	b := &qualBinder{classes: goKinds()}

	// The signature: receiver, parameters, and any NAMED results — all three
	// are parameter_lists whose parameter_declarations carry identifiers
	// before a type, so one binder handles them.
	recv, params, result := goSignatureParts(declNode)
	bindGoParameterList(b, recv, src)
	bindGoParameterList(b, params, src)
	if result != nil && b.classes.class(result.Symbol()) == goKindParameterList {
		bindGoParameterList(b, result, src)
	}

	// The body: closures' parameters and every local declaration, at any depth.
	walkGoQualifiers(b, declNode, src)

	if len(b.types) == 0 {
		return nil
	}
	return b.types
}

// goSignatureParts returns a declaration's receiver list, parameter list and
// result node, any of which may be nil.
//
// THE RESULT IS FOUND POSITIONALLY AND THEN TESTED, NEVER BY ORDINAL. On a
// function_declaration the parameter list is the first parameter_list; on a
// method_declaration the RECEIVER holds that slot and the parameter list is the
// second. The result is whatever node FOLLOWS the parameter list: a `block`
// means the declaration has no results at all, and any other node is the
// result.
//
// An ordinal rule — "the result is the third parameter_list" — gets the
// three-list method right and gets a result-less method right BY ACCIDENT, but
// SILENTLY DROPS the result of `func (s *S) n(p Q) T`, whose bare-type result
// is no parameter_list at all. That is a missing binding rather than a loud
// failure, which is why the rule here tests the node instead of counting lists.
func goSignatureParts(declNode *sitter.Node) (recv, params, result *sitter.Node) {
	classes := goKinds()
	kind := classes.class(declNode.Symbol())
	if kind != goKindFunctionDeclaration && kind != goKindMethodDeclaration {
		return nil, nil, nil
	}

	n := int(declNode.NamedChildCount())
	i := 0
	if kind == goKindMethodDeclaration {
		// The receiver is the first parameter_list, taken positionally: the
		// method's own name is a field_identifier, so the receiver list cannot
		// be confused with the parameter list by kind alone.
		for ; i < n; i++ {
			if classes.class(declNode.NamedChild(i).Symbol()) == goKindParameterList {
				recv = declNode.NamedChild(i)
				i++
				break
			}
		}
	}
	for ; i < n; i++ {
		if classes.class(declNode.NamedChild(i).Symbol()) == goKindParameterList {
			params = declNode.NamedChild(i)
			i++
			break
		}
	}
	if params != nil && i < n {
		if next := declNode.NamedChild(i); classes.class(next.Symbol()) != goKindBlock {
			result = next
		}
	}
	return recv, params, result
}

// bindGoParameterList binds every named entry of one parameter_list.
//
// A parameter_declaration's named children are its identifiers followed by its
// single type — `p Q` is (identifier, type_identifier) and `r, t R` is
// (identifier, identifier, type_identifier) — so MULTIPLE NAMES SHARE ONE TYPE
// and every one of them must be bound. A first-child rule silently drops all
// but one. An entry carrying only a type and no identifier is an unnamed
// parameter or an unnamed result: it names no qualifier and binds nothing.
func bindGoParameterList(b *qualBinder, list *sitter.Node, src []byte) {
	if list == nil {
		return
	}
	for i := range int(list.NamedChildCount()) {
		decl := list.NamedChild(i)
		if b.classes.class(decl.Symbol()) != goKindParameterDeclaration {
			continue
		}
		names, typeNode := goNamesAndType(decl, src)
		if typeNode == nil {
			continue
		}
		text := goQualTypeText(typeNode, src)
		for _, name := range names {
			b.bind(name.Content(src), QualType{Text: text})
		}
	}
}

// goQualTypeText renders a type expression as the text a qualifier's type is
// recorded under, or "" to decline it.
//
// IT IS A CLOSED ALLOWLIST, and that is the point. Only three kinds are
// accepted — a bare type, a package-qualified type, and a generic instantiation
// whose type arguments are stripped — while pointer and parenthesized wrappers
// are stripped and EVERYTHING ELSE DECLINES. The alternative shape, delegating
// straight to qualifiedTypeName, would be wrong here: its final fallback is
// findTypeIdentifier, which digs the first type_identifier out of ANY subtree,
// so `*[]T` would come back bound to T. Declining by default is what keeps a
// container from binding a method its value does not have.
func goQualTypeText(typeNode *sitter.Node, src []byte) string {
	if typeNode == nil {
		return ""
	}
	switch goKinds().class(typeNode.Symbol()) {
	case goKindTypeIdentifier, goKindQualifiedType, goKindGenericType:
		// qualifiedTypeName keeps a qualified type's package intact and strips
		// a generic instantiation's type arguments, which is exactly the
		// descent wanted here.
		return qualifiedTypeName(typeNode, src)
	case goKindPointerType, goKindParenthesizedType:
		// Re-enter through this function rather than recursing inside
		// qualifiedTypeName, so a wrapper around a container still declines.
		if typeNode.NamedChildCount() > 0 {
			return goQualTypeText(typeNode.NamedChild(0), src)
		}
	}
	return ""
}

// walkGoQualifiers descends one declaration binding the local syntax that makes
// a qualifier visible: closure parameters and the three local declaration
// forms. It handles no other kind, so a parameter_list belonging to a
// `function_type` — a type, not a scope — is never mistaken for a scope that
// introduces names.
//
// THE SWITCH IS ON THE NUMERIC SYMBOL, NOT ON THE KIND NAME, AND THAT IS THE
// MEASURED COST OF THIS FUNCTION. Reading a node's kind name converts a cgo
// C-string into a FRESH Go string on every call, so a recursive walk that names
// every named node at every depth allocates once per node visited; the symbol
// is a scalar the binding already holds and costs nothing to read. b.classes
// turns it into one bounds-checked array index.
//
// THE CLASS TABLE MAPS A KIND NAME TO A SET OF SYMBOLS, NEVER TO ONE. The Go
// grammar spells "identifier" as symbols 1, 60 and 61 and "argument_list" as
// 172 and 173. Do not simplify the table back to one id per name: today only
// symbol 1 surfaces for "identifier", so a single-id table would be right by
// coincidence and wrong the moment the grammar's own routing changes.
func walkGoQualifiers(b *qualBinder, node *sitter.Node, src []byte) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		switch b.classes.class(child.Symbol()) {
		case goKindFuncLiteral:
			// Closures are local syntax of the SAME declaration, so their
			// parameters are qualifiers of it, at any depth.
			params, result := goFuncLiteralParts(child)
			bindGoParameterList(b, params, src)
			if result != nil && b.classes.class(result.Symbol()) == goKindParameterList {
				bindGoParameterList(b, result, src)
			}
		case goKindVarDecl, goKindConstDecl:
			bindGoSpecs(b, child, src)
		case goKindShortVarDecl:
			bindGoShortVarDeclaration(b, child, src)
		}
		walkGoQualifiers(b, child, src)
	}
}

// goFuncLiteralParts is goSignatureParts for a func_literal, whose named
// children are its parameter_list, an optional result, then its block.
func goFuncLiteralParts(lit *sitter.Node) (params, result *sitter.Node) {
	classes := goKinds()
	n := int(lit.NamedChildCount())
	i := 0
	for ; i < n; i++ {
		if classes.class(lit.NamedChild(i).Symbol()) == goKindParameterList {
			params = lit.NamedChild(i)
			i++
			break
		}
	}
	if params != nil && i < n {
		if next := lit.NamedChild(i); classes.class(next.Symbol()) != goKindBlock {
			result = next
		}
	}
	return params, result
}

// bindGoSpecs binds the var_spec / const_spec children of a var or const
// declaration, only where an explicit type node is present.
func bindGoSpecs(b *qualBinder, decl *sitter.Node, src []byte) {
	for i := range int(decl.NamedChildCount()) {
		spec := decl.NamedChild(i)
		if kind := b.classes.class(spec.Symbol()); kind != goKindVarSpec && kind != goKindConstSpec {
			continue
		}
		names, typeNode := goNamesAndType(spec, src)
		if typeNode == nil {
			continue
		}
		text := goQualTypeText(typeNode, src)
		for _, name := range names {
			b.bind(name.Content(src), QualType{Text: text})
		}
	}
}

// bindGoShortVarDeclaration binds the `:=` form.
//
// BOTH SIDES ARE expression_list WRAPPERS, so the identifiers and the
// right-hand expressions are GRANDCHILDREN of the declaration — a walk that
// reads direct children finds nothing at all.
//
// Two arrangements are handled. When the counts match, name i takes its type
// from expression i. When the right side is a SINGLE call_expression and the
// left holds N identifiers, this is the multi-value form: every name binds to
// that one callee with its own ResultIndex, which is the position the parser
// later indexes into the callee's declared result list.
func bindGoShortVarDeclaration(b *qualBinder, decl *sitter.Node, src []byte) {
	left, right := goShortVarSides(decl)
	if left == nil || right == nil {
		return
	}
	names := goNamedChildrenOfKind(left, goKindIdentifier)
	if len(names) == 0 {
		return
	}

	if int(right.NamedChildCount()) == 1 && b.classes.class(right.NamedChild(0).Symbol()) == goKindCallExpression {
		call := right.NamedChild(0)
		text := goCalleeText(call, src)
		for i, name := range names {
			b.bind(name.Content(src), QualType{Text: text, FromCall: true, ResultIndex: i})
		}
		return
	}

	if int(right.NamedChildCount()) != len(names) {
		return
	}
	for i, name := range names {
		qt, ok := goQualTypeFromExpr(right.NamedChild(i), src)
		if !ok {
			continue
		}
		b.bind(name.Content(src), qt)
	}
}

// goShortVarSides returns the left and right expression_list of a
// short_var_declaration.
func goShortVarSides(decl *sitter.Node) (left, right *sitter.Node) {
	lists := goNamedChildrenOfKind(decl, goKindExpressionList)
	if len(lists) != 2 {
		return nil, nil
	}
	return lists[0], lists[1]
}

// goNamedChildrenOfKind collects a node's direct named children of one kind
// class.
func goNamedChildrenOfKind(node *sitter.Node, kind uint8) []*sitter.Node {
	classes := goKinds()
	var out []*sitter.Node
	for i := range int(node.NamedChildCount()) {
		if child := node.NamedChild(i); classes.class(child.Symbol()) == kind {
			out = append(out, child)
		}
	}
	return out
}

// goQualTypeFromExpr infers a qualifier's type from the expression it was
// assigned, for the four right-hand shapes the simulation binds.
func goQualTypeFromExpr(expr *sitter.Node, src []byte) (QualType, bool) {
	classes := goKinds()
	switch classes.class(expr.Symbol()) {
	case goKindCompositeLiteral:
		// `T{}` — the composite literal's type is its first named child.
		if expr.NamedChildCount() == 0 {
			return QualType{}, false
		}
		text := goQualTypeText(expr.NamedChild(0), src)
		return QualType{Text: text}, text != ""

	case goKindUnaryExpression:
		// `&T{}` — the address of a composite literal has that literal's type
		// for method-set purposes. Any other unary operator (`*p`, `-n`) is not
		// a type-bearing shape and declines.
		if expr.ChildCount() == 0 || expr.Child(0).Content(src) != "&" {
			return QualType{}, false
		}
		if expr.NamedChildCount() == 0 || classes.class(expr.NamedChild(0).Symbol()) != goKindCompositeLiteral {
			return QualType{}, false
		}
		return goQualTypeFromExpr(expr.NamedChild(0), src)

	case goKindTypeAssertionExpression:
		// `x.(T)` — the named children are the operand and the asserted type,
		// so the type is the second.
		if expr.NamedChildCount() < 2 {
			return QualType{}, false
		}
		text := goQualTypeText(expr.NamedChild(1), src)
		return QualType{Text: text}, text != ""

	case goKindCallExpression:
		text := goCalleeText(expr, src)
		return QualType{Text: text, FromCall: true}, text != ""
	}
	return QualType{}, false
}

// goCalleeText returns a call's callee AS WRITTEN, or "" when the callee is not
// a name this rung can carry.
//
// A CONVERSION IS NOT A CALL and declines here naturally rather than by special
// case: `(*Wrapped)(nil)` is a call_expression whose function is a
// parenthesized_expression, which is neither of the two accepted kinds. Do not
// add a case that unwraps it — the unwrapped text names a type being converted
// to, not a declaration whose result type could be looked up.
//
// `New[T]()` is accepted: its callee is still the bare identifier, and the
// type_arguments node is a SIBLING of the callee rather than part of it.
//
// A receiver-method callee like `y.Bar` is RECORDED here, with FromCall set,
// even though resolving it needs a hop through y's own type that this ticket
// does not implement. The parser declines it at resolution time; recording it
// keeps this arm's output a description of the syntax rather than a guess about
// what the resolver can currently do with it.
func goCalleeText(call *sitter.Node, src []byte) string {
	if call.NamedChildCount() == 0 {
		return ""
	}
	callee := call.NamedChild(0)
	switch goKinds().class(callee.Symbol()) {
	case goKindIdentifier, goKindSelectorExpression:
		return callee.Content(src)
	}
	return ""
}
