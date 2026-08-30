// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// recordingStatsRPC captures the last QueryPlan passed to Execute and the last
// StatsRequest, and hands back canned responses, so the lowered plan shape can
// be asserted (the OP_PREFIX predicate guard) without a live server.
// truncated / total ride the canned response so a subtest can drive the server's
// truncation verdict and the corpus total independently of the node count — the
// two things the page itself cannot tell a reader.
type recordingStatsRPC struct {
	lastQuery *knowledgev1.QueryPlan
	nodes     []*knowledgev1.Node
	stats     *knowledgev1.GraphStats
	truncated bool
	total     int64
}

func (r *recordingStatsRPC) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	r.lastQuery = req.GetQuery()
	resp := enginetest.ResponseWithNodes(r.nodes...)
	resp.Truncated = r.truncated
	resp.Total = r.total
	return resp, nil
}

func (r *recordingStatsRPC) Stats(_ context.Context, _ *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	return &knowledgev1.StatsResponse{GraphStats: r.stats}, nil
}

// TestResourceBrowse_OPPrefixPredicate is the BOUNDED guard: the resource_type
// browse compiles to an Execute query carrying the OP_PREFIX metadata predicate
// on resource_type — NOT a client-side full-scan + filter. Asserted on the
// lowered QueryPlan for both cloud and cicd.
func TestResourceBrowse_OPPrefixPredicate(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind resourceGraphKind
	}{
		{"cloud", cloudGraphKind},
		{"cicd", cicdGraphKind},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordingStatsRPC{nodes: []*knowledgev1.Node{{Id: "r1", SymbolName: "res", Metadata: map[string]string{"resource_type": "ec2:instance"}}}}
			a := queryArgs{Graph: tc.kind.graph, Account: "acme", ResourceType: "ec2"}
			res := resourceBrowse(context.Background(), rec.Execute, tc.kind, a)
			require.False(t, res.IsError, textBodyTools(res))

			require.NotNil(t, rec.lastQuery)
			sel := rec.lastQuery.GetSelection()
			require.NotNil(t, sel)
			assert.Equal(t, string(tc.kind.nodeType), sel.GetNodeType())
			preds := sel.GetMetadataPredicates()
			require.Len(t, preds, 1, "browse must push a single metadata predicate, not iterate client-side")
			assert.Equal(t, "resource_type", preds[0].GetKey())
			assert.Equal(t, knowledgev1.MetadataPredicate_OP_PREFIX, preds[0].GetOp())
			assert.Equal(t, "ec2", preds[0].GetValue())
		})
	}
}

// TestResourceBrowse_NoResourceType asserts a plain browse carries the node-type
// selection but NO metadata predicate.
func TestResourceBrowse_NoResourceType(t *testing.T) {
	rec := &recordingStatsRPC{nodes: []*knowledgev1.Node{{Id: "r1", SymbolName: "res"}}}
	a := queryArgs{Graph: "cloud", Account: "acme"}
	res := resourceBrowse(context.Background(), rec.Execute, cloudGraphKind, a)
	require.False(t, res.IsError)
	require.NotNil(t, rec.lastQuery.GetSelection())
	assert.Empty(t, rec.lastQuery.GetSelection().GetMetadataPredicates())
}

// TestResourceGetNode_BothKinds drives id getNode for cloud (Region) and cicd
// (Provider), asserting the node render headers + secondary lines.
func TestResourceGetNode_BothKinds(t *testing.T) {
	node := knowledgev1.Node{
		Id: "r1", SymbolName: "res", Metadata: map[string]string{"resource_type": "ec2:instance", "region": "us-east-1", "provider": "aws"},
	}
	t.Run("cloud", func(t *testing.T) {
		rec := &recordingStatsRPC{nodes: []*knowledgev1.Node{&node}}
		res := resourceGetNode(context.Background(), rec.Execute, cloudGraphKind, queryArgs{Graph: "cloud", Account: "acme", ID: "r1"})
		assert.Contains(t, textBodyTools(res), "## Cloud Resource [acme]")
		assert.Contains(t, textBodyTools(res), "Region: us-east-1")
	})
	t.Run("cicd", func(t *testing.T) {
		rec := &recordingStatsRPC{nodes: []*knowledgev1.Node{&node}}
		res := resourceGetNode(context.Background(), rec.Execute, cicdGraphKind, queryArgs{Graph: "cicd", Account: "acme", ID: "r1"})
		assert.Contains(t, textBodyTools(res), "## CI/CD Resource [acme]")
		assert.Contains(t, textBodyTools(res), "Provider: aws")
	})
}

