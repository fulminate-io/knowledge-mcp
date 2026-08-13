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

// TestWorkingSet_InstanceTargetAdmitsTypeOnlyDoesNot pins the structural half of
// the gate and its one named exception.
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
		// THE NAMED EXCEPTION, pinned rather than left as prose. knowledge is
		// single-instance, so a type-only knowledge target IS the default
		// instance and a user query against it is a direct interaction. For this
		// family the operation partition stays load-bearing instead of being
		// backstopped by the instance-key gate.
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
