// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
)

// mcphost_test.go — the MCP host driven end to end against a REAL provider on
// both transports: a child process for stdio, an in-process streamable-HTTP
// server for http. Nothing here is a double for the side under test.

// startHTTPStub stands the stub provider up behind an httptest server and
// returns its URL plus a recorder of every request header the client sent.
func startHTTPStub(t *testing.T, mode string) (url string, headers *headerRecorder) {
	t.Helper()
	headers = &headerRecorder{}
	server := newStubServer(mode, "")
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	srv := httptest.NewServer(headers.wrap(handler))
	// ONE server instance rather than one per request, so its sessions are
	// enumerable and closeable at teardown: the SDK's stateful streamable handler
	// exposes no exported close for the sessions it holds, and a stub that left
	// them reading would leak a goroutine per test.
	t.Cleanup(func() {
		for ss := range server.Sessions() {
			_ = ss.Close()
		}
		srv.Close()
	})
	return srv.URL, headers
}

// headerRecorder records the headers of every request that reaches the provider,
// so the no-auth assertion covers the handshake, the tool listing and the call
// rather than whichever one a single-request check happened to see.
type headerRecorder struct {
	mu   sync.Mutex
	seen []http.Header
}

func (h *headerRecorder) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.seen = append(h.seen, r.Header.Clone())
		h.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (h *headerRecorder) all() []http.Header {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]http.Header(nil), h.seen...)
}

// --- R2(a): a conforming provider is dialed, verified, called and admitted ---

// TestRunMCP_ConformingProvider_BothTransports is the matrix's happy cell on
// both transports, and it pins the equality that gives a single contract over
// two transports its meaning: the SAME provider result must produce the SAME
// admitted graph whichever transport carried it.
func TestRunMCP_ConformingProvider_BothTransports(t *testing.T) {
	stdioRes, _, err := RunMCP(context.Background(), stdioDef(t, stubModeConforming), nil, "board", nil)
	require.NoError(t, err)

	url, _ := startHTTPStub(t, stubModeConforming)
	httpRes, _, err := RunMCP(context.Background(), httpDef(url), nil, "board", nil)
	require.NoError(t, err)

	assert.Equal(t, renderGraph(stdioRes), renderGraph(httpRes),
		"one contract over two transports means one admitted graph; a difference here is a transport-specific decode divergence")

	// The family is the registration name and the instance is the collect id —
	// neither is provider-supplied.
	assert.Equal(t, "jira", string(stdioRes.GraphType))
	assert.Equal(t, "board", stdioRes.GraphName)
	assert.Equal(t, "ISSUE-1", stdioRes.Nodes[0].GetId())
	assert.Equal(t, "high", stdioRes.Nodes[0].GetMetadata()["priority"])
	assert.Empty(t, stdioRes.Nodes[1].GetSummary(), "a node with every optional field absent must survive the decode")
	// THE BOUNDARY CELL, asserted rather than merely carried: present-and-empty
	// is a different input from absent, and both must survive the two
	// validations and the strict decode.
	require.Len(t, stdioRes.Nodes, 3)
	boundary := stdioRes.Nodes[2]
	assert.Equal(t, "ISSUE-3", boundary.GetId())
	assert.Empty(t, boundary.GetSummary(), "an empty-string optional must survive as empty")
	assert.Zero(t, boundary.GetStartLine())
	assert.False(t, boundary.GetIsExported())
	// NotNil AND empty: assert.Empty alone is satisfied by nil, which would
	// collapse the very distinction this cell exists for — present-and-empty
	// against absent.
	require.NotNil(t, boundary.GetMetadata(), "a metadata map sent as {} must arrive PRESENT, not dropped to nil")
	assert.Empty(t, boundary.GetMetadata(), "and it must arrive empty")
	require.Len(t, stdioRes.Edges, 1)
	assert.True(t, stdioRes.WalkComplete)
}

