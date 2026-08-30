// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestRecipeEmit_RefusesCheckTypeNode pins the check fence on the recipe write
// path, which is the one route into a practice graph that never touches the
// mutate-side admission gate.
//
// The zero-WriteResult assertion is what makes this a FENCE rather than a
// warning: an implementation that logged and shipped anyway would satisfy an
// error-returned assertion on its own.
func TestRecipeEmit_RefusesCheckTypeNode(t *testing.T) {
	target := TargetSpec{GraphType: kgtypes.GraphPractice, Name: "go"}
	res := &Result{
		Nodes: []*knowledgev1.Node{
			{Id: "n1", Type: "pattern"},
			{Id: "n2", Type: "finding", Metadata: map[string]string{
				corpus.MetaCheckType:  string(corpus.CheckAstPattern),
				corpus.MetaDSLPattern: "defer $X.Close()",
			}},
		},
	}

	sink := &captureSink{}
	err := writeResult(context.Background(), sink, target, res)
	require.Error(t, err, "an emission carrying check_type must be refused")
	assert.Contains(t, err.Error(), corpus.MetaCheckType, "the refusal must name the offending key")
	assert.Contains(t, err.Error(), "n2", "the refusal must name the offending node")
	assert.Zero(t, sink.calls, "nothing may reach the Sink once the fence fires")

	// Known-positive control: the SAME shipment with the check metadata removed
	// goes through. Without it, a writeResult broken in some unrelated way would
	// produce the error above for a reason that has nothing to do with the fence.
	res.Nodes[1].Metadata = nil
	require.NoError(t, writeResult(context.Background(), sink, target, res))
	require.Equal(t, 1, sink.calls, "the check-free shipment reaches the Sink")
}
