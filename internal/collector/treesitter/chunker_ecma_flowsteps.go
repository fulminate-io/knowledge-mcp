// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterECMAFlowSteps()
}

// RegisterECMAFlowSteps installs the typescript, tsx and javascript flow-step
// arms. It is EXPORTED for the reason RegisterGoFlowSteps is: a test that takes
// an arm OUT must be able to RESTORE the production registration, and
// UnregisterFlowSteps DELETES the entry rather than parking it.
func RegisterECMAFlowSteps() {
	RegisterFlowSteps(LangTypeScript, tsFlowSteps)
	RegisterFlowSteps(LangTSX, tsxFlowSteps)
	RegisterFlowSteps(LangJavaScript, jsFlowSteps)
}

// The three arms. Each is a thin wrapper passing its OWN grammar's class table
// and its OWN Language into one shared body — the grammars differ in symbol
// NUMBERING even where they share a kind name (typescript declares 384 symbols
// against tsx's 404), so a shared table would classify one grammar's nodes
// wrongly rather than not at all. The Language is passed because the callee
// profile is per-language.
func tsFlowSteps(declNode *sitter.Node, src []byte) []FlowStep {
	return ecmaFlowSteps(tsKinds(), LangTypeScript, declNode, src)
}

func tsxFlowSteps(declNode *sitter.Node, src []byte) []FlowStep {
	return ecmaFlowSteps(tsxKinds(), LangTSX, declNode, src)
}

func jsFlowSteps(declNode *sitter.Node, src []byte) []FlowStep {
	return ecmaFlowSteps(jsKinds(), LangJavaScript, declNode, src)
}

// ecmaFlowWalker accumulates one declaration's flow steps during a single
// descent. classes and prof are HOISTED for the reason qualBinder.classes
// documents: the walk classifies a node with a struct-field read plus one array
// index, and the profile is a map read whose answer is identical for every call
// site in the declaration.
type ecmaFlowWalker struct {
	classes symbolClasses
	prof    calleeProfile
	src     []byte
	steps   []FlowStep
}

// ecmaFlowSteps is the shared body: one walk of a declaration's subtree,
// returning the flow steps its grammar shows IN SOURCE ORDER.
//
// THERE IS NO RECEIVER StepParam IN THIS FAMILY, and the asymmetry with the Go
// arm is a property of the languages rather than an omission. Go's receiver is a
// NAMED parameter, so it seeds the taint map under its own name; ECMAScript's
// `this` is a keyword with no declaration node to name, so nothing seeds it. A
// `this`-qualified FIELD WRITE still records, because the fact it produces is
// about the PARAMETER on the right-hand side — `this.cache = p` states that
// parameter zero reaches the field, which needs no taint on `this` at all.
func ecmaFlowSteps(classes symbolClasses, lang Language, declNode *sitter.Node, src []byte) []FlowStep {
	if declNode == nil {
		return nil
	}
	// The export unwrap comes FIRST, before anything reads a child. The TopLevel
	// query binds an exported declaration's @decl to the export_statement, so a
	// descent run against the raw node finds no parameters and no body on the
	// majority of real TypeScript.
	decl := unwrapExportedDecl(declNode)

	w := &ecmaFlowWalker{classes: classes, prof: calleeProfileFor(lang), src: src}
	w.seedParams(ecmaFormalParameters(classes, decl))
	walkECMAFlowSteps(w, decl, false)

	if len(w.steps) == 0 {
		return nil
	}
	return w.steps
}

// ecmaFormalParameters returns a declaration's own formal_parameters list, or
// nil.
//
// IT SEARCHES DIRECT NAMED CHILDREN ONLY, never a descent: a descent would find
// a nested arrow function's parameter list and seed ITS parameters as though
// they were the declaration's own.
func ecmaFormalParameters(classes symbolClasses, decl *sitter.Node) *sitter.Node {
	for i := range int(decl.NamedChildCount()) {
		if child := decl.NamedChild(i); classes.class(child.Symbol()) == ecmaKindFormalParameters {
			return child
		}
	}
	return nil
}

// seedParams emits one StepParam per parameter POSITION.
//
// TWO SHAPES BIND A NAME, and both must be covered or the coverage is
// per-grammar rather than per-family: javascript writes a bare identifier, while
// the typed grammars wrap it in a required_parameter or optional_parameter
// alongside its annotation. EVERY OTHER SHAPE — a destructuring pattern, a rest
// element, a default-valued assignment pattern — HOLDS ITS POSITION WITH A NIL
// Target rather than being skipped, because a dropped entry shifts every later
// position and silently rebinds them to the wrong parameter.
func (w *ecmaFlowWalker) seedParams(list *sitter.Node) {
	if list == nil {
		return
	}
	for i := range int(list.NamedChildCount()) {
		param := list.NamedChild(i)
		step := FlowStep{Kind: StepParam, Index: i}
		switch w.classes.class(param.Symbol()) {
		case ecmaKindIdentifier:
			step.Target = param
		case ecmaKindRequiredParameter, ecmaKindOptionalParameter:
			// THE NAME IS FOUND BY KIND, NEVER BY POSITION: a TypeScript
			// parameter property parses as (accessibility_modifier, identifier,
			// type_annotation), so a rule reading named child 0 binds the modifier
			// `private` as the parameter's name.
			step.Target = ecmaFirstChildOfClass(w.classes, param, ecmaKindIdentifier)
		}
		w.steps = append(w.steps, step)
	}
}

