// SPDX-License-Identifier: Apache-2.0

package tools

// fake_graph_caller_ordinal_test.go holds the self-test for the scripted
// GraphCaller's ordinal mutation-failure knob. It lives beside
// fake_graph_caller_test.go rather than inside it because that file had reached
// the repo's per-file length ceiling and a change to the fake could not land
// while it was over.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestFakeGraphCaller_MutateErrOnNth_FailsOnlyNamedCall makes the ordinal knob's
// own semantics falsifiable rather than inferred from whatever downstream test
// happens to use it. The knob exists because the coarser knobs cannot express
// "fail the second of two writes": a blanket error fails both, and keying on
// MutationKind or Target cannot separate two writes that share both.
func TestFakeGraphCaller_MutateErrOnNth_FailsOnlyNamedCall(t *testing.T) {
	wantErr := errors.New("second write failed")
	fc := &fakeGraphCaller{mutateErrOnNth: map[int]error{2: wantErr}}
	plan := &knowledgev1.MutationPlan{Kind: knowledgev1.MutationPlan_MUTATION_KIND_UPDATE}

	_, firstErr := fc.Execute(context.Background(), &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: plan},
	})
	require.NoError(t, firstErr, "the first Mutation Execute must succeed")

	_, secondErr := fc.Execute(context.Background(), &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: plan},
	})
	require.Error(t, secondErr, "the second Mutation Execute must fail")
	assert.Equal(t, wantErr, secondErr, "the seeded error must surface verbatim")

	assert.Len(t, fc.execMutations, 2, "both mutations are recorded — the failing call is still observed")
}
