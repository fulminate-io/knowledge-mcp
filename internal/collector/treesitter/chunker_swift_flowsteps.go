// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterSwiftFlowSteps()
}

// RegisterSwiftFlowSteps installs the swift flow-step arm. It is EXPORTED for
// the reason RegisterGoFlowSteps is: a test that takes the arm OUT must be able
// to RESTORE the production registration, and UnregisterFlowSteps DELETES the
// entry rather than parking it.
func RegisterSwiftFlowSteps() {
	RegisterFlowSteps(LangSwift, swiftFlowSteps)
}

// swiftFlowSteps is the swift arm.
//
// SWIFT IS ITS OWN ARM RATHER THAN A MEMBER OF THE NOMINAL-STATIC FAMILY, for
// the reason its qualifier arm is separate: one node kind serves class, struct,
// enum, actor and extension, and its declaration shapes behave unlike the JVM
// family's. It reuses the shared nominalFlowWalker carrier and every emitter on
// it, which is where the genuine commonality is.
//
// ITS PARAMETERS ARE DIRECT CHILDREN OF THE DECLARATION, not children of a
// parameter-list node, which is why the declaration itself is handed to the
// seeder. A nested closure's parameters are `lambda_parameter` nodes and a
// different kind entirely, so they cannot leak into the declaration's own
// positions.
func swiftFlowSteps(declNode *sitter.Node, src []byte) []FlowStep {
	if declNode == nil {
		return nil
	}
	w := &nominalFlowWalker{
		classes: swiftKinds(), prof: calleeProfileFor(LangSwift),
		ident: swiftKindSimpleIdentifier, src: src,
	}
	w.seedParams(declNode, swiftKindParameter)
	walkSwiftFlowSteps(w, declNode, false)
	if len(w.steps) == 0 {
		return nil
	}
	return w.steps
}

// walkSwiftFlowSteps descends one swift declaration.
//
// THE SWITCH IS ON THE NUMERIC SYMBOL, NOT ON THE KIND NAME, and that is the
// measured cost of this function: reading a node's kind name converts a cgo
// C-string into a FRESH Go string on every call, so a recursive walk that names
// every named node at every depth allocates once per node visited.
//
// inClosure SUPPRESSES RETURNS ONLY: a closure's `return` belongs to the
// closure, while its calls and assignments belong to the enclosing
// declaration's dataflow.
func walkSwiftFlowSteps(w *nominalFlowWalker, node *sitter.Node, inClosure bool) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		nested := inClosure
		switch w.classes.class(child.Symbol()) {
		case swiftKindPropertyDeclaration:
			// `let a = p` and `var a = p` are one kind. The bound name sits inside
			// a `pattern` wrapper and the initializer under the `value:` field.
			w.emitDefine(w.firstIdent(child.ChildByFieldName("name")),
				w.operands(child.ChildByFieldName("value"), nil))
		case swiftKindAssignment:
			w.swiftAssign(child)
		case swiftKindCallExpression:
			w.swiftCall(child)
		case swiftKindControlTransferStatement:
			// `return`, `break`, `continue` and `throw` share one node kind. Only a
			// form carrying a RESULT contributes a flow, and emitReturn's own
			// empty-operands guard is what declines the rest.
			if !inClosure {
				w.emitReturn(child)
			}
		case swiftKindLambdaLiteral:
			nested = true
		}
		walkSwiftFlowSteps(w, child, nested)
	}
}

// swiftAssign records swift's two assignment shapes.
//
// THE TARGET IS ALWAYS WRAPPED in a directly_assignable_expression, so both the
// plain local rebind and the self-qualified field write are reached by unwrapping
// it first. A write through any object OTHER than self emits nothing, because
// binding that owner needs a type lookup the chunker does not have.
func (w *nominalFlowWalker) swiftAssign(node *sitter.Node) {
	target, sources := node.ChildByFieldName("target"), w.operands(node.ChildByFieldName("result"), nil)
	if target == nil {
		return
	}
	if w.classes.class(target.Symbol()) == swiftKindDirectlyAssignableExpression {
		if target.NamedChildCount() == 0 {
			return
		}
		target = target.NamedChild(0)
	}
	switch w.classes.class(target.Symbol()) {
	case swiftKindSimpleIdentifier:
		w.emitAssign(target, sources)
	case swiftKindNavigationExpression:
		if w.classes.class(target.ChildByFieldName("target").Symbol()) != swiftKindSelfExpression {
			return
		}
		w.emitFieldWrite(w.firstIdent(target.ChildByFieldName("suffix")), sources)
	}
}

// swiftCall emits one swift call's argument steps.
//
// A LABELED ARGUMENT STILL OCCUPIES AN ORDINAL POSITION, and the ordinal is
// what Evidence carries: `f(label: p)` records argument 0, exactly as `f(p)`
// does. Swift's labels are part of the callee's declared signature rather than a
// reordering, so the position is the honest carrier and the label adds nothing a
// consumer could act on.
//
// THE CALLEE GOES THROUGH normalizeCallee WITHOUT EXCEPTION. Swift's profile is
// {ChainOps:"?!", DeclineNonName:true} and carries NO ElideLiteralBodies
// deliberately, so a trailing-closure receiver is DECLINED rather than repaired
// — which is truthful, where eliding would fabricate a qualifier.
func (w *nominalFlowWalker) swiftCall(call *sitter.Node) {
	if call.NamedChildCount() == 0 {
		return
	}
	callee := call.NamedChild(0)
	switch w.classes.class(callee.Symbol()) {
	case swiftKindSimpleIdentifier, swiftKindNavigationExpression:
	default:
		return
	}
	suffix := w.firstChildOfKind(call, swiftKindCallSuffix)
	if suffix == nil {
		return
	}
	raw := callee.Content(w.src)
	w.emitCallArgs(raw, callee, raw,
		w.firstChildOfKind(suffix, swiftKindValueArguments), swiftKindValueArgument)
}
