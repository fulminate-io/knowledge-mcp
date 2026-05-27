// SPDX-License-Identifier: Apache-2.0

package prune

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// fakeCaller is a pure-Go GraphCaller stand-in — no httptest, no grpctest (per
// locked feedback: client-side unit tests use pure-function mocks, not
// over-the-wire harnesses). The prune startup runner rides the Execute carrier
// seam (T-GTB6), so it records Execute requests + returns a canned response /
// error. Call is retained to satisfy the interface but is unused by the runner.
type fakeCaller struct {
	mu       sync.Mutex
	requests []*knowledgev1.ExecuteRequest
	resp     *knowledgev1.ExecuteResponse
	err      error
	delay    time.Duration
}

func (f *fakeCaller) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.ToolResult{}, nil
}

func (f *fakeCaller) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	resp := f.resp
	if resp == nil {
		resp = &knowledgev1.ExecuteResponse{}
	}
	return resp, nil
}

func (f *fakeCaller) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeCaller) snapshot() []*knowledgev1.ExecuteRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*knowledgev1.ExecuteRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

func okResp(affected int64) *knowledgev1.ExecuteResponse {
	return &knowledgev1.ExecuteResponse{AffectedCount: affected}
}

func installRetention(t *testing.T, sessions string) {
	t.Helper()
	cfg := &config.Config{
		SchemaVersion: 1,
		Default: config.Section{
			Provider: config.ProviderClaudeCLI,
			Model:    "m",
			CLIBin:   "/x",
		},
	}
	if sessions != "" {
		cfg.Retention = &config.Retention{Sessions: sessions}
	}
	t.Cleanup(config.SetForTest(cfg))
}

// TestRun_NoSection: Retention nil → zero calls, returns nil.
func TestRun_NoSection(t *testing.T) {
	installRetention(t, "") // produces cfg with Retention == nil
	f := &fakeCaller{resp: okResp(0)}
	if err := Run(context.Background(), f); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := f.callCount(); got != 0 {
		t.Errorf("call count = %d; want 0", got)
	}
}

// TestRun_EmptyFields: Retention non-nil but the field empty → zero
// calls, returns nil. This is the "section exists but I left it empty"
// case which behaves identically to the absent section per the BCN7
// strict-opt-in design.
func TestRun_EmptyFields(t *testing.T) {
	cfg := &config.Config{
		SchemaVersion: 1,
		Default:       config.Section{Provider: config.ProviderClaudeCLI, Model: "m", CLIBin: "/x"},
		Retention:     &config.Retention{Sessions: ""},
	}
	t.Cleanup(config.SetForTest(cfg))

	f := &fakeCaller{resp: okResp(0)}
	if err := Run(context.Background(), f); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := f.callCount(); got != 0 {
		t.Errorf("call count = %d; want 0", got)
	}
}

// TestRun_SessionsOnly: Sessions populated → exactly one DELETE Execute whose
// compiled plan targets NodeType=session with a created_at OP_LT FieldPredicate.
func TestRun_SessionsOnly(t *testing.T) {
	installRetention(t, "7d")
	f := &fakeCaller{resp: okResp(2)}
	if err := Run(context.Background(), f); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reqs := f.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("len(requests) = %d; want 1", len(reqs))
	}
	m := reqs[0].GetMutation()
	if m == nil {
		t.Fatal("expected a MutationPlan")
	}
	if m.GetKind() != knowledgev1.MutationPlan_MUTATION_KIND_DELETE {
		t.Errorf("kind = %v; want DELETE", m.GetKind())
	}
	sel := m.GetSelection()
	if sel.GetNodeType() != "session" {
		t.Errorf("node_type = %q; want session", sel.GetNodeType())
	}
	preds := sel.GetFieldPredicates()
	if len(preds) != 1 || preds[0].GetField() != "created_at" || preds[0].GetOp() != knowledgev1.MetadataPredicate_OP_LT {
		t.Errorf("expected one created_at OP_LT FieldPredicate, got %+v", preds)
	}
}

// TestRun_ServerErrorPropagates: an Execute error → Run wraps and returns it.
func TestRun_ServerErrorPropagates(t *testing.T) {
	installRetention(t, "7d")
	f := &fakeCaller{err: errors.New("invalid duration: bad")}
	err := Run(context.Background(), f)
	if err == nil {
		t.Fatal("Run: want error, got nil")
	}
	if !errors.Is(err, f.err) {
		t.Errorf("err = %v; want wrap of the Execute error", err)
	}
}

// TestRun_TransportErrorPropagates: GraphCaller returns a non-nil
// transport error → Run wraps and returns it.
func TestRun_TransportErrorPropagates(t *testing.T) {
	installRetention(t, "7d")
	wireErr := errors.New("connection refused")
	f := &fakeCaller{err: wireErr}
	err := Run(context.Background(), f)
	if err == nil {
		t.Fatal("Run: want error, got nil")
	}
	if !errors.Is(err, wireErr) {
		t.Errorf("err = %v; want wrap of %v", err, wireErr)
	}
}

// TestRun_ParentTimeoutBounded: parent ctx WithTimeout(200ms),
// fakeCaller delay=2s. Parent context wins (more restrictive than the
// per-call 60s cap). Run completes within ~300ms with
// context.DeadlineExceeded carried through.
func TestRun_ParentTimeoutBounded(t *testing.T) {
	installRetention(t, "7d")
	f := &fakeCaller{delay: 2 * time.Second, resp: okResp(0)}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := Run(ctx, f)
	elapsed := time.Since(start)

	if elapsed > 600*time.Millisecond {
		t.Errorf("elapsed = %v; want < 600ms (parent timeout should bound)", elapsed)
	}
	if err == nil {
		t.Fatal("Run: want timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v; want context.DeadlineExceeded", err)
	}
}
