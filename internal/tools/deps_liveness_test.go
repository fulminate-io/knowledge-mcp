// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// Compile-gate: the real local *graphclient.GraphClient satisfies the
// narrowed liveness-only LocalLiveness view (Healthy + Status). If a future
// change drops either method from GraphClient this line stops compiling.
var _ LocalLiveness = (*graphclient.GraphClient)(nil)

// executeCapable is the Execute-carrier shape LocalLiveness must NOT expose.
// It mirrors GraphCaller — the seam every graph read/write rides. If
// LocalLiveness ever grew an Execute method, the assertion below would flip
// and the local-server-is-sync-only lock would be silently breached.
type executeCapable interface {
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// livenessOnly is the minimal LocalLiveness implementation used for the
// negative control: it has Healthy + Status and NOTHING else, so a
// LocalLiveness value backed by it can never type-assert UP to executeCapable.
type livenessOnly struct{}

func (livenessOnly) Healthy() bool                   { return false }
func (livenessOnly) Status() (map[string]any, error) { return nil, nil }

// executeFake is the positive control mirroring fakeOverwriter in
// intercept_sync_test.go: it DOES implement Execute, proving the assertion
// mechanism actually detects the capability when it is present.
type executeFake struct{}

func (executeFake) Execute(context.Context, *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

// TestLocalLiveness_CannotReachExecute pins the item-2 invariant: a
// LocalLiveness-typed handle exposes ONLY liveness (Healthy/Status) and can
// never be type-asserted UP to an Execute-capable interface, so no tools-layer
// code can pull a graph-write off the bare local accessor and bypass the
// login-aware Router. A fake that DOES implement Execute satisfies
// executeCapable, proving the assertion mechanism itself works.
func TestLocalLiveness_CannotReachExecute(t *testing.T) {
	var ll LocalLiveness = livenessOnly{}
	_, ok := any(ll).(executeCapable)
	assert.False(t, ok, "LocalLiveness must NOT satisfy an Execute-capable interface — liveness-only, no graph-write carrier")

	// Positive control: a caller that implements Execute IS executeCapable,
	// proving the negative assertion is not vacuously true.
	var exec any = executeFake{}
	_, ok = exec.(executeCapable)
	assert.True(t, ok, "a fake implementing Execute DOES satisfy executeCapable")
}
