// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// capturingGc records the ExecuteRequest assembleTestPlanNewRun sends and
// answers with an empty response. It stands in for the WIRE, not for the code
// under test: the payload is marshaled and run through engine.Compile by the
// production function before it ever reaches Execute, so the captured
// NodeBodies are the lowered bodies the server would receive.
type capturingGc struct {
	captured []*knowledgev1.ExecuteRequest
}

func (c *capturingGc) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	c.captured = append(c.captured, req)
	return &knowledgev1.ExecuteResponse{}, nil
}

// TestAssembleTestPlanNewRun_TestRunBodiesCarrySummary pins the summary onto
// every test_run body the run-session create sends. test_run is
// !Summarizable, so the server's create gate requires a non-empty summary and
// refuses the whole batch on the first body without one — which is why a test
// plan with any steps could not open a run session at all.
//
// The assertion is on the CAPTURED request rather than a local struct, so it
// covers the lowering too: a Summary field that the create_batch arm's
// nodeBodyToProto did not read would leave the captured body empty and fail
// here.
func TestAssembleTestPlanNewRun_TestRunBodiesCarrySummary(t *testing.T) {
	steps := []*knowledgev1.Node{
		{Id: "step-1", Type: string(kgtypes.NodeTestStep), SymbolName: "the daemon answers /healthz"},
		{Id: "step-2", Type: string(kgtypes.NodeTestStep), SymbolName: "a cold cache warms under 2s"},
	}
	gc := &capturingGc{}
	var sb strings.Builder

	res := assembleTestPlanNewRun(context.Background(), gc, &sb, steps)
	require.False(t, res.IsError, "the run-session create must succeed: %v", res.Content)

	require.Len(t, gc.captured, 1, "exactly one create_batch Execute fires")
	bodies := gc.captured[0].GetMutation().GetNodeBodies()
	require.Len(t, bodies, len(steps), "one test_run body per step")

	for i, body := range bodies {
		assert.Equal(t, string(kgtypes.NodeTestRun), body.GetType())
		assert.Equal(t, "Test run: "+steps[i].GetSymbolName(), body.GetSummary(),
			"every test_run body must carry the authored summary the server's create gate requires")
		// The name is what the run is LISTED under; catches a fix that writes the
		// summary over the name instead of beside it.
		assert.Equal(t, steps[i].GetSymbolName(), body.GetName(),
			"the name must be unchanged — the summary lands beside it, never over it")
	}
}
