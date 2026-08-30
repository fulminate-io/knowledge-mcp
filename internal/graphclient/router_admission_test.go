// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// admissionRecorder captures every admission the Router records. It is the
// recorder the absence assertions need: an admission that happens is appended,
// so an empty slice means no admission was attempted rather than that nothing
// was wired.
type admissionRecorder struct {
	mu   sync.Mutex
	refs []string
}

func (a *admissionRecorder) admit(gt kgtypes.GraphType, name, _ string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refs = append(a.refs, string(gt)+"/"+name)
}

func (a *admissionRecorder) recorded() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.refs...)
}

// routerWithRecorder returns a Router carrying the recorder. It has no backend,
// so Execute fails at pick() — deliberately: the admission is recorded BEFORE
// dispatch, which is what these tests observe, and it keeps them off the wire.
func routerWithRecorder(t *testing.T) (*Router, *admissionRecorder) {
	t.Helper()
	r := &Router{}
	rec := &admissionRecorder{}
	r.AttachWorkingSet(rec.admit)
	return r, rec
}

func codeMutation(repo string) *knowledgev1.ExecuteRequest {
	return &knowledgev1.ExecuteRequest{
		Target: &knowledgev1.GraphSelector{Graph: string(kgtypes.GraphCode), Repo: repo},
		Plan:   &knowledgev1.ExecuteRequest_Mutation{Mutation: &knowledgev1.MutationPlan{}},
	}
}

// TestWorkingSet_UserMutationAdmitsGraph — a user write against a named graph is
// a direct interaction with that graph.
func TestWorkingSet_UserMutationAdmitsGraph(t *testing.T) {
	t.Parallel()

	r, rec := routerWithRecorder(t)
	ctx := WithOperation(context.Background(), OpMutate)
	_, _ = r.Execute(ctx, codeMutation("repoA"))

	assert.Equal(t, []string{"code/repoA"}, rec.recorded())
}

// TestWorkingSet_PipelineWritebackDoesNotAdmit drives the SAME request under the
// writeback operation and asserts it admits nothing.
//
// The recorder sits on Router.Execute, the funnel background and user traffic
// share, so the operation partition is the SOLE mechanism excluding this path —
// there is no second, structural exclusion behind it. If this goes red the
// working set is self-admitting and the whole gate is a no-op.
func TestWorkingSet_PipelineWritebackDoesNotAdmit(t *testing.T) {
	t.Parallel()

	r, rec := routerWithRecorder(t)

	// KNOWN-POSITIVE CONTROL FIRST: the same Router, the same request shape,
	// under an admitting operation. Without it the empty result below would
	// prove only that the recorder was never reachable.
	_, _ = r.Execute(WithOperation(context.Background(), OpMutate), codeMutation("control-repo"))
	require.Equal(t, []string{"code/control-repo"}, rec.recorded(),
		"control: the recorder must be live before its silence means anything")

	_, _ = r.Execute(
		WithOperation(context.Background(), OpPipelineEmbedWriteback), codeMutation("repoA"))

	assert.Equal(t, []string{"code/control-repo"}, rec.recorded(),
		"pipeline writeback is NOT an admission: only user interactions admit a graph, and "+
			"admitting the writeback would make the working set self-admitting")
}

// TestWorkingSet_StatsRPCAdmitsNothing pins the property the coverage table's
// unmanaged rows now depend on by name: manage(status) issues a per-graph Stats
// RPC for EVERY graph in the account, including the ones this client has never
// interacted with, and not one of them may become a working-set member as a
// result.
//
// IT IS A SEPARATE PIN FROM TestManageStatusSweepDoesNotAdmit (bootstrap
// client_workingset_test.go) BECAUSE IT COVERS A DIFFERENT ROUTE. That test drives
// Stats-SHAPED requests through Router.Execute, where the operation partition is
// what excludes them; production reaches the coverage counts through this method
// instead — the collector type-asserts the GraphCaller up to the Stats seam and
// calls it directly — and Router.Stats is a bare pick-and-forward that touches no
// admitter at all. Nothing about the Execute pin constrains this method, so before
// this test the route the counts actually travel had no admission pin on it.
//
// THE KNOWN-POSITIVE IS THE SAME RECORDER IN THE SAME TEST. An empty slice from a
// recorder that was never wired proves nothing, so an admitting Execute runs first
// and its admission is asserted before the Stats calls' silence is read.
func TestWorkingSet_StatsRPCAdmitsNothing(t *testing.T) {
	t.Parallel()

	r, rec := routerWithRecorder(t)

	_, _ = r.Execute(WithOperation(context.Background(), OpMutate), codeMutation("control-repo"))
	require.Equal(t, []string{"code/control-repo"}, rec.recorded(),
		"control: the recorder must be live before its silence means anything")

	// The coverage fan-out's own shape: concrete instance targets, under the manage
	// operation, for graphs no interaction has admitted. A type-only target would be
	// refused by the structural instance-key gate and would prove nothing here.
	manageCtx := WithOperation(context.Background(), OpManage)
	for _, repo := range []string{"foreign-a", "foreign-b", "foreign-c"} {
		_, _ = r.Stats(manageCtx, &knowledgev1.StatsRequest{
			Target:          &knowledgev1.GraphSelector{Graph: string(kgtypes.GraphCode), Repo: repo},
			IncludeCoverage: true,
		})
	}

	assert.Equal(t, []string{"code/control-repo"}, rec.recorded(),
		"counting a graph is not interacting with it: a per-graph Stats RPC must leave the "+
			"working set exactly as it found it, however many graphs the coverage walk counts")
}

