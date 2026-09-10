// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// stubprovider_test.go — the STUB MCP PROVIDER every host test drives, in both
// the shapes the contract has to be proven against.
//
// THE STDIO PROVIDER IS THIS TEST BINARY, RE-EXECED. The thing under test in the
// environment arm IS the child's environment, so the provider must be a real
// child process; and a child that is this binary needs no compile step, no
// module scaffolding in a temp directory and no network. TestMain checks one
// switch variable and, when it is set, serves MCP over stdin/stdout instead of
// running tests. The switch variable must therefore be CARRIED IN THE ENTRY'S
// ENV BLOCK — which is itself a demonstration that the block is the child's
// whole environment. Note what stopped being necessary with the config-file
// contract: the switch variable is NOT set in the parent process, because
// nothing is copied from the parent any more.
//
// THE PROVIDER WRITES NOTHING TO STDOUT BUT JSON-RPC. stdout is the protocol
// stream for an MCP stdio speaker; a stray print corrupts the framing and
// surfaces as an opaque handshake failure. Every diagnostic below goes to stderr.

const (
	// stubModeEnv switches this binary from "run tests" to "be an MCP provider".
	stubModeEnv = "FUL1776_STUB_MODE"
	// stubToolEnv names the tool the stub advertises; empty means the default.
	stubToolEnv = "FUL1776_STUB_TOOL"
	// declaredEnv / canaryEnv are the environment-allowlist probe's two subjects:
	// one the registration declares, one it does not.
	declaredEnv = "FUL1776_DECLARED"
	canaryEnv   = "FUL1776_CANARY"

	defaultStubTool = "collect_graph"
	// stubFamily is the registration name every case here registers under — the
	// graph FAMILY a collected result lands in.
	stubFamily = "jira"
)

// Stub modes. Each is one shape the contract must be proven against.
const (
	// stubModeConforming advertises contract-satisfying schemas and returns a
	// small conforming graph.
	stubModeConforming = "conforming"
	// stubModeEnvReport is the environment probe: it answers PER-NAME lookups
	// and never serializes its whole environment, because a stub that dumped
	// os.Environ into a result the collect then admitted would write whatever the
	// process held into a graph.
	stubModeEnvReport = "env-report"
	// stubModeNoOutputSchema advertises no output schema at all.
	stubModeNoOutputSchema = "no-output-schema"
	// stubModeBadOutputSchema advertises an output schema missing the
	// completeness assertion.
	stubModeBadOutputSchema = "bad-output-schema"
	// stubModeBadInputSchema advertises an input schema that does not require the
	// collect id.
	stubModeBadInputSchema = "bad-input-schema"
	// stubModeBreaksOwnWord advertises a stricter output schema than the contract
	// and then returns a result violating it.
	stubModeBreaksOwnWord = "breaks-own-word"
	// stubModeToolError returns an error result.
	stubModeToolError = "tool-error"
	// stubModeEmptyNodeType returns a node with an empty type.
	stubModeEmptyNodeType = "empty-node-type"
	// stubModeUnknownField returns a node carrying a field the envelope does not
	// define.
	stubModeUnknownField = "unknown-field"
	// stubModeDanglingEdge returns an edge whose endpoint is not in the result.
	stubModeDanglingEdge = "dangling-edge"
	// stubModeIncompleteWalk returns a conforming result asserting walk_complete
	// false.
	stubModeIncompleteWalk = "incomplete-walk"
	// stubModeEmptyGraph returns zero nodes and zero edges.
	stubModeEmptyGraph = "empty-graph"
	// stubModeOverFormerCap returns a conforming result LARGER than the 64 MiB
	// bound this package used to enforce. It exists because the retired
	// over-cap tests reached their boundary by lowering the cap, and there is no
	// cap left to lower: crossing the former bound with real bytes is the only
	// way to prove it no longer applies.
	stubModeOverFormerCap = "over-former-cap"
	// stubModeExitBeforeHandshake exits non-zero without speaking MCP.
	stubModeExitBeforeHandshake = "exit-before-handshake"
	// stubModeExitMidSession is the DISTINCT arm: this provider boots, answers
	// the handshake, is listed and schema-verified, and only then dies — inside
	// the tool call. It is the likeliest real stdio failure (a collector that
	// starts fine and crashes on the walk) and the one arm on the path where a
	// partial result is conceivable, because the session has already produced a
	// tool listing and passed the schema gate.
	stubModeExitMidSession = "exit-mid-session"
	// stubModeNoSuchTool serves a provider listing a DIFFERENT tool.
	stubModeNoSuchTool = "no-such-tool"
	// stubModeNoDescribe serves the collect tool ALONE: every provider written
	// before the describe tool was required. It is the arm that proves the
	// requirement is a requirement.
	stubModeNoDescribe = "no-describe"
	// stubModeBadDescribeSchema serves a describe tool whose advertised output
	// schema does not satisfy the describe contract.
	stubModeBadDescribeSchema = "bad-describe-schema"
	// stubModeBadDeclaration serves a conforming describe SCHEMA and then returns
	// a declaration that violates it — the provider breaking its own word, on the
	// describe side.
	stubModeBadDeclaration = "bad-declaration"
	// stubModeDescribeError serves a describe tool that reports an error result.
	stubModeDescribeError = "describe-error"
)