// TestResourceStats_BothKinds drives mode=stats for both graphs, asserting the
// per-account header + the shared stats breakdown body.
func TestResourceStats_BothKinds(t *testing.T) {
	stats := &knowledgev1.GraphStats{NodeCount: 4, EdgeCount: 2, NodesByType: map[string]int64{"cloud-resource": 4}}
	t.Run("cloud", func(t *testing.T) {
		rec := &recordingStatsRPC{stats: stats}
		res := resourceStats(context.Background(), rec, cloudGraphKind, queryArgs{Graph: "cloud", Account: "acme", Mode: "stats"})
		body := textBodyTools(res)
		assert.Contains(t, body, "## Cloud Graph: acme")
		assert.Contains(t, body, "Nodes: 4")
		assert.Contains(t, body, "### Nodes by Type")
	})
	t.Run("cicd", func(t *testing.T) {
		rec := &recordingStatsRPC{stats: stats}
		res := resourceStats(context.Background(), rec, cicdGraphKind, queryArgs{Graph: "cicd", Account: "acme", Mode: "stats"})
		assert.Contains(t, textBodyTools(res), "## CI/CD Graph: acme")
	})
}

// TestResourceStats_JSON asserts the format:"json" branch for BOTH resource
// kinds (cloud + cicd) returns the structured shape with the right graph label,
// account instance key, counts, and type maps. The text path stays covered by
// TestResourceStats_BothKinds.
func TestResourceStats_JSON(t *testing.T) {
	stats := &knowledgev1.GraphStats{
		NodeCount: 4, EdgeCount: 2, BinaryVectorCount: 1,
		NodesByType: map[string]int64{"cloud-resource": 4},
		EdgesByType: map[string]int64{"depends-on": 2},
	}
	for _, tc := range []struct {
		name string
		kind resourceGraphKind
	}{
		{"cloud", cloudGraphKind},
		{"cicd", cicdGraphKind},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordingStatsRPC{stats: stats}
			res := resourceStats(context.Background(), rec, tc.kind, queryArgs{Graph: tc.kind.graph, Account: "acme", Mode: "stats", Format: "json"})
			require.False(t, res.IsError, textBodyTools(res))

			var payload map[string]any
			require.NoError(t, json.Unmarshal([]byte(textBodyTools(res)), &payload), "body must be valid JSON")
			assert.Equal(t, tc.kind.graph, payload["graph"])
			assert.Equal(t, "acme", payload["account"])
			assert.EqualValues(t, 4, payload["node_count"])
			assert.EqualValues(t, 2, payload["edge_count"])
			assert.EqualValues(t, 1, payload["binary_vector_count"])
			nbt, ok := payload["nodes_by_type"].(map[string]any)
			require.True(t, ok, "nodes_by_type is an object")
			assert.EqualValues(t, 4, nbt["cloud-resource"])
			ebt, ok := payload["edges_by_type"].(map[string]any)
			require.True(t, ok, "edges_by_type is an object")
			assert.EqualValues(t, 2, ebt["depends-on"])
		})
	}
}

// typeSampleFake answers the per-type sample reads fetchTypeSamples issues,
// keyed on the node type in the plan: a type in failures errors, a type in
// empties returns zero rows, and every other type returns one sample node.
// Driving the three outcomes separately is the whole point — the defect was that
// a broken read and an empty type were indistinguishable in the render.
type typeSampleFake struct {
	stats    *knowledgev1.GraphStats
	failures map[string]bool
	empties  map[string]bool
}

func (f *typeSampleFake) Stats(_ context.Context, _ *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	return &knowledgev1.StatsResponse{GraphStats: f.stats}, nil
}

func (f *typeSampleFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	nt := req.GetQuery().GetSelection().GetNodeType()
	if f.failures[nt] {
		return nil, errors.New("sample read failed for " + nt)
	}
	if f.empties[nt] {
		return enginetest.ResponseWithNodes(), nil
	}
	return enginetest.ResponseWithNodes(&knowledgev1.Node{Id: nt + "-1", SymbolName: nt + "-sample"}), nil
}

