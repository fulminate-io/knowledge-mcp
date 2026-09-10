// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// context_registration_test.go — the REGISTRATION-TIME refusal: an entry that
// DECLARES foreign-graph context against a provider whose advertised input
// schema does not carry the property.
//
// WHY IT IS AN ERROR AND NOT A SILENT OMISSION. The block is optional to the
// GATE so a provider that never heard of it is admitted; but an entry that
// declares context has asked for something, and a provider that does not
// advertise the property either drops the block silently on decode or refuses
// the call with a schema error naming a key the operator did not write. Both
// outcomes are a collector computing from nothing while the operator's file says
// otherwise, which is the failure "bad input always errors" names.
//
// IT FIRES AT REGISTRATION AND AT COLLECT, from one place: verifiedTool is the
// single frame `knowledge collector add` and every collect both pass through, so
// the two cannot answer differently.

// startContextStub stands an http provider up whose advertised input schema is
// the caller's, so a row varies exactly the property under test.
func startContextStub(t *testing.T, inputSchema any) string {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "t21-context-stub", Version: "v1"}, nil)
	server.AddTool(&mcp.Tool{
		Name:         defaultStubTool,
		Description:  "context-declaring stub",
		InputSchema:  inputSchema,
		OutputSchema: contractSchemaMap(OutputContractJSON()),
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{StructuredContent: map[string]any{
			"nodes": []any{}, "edges": []any{}, "walk_complete": true,
		}}, nil
	})
	// The describe tool is REQUIRED of every provider, so this stub serves it
	// too: without it every row here would fail on the missing tool rather than
	// on the property it is about.
	server.AddTool(&mcp.Tool{
		Name:         DescribeToolName,
		Description:  "stub declaration",
		InputSchema:  map[string]any{"type": "object"},
		OutputSchema: contractSchemaMap(DescribeContractJSON()),
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{StructuredContent: stubDeclarationDocument()}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(func() {
		for ss := range server.Sessions() {
			_ = ss.Close()
		}
		srv.Close()
	})
	return srv.URL
}

// contextRegistration builds a registration pointing at url and carrying decl.
func contextRegistration(url string, decl ContextDeclaration) *Registration {
	return &Registration{
		Def: &knowledgev1.GraphTypeDef{
			Name: stubFamily,
			Collector: &knowledgev1.CollectorSpec{
				Tool:     defaultStubTool,
				Provider: &knowledgev1.CollectorSpec_Http{Http: &knowledgev1.HttpProvider{Url: url}},
			},
		},
		Context: decl,
	}
}

// schemaWithoutContext is the shape a provider written before this property
// existed advertises: the contract's own document with the context property
// removed.
func schemaWithoutContext(t *testing.T) map[string]any {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal(InputContractJSON(), &doc))
	delete(doc["properties"].(map[string]any), "context")
	return doc
}

// declaresContext is a minimal non-empty declaration. The family it names is a
// FIXTURE CHOICE — these tests are about whether a declaring entry and a
// receiving provider agree, not about which family was declared.
func declaresContext() ContextDeclaration {
	return ContextDeclaration{ContextFamilyCode: {
		NodeTypes:  []string{"file"},
		NodeFields: []string{ContextNodeFieldID},
	}}
}

// TestVerifyRegistration_RefusesADeclaringEntryAgainstANonAdvertisingProvider is
// R3's third refusal, with its positive control in the same test.
func TestVerifyRegistration_RefusesADeclaringEntryAgainstANonAdvertisingProvider(t *testing.T) {
	t.Run("the provider does not advertise the property", func(t *testing.T) {
		url := startContextStub(t, schemaWithoutContext(t))
		_, err := VerifyRegistration(context.Background(), contextRegistration(url, declaresContext()))
		require.Error(t, err, "an entry that declares context needs a provider that can receive it")
		assert.Contains(t, err.Error(), "context",
			"the refusal names the property the operator has to add")
		assert.Contains(t, err.Error(), defaultStubTool,
			"and the tool, which is what an operator edits")
	})

	// THE SAME-RUN POSITIVE CONTROL, same instrument, same path, same declaration:
	// the identical entry against a provider that DOES advertise the property
	// registers clean, so the refusal above is about the property rather than
	// about a stub that cannot register at all.
	t.Run("control: the same entry against an advertising provider registers", func(t *testing.T) {
		url := startContextStub(t, contractSchemaMap(InputContractJSON()))
		_, verifyErr := VerifyRegistration(context.Background(), contextRegistration(url, declaresContext()))
		assert.NoError(t, verifyErr)
	})

	// THE SECOND CONTROL, and it is the compatibility promise itself: a
	// NON-declaring entry against the same non-advertising provider registers
	// clean. Without this row the refusal could be a gate that started rejecting
	// every pre-existing provider, which is precisely what the optional property
	// was chosen to avoid.
	t.Run("control: an entry declaring nothing registers against the same provider", func(t *testing.T) {
		url := startContextStub(t, schemaWithoutContext(t))
		_, verifyErr := VerifyRegistration(context.Background(), contextRegistration(url, nil))
		assert.NoError(t, verifyErr)
	})
}

