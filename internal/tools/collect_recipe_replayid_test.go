// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/pdfcollector"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// replayNameCaller routes Execute calls on graph + "/" + name and records every
// key it was asked for.
//
// A NEW FAKE IS REQUIRED AND THE REASON IS THE SUBJECT. recipeRoutingCaller
// routes on the graph TYPE alone and answers every NAME identically, so it
// cannot witness a name mismatch — which is the entire property under test here.
// Everything else reuses recipeDeps and recipeCaptureSink from
// collect_recipe_test.go.
type replayNameCaller struct {
	nodesByKey map[string][]*knowledgev1.Node
	edgesByKey map[string][]*knowledgev1.Edge

	// readKeys records every graph/name pair asked for, in order. An empty slice
	// is what proves a run never read anything, which a rows-only assertion would
	// not distinguish from a run that read the right graph and matched nothing.
	readKeys []string
}

func (c *replayNameCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if req.GetMutation() != nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	key := req.GetTarget().GetGraph() + "/" + req.GetTarget().GetName()
	c.readKeys = append(c.readKeys, key)
	resp := &knowledgev1.ExecuteResponse{}
	if req.GetQuery().GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		resp.Edges = c.edgesByKey[key]
		return resp, nil
	}
	resp.Nodes = c.nodesByKey[key]
	return resp, nil
}

// replayFixturePath is the shape a pdf collect id really takes: an absolute
// path. pdfcollector.Collect refuses a non-absolute id, which is why
// filepath.IsAbs is an exact discriminator rather than a heuristic.
const replayFixturePath = "/tmp/replay-fixture/Designing Fixtures.pdf"

const replayBody = `select section
emit heading {
    name := section.symbol_name
}
`

// replayCallerFor builds a caller serving a two-node pdf document under the
// SLUG key only.
//
// THE EXPECTATION IS EXTERNAL. The slug is derived with
// pdfcollector.SourceSlug, never written as a literal, so the test cannot agree
// with the resolver by construction: if either side changed, the keyed fake
// would serve an empty graph and the run would be refused.
func replayCallerFor(path string) (*replayNameCaller, string) {
	slug := pdfcollector.SourceSlug(path)
	key := string(kgtypes.GraphPDFRaw) + "/" + slug
	return &replayNameCaller{
		nodesByKey: map[string][]*knowledgev1.Node{
			key: {
				{Id: "d1", Type: "document", SymbolName: "Designing Fixtures", Source: "pdf-collect",
					Metadata: map[string]string{"source": "pdf", "path": path}},
				{Id: "s1", Type: "section", SymbolName: "Event-Driven Services",
					Content: "Event-Driven Services", Source: "pdf-collect",
					Metadata: map[string]string{"source": "pdf", "position": "0", "page_first": "10"}},
			},
		},
		edgesByKey: map[string][]*knowledgev1.Edge{
			key: {{FromId: "d1", ToId: "s1", Type: string(kgtypes.EdgeContains), Evidence: `{"position":"0"}`}},
		},
	}, slug
}

func replayExtractParams(t *testing.T, id string) kgtools.CallToolParams {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"type":        "pdf",
		"id":          id,
		"transformer": "recipe",
		"extract":     true,
		"recipe_body": replayBody,
	})
	require.NoError(t, err)
	return kgtools.CallToolParams{Name: "collect", Arguments: raw}
}

// TestCollectRecipe_ReplayAcceptsAbsolutePathAndSlug drives the SAME fixture
// document through both id forms and requires they are indistinguishable.
func TestCollectRecipe_ReplayAcceptsAbsolutePathAndSlug(t *testing.T) {
	byPathCaller, slug := replayCallerFor(replayFixturePath)
	bySlugCaller, _ := replayCallerFor(replayFixturePath)

	handled, byPath := InterceptCollect(opCtx(),
		&recipeDeps{sink: &recipeCaptureSink{}, gc: byPathCaller}, replayExtractParams(t, replayFixturePath))
	require.True(t, handled)
	require.False(t, byPath.IsError, "the absolute-path replay must not error: %s", resultText(byPath))

	handled, bySlug := InterceptCollect(opCtx(),
		&recipeDeps{sink: &recipeCaptureSink{}, gc: bySlugCaller}, replayExtractParams(t, slug))
	require.True(t, handled)
	require.False(t, bySlug.IsError, "the slug replay must not error: %s", resultText(bySlug))

	assert.Equal(t, resultText(bySlug), resultText(byPath),
		"the two id forms must render byte-identically, header included")
	assert.Contains(t, resultText(byPath), "source=pdf/"+slug,
		"the path run's header must name the resolved slug so the caller can copy it")

	require.NotEmpty(t, byPathCaller.readKeys,
		"a run that read no graph at all must not be able to pass")
	for _, k := range byPathCaller.readKeys {
		assert.Equal(t, string(kgtypes.GraphPDFRaw)+"/"+slug, k,
			"every graph the path run read must be the slug-keyed one")
	}
}

// TestCollectRecipe_ReplayByPath_MissNamesBothFormsAndModules drives a path for
// a document nobody collected and requires the failure to carry BOTH halves:
// the interpreter's own cause survives AND the remedy arrives.
func TestCollectRecipe_ReplayByPath_MissNamesBothFormsAndModules(t *testing.T) {
	const missing = "/tmp/replay-fixture/Never Collected.pdf"
	empty := &replayNameCaller{}

	handled, res := InterceptCollect(opCtx(),
		&recipeDeps{sink: &recipeCaptureSink{}, gc: empty}, replayExtractParams(t, missing))
	require.True(t, handled)
	require.True(t, res.IsError, "a replay naming an uncollected document must be an error")

	msg := resultText(res)
	assert.Contains(t, msg, "not in the source graph", "the interpreter's own cause must survive")
	assert.Contains(t, msg, missing, "the message must name the path the caller typed")
	assert.Contains(t, msg, pdfcollector.SourceSlug(missing), "the message must name the slug it resolved to")
	assert.Contains(t, msg, `mode:"modules"`, "the message must name the surface that lists collected graphs")
	assert.Contains(t, msg, "both forms are accepted",
		"the message must say both id forms are accepted, got: %s", msg)
}
