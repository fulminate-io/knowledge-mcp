// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterNominalFlowSteps()
}

// RegisterNominalFlowSteps installs the java, kotlin, scala, groovy and csharp
// flow-step arms. It is EXPORTED for the reason RegisterGoFlowSteps is: a test
// that takes an arm OUT must be able to RESTORE the production registration,
// and UnregisterFlowSteps DELETES the entry rather than parking it.
func RegisterNominalFlowSteps() {
	RegisterFlowSteps(LangJava, javaFlowSteps)
	RegisterFlowSteps(LangKotlin, kotlinFlowSteps)
	RegisterFlowSteps(LangScala, scalaFlowSteps)
	RegisterFlowSteps(LangGroovy, groovyFlowSteps)
	RegisterFlowSteps(LangCSharp, csharpFlowSteps)
}

// nominalFlowWalker accumulates one declaration's flow steps during a single
// descent.
//
// FIVE ARMS SHARE THIS CARRIER BUT NOT ONE WALK, and the split is a property of
// the grammars rather than a missed abstraction. These five languages express
// the SAME five flow shapes through five different node vocabularies — java
// composes a qualified callee from an `object` field and a `name` field, kotlin
// wraps arguments in value_argument, scala's return is a return_expression,
// groovy's is a bare `return` node, and C# has no named `this` node at all — so
// a single switch would be a table of five special cases wearing one name. What
// IS shared is everything below the grammar: operand collection, parameter
// position accounting, callee normalization, and the emission itself.
type nominalFlowWalker struct {
	// classes and prof are HOISTED for the reason qualBinder.classes documents:
	// the walk classifies a node with a struct-field read plus one array index,
	// and the profile is a map read whose answer is identical for every call
	// site in the declaration.
	classes symbolClasses
	prof    calleeProfile

	// ident is the kind whose TEXT names a binding in this grammar — kotlin
	// spells it simple_identifier where the other four spell it identifier.
	ident uint8

	src   []byte
	steps []FlowStep
}

// operands collects the identifier nodes an expression READS, appending to out.
//
// THE DESCENT STOPS AT THE FIRST IDENTIFIER IT FINDS, which is what keeps a
// qualified read's FIELD from being collected: `obj.field` yields `obj` in the
// grammars where the field is a distinct kind, and in the ones where both halves
// are plain identifiers it yields both — over-approximating in the direction the
// whole design over-approximates.
func (w *nominalFlowWalker) operands(node *sitter.Node, out []*sitter.Node) []*sitter.Node {
	if node == nil {
		return out
	}
	if w.classes.class(node.Symbol()) == w.ident {
		return append(out, node)
	}
	for i := range int(node.NamedChildCount()) {
		out = w.operands(node.NamedChild(i), out)
	}
	return out
}

// firstIdent returns the first identifier node inside a subtree, or nil.
func (w *nominalFlowWalker) firstIdent(node *sitter.Node) *sitter.Node {
	if found := w.operands(node, nil); len(found) > 0 {
		return found[0]
	}
	return nil
}

// seedParams emits one StepParam per parameter POSITION of a parameter list.
//
// A PARAMETER WHOSE NAME THIS ARM CANNOT FIND STILL HOLDS ITS POSITION with a
// nil Target, because a dropped entry shifts every later position and silently
// rebinds them to the wrong parameter.
func (w *nominalFlowWalker) seedParams(list *sitter.Node, paramKind uint8) {
	if list == nil {
		return
	}
	idx := 0
	for i := range int(list.NamedChildCount()) {
		param := list.NamedChild(i)
		if w.classes.class(param.Symbol()) != paramKind {
			continue
		}
		name := param.ChildByFieldName("name")
		if name == nil || w.classes.class(name.Symbol()) != w.ident {
			name = w.firstIdent(param)
		}
		w.steps = append(w.steps, FlowStep{Kind: StepParam, Target: name, Index: idx})
		idx++
	}
}

// emitCallArgs normalizes a callee span and emits one StepCallArg per argument
// position that reads something.
//
// THE CALLEE GOES THROUGH normalizeCallee WITHOUT EXCEPTION. All five of these
// languages rewrite or decline a spelling before a CALLS edge carries it, so a
// hand-derived spelling would bind a different declaration than the sibling
// CALLS edge does.
//
// argWrapper unwraps the grammars that box an argument — kotlin's value_argument
// and C#'s argument — and is 0 where the arguments are the expressions
// themselves.
func (w *nominalFlowWalker) emitCallArgs(
	raw string, calleeNode *sitter.Node, lastCapture string, args *sitter.Node, argWrapper uint8,
) {
	if raw == "" || calleeNode == nil || args == nil {
		return
	}
	callee, emit := normalizeCallee(raw, w.prof, lastCapture, calleeNode, calleeNode.StartByte())
	if !emit {
		return
	}
	for i := range int(args.NamedChildCount()) {
		arg := args.NamedChild(i)
		if argWrapper != 0 && w.classes.class(arg.Symbol()) != argWrapper {
			continue
		}
		sources := w.operands(arg, nil)
		if len(sources) == 0 {
			continue
		}
		w.steps = append(w.steps, FlowStep{Kind: StepCallArg, Callee: callee, Index: i, Sources: sources})
	}
}

