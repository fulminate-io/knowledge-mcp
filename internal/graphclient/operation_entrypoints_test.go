// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestOperationEntryPoints is the COMPLETENESS GATE for the client stamping
// seam, and it proves the POSITIVE property: every tool-dispatch entry point
// puts a non-empty, declared operation on the ctx that the rest of the call
// inherits. It is NOT the negative guard — that an unstamped path fails loudly
// is TestUnstampedCoveredRPCFailsInTestBuild, a different property with its own
// test on purpose.
//
// COMPLETENESS-GATE CHOICE, stated explicitly because the plan asked for one:
// this is option (ii), a test driving the entry points, NOT option (i), a census
// script walking the source. Option (ii) keeps the gate in the same language and
// build as the code it guards, so it runs on every `go test` rather than only
// when someone remembers to run a script.
//
// SCOPE, stated so nobody over-reads it: this drives the TOOL-DISPATCH entry
// point exhaustively — every one of the advertised catalog tools, through the
// real dispatchToolCall — because that family is where the overwhelming majority
// of covered RPCs originate. The background loops in other packages (the LLM
// pipeline, the thought propagation loop, the collector upload sink, the hive
// monitor) cannot be reached from this package and each carries its own
// stamping test beside the loop it guards.
func TestOperationEntryPoints(t *testing.T) {
	// Mirrors tools.AllToolSchemas(); restated rather than imported because the
	// tools package depends on this one.
	catalog := []string{
		"query", "traverse", "mutate", "delete", "manage", "sync",
		"thoughts", "search", "file_symbols", "collect",
		"worker", "custom_collector", "ast", "help", "record_decision",
		"analyze_usage", "create_plan", "create_ticket", "create_project",
		"create_research", "create_test_plan", "assemble", "hive",
	}

	declared := make(map[Operation]struct{}, len(AllOperations))
	for _, op := range AllOperations {
		declared[op] = struct{}{}
	}

	for _, tool := range catalog {
		t.Run("tool dispatch stamps: "+tool, func(t *testing.T) {
			// The intercept chain is the FIRST thing dispatchToolCall runs after
			// stamping, so reading ctx here observes exactly what every
			// downstream RPC in this call would inherit. Handling the call inline
			// keeps the test off the network.
			var gotOp Operation
			var gotOK bool
			m := NewMCPClient(MCPClientConfig{
				InterceptChain: func(ctx context.Context, p kgtools.CallToolParams) (kgtools.CallToolParams, bool, kgtools.ToolResult) {
					gotOp, gotOK = OperationFromContext(ctx)
					return p, true, kgtools.TextResult("handled")
				},
			})

			args, err := json.Marshal(map[string]any{})
			require.NoError(t, err)
			resp := m.dispatchToolCall(context.Background(), kgtools.JSONRPCRequest{
				JSONRPC: "2.0",
				Params:  mustRawParams(t, tool, args),
			})
			require.NotNil(t, resp)

			require.True(t, gotOK, "tool %q reached the intercept chain with NO operation on ctx", tool)
			assert.NotEmpty(t, string(gotOp), "tool %q stamped an empty operation", tool)
			_, isDeclared := declared[gotOp]
			assert.True(t, isDeclared,
				"tool %q stamped %q, which is not enumerated in AllOperations", tool, gotOp)
			assert.NotEqual(t, OpToolUnknown, gotOp,
				"tool %q is in the advertised catalog but has no declared term", tool)
		})
	}

	t.Run("the background-loop terms are declared and distinct", func(t *testing.T) {
		// The loops themselves are tested beside their own code; what this file
		// can usefully assert is that the terms they stamp exist and do not
		// collide with a tool term (which would merge two load sources into one
		// bucket and defeat the attribution).
		loopTerms := []Operation{
			OpPipelineGapScan, OpPipelineGraphDiscovery,
			OpPipelineGenPoll, OpPipelineEmbedWriteback,
			OpCorpusDeltaDrain, OpRebuildSegments, OpPropagationReflect,
			OpHiveMonitor, OpCollectChunk, OpCollectFinalize,
			OpCollectFetchSubgraph, OpFileSymbolsSuffixFallback,
			OpAstHydrate, OpTraverseGraphWide,
		}
		toolTerms := make(map[Operation]struct{}, len(toolOperations))
		for _, op := range toolOperations {
			toolTerms[op] = struct{}{}
		}
		for _, op := range loopTerms {
			_, isDeclared := declared[op]
			assert.True(t, isDeclared, "loop term %q is not enumerated in AllOperations", op)
			_, collides := toolTerms[op]
			assert.False(t, collides, "loop term %q collides with a tool term", op)
		}
	})
}

