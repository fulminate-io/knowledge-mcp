// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorconfig"
	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collect_custom_test.go — the CUSTOM collect at the dispatch level: a
// registered MCP provider is proxied and its result rides the SAME post-collect
// tail a builtin collect rides.
//
// THE PROVIDER IS REAL on both sides of the seam: an in-process MCP server
// behind an httptest listener, driven through the production dispatch. The
// environment-allowlist arm needs a child process and lives with the host
// (externalcollector); everything here is about what the dispatch does with the
// result.

const (
	customStubTool = "collect_graph"
	// customStubFamily is the registration name — the graph FAMILY a collected
	// result lands in.
	customStubFamily = "jira"
)

// startCustomProvider stands a conforming stub provider up and returns its URL.
// payload is the structuredContent the tool returns.
func startCustomProvider(t *testing.T, payload any) string {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "ful1776-tools-stub", Version: "v1"}, nil)
	server.AddTool(&mcp.Tool{
		Name:         customStubTool,
		Description:  "stub custom collector",
		InputSchema:  contractSchema(t, externalcollector.InputContractJSON()),
		OutputSchema: contractSchema(t, externalcollector.OutputContractJSON()),
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{StructuredContent: payloadFor(payload, req)}, nil
	})
	// THE INBOUND BOUND IS DISABLED ON THE STUB, DELIBERATELY, and the reason is
	// which side each row is about. The SDK's streamable handler bounds an
	// incoming request body at DefaultMaxRequestBodyBytes (4 MiB) when the option
	// is left at zero; a stub carrying that default would make the large-block row
	// measure the STUB rather than what the client sends. Our own server arm is
	// not exempted by this line — the collector framework's handler gets its own
	// row, in its own module, against its own real handler.
	// The describe tool is REQUIRED of every provider on every dial, so every stub
	// in this package serves it: a stub without it would fail each row on the
	// missing tool rather than on the property the row is about. The declaration
	// is the smallest conforming one — what a row needs from it, it sets itself.
	addCustomDescribeTool(server, nil)
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{MaxRequestBodyBytes: -1},
	)
	srv := httptest.NewServer(handler)
	// ONE server instance rather than one per request, so its sessions are
	// enumerable and closeable at teardown. The SDK's stateful streamable handler
	// exposes no exported close for the sessions it holds, and this package's
	// TestMain runs an EMPTY-allowlist goroutine-leak gate, so a stub that left
	// its server sessions reading would fail the whole suite.
	t.Cleanup(func() {
		for ss := range server.Sessions() {
			_ = ss.Close()
		}
		srv.Close()
	})
	return srv.URL
}

// payloadFor lets a test supply either a fixed payload or one derived from the
// call, so the id-passthrough assertion can read what the provider was told.
func payloadFor(payload any, req *mcp.CallToolRequest) any {
	if fn, ok := payload.(func(*mcp.CallToolRequest) any); ok {
		return fn(req)
	}
	return payload
}

// contractSchema decodes a checked-in contract schema for the stub to advertise
// verbatim. Reading the SAME bytes the host validates against is the point: a
// hand-typed copy here would drift from the contract silently.
func contractSchema(t *testing.T, raw []byte) any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

// conformingCustomPayload is one node, one edge, a complete walk.
func conformingCustomPayload() any {
	return map[string]any{
		"nodes":         []any{map[string]any{"id": "ISSUE-1", "type": "issue"}},
		"edges":         []any{},
		"walk_complete": true,
	}
}

// customDef builds the CONFIG ENTRY that registers a family pointing at url. The
// family is customStubFamily for every case here: what varies across these tests
// is the provider's result and the dispatch's handling of it, never the name.
//
// IT IS AN ENTRY RATHER THAN A RECORD because the file IS the registration
// record: the dispatch resolves families from the scoped config files, and
// nothing resolves from the server catalog.
func customDef(url string) namedEntry {
	return namedCustomDef(customStubFamily, url)
}

// namedCustomDef builds an entry under an explicit family name, for the tests
// that need two families at once.
func namedCustomDef(name, url string) namedEntry {
	return namedEntry{name: name, entry: collectorconfig.Entry{
		Type: collectorconfig.TransportHTTP,
		URL:  url,
		Tool: customStubTool,
	}}
}

