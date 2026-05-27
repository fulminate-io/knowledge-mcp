// SPDX-License-Identifier: Apache-2.0

package rerank

import (
	"encoding/json"
	"fmt"
)

// Intent-aware reranker weights
//
// rerank_default_pipeline.go owns the intent-aware default Post pipeline
// installed by executeSearchHybrid when the caller does not supply one and
// the search targets the code graph. classifyQueryIntent (rerank_intent.go)
// inspects the query string at request time and selects one of three
// per-intent Pipelines via defaultRerankPipeline. The per-intent ×
// per-test_kind weight table was locked via the rankeval ablation in
// ticket cddf5098 (see finding 2aad0816fa13682f5a4744c356c0047f for the
// directionality-rule analysis behind these specific magnitudes).
//
// Locked decisions referenced:
//   - Q1: ADDITIVE ScoreOps (Mode "add"), NOT multiplicative.
//   - Q3: typed Go helper, weights as constants in this file (no env, no
//     config, no JSON-string parse).
//
// Per-intent × per-test_kind table:
//
//	| TestKind                  | Impl-intent | Test-intent | Unknown |
//	|---------------------------|-------------|-------------|---------|
//	| test                      | -0.15       | +0.10       | -0.10   |
//	| benchmark                 | -0.15       | +0.10       | -0.10   |
//	| example                   | -0.15       | +0.10       | -0.10   |
//	| fuzz                      | -0.15       | +0.10       | -0.10   |
//	| setup                     | -0.05       | +0.05       |  0.00   |
//	| teardown                  | -0.05       | +0.05       |  0.00   |
//	| mock                      |  0.00       |  0.00       |  0.00   |
//	| fixture                   |  0.00       |  0.00       |  0.00   |
//	| helper                    |  0.00       |  0.00       |  0.00   |
//	| (non-test, TestKindNone)  | +0.02       | -0.05       |  0.00   |
//
// Helpers/mocks/fixtures stay neutral on every intent (the don't-mask-
// information principle from brainstorm). Non-test code uses an empty-
// string TestKind, matched via Field=test_kind Match=equals Value="".
const (
	weightImplDemoteStrong = -0.15 // test/benchmark/example/fuzz on impl
	weightImplDemoteMild   = -0.05 // setup/teardown on impl
	weightImplPromoteCode  = +0.02 // non-test code on impl

	weightTestPromoteStrong = +0.10 // test/benchmark/example/fuzz on test
	weightTestPromoteMild   = +0.05 // setup/teardown on test
	weightTestDemoteCode    = -0.05 // non-test code on test

	weightUnknownDemoteMild = -0.10 // test/benchmark/example/fuzz on unknown
)

// pipelineImpl/pipelineTest/pipelineUnknown are constructed ONCE at package
// init via build*Pipeline. Each is run through Pipeline.Validate during
// mustValidate so any wiring error (bad Match operator, malformed Predicate
// JSON) panics at server boot rather than silently degrading rerank
// behavior at query time. defaultRerankPipeline is a pure pointer-return
// dispatch on the classified Intent — no per-call allocation.
var (
	pipelineImpl    = mustValidate("impl", buildImplPipeline())
	pipelineTest    = mustValidate("test", buildTestPipeline())
	pipelineUnknown = mustValidate("unknown", buildUnknownPipeline())
)

// mustValidate guards against a malformed default pipeline at package init.
// A panic here surfaces as a server-boot crash, which is the right failure
// mode: the alternative (silent fallback to no rerank) would mask wiring
// bugs and leave search ordering subtly broken in production.
func mustValidate(name string, p *Pipeline) *Pipeline {
	if err := p.Validate(); err != nil {
		panic(fmt.Sprintf("rerank: invalid default pipeline %q: %v", name, err))
	}
	return p
}

// buildImplPipeline constructs the IntentImpl Post pipeline: demote
// test/benchmark/example/fuzz strongly, demote setup/teardown mildly,
// gently promote non-test code. Helpers/mocks/fixtures stay neutral.
func buildImplPipeline() *Pipeline {
	strongKinds := json.RawMessage(`["test","benchmark","example","fuzz"]`)
	mildKinds := json.RawMessage(`["setup","teardown"]`)
	nonTest := json.RawMessage(`""`)
	return &Pipeline{Post: []Op{
		&ScoreOp{Op: "score", Mode: "add", Value: weightImplDemoteStrong,
			Where: Predicate{Field: "test_kind", Match: "in", Value: strongKinds}},
		&ScoreOp{Op: "score", Mode: "add", Value: weightImplDemoteMild,
			Where: Predicate{Field: "test_kind", Match: "in", Value: mildKinds}},
		&ScoreOp{Op: "score", Mode: "add", Value: weightImplPromoteCode,
			Where: Predicate{Field: "test_kind", Match: "equals", Value: nonTest}},
	}}
}

// buildTestPipeline constructs the IntentTest Post pipeline: mirror image
// of impl — promote strong test kinds, promote setup/teardown mildly,
// demote non-test code. Helpers/mocks/fixtures stay neutral.
func buildTestPipeline() *Pipeline {
	strongKinds := json.RawMessage(`["test","benchmark","example","fuzz"]`)
	mildKinds := json.RawMessage(`["setup","teardown"]`)
	nonTest := json.RawMessage(`""`)
	return &Pipeline{Post: []Op{
		&ScoreOp{Op: "score", Mode: "add", Value: weightTestPromoteStrong,
			Where: Predicate{Field: "test_kind", Match: "in", Value: strongKinds}},
		&ScoreOp{Op: "score", Mode: "add", Value: weightTestPromoteMild,
			Where: Predicate{Field: "test_kind", Match: "in", Value: mildKinds}},
		&ScoreOp{Op: "score", Mode: "add", Value: weightTestDemoteCode,
			Where: Predicate{Field: "test_kind", Match: "equals", Value: nonTest}},
	}}
}

// buildUnknownPipeline constructs the IntentUnknown Post pipeline: a
// single mild demote on strong test kinds (≤2-token queries are noisy;
// avoid amplifying either direction).
func buildUnknownPipeline() *Pipeline {
	strongKinds := json.RawMessage(`["test","benchmark","example","fuzz"]`)
	return &Pipeline{Post: []Op{
		&ScoreOp{Op: "score", Mode: "add", Value: weightUnknownDemoteMild,
			Where: Predicate{Field: "test_kind", Match: "in", Value: strongKinds}},
	}}
}

// defaultRerankPipeline returns the package-init-constructed Pipeline
// pointer for the classified Intent. No per-call allocation. Returns nil
// for an unrecognized Intent (defensive — should not happen given the
// closed enum in intent.go).
func defaultRerankPipeline(intent Intent) *Pipeline {
	switch intent {
	case IntentImpl:
		return pipelineImpl
	case IntentTest:
		return pipelineTest
	case IntentUnknown:
		return pipelineUnknown
	}
	return nil
}
