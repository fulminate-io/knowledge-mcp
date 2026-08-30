// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterGoFlowSteps()
}

// RegisterGoFlowSteps installs the Go flow-step arm. It is EXPORTED for the
// reason RegisterGoQualifierTypes is: a test that takes the arm OUT to measure
// the unarmed baseline must RESTORE the production registration on cleanup, and
// UnregisterFlowSteps DELETES the entry rather than parking it. A cleanup that
// only unregisters would silently disarm flow collection for every later test
// in the same binary.
func RegisterGoFlowSteps() {
	RegisterFlowSteps(LangGo, goFlowSteps)
}

// goFlowWalker accumulates one declaration's flow steps during a single
// descent.
type goFlowWalker struct {
	// classes is the Go symbol class table, HOISTED HERE for the reason
	// qualBinder.classes documents: the recursive descent classifies a node with
	// a struct-field read plus one array index, never a per-node cgo string
	// conversion.
	classes symbolClasses

	// prof is the Go callee profile, read ONCE per declaration rather than per
	// call site — the same hoist extractCallEdges applies to its own loop.
	prof calleeProfile

	// recv is the receiver's bound name, or "" when the declaration has none or
	// its receiver is unnamed. It is what scopes a field write: a selector
	// assignment whose base is anything else emits no step at all, because the
	// owner cannot be bound without a type lookup the chunker does not have.
	recv string

	src   []byte
	steps []FlowStep
}

// goFlowSteps is the Go arm: one walk of a declaration's subtree, returning the
// flow steps its grammar shows IN SOURCE ORDER.
//
// IT IS THE REFERENCE ARM the other fourteen are written against, so the shape
// here is the shape they copy: seed the signature, descend once, and report
// grammar shape ONLY — deciding which parameter reaches which sink is
// flowClosure's job and no arm may duplicate it.
//
// It is a plain recursive node walk rather than a tree-sitter query, for the
// reason goQualifierTypes gives: a QueryCursor is a cgo handle that must be
// closed on every path, and this walk needs no pattern matching NamedChild
// cannot express.
func goFlowSteps(declNode *sitter.Node, src []byte) []FlowStep {
	if declNode == nil {
		return nil
	}
	w := &goFlowWalker{classes: goKinds(), prof: calleeProfileFor(LangGo), src: src}

	// The signature seeds the sources. goSignatureParts is DELEGATED rather than
	// re-derived: it finds the parameter list positionally and TESTS the
	// following node instead of counting lists, which is what stops
	// `func (s *S) n(p Q) T` losing its bare-type result.
	recv, params, _ := goSignatureParts(declNode)
	w.seedReceiver(recv)
	w.seedParams(params)

	// The body. SOURCE ORDER IS THE CONTRACT FlowStep documents, and it holds
	// here by construction rather than by a sort: the signature is seeded before
	// the descent, and the descent is a pre-order walk over named children,
	// which visits strictly ascending StartByte.
	walkGoFlowSteps(w, declNode, false)

	if len(w.steps) == 0 {
		return nil
	}
	return w.steps
}

// seedReceiver emits the receiver's StepParam, marked Receiver, and records its
// bound name for the field-write scoping.
func (w *goFlowWalker) seedReceiver(list *sitter.Node) {
	if list == nil {
		return
	}
	for i := range int(list.NamedChildCount()) {
		decl := list.NamedChild(i)
		if w.classes.class(decl.Symbol()) != goKindParameterDeclaration {
			continue
		}
		names, _ := goNamesAndType(decl, w.src)
		if names == 0 {
			w.steps = append(w.steps, FlowStep{Kind: StepParam, Receiver: true})
			return
		}
		// goNamesAndType returns a COUNT: the names it found are the
		// declaration's first `names` named children, read back by index for
		// nothing because the tree memoizes each node's wrapper.
		recv := decl.NamedChild(0)
		w.recv = recv.Content(w.src)
		w.steps = append(w.steps, FlowStep{Kind: StepParam, Target: recv, Receiver: true})
		return
	}
}

