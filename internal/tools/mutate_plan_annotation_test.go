// SPDX-License-Identifier: Apache-2.0

package tools

// mutate_plan_annotation_test.go covers the pre-write guard on a plan_annotation
// write: the three refusal arms, the two determinism rules they inherit, and the
// proof that a refusal writes nothing.
//
// EVERY ARM ASSERTS THE MESSAGE NAMES THE OFFENDING KEY AND THE VALID SET, and
// that no mutation Execute fired. The house rule is "bad input always errors":
// nothing here is defaulted, coerced or dropped.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// countingMutateCaller records every write Execute so a refusal arm can prove it
// wrote nothing.
type countingMutateCaller struct {
	mutations int
}

func (c *countingMutateCaller) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.ToolResult{}, nil
}

func (c *countingMutateCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if req.GetMutation() != nil {
		c.mutations++
		return &knowledgev1.ExecuteResponse{Ids: []string{"ann-1"}, AffectedCount: 1}, nil
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// annotationMutate drives the real InterceptMutate and returns the write counter
// with the result.
//
// IT DOES NOT ASSERT `handled`. A REFUSED write is claimed by the guard, while an
// ACCEPTED one falls through to the engine arm with handled=false — that
// fall-through IS the accept path, so requiring handled would make the accept
// controls unwritable and, worse, would pass only if the guard claimed
// everything.
func annotationMutate(t *testing.T, args string) (*countingMutateCaller, kgtools.ToolResult) {
	t.Helper()
	fc := &countingMutateCaller{}
	_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(args),
	})
	return fc, res
}

// R3-b. The three error arms on a SINGLE typed create, one case each.
func TestMutate_PlanAnnotationRefusals_SingleCreate(t *testing.T) {
	cases := []struct {
		name    string
		args    string
		wantSub []string
	}{
		{
			name:    "a kind outside the three",
			args:    `{"operation":"create","type":"plan_annotation","name":"n","summary":"s","metadata":{"annotation_kind":"suggestion"},"links":["sec-0"]}`,
			wantSub: []string{"annotation_kind", "suggestion", "correct", "finding", "needed change"},
		},
		{
			name:    "no kind at all",
			args:    `{"operation":"create","type":"plan_annotation","name":"n","summary":"s","links":["sec-0"]}`,
			wantSub: []string{"annotation_kind", "correct", "finding", "needed change"},
		},
		{
			name:    "a finding with no tier",
			args:    `{"operation":"create","type":"plan_annotation","name":"n","summary":"s","metadata":{"annotation_kind":"finding"},"links":["sec-0"]}`,
			wantSub: []string{"annotation_tier", "finding"},
		},
		{
			name:    "a needed change with no replacement text",
			args:    `{"operation":"create","type":"plan_annotation","name":"n","summary":"s","metadata":{"annotation_kind":"needed change"},"links":["sec-0"]}`,
			wantSub: []string{"replacement_text", "needed change"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc, res := annotationMutate(t, tc.args)
			require.True(t, res.IsError, "expected a refusal, got: %s", toolResultText(res))
			for _, sub := range tc.wantSub {
				assert.Contains(t, toolResultText(res), sub, "the refusal must name %q", sub)
			}
			assert.Zero(t, fc.mutations, "a refused annotation write must persist nothing")
		})
	}
}

// The ACCEPT side, without which every refusal above is satisfiable by a guard
// that refuses everything.
func TestMutate_PlanAnnotationAccepted(t *testing.T) {
	cases := map[string]string{
		"correct":       `{"operation":"create","type":"plan_annotation","name":"n","summary":"s","metadata":{"annotation_kind":"correct","reviewer_lane":"rv-1"},"links":["sec-0"]}`,
		"finding":       `{"operation":"create","type":"plan_annotation","name":"n","summary":"s","metadata":{"annotation_kind":"finding","annotation_tier":"T2","reviewer_lane":"rv-1"},"links":["sec-0"]}`,
		"needed change": `{"operation":"create","type":"plan_annotation","name":"n","summary":"s","metadata":{"annotation_kind":"needed change","replacement_text":"the exact replacement","reviewer_lane":"rv-1"},"links":["sec-0"]}`,
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			_, res := annotationMutate(t, args)
			assert.False(t, res.IsError, "a well-formed %s annotation must be accepted: %s", name, toolResultText(res))
		})
	}
}