// TestUnstampedCoveredRPCFailsInTestBuild is default-deny HALF A: in a test
// build, a COVERED request that reaches the stamping interceptor with no
// operation in ctx must fail loudly, so the defect is caught before it ships.
// Half B (production stamps the reserved client.unstamped term instead of
// failing a user's call) is covered by TestUnstampedCoveredRPCStampsReserved.
//
// This is NOT subsumed by TestOperationEntryPoints: that one asserts entry
// points DO stamp; this asserts an unstamped path FAILS. A regression could
// break either without touching the other.
func TestUnstampedCoveredRPCFailsInTestBuild(t *testing.T) {
	t.Run("a covered request with no ctx operation fails", func(t *testing.T) {
		// ExecuteRequest carries client_context, so it is covered.
		err := stampOperation(context.Background(), &knowledgev1.ExecuteRequest{}, "/knowledge.v1.EngineService/Execute")
		require.Error(t, err, "an unstamped covered RPC must fail loudly in a test build")
		assert.Contains(t, err.Error(), "no operation in context",
			"the error must say what is wrong")
		assert.Contains(t, err.Error(), "WithOperation",
			"the error must name the fix, not just the symptom")
		assert.Contains(t, err.Error(), "EngineService/Execute",
			"the error must name the offending procedure so the call site is findable")
	})

	t.Run("an UNCOVERED request with no ctx operation does NOT fail", func(t *testing.T) {
		// HealthCheckRequest has no client_context field. Failing here would
		// break credential-less liveness probing, which is the carve-out's whole
		// point — so this is the boundary the loud failure must respect.
		err := stampOperation(context.Background(), &knowledgev1.HealthCheckRequest{}, "/knowledge.v1.HealthService/Check")
		require.NoError(t, err, "an uncovered RPC is uncovered by design and must never trip default-deny")
	})

	t.Run("a stamped covered request does not fail", func(t *testing.T) {
		req := &knowledgev1.ExecuteRequest{}
		ctx := WithOperation(context.Background(), OpSearch)
		require.NoError(t, stampOperation(ctx, req, "/knowledge.v1.EngineService/Execute"))
		assert.Equal(t, string(OpSearch), req.GetClientContext().GetOperation())
	})
}

// TestUnstampedCoveredRPCStampsReserved is default-deny HALF B — the half that
// actually SHIPS. In a production build an unstamped covered request must not
// fail the user's call over an instrumentation defect; it stamps the reserved
// client.unstamped term, which is shape-valid (so the server accepts it) and
// means exactly one thing, so the bug surfaces as its own alarm bucket in the
// per-tag metrics rather than hiding inside normal traffic.
func TestUnstampedCoveredRPCStampsReserved(t *testing.T) {
	// Drive the PRODUCTION branch, which `go test` would otherwise make
	// unreachable — leaving the only half that ships untested.
	orig := inTestBuild
	inTestBuild = func() bool { return false }
	t.Cleanup(func() { inTestBuild = orig })

	req := &knowledgev1.ExecuteRequest{}
	err := stampOperation(context.Background(), req, "/knowledge.v1.EngineService/Execute")
	require.NoError(t, err, "production must not fail a user's call over a stamping defect")
	assert.Equal(t, string(OpUnstamped), req.GetClientContext().GetOperation(),
		"the reserved term must reach the wire so the defect is visible in the metrics")
	assert.NotNil(t, req.GetClientContext(),
		"proto3 presence must be SET — the server tells 'old client, no context' from 'empty operation' by presence")
}

// mustRawParams builds the tools/call params envelope dispatchToolCall decodes.
func mustRawParams(t *testing.T, name string, args json.RawMessage) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(kgtools.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	return raw
}
