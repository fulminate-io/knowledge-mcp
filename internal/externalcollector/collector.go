// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
)

// Registration is the RUNTIME record one registered collect is driven from: the
// graph-type record carrying the provider to dial and the tool to call, plus the
// http headers the operator's config entry supplies.
//
// THE HEADERS SIT BESIDE THE RECORD RATHER THAN INSIDE IT, and the reason is
// mechanical rather than a judgement about headers. HttpProvider carries a url
// and nothing else, and this contract changes no proto message; so the headers
// ride here, are set by the transport on every request, and never enter the
// *knowledgev1.GraphTypeDef that providerIdentity digests. No header value is
// ever digested and none is ever persisted. The stdio arm is deliberately
// asymmetric with it: an env KEY does participate in the identity, because it
// travels ON the record, and whether a header key ought to as well is a question
// this contract does not settle.
//
// A registration is built by the config loader from ONE file entry. Nothing
// behavior-only ever reaches RunMCP: a record with no collector, no tool or no
// provider transport is refused by three separate guards below.
type Registration struct {
	// Def carries the family name, the synthesized CollectorSpec and the entry's
	// behavior. It never crosses the wire.
	Def *knowledgev1.GraphTypeDef
	// Headers are sent on EVERY request to an http provider, exactly as written
	// in the entry. The daemon adds none of its own and composes none from its
	// environment or credentials.
	Headers map[string]string
	// Context is the foreign-graph context this entry DECLARES it needs. It is a
	// declaration and never a value: the client fills the collect input's block
	// from it, and an entry declaring nothing receives no block.
	//
	// IT RIDES HERE RATHER THAN ON THE RECORD for the same reason the headers do
	// and one more: this contract changes no proto message, and the declaration
	// is client-only by design — the server holds no domain knowledge about what
	// a collector reads. Nothing of it is ever digested by providerIdentity and
	// nothing of it is ever persisted.
	Context ContextDeclaration
}

// record returns the registration's graph-type record, erroring on a nil
// registration rather than dereferencing it. A nil registration reaching a
// collect means the caller resolved nothing and ran anyway.
func (r *Registration) record() (*knowledgev1.GraphTypeDef, error) {
	if r == nil {
		return nil, fmt.Errorf("custom collector: nil registration")
	}
	return r.Def, nil
}