// A create of a DIFFERENT type carrying the same malformed metadata is NOT
// refused: the guard is scoped to plan_annotation and must not become a
// tree-wide metadata validator.
func TestMutate_PlanAnnotationGuardIsScopedToTheType(t *testing.T) {
	_, res := annotationMutate(t,
		`{"operation":"create","type":"document","name":"n","summary":"s","metadata":{"annotation_kind":"suggestion"},"links":["sec-0"]}`)
	assert.False(t, res.IsError, "the guard must not fire on another node type: %s", toolResultText(res))
}

// R3-b on the BATCH path: create_batch carries the same three arms, at the same
// seam the criterion-pair gate already uses.
func TestMutate_PlanAnnotationRefusals_CreateBatch(t *testing.T) {
	cases := []struct {
		name    string
		args    string
		wantSub []string
	}{
		{
			name:    "a kind outside the three",
			args:    `{"operation":"create_batch","nodes":[{"type":"plan_annotation","name":"a","summary":"s","metadata":{"annotation_kind":"nit"}}]}`,
			wantSub: []string{"nodes[0]", "annotation_kind", "nit"},
		},
		{
			name:    "a finding with no tier",
			args:    `{"operation":"create_batch","nodes":[{"type":"plan_section","name":"sec","summary":"s"},{"type":"plan_annotation","name":"a","summary":"s","metadata":{"annotation_kind":"finding"}}]}`,
			wantSub: []string{"nodes[1]", "annotation_tier"},
		},
		{
			name:    "a needed change with no replacement text",
			args:    `{"operation":"create_batch","nodes":[{"type":"plan_annotation","name":"a","summary":"s","metadata":{"annotation_kind":"needed change"}}]}`,
			wantSub: []string{"nodes[0]", "replacement_text"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc, res := annotationMutate(t, tc.args)
			require.True(t, res.IsError, "expected a refusal, got: %s", toolResultText(res))
			for _, sub := range tc.wantSub {
				assert.Contains(t, toolResultText(res), sub, "the refusal must name %q", sub)
			}
			assert.Zero(t, fc.mutations, "a refused batch must persist nothing")
		})
	}
	t.Run("a well-formed batch is accepted", func(t *testing.T) {
		_, res := annotationMutate(t,
			`{"operation":"create_batch","nodes":[{"type":"plan_annotation","name":"a","summary":"s","metadata":{"annotation_kind":"correct"}}]}`)
		assert.False(t, res.IsError, "control: a well-formed annotation batch is not refused: %s", toolResultText(res))
	})
}

// The determinism rule: a batch with TWO offenders always names the same one
// first, because the guard walks nodes[] in the CALLER'S own order.
func TestMutate_PlanAnnotationRefusalIsDeterministic(t *testing.T) {
	const twoOffenders = `{"operation":"create_batch","nodes":[
		{"type":"plan_annotation","name":"a","summary":"s","metadata":{"annotation_kind":"nit"}},
		{"type":"plan_annotation","name":"b","summary":"s","metadata":{"annotation_kind":"finding"}}
	]}`
	var first string
	for i := range 8 {
		_, res := annotationMutate(t, twoOffenders)
		require.True(t, res.IsError)
		body := toolResultText(res)
		if i == 0 {
			first = body
			assert.Contains(t, body, "nodes[0]", "the FIRST offender in the caller's order is named")
			assert.NotContains(t, body, "nodes[1]")
			continue
		}
		assert.Equal(t, first, body, "the same payload names the same offender on every run")
	}
}

// TestMutate_PlanAnnotationEdgeCarriesKindAndTier is the WRITE half of the
// severity-on-the-edge requirement: creating an annotation with links:[section]
// stamps the annotation's kind and tier onto the relates-to edge it makes.
//
// THE ASSERTION IS ON THE PERSISTED EDGE, read off the batch that reached the
// write path, not on the response. A create that succeeded while writing an
// unstamped edge is exactly the failure this requirement exists to prevent, and
// it returns the same success line either way.
//
// The READ half — the tier answered from the section's edges with no annotation
// node hydrated — is TestSectionAnnotationSeverity_ReadableFromEdgesAlone in the
// render package.
func TestMutate_PlanAnnotationEdgeCarriesKindAndTier(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"sec-0": nodeResultJSON(t, "sec-0", string(kgtypes.NodePlanSection), nil),
		},
		mutateIDs: []string{"ann-1"},
	}
	_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: json.RawMessage(`{"operation":"create","type":"plan_annotation",` +
			`"name":"the caller census is short by two","summary":"the census misses two callers",` +
			`"links":["sec-0"],` +
			`"metadata":{"annotation_kind":"finding","annotation_tier":"T2","reviewer_lane":"rv-1"}}`),
	})
	require.False(t, res.IsError, "%s", toolResultText(res))

	require.Len(t, fc.execMutations, 1, "one create is one batch")
	edges := fc.execMutations[0].GetEdges()
	require.Len(t, edges, 1, "links:[sec-0] makes exactly one edge")

	assert.Equal(t, string(kgtypes.EdgeRelatesTo), edges[0].GetType(), "no new edge type")
	assert.Equal(t, "sec-0", edges[0].GetToId())
	assert.Equal(t, kgtypes.AnnotationEdgeMethod, edges[0].GetMethod(),
		"the method identifies this relates-to edge as an annotation's without hydrating the peer")

	kind, tier, ok := kgtypes.ParseAnnotationEdgeSeverity(edges[0].GetEvidence())
	require.True(t, ok, "the edge carries a readable severity")
	assert.Equal(t, kgtypes.AnnotationKindFinding, kind)
	assert.Equal(t, "T2", tier)
}

