// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
)

// collect_context_refusal_test.go — R3 AT THE COLLECT: a declaration the client
// cannot satisfy refuses the collect, naming what is wrong with it, and nothing
// is silently omitted.
//
// EACH REFUSAL CARRIES A SAME-RUN POSITIVE CONTROL through the same instrument
// and the same path: the identical collect with a satisfiable declaration
// succeeds and the provider is reached, so a refusal reads as a decision about
// the declaration rather than as a dispatch that could not run at all.
//
// THE REFUSALS FIRE ON THE FILL PATH, not only at load. The config file is not
// the only way an entry reaches a collect — a hand-edited file passes through no
// write path — so the validation the loader would run is run again where the
// block is built.
//
// THE FAMILY VOCABULARY IS NOW THE REGISTRY'S, and that splits these rows in
// two. A refusal about the closed vocabulary (a node field, a narrowing) needs
// no registry; a refusal about a family NAME needs one, and the deps below wire
// a registry holding exactly `aws` so the accepted set is a fact of the fixture
// rather than of the tree.

// registeredFamilyDecl is the standing satisfiable declaration: the registered
// aws family, its resource nodes, one field.
func registeredFamilyDecl() externalcollector.ContextDeclaration {
	return externalcollector.ContextDeclaration{"aws": {
		NodeTypes:  []string{"aws-resource"},
		NodeFields: []string{externalcollector.ContextNodeFieldID},
	}}
}

// declaringDeps stands the echo provider up with a declaring entry, the read
// seam and a registry holding `aws`, and returns the deps plus the echo record.
func declaringDeps(t *testing.T, decl externalcollector.ContextDeclaration) (*customDeps, *echoedArgs) {
	t.Helper()
	url, seen := argEchoProvider(t)
	deps := newCustomDeps(t, declaringEntry(url, decl))
	deps.crud = registeredCRUD("aws")
	deps.graphs = bothFamiliesCaller()
	return deps, seen
}

// bothFamiliesCaller answers the code family and the registered aws family from
// one caller, which is the shape the fill now reads them through.
func bothFamiliesCaller() *contextGraphCaller {
	code, aws := chartFileCaller(), twoResourceCaller()
	code.names["aws"] = aws.names["aws"]
	code.graphs["aws"] = aws.graphs["aws"]
	code.edges = aws.edges
	return code
}

// TestCustomCollect_RefusesAnUnsupplyableDeclaration is R3's refusals, driven
// end to end through the dispatch.
func TestCustomCollect_RefusesAnUnsupplyableDeclaration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		decl    externalcollector.ContextDeclaration
		wantHas []string
	}{
		{
			// THE DECLARATION IS COHERENT ON EVERY OTHER AXIS, deliberately: node
			// fields with no node_types is refused by the closed-vocabulary half
			// FIRST, so a fixture omitting them would measure that refusal instead
			// of the family check this row is for.
			"a graph family nothing registered",
			externalcollector.ContextDeclaration{"linkage": {
				NodeTypes: []string{"proxy"}, NodeFields: []string{"id"},
			}},
			[]string{`"linkage"`, "aws", "code"},
		},
		{
			// THE ROUTE IT MUST NAME IS THE ONE THAT EXISTS. This row asked for
			// "custom_collector", which the refusal used to satisfy by sending the
			// reader to custom_collector(operation:"register") — an operation the
			// tool does not have. Registration is a CONFIG FILE installed with
			// `knowledge collector add`, so that is what the refusal names and what
			// this row pins.
			"a RETIRED graph family",
			externalcollector.ContextDeclaration{"cloud": {NodeFields: []string{"id"}}},
			[]string{`"cloud"`, "retired", "knowledge collector add", "collectors.json"},
		},
		{
			"a node field the projection cannot produce",
			externalcollector.ContextDeclaration{"aws": {NodeFields: []string{"summary"}}},
			[]string{`"summary"`, "symbol_name"},
		},
		{
			"a narrowing that does not apply to the declared family",
			externalcollector.ContextDeclaration{"aws": {PathBasenames: []string{"Chart.yaml"}}},
			[]string{"path_basenames", "code"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, seen := declaringDeps(t, tc.decl)

			handled, res := callCollect(deps, `{"type":"jira","id":"board"}`)
			require.True(t, handled)
			require.True(t, res.IsError, "an unsatisfiable declaration refuses the collect")
			for _, want := range tc.wantHas {
				assert.Contains(t, resultText(res), want,
					"the refusal names the offending value; a bare \"invalid declaration\" is not actionable")
			}
			assert.Empty(t, seen.keys(), "the provider is never reached, so nothing was collected under a wrong slice")
			assert.Nil(t, deps.sink.last(), "and nothing was shipped")
		})
	}

	// THE SAME-RUN POSITIVE CONTROL: the same dispatch, the same seams, a
	// SATISFIABLE declaration, and the collect succeeds with the block on the
	// wire. Without it every refusal above is satisfied by a dispatch that
	// refuses everything.
	t.Run("control: a satisfiable declaration collects", func(t *testing.T) {
		deps, seen := declaringDeps(t, registeredFamilyDecl())

		handled, res := callCollect(deps, `{"type":"jira","id":"board"}`)
		require.True(t, handled)
		require.False(t, res.IsError, resultText(res))
		assert.Equal(t, []string{"context", "id"}, seen.keys())
		assert.NotNil(t, deps.sink.last())
	})
}

