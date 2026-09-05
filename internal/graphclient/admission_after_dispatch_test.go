// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWorkingSet_FailedDispatchAdmitsNothing pins the half of the convergence
// rule that stops a phantom member being CREATED.
//
// Admission used to be the first statement of Router.Execute, before dispatch
// and regardless of outcome — and admission is not an existence check, so a read
// of a graph that does not exist enrolled it exactly as a read of one that does.
// Nothing then aged the member out, so the phantom earned a collector, a
// gen-poll entry and a scan cadence forever.
//
// ASSERTING THE RETURNED CODE MATCHES THE SCRIPTED ONE IS LOAD-BEARING. Without
// it, an empty recorder could equally be the emptiness of a call that never
// reached the backend at all — a transport failure, a nil client, a harness that
// never wired up. Matching the code proves the request went out, the scripted
// backend answered it, and the answer is what suppressed the admission.
//
// NOTE THE WALL CLOCK: the CodeUnavailable case takes about four seconds,
// because the GraphClient's reconnect interceptor retries that code before
// giving up. That is real behaviour rather than a sleep, and it is the whole of
// this test's runtime.
func TestWorkingSet_FailedDispatchAdmitsNothing(t *testing.T) {
	t.Parallel()

	for _, code := range []connect.Code{
		connect.CodeNotFound,
		connect.CodeUnavailable,
		connect.CodeInternal,
		connect.CodePermissionDenied,
	} {
		t.Run(code.String(), func(t *testing.T) {
			r, rec := routerWithFailingBackend(t, code)

			_, err := r.Execute(opCtx(), codeMutation("phantom-repo"))

			require.Error(t, err, "the scripted backend must refuse")
			assert.Equal(t, code, connect.CodeOf(err),
				"the returned code must be the SCRIPTED one — otherwise the empty recorder is the emptiness of a call that never reached the backend")
			assert.Empty(t, rec.recorded(),
				"a call that did not succeed must enroll nothing: admission is not an existence check")
		})
	}
}

// TestWorkingSet_SuccessfulDispatchStillAdmits is the known-positive control on
// the same request through a succeeding backend.
//
// Without it, DELETING the admission call outright would satisfy every assertion
// above while turning the working set into a permanent empty — which would stop
// every collector in the product.
func TestWorkingSet_SuccessfulDispatchStillAdmits(t *testing.T) {
	t.Parallel()

	r, rec := routerWithRecorder(t)

	_, err := r.Execute(opCtx(), codeMutation("phantom-repo"))

	require.NoError(t, err, "the succeeding backend must answer")
	assert.Equal(t, []string{"code/phantom-repo"}, rec.recorded(),
		"a successful call against a named graph must STILL admit it — the reordering narrows admission, it does not remove it")
}