// TestUserCallAdmitsOnlyTheNamedGraph states the invariant the whole fan-out
// CLASS must keep: one user call admits the one graph it named, and the internal
// fan-outs it triggers admit nothing — however many graphs they happen to
// address. The per-site tests prove the two fan-outs that exist today stamp
// correctly; this one gives a third fan-out, added later with an inherited
// stamp, a written rule to be measured against.
//
// ITS REACH IS THE ROUTER, NOT THE SYSTEM. It observes only admissions flowing
// through Router.Execute, and two production admission vectors deliberately
// bypass that funnel — the collect sink (admittingSink.WriteResult) and the
// segmentdist search admitter — each a genuine user interaction carrying its own
// test, TestCollectSinkAdmitsEveryGraphFamily among them.
func TestUserCallAdmitsOnlyTheNamedGraph(t *testing.T) {
	t.Parallel()

	r, rec := routerWithRecorder(t)

	// (1) KNOWN-POSITIVE CONTROL, first: the user's own call against the graph
	// the user named. If this does not fire, the two silences below prove only
	// that the recorder was never reachable.
	_, _ = r.Execute(WithOperation(context.Background(), OpThoughts), codeMutation("named-by-the-user"))
	require.Equal(t, []string{"code/named-by-the-user"}, rec.recorded(),
		"control: the recorder must be live before its silence means anything")

	// (2) and (3): the fan-outs that same call triggers, each addressing a
	// DIFFERENT graph the user never named.
	_, _ = r.Execute(WithOperation(context.Background(), OpCrossGraphProbe), codeMutation("probed-not-named"))
	_, _ = r.Execute(WithOperation(context.Background(), OpPostCollectFanout), codeMutation("enriched-not-named"))

	// SET EQUALITY, never a count: a count of one is equally satisfied by having
	// admitted the wrong graph.
	assert.Equal(t, []string{"code/named-by-the-user"}, rec.recorded(),
		"a user call admits ONLY the graph it named — the graphs its internal fan-outs "+
			"reach are not interactions, and admitting them is what let one call pull in "+
			"every graph in the account")
}

// TestWorkingSet_InstanceTargetAdmitsTypeOnlyDoesNot pins the structural half of
// the gate and its named exceptions. It covers the knowledge one; the checks
// sibling — the other single-instance family — is pinned in
// router_admission_checks_test.go alongside the defect it was found by.
func TestWorkingSet_InstanceTargetAdmitsTypeOnlyDoesNot(t *testing.T) {
	t.Parallel()

	query := func(sel *knowledgev1.GraphSelector) *knowledgev1.ExecuteRequest {
		return &knowledgev1.ExecuteRequest{
			Target: sel,
			Plan:   &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{}},
		}
	}

	t.Run("a concrete instance target admits", func(t *testing.T) {
		t.Parallel()
		r, rec := routerWithRecorder(t)
		_, _ = r.Execute(WithOperation(context.Background(), OpQuery),
			query(&knowledgev1.GraphSelector{Graph: string(kgtypes.GraphCode), Repo: "repoA"}))
		assert.Equal(t, []string{"code/repoA"}, rec.recorded())
	})

	t.Run("a type-only enumeration under the same operation admits nothing", func(t *testing.T) {
		t.Parallel()
		r, rec := routerWithRecorder(t)
		// The shape a catalog enumeration compiles to: the graph type, no
		// instance key. It cannot admit what it enumerates.
		_, _ = r.Execute(WithOperation(context.Background(), OpQuery),
			query(&knowledgev1.GraphSelector{Graph: string(kgtypes.GraphCode)}))
		assert.Equal(t, []string(nil), rec.recorded())
	})

	t.Run("a type-only knowledge target admits the default instance", func(t *testing.T) {
		t.Parallel()
		// A NAMED EXCEPTION, pinned rather than left as prose. knowledge is
		// single-instance, so a type-only knowledge target IS the default
		// instance and a user query against it is a direct interaction. For the
		// single-instance families the operation partition stays load-bearing
		// instead of being backstopped by the instance-key gate.
		r, rec := routerWithRecorder(t)
		_, _ = r.Execute(WithOperation(context.Background(), OpQuery),
			query(&knowledgev1.GraphSelector{Graph: string(kgtypes.GraphKnowledge)}))
		assert.Equal(t, []string{"knowledge/"}, rec.recorded(),
			"the empty instance name is carried through; workingset.Normalize collapses "+
				"it to the default instance")
	})

	t.Run("an unstamped context admits nothing", func(t *testing.T) {
		t.Parallel()
		r, rec := routerWithRecorder(t)
		_, _ = r.Execute(context.Background(),
			query(&knowledgev1.GraphSelector{Graph: string(kgtypes.GraphCode), Repo: "repoA"}))
		assert.Equal(t, []string(nil), rec.recorded(), "default-deny: no operation, no admission")
	})
}