// TestCustomCollect_RefusesADeclaredFamilyWithNoSeamToReadItThrough holds the
// no-degradation half. A client constructed without the read seam a declaration
// names is refused loudly; serving an empty block instead would tell the module
// its graphs are empty.
func TestCustomCollect_RefusesADeclaredFamilyWithNoSeamToReadItThrough(t *testing.T) {
	t.Run("a registered family declared, no graph caller", func(t *testing.T) {
		url, seen := argEchoProvider(t)
		deps := newCustomDeps(t, declaringEntry(url, registeredFamilyDecl()))
		deps.crud = registeredCRUD("aws") // the registry IS readable, so the miss is specific.

		handled, res := callCollect(deps, `{"type":"jira","id":"board"}`)
		require.True(t, handled)
		require.True(t, res.IsError)
		assert.Contains(t, resultText(res), "graph caller")
		assert.Empty(t, seen.keys(), "the provider is not called with a block the client could not fill")
	})

	t.Run("code declared, no graph caller", func(t *testing.T) {
		url, seen := argEchoProvider(t)
		decl := externalcollector.ContextDeclaration{externalcollector.ContextFamilyCode: {}}
		deps := newCustomDeps(t, declaringEntry(url, decl))

		handled, res := callCollect(deps, `{"type":"jira","id":"board"}`)
		require.True(t, handled)
		require.True(t, res.IsError)
		assert.Contains(t, resultText(res), "graph caller")
		assert.Empty(t, seen.keys())
	})

	// AN UNREADABLE REGISTRY IS AN ERROR, NOT AN EMPTY SET, and it is refused
	// with a different sentence from "nothing is registered under that name".
	// Collapsing the two would refuse a CORRECT declaration for a confidently
	// wrong reason on any client whose registry read failed.
	t.Run("a registered family declared, registry unreadable", func(t *testing.T) {
		url, seen := argEchoProvider(t)
		deps := newCustomDeps(t, declaringEntry(url, registeredFamilyDecl()))
		deps.crud = &unreadableCRUD{err: errors.New("registry backend unavailable")}
		deps.graphs = bothFamiliesCaller()

		handled, res := callCollect(deps, `{"type":"jira","id":"board"}`)
		require.True(t, handled)
		require.True(t, res.IsError)
		assert.Contains(t, resultText(res), "registry backend unavailable",
			"the read's own words survive the wrap")
		assert.Contains(t, resultText(res), "could not be read",
			"and the sentence says the answer is UNKNOWN, not that the family is unregistered")
		assert.NotContains(t, resultText(res), "this client supplies",
			"an unreadable registry must not render a confident accepted-set")
		assert.Empty(t, seen.keys())
	})

	// THE CODE-ONLY CONTROL for the row above: a declaration naming only `code`
	// is satisfiable WITHOUT the registry, so an unreadable one must not refuse
	// it. Without this leg the fill could read the registry unconditionally and
	// every row above would still pass.
	t.Run("control: a code-only declaration needs no registry at all", func(t *testing.T) {
		url, seen := argEchoProvider(t)
		deps := newCustomDeps(t, declaringEntry(url,
			externalcollector.ContextDeclaration{externalcollector.ContextFamilyCode: {}}))
		deps.crud = &unreadableCRUD{err: errors.New("registry backend unavailable")}
		deps.graphs = bothFamiliesCaller()

		handled, res := callCollect(deps, `{"type":"jira","id":"board"}`)
		require.True(t, handled)
		require.False(t, res.IsError, resultText(res))
		assert.Equal(t, []string{"context", "id"}, seen.keys())
	})
}