// TestFetchTypeSamples_DisclosesFailures pins that a cloud/cicd stats render says
// WHICH node types it could not read samples for, instead of quietly showing a
// shorter list. Two bare continues used to swallow both the exec error and the
// decode error, so a reader saw fewer sections with no error, no warning and no
// count.
//
// THE SECOND LEG IS THE ONE THAT MATTERS. `derr != nil || len(nodes) == 0`
// conflated a decode failure with a genuinely empty type; an implementation that
// reports every empty type as broken would satisfy the first leg alone and would
// make every empty resource type look faulty — a new false statement rather than
// a fix.
func TestFetchTypeSamples_DisclosesFailures(t *testing.T) {
	stats := &knowledgev1.GraphStats{
		NodeCount: 6, EdgeCount: 0,
		NodesByType: map[string]int64{"cloud-resource": 4, "broken-type": 1, "empty-type": 1},
	}
	args := queryArgs{Graph: "cloud", Account: "acme", Mode: "stats", Samples: true}

	t.Run("a failing type is named in the output", func(t *testing.T) {
		f := &typeSampleFake{stats: stats, failures: map[string]bool{"broken-type": true}}
		res := resourceStats(context.Background(), f, cloudGraphKind, args)
		body := textBodyTools(res)
		require.False(t, res.IsError, body)
		assert.Contains(t, body, "Sample names could not be read",
			"a stats render that dropped a sample section must say so")
		assert.Contains(t, body, "broken-type",
			"the notice must NAME the type whose read failed — a bare count leaves the reader guessing")
		assert.Contains(t, body, "cloud-resource-sample",
			"the healthy types still render their samples: this is disclosure, not suppression")
	})

	t.Run("a legitimately empty type produces no failure notice", func(t *testing.T) {
		f := &typeSampleFake{stats: stats, empties: map[string]bool{"empty-type": true}}
		res := resourceStats(context.Background(), f, cloudGraphKind, args)
		body := textBodyTools(res)
		require.False(t, res.IsError, body)
		assert.NotContains(t, body, "Sample names could not be read",
			"a type with no rows is an ordinary answer — reporting it as a failure would make every "+
				"empty resource type look broken")
		assert.Contains(t, body, "cloud-resource-sample",
			"known positive: the render still reaches the sample sections in this leg")
	})
}

// TestResourceBrowse_TruncationNotice pins that the cloud/cicd resource browse
// discloses the SERVER'S truncation verdict. The arm issues its own Execute and
// renders directly, so it never passes through engine.Render — the single place
// every compiled tool's response picks up the notice. Without the disclosure a
// browse the row ceiling clamped renders as a complete-looking listing.
//
// PER KIND IN ITS OWN SUB-TEST, never one call standing in for both: resourceBrowse
// serves cloud AND cicd off the same body, so a cloud-only test leaves half the arm
// ungated. TWO POLARITIES: the untruncated leg refuses a notice appended
// unconditionally.
func TestResourceBrowse_TruncationNotice(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind resourceGraphKind
	}{
		{"cloud", cloudGraphKind},
		{"cicd", cicdGraphKind},
	} {
		t.Run(tc.name, func(t *testing.T) {
			browse := func(truncated bool) string {
				rec := &recordingStatsRPC{
					nodes:     []*knowledgev1.Node{{Id: "r1", SymbolName: "res"}},
					total:     1,
					truncated: truncated,
				}
				res := resourceBrowse(context.Background(), rec.Execute, tc.kind, queryArgs{Graph: tc.kind.graph, Account: "acme"})
				require.False(t, res.IsError, textBodyTools(res))
				return textBodyTools(res)
			}

			assert.Contains(t, browse(true), serverRowCeilingSentence,
				"resourceBrowse dropped the server's truncation verdict: the rendered result carries no "+
					"row-ceiling disclosure, so a clamped "+tc.kind.graph+" browse reads as a complete one")
			assert.NotContains(t, browse(false), serverRowCeilingSentence,
				"an untruncated browse must not claim to be partial")
		})
	}
}

