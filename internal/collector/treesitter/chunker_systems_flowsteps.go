// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterSystemsFlowSteps()
}

// RegisterSystemsFlowSteps installs the c, cpp and rust flow-step arms. It is
// EXPORTED for the reason RegisterGoFlowSteps is: a test that takes an arm OUT
// must be able to RESTORE the production registration, and UnregisterFlowSteps
// DELETES the entry rather than parking it.
func RegisterSystemsFlowSteps() {
	RegisterFlowSteps(LangC, cFlowSteps)
	RegisterFlowSteps(LangCPP, cppFlowSteps)
	RegisterFlowSteps(LangRust, rustFlowSteps)
}

// ADDRESS-OF AND DEREFERENCE UNWRAP TO THE OPERAND, IN ALL THREE LANGUAGES, and
// they do so for free rather than by a special case: `&q` is a pointer_expression
// in C and C++ and a reference_expression in rust, `*p` is a pointer_expression,
// and each wraps the identifier it applies to — so the shared operand descent,
// which stops at the first identifier it reaches, already yields `q` and `p`.
//
// IT IS A DELIBERATE OVER-APPROXIMATION. A pointer to tainted data is tainted,
// and the alternative — treating the address-of as opaque — loses the single
// most common C and rust call shape, which is exactly the population a security
// consumer is looking for.

// cFlowSteps is the c arm.
func cFlowSteps(declNode *sitter.Node, src []byte) []FlowStep {
	if declNode == nil {
		return nil
	}
	w := &nominalFlowWalker{
		classes: cKinds(), prof: calleeProfileFor(LangC), ident: cKindIdentifier, src: src,
	}
	w.seedParams(cSignatureParams(declNode), cKindParameterDeclaration)
	walkCFlowSteps(w, declNode)
	if len(w.steps) == 0 {
		return nil
	}
	return w.steps
}

// cSignatureParams finds a C or C++ function's parameter list, which sits one
// level down inside its function_declarator rather than beside the body.
func cSignatureParams(decl *sitter.Node) *sitter.Node {
	declarator := decl.ChildByFieldName("declarator")
	if declarator == nil {
		return nil
	}
	return declarator.ChildByFieldName("parameters")
}

// walkCFlowSteps descends one C declaration.
//
// THE SWITCH IS ON THE NUMERIC SYMBOL, NOT ON THE KIND NAME, and that is the
// measured cost of this function: reading a node's kind name converts a cgo
// C-string into a FRESH Go string on every call, so a recursive walk that names
// every named node at every depth allocates once per node visited.
//
// C HAS NO NESTED FUNCTION SCOPE to suppress returns for — the language has no
// closures and no lambdas — so this walk takes no inLambda flag, unlike its C++
// and rust siblings.
func walkCFlowSteps(w *nominalFlowWalker, node *sitter.Node) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		switch w.classes.class(child.Symbol()) {
		case cKindDeclaration:
			for j := range int(child.NamedChildCount()) {
				init := child.NamedChild(j)
				if w.classes.class(init.Symbol()) != cKindInitDeclarator {
					continue
				}
				w.emitDefine(w.firstIdent(init.ChildByFieldName("declarator")),
					w.operands(init.ChildByFieldName("value"), nil))
			}
		case cKindAssignmentExpression:
			w.systemsAssign(child, cKindIdentifier, cKindFieldExpression, "argument", "field")
		case cKindCallExpression:
			w.cCall(child)
		case cKindReturnStatement:
			w.emitReturn(child)
		}
		walkCFlowSteps(w, child)
	}
}

// cCall emits one C call's argument steps.
//
// THE PARENTHESIZED POINTER FORM IS HANDLED, not declined, because the Calls
// query captures it: `(*ops->fn)(x)` binds the inner field_expression as the
// callee, so an arm reading only the `function:` field would emit a CALLS edge
// with no flow step beside it and break parity for a shape C uses routinely.
func (w *nominalFlowWalker) cCall(call *sitter.Node) {
	fn := call.ChildByFieldName("function")
	if fn == nil {
		return
	}
	if w.classes.class(fn.Symbol()) == cKindParenthesizedExpression {
		if inner := w.firstChildOfKind(fn, cKindPointerExpression); inner != nil {
			fn = w.firstChildOfKind(inner, cKindFieldExpression)
		}
	}
	if fn == nil {
		return
	}
	switch w.classes.class(fn.Symbol()) {
	case cKindIdentifier, cKindFieldExpression:
		raw := fn.Content(w.src)
		w.emitCallArgs(raw, fn, raw, call.ChildByFieldName("arguments"), 0)
	}
}

// cppFlowSteps is the cpp arm.
func cppFlowSteps(declNode *sitter.Node, src []byte) []FlowStep {
	if declNode == nil {
		return nil
	}
	w := &nominalFlowWalker{
		classes: cppKinds(), prof: calleeProfileFor(LangCPP), ident: cppKindIdentifier, src: src,
	}
	w.seedParams(cSignatureParams(declNode), cppKindParameterDeclaration)
	walkCPPFlowSteps(w, declNode, false)
	if len(w.steps) == 0 {
		return nil
	}
	return w.steps
}