// unreadableCRUD fails every registry LIST with a caller-supplied error, while
// answering ByName as a plain miss. It is the fixture for the distinction
// between "not registered" and "whether it is registered is unknown".
//
// It is NOT unreadableCRUD (collect_custom_shadow_test.go): that one fails
// ByName too, which the collect path reads BEFORE the context fill, so a
// refusal observed through it would be the lookup's rather than the fill's.
type unreadableCRUD struct{ err error }

func (f *unreadableCRUD) List(_ context.Context) ([]*knowledgev1.GraphTypeDef, error) {
	return nil, f.err
}

func (f *unreadableCRUD) ByName(_ context.Context, _ string) (*knowledgev1.GraphTypeDef, bool, error) {
	return nil, false, nil
}

func (f *unreadableCRUD) Update(_ context.Context, _ *knowledgev1.GraphTypeDef) error {
	return nil
}

// TestCustomCollect_ReadFailureRefusesTheCollect keeps a failed read from
// reading as an empty graph. A read that errored and a store that held nothing
// are different facts and a module cannot tell them apart from the block.
func TestCustomCollect_ReadFailureRefusesTheCollect(t *testing.T) {
	url, seen := argEchoProvider(t)
	deps := newCustomDeps(t, declaringEntry(url, registeredFamilyDecl()))
	deps.crud = registeredCRUD("aws")
	caller := bothFamiliesCaller()
	caller.err = errors.New("the ingest service is unreachable")
	deps.graphs = caller

	handled, res := callCollect(deps, `{"type":"jira","id":"board"}`)
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, resultText(res), "the ingest service is unreachable",
		"the read's own words survive the wrap")
	assert.Empty(t, seen.keys())

	// THE CONTROL: the same caller without the error serves the block.
	caller.err = nil
	handled, res = callCollect(deps, `{"type":"jira","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))
	assert.Equal(t, []string{"context", "id"}, seen.keys())
}

// TestCustomCollect_UndeclaredEntryReadsNoGraphAtAll is the cost half of
// declared-only, and it is what stops the fill becoming a tax on every custom
// collect: an entry that declares nothing issues NO read.
func TestCustomCollect_UndeclaredEntryReadsNoGraphAtAll(t *testing.T) {
	url, _ := argEchoProvider(t)
	deps := newCustomDeps(t, customDef(url))
	caller := bothFamiliesCaller()
	deps.crud, deps.graphs = registeredCRUD("aws"), caller

	handled, res := callCollect(deps, `{"type":"jira","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	assert.Zero(t, caller.browseCount(), "no declaration, no node browse")
	assert.Zero(t, caller.nameCalls(), "and no graph-name enumeration either")

	// THE CONTROLS for both zeros, in the same run and through the same counters:
	// a declaring entry moves each off zero, so neither zero is a counter nobody
	// wired.
	deps.scope.write(t, declaringEntry(url, externalcollector.ContextDeclaration{
		"aws":                               {NodeTypes: []string{"aws-resource"}, NodeFields: []string{"id"}},
		externalcollector.ContextFamilyCode: {NodeTypes: []string{"file"}, NodeFields: []string{"id"}},
	}))
	handled, res = callCollect(deps, `{"type":"jira","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))
	assert.NotZero(t, caller.browseCount())
	assert.NotZero(t, caller.nameCalls())
}