// stubArgvMarker is passed as the child's FIRST argument and is what makes a
// child identifiable as a child even when its environment is wrong.
//
// IT EXISTS BECAUSE THE FAILURE MODE IS UNBOUNDED. TestMain switched on the env
// variable alone, so a child that did not receive it fell through to `m.Run()`
// and ran the WHOLE SUITE — which spawns more children, each of which does the
// same. That is a fork bomb, and it is reachable from any change or experiment
// that stops the entry's env block arriving at the child, which is precisely the
// mechanism these tests exist to exercise. Observed once on this branch: a
// deliberate mutation of the env path took the machine to a load average in the
// hundreds inside four minutes.
//
// The marker rides argv rather than the environment for the same reason: argv is
// the one channel this contract does NOT scrub.
const stubArgvMarker = "--ful1794-stub-provider"

// TestMain re-execs this binary as an MCP provider when the switch is set.
//
// A CHILD WITH THE MARKER AND NO MODE EXITS LOUD rather than running the suite:
// the mode is delivered through the very mechanism under test, so its absence is
// the interesting failure and must be reported as one, not amplified.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == stubArgvMarker {
		mode := os.Getenv(stubModeEnv)
		if mode == "" {
			fmt.Fprintf(os.Stderr,
				"stub provider: spawned as a child (%s) with %s UNSET — the entry's env block did not reach it. "+
					"Exiting rather than running the test suite, which would spawn more children.\n",
				stubArgvMarker, stubModeEnv)
			os.Exit(9)
		}
		serveStubProvider(mode)
		return
	}
	// A mode set in the PARENT's environment is not a reason to serve: only a
	// marked child does. The parent runs the suite — unless the external-collector
	// seam is configured in a way that cannot mean what it says, which is refused
	// here rather than absorbed into a green run.
	if err := validateSeamEnvironment(); err != nil {
		fmt.Fprintf(os.Stderr, "external collector seam: %v\n", err)
		os.Exit(8)
	}
	os.Exit(m.Run())
}

// serveStubProvider runs the stub over stdio and never returns.
func serveStubProvider(mode string) {
	if mode == stubModeExitBeforeHandshake {
		fmt.Fprintln(os.Stderr, "stub provider: exiting before the handshake, deliberately")
		os.Exit(3)
	}
	server := newStubServer(mode, os.Getenv(stubToolEnv))
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "stub provider: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// newStubServer builds the MCP server for one stub mode. Shared by the stdio
// child and the in-process http server, so the two transports are proven against
// the SAME provider behavior rather than two hand-written approximations.
func newStubServer(mode, toolName string) *mcp.Server {
	if toolName == "" {
		toolName = defaultStubTool
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "ful1776-stub", Version: "v1"}, nil)
	name := toolName
	if mode == stubModeNoSuchTool {
		name = "some_other_tool"
	}
	s.AddTool(&mcp.Tool{
		Name:         name,
		Description:  "stub custom collector",
		InputSchema:  stubInputSchema(mode),
		OutputSchema: stubOutputSchema(mode),
	}, stubHandler(mode))
	if mode != stubModeNoDescribe {
		s.AddTool(&mcp.Tool{
			Name:         DescribeToolName,
			Description:  "stub declaration",
			InputSchema:  map[string]any{"type": "object"},
			OutputSchema: stubDescribeSchema(mode),
		}, stubDescribeHandler(mode))
	}
	return s
}

// stubInputSchema returns the input schema this mode advertises.
func stubInputSchema(mode string) any {
	if mode == stubModeBadInputSchema {
		// Type object, but it does not require the collect id — the one thing the
		// contract's input side insists on.
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{"params": map[string]any{"type": "object"}},
		}
	}
	return contractSchemaMap(InputContractJSON())
}