// TestResourceBrowse_JSONFormat pins that the cloud/cicd browse HONORS
// format:"json" instead of silently returning markdown, and that the default
// format still renders markdown.
//
// TWO POLARITIES BY CONSTRUCTION. Without the markdown leg, a fix that returned
// JSON to every caller would pass — and that would break every existing reader.
// PER KIND: resourceBrowse serves cloud AND cicd off one body, so a cloud-only
// test leaves half the arm inert.
func TestResourceBrowse_JSONFormat(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind resourceGraphKind
	}{
		{"cloud", cloudGraphKind},
		{"cicd", cicdGraphKind},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newRec := func() *recordingStatsRPC {
				return &recordingStatsRPC{
					nodes: []*knowledgev1.Node{{Id: "r1", SymbolName: "res", Type: string(tc.kind.nodeType)}},
					total: 7,
				}
			}

			t.Run("format json serializes the browse envelope", func(t *testing.T) {
				rec := newRec()
				res := resourceBrowse(context.Background(), rec.Execute, tc.kind,
					queryArgs{Graph: tc.kind.graph, Account: "acme", Format: "json"})
				require.False(t, res.IsError, textBodyTools(res))

				var payload map[string]any
				require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &payload),
					"format json must produce a parseable payload, not markdown prose: %s", textBodyTools(res))
				assert.Equal(t, tc.kind.graph, payload["graph"])
				assert.Equal(t, string(tc.kind.nodeType), payload["type"])
				assert.Contains(t, payload, "results")
				assert.EqualValues(t, 7, payload["total"], "the envelope carries the CORPUS total, not the page length")
				assert.Contains(t, payload, "truncated",
					"the json arm is born carrying the key every sibling envelope emits")
			})

			t.Run("the default format still renders markdown", func(t *testing.T) {
				rec := newRec()
				res := resourceBrowse(context.Background(), rec.Execute, tc.kind,
					queryArgs{Graph: tc.kind.graph, Account: "acme"})
				require.False(t, res.IsError, textBodyTools(res))
				assert.Contains(t, textBodyTools(res), "## ",
					"a caller that did not ask for json must still get the markdown listing")
			})

			t.Run("an empty account serializes as results, never prose", func(t *testing.T) {
				// The json return MUST precede the markdown empty-case return: the rules
				// arm's own comment warns that otherwise an empty result set serializes
				// as prose and breaks the caller's JSON.parse.
				rec := &recordingStatsRPC{nodes: nil, total: 0}
				res := resourceBrowse(context.Background(), rec.Execute, tc.kind,
					queryArgs{Graph: tc.kind.graph, Account: "acme", Format: "json"})
				var payload map[string]any
				require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &payload),
					"an empty json browse must still be JSON: %s", textBodyTools(res))
				assert.Contains(t, payload, "results")
			})
		})
	}
}

// TestResourceBrowse_JSONTruncatedValue is the VALUE gate for the cloud/cicd
// browse envelope. TestResourceBrowse_JSONFormat asserts the envelope's KEYS are
// present; it does not assert what `truncated` SAYS, so pinning the threaded
// verdict to a constant left every package green. Presence without value is half
// a gate: a key that never tracks the response is exactly the inference-from-
// absence defect wearing a different hat.
func TestResourceBrowse_JSONTruncatedValue(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind resourceGraphKind
	}{
		{"cloud", cloudGraphKind},
		{"cicd", cicdGraphKind},
	} {
		t.Run(tc.name, func(t *testing.T) {
			browse := func(truncated bool) map[string]any {
				rec := &recordingStatsRPC{
					nodes:     []*knowledgev1.Node{{Id: "r1", SymbolName: "res", Type: string(tc.kind.nodeType)}},
					total:     7,
					truncated: truncated,
				}
				res := resourceBrowse(context.Background(), rec.Execute, tc.kind,
					queryArgs{Graph: tc.kind.graph, Account: "acme", Format: "json"})
				require.False(t, res.IsError, textBodyTools(res))
				var payload map[string]any
				require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &payload),
					"content[0] must stay the JSON envelope: %s", res.Content[0].Text)
				return payload
			}

			got, ok := browse(true)["truncated"]
			require.True(t, ok, "the browse envelope carries no truncated key")
			assert.Equal(t, true, got,
				"a clamped cloud/cicd browse must SAY so — the key has to track resp.GetTruncated(), "+
					"not sit at a constant the key-presence test cannot see")

			got, ok = browse(false)["truncated"]
			require.True(t, ok)
			assert.Equal(t, false, got, "a whole browse must not claim to be partial")
		})
	}
}

// textBodyTools concatenates a ToolResult's text content for assertions.
func textBodyTools(r kgtools.ToolResult) string {
	var sb []byte
	for _, c := range r.Content {
		if c.Type == "text" {
			sb = append(sb, c.Text...)
		}
	}
	return string(sb)
}

// TestInterceptQueryCloudCICD_DeclinesTopologyMode pins that a
// query(mode:"topology") over a cloud or cicd graph is NOT claimed by this
// intercept: the topology intercept runs LATER in the daemon's chain (the
// query-domain subchain dispatches first), so a claim here silently renders a
// resource browse instead of running the analyzer — which is exactly how the
// defect shipped and was caught live.
func TestInterceptQueryCloudCICD_DeclinesTopologyMode(t *testing.T) {
	for _, graph := range []string{"cloud", "cicd"} {
		t.Run(graph, func(t *testing.T) {
			params := kgtools.CallToolParams{
				Name: "query",
				Arguments: []byte(`{"mode":"topology","algorithm":"hits_hubs","graph":"` +
					graph + `","account":"acme","top_k":10}`),
			}
			handled, _ := InterceptQueryCloudCICD(context.Background(), nil, params)
			require.False(t, handled,
				"mode:topology must fall through to the topology intercept, never render a browse")
		})
	}
}