// TestMutate_NonAnnotationEdgesAreNotStamped is the control: the stamp is scoped
// to plan_annotation creates and does not rewrite any other type's links. Without
// it, a stamp applied to every create would satisfy the test above.
func TestMutate_NonAnnotationEdgesAreNotStamped(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"sec-0": nodeResultJSON(t, "sec-0", string(kgtypes.NodePlanSection), nil),
		},
		mutateIDs: []string{"doc-1"},
	}
	_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: json.RawMessage(`{"operation":"create","type":"document",` +
			`"name":"a note","summary":"a note about the section","links":["sec-0"]}`),
	})
	require.False(t, res.IsError, "%s", toolResultText(res))

	require.Len(t, fc.execMutations, 1)
	edges := fc.execMutations[0].GetEdges()
	require.Len(t, edges, 1)
	assert.Empty(t, edges[0].GetMethod(), "a document's links edge is untouched")
	assert.Empty(t, edges[0].GetEvidence())
}

// TestValidateAnnotationMetadata_SurfacesTheSharedValidatorsOwnWords pins that
// the tier refusal REPORTS the shared validator rather than paraphrasing it.
//
// WHY THIS NEEDS A TEST AT ALL. The composed message used to bind the validator's
// error and throw it away, then assert one specific cause — a missing tier. That
// is correct only while the missing-tier arm is the validator's ONLY remaining
// failure, which holds today because the kind rule ran on the line above. The
// day the shared validator grows a third rule, an annotation refused for that
// third reason would be reported to the caller as a missing tier: a sentence
// naming a key they may well have supplied.
//
// THE EXPECTATION IS EXTERNAL, which is what makes this more than a restatement:
// the wanted text is not written here, it is asked of the validator at run time.
// A message that stops carrying the validator's words fails whatever either
// sentence says.
func TestValidateAnnotationMetadata_SurfacesTheSharedValidatorsOwnWords(t *testing.T) {
	shared := kgtypes.ValidateAnnotationSeverity(kgtypes.AnnotationKindFinding, "")
	require.Error(t, shared, "the control: the shared validator refuses a finding with no tier")

	err := validateAnnotationMetadata("mutate(create)", "", map[string]string{
		kgtypes.AnnotationKindKey: kgtypes.AnnotationKindFinding,
	})
	require.Error(t, err, "a finding with no tier must be refused")
	assert.Contains(t, err.Error(), shared.Error(),
		"the refusal must carry the shared validator's own sentence — two copies of one rule is how an "+
			"annotation ends up acceptable on one carrier and unwritable on the other")
	// AND IT WRAPS RATHER THAN RESTATES, so a caller inspecting the chain reaches
	// the rule. The two error VALUES are distinct by construction — each call to
	// the validator returns a fresh one — so the chain is compared by the sentence
	// it carries, not by identity.
	unwrapped := errors.Unwrap(err)
	require.Error(t, unwrapped, "the refusal must wrap the validator's error, not swallow it")
	assert.Equal(t, shared.Error(), unwrapped.Error(),
		"and the wrapped error is the validator's own")

	// THE CALLER STILL LEARNS WHICH KEY TO SET. Carrying the validator's sentence
	// is worth nothing if the composed refusal stops naming the metadata key, so
	// the key is asserted here and named by the validator itself.
	assert.Contains(t, err.Error(), kgtypes.AnnotationTierKey,
		"the refusal names the metadata key the caller must set")

	// SAME-RUN CONTROL: a well-formed finding is not refused, so the assertions
	// above describe the tier arm rather than a validator that refuses everything.
	require.NoError(t, validateAnnotationMetadata("mutate(create)", "", map[string]string{
		kgtypes.AnnotationKindKey: kgtypes.AnnotationKindFinding,
		kgtypes.AnnotationTierKey: "T2",
	}))
}