// seedParams emits one StepParam per parameter POSITION.
//
// AN UNNAMED PARAMETER OCCUPIES ITS POSITION AND EMITS A StepParam WITH A NIL
// Target: it binds no name, but it MUST advance the index, for the reason
// TypeFacts.Results states verbatim — a dropped entry shifts every later
// position and silently rebinds them to the wrong parameter. The blank
// identifier is the same case: `_` names no binding, so its Target is nil while
// its position is held.
//
// A parameter_declaration's identifiers are FLATTENED, so `func f(a, b string,
// c int)` yields Index 0, 1, 2 rather than 0, 0, 1.
func (w *goFlowWalker) seedParams(list *sitter.Node) {
	if list == nil {
		return
	}
	idx := 0
	for i := range int(list.NamedChildCount()) {
		decl := list.NamedChild(i)
		switch w.classes.class(decl.Symbol()) {
		case goKindParameterDeclaration, goKindVariadicParameterDeclaration:
		default:
			continue
		}
		names, _ := goNamesAndType(decl, w.src)
		if names == 0 {
			w.steps = append(w.steps, FlowStep{Kind: StepParam, Index: idx})
			idx++
			continue
		}
		for j := range names {
			name := decl.NamedChild(j)
			step := FlowStep{Kind: StepParam, Index: idx}
			if name.Content(w.src) != "_" {
				step.Target = name
			}
			w.steps = append(w.steps, step)
			idx++
		}
	}
}

// walkGoFlowSteps descends one declaration emitting the local syntax the
// closure engine reads.
//
// THE SWITCH IS ON THE NUMERIC SYMBOL, NOT ON THE KIND NAME, and that is the
// measured cost of this function: reading a node's kind name converts a cgo
// C-string into a FRESH Go string on every call, so a recursive walk that names
// every named node at every depth allocates once per node visited. w.classes
// turns the symbol the binding already holds into one bounds-checked array
// index.
//
// inLiteral SUPPRESSES RETURNS ONLY, and the asymmetry is deliberate. A
// func_literal's `return` belongs to the closure, not to the enclosing
// declaration, so counting it would attribute a closure's result position to
// the outer signature. Its assignments and calls DO belong to the enclosing
// declaration's dataflow — a closure that passes an outer parameter to a sink
// passes it — so those keep emitting at any depth.
func walkGoFlowSteps(w *goFlowWalker, node *sitter.Node, inLiteral bool) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		nested := inLiteral
		switch w.classes.class(child.Symbol()) {
		case goKindShortVarDecl:
			w.defineFromShortVar(child)
		case goKindVarDecl, goKindConstDecl:
			w.defineFromSpecs(child)
		case goKindAssignmentStatement:
			w.assign(child)
		case goKindCallExpression:
			w.callArgs(child)
		case goKindReturnStatement:
			if !inLiteral {
				w.returns(child)
			}
		case goKindFuncLiteral:
			nested = true
		}
		walkGoFlowSteps(w, child, nested)
	}
}

// defineFromShortVar emits one StepDefine per name bound by `:=`.
//
// BOTH SIDES ARE expression_list WRAPPERS, so the identifiers and the
// right-hand expressions are GRANDCHILDREN of the declaration — a walk reading
// direct children finds nothing at all.
//
// EVERY NAME TAKES THE WHOLE RIGHT-HAND SIDE'S OPERANDS, which is deliberately
// over-approximate and correct for taint: the multi-value `a, b := f(p)` form
// cannot say which result carries p, so both names carry it.
func (w *goFlowWalker) defineFromShortVar(decl *sitter.Node) {
	left, right := goShortVarSides(decl)
	if left == nil || right == nil {
		return
	}
	if goCountNamedChildrenOfKind(left, goKindIdentifier) == 0 {
		return
	}
	sources := w.operands(right, nil)
	goEachNamedChildOfKind(left, goKindIdentifier, func(_ int, name *sitter.Node) {
		w.steps = append(w.steps, FlowStep{Kind: StepDefine, Target: name, Sources: sources})
	})
}

// defineFromSpecs emits a StepDefine per name bound by a var or const
// declaration.
//
// A SPEC WITH NO VALUE STILL EMITS, with empty Sources, and that is the point
// rather than waste: `var a T` inside a body REBINDS the name, and the engine's
// empty-union delete is what stops an outer parameter's taint surviving into
// the shadowed scope.
func (w *goFlowWalker) defineFromSpecs(decl *sitter.Node) {
	for i := range int(decl.NamedChildCount()) {
		spec := decl.NamedChild(i)
		if kind := w.classes.class(spec.Symbol()); kind != goKindVarSpec && kind != goKindConstSpec {
			continue
		}
		if goCountNamedChildrenOfKind(spec, goKindIdentifier) == 0 {
			continue
		}
		var sources []*sitter.Node
		goEachNamedChildOfKind(spec, goKindExpressionList, func(_ int, values *sitter.Node) {
			sources = w.operands(values, sources)
		})
		goEachNamedChildOfKind(spec, goKindIdentifier, func(_ int, name *sitter.Node) {
			w.steps = append(w.steps, FlowStep{Kind: StepDefine, Target: name, Sources: sources})
		})
	}
}

