// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// The scala, groovy and csharp halves of the nominal-static family. They live
// beside chunker_nominal_flowsteps.go rather than in it for the 500-line file
// cap, and share that file's nominalFlowWalker carrier and every emitter on it.

// scalaFlowSteps is the scala arm.
func scalaFlowSteps(declNode *sitter.Node, src []byte) []FlowStep {
	if declNode == nil {
		return nil
	}
	w := &nominalFlowWalker{
		classes: scalaKinds(), prof: calleeProfileFor(LangScala), ident: scalaKindIdentifier, src: src,
	}
	w.seedParams(w.firstChildOfKind(declNode, scalaKindParameters), scalaKindParameter)
	walkScalaFlowSteps(w, declNode, false)
	if len(w.steps) == 0 {
		return nil
	}
	return w.steps
}

// walkScalaFlowSteps descends one scala declaration. Same numeric-symbol and
// return-suppression rules the java walk documents.
//
// ONLY AN EXPLICIT `return` RECORDS A RESULT FLOW. Scala's idiomatic form is the
// block's last expression, which is not distinguishable at this layer from any
// other statement — reading it as a return would need the enclosing block's own
// position, and getting that wrong records a fact the source does not state.
// The absence is covered by the absence-is-not-proof contract on the edge types.
func walkScalaFlowSteps(w *nominalFlowWalker, node *sitter.Node, inLambda bool) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		nested := inLambda
		switch w.classes.class(child.Symbol()) {
		case scalaKindValDefinition, scalaKindVarDefinition:
			w.emitDefine(w.firstIdent(child.ChildByFieldName("pattern")),
				w.operands(child.ChildByFieldName("value"), nil))
		case scalaKindAssignmentExpression:
			left, sources := child.ChildByFieldName("left"), w.operands(child.ChildByFieldName("right"), nil)
			if left == nil {
				break
			}
			switch w.classes.class(left.Symbol()) {
			case scalaKindIdentifier:
				w.emitAssign(left, sources)
			case scalaKindFieldExpression:
				if isThisText(left.ChildByFieldName("value"), w.src) {
					w.emitFieldWrite(left.ChildByFieldName("field"), sources)
				}
			}
		case scalaKindCallExpression:
			fn := child.ChildByFieldName("function")
			if fn == nil {
				break
			}
			switch w.classes.class(fn.Symbol()) {
			case scalaKindIdentifier, scalaKindFieldExpression:
				raw := fn.Content(w.src)
				w.emitCallArgs(raw, fn, raw, child.ChildByFieldName("arguments"), 0)
			}
		case scalaKindReturnExpression:
			if !inLambda {
				w.emitReturn(child)
			}
		case scalaKindLambdaExpression:
			nested = true
		}
		walkScalaFlowSteps(w, child, nested)
	}
}

// groovyFlowSteps is the groovy arm.
//
// THE GROOVY GRAMMAR DECLINES SHAPES IT CANNOT PARSE, and this arm declines with
// it: where the parse produced an ERROR node the surrounding statement is not
// walked into, because a recovered parse cannot say which operand was which and
// a wrong step produces a wrong fact. A thin result on groovy is the grammar's
// boundary rather than this arm's breakage. Measured example: `this.cache = p`
// parses to an ERROR node beside a bare `cache = p` assignment, so groovy
// records no receiver-field write at all.
func groovyFlowSteps(declNode *sitter.Node, src []byte) []FlowStep {
	if declNode == nil {
		return nil
	}
	w := &nominalFlowWalker{
		classes: groovyKinds(), prof: calleeProfileFor(LangGroovy), ident: groovyKindIdentifier, src: src,
	}
	w.seedParams(w.firstChildOfKind(declNode, groovyKindParameterList), groovyKindParameter)
	walkGroovyFlowSteps(w, declNode)
	if len(w.steps) == 0 {
		return nil
	}
	return w.steps
}

