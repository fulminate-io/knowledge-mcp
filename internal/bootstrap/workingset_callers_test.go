// SPDX-License-Identifier: Apache-2.0

package bootstrap

// workingset_callers_test.go observes the working-set membership predicate AT ITS
// TWO PRODUCTION CALLERS rather than only at the predicate.
//
// WHY THAT DISTINCTION IS WORTH A FILE. workingset.Set.Has answered false for
// every destination-bearing member until it was widened, and the cost was not
// abstract: the propagation loop's gate stayed shut and the manage(status)
// membership cell read "unmanaged" for a graph the client really maintains. A test
// on the predicate alone is a PROXY for both of those; a caller that stopped
// consulting the predicate, or consulted it with a re-normalized ref, would leave
// that proxy perfectly green. These two rows are the callers themselves.

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// TestPropagationGateOpensForADestinationBoundAdmission drives the gate the
// propagation loop is handed — the named method wirePropagationRuntime passes to
// WithWorkingSetGate — over a client whose only admission of knowledge/default
// carries a destination, which is what every admission route records now.
func TestPropagationGateOpensForADestinationBoundAdmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  workingset.Ref
		want bool
	}{
		{
			name: "local destination admission opens the gate",
			ref:  workingset.Ref{GraphType: kgtypes.GraphKnowledge, Name: "default", Storage: "local"},
			want: true,
		},
		{
			name: "signed in account admission opens the gate",
			ref:  workingset.Ref{GraphType: kgtypes.GraphKnowledge, Name: "default", Storage: "cloud", Account: "acct-1"},
			want: true,
		},
		{
			name: "an admission of another graph does not",
			ref:  workingset.Ref{GraphType: kgtypes.GraphCode, Name: "somerepo", Storage: "local"},
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &client{workingSet: workingset.New()}
			// THE CLOSED STATE FIRST, so the open one below is a transition rather than
			// a reading of a gate that was never shut.
			if c.propagationWorkingSetGate() {
				t.Fatal("fixture check: the gate must be shut before any admission, else this row proves nothing")
			}
			c.workingSet.AdmitRef(tc.ref, "test")
			if got := c.propagationWorkingSetGate(); got != tc.want {
				t.Errorf("propagationWorkingSetGate() = %t after admitting %+v, want %t — the loop that "+
					"gate guards does the thought-graph propagation for knowledge/default, and a gate that "+
					"cannot see a destination-bearing member keeps it shut forever", got, tc.ref, tc.want)
			}
		})
	}
}

// TestStatusMembershipCellReadsADestinationBoundMember drives the OTHER caller:
// client.InWorkingSet, which is the seam the manage(status) coverage table
// type-asserts for and reads its membership cell from (tools inWorkingSetFor). A
// false here renders a maintained graph in the "unmanaged" band — a row telling an
// operator no arm services a graph this client is actively draining.
func TestStatusMembershipCellReadsADestinationBoundMember(t *testing.T) {
	c := &client{workingSet: workingset.New()}
	if c.InWorkingSet(kgtypes.GraphKnowledge, "default") {
		t.Fatal("fixture check: membership must be false before any admission")
	}

	// THE PRODUCTION ADMISSION PATH, not a direct set write: this is the method the
	// search admitter, the routed-call recorder and the collect sink all record
	// through, and its whole point is that it retains the destination.
	local := graphclient.WithDestination(t.Context(), graphclient.Destination{Storage: "local"})
	c.AdmitDestinationGraph(local, kgtypes.GraphKnowledge, "default", "search")

	if !c.InWorkingSet(kgtypes.GraphKnowledge, "default") {
		t.Errorf("InWorkingSet(knowledge, default) = false after a destination-bound admission; "+
			"members=%+v — the status table reads this seam for its membership cell, so a false here "+
			"bands a graph this client maintains as unmanaged", c.workingSet.Members())
	}
	// AND IT STILL DISCRIMINATES: a graph nobody admitted is not a member, so the
	// assertion above is not satisfied by a seam that answers true for everything.
	if c.InWorkingSet(kgtypes.GraphCode, "never-admitted") {
		t.Error("InWorkingSet(code, never-admitted) = true — the seam must still be a membership test")
	}
}