// assign emits the StepAssign forms.
//
// A PLAIN IDENTIFIER TARGET is a rebind of a local. A SELECTOR TARGET emits a
// step ONLY when its base is the receiver's own bound name: that is the shape
// that becomes a FLOWS_TO_FIELD edge, whose owner is the declaration's own
// receiver type. A selector on any other operand emits NOTHING, because binding
// that owner needs a type lookup the chunker does not have — the same
// decline-by-default rule goQualTypeText applies.
func (w *goFlowWalker) assign(node *sitter.Node) {
	left, right := goTwoNamedChildrenOfKind(node, goKindExpressionList)
	if left == nil || right == nil {
		return
	}
	sources := w.operands(right, nil)
	for i := range int(left.NamedChildCount()) {
		target := left.NamedChild(i)
		switch w.classes.class(target.Symbol()) {
		case goKindIdentifier:
			w.steps = append(w.steps, FlowStep{Kind: StepAssign, Target: target, Sources: sources})
		case goKindSelectorExpression:
			w.assignField(target, sources)
		}
	}
}

// assignField emits the receiver-field write, or nothing.
func (w *goFlowWalker) assignField(target *sitter.Node, sources []*sitter.Node) {
	if w.recv == "" || target.NamedChildCount() < 2 {
		return
	}
	base := target.NamedChild(0)
	if w.classes.class(base.Symbol()) != goKindIdentifier || base.Content(w.src) != w.recv {
		return
	}
	w.steps = append(w.steps, FlowStep{
		Kind:     StepAssign,
		Target:   base,
		Sources:  sources,
		Field:    target.NamedChild(1).Content(w.src),
		Receiver: true,
	})
}

// callArgs emits one StepCallArg per (call, argument position).
//
// THE CALLEE GOES THROUGH normalizeCallee WITHOUT EXCEPTION, and goCalleeText's
// own output is NOT the CALLS spelling: for `obj.a(1).b(2)` it returns
// `obj.a(1).b` while extractCallEdges cuts to `b` and then DECLINES it as a
// chained tail. An arm trusting the raw text would emit a spelling that binds a
// same-named local — the accidental bind the parity rule exists to prevent.
//
// AN ARGUMENT WITH NO OPERAND IDENTIFIER EMITS NO STEP. These facts are
// anchored on param flows rather than on call sites, so an all-constant call is
// correctly absent from this arm's output even though it emits a CALLS edge.
//
// A GENERIC CALL WRITTEN AS AN EXPLICIT INSTANTIATION IS DECLINED. The Go
// grammar parses `newPresizedMap[string, int](100)` as a
// type_conversion_expression rather than a call_expression, and a conversion and
// a generic call are structurally identical there — so this arm states nothing
// about it rather than guessing, and the absence is covered by the
// absence-is-not-proof contract on the edge types.
func (w *goFlowWalker) callArgs(call *sitter.Node) {
	raw := goCalleeText(call, w.src)
	if raw == "" {
		return
	}
	calleeNode := call.NamedChild(0)
	callee, emit := normalizeCallee(raw, w.prof, raw, calleeNode, calleeNode.StartByte())
	if !emit {
		return
	}
	args := goFirstNamedChildOfKind(call, goKindArgumentList)
	if args == nil {
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

// returns emits one StepReturn per result position of a return in the
// declaration's OWN body.
func (w *goFlowWalker) returns(node *sitter.Node) {
	values := goFirstNamedChildOfKind(node, goKindExpressionList)
	if values == nil {
		return
	}
	for i := range int(values.NamedChildCount()) {
		sources := w.operands(values.NamedChild(i), nil)
		if len(sources) == 0 {
			continue
		}
		w.steps = append(w.steps, FlowStep{Kind: StepReturn, Index: i, Sources: sources})
	}
}

// operands collects the identifier nodes an expression READS, appending to out.
//
// A SELECTOR'S FIELD IS NOT AN OPERAND: `obj.field` yields `obj` alone, because
// the field name is a field_identifier rather than an identifier and the
// descent stops at the identifier it finds. That is what keeps a field READ
// from being mistaken for a name the taint map could hold.
//
// THE DESCENT IS DELIBERATELY UNSCOPED WITHIN THE EXPRESSION — a nested call's
// own arguments are operands of the outer expression too. That over-approximates
// in the direction the whole design over-approximates: presence proves the value
// is parameter-derived, and a result computed from a parameter is
// parameter-derived.
func (w *goFlowWalker) operands(node *sitter.Node, out []*sitter.Node) []*sitter.Node {
	if node == nil {
		return out
	}
	if w.classes.class(node.Symbol()) == goKindIdentifier {
		return append(out, node)
	}
	for i := range int(node.NamedChildCount()) {
		out = w.operands(node.NamedChild(i), out)
	}
	return out
}