// renderGraph flattens an admitted result into a comparable form. Comparing the
// wire structs directly would compare proto-internal state, so this names the
// fields the equality is actually about.
//
// IT RENDERS A NIL METADATA MAP AS `nil` AND AN EMPTY ONE AS `{}`, and that is
// the whole reason renderMetadata exists rather than an inline index. The
// boundary node carries present-and-empty metadata, and the stdio arm asserts
// that property directly (NotNil then Empty). The http arm has no such assertion
// and relies on this equality — but indexing a nil map and indexing an empty map
// both yield "", so a version of this function that read metadata["priority"]
// produced an identical string either way and the cross-transport equality was
// silent on the one property the boundary cell exists for. Distinguishing the
// two here is what makes that cell carry on both transports for free.
func renderGraph(r *collectorwire.CollectResult) string {
	var sb strings.Builder
	sb.WriteString(string(r.GraphType) + "|" + r.GraphName + "|")
	for _, n := range r.Nodes {
		sb.WriteString(n.GetId() + ":" + n.GetType() + ":" + n.GetSummary() + ":" + renderMetadata(n.GetMetadata()) + ";")
	}
	sb.WriteString("|")
	for _, e := range r.Edges {
		sb.WriteString(e.FromID + "->" + e.ToID + ":" + string(e.Type) + ";")
	}
	sb.WriteString("|walk_complete=")
	if r.WalkComplete {
		sb.WriteString("true")
	} else {
		sb.WriteString("false")
	}
	return sb.String()
}

// renderMetadata renders a node's metadata so that ABSENT and PRESENT-BUT-EMPTY
// are different strings. It renders every key in sorted order rather than one
// named key: keying on "priority" alone meant a transport that dropped any OTHER
// key rendered identically, so the equality was narrower than it looked.
func renderMetadata(md map[string]string) string {
	if md == nil {
		return "nil"
	}
	keys := make([]string, 0, len(md))
	for k := range md {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(k + "=" + md[k])
	}
	sb.WriteString("}")
	return sb.String()
}

// --- R2(b)-(f): the schema gate refuses, names the mismatch, and writes nothing ---

// TestRunMCP_SchemaGateRefusals covers the contract's refusal arms on BOTH
// transports. Each asserts a nil result alongside the error: a refusal that
// still returned a graph would be admitted by the caller.
func TestRunMCP_SchemaGateRefusals(t *testing.T) {
	cases := []struct {
		name      string
		mode      string
		wantParts []string
	}{
		{
			"tool the provider does not list",
			stubModeNoSuchTool,
			[]string{defaultStubTool, "does not list", "some_other_tool"},
		},
		{
			"tool advertising no output schema",
			stubModeNoOutputSchema,
			[]string{defaultStubTool, "NO output schema"},
		},
		{
			"output schema missing the completeness assertion",
			stubModeBadOutputSchema,
			[]string{defaultStubTool, "output schema does not satisfy", "walk_complete"},
		},
		{
			"input schema not requiring the collect id",
			stubModeBadInputSchema,
			[]string{defaultStubTool, "input schema does not satisfy", "id"},
		},
	}
	for _, tc := range cases {
		t.Run("stdio/"+tc.name, func(t *testing.T) {
			res, _, err := RunMCP(context.Background(), stdioDef(t, tc.mode), nil, "board", nil)
			requireRefusal(t, res, err, tc.wantParts)
		})
		t.Run("http/"+tc.name, func(t *testing.T) {
			url, _ := startHTTPStub(t, tc.mode)
			res, _, err := RunMCP(context.Background(), httpDef(url), nil, "board", nil)
			requireRefusal(t, res, err, tc.wantParts)
		})
	}
}

// requireRefusal asserts a refusal names what it refused and produced nothing.
func requireRefusal(t *testing.T, res *collectorwire.CollectResult, err error, wantParts []string) {
	t.Helper()
	require.Error(t, err)
	assert.Nil(t, res, "a refused collect must produce NO result for the caller to ship")
	for _, part := range wantParts {
		assert.Contains(t, err.Error(), part)
	}
}