// walkCPPFlowSteps descends one C++ declaration. Same numeric-symbol rule the C
// walk documents; inLambda suppresses returns only, since a lambda's `return`
// belongs to the lambda while its calls belong to the enclosing declaration's
// dataflow.
func walkCPPFlowSteps(w *nominalFlowWalker, node *sitter.Node, inLambda bool) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		nested := inLambda
		switch w.classes.class(child.Symbol()) {
		case cppKindDeclaration:
			for j := range int(child.NamedChildCount()) {
				init := child.NamedChild(j)
				if w.classes.class(init.Symbol()) != cppKindInitDeclarator {
					continue
				}
				w.emitDefine(w.firstIdent(init.ChildByFieldName("declarator")),
					w.operands(init.ChildByFieldName("value"), nil))
			}
		case cppKindAssignmentExpression:
			w.systemsAssign(child, cppKindIdentifier, cppKindFieldExpression, "argument", "field")
		case cppKindCallExpression:
			fn := child.ChildByFieldName("function")
			if fn == nil {
				break
			}
			switch w.classes.class(fn.Symbol()) {
			case cppKindIdentifier, cppKindFieldExpression, cppKindQualifiedIdentifier:
				// C++ IS THE FAMILY THAT PROVES A CALLEE SPELLING CARRIES
				// SEPARATORS: `ptr->run` and `Ns::fn` are both nameable, which is
				// why no reader may classify a flow edge by scanning its Evidence
				// for a `>` or an `@`.
				raw := fn.Content(w.src)
				w.emitCallArgs(raw, fn, raw, child.ChildByFieldName("arguments"), 0)
			}
		case cppKindReturnStatement:
			if !inLambda {
				w.emitReturn(child)
			}
		case cppKindLambdaExpression:
			nested = true
		}
		walkCPPFlowSteps(w, child, nested)
	}
}

// rustFlowSteps is the rust arm.
func rustFlowSteps(declNode *sitter.Node, src []byte) []FlowStep {
	if declNode == nil {
		return nil
	}
	w := &nominalFlowWalker{
		classes: rustKinds(), prof: calleeProfileFor(LangRust), ident: rustKindIdentifier, src: src,
	}
	// THE self_parameter IS NOT A POSITION. seedParams counts only `parameter`
	// nodes, so `fn handle(&self, p: String)` gives p index 0 — which is the
	// position a CALLER writes it at, and the position a FLOWS_TO_ARG fact on a
	// call to this function would name.
	w.seedParams(declNode.ChildByFieldName("parameters"), rustKindParameter)
	walkRustFlowSteps(w, declNode, false)
	if len(w.steps) == 0 {
		return nil
	}
	return w.steps
}

// walkRustFlowSteps descends one rust declaration. Same numeric-symbol rule the
// C walk documents; inClosure suppresses returns only.
func walkRustFlowSteps(w *nominalFlowWalker, node *sitter.Node, inClosure bool) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		nested := inClosure
		switch w.classes.class(child.Symbol()) {
		case rustKindLetDeclaration:
			w.emitDefine(w.firstIdent(child.ChildByFieldName("pattern")),
				w.operands(child.ChildByFieldName("value"), nil))
		case rustKindAssignmentExpression:
			w.systemsAssign(child, rustKindIdentifier, rustKindFieldExpression, "value", "field")
		case rustKindCallExpression:
			fn := child.ChildByFieldName("function")
			if fn == nil {
				break
			}
			switch w.classes.class(fn.Symbol()) {
			case rustKindIdentifier, rustKindFieldExpression, rustKindScopedIdentifier:
				raw := fn.Content(w.src)
				w.emitCallArgs(raw, fn, raw, child.ChildByFieldName("arguments"), 0)
			}
		case rustKindReturnExpression:
			if !inClosure {
				w.emitReturn(child)
			}
		case rustKindClosureExpression:
			nested = true
		}
		walkRustFlowSteps(w, child, nested)
	}
}

// systemsAssign records the two assignment shapes this family writes.
//
// baseField NAMES THE CHILD HOLDING THE QUALIFIED WRITE'S OBJECT, and the three
// grammars disagree on it: C and C++ spell a field_expression's object
// `argument:`, rust spells it `value:`. Passing the name keeps one rule with one
// spelling rather than three near-copies.
//
// THE RECEIVER KEYWORD IS `this` OR `self` depending on the language, and both
// are admitted here rather than parameterized: neither is a legal identifier for
// anything else in any of the three, so accepting both cannot misread one
// language's ordinary local as the other's receiver.
//
// A WRITE THROUGH ANY OTHER OBJECT EMITS NOTHING, because binding that owner
// needs a type lookup the chunker does not have.
func (w *nominalFlowWalker) systemsAssign(node *sitter.Node, identKind, fieldKind uint8, baseField, nameField string) {
	left, sources := node.ChildByFieldName("left"), w.operands(node.ChildByFieldName("right"), nil)
	if left == nil {
		return
	}
	switch w.classes.class(left.Symbol()) {
	case identKind:
		w.emitAssign(left, sources)
	case fieldKind:
		if base := left.ChildByFieldName(baseField); isReceiverKeyword(base, w.src) {
			w.emitFieldWrite(left.ChildByFieldName(nameField), sources)
		}
	}
}

// isReceiverKeyword reports whether a node is `this` or `self`.
func isReceiverKeyword(node *sitter.Node, src []byte) bool {
	if node == nil {
		return false
	}
	switch node.Content(src) {
	case "this", "self":
		return true
	}
	return false
}