// stubOutputSchema returns the output schema this mode advertises.
func stubOutputSchema(mode string) any {
	switch mode {
	case stubModeNoOutputSchema:
		return nil
	case stubModeBadOutputSchema:
		// Conforming but for the completeness assertion, which is exactly the
		// omission the contract exists to refuse.
		out := contractSchemaMap(OutputContractJSON())
		out["required"] = []any{"nodes", "edges"}
		delete(out["properties"].(map[string]any), "walk_complete")
		return out
	case stubModeBreaksOwnWord:
		// STRICTER than the contract: every node must also carry a summary. The
		// handler then returns one that does not.
		out := contractSchemaMap(OutputContractJSON())
		props := out["properties"].(map[string]any)
		nodes := props["nodes"].(map[string]any)
		items := nodes["items"].(map[string]any)
		items["required"] = []any{"id", "type", "summary"}
		return out
	default:
		return contractSchemaMap(OutputContractJSON())
	}
}

// contractSchemaMap decodes a checked-in contract schema into a mutable map so a
// stub can advertise it verbatim or bend one keyword of it.
func contractSchemaMap(raw []byte) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		panic("stub provider: the checked-in contract schema does not decode: " + err.Error())
	}
	return m
}

// stubHandler returns the tool handler for one mode. The raw ToolHandler form is
// deliberate: the SDK's generic AddTool would validate the result against the
// advertised output schema server-side, which would make the "provider breaks its
// own word" arm untestable.
func stubHandler(mode string) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if mode == stubModeExitMidSession {
			// Dies WITH THE CALL IN FLIGHT. os.Exit rather than returning an
			// error: the arm under test is a provider that stops existing, not
			// one that reports a failure, and those reach different code.
			fmt.Fprintln(os.Stderr, "stub provider: exiting mid-session, deliberately")
			os.Exit(7)
		}
		if mode == stubModeToolError {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "the stub provider refused this collect"}},
			}, nil
		}
		return &mcp.CallToolResult{StructuredContent: stubPayload(mode, req)}, nil
	}
}

// stubPayload builds the structuredContent for one mode.
func stubPayload(mode string, req *mcp.CallToolRequest) any {
	switch mode {
	case stubModeEnvReport:
		return envReportPayload(req)
	case stubModeEmptyNodeType:
		return map[string]any{"nodes": []any{map[string]any{"id": "n1", "type": ""}}, "edges": []any{}, "walk_complete": true}
	case stubModeUnknownField:
		return map[string]any{
			"nodes":         []any{map[string]any{"id": "n1", "type": "issue", "summry": "typo'd key"}},
			"edges":         []any{},
			"walk_complete": true,
		}
	case stubModeDanglingEdge:
		return map[string]any{
			"nodes":         []any{map[string]any{"id": "n1", "type": "issue"}},
			"edges":         []any{map[string]any{"from_id": "n1", "to_id": "absent", "type": "blocks"}},
			"walk_complete": true,
		}
	case stubModeIncompleteWalk:
		return map[string]any{"nodes": []any{map[string]any{"id": "n1", "type": "issue"}}, "edges": []any{}, "walk_complete": false}
	case stubModeEmptyGraph:
		return map[string]any{"nodes": []any{}, "edges": []any{}, "walk_complete": true}
	case stubModeOverFormerCap:
		return overFormerCapPayload()
	case stubModeBreaksOwnWord:
		// No summary, which the schema this stub advertised requires.
		return map[string]any{"nodes": []any{map[string]any{"id": "n1", "type": "issue"}}, "edges": []any{}, "walk_complete": true}
	default:
		return conformingPayload()
	}
}

// conformingPayload is the reference result: two nodes, one edge, a complete
// walk, every optional field exercised on one node and absent on the other.
func conformingPayload() any {
	return map[string]any{
		"nodes": []any{
			map[string]any{
				"id": "ISSUE-1", "type": "issue",
				"symbol_name": "Login broken", "file_path": "src/login.go", "language": "go",
				"start_line": 10, "end_line": 20, "content": "body", "signature": "sig",
				"summary": "one line", "description": "longer", "source": "stub", "status": "open",
				"keywords": "login auth", "is_exported": true,
				"metadata": map[string]any{"priority": "high"},
			},
			map[string]any{"id": "ISSUE-2", "type": "issue"},
			// THE BOUNDARY COLUMN of the transport-by-result-shape matrix, and it
			// is a different input from ISSUE-2 above rather than a duplicate of
			// it: ISSUE-2 OMITS its optional fields, this one carries them
			// PRESENT AND EMPTY. Absent and empty-valued are distinct inputs to
			// the JSON Schema validation and to the DisallowUnknownFields decode,
			// and the change runs two independent validations over this payload.
			// Living in conformingPayload puts the cell on BOTH transports at
			// once and inside the byte-identical equality between them.
			map[string]any{
				"id": "ISSUE-3", "type": "issue",
				"symbol_name": "", "file_path": "", "language": "", "content": "",
				"signature": "", "summary": "", "description": "", "source": "",
				"status": "", "keywords": "", "start_line": 0, "end_line": 0,
				"is_exported": false,
				"metadata":    map[string]any{},
			},
		},
		"edges":         []any{map[string]any{"from_id": "ISSUE-1", "to_id": "ISSUE-2", "type": "blocks"}},
		"walk_complete": true,
	}
}