// emitDefine records a binding whose target is one identifier.
func (w *nominalFlowWalker) emitDefine(target *sitter.Node, sources []*sitter.Node) {
	if target == nil {
		return
	}
	w.steps = append(w.steps, FlowStep{Kind: StepDefine, Target: target, Sources: sources})
}

// emitAssign records a rebind of a plain local.
func (w *nominalFlowWalker) emitAssign(target *sitter.Node, sources []*sitter.Node) {
	if target == nil {
		return
	}
	w.steps = append(w.steps, FlowStep{Kind: StepAssign, Target: target, Sources: sources})
}

// emitFieldWrite records a write into a field ON THE RECEIVER.
//
// A WRITE THROUGH ANY OTHER OBJECT EMITS NOTHING, in every one of these five
// languages, because binding that owner needs a type lookup the chunker does not
// have — the same decline-by-default rule the Go arm applies to a selector on a
// non-receiver operand.
func (w *nominalFlowWalker) emitFieldWrite(field *sitter.Node, sources []*sitter.Node) {
	if field == nil {
		return
	}
	w.steps = append(w.steps, FlowStep{
		Kind: StepAssign, Sources: sources, Field: field.Content(w.src), Receiver: true,
	})
}

// emitReturn records a value occupying result position 0.
//
// ALL FIVE LANGUAGES RETURN ONE VALUE, so the result index is always 0 — a
// returned tuple or collection is one value whose shape this layer does not
// decompose.
func (w *nominalFlowWalker) emitReturn(node *sitter.Node) {
	if node == nil || node.NamedChildCount() == 0 {
		return
	}
	sources := w.operands(node.NamedChild(0), nil)
	if len(sources) == 0 {
		return
	}
	w.steps = append(w.steps, FlowStep{Kind: StepReturn, Index: 0, Sources: sources})
}

// firstChildOfKind returns a node's first DIRECT named child of one kind class.
//
// DIRECT CHILDREN ONLY, never a descent: a descent looking for a parameter list
// would find a nested lambda's list and seed ITS parameters as the
// declaration's own.
func (w *nominalFlowWalker) firstChildOfKind(node *sitter.Node, kind uint8) *sitter.Node {
	for i := range int(node.NamedChildCount()) {
		if child := node.NamedChild(i); w.classes.class(child.Symbol()) == kind {
			return child
		}
	}
	return nil
}

// isThisText reports whether a node is the receiver keyword.
//
// IT IS A TEXT COMPARISON RATHER THAN A KIND COMPARISON because C# HAS NO NAMED
// this NODE AT ALL — measured against the vendored grammar, `this_expression`
// carries zero regular symbol ids there, so its `this` arrives as an anonymous
// token inside a member_access_expression. One bounded text read serves all five
// grammars where five kind constants would serve four.
func isThisText(node *sitter.Node, src []byte) bool {
	return node != nil && node.Content(src) == "this"
}

// javaFlowSteps is the java arm.
func javaFlowSteps(declNode *sitter.Node, src []byte) []FlowStep {
	if declNode == nil {
		return nil
	}
	w := &nominalFlowWalker{
		classes: javaKinds(), prof: calleeProfileFor(LangJava), ident: javaKindIdentifier, src: src,
	}
	w.seedParams(w.firstChildOfKind(declNode, javaKindFormalParameters), javaKindFormalParameter)
	walkJavaFlowSteps(w, declNode, false)
	if len(w.steps) == 0 {
		return nil
	}
	return w.steps
}

