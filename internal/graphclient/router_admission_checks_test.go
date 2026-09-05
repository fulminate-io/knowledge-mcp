// SPDX-License-Identifier: Apache-2.0

package graphclient

// router_admission_checks_test.go reproduces the second cutover defect: a direct
// user interaction with the checks graph recorded NO admission, so the catalog
// loop registered no collector for it and its nodes stayed unembedded through
// every drain.
//
// BOTH HALVES OF THE GATE HAD TO BE READ TO FIND IT. The operation half already
// passed — manage_checks is on the admitting list. The failure was the STRUCTURAL
// half: checks is the one family whose selector policy declares it carries no
// instance field, so its target's instance key is legitimately EMPTY, and the
// normalizer refused an empty name for every graph except knowledge. The refusal
// is right for code / cloud / practice, where an empty instance field means a
// catalog enumeration; it is wrong for a family that has no instance field to
// leave empty.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// checksRequest builds the request a checks read actually puts on the wire.
//
// THE TARGET COMES FROM THE PRODUCTION BUILDER, not from a hand-written literal.
// graphsel.GraphSelectorFor is what every checks read and write composes its
// selector through, and it deliberately attaches no instance name; a hand-built
// target here could carry a name the real one never has and the test would be
// measuring a shape that does not ship.
func checksRequest() *knowledgev1.ExecuteRequest {
	return &knowledgev1.ExecuteRequest{
		Target: graphsel.GraphSelectorFor(kgtypes.GraphChecks, "", false),
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{}},
	}
}

// TestWorkingSet_ManageChecksAdmitsTheChecksGraph is the reproduction.
//
// Every manage_checks operation reaches the graph the same way — list and run
// read the corpus, create writes it — so one admitting read under the tool's own
// operation term is what the whole surface depends on.
func TestWorkingSet_ManageChecksAdmitsTheChecksGraph(t *testing.T) {
	t.Parallel()

	r, rec := routerWithRecorder(t)
	ctx := WithOperation(context.Background(), OpManageChecks)
	_, _ = r.Execute(ctx, checksRequest())

	// THE RAW INSTANCE NAME IS CARRIED THROUGH, exactly as the knowledge
	// exception already does — workingset.Normalize is what collapses it to the
	// default instance, and the sibling test below observes that half. What this
	// row pins is that an admission is RECORDED AT ALL, which is what was missing.
	assert.Equal(t, []string{"checks/"}, rec.recorded(),
		"a manage_checks interaction must admit the checks graph, or the catalog loop registers no collector for it")
}

// TestWorkingSet_ChecksSearchAdmitsTheChecksGraph covers the other read the
// cutover added.
//
// It is a SEPARATE row rather than a parameterisation because search reaches the
// working set by a different route in production — the segment manager's own
// admitter — and the two share only the normalizer this defect lived in. Pinning
// the query-tool side alone would leave the shared cause looking like a
// manage_checks-specific quirk.
func TestWorkingSet_ChecksSearchAdmitsTheChecksGraph(t *testing.T) {
	t.Parallel()

	r, rec := routerWithRecorder(t)
	ctx := WithOperation(context.Background(), OpSearch)
	_, _ = r.Execute(ctx, checksRequest())

	assert.Equal(t, []string{"checks/"}, rec.recorded(),
		"a checks search is a direct interaction and must admit the graph it searched")
}

// TestWorkingSet_ChecksAdmissionReachesTheRealSet drives the recorder into a REAL
// workingset.Set and asserts membership, which is the property every downstream
// consumer actually reads.
//
// The recorder tests above observe the admit CALL; this observes that the call
// produces a MEMBER. They are different failures — a call whose ref the set then
// refuses would satisfy the first and not the second, which is precisely the
// shape of the defect being fixed.
func TestWorkingSet_ChecksAdmissionReachesTheRealSet(t *testing.T) {
	t.Parallel()

	set := workingset.New()
	// The succeeding backend is load-bearing: admission now follows a successful
	// dispatch, so a backendless Router would admit nothing and the assertion
	// below would fail for a reason unrelated to what it pins.
	r := &Router{local: succeedingBackend(t)}
	r.AttachWorkingSet(func(gt kgtypes.GraphType, name, reason string) {
		set.Admit(gt, name, reason)
	})

	require.False(t, set.Has(kgtypes.GraphChecks, "default"),
		"control: the set must start empty, or the assertion below proves nothing")

	ctx := WithOperation(context.Background(), OpManageChecks)
	_, _ = r.Execute(ctx, checksRequest())

	assert.True(t, set.Has(kgtypes.GraphChecks, "default"),
		"the admission must land as a MEMBER under the name the catalog loop asks for")
	assert.True(t, set.Has(kgtypes.GraphChecks, ""),
		"and under the empty name the corpus reader sends")
}

// TestWorkingSet_ChecksAdmissionStillObeysTheOperationPartition is the control
// that keeps this fix from widening admission.
//
// The structural half is what changed; the OPERATION half must be untouched. A
// background term addressing the very same checks target must still admit
// nothing — otherwise the normalizer change would have quietly turned the checks
// graph into a self-admitting one, which is the class the working set exists to
// kill.
func TestWorkingSet_ChecksAdmissionStillObeysTheOperationPartition(t *testing.T) {
	t.Parallel()

	for _, op := range []Operation{OpPipelineEmbedWriteback, OpManage, OpCrossGraphProbe, OpUnstamped} {
		t.Run(string(op), func(t *testing.T) {
			t.Parallel()
			r, rec := routerWithRecorder(t)
			_, _ = r.Execute(WithOperation(context.Background(), op), checksRequest())
			assert.Empty(t, rec.recorded(),
				"%q is not an admitting operation and must admit nothing, checks target or not", op)
		})
	}

	// KNOWN-POSITIVE CONTROL on the same request in the same test: an admitting
	// term DOES admit it, so the empties above are a classification rather than a
	// recorder that never fires.
	r, rec := routerWithRecorder(t)
	_, _ = r.Execute(WithOperation(context.Background(), OpManageChecks), checksRequest())
	require.Equal(t, []string{"checks/"}, rec.recorded(),
		"control: the admitting term must still admit, or every assertion above is vacuous")
}
