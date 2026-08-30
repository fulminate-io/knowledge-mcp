// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"slices"

	sitter "github.com/smacker/go-tree-sitter"
)

// FlowKind names the sink a parameter reached. The vocabulary is closed at
// three members, one per emitted edge type.
type FlowKind uint8

const (
	FlowToReturn FlowKind = iota // a result position of the declaration
	FlowToArg                    // an argument position of a call
	FlowToField                  // a field written on the receiver
)

// FlowSource names the parameter a fact starts at: a positional parameter, or
// the receiver.
//
// Receiver AND ParamIndex ARE NOT INTERCHANGEABLE. A receiver has no position
// in the parameter list, so ParamIndex is meaningless when Receiver is set and
// the Evidence renderer spells it `recv` rather than `p0`. A reader that
// collapsed the two would attribute every receiver flow to parameter zero.
type FlowSource struct {
	Receiver   bool
	ParamIndex int
}

// ParamFlow is ONE fact: a parameter reached a sink. Exactly the fields its
// Kind uses are meaningful; the rest are zero.
type ParamFlow struct {
	Source FlowSource
	Kind   FlowKind

	// ResultIndex is the result position, on FlowToReturn.
	ResultIndex int

	// Callee is the spelling of the called declaration and ArgIndex its
	// argument position, on FlowToArg.
	Callee   string
	ArgIndex int

	// Field is the written field's name, on FlowToField.
	Field string
}

// flowClosure turns one declaration's source-ordered flow steps into the
// parameter-flow facts they imply.
//
// IT IS THE SINGLE DEFINITION OF WHAT CLOSURE MEANS, and that is why it holds
// no language knowledge whatsoever: no language constant, no kind table, no
// node-kind name. Its only inputs are FlowStep values and the source bytes the
// step's nodes index into. Fifteen per-language spellings of this algorithm
// would drift; one cannot.
//
// THE ALGORITHM IS ONE FORWARD PASS carrying a taint map from NAME TEXT to a
// set of FlowSource:
//
//  1. StepParam seeds its Target's name with its own source. A nil Target
//     seeds nothing — the position was still counted by the arm.
//  2. StepDefine and StepAssign compute the union of taint over their Sources'
//     texts. A NON-EMPTY union is written to the target's name; an EMPTY union
//     DELETES it.
//  3. StepAssign carrying a Field emits a FlowToField fact instead of
//     rebinding, because the write lands on the field rather than on the base
//     name. Rebinding there would move the receiver's own taint.
//  4. StepCallArg emits FlowToArg per source in its union.
//  5. StepReturn emits FlowToReturn per source in its union.
//
// THE DELETE IS THE WHOLE OF THE REBIND SEMANTICS, and it is what makes order
// matter: `a := p; a = untainted; sink(a)` must record NOTHING, while
// `a := untainted; a = p; sink(a)` must record. A propagate-only engine gets
// the first wrong and is indistinguishable from a correct one on any fixture
// that never rebinds — which is why the rebind subtest carries its own
// positive control.
//
// SHADOWING IS HANDLED BY THAT SAME DELETE rather than by a conflicted set: an
// inner `for _, p := range xs` is a StepDefine binding p from untainted
// sources, so taint[p] is deleted and the inner uses record nothing. That is
// strictly better than declining the whole parameter, which would lose the
// OUTER uses too.
//
// SELF-REFERENCE IS NOT A CYCLE. `a = a + p` reads taint[a] in the union BEFORE
// the assignment writes it, so a single forward pass suffices. There is no
// fixpoint iteration and none is needed: a declaration's steps are a finite
// ordered list, not a graph. Do not add an iterate-to-fixpoint loop.
func flowClosure(steps []FlowStep, src []byte) []ParamFlow {
	if len(steps) == 0 {
		return nil
	}

	// Both maps are nil until the first write, so a declaration whose steps
	// produce nothing allocates nothing at all.
	var st flowState

	for i := range steps {
		step := &steps[i]
		switch step.Kind {
		case StepParam:
			st.seed(step, src)
		case StepDefine, StepAssign:
			st.rebind(step, src)
		case StepCallArg:
			for _, s := range flowUnion(st.taint, step.Sources, src) {
				st.out = append(st.out, ParamFlow{
					Source: s, Kind: FlowToArg, Callee: step.Callee, ArgIndex: step.Index,
				})
			}
		case StepReturn:
			for _, s := range flowUnion(st.taint, step.Sources, src) {
				st.out = append(st.out, ParamFlow{Source: s, Kind: FlowToReturn, ResultIndex: step.Index})
			}
		}
	}

	return dedupeFlows(st.out)
}