// TestRunMCP_RefusesADeclaringEntryAgainstANonAdvertisingProvider proves the
// refusal reaches the COLLECT as well, which is the half a hand-edited entry
// takes: a hand edit passes through no write path, so registration never ran for
// it.
func TestRunMCP_RefusesADeclaringEntryAgainstANonAdvertisingProvider(t *testing.T) {
	url := startContextStub(t, schemaWithoutContext(t))
	res, _, err := RunMCP(context.Background(), contextRegistration(url, declaresContext()), nil, "board", nil)
	require.Error(t, err)
	assert.Nil(t, res, "every refusal writes nothing")
	assert.Contains(t, err.Error(), "context")

	// THE CONTROL: the same provider, an entry declaring nothing, collects.
	res, _, err = RunMCP(context.Background(), contextRegistration(url, nil), nil, "board", nil)
	require.NoError(t, err)
	assert.NotNil(t, res)
}

// TestContextDeclaration_ValidateRefusesWhatTheClientCannotSupply is R3's first
// two refusals at the unit they are enforced in, each beside a positive control.
func TestContextDeclaration_ValidateRefusesWhatTheClientCannotSupply(t *testing.T) {
	// A RETIRED FAMILY IS THE ONE FAMILY NAME THIS HALF JUDGES. Which families
	// can be supplied is `code` plus whatever is registered, which this package
	// cannot read; a family that WAS supplyable is knowable without the registry
	// and is refused here so an entry written against the old release fails at the
	// add rather than at the next collect.
	t.Run("a retired graph family", func(t *testing.T) {
		for _, family := range []string{"cloud", "logs", "cicd"} {
			err := ContextDeclaration{family: {NodeFields: []string{ContextNodeFieldID}}}.Validate()
			require.Errorf(t, err, "the retired family %q must be refused", family)
			assert.Contains(t, err.Error(), `"`+family+`"`, "the refusal names the family that is wrong")
			assert.Contains(t, err.Error(), "retired", "and says it was removed rather than never existing")
		}
	})

	// THE CONTROL FOR THE ROW ABOVE. A validator that refused every family name
	// would satisfy it; an UNREGISTERED family must pass HERE and be refused by
	// ValidateFamilies instead, which is the whole point of the split.
	t.Run("control: an unregistered family passes the closed-vocabulary half", func(t *testing.T) {
		assert.NoError(t, ContextDeclaration{"linkage": {
			NodeTypes: []string{"proxy"}, NodeFields: []string{ContextNodeFieldID},
		}}.Validate(),
			"Validate judges the closed vocabulary only; family membership is the registry's answer")
	})

	t.Run("a node field the projection cannot produce", func(t *testing.T) {
		err := ContextDeclaration{ContextFamilyCode: {NodeFields: []string{"summary"}}}.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"summary"`)
		assert.Contains(t, err.Error(), ContextNodeFieldSymbolName, "and lists what it supplies")
	})

	t.Run("an edge field the projection cannot produce", func(t *testing.T) {
		err := ContextDeclaration{ContextFamilyCode: {EdgeFields: []string{"evidence"}}}.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"evidence"`)
	})

	t.Run("fields or metadata keys with no node types to carry them", func(t *testing.T) {
		err := ContextDeclaration{"aws": {
			MetadataKeys: []string{"resource_type"},
		}}.Validate()
		require.Error(t, err, "a declaration asking for node facts with no node to put them on acts on nothing")
		assert.Contains(t, err.Error(), "node_types")

		err = ContextDeclaration{ContextFamilyCode: {
			NodeFields: []string{ContextNodeFieldID},
		}}.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "node_types")
	})

	// THE FORM THIS MUST NOT MAKE ILLEGAL, in the same run: a family declared
	// with nothing at all yields the graph names alone, which is the whole input
	// a membership test over repository names needs. Without this control the
	// refusal above would be indistinguishable from banning the empty form.
	t.Run("control: the graph-names-only declaration stays legal", func(t *testing.T) {
		assert.NoError(t, ContextDeclaration{ContextFamilyCode: {}}.Validate())
		assert.NoError(t, ContextDeclaration{"aws": {}}.Validate())
	})

	t.Run("a narrowing that does not apply to the family", func(t *testing.T) {
		err := ContextDeclaration{"aws": {PathBasenames: []string{"Chart.yaml"}}}.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "path_basenames")
	})

	// EDGE_FIELDS IS NO LONGER A FAMILY-SCOPED NARROWING, and this pins the
	// change rather than leaving its absence to be inferred. It was refused for
	// every family but cloud, because the cloud subgraph fetch was the only read
	// that returned edges; the fill now reads edges for any family it reads nodes
	// for, so declaring them under code — or under a registered type — is a
	// correct declaration a scoped refusal would have rejected.
	t.Run("edge_fields is legal under any family that carries nodes", func(t *testing.T) {
		assert.NoError(t, ContextDeclaration{ContextFamilyCode: {
			NodeTypes:  []string{"file"},
			NodeFields: []string{ContextNodeFieldID},
			EdgeFields: []string{ContextEdgeFieldFromID},
		}}.Validate())
		assert.NoError(t, ContextDeclaration{"aws": {
			NodeTypes:  []string{"aws-resource"},
			NodeFields: []string{ContextNodeFieldID},
			EdgeFields: []string{ContextEdgeFieldFromID, ContextEdgeFieldToID},
		}}.Validate())
	})

	// THE POSITIVE CONTROLS, in the same run and through the same call: every
	// shape the four rows above bend is legal in its own place, so each refusal
	// is about the thing varied.
	t.Run("control: the declarations the two families legitimately carry", func(t *testing.T) {
		assert.NoError(t, ContextDeclaration{
			"aws": {
				NodeTypes:    []string{"aws-resource", "proxy"},
				NodeFields:   []string{ContextNodeFieldID, ContextNodeFieldType, ContextNodeFieldSymbolName},
				MetadataKeys: []string{"resource_type", "foreign_graph", "account", "foreign_id"},
				EdgeFields:   []string{ContextEdgeFieldFromID, ContextEdgeFieldToID},
			},
			ContextFamilyCode: {
				NodeTypes:     []string{"file"},
				NodeFields:    []string{ContextNodeFieldID, ContextNodeFieldFilePath, ContextNodeFieldContent},
				PathBasenames: []string{"Chart.yaml", "Chart.yml"},
			},
		}.Validate())
		assert.NoError(t, ContextDeclaration{}.Validate(), "an empty declaration asks for nothing and is legal")
	})
}

