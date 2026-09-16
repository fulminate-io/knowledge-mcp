// SPDX-License-Identifier: Apache-2.0

package graphclient

// router_admission_fence_test.go is the keyless-BM25 fix's admission fence: a KEEP-GREEN FENCE, not a
// regression row. The ticket's fix changes WHICH Ref a search records, WHEN the
// BM25 arm is constructed and WHEN it is woken. None of that widens what admits,
// and this test is what catches a later fix that makes a read admit in order to
// pass a search test.
//
// THE BEHAVIOUR IT FENCES, observed on a scratch pair built from the release the defect was reproduced on: an ordinary
// knowledge write (mutate) and an ordinary by-id read both send a NIL
// GraphSelector — engine.mutateTarget returns nil when graph, instance and branch
// are all empty — so resolveAdmissionTarget yields the empty graph type and
// recordAdmission returns before admitting. A create alone therefore produces no
// drain and no segments; the FIRST SEARCH is what admits, and the regression rows
// for R1/R2 are written as create → poll a text search until the hit for exactly
// that reason.

import (
	"context"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestByIDQueryDoesNotAdmitTheGraph(t *testing.T) {
	type admission struct {
		gt     kgtypes.GraphType
		name   string
		reason string
	}
	var admitted []admission
	r := &Router{}
	r.AttachWorkingSet(func(gt kgtypes.GraphType, name, reason string) {
		admitted = append(admitted, admission{gt: gt, name: name, reason: reason})
	})

	// A by-id read and an ordinary knowledge write both compile to a nil Target.
	// OpQuery and OpMutate are BOTH inside the admitting partition, so the operation
	// half of the gate passes and the structural half is what declines — which is
	// the mechanism this fence is about.
	for _, op := range []Operation{OpQuery, OpMutate} {
		if !AdmitsWorkingSet(op) {
			t.Fatalf("fixture check: %s must be inside the admitting partition, or this fence would "+
				"pass on the operation half and prove nothing about the target half", op)
		}
		ctx := WithOperation(context.Background(), op)
		gt, name, err := resolveAdmissionTarget(&knowledgev1.ExecuteRequest{})
		if err != nil {
			t.Fatalf("resolveAdmissionTarget(nil target) returned error: %v", err)
		}
		if gt != "" || name != "" {
			t.Errorf("resolveAdmissionTarget(nil target) = (%q, %q), want the empty pair — synthesizing a "+
				"knowledge target for a nil selector would make every by-id read and every ordinary write "+
				"admit, widening the working-set rule", gt, name)
		}
		r.recordAdmission(ctx, gt, name)
	}
	if len(admitted) != 0 {
		t.Errorf("a nil-target call under an admitting operation recorded %d admissions (%+v), want 0",
			len(admitted), admitted)
	}

	// THE KNOWN POSITIVE, same recorder, same run: a call that DOES name a concrete
	// instance admits. Without it a zero above is indistinguishable from a recorder
	// that was never wired.
	ctx := WithOperation(context.Background(), OpQuery)
	gt, name, err := resolveAdmissionTarget(&knowledgev1.ExecuteRequest{
		Target: &knowledgev1.GraphSelector{Graph: string(kgtypes.GraphCode), Repo: "fenceRepo"},
	})
	if err != nil {
		t.Fatalf("resolveAdmissionTarget(code/fenceRepo) returned error: %v", err)
	}
	r.recordAdmission(ctx, gt, name)
	if len(admitted) != 1 || admitted[0].gt != kgtypes.GraphCode || admitted[0].name != "fenceRepo" {
		t.Fatalf("KNOWN POSITIVE FAILED: a concrete-instance read recorded %+v, want exactly one "+
			"admission of code/fenceRepo — the zeros above prove nothing without it", admitted)
	}
}