// walkJavaFlowSteps descends one java declaration.
//
// THE SWITCH IS ON THE NUMERIC SYMBOL, NOT ON THE KIND NAME, and that is the
// measured cost of this function: reading a node's kind name converts a cgo
// C-string into a FRESH Go string on every call, so a recursive walk that names
// every named node at every depth allocates once per node visited.
//
// inLambda SUPPRESSES RETURNS ONLY: a lambda's `return` belongs to the lambda,
// not to the enclosing method, while its calls and assignments do belong to the
// enclosing declaration's dataflow.
func walkJavaFlowSteps(w *nominalFlowWalker, node *sitter.Node, inLambda bool) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		nested := inLambda
		switch w.classes.class(child.Symbol()) {
		case javaKindLocalVariableDeclaration:
			for j := range int(child.NamedChildCount()) {
				decl := child.NamedChild(j)
				if w.classes.class(decl.Symbol()) != javaKindVariableDeclarator {
					continue
				}
				w.emitDefine(decl.ChildByFieldName("name"), w.operands(decl.ChildByFieldName("value"), nil))
			}
		case javaKindAssignmentExpression:
			left, sources := child.ChildByFieldName("left"), w.operands(child.ChildByFieldName("right"), nil)
			if left == nil {
				break
			}
			switch w.classes.class(left.Symbol()) {
			case javaKindIdentifier:
				w.emitAssign(left, sources)
			case javaKindFieldAccess:
				if isThisText(left.ChildByFieldName("object"), w.src) {
					w.emitFieldWrite(left.ChildByFieldName("field"), sources)
				}
			}
		case javaKindMethodInvocation:
			// THE SPAN IS COMPOSED FROM TWO CAPTURES, matching the Calls query:
			// java binds `object:` and `name:` separately, and extractCallEdges
			// composes the source span from the first to the last of them. An
			// unqualified call binds `name` alone.
			name, args := child.ChildByFieldName("name"), child.ChildByFieldName("arguments")
			if name == nil {
				break
			}
			if object := child.ChildByFieldName("object"); object != nil {
				w.emitCallArgs(string(w.src[object.StartByte():name.EndByte()]),
					object, name.Content(w.src), args, 0)
				break
			}
			w.emitCallArgs(name.Content(w.src), name, name.Content(w.src), args, 0)
		case javaKindReturnStatement:
			if !inLambda {
				w.emitReturn(child)
			}
		case javaKindLambdaExpression:
			nested = true
		}
		walkJavaFlowSteps(w, child, nested)
	}
}

// kotlinFlowSteps is the kotlin arm.
func kotlinFlowSteps(declNode *sitter.Node, src []byte) []FlowStep {
	if declNode == nil {
		return nil
	}
	w := &nominalFlowWalker{
		classes: kotlinKinds(), prof: calleeProfileFor(LangKotlin),
		ident: kotlinKindSimpleIdentifier, src: src,
	}
	w.seedParams(w.firstChildOfKind(declNode, kotlinKindFunctionValueParameters), kotlinKindParameter)
	walkKotlinFlowSteps(w, declNode, false)
	if len(w.steps) == 0 {
		return nil
	}
	return w.steps
}

// walkKotlinFlowSteps descends one kotlin declaration. Same numeric-symbol and
// return-suppression rules the java walk documents.
func walkKotlinFlowSteps(w *nominalFlowWalker, node *sitter.Node, inLambda bool) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		nested := inLambda
		switch w.classes.class(child.Symbol()) {
		case kotlinKindPropertyDeclaration:
			// A property_declaration holds its binding_pattern_kind, its
			// variable_declaration and its VALUE as flat siblings, so the value is
			// every named child that is neither of the first two.
			binding := w.firstChildOfKind(child, kotlinKindVariableDeclaration)
			if binding == nil {
				break
			}
			var sources []*sitter.Node
			for j := range int(child.NamedChildCount()) {
				sib := child.NamedChild(j)
				switch w.classes.class(sib.Symbol()) {
				case kotlinKindVariableDeclaration, kotlinKindBindingPatternKind:
					continue
				}
				sources = w.operands(sib, sources)
			}
			w.emitDefine(w.firstIdent(binding), sources)
		case kotlinKindAssignment:
			if child.NamedChildCount() < 2 {
				break
			}
			target, sources := child.NamedChild(0), w.operands(child.NamedChild(1), nil)
			switch w.classes.class(target.Symbol()) {
			case kotlinKindSimpleIdentifier:
				w.emitAssign(target, sources)
			case kotlinKindDirectlyAssignableExpression:
				suffix := w.firstChildOfKind(target, kotlinKindNavigationSuffix)
				if suffix != nil && isThisText(target.NamedChild(0), w.src) {
					w.emitFieldWrite(w.firstIdent(suffix), sources)
				}
			}
		case kotlinKindCallExpression:
			w.kotlinCall(child)
		case kotlinKindJumpExpression:
			if !inLambda {
				w.emitReturn(child)
			}
		case kotlinKindLambdaLiteral, kotlinKindAnonymousFunction:
			nested = true
		}
		walkKotlinFlowSteps(w, child, nested)
	}
}

// kotlinCall emits one kotlin call's argument steps.
//
// KOTLIN'S CALLEE IS THE CALL'S FIRST CHILD rather than a named field, and its
// arguments sit two levels down under a call_suffix — which is also where a
// TRAILING LAMBDA lands. A call written with a trailing lambda and no
// parenthesized arguments therefore has no value_arguments node at all and
// emits nothing, which is correct: it passes no argument this arm can position.
func (w *nominalFlowWalker) kotlinCall(call *sitter.Node) {
	if call.NamedChildCount() == 0 {
		return
	}
	callee := call.NamedChild(0)
	switch w.classes.class(callee.Symbol()) {
	case kotlinKindSimpleIdentifier, kotlinKindNavigationExpression:
	default:
		return
	}
	suffix := w.firstChildOfKind(call, kotlinKindCallSuffix)
	if suffix == nil {
		return
	}
	raw := callee.Content(w.src)
	w.emitCallArgs(raw, callee, raw,
		w.firstChildOfKind(suffix, kotlinKindValueArguments), kotlinKindValueArgument)
}