// RunMCP is the single composition seam the collect dispatch calls for a
// registered (non-builtin) graph type. It proxies the registered MCP provider:
// dial, handshake, list tools, VERIFY the target tool's schemas against the
// collector contract, call the tool with the collect params, validate the
// structured result, and convert it to the in-tree wire payload.
//
// graphName is the collect id — the graph INSTANCE the result lands in. The
// registration name is the FAMILY. Neither comes from the provider: the MCP
// contract carries no graph identity in the result, so there is nothing to pin
// and no way for a provider to write into a graph type it was not registered as.
//
// EVERY REFUSAL WRITES NOTHING. The conversion is the last step and the caller
// ships only what this returns, so a missing tool, a missing or mismatched
// schema, a provider error result or a payload that fails validation all return
// an error with no result at all.
//
// IT RETURNS TWO HALVES BECAUSE THE PROVIDER EMITTED TWO. The CollectResult is
// the collect's own graph and is what the sink ships; the CrossGraphEdges are
// the edges that named another graph, which the CALLER resolves and links —
// this package holds no graph caller and issues no wire work of its own. The
// halves are returned separately rather than joined because kgwire.BatchEdge
// carries no graph selector: an edge folded into the CollectResult could no
// longer be told apart from an in-graph one, and would be written into the
// collect's own graph.
//
// foreign is the DECLARED FOREIGN-GRAPH CONTEXT the caller filled from the
// registration's declaration, or nil when the entry declared none. It is filled
// on the caller's side because that is where the graph seams live; this frame
// only places it on the arguments. IT IS THE INPUT COUNTERPART OF THE TWO
// RETURNS ABOVE, and the two never meet: what the caller declares is resolved
// into an argument BEFORE the provider is called, and what the provider emits
// is resolved out of the result AFTER.
func RunMCP(
	ctx context.Context,
	reg *Registration,
	params map[string]any,
	graphName string,
	foreign *CollectContext,
) (*collectorwire.CollectResult, []CrossGraphEdge, error) {
	def, err := reg.record()
	if err != nil {
		return nil, nil, err
	}
	col, err := collectorSpec(def)
	if err != nil {
		return nil, nil, err
	}
	session, err := dialProvider(ctx, col, reg.Headers)
	if err != nil {
		return nil, nil, err
	}
	defer session.close()

	tool, err := verifiedTool(ctx, session.session, col.GetTool(), reg.Context)
	if err != nil {
		return nil, nil, err
	}

	// The tool arguments are the contract's input shape: the collect id names
	// the instance, and the params object rides through verbatim. They are
	// validated against the schema the PROVIDER advertised before the call, so a
	// provider whose tool needs more than this collect supplies refuses here
	// with a named mismatch rather than inside its own handler.
	args := map[string]any{"id": graphName}
	if len(params) > 0 {
		args["params"] = params
	}
	// THE BLOCK IS GUARDED ON THE DECLARATION, NOT ON ITS CONTENT, which is where
	// it parts company with the params guard above. params is a value the operator
	// supplied, so an empty one is nothing to send; the context block is an ANSWER
	// to a question the entry asked, and a declaring entry whose graphs held
	// nothing is entitled to see that its question was answered rather than to
	// read an omitted key as either possibility. An entry declaring nothing gets
	// a nil here and therefore no key at all, which is what keeps a provider
	// advertising additionalProperties:false working unchanged.
	if foreign != nil {
		args[contextInputProperty] = foreign
	}
	if err := ValidateAgainstAdvertised(tool.Name, "call arguments", tool.InputSchema, args); err != nil {
		return nil, nil, err
	}

	res, err := session.session.CallTool(ctx, &mcp.CallToolParams{Name: tool.Name, Arguments: args})
	if err != nil {
		return nil, nil, fmt.Errorf("custom collector: calling tool %q failed: %w", tool.Name, err)
	}
	if res.IsError {
		return nil, nil, fmt.Errorf("custom collector: tool %q reported an error result: %s", tool.Name, toolErrorText(res))
	}

	envelope, err := DecodeResult(tool.Name, res.StructuredContent)
	if err != nil {
		return nil, nil, err
	}
	// The provider's own advertised schema is the second assertion, and it is a
	// different question from the contract's: the contract says what a collector
	// result must always be, this says whether the provider kept its own word.
	if err := ValidateAgainstAdvertised(tool.Name, "result", tool.OutputSchema, res.StructuredContent); err != nil {
		return nil, nil, err
	}

	collected, err := envelope.ToCollectResult(def.GetName(), graphName)
	if err != nil {
		return nil, nil, err
	}
	// THE PRODUCER STAMPS ARE APPLIED HERE, NOT IN ToCollectResult, because this
	// is the only frame that holds both of their inputs: the registration record
	// (who produces these rows) and the collect params (what was asked for). The
	// converter sees the envelope alone, which carries neither — the same reason
	// the code collector stamps them in its discovery pass rather than in its
	// wire marshal. See identity.go for what each one covers and why the diff
	// refuses a result without them.
	version, err := providerIdentity(def)
	if err != nil {
		return nil, nil, err
	}
	fingerprint, err := discoveryFingerprint(params)
	if err != nil {
		return nil, nil, err
	}
	collected.CollectorOutputVersion = version
	collected.DiscoveryFingerprint = fingerprint
	return collected, envelope.CrossGraphEdges(), nil
}

// VerifyRegistration dials the provider a registration names, asserts the whole
// contract WITHOUT calling the collect tool, and RETURNS THE PROVIDER'S OWN
// DECLARATION: the provider answers a handshake, it lists the registered collect
// tool and the required describe tool, both advertise schemas satisfying their
// contracts, and describe is called once.
//
// IT RETURNS THE DECLARATION BECAUSE THE CALLER WRITES THE ENTRY FROM IT, and
// one dial serves both: `collector add` cannot verify in one connection and then
// fill from a second, because a provider that answered the first and not the
// second would leave an entry half-written from a state nobody observed.
//
// IT RUNS AT `knowledge collector add`, BEFORE THE ENTRY IS WRITTEN, which makes
// installing a collector a network operation. That is the point of a hard schema
// requirement: an entry admitted without it would be a registration whose first
// proof of correctness is a failed collect, and the refusal would name a tool
// nobody could see at the time they wrote it down.
//
// A HAND-EDITED ENTRY PASSES THROUGH NO WRITE PATH, so nothing dials it here.
// It is dialed at its FIRST COLLECT instead, through the same verifiedTool this
// function uses — a hand edit is never a way past the contract check, and that
// now covers the describe requirement as well as the schemas.
func VerifyRegistration(ctx context.Context, reg *Registration) (*Declaration, error) {
	def, err := reg.record()
	if err != nil {
		return nil, err
	}
	col, err := collectorSpec(def)
	if err != nil {
		return nil, err
	}
	session, err := dialProvider(ctx, col, reg.Headers)
	if err != nil {
		return nil, err
	}
	defer session.close()
	if _, err := verifiedTool(ctx, session.session, col.GetTool(), reg.Context); err != nil {
		return nil, err
	}
	return callDescribe(ctx, session.session)
}