// blockingCustomProvider stands a provider up whose tool call BLOCKS until
// release is closed, and signals started once the call has been entered. It is
// how a custom collect is held in flight while the test observes the runtime.
//
// CLEANUP ORDER IS LOAD-BEARING: the release close is registered AFTER the
// provider's own cleanup so it runs BEFORE it (t.Cleanup is LIFO). A provider
// server torn down while its handler is still blocked would hang the test.
func blockingCustomProvider(t *testing.T) (url string, started <-chan struct{}, release func()) {
	t.Helper()
	entered := make(chan struct{})
	gate := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	u := startCustomProvider(t, func(*mcp.CallToolRequest) any {
		enterOnce.Do(func() { close(entered) })
		<-gate
		return conformingCustomPayload()
	})
	// The releaser is idempotent: a test that releases mid-body and the cleanup
	// that releases on the way out are both legitimate, and a second close of a
	// channel panics.
	return u, entered, func() { releaseOnce.Do(func() { close(gate) }) }
}

// stubGraphTypeCRUD is a minimal GraphTypeCRUDAPI whose ByName answers from a
// fixed set of GraphTypeDefs. Only ByName is exercised by the collect dispatch.
// It holds a SET rather than one record so a test can register two families and
// observe that the in-flight gate keys on the pair rather than on the name.
type stubGraphTypeCRUD struct {
	defs []*knowledgev1.GraphTypeDef

	mu        sync.Mutex
	upserted  []*knowledgev1.GraphTypeDef
	updateErr error
}

// List returns the seeded catalog PLUS whatever the loader has upserted, which
// is what makes a collect-then-list test observe the real chain rather than two
// halves of it.
func (s *stubGraphTypeCRUD) List(context.Context) ([]*knowledgev1.GraphTypeDef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]*knowledgev1.GraphTypeDef(nil), s.defs...)
	for _, d := range s.upserted {
		out = append(out, d)
	}
	return out, nil
}

func (s *stubGraphTypeCRUD) ByName(_ context.Context, name string) (*knowledgev1.GraphTypeDef, bool, error) {
	for _, d := range s.defs {
		if d.GetName() == name {
			return d, true, nil
		}
	}
	return nil, false, nil
}

// Update records the behavior records the loader upserts, which is how a test
// reads WHAT reached the server without a wire.
func (s *stubGraphTypeCRUD) Update(_ context.Context, d *knowledgev1.GraphTypeDef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upserted = append(s.upserted, d)
	return s.updateErr
}

// lastUpserted returns the most recent behavior record written, or nil.
func (s *stubGraphTypeCRUD) lastUpserted() *knowledgev1.GraphTypeDef {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.upserted) == 0 {
		return nil
	}
	return s.upserted[len(s.upserted)-1]
}

func callCollect(deps ClientDeps, args string) (bool, kgtools.ToolResult) {
	return InterceptCollect(opCtx(), deps, kgtools.CallToolParams{
		Name:      "collect",
		Arguments: json.RawMessage(args),
	})
}

// --- R10: the custom collect runs the builtin tail ---

