// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterDynamicFlowSteps()
}

// RegisterDynamicFlowSteps installs the python and ruby flow-step arms. It is
// EXPORTED for the reason RegisterGoFlowSteps is: a test that takes an arm OUT
// must be able to RESTORE the production registration, and UnregisterFlowSteps
// DELETES the entry rather than parking it.
func RegisterDynamicFlowSteps() {
	RegisterFlowSteps(LangPython, pythonFlowSteps)
	RegisterFlowSteps(LangRuby, rubyFlowSteps)
}

// SELF IS THE RECEIVER, NOT PARAMETER ZERO, and that is the whole reason this
// family is its own phase.
//
// Python writes the receiver as an ORDINARY FIRST PARAMETER, so an arm that
// counted positions naively would give the first real argument index 1 and every
// FLOWS_TO_ARG fact about a method would name the wrong position. This arm emits
// `self` (and `cls`) as StepParam with Receiver set and NO position, so the
// declaration's first real parameter takes index 0 — the position a CALLER
// writes it at. A self-qualified field write then renders `flow:recv>f:cache`,
// BYTE-IDENTICAL to what a Go method's receiver-field write renders.
//
// Ruby has no receiver parameter to skip, so its positions are already right;
// its receiver appears only on the WRITE side, as `self.x =` or as an instance
// variable.

// pythonFlowSteps is the python arm.
func pythonFlowSteps(declNode *sitter.Node, src []byte) []FlowStep {
	if declNode == nil {
		return nil
	}
	w := &nominalFlowWalker{
		classes: pyKinds(), prof: calleeProfileFor(LangPython), ident: pyKindIdentifier, src: src,
	}
	w.seedPythonParams(declNode.ChildByFieldName("parameters"))
	walkPythonFlowSteps(w, declNode, false)
	if len(w.steps) == 0 {
		return nil
	}
	return w.steps
}

// seedPythonParams emits one StepParam per parameter, with the receiver
// separated out.
//
// EVERY BINDING FORM THE PYTHON QUALIFIER ARM COVERS IS COVERED HERE — plain
// identifier, typed_parameter, default_parameter, typed_default_parameter, and
// the splat forms — because a form that arm binds and this one drops is a silent
// per-form hole rather than a visible failure.
//
// THE KEYWORD SEPARATOR IS NOT A PARAMETER. A bare `*` in a parameter list marks
// the start of the keyword-only section and binds no name, so it must not
// consume a position: without this case `def f(a, *, b)` would give b index 2.
func (w *nominalFlowWalker) seedPythonParams(list *sitter.Node) {
	if list == nil {
		return
	}
	idx, first := 0, true
	for i := range int(list.NamedChildCount()) {
		param := list.NamedChild(i)
		switch w.classes.class(param.Symbol()) {
		case pyKindIdentifier, pyKindTypedParameter, pyKindDefaultParameter,
			pyKindTypedDefaultParameter, pyKindListSplatPattern, pyKindDictionarySplatPattern:
		default:
			continue
		}
		name := w.firstIdent(param)
		if first {
			first = false
			if name != nil && isPythonReceiverName(name.Content(w.src)) {
				w.steps = append(w.steps, FlowStep{Kind: StepParam, Target: name, Receiver: true})
				continue
			}
		}
		w.steps = append(w.steps, FlowStep{Kind: StepParam, Target: name, Index: idx})
		idx++
	}
}

// isPythonReceiverName reports whether a first parameter's name is the
// convention for a receiver.
//
// IT IS A NAME CONVENTION RATHER THAN A GRAMMAR FACT, because python has no
// grammar-level receiver: a module-level `def f(x)` and a method `def m(self)`
// have identical parameter lists. Only the two conventional spellings are
// admitted, so a module-level function whose first parameter happens to be named
// something else keeps index 0.
func isPythonReceiverName(name string) bool {
	return name == "self" || name == "cls"
}