// verifiedTool resolves the named tool in the provider's listing and asserts its
// schemas against the contract, asserts that the provider also serves the
// REQUIRED describe tool with a conforming schema, and asserts the entry's own
// DECLARATION against what the collect tool advertises. It is the ONE place both
// the register-time and the collect-time check run, so the two cannot drift into
// different answers.
//
// DESCRIBE IS RE-VERIFIED ON EVERY COLLECT, and that is a decision rather than
// an accident of placement. The requirement is a property of the PROVIDER, not
// of the entry, so a provider downgraded in place after an operator installed it
// must be refused the next time it is dialed; and a hand-edited entry passes
// through no write path at all, so verifying only at `add` would make hand
// editing a way past a required tool. It costs one listing lookup — the listing
// is already fetched for the collect tool — and never a tool call: describe is
// CALLED only at `add`, where its answer is written into the entry.
func verifiedTool(
	ctx context.Context, session *mcp.ClientSession, name string, decl ContextDeclaration,
) (*mcp.Tool, error) {
	tool, err := findTool(ctx, session, name)
	if err != nil {
		return nil, err
	}
	if err := CheckToolSchemas(tool.Name, tool.InputSchema, tool.OutputSchema); err != nil {
		return nil, err
	}
	if _, err := verifiedDescribeTool(ctx, session); err != nil {
		return nil, err
	}
	if err := checkDeclaredContextIsReceivable(tool, decl); err != nil {
		return nil, err
	}
	return tool, nil
}

// checkDeclaredContextIsReceivable refuses an entry that DECLARES foreign-graph
// context against a tool whose advertised input schema does not carry the
// property.
//
// THE ASYMMETRY WITH THE CONTRACT GATE IS THE WHOLE POINT, and it is not an
// inconsistency. The gate treats the property as OPTIONAL so a provider written
// before it existed still registers; this check is about the OPERATOR'S OWN
// ENTRY, which asked for something. A provider that does not advertise the
// property drops the block on decode or refuses the call naming a key its author
// never wrote, and either way the collector computes from nothing while the file
// says otherwise. Declared-only means the declaration decides, so a declaration
// nothing can honor is refused rather than quietly unfulfilled.
func checkDeclaredContextIsReceivable(tool *mcp.Tool, decl ContextDeclaration) error {
	if decl.IsEmpty() {
		return nil
	}
	// CAN THE CLIENT SUPPLY WHAT THIS ENTRY DECLARED, asked here rather than only
	// on the fill path. The two questions on this path are different and both are
	// owed: the check below asks whether the PROVIDER can receive a block, and
	// this asks whether the CLIENT can produce the one the entry named. Without
	// it an entry naming a family this client cannot read installs cleanly and
	// the operator learns about the typo at the next collect, which reads as a
	// broken collect rather than as a bad entry.
	if err := decl.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		return fmt.Errorf("custom collector: tool %q input schema is not JSON: %w", tool.Name, err)
	}
	advertised, err := parseSchema(raw)
	if err != nil {
		return fmt.Errorf("custom collector: tool %q input schema is not a readable JSON Schema: %w", tool.Name, err)
	}
	if _, ok := advertised.Properties[contextInputProperty]; ok {
		return nil
	}
	return fmt.Errorf(
		"custom collector: this collector's entry DECLARES foreign-graph context (%s) and tool %q does not advertise the %q input property, "+
			"so the block would be sent and dropped; either add %q to the tool's input schema (see contract/collector_input.schema.json) or remove the entry's context declaration",
		strings.Join(slices.Sorted(maps.Keys(decl)), ", "), tool.Name, contextInputProperty, contextInputProperty)
}

// collectorSpec extracts the collector half of a registration record, erroring
// on the two shapes a validated record cannot have.
func collectorSpec(def *knowledgev1.GraphTypeDef) (*knowledgev1.CollectorSpec, error) {
	if def == nil {
		return nil, fmt.Errorf("custom collector: nil GraphTypeDef")
	}
	col := def.GetCollector()
	if col == nil {
		return nil, fmt.Errorf("custom collector: graph type %q has no collector spec", def.GetName())
	}
	if col.GetTool() == "" {
		return nil, fmt.Errorf("custom collector: graph type %q names no tool to call", def.GetName())
	}
	return col, nil
}

// toolErrorText renders the text content of a provider's error result so the
// refusal carries the provider's own words. An error result with no text says so
// rather than rendering an empty message.
func toolErrorText(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok && tc.Text != "" {
			return tc.Text
		}
	}
	return "(the provider returned an error result with no text content)"
}