// TestVerifyRegistration_RefusesAtRegisterToo pins that the SAME gate runs at
// registration: a provider that would be refused at collect is refused before
// the record is ever written.
func TestVerifyRegistration_RefusesAtRegisterToo(t *testing.T) {
	_, conformingErr := VerifyRegistration(context.Background(), stdioDef(t, stubModeConforming))
	require.NoError(t, conformingErr)

	for _, mode := range []string{stubModeNoSuchTool, stubModeNoOutputSchema, stubModeBadOutputSchema, stubModeBadInputSchema} {
		t.Run(mode, func(t *testing.T) {
			_, err := VerifyRegistration(context.Background(), stdioDef(t, mode))
			require.Error(t, err)
			assert.Contains(t, err.Error(), defaultStubTool, "the register-time refusal must name the tool")
		})
	}
}

// --- R2(g): the completeness assertion is carried, both ways ---

func TestRunMCP_CompletenessAssertion(t *testing.T) {
	// BOTH TRANSPORTS, both values. The false arm is the one that matters — it is
	// what disables the deletion phase downstream — so it is asserted on each
	// transport rather than on stdio alone.
	for _, tc := range []struct {
		name              string
		complete, partial func(t *testing.T) *Registration
	}{
		{
			"stdio",
			func(t *testing.T) *Registration { return stdioDef(t, stubModeConforming) },
			func(t *testing.T) *Registration { return stdioDef(t, stubModeIncompleteWalk) },
		},
		{
			"http",
			func(t *testing.T) *Registration {
				url, _ := startHTTPStub(t, stubModeConforming)
				return httpDef(url)
			},
			func(t *testing.T) *Registration {
				url, _ := startHTTPStub(t, stubModeIncompleteWalk)
				return httpDef(url)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			complete, _, err := RunMCP(context.Background(), tc.complete(t), nil, "board", nil)
			require.NoError(t, err)
			assert.True(t, complete.WalkComplete)

			incomplete, _, err := RunMCP(context.Background(), tc.partial(t), nil, "board", nil)
			require.NoError(t, err)
			assert.False(t, incomplete.WalkComplete,
				"a provider asserting an incomplete walk must be admitted WITH that assertion — it is what disables the deletion phase")
		})
	}
}

// --- R2: the entry's env block is the child's whole environment ---

// TestRunMCP_StdioEnvironmentIsTheEntrysBlock is R2's positive arm: a name in
// the block arrives at the provider carrying THE BLOCK'S value.
//
// THE ZERO HAS ITS CONTROL IN THE SAME RUN: the block's variable must be present
// through the same instrument that reports the others absent. Without it, every
// absence would be equally explained by a probe that read nothing.
func TestRunMCP_StdioEnvironmentIsTheEntrysBlock(t *testing.T) {
	const sentinel = "block-value-ful1794"
	reg := stdioDefEnv(t, stubModeEnvReport, map[string]string{declaredEnv: sentinel})
	res, _, err := RunMCP(context.Background(), reg, map[string]any{"names": []string{declaredEnv}}, "probe", nil)
	require.NoError(t, err)

	report := envReport(t, res)
	assert.Equal(t, "true", report[declaredEnv]["present"], "a variable the ENTRY supplies must reach the provider")
	assert.Equal(t, sentinel, report[declaredEnv]["value"],
		"and must carry the ENTRY's value — the daemon never held this variable at all")
}

// TestRunMCP_StdioNameAbsentFromTheBlockIsAbsentInTheChild is R2's negative arm.
//
// THE PLANT IS WHAT MAKES THE ABSENCES REAL. Every name below is SET in the
// daemon's own environment and absent from the entry's block, so the only way
// the child can lack them is that the spawn passed nothing of its own. Without
// the plant, an unset host variable would report absent against ANY
// implementation, inheriting or not.
func TestRunMCP_StdioNameAbsentFromTheBlockIsAbsentInTheChild(t *testing.T) {
	planted := []string{canaryEnv, "AWS_ACCESS_KEY_ID", "HOME", "PATH"}
	for _, n := range planted {
		t.Setenv(n, "daemon-value-"+n)
	}

	reg := stdioDefEnv(t, stubModeEnvReport, map[string]string{declaredEnv: "known-positive"})
	names := append([]string{declaredEnv}, planted...)
	res, _, err := RunMCP(context.Background(), reg, map[string]any{"names": names}, "probe", nil)
	require.NoError(t, err)

	report := envReport(t, res)
	require.Equal(t, "true", report[declaredEnv]["present"],
		"the known-positive must arrive, or the absences below are the absence of a working probe")

	for _, name := range planted {
		assert.Equal(t, "false", report[name]["present"],
			"%s is SET in the daemon's environment and the entry does not carry it, so it must NOT reach the child", name)
		_, hasValue := report[name]["value"]
		assert.False(t, hasValue, "an absent variable must carry no value at all")
	}
}