// walkGroovyFlowSteps descends one groovy declaration.
//
// THERE IS NO NESTED-SCOPE SUPPRESSION HERE, and its absence is deliberate
// rather than an omission: groovy spells a method's own BODY as a `closure`, the
// same kind it spells an inline closure with, so suppressing returns inside a
// closure would suppress every method's own return. Groovy is the one language
// in this family where the body and the lambda share a node kind.
func walkGroovyFlowSteps(w *nominalFlowWalker, node *sitter.Node) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		switch w.classes.class(child.Symbol()) {
		case groovyKindDeclaration:
			// A declaration holds its bound name first and its value after, both
			// as flat named children.
			if child.NamedChildCount() == 0 {
				break
			}
			var sources []*sitter.Node
			for j := 1; j < int(child.NamedChildCount()); j++ {
				sources = w.operands(child.NamedChild(j), sources)
			}
			w.emitDefine(w.firstIdent(child.NamedChild(0)), sources)
		case groovyKindAssignment:
			if child.NamedChildCount() < 2 {
				break
			}
			w.emitAssign(child.NamedChild(0), w.operands(child.NamedChild(1), nil))
		case groovyKindFunctionCall, groovyKindJuxtFunctionCall:
			fn := child.ChildByFieldName("function")
			if fn == nil {
				break
			}
			switch w.classes.class(fn.Symbol()) {
			case groovyKindIdentifier, groovyKindDottedIdentifier:
				raw := fn.Content(w.src)
				w.emitCallArgs(raw, fn, raw, w.firstChildOfKind(child, groovyKindArgumentList), 0)
			}
		case groovyKindReturn:
			w.emitReturn(child)
		}
		walkGroovyFlowSteps(w, child)
	}
}

// csharpFlowSteps is the csharp arm.
func csharpFlowSteps(declNode *sitter.Node, src []byte) []FlowStep {
	if declNode == nil {
		return nil
	}
	w := &nominalFlowWalker{
		classes: csharpKinds(), prof: calleeProfileFor(LangCSharp), ident: csharpKindIdentifier, src: src,
	}
	w.seedParams(w.firstChildOfKind(declNode, csharpKindParameterList), csharpKindParameter)
	walkCSharpFlowSteps(w, declNode, false)
	if len(w.steps) == 0 {
		return nil
	}
	return w.steps
}

// walkCSharpFlowSteps descends one csharp declaration. Same numeric-symbol and
// return-suppression rules the java walk documents.
func walkCSharpFlowSteps(w *nominalFlowWalker, node *sitter.Node, inLambda bool) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		nested := inLambda
		switch w.classes.class(child.Symbol()) {
		case csharpKindVariableDeclarator:
			// The declarator holds its name in a field and its initializer as the
			// remaining named children, so the sources are everything but the name.
			name := child.ChildByFieldName("name")
			var sources []*sitter.Node
			for j := range int(child.NamedChildCount()) {
				if sib := child.NamedChild(j); sib != name {
					sources = w.operands(sib, sources)
				}
			}
			w.emitDefine(name, sources)
		case csharpKindAssignmentExpression:
			left, sources := child.ChildByFieldName("left"), w.operands(child.ChildByFieldName("right"), nil)
			if left == nil {
				break
			}
			switch w.classes.class(left.Symbol()) {
			case csharpKindIdentifier:
				w.emitAssign(left, sources)
			case csharpKindMemberAccessExpression:
				// C# HAS NO NAMED `this` NODE, so the receiver arrives as an
				// anonymous token in the expression field and is recognized by text.
				if isThisText(left.ChildByFieldName("expression"), w.src) {
					w.emitFieldWrite(left.ChildByFieldName("name"), sources)
				}
			}
		case csharpKindInvocationExpression:
			fn := child.ChildByFieldName("function")
			if fn == nil {
				break
			}
			switch w.classes.class(fn.Symbol()) {
			case csharpKindIdentifier, csharpKindMemberAccessExpression:
				raw := fn.Content(w.src)
				w.emitCallArgs(raw, fn, raw, child.ChildByFieldName("arguments"), csharpKindArgument)
			}
		case csharpKindReturnStatement:
			if !inLambda {
				w.emitReturn(child)
			}
		case csharpKindLambdaExpression:
			nested = true
		}
		walkCSharpFlowSteps(w, child, nested)
	}
}
