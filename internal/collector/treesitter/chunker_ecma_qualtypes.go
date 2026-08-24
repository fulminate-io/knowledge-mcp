// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterECMAQualifierTypes()
}

// RegisterECMAQualifierTypes installs the typescript, tsx and javascript
// qualifier-type arms.
//
// It is EXPORTED for the reason RegisterGoQualifierTypes is: a test that takes
// an arm OUT to measure an unarmed baseline must be able to RESTORE the
// production registration, and UnregisterQualifierTypes DELETES the entry rather
// than parking it. A cleanup that only unregistered would silently disarm the
// rung for every later test in the same binary.
func RegisterECMAQualifierTypes() {
	RegisterQualifierTypes(LangTypeScript, tsQualifierTypes)
	RegisterQualifierTypes(LangTSX, tsxQualifierTypes)
	RegisterQualifierTypes(LangJavaScript, jsQualifierTypes)
}

// The three arms. Each is a thin wrapper passing its own grammar's class table
// and its own type-syntax capability into one shared body — the grammars differ
// in symbol numbering and in whether type annotations exist at all, and neither
// difference justifies three copies of the walk.
// Each arm also passes its OWN Language, because the shared body's container
// ascent reads a per-language admission row. A constant would be wrong in both
// directions: TypeScript's row would hand JavaScript admissions its grammar
// never declares, and JavaScript's would strip interface_declaration and
// abstract_class_declaration from TypeScript, so a `this` inside an abstract
// class's method would ascend PAST the class and bind to the wrong receiver
// type rather than merely lose one.
func tsQualifierTypes(declNode *sitter.Node, src []byte) map[string]QualType {
	return ecmaQualifierTypes(tsKinds(), true, LangTypeScript, declNode, src)
}

func tsxQualifierTypes(declNode *sitter.Node, src []byte) map[string]QualType {
	return ecmaQualifierTypes(tsxKinds(), true, LangTSX, declNode, src)
}

func jsQualifierTypes(declNode *sitter.Node, src []byte) map[string]QualType {
	return ecmaQualifierTypes(jsKinds(), false, LangJavaScript, declNode, src)
}

// ecmaQualifierTypes is the shared body: one walk of a declaration's subtree,
// returning the qualifier names it makes visible mapped to their declared types.
//
// It is a plain recursive NamedChild walk rather than a tree-sitter query, on
// the Go arm's stated reasoning: a QueryCursor is a cgo handle that must be
// closed on every path, and not creating one REMOVES that failure mode instead
// of guarding it.
//
// typed SEPARATES THE TYPE-SYNTAX ROUTES FROM THE STRUCTURAL ONES. typescript
// and tsx get all four routes; javascript gets the receiver and the constructor
// local only, because it has no annotation syntax to read and its JSDoc arrives
// as one opaque comment token this collector does not parse.
func ecmaQualifierTypes(classes symbolClasses, typed bool, lang Language, declNode *sitter.Node, src []byte) map[string]QualType {
	if declNode == nil {
		return nil
	}
	// The export unwrap comes FIRST, before anything reads a child. The TopLevel
	// query binds an exported declaration's @decl to the export_statement, so a
	// descent run against the raw node would find no class, no parameters and no
	// body on the majority of real TypeScript.
	decl := unwrapExportedDecl(declNode)

	b := &qualBinder{classes: classes}
	bindECMAReceiver(b, decl, src, lang)
	walkECMAQualifiers(b, decl, src, typed)

	if len(b.types) == 0 {
		return nil
	}
	return b.types
}

// bindECMAReceiver binds `this` to the name of the class the declaration is
// declared in.
//
// IT IS THE DIRECT ANALOG OF GO'S RECEIVER BINDING — the first thing
// QualifierTypeResolver's own doc comment names — and for a class-based
// codebase it is the route that carries the arm.
//
// THE NON-ARROW FUNCTION FORM SUPPRESSES THE WHOLE BINDING, and the suppression
// is load-bearing rather than cautious. A `function` declaration or a
// `function` expression REBINDS `this` at run time, and this arm has no
// positional discrimination inside a declaration: it cannot say that the calls
// before the nested function saw the class and the ones inside it saw something
// else. Declining the whole declaration costs recall only, because the rung is
// bind-only — a qualifier it does not bind resolves exactly as it did before
// this arm existed. Arrow functions are transparent to `this` and correctly do
// NOT suppress.
func bindECMAReceiver(b *qualBinder, decl *sitter.Node, src []byte, lang Language) {
	if ecmaRebindsThis(b, decl) {
		return
	}
	if name := ecmaClassScopeName(decl, src, lang); name != "" {
		b.bind("this", QualType{Text: name})
	}
}