// envReportPayload answers the environment probe. It reports ONLY the names the
// caller asked about, one node each, carrying whether the variable is present in
// this child's environment and — for a present one — its value.
//
// IT NEVER SERIALIZES THE WHOLE ENVIRONMENT. os.Environ returns everything, so a
// stub that dumped it into a result the collect then admitted would write
// whatever the process held into a graph — the very shape this ticket exists to
// close, reproduced inside its own test.
func envReportPayload(req *mcp.CallToolRequest) any {
	names := askedNames(req)
	nodes := make([]any, 0, len(names))
	for _, name := range names {
		value, present := os.LookupEnv(name)
		meta := map[string]any{"present": fmt.Sprintf("%v", present)}
		if present {
			meta["value"] = value
		}
		nodes = append(nodes, map[string]any{"id": name, "type": "env_var", "metadata": meta})
	}
	return map[string]any{"nodes": nodes, "edges": []any{}, "walk_complete": true}
}

// askedNames pulls the requested variable names out of the call arguments.
func askedNames(req *mcp.CallToolRequest) []string {
	raw, err := json.Marshal(req.Params.Arguments)
	if err != nil {
		return nil
	}
	var args struct {
		Params struct {
			Names []string `json:"names"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil
	}
	return args.Params.Names
}

// stdioDef builds the runtime registration whose stdio provider is this test
// binary re-execed in the named mode, with the stub's switch variable as the
// child's WHOLE environment.
func stdioDef(t *testing.T, mode string) *Registration {
	t.Helper()
	return stdioDefEnv(t, mode, nil)
}

// stdioDefEnv is stdioDef with extra environment for the child. The switch
// variable is added here rather than by the caller: a case whose block omitted
// it would spawn the test suite instead of the stub, which is the block proving
// itself.
//
// THE PAIRS ARE EMITTED SORTED, exactly as the config loader emits them, because
// the collector identity digests this slice and Go map iteration is randomized.
func stdioDefEnv(t *testing.T, mode string, env map[string]string) *Registration {
	t.Helper()
	return stdioDefFor(t, topLevelTestName(t.Name()), mode, env)
}

// sortedEnvPairs renders an env block the way the config loader does: NAME=value
// in sorted key order.
func sortedEnvPairs(block map[string]string) []string {
	keys := make([]string, 0, len(block))
	for k := range block {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+block[k])
	}
	return out
}

// httpDef builds the runtime registration pointing at an in-process
// streamable-HTTP server serving the SAME stub, so both transports are proven
// against one provider behavior. The family is stubFamily for every case here:
// what varies across these tests is the PROVIDER's behavior, never the entry
// name.
func httpDef(url string) *Registration {
	return httpDefHeaders(url, nil)
}

// httpDefHeaders is httpDef carrying the entry's headers, which ride beside the
// record rather than on it.
func httpDefHeaders(url string, headers map[string]string) *Registration {
	return &Registration{
		Def: &knowledgev1.GraphTypeDef{
			Name: stubFamily,
			Collector: &knowledgev1.CollectorSpec{
				Tool:     defaultStubTool,
				Provider: &knowledgev1.CollectorSpec_Http{Http: &knowledgev1.HttpProvider{Url: url}},
			},
		},
		Headers: headers,
	}
}

// overFormerCapPayload builds the conforming result stubModeOverFormerCap
// serves: 68 MiB of node bodies, over the 64 MiB bound this package retired.
// The shape is duplicated from the test-side builder deliberately — this half
// runs in the re-execed CHILD process, which shares no testing.T with the
// parent and cannot call a helper that takes one.
func overFormerCapPayload() any {
	const (
		bodyLen  = 1 << 20
		nodeRows = 68
	)
	body := strings.Repeat("x", bodyLen)
	nodes := make([]any, 0, nodeRows)
	edges := make([]any, 0, nodeRows-1)
	for i := range nodeRows {
		nodes = append(nodes, map[string]any{
			"id": fmt.Sprintf("big-%d", i), "type": "blob", "content": body,
		})
		if i > 0 {
			edges = append(edges, map[string]any{
				"from_id": fmt.Sprintf("big-%d", i-1),
				"to_id":   fmt.Sprintf("big-%d", i),
				"type":    "NEXT",
			})
		}
	}
	return map[string]any{"nodes": nodes, "edges": edges, "walk_complete": true}
}