// walkECMAFlowSteps descends one declaration emitting the local syntax the
// closure engine reads.
//
// THE SWITCH IS ON THE NUMERIC SYMBOL, NOT ON THE KIND NAME, and that is the
// measured cost of this function: reading a node's kind name converts a cgo
// C-string into a fresh Go string on every call, so a recursive walk that names
// every named node at every depth allocates once per node visited.
//
// inLiteral SUPPRESSES RETURNS ONLY. A nested function's `return` belongs to
// that function, not to the enclosing declaration, so counting it would
// attribute an inner result position to the outer signature. Its assignments and
// calls DO belong to the enclosing declaration's dataflow and keep emitting at
// any depth. Arrow functions are included: an arrow body's `return` is the
// arrow's, not the enclosing method's.
func walkECMAFlowSteps(w *ecmaFlowWalker, node *sitter.Node, inLiteral bool) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		nested := inLiteral
		switch w.classes.class(child.Symbol()) {
		case ecmaKindVariableDeclarator:
			w.define(child)
		case ecmaKindAssignmentExpression:
			w.assign(child)
		case ecmaKindCallExpression:
			w.callArgs(child)
		case ecmaKindReturnStatement:
			if !inLiteral {
				w.returns(child)
			}
		case ecmaKindFunctionDeclaration, ecmaKindFunctionExpression, ecmaKindArrowFunction:
			nested = true
		}
		walkECMAFlowSteps(w, child, nested)
	}
}

// define emits a StepDefine for one `const`/`let`/`var` declarator. A declarator
// with NO initializer still emits, with empty Sources, because it REBINDS the
// name — the engine's empty-union delete is what stops an outer parameter's
// taint surviving into the shadowed scope.
func (w *ecmaFlowWalker) define(decl *sitter.Node) {
	name := decl.ChildByFieldName("name")
	if name == nil || w.classes.class(name.Symbol()) != ecmaKindIdentifier {
		return
	}
	w.steps = append(w.steps, FlowStep{
		Kind: StepDefine, Target: name, Sources: w.operands(decl.ChildByFieldName("value"), nil),
	})
}

// assign emits the StepAssign forms.
//
// A `this`-QUALIFIED MEMBER TARGET IS THE FIELD WRITE; a member write through
// any OTHER object emits NOTHING, because binding that owner needs a type lookup
// the chunker does not have — the same decline-by-default rule the Go arm
// applies to a selector on a non-receiver operand.
func (w *ecmaFlowWalker) assign(node *sitter.Node) {
	left, right := node.ChildByFieldName("left"), node.ChildByFieldName("right")
	if left == nil {
		return
	}
	sources := w.operands(right, nil)
	switch w.classes.class(left.Symbol()) {
	case ecmaKindIdentifier:
		w.steps = append(w.steps, FlowStep{Kind: StepAssign, Target: left, Sources: sources})
	case ecmaKindMemberExpression:
		object, property := left.ChildByFieldName("object"), left.ChildByFieldName("property")
		if object == nil || property == nil || object.Content(w.src) != "this" {
			return
		}
		w.steps = append(w.steps, FlowStep{
			Kind: StepAssign, Sources: sources,
			Field: property.Content(w.src), Receiver: true,
		})
	}
}

// callArgs emits one StepCallArg per (call, argument position).
//
// THE CALLEE GOES THROUGH normalizeCallee WITHOUT EXCEPTION. It matters more in
// this family than in any other: all three languages carry
// {ChainOps:"?!", ElideLiteralBodies:true, DeclineNonName:true}, so an
// optional-chain or chained callee is rewritten or declined before a CALLS edge
// ever carries it, and a spelling derived any other way would bind a different
// declaration than the sibling CALLS edge does.
//
// A `new` EXPRESSION IS DECLINED. The Calls query emits a constructor as a
// callee, but a construction is not an argument-passing shape this arm models,
// so it states nothing rather than guessing — covered by the absence-is-not-proof
// contract on the edge types.
func (w *ecmaFlowWalker) callArgs(call *sitter.Node) {
	fn, args := call.ChildByFieldName("function"), call.ChildByFieldName("arguments")
	if fn == nil || args == nil {
		return
	}
	switch w.classes.class(fn.Symbol()) {
	case ecmaKindIdentifier, ecmaKindMemberExpression:
	default:
		return
	}
	raw := fn.Content(w.src)
	callee, emit := normalizeCallee(raw, w.prof, raw, fn, fn.StartByte())
	if !emit {
		return
	}
	for i := range int(args.NamedChildCount()) {
		sources := w.operands(args.NamedChild(i), nil)
		if len(sources) == 0 {
			continue
		}
		w.steps = append(w.steps, FlowStep{Kind: StepCallArg, Callee: callee, Index: i, Sources: sources})
	}
}

// returns emits a StepReturn for a return in the declaration's OWN body.
//
// ECMAScript RETURNS ONE VALUE, so the result index is always 0 — a returned
// array or object is one value whose shape this layer does not decompose.
func (w *ecmaFlowWalker) returns(node *sitter.Node) {
	if node.NamedChildCount() == 0 {
		return
	}
	sources := w.operands(node.NamedChild(0), nil)
	if len(sources) == 0 {
		return
	}
	w.steps = append(w.steps, FlowStep{Kind: StepReturn, Index: 0, Sources: sources})
}

// operands collects the identifier nodes an expression READS, appending to out.
//
// A MEMBER EXPRESSION'S PROPERTY IS NOT AN OPERAND: `obj.field` yields `obj`
// alone, because the property is a property_identifier rather than an identifier
// and the descent stops at the identifier it finds first.
func (w *ecmaFlowWalker) operands(node *sitter.Node, out []*sitter.Node) []*sitter.Node {
	if node == nil {
		return out
	}
	if w.classes.class(node.Symbol()) == ecmaKindIdentifier {
		return append(out, node)
	}
	for i := range int(node.NamedChildCount()) {
		out = w.operands(node.NamedChild(i), out)
	}
	return out
}