// TestRunMCP_StdioBlockValueBeatsTheDaemonsOwn is the row that makes the two
// above mean what they say.
//
// IT IS THE NEGATIVE CONTROL FOR INHERITANCE: the same NAME is set in the
// daemon's environment and in the entry's block, with DIFFERENT values. An
// implementation that copied from the daemon, or that merged the two, passes
// every other row here and fails this one.
func TestRunMCP_StdioBlockValueBeatsTheDaemonsOwn(t *testing.T) {
	t.Setenv(declaredEnv, "daemon-value-ful1794")
	reg := stdioDefEnv(t, stubModeEnvReport, map[string]string{declaredEnv: "entry-value-ful1794"})
	res, _, err := RunMCP(context.Background(), reg, map[string]any{"names": []string{declaredEnv}}, "probe", nil)
	require.NoError(t, err)

	report := envReport(t, res)
	assert.Equal(t, "true", report[declaredEnv]["present"])
	assert.Equal(t, "entry-value-ful1794", report[declaredEnv]["value"],
		"the child must see the ENTRY's value; seeing the daemon's would mean the daemon is still the source")
}

// TestRunMCP_StdioEmptyValueArrivesPresentAndEmpty pins the distinction the
// retired contract expressed the other way round. There is no daemon lookup any
// more, so "supplied but unset" no longer exists; what remains is a variable the
// block sets to the empty string, which must arrive PRESENT, against one the
// block does not carry at all, which must be ABSENT. They are different inputs
// to a provider that tests for presence.
func TestRunMCP_StdioEmptyValueArrivesPresentAndEmpty(t *testing.T) {
	const absentName = "FUL1794_NOT_IN_THE_BLOCK"
	require.NoError(t, os.Unsetenv(absentName))
	reg := stdioDefEnv(t, stubModeEnvReport, map[string]string{declaredEnv: ""})
	res, _, err := RunMCP(context.Background(), reg, map[string]any{"names": []string{declaredEnv, absentName}}, "probe", nil)
	require.NoError(t, err)

	report := envReport(t, res)
	assert.Equal(t, "true", report[declaredEnv]["present"],
		"a variable the block sets to the empty string is SET, and the provider must see it")
	assert.Empty(t, report[declaredEnv]["value"])
	assert.Equal(t, "false", report[absentName]["present"],
		"and one the block does not carry must be absent — the two are different inputs")
}

// TestChildEnv_EmptyBlockIsAnEmptyEnvironmentNotInheritance pins the
// nil-versus-empty distinction os/exec turns into inheritance. It is a unit
// assertion because the difference is invisible at the child: a nil Env and a
// correctly-built one both "work". The proto getter returns nil for an empty
// repeated field, so this conversion is the whole guard.
func TestChildEnv_EmptyBlockIsAnEmptyEnvironmentNotInheritance(t *testing.T) {
	env := childEnv(nil)
	assert.NotNil(t, env, "a nil Env means INHERIT to os/exec — the child environment must never be one")
	assert.Empty(t, env)
}

// envReport turns the probe's nodes back into name → metadata.
func envReport(t *testing.T, res *collectorwire.CollectResult) map[string]map[string]string {
	t.Helper()
	out := make(map[string]map[string]string, len(res.Nodes))
	for _, n := range res.Nodes {
		out[n.GetId()] = n.GetMetadata()
	}
	return out
}

