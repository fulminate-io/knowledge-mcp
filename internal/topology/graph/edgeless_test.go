// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// edgeless_test.go is the termination audit for the whole analyzer family in
// this package. An edgeless graph — nodes materialized, zero edges surviving
// the adapter — is a real production state: a freshly collected graph whose
// edges have not landed, a subset predicate that drops every edge's endpoints,
// or a graph whose only edges are self-loops (the adapter drops those).
//
// Iterative analyzers are the risk: a convergence loop whose break condition
// can never be satisfied spins forever, and a ctx check that sits outside the
// loop cannot interrupt it. Every analyzer therefore runs here under a
// deadline; a hang fails the test fast instead of parking the package until
// the go test timeout fires.

// edgelessDeadline bounds each analyzer run. Every analyzer in this package
// completes an edgeless graph in microseconds, so anything approaching this
// is a non-terminating loop, not slowness.
const edgelessDeadline = 5 * time.Second

// buildEdgelessFixture returns the 2-node, zero-edge fixture: the minimal
// graph that exercises "nodes exist, no edges survive the adapter".
func buildEdgelessFixture() *graphFixture {
	f := newGraphFixture()
	f.AddNodeFull("finding-0", "finding-0", kgtypes.NodeFinding, "", nil)
	f.AddNodeFull("finding-1", "finding-1", kgtypes.NodeFinding, "", nil)
	return f
}

// runWithDeadline runs a.Run on its own goroutine and fails the test if it has
// not returned within edgelessDeadline. The analyzer goroutine is deliberately
// abandoned on timeout — a non-terminating analyzer cannot be stopped from the
// outside, which is precisely the defect this guards.
func runWithDeadline(
	t *testing.T,
	ctx context.Context,
	a foundation.Analyzer,
	req foundation.Request,
) ([]foundation.Finding, error) {
	t.Helper()
	type outcome struct {
		findings []foundation.Finding
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		findings, err := a.Run(ctx, req)
		done <- outcome{findings: findings, err: err}
	}()
	select {
	case got := <-done:
		return got.findings, got.err
	case <-time.After(edgelessDeadline):
		t.Fatalf("analyzer %q did not return within %s on an edgeless graph: non-terminating iteration",
			a.Name(), edgelessDeadline)
		return nil, nil
	}
}

// TestAnalyzers_EdgelessGraph_Terminate drives every analyzer registered by
// this package against the edgeless fixture. Returning an error is an
// acceptable disposition; never returning is not.
func TestAnalyzers_EdgelessGraph_Terminate(t *testing.T) {
	analyzers := foundation.All()
	if len(analyzers) == 0 {
		t.Fatal("no analyzers registered — the audit would vacuously pass")
	}
	for _, a := range analyzers {
		t.Run(a.Name(), func(t *testing.T) {
			f := buildEdgelessFixture()
			findings, err := runWithDeadline(t, newTestCtx(t), a, starReq(f, 20))
			if err != nil {
				t.Logf("%s returned an error on the edgeless graph (acceptable, it terminated): %v", a.Name(), err)
			}
			t.Logf("%s produced %d findings", a.Name(), len(findings))
		})
	}
}