// ecmaClassScopeName returns the class `this` refers to inside one declaration,
// or "" when there is none or it is anonymous.
//
// A DECLARATION THAT IS ITSELF A CLASS IS ITS OWN ANSWER, AND MUST NOT ASCEND.
// Ascending from a class would walk past it to whatever class encloses it and
// bind `this` to the outer one, which names a type the receiver does not have.
//
// AN ANONYMOUS CONTAINER STOPS THE ASCENT RATHER THAN CONTINUING IT, for the
// same reason one level up. findEnclosingScope continues in that case because it
// is answering a different question: what should this chunk be filed under.
func ecmaClassScopeName(decl *sitter.Node, src []byte, lang Language) string {
	// The caller's own language, not a constant: the three arms sharing this
	// body have three different admission rows.
	admit := classLikeByLang[lang]
	if admit[decl.Type()] {
		return containerName(decl, src)
	}
	for p := decl.Parent(); p != nil; p = p.Parent() {
		if admit[p.Type()] {
			return containerName(p, src)
		}
	}
	return ""
}

// ecmaRebindsThis reports whether a declaration's subtree introduces a new
// `this`.
//
// TWO FORMS DO, AND A CLASS IS THE SECOND. A `function` declaration or
// expression rebinds `this` at run time; a nested CLASS rebinds it for every one
// of its own members, so a declaration holding a class expression — the
// `const Inner = class { ... }` form — cannot attribute a `this` inside it to
// the enclosing class either. Without the class arm the qualifier map for that
// declaration binds `this` to the OUTER class and every `this.x` written inside
// the class expression resolves against the wrong type.
//
// Arrow functions are transparent to `this` and correctly do NOT appear here.
func ecmaRebindsThis(b *qualBinder, node *sitter.Node) bool {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		switch b.classes.class(child.Symbol()) {
		case ecmaKindFunctionDeclaration, ecmaKindFunctionExpression,
			ecmaKindClassExpression, ecmaKindClassDeclaration, ecmaKindAbstractClassDeclaration:
			return true
		}
		if ecmaRebindsThis(b, child) {
			return true
		}
	}
	return false
}

// walkECMAQualifiers descends one declaration binding the local syntax that
// makes a qualifier visible: annotated parameters, and the three local
// declarator forms.
//
// THE SWITCH IS ON THE NUMERIC SYMBOL, NOT ON THE KIND NAME, and that is the
// measured cost of this function. Reading a node's kind name converts a cgo
// C-string into a fresh Go string on every call, so a recursive walk that names
// every node at every depth allocates once per node visited; the symbol is a
// scalar the binding already holds, and b.classes turns it into one
// bounds-checked array index.
func walkECMAQualifiers(b *qualBinder, node *sitter.Node, src []byte, typed bool) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		switch b.classes.class(child.Symbol()) {
		case ecmaKindRequiredParameter, ecmaKindOptionalParameter:
			// Both kinds exist only in the typed grammars, so this arm is
			// unreachable for javascript by vocabulary as well as by the flag.
			if typed {
				bindECMAParameter(b, child, src)
			}
		case ecmaKindVariableDeclarator:
			bindECMADeclarator(b, child, src, typed)
		}
		walkECMAQualifiers(b, child, src, typed)
	}
}

// bindECMAParameter binds one annotated parameter.
//
// THE NAME IS FOUND BY KIND, NEVER BY POSITION, and the reason is measured
// rather than defensive. A TypeScript parameter property parses as
// (accessibility_modifier, identifier, type_annotation), so a rule reading named
// child 0 binds the modifier `private` as the parameter's name; and `readonly`
// arrives as an ANONYMOUS token, which shifts nothing in the named-child list
// and so cannot be corrected by a fixed offset either. The identifier is at no
// fixed index and must be located by what it is.
func bindECMAParameter(b *qualBinder, param *sitter.Node, src []byte) {
	var nameNode, annotation *sitter.Node
	for i := range int(param.NamedChildCount()) {
		child := param.NamedChild(i)
		switch b.classes.class(child.Symbol()) {
		case ecmaKindIdentifier:
			if nameNode == nil {
				nameNode = child
			}
		case ecmaKindTypeAnnotation:
			annotation = child
		}
	}
	if nameNode == nil || annotation == nil {
		return
	}
	b.bind(nameNode.Content(src), QualType{Text: ecmaAnnotationType(b, annotation, src)})
}

