// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// admissionRecordingCaller wraps the package's scripted fake with the working
// set's own admission gate, so a tail's reads are judged by the SAME predicates
// the real recorder applies on Router.Execute: the ctx-stamped operation must be
// an admitting one AND the request's target must resolve a concrete instance key
// (router_admission.go:70-92). The predicates are the exported production
// functions rather than a restatement of the policy — only the wiring is local,
// because the real recorder hangs off a Router that needs a live backend.
//
// It embeds fakeGraphCaller rather than reimplementing response serving, so the
// graph-name enumeration the tails drive answers exactly as it does elsewhere.
type admissionRecordingCaller struct {
	*fakeGraphCaller

	mu       sync.Mutex
	admitted []string // "graphType/name" per admission, in order
	executes int
}

func (f *admissionRecordingCaller) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.mu.Lock()
	f.executes++
	f.mu.Unlock()
	f.recordAdmission(ctx, req)
	return f.fakeGraphCaller.Execute(ctx, req)
}

// recordAdmission mirrors Router.recordAdmission's two-half gate.
func (f *admissionRecordingCaller) recordAdmission(ctx context.Context, req *knowledgev1.ExecuteRequest) {
	op, ok := graphclient.OperationFromContext(ctx)
	if !ok || !graphclient.AdmitsWorkingSet(op) {
		return
	}
	gt, name, ok := graphsel.InstanceKeyOf(req.GetTarget())
	if !ok {
		return
	}
	if _, ok := workingset.Normalize(gt, name); !ok {
		return
	}
	f.mu.Lock()
	f.admitted = append(f.admitted, string(gt)+"/"+name)
	f.mu.Unlock()
}

func (f *admissionRecordingCaller) snapshot() (admitted []string, executes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.admitted...), f.executes
}

// TestPostCollectFanoutDoesNotAdmit pins that neither post-collect tail can earn
// a graph a place in the working set. Both walk EVERY graph of a family rather
// than the one graph the user collected, so inheriting the collect's own stamp
// would let a single collect admit the whole account — the graph that was
// actually collected is admitted earlier and separately, by the collect sink.
//
// BOTH tails are driven because the region grep proves only that the stamp LINE
// is present; only this proves the stamp actually reaches the reads. The linker
// tail matters most here: its sub-linkers read across cloud, cicd and code
// graphs, so an unstamped RunAll would admit all three families at once.
func TestPostCollectFanoutDoesNotAdmit(t *testing.T) {
	const tailType = "postcollect-fanout-admission-test"

	// Map the test collector type onto the cloud graph type so the postpopulate
	// tail enumerates cloud graph names; restore afterwards.
	prev, had := postPopulateGraphType[tailType]
	postPopulateGraphType[tailType] = kgtypes.GraphCloud
	t.Cleanup(func() {
		if had {
			postPopulateGraphType[tailType] = prev
		} else {
			delete(postPopulateGraphType, tailType)
		}
	})

	// A hook that reads through the caller, so the per-graph hook calls — not
	// just the enumeration — are subject to the admission gate.
	postpopulate.Register(tailType, func(ctx context.Context, gc postpopulate.GraphCaller, name string) error {
		_, err := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
			Target: &knowledgev1.GraphSelector{Graph: "cloud", Account: name},
			Plan:   &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: "probe"}},
		})
		return err
	})

	prevLink, hadLink := postCollectLinkerTypes[tailType]
	postCollectLinkerTypes[tailType] = true
	t.Cleanup(func() {
		if hadLink {
			postCollectLinkerTypes[tailType] = prevLink
		} else {
			delete(postCollectLinkerTypes, tailType)
		}
	})

	// MULTI-GRAPH on purpose: a single-graph fixture cannot distinguish "the
	// fan-out admits nothing" from "the fan-out only ever saw one graph".
	caller := &admissionRecordingCaller{fakeGraphCaller: &fakeGraphCaller{
		listGraphsResult: &kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"graphs":[` +
				`{"graph_type":"cloud","graph_name":"aws-acct-123"},` +
				`{"graph_type":"cloud","graph_name":"aws-acct-456"},` +
				`{"graph_type":"cloud","graph_name":"gcp-proj-789"}` +
				`]}`}},
		},
	}}
	deps := &tailRoutingDeps{routed: caller, local: &fakeGraphCaller{}}

	// THE CTX THE TAILS ACTUALLY RECEIVE: both run while InterceptCollect is
	// handling a user's collect, so the ctx handed to them already carries the
	// admitting OpCollect stamp (collect.go:95). Driving them from a bare
	// context.Background() would make these assertions vacuous — with no stamp to
	// inherit there is nothing for the re-stamp to displace, and deleting both
	// re-stamps would leave the test green.
	ctx := graphclient.WithOperation(context.Background(), graphclient.OpCollect)

	runPostCollectPostPopulate(ctx, deps, tailType)
	ppAdmitted, ppExecutes := caller.snapshot()
	require.Positive(t, ppExecutes,
		"the postpopulate tail must actually have issued reads — a tail that never ran admits nothing trivially")
	assert.Empty(t, ppAdmitted,
		"the postpopulate tail walks every graph of the family, so none of them may be admitted by it")

	runPostCollectLinker(ctx, deps, tailType)
	lkAdmitted, lkExecutes := caller.snapshot()
	require.Greater(t, lkExecutes, ppExecutes,
		"the linker tail must actually have issued reads of its own")
	assert.Empty(t, lkAdmitted,
		"the linker tail reads across whole families, so none of those graphs may be admitted by it")

	// KNOWN-POSITIVE CONTROL, same caller and same gate: a read stamped with a
	// user's own collect, addressing ONE named graph, admits exactly that graph.
	// Without it, the two emptiness assertions above would pass just as happily
	// against a recorder that never fires at all.
	_, err := caller.Execute(
		graphclient.WithOperation(ctx, graphclient.OpCollect),
		&knowledgev1.ExecuteRequest{
			Target: &knowledgev1.GraphSelector{Graph: "cloud", Account: "aws-acct-123"},
			Plan:   &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: "probe"}},
		})
	require.NoError(t, err)

	controlAdmitted, _ := caller.snapshot()
	assert.Equal(t, []string{"cloud/aws-acct-123"}, controlAdmitted,
		"control: a stamped user collect naming one graph admits exactly that graph — "+
			"so the empty results above are a live gate, not a dead recorder")
}
