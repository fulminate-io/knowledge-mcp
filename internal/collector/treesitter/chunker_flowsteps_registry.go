// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// FlowStepResolver walks ONE declaration's subtree and returns the flow steps
// its grammar shows — parameters bound, locals defined, values assigned,
// arguments occupied, results returned — IN SOURCE ORDER.
//
// It returns nil when the declaration shows nothing, and nil is a meaningful
// answer rather than an empty one: a declaration with no parameters and no
// calls has no flow to state, and the closure engine is never entered for it.
//
// The carrier's contract lives on FlowStep in types.go: source ordering, live
// node pointers valid only inside the parse, and the callee-spelling parity
// rule. An arm author reads that doc block before writing an arm.
type FlowStepResolver func(declNode *sitter.Node, src []byte) []FlowStep

// flowStepResolvers holds the registered per-language arms. It is the fifth
// per-language registry in this package, after declNameResolvers,
// testKindClassifiers, bindsResolvers and qualifierTypeResolvers, and it
// follows their shape: empty until a language's own init installs an arm, and
// inert for every language that ships none.
var flowStepResolvers = map[Language]FlowStepResolver{}

// RegisterFlowSteps installs the flow-step arm for one language.
//
// IT OVERWRITES SILENTLY RATHER THAN PANICKING ON A DUPLICATE, for the reason
// RegisterQualifierTypes documents: tests must be able to swap an arm in and
// restore the production one, and the hazard worth avoiding is an unregistered
// production arm silently disarming every later test in the same binary.
func RegisterFlowSteps(lang Language, r FlowStepResolver) {
	flowStepResolvers[lang] = r
}

// UnregisterFlowSteps removes a language's arm, restoring the unregistered
// state exactly.
//
// IT IS NOT DECORATION AND NOT ONLY FOR TEST CLEANUP. The allocation
// instrument's flow_off leg drives it, because a baseline that leaves the arm
// registered compares a number against itself — the affordance that was built
// and never exercised is what let the qualifier arms' allocation move go unseen
// by a time-only budget. The same inverse hazard UnregisterQualifierTypes
// documents applies: a test that installs a fake over the production arm must
// restore the production registration in its cleanup rather than merely
// deleting its fake.
func UnregisterFlowSteps(lang Language) {
	delete(flowStepResolvers, lang)
}

// FlowStepsArm returns the arm registered for one language, and whether there
// is one.
//
// IT IS A CROSS-TICKET CONTRACT, not merely a reader. "Armed", for the ast flow
// leaf's loud-error policy, MEANS has a flow arm read through this accessor;
// the collector-capability marker in package parser calls it to decide whether
// a language's hub node carries the flow_arm key. Renaming it is a change to
// both of those consumers.
func FlowStepsArm(lang Language) (FlowStepResolver, bool) {
	r, ok := flowStepResolvers[lang]
	return r, ok
}

// flowStepsFor runs the registered arm for one declaration's language, or
// returns nil when none is registered.
//
// NIL IS THE WHOLE OF THE UNREGISTERED CONTRACT. A language without an arm pays
// one nil map read per declaration rather than an allocation, and emits
// byte-identical output to what it emitted before this registry existed.
func flowStepsFor(lang Language, declNode *sitter.Node, src []byte) []FlowStep {
	r, ok := flowStepResolvers[lang]
	if !ok {
		return nil
	}
	return r(declNode, src)
}
