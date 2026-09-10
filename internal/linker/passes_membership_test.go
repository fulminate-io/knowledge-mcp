// SPDX-License-Identifier: Apache-2.0

package linker

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// passes_membership_test.go — R1b: THE LINKER RUNS ONE PASS, AND A RE-ADDED PASS
// CANNOT REJOIN SILENTLY.
//
// WHAT R1b ASKED FOR AND WHY IT NEEDS A TEST AT ALL. image, helm and
// workload_identity each read a cloud graph on one side and went with the
// built-in cloud collectors. LinkResult DROPPED their counters rather than
// zeroing them, deliberately: a field that is always zero reads to a caller as
// "the pass ran and found nothing", which is a different statement from "there
// is no such pass", and manage(link) renders these counts to an operator.
//
// The production side is right. Nothing observed it — so a pass re-added later,
// or a counter reinstated at zero, would rejoin the result with no test to
// notice. That is the failure this file exists for.
//
// THE MEMBERSHIP IS READ OFF THE TYPE, not off a list of names in a comment.
// LinkResult's exported field set IS the report's shape, so reflecting over it
// asks the question a reader actually has — "what does a caller receive?" —
// rather than the question a hand-written list answers, which is "what did
// someone remember to write down?".

// TestLinkResult_ReportsExactlyTheDockerfilePass pins the shape of what RunAll
// hands back.
func TestLinkResult_ReportsExactlyTheDockerfilePass(t *testing.T) {
	rt := reflect.TypeFor[LinkResult]()
	require.Equal(t, reflect.Struct, rt.Kind(), "control: LinkResult is a struct")

	got := make([]string, 0, rt.NumField())
	for f := range rt.Fields() {
		if f.IsExported() {
			got = append(got, f.Name)
		}
	}

	assert.ElementsMatch(t, []string{"DockerfileLinks", "Errors"}, got,
		"LinkResult's exported fields are the linker's whole report. A COUNTER ADDED HERE is a "+
			"pass rejoining the result, and one removed is a pass leaving it; either way this row "+
			"is where it gets said out loud rather than discovered by an operator reading a total "+
			"that does not add up")

	// THE NEGATIVE LEG, naming the three retired passes so a reinstated counter
	// fails by NAME rather than as an opaque set mismatch.
	for _, retired := range []string{"ImageLinks", "HelmLinks", "WorkloadIdentityLinks"} {
		_, found := rt.FieldByName(retired)
		assert.Falsef(t, found,
			"LinkResult carries %s again. The %s pass read a cloud graph on one side and went with "+
				"the built-in cloud collectors; a counter for it reports a pass that cannot run, and "+
				"a zero here tells an operator it ran and matched nothing", retired, retired)
	}
}

// TestLinkResult_ZeroValueIsAnHonestEmptyReport is the arm that makes the
// dropped-versus-zeroed distinction observable rather than merely commented.
//
// A caller that receives the zero value must be able to tell "one pass ran and
// found nothing" from "three other passes exist and found nothing" — and with
// the counters dropped, the second reading is not expressible, which is the
// point. What can be checked is that the one surviving counter is the only
// numeric field, so a future zeroed-counter reinstatement is caught here too.
func TestLinkResult_ZeroValueIsAnHonestEmptyReport(t *testing.T) {
	var res LinkResult
	assert.Equal(t, 0, res.DockerfileLinks)
	assert.Empty(t, res.Errors)

	rt := reflect.TypeFor[LinkResult]()
	numeric := 0
	for f := range rt.Fields() {
		if f.IsExported() && f.Type.Kind() == reflect.Int {
			numeric++
		}
	}
	assert.Equal(t, 1, numeric,
		"exactly one pass counter survives; a second one is a pass rejoining the report")
}
