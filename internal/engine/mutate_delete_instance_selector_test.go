// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestCompileDelete_InstanceSelectorRoutesToTarget asserts that a delete carries
// the caller's graph-INSTANCE selector onto the Execute Target, so a code or
// an instance-keyed graph is addressable rather than merely nameable.
//
// THE PRACTICE CONTROL IS LOAD-BEARING. It runs through the IDENTICAL probe —
// same compileMutate entry, same GetTarget() read — and was green while the repo
// leg was red, so the red is a statement about repo and not about the probe.
// Without the control leg a green cannot be told from a compiler that started
// accepting anything.
//
// THE EMPTY-Name LEG IS NOT DECORATION. mutateTarget projects through
// graphsel.InstanceValueOf precisely so a selector can only ever carry the one
// field its family reads, and a Name leaking onto a code selector is the shape
// TestCompileMutate_NodeNameNeverRidesTheGraphSelector exists to keep closed.
func TestCompileDelete_InstanceSelectorRoutesToTarget(t *testing.T) {
	t.Run("code delete carries repo on the Target", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(
			`{"operation":"delete","graph":"code","repo":"knowledge","ids":["a.go:Alpha"]}`))
		require.True(t, ok, "a code delete-by-ids must compile")
		assert.Equal(t, "code", req.GetTarget().GetGraph())
		assert.Equal(t, "knowledge", req.GetTarget().GetRepo(),
			"the code delete Target must carry the caller's repo")
		assert.Empty(t, req.GetTarget().GetName(),
			"a code selector consumes repo alone — no Name may ride it")
	})

	t.Run("a caller-supplied account reaches no delete Target", func(t *testing.T) {
		// THIS ROW WAS THE ACCOUNT-KEYED FAMILY'S. cloud and cicd were the two and
		// both are retired, so graphsel projects no account onto a Target at all —
		// the row asserts the ABSENCE rather than disappearing with the family,
		// because an account silently riding a live family's selector is what the
		// projection exists to prevent and is now unobserved anywhere else.
		req, ok := compileMutate(json.RawMessage(
			`{"operation":"delete","graph":"code","repo":"knowledge","account":"acme","ids":["i-1"]}`))
		require.True(t, ok, "a code delete-by-ids must compile")
		assert.Equal(t, "code", req.GetTarget().GetGraph())
		assert.Empty(t, req.GetTarget().GetAccount(),
			"no family consumes account, so no delete Target may carry one")
		assert.Equal(t, "knowledge", req.GetTarget().GetRepo(),
			"and the family's own instance key still rides, so the emptiness above is the projection acting")
		assert.Empty(t, req.GetTarget().GetName(),
			"an instance-keyed selector consumes its own field alone — no Name may ride it")
	})

	t.Run("practice control carries no instance field", func(t *testing.T) {
		// The control still discriminates, and what it asserts inverted: practice is
		// a singleton, so a delete targets the one graph with no instance field.
		// Asserting the EMPTY value rather than dropping the row is what catches a
		// repo fix that starts routing a name here again — the server's practice
		// policy row refuses one, so a name is a refused delete.
		req, ok := compileMutate(json.RawMessage(
			`{"operation":"delete","graph":"practice","ids":["p1"]}`))
		require.True(t, ok, "a practice delete-by-ids must compile")
		assert.Equal(t, "practice", req.GetTarget().GetGraph())
		assert.Empty(t, req.GetTarget().GetLanguage(),
			"a practice delete consumes no instance field")
		assert.Empty(t, req.GetTarget().GetName(),
			"and does not fall through to a name either")
	})

	// THE SECOND TARGET SITE. dispatchDeletePreview builds its Target on a path
	// claimed BEFORE Compile, so it never reaches deleteRequest: fixing only
	// deleteRequest leaves a dry-run unable to resolve its graph, and the preview
	// is exactly where a caller checks before doing something irreversible. The
	// exec-closure idiom is lifted from TestDeletePreviewJSON_TruncatedField and
	// extended from reading the RESPONSE to capturing the REQUEST.
	t.Run("the dry run preview targets the same graph the real delete would", func(t *testing.T) {
		var seen *knowledgev1.ExecuteRequest
		exec := func(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
			seen = req
			return &knowledgev1.ExecuteResponse{
				Nodes: []*knowledgev1.Node{{Id: "a.go:Alpha", SymbolName: "Alpha", Type: "function"}},
			}, nil
		}
		res, handled := dispatchDeletePreview(context.Background(), exec,
			json.RawMessage(`{"dry_run":true,"graph":"code","repo":"venue","ids":["a.go:Alpha"]}`))
		require.True(t, handled, "a dry-run delete is the shape this seam claims")
		require.False(t, res.IsError, "the preview must not error")
		require.NotNil(t, seen, "the preview must issue an Execute the closure can observe")
		assert.Equal(t, "code", seen.GetTarget().GetGraph())
		assert.Equal(t, "venue", seen.GetTarget().GetRepo(),
			"the dry-run preview Target must carry the caller's repo, like the real delete")
		assert.Empty(t, seen.GetTarget().GetName(),
			"a code selector consumes repo alone — no Name may ride the preview Target")
	})

	// THE SHARED SEAM, asserted across the operation axis. deleteRequest and the
	// preview site both route through mutateTarget, the same projection helper the
	// other eight reducible operations use, so a fix that routed delete by altering
	// that helper's contract would break them here and nowhere else in this plan.
	//
	// MEASURED: on an unmodified checkout eight of these nine rows already return
	// repo=="venue" and delete alone returns "" — so this is a CHARACTERIZATION
	// GUARD for eight and a red-first assertion for one.
	t.Run("the shared seam routes every reducible operation", func(t *testing.T) {
		reducible := []struct {
			operation string
			payload   string
		}{
			{"create", `{"operation":"create","graph":"code","repo":"venue","type":"finding","name":"N","summary":"s"}`},
			{"create_batch", `{"operation":"create_batch","graph":"code","repo":"venue","nodes":[{"type":"finding","name":"N","summary":"s"}]}`},
			{"upsert", `{"operation":"upsert","graph":"code","repo":"venue","id":"a.go:Alpha","type":"finding"}`},
			{"update", `{"operation":"update","graph":"code","repo":"venue","id":"a.go:Alpha","summary":"s"}`},
			{"update_batch", `{"operation":"update_batch","graph":"code","repo":"venue","items":[{"id":"a.go:Alpha","summary":"s"}]}`},
			{"bulk_update_metadata", `{"operation":"bulk_update_metadata","graph":"code","repo":"venue","updates":[{"id":"a.go:Alpha","metadata":{"k":"v"}}]}`},
			{"delete", `{"operation":"delete","graph":"code","repo":"venue","ids":["a.go:Alpha"]}`},
			{"link", `{"operation":"link","graph":"code","repo":"venue","from":"a.go:Alpha","to":"b.go:Beta","relationship":"calls"}`},
			{"unlink", `{"operation":"unlink","graph":"code","repo":"venue","from":"a.go:Alpha","to":"b.go:Beta","relationship":"calls"}`},
		}
		for _, row := range reducible {
			req, ok := compileMutate(json.RawMessage(row.payload))
			require.Truef(t, ok, "%s must compile against a code graph", row.operation)
			assert.Equalf(t, "venue", req.GetTarget().GetRepo(),
				"%s must carry the caller's repo onto the Target", row.operation)
		}

		// `answer` is knowledge-only by construction and is NOT engine-reducible.
		// That is correct and must not change: routing an instance selector may not
		// widen the reducible set.
		req, ok := compileMutate(json.RawMessage(
			`{"operation":"answer","graph":"code","repo":"venue","id":"q1","summary":"s"}`))
		assert.False(t, ok, "answer must stay non-reducible")
		assert.Nil(t, req)
	})
}