// walkPythonFlowSteps descends one python declaration.
//
// THE SWITCH IS ON THE NUMERIC SYMBOL, NOT ON THE KIND NAME, and that is the
// measured cost of this function: reading a node's kind name converts a cgo
// C-string into a FRESH Go string on every call, so a recursive walk that names
// every named node at every depth allocates once per node visited.
//
// inLambda SUPPRESSES RETURNS ONLY.
func walkPythonFlowSteps(w *nominalFlowWalker, node *sitter.Node, inLambda bool) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		nested := inLambda
		switch w.classes.class(child.Symbol()) {
		case pyKindAssignment:
			// PYTHON HAS ONE ASSIGNMENT NODE FOR BOTH THE DEFINE AND THE REBIND,
			// which is correct rather than lossy: the closure engine treats
			// StepDefine and StepAssign identically apart from naming, and python
			// genuinely does not distinguish a first binding from a later one.
			left, sources := child.ChildByFieldName("left"), w.operands(child.ChildByFieldName("right"), nil)
			if left == nil {
				break
			}
			switch w.classes.class(left.Symbol()) {
			case pyKindIdentifier:
				w.emitDefine(left, sources)
			case pyKindAttribute:
				if object := left.ChildByFieldName("object"); isReceiverKeyword(object, w.src) {
					w.emitFieldWrite(left.ChildByFieldName("attribute"), sources)
				}
			}
		case pyKindCall:
			fn := child.ChildByFieldName("function")
			if fn == nil {
				break
			}
			switch w.classes.class(fn.Symbol()) {
			case pyKindIdentifier, pyKindAttribute:
				raw := fn.Content(w.src)
				w.emitCallArgs(raw, fn, raw, child.ChildByFieldName("arguments"), 0)
			}
		case pyKindReturnStatement:
			if !inLambda {
				w.emitReturn(child)
			}
		case pyKindLambda:
			nested = true
		}
		walkPythonFlowSteps(w, child, nested)
	}
}

// rubyFlowSteps is the ruby arm.
func rubyFlowSteps(declNode *sitter.Node, src []byte) []FlowStep {
	if declNode == nil {
		return nil
	}
	w := &nominalFlowWalker{
		classes: rbKinds(), prof: calleeProfileFor(LangRuby), ident: rbKindIdentifier, src: src,
	}
	// Ruby's receiver is implicit, so every declared parameter is positional and
	// the shared seeder is exactly right.
	w.seedParams(declNode.ChildByFieldName("parameters"), rbKindIdentifier)
	walkRubyFlowSteps(w, declNode)
	if len(w.steps) == 0 {
		return nil
	}
	return w.steps
}

// walkRubyFlowSteps descends one ruby declaration.
//
// THERE IS NO NESTED-SCOPE SUPPRESSION. Ruby's `return` inside a block returns
// from the ENCLOSING METHOD rather than from the block, so a return seen at any
// depth genuinely occupies the method's result position.
func walkRubyFlowSteps(w *nominalFlowWalker, node *sitter.Node) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		switch w.classes.class(child.Symbol()) {
		case rbKindAssignment:
			w.rubyAssign(child)
		case rbKindCall:
			w.rubyCall(child)
		case rbKindReturn:
			w.emitReturn(child)
		}
		walkRubyFlowSteps(w, child)
	}
}

// rubyAssign records ruby's three assignment shapes.
//
// RUBY WRITES A RECEIVER FIELD TWO WAYS and both record: an INSTANCE VARIABLE
// (`@cache = p`) and an explicit self-qualified setter (`self.cache = p`, which
// the grammar parses as a `call` on the left). An arm covering only one of them
// would miss whichever half a codebase happens to prefer.
//
// THE FIELD NAME IS RECORDED AS WRITTEN, `@` sigil included. No identifier
// normalization happens here or downstream — the same rule that keeps PHP's `$`
// sigil intact — so a consumer reads the spelling the source used rather than a
// guess at what it would be called without it.
func (w *nominalFlowWalker) rubyAssign(node *sitter.Node) {
	left, sources := node.ChildByFieldName("left"), w.operands(node.ChildByFieldName("right"), nil)
	if left == nil {
		return
	}
	switch w.classes.class(left.Symbol()) {
	case rbKindIdentifier:
		w.emitDefine(left, sources)
	case rbKindInstanceVariable:
		w.emitFieldWrite(left, sources)
	case rbKindCall:
		if isReceiverKeyword(left.ChildByFieldName("receiver"), w.src) {
			w.emitFieldWrite(left.ChildByFieldName("method"), sources)
		}
	}
}

// rubyCall emits one ruby call's argument steps.
//
// THE SPAN IS COMPOSED FROM TWO CAPTURES, matching the Calls query: ruby binds
// `receiver:` and `method:` separately, and extractCallEdges composes the source
// span from the first to the last kept capture. THAT IS WHY `@logger.info(p)`
// EMITS THE SPELLING `@logger.info` — the `@` is an admitted callee-name
// character in every language, so the composed span survives ruby's own decline
// rules and reaches the graph intact.
//
// It is the case that forced the unresolved-callee test to be STRUCTURAL rather
// than a scan of Evidence for an `@`: this is an ORDINARY RESOLVED callee whose
// spelling carries one.
func (w *nominalFlowWalker) rubyCall(call *sitter.Node) {
	method, args := call.ChildByFieldName("method"), call.ChildByFieldName("arguments")
	if method == nil {
		return
	}
	if receiver := call.ChildByFieldName("receiver"); receiver != nil {
		w.emitCallArgs(string(w.src[receiver.StartByte():method.EndByte()]),
			receiver, method.Content(w.src), args, 0)
		return
	}
	w.emitCallArgs(method.Content(w.src), method, method.Content(w.src), args, 0)
}