// TestRunMCP_DanglingEdgeIsAdmitted records what the contract does NOT assert,
// so the absence is a decision rather than an oversight: an edge naming a node
// the result does not carry is CONVERTED, because endpoint resolution belongs to
// the write path (the -1/-1 index sentinel selects by id) and every builtin
// collector emits cross-graph edges the same way.
func TestRunMCP_DanglingEdgeIsAdmitted(t *testing.T) {
	// BOTH TRANSPORTS: the matrix names the cell per transport, and the two
	// bounds converge on one DecodeResult only if nothing transport-specific
	// intervenes — which is the claim, not the premise.
	for _, tc := range []struct {
		name string
		def  func(t *testing.T) *Registration
	}{
		{"stdio", func(t *testing.T) *Registration { return stdioDef(t, stubModeDanglingEdge) }},
		{"http", func(t *testing.T) *Registration {
			url, _ := startHTTPStub(t, stubModeDanglingEdge)
			return httpDef(url)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, _, err := RunMCP(context.Background(), tc.def(t), nil, "board", nil)
			require.NoError(t, err)
			require.Len(t, res.Edges, 1)
			assert.Equal(t, "absent", res.Edges[0].ToID)
			assert.Equal(t, -1, res.Edges[0].FromIdx, "the external contract references endpoints by id")
		})
	}
}

// TestRunMCP_EmptyGraphIsAdmitted is the matrix's zero cell: a provider that
// found nothing returns a conforming, empty result rather than an error.
func TestRunMCP_EmptyGraphIsAdmitted(t *testing.T) {
	for _, tc := range []struct {
		name string
		def  func(t *testing.T) *Registration
	}{
		{"stdio", func(t *testing.T) *Registration { return stdioDef(t, stubModeEmptyGraph) }},
		{"http", func(t *testing.T) *Registration {
			url, _ := startHTTPStub(t, stubModeEmptyGraph)
			return httpDef(url)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, _, err := RunMCP(context.Background(), tc.def(t), nil, "board", nil)
			require.NoError(t, err)
			assert.Empty(t, res.Nodes)
			assert.Empty(t, res.Edges)
		})
	}
}

// TestRunMCP_RecordShapeGuards pins the shapes a runtime registration cannot
// have, at the seam that would otherwise dial nothing.
//
// THE BEHAVIOR-ONLY ROW IS THE ONE THIS CONTRACT ADDS. The record the server
// persists carries a name and a behavior and NO collector, so handing that
// record to the collect path — instead of the runtime record the config loader
// synthesizes — is the mistake the design has to make impossible. It is refused
// here by the same guard that refuses a nil collector.
func TestRunMCP_RecordShapeGuards(t *testing.T) {
	res, _, err := RunMCP(context.Background(), nil, nil, "board", nil)
	requireRefusal(t, res, err, []string{"nil registration"})

	res, _, err = RunMCP(context.Background(), &Registration{}, nil, "board", nil)
	requireRefusal(t, res, err, []string{"nil GraphTypeDef"})

	behaviorOnly := &Registration{Def: &knowledgev1.GraphTypeDef{
		Name:     "jira",
		Behavior: &knowledgev1.BehaviorDefaults{},
	}}
	res, _, err = RunMCP(context.Background(), behaviorOnly, nil, "board", nil)
	requireRefusal(t, res, err, []string{"has no collector spec"})

	noProvider := &Registration{Def: &knowledgev1.GraphTypeDef{Name: "jira", Collector: &knowledgev1.CollectorSpec{Tool: "t"}}}
	res, _, err = RunMCP(context.Background(), noProvider, nil, "board", nil)
	requireRefusal(t, res, err, []string{"names no provider"})

	noTool := &Registration{Def: &knowledgev1.GraphTypeDef{Name: "jira", Collector: &knowledgev1.CollectorSpec{
		Provider: &knowledgev1.CollectorSpec_Http{Http: &knowledgev1.HttpProvider{Url: "https://p.example/mcp"}},
	}}}
	res, _, err = RunMCP(context.Background(), noTool, nil, "board", nil)
	requireRefusal(t, res, err, []string{"names no tool"})
}