// TestCustomCollect_ReachesThePipelineWake is R10's core observation. The
// pipeline wake is the tail stage a custom collect never reached under the
// retired exec contract, which returned straight after the sink write.
func TestCustomCollect_ReachesThePipelineWake(t *testing.T) {
	url := startCustomProvider(t, conformingCustomPayload())
	deps := newCustomDeps(t, customDef(url))

	handled, res := callCollect(deps, `{"type":"jira","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	assert.Equal(t, 1, deps.wakeCount(),
		"a custom collect must wake the LLM pipeline exactly as a builtin collect does — the nodes it just uploaded need summarizing")
}

// TestCustomCollect_ShipsUnderTheRegistrationFamilyAndCollectID is R5's
// observable: the registration name is the graph FAMILY and the collect id is
// the INSTANCE. The provider supplies neither, so there is nothing for it to
// cross into another graph type.
func TestCustomCollect_ShipsUnderTheRegistrationFamilyAndCollectID(t *testing.T) {
	url := startCustomProvider(t, conformingCustomPayload())
	deps := newCustomDeps(t, customDef(url))

	handled, res := callCollect(deps, `{"type":"jira","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	got := deps.sink.last()
	require.NotNil(t, got, "the collect must have shipped a result")
	assert.Equal(t, kgtypes.GraphType("jira"), got.GraphType)
	assert.Equal(t, "board", got.GraphName)
	require.Len(t, got.Nodes, 1)
	assert.Equal(t, "ISSUE-1", got.Nodes[0].GetId())
	assert.True(t, got.WalkComplete)
}

// TestCustomCollect_TwoIDsAreTwoInstancesOfOneFamily pins the family/instance
// split: two collects under one registration with different ids produce two
// graph instances inside one family.
func TestCustomCollect_TwoIDsAreTwoInstancesOfOneFamily(t *testing.T) {
	url := startCustomProvider(t, conformingCustomPayload())
	deps := newCustomDeps(t, customDef(url))

	for _, id := range []string{"board-a", "board-b"} {
		handled, res := callCollect(deps, `{"type":"jira","id":"`+id+`"}`)
		require.True(t, handled)
		require.False(t, res.IsError, resultText(res))
	}
	require.Len(t, deps.sink.results, 2)
	assert.Equal(t, kgtypes.GraphType("jira"), deps.sink.results[0].GraphType)
	assert.Equal(t, kgtypes.GraphType("jira"), deps.sink.results[1].GraphType)
	assert.Equal(t, "board-a", deps.sink.results[0].GraphName)
	assert.Equal(t, "board-b", deps.sink.results[1].GraphName)
}

// TestCustomCollect_GateIdentityIsTheRegistrationNameAndCollectID pins BOTH
// halves of the in-flight gate identity. An arm added to one half alone leaves
// the other empty and the gate permanently inert, which is a failure with no
// per-test symptom at all.
func TestCustomCollect_GateIdentityIsTheRegistrationNameAndCollectID(t *testing.T) {
	name, err := CollectGateGraphName("jira", "board", nil, true)
	require.NoError(t, err)
	assert.Equal(t, "board", name, "the name half must derive the collect id")

	family, gateName, err := collectGateGraphIdentity("jira", "board", nil, true)
	require.NoError(t, err)
	assert.Equal(t, kgtypes.GraphType("jira"), family, "the family half must be the registration name")
	assert.Equal(t, "board", gateName)

	// THE CONTROL: the same type WITHOUT the registered-custom flag derives
	// nothing, so the assertions above are evidence of the new arm rather than of
	// a derivation that answers for every string.
	unregisteredName, err := CollectGateGraphName("jira", "board", nil, false)
	require.NoError(t, err)
	assert.Empty(t, unregisteredName, "an unregistered type must derive no name")
}

// TestCustomCollect_EmptyIDIsRefused pins the tightening the family/instance
// rule brings: the collect id names the graph instance, so a custom collect
// without one names no graph and is refused rather than admitted under an empty
// name. The retired exec contract accepted this shape.
func TestCustomCollect_EmptyIDIsRefused(t *testing.T) {
	url := startCustomProvider(t, conformingCustomPayload())
	deps := newCustomDeps(t, customDef(url))

	handled, res := callCollect(deps, `{"type":"jira","id":"","params":{"repo":"x"}}`)
	require.True(t, handled)
	require.True(t, res.IsError, resultText(res))
	assert.Contains(t, resultText(res), "'id' is required")
	assert.Nil(t, deps.sink.last(), "a refused collect must write nothing")
}

// TestCustomCollect_ParamsAndIDReachTheProvider pins the contract's input side
// end to end: the collect id and the params object arrive at the tool.
func TestCustomCollect_ParamsAndIDReachTheProvider(t *testing.T) {
	url := startCustomProvider(t, func(req *mcp.CallToolRequest) any {
		raw, _ := json.Marshal(req.Params.Arguments)
		var args struct {
			ID     string `json:"id"`
			Params struct {
				Repo string `json:"repo"`
			} `json:"params"`
		}
		_ = json.Unmarshal(raw, &args)
		return map[string]any{
			"nodes": []any{map[string]any{
				"id": "echo", "type": "issue",
				"metadata": map[string]any{"got_id": args.ID, "got_repo": args.Params.Repo},
			}},
			"edges":         []any{},
			"walk_complete": true,
		}
	})
	deps := newCustomDeps(t, customDef(url))

	handled, res := callCollect(deps, `{"type":"jira","id":"board","params":{"repo":"acme"}}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	got := deps.sink.last()
	require.NotNil(t, got)
	require.Len(t, got.Nodes, 1)
	assert.Equal(t, "board", got.Nodes[0].GetMetadata()["got_id"])
	assert.Equal(t, "acme", got.Nodes[0].GetMetadata()["got_repo"])
}

// TestCustomCollect_IncompleteWalkRidesToTheSink pins that the completeness
// assertion survives the dispatch, since it is what a later deletion phase reads.
func TestCustomCollect_IncompleteWalkRidesToTheSink(t *testing.T) {
	url := startCustomProvider(t, map[string]any{
		"nodes":         []any{map[string]any{"id": "ISSUE-1", "type": "issue"}},
		"edges":         []any{},
		"walk_complete": false,
	})
	deps := newCustomDeps(t, customDef(url))

	handled, res := callCollect(deps, `{"type":"jira","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	got := deps.sink.last()
	require.NotNil(t, got)
	assert.False(t, got.WalkComplete)
}

// TestCustomCollect_DetachesPastTheSynchronousCap is R10's detach row, driven
// through a custom collect rather than at collectWaitOrDetach directly: a custom
// run that outlives the synchronous cap must return the still-running message
// and keep going, exactly as a code collect does.
func TestCustomCollect_DetachesPastTheSynchronousCap(t *testing.T) {
	url, started, release := blockingCustomProvider(t)
	deps := newCustomDeps(t, customDef(url))
	deps.rt = NewCollectRuntime()
	deps.rt.detachAfter = 50 * time.Millisecond
	t.Cleanup(func() { deps.rt.Stop(10 * time.Second) })
	// Registered LAST so it runs FIRST: the runtime's Stop waits for the in-flight
	// run, which cannot finish while the provider is blocked.
	t.Cleanup(release)

	handled, res := callCollect(deps, `{"type":"`+customStubFamily+`","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))
	assert.Contains(t, resultText(res), "STILL RUNNING",
		"a custom collect past the synchronous cap must return the still-running answer rather than blocking the handler")

	<-started // the run really did reach the provider, so the detach is of a live run
}

// TestCustomCollect_InFlightGateHoldsPerFamilyNotAcrossFamilies is R10's
// in-flight-gate row. A second collect of the SAME registered family and id
// coalesces onto the running one; a collect of a DIFFERENT family carrying the
// same id does not.
//
// The cross-family arm is the one that would pass vacuously without its
// same-family sibling: a gate that coalesced NOTHING would satisfy it alone.
func TestCustomCollect_InFlightGateHoldsPerFamilyNotAcrossFamilies(t *testing.T) {
	blockedURL, started, release := blockingCustomProvider(t)
	fastURL := startCustomProvider(t, conformingCustomPayload())

	deps := newCustomDeps(t,
		namedCustomDef("jira", blockedURL),
		namedCustomDef("linear", fastURL),
	)
	deps.rt = NewCollectRuntime()
	deps.rt.detachAfter = time.Hour // completion never wins; the first call stays in flight
	t.Cleanup(func() { deps.rt.Stop(10 * time.Second) })
	t.Cleanup(release)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = callCollect(deps, `{"type":"jira","id":"board"}`)
	}()
	<-started // the first collect is inside the provider call, so it is genuinely in flight

	handled, res := callCollect(deps, `{"type":"jira","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))
	assert.Contains(t, resultText(res), "already running",
		"a second collect of the same (family, name) must coalesce onto the one in flight")

	handled, res = callCollect(deps, `{"type":"linear","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))
	assert.NotContains(t, resultText(res), "already running",
		"a collect of a DIFFERENT family carrying the same id must not be held by the jira run")
	assert.Contains(t, resultText(res), "Collected linear board")

	release()
	<-done
}