// bindECMADeclarator binds one `const`/`let`/`var` declarator, by three routes
// in priority order.
//
// THE ANNOTATION WINS OVER THE INITIALISER, and it is an if/else rather than two
// binds on purpose. qualBinder treats a rebind to different text as a CONFLICT
// and deletes the entry, so binding both halves of `const x: Sink = new Impl()`
// would knock the qualifier out entirely — losing a binding the source states
// outright. The written annotation is the declared type; the initialiser is only
// evidence of it.
func bindECMADeclarator(b *qualBinder, decl *sitter.Node, src []byte, typed bool) {
	name := varDeclaratorName(decl, src)
	if name == "" {
		return
	}
	if typed {
		if annotation := ecmaFirstChildOfClass(b.classes, decl, ecmaKindTypeAnnotation); annotation != nil {
			b.bind(name, QualType{Text: ecmaAnnotationType(b, annotation, src)})
			return
		}
	}
	value := decl.ChildByFieldName("value")
	if value == nil {
		return
	}
	switch b.classes.class(value.Symbol()) {
	case ecmaKindNewExpression:
		// In ECMAScript the constructor NAMES the type, so this is a DIRECT
		// binding rather than a call whose result must be looked up.
		b.bind(name, QualType{Text: ecmaConstructorText(b, value, src)})
	case ecmaKindCallExpression:
		// The call-return route: the rung's existing arm reads the callee's
		// declared result type out of TypeFacts.Results. javascript declares no
		// result types, so it takes this route nowhere and does not walk it.
		if typed {
			if text := ecmaCalleeText(b, value, src); text != "" {
				b.bind(name, QualType{Text: text, FromCall: true})
			}
		}
	}
}

// ecmaConstructorText returns the type a `new` expression constructs, or "" to
// decline.
//
// `new ns.Thing()` DECLINES. Its constructor is a member_expression whose
// qualifier is itself a bound name, and resolving that is a second hop this arm
// does not take — the same line the Go arm draws at a chained receiver.
func ecmaConstructorText(b *qualBinder, expr *sitter.Node, src []byte) string {
	if expr.NamedChildCount() == 0 {
		return ""
	}
	ctor := expr.NamedChild(0)
	switch b.classes.class(ctor.Symbol()) {
	case ecmaKindIdentifier, ecmaKindTypeIdentifier:
		return ctor.Content(src)
	}
	return ""
}

// ecmaCalleeText returns a call's callee AS WRITTEN, or "" when the callee is
// not a name this rung can carry.
//
// A member_expression callee is RECORDED, with FromCall set, even though
// resolving it needs a hop through the receiver's own type. Recording it keeps
// this arm's output a description of the syntax rather than a guess about what
// the resolver can currently do with it; the parser declines it at resolution
// time.
func ecmaCalleeText(b *qualBinder, call *sitter.Node, src []byte) string {
	if call.NamedChildCount() == 0 {
		return ""
	}
	callee := call.NamedChild(0)
	switch b.classes.class(callee.Symbol()) {
	case ecmaKindIdentifier, ecmaKindMemberExpression:
		return callee.Content(src)
	}
	return ""
}

// ecmaAnnotationType renders the type inside a type_annotation, or "" to decline
// it.
//
// IT IS A CLOSED ALLOWLIST, and that is the point. Only a bare type and a
// generic instantiation with its type arguments stripped are accepted;
// everything else declines — a predefined_type (number, string, void) above all,
// plus union, array, object, function and qualified types. Declining by default
// is what keeps a qualifier from being bound to a name whose declaration could
// never be the thing the value holds, which is how a bind-only rung stays
// wrong-target-free.
func ecmaAnnotationType(b *qualBinder, annotation *sitter.Node, src []byte) string {
	if annotation.NamedChildCount() == 0 {
		return ""
	}
	return ecmaTypeText(b, annotation.NamedChild(0), src)
}

// ecmaTypeText renders one type expression under the allowlist above.
func ecmaTypeText(b *qualBinder, typeNode *sitter.Node, src []byte) string {
	if typeNode == nil {
		return ""
	}
	switch b.classes.class(typeNode.Symbol()) {
	case ecmaKindTypeIdentifier:
		return typeNode.Content(src)
	case ecmaKindGenericType:
		// A generic instantiation's base type is its first named child and its
		// type_arguments a sibling, so re-entering here strips the arguments and
		// still declines a base this allowlist does not admit.
		if typeNode.NamedChildCount() > 0 {
			return ecmaTypeText(b, typeNode.NamedChild(0), src)
		}
	}
	return ""
}