// TestVerifyRegistration_RefusesAnUnsupplyableDeclaration is the half the guide
// promised and the code did not perform.
//
// THE TWO CHECKS ON THE REGISTRATION PATH ANSWER DIFFERENT QUESTIONS and both
// have to run. One asks whether the provider can RECEIVE a block; this asks
// whether the client can SUPPLY the one the entry declared. An entry naming a
// family this client cannot read passed the first and was installed, and the
// operator learned about the typo at the next collect instead of at the add.
func TestVerifyRegistration_RefusesAnUnsupplyableDeclaration(t *testing.T) {
	// The provider DOES advertise the property, so the receivability check
	// passes and only the declaration's own validity is under test.
	url := startContextStub(t, contractSchemaMap(InputContractJSON()))

	for _, tc := range []struct {
		name    string
		decl    ContextDeclaration
		wantHas string
	}{
		{"a retired family", ContextDeclaration{"cloud": {NodeFields: []string{ContextNodeFieldID}}}, `"cloud"`},
		{"a node field it cannot produce", ContextDeclaration{"aws": {
			NodeTypes: []string{"aws-resource"}, NodeFields: []string{"summary"},
		}}, `"summary"`},
		{"a narrowing under the wrong family", ContextDeclaration{"aws": {
			PathBasenames: []string{"Chart.yaml"},
		}}, "path_basenames"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := VerifyRegistration(context.Background(), contextRegistration(url, tc.decl))
			require.Error(t, err, "an entry declaring what this client cannot supply must not install")
			assert.Contains(t, err.Error(), tc.wantHas, "the refusal names the offending value")
		})
	}

	// THE SAME-RUN POSITIVE CONTROL, same call and same provider: a satisfiable
	// declaration registers, so the three refusals are about the declaration
	// rather than about a stub that cannot register at all.
	t.Run("control: a satisfiable declaration registers", func(t *testing.T) {
		_, verifyErr := VerifyRegistration(context.Background(), contextRegistration(url, declaresContext()))
		assert.NoError(t, verifyErr)
	})
}