// flowState is the forward pass's working memory: the name-to-source taint map
// and the facts produced so far.
type flowState struct {
	taint map[string][]FlowSource
	out   []ParamFlow
}

// seed records a parameter as a taint source under its own bound name.
func (s *flowState) seed(step *FlowStep, src []byte) {
	name := flowName(step.Target, src)
	if name == "" {
		return
	}
	s.bind(name, []FlowSource{{Receiver: step.Receiver, ParamIndex: step.Index}})
}

// bind writes a non-empty source set, allocating the map on first use.
func (s *flowState) bind(name string, union []FlowSource) {
	if s.taint == nil {
		s.taint = make(map[string][]FlowSource, 4)
	}
	s.taint[name] = union
}

// rebind applies a StepDefine or StepAssign.
//
// A FIELD WRITE EMITS INSTEAD OF REBINDING, because the write lands on the field
// rather than on the base name: rebinding there would move the receiver's own
// taint onto whatever the right-hand side carried.
//
// AN EMPTY UNION DELETES, and that delete is the whole of the rebind semantics.
func (s *flowState) rebind(step *FlowStep, src []byte) {
	union := flowUnion(s.taint, step.Sources, src)
	if step.Field != "" {
		for _, source := range union {
			s.out = append(s.out, ParamFlow{Source: source, Kind: FlowToField, Field: step.Field})
		}
		return
	}
	name := flowName(step.Target, src)
	if name == "" {
		return
	}
	if len(union) == 0 {
		delete(s.taint, name)
		return
	}
	s.bind(name, union)
}

// flowName reads a step target's bound name, or "" when there is nothing to
// bind.
//
// THE BLANK IDENTIFIER IS NOT A NAME. It is written in several of the languages
// this engine serves and binds nothing in any of them, so admitting it would
// pool every discarded value under one key and leak taint between unrelated
// statements.
func flowName(target *sitter.Node, src []byte) string {
	if target == nil {
		return ""
	}
	name := target.Content(src)
	if name == "_" {
		return ""
	}
	return name
}

// flowUnion collects the taint of every operand a step reads.
//
// The result is a SLICE-BACKED SET rather than a map: a union holds one or two
// sources in the overwhelming majority of declarations, so a linear membership
// scan beats a map allocation per name.
func flowUnion(taint map[string][]FlowSource, sources []*sitter.Node, src []byte) []FlowSource {
	if len(taint) == 0 || len(sources) == 0 {
		return nil
	}
	var union []FlowSource
	for _, node := range sources {
		if node == nil {
			continue
		}
		for _, s := range taint[node.Content(src)] {
			if !slices.Contains(union, s) {
				union = append(union, s)
			}
		}
	}
	return union
}

// dedupeFlows collapses identical facts, PRESERVING FIRST-SEEN ORDER.
//
// Two call sites passing parameter 1 into argument 2 of the same callee are ONE
// fact, and the four-part edge identity would collapse them downstream anyway.
// Order is preserved rather than sorted because the input is already in source
// order, and a deterministic output is what keeps the Evidence-keyed edge
// identity stable across collects.
func dedupeFlows(flows []ParamFlow) []ParamFlow {
	if len(flows) < 2 {
		return flows
	}
	seen := make(map[ParamFlow]struct{}, len(flows))
	out := flows[:0]
	for _, f := range flows {
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	return out
}
