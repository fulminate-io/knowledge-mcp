// SPDX-License-Identifier: Apache-2.0

package tools

// mutate_plan_annotation_coherence_test.go covers the invariant that an
// annotation's kind and tier are never different on its node and on its edge.
//
// THE FACT IS STORED TWICE ON PURPOSE — on the node because it is the
// annotation's own record, on the edge because that is what makes a section's
// review state readable without hydrating anything — and two carriers of one
// fact are exactly the class this ticket exists to remove one level up. So the
// duplication is only defensible if no supported write can separate them.
//
// FIVE SPELLINGS COULD HAVE, and each gets a test here with a control that a
// supported write still lands. Three are decidable from the payload alone; two
// name an EXISTING annotation and so cost a read:
//
//	update / update_batch / bulk_update_metadata / upsert — move the node's copy
//	   and leave the edge's behind. Payload alone.
//	create_batch, edges[] with from_idx — attaches a NEW annotation named by slot,
//	   whose kind and tier are in the same payload. Payload alone.
//	create_batch, edges[] with from_id — the same attachment naming an EXISTING
//	   annotation. Needs a read.
//	link — attaches an existing annotation with whatever edge metadata the caller
//	   chose, including none. Needs a read.
//	upsert of the annotation itself — reaches plan_annotation through an arm the
//	   contract guard did not cover, and produced a node with NO KIND at all.
//
// A COHERENT EDGE IS ACCEPTED IN BOTH create_batch SPELLINGS, and an earlier
// version of this header said the opposite. It claimed the edges[] entry "cannot
// carry a severity" because the SCHEMA is a closed object of five keys. The
// runtime was never so: engine.edgeBody decodes method and evidence, and
// TestCompileMutate_CreateBatchEdgeMetadata has always asserted they land. That
// false premise made the guard refuse the spelling that could be right and — a
// blanket refusal having no reason to resolve its endpoint carefully — admit the
// spelling that could not. Both are covered below, in both directions.
//
// THE CONTROLS ARE NOT DECORATION HERE. Every assertion below is that a write is
// REFUSED, and a guard that refused everything would satisfy all of them; each
// control is a neighboring write that must still succeed.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestMutate_AnnotationSeverityEditIsRefused is THE path the gap report names:
// today a plain metadata update moves the node's kind or tier and leaves the
// edge's behind, and the plan then reports a severity nobody wrote.
func TestMutate_AnnotationSeverityEditIsRefused(t *testing.T) {
	cases := []struct {
		name    string
		args    string
		wantSub []string
	}{
		{
			name:    "update moves the tier on the node alone",
			args:    `{"operation":"update","id":"ann-1","metadata":{"annotation_tier":"T1"}}`,
			wantSub: []string{"annotation_tier", "written ONCE", "links"},
		},
		{
			name:    "update moves the kind on the node alone",
			args:    `{"operation":"update","id":"ann-1","metadata":{"annotation_kind":"correct"}}`,
			wantSub: []string{"annotation_kind", "written ONCE"},
		},
		{
			name:    "update_batch does the same, per item",
			args:    `{"operation":"update_batch","items":[{"id":"n1","summary":"s"},{"id":"ann-1","metadata":{"annotation_tier":"T1"}}]}`,
			wantSub: []string{"items[1].metadata", "annotation_tier"},
		},
		{
			name:    "bulk_update_metadata does the same, per update",
			args:    `{"operation":"bulk_update_metadata","updates":[{"id":"ann-1","metadata":{"annotation_kind":"finding","annotation_tier":"T3"}}]}`,
			wantSub: []string{"updates[0].metadata", "annotation_kind"},
		},
		{
			// UPSERT IS ANSWERED BY THE TYPE RULE, not this one, and the case stays
			// here so the difference is visible: an annotation upsert is refused
			// whatever its body, because upsert writes no edge. The write-once
			// message below belongs to the operations that CAN move a node's copy
			// while an edge already exists.
			name:    "upsert of an annotation is refused by type, not by key",
			args:    `{"operation":"upsert","id":"ann-1","type":"plan_annotation","metadata":{"annotation_kind":"correct"}}`,
			wantSub: []string{"mutate(upsert)", "not created or updated by upsert"},
		},
		{
			// The severity keys on an upsert of ANY OTHER type still take the
			// write-once answer, so the type rule above did not swallow this one.
			name:    "upsert of another type carrying a severity key",
			args:    `{"operation":"upsert","id":"n-1","type":"document","metadata":{"annotation_tier":"T1"}}`,
			wantSub: []string{"annotation_tier", "written ONCE"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := &countingMutateCaller{}
			_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
				Name: "mutate", Arguments: json.RawMessage(tc.args),
			})
			require.True(t, res.IsError, "expected a refusal, got: %s", toolResultText(res))
			for _, sub := range tc.wantSub {
				assert.Contains(t, toolResultText(res), sub, "the refusal must name %q", sub)
			}
			assert.Zero(t, fc.mutations, "a refused write must persist nothing")
		})
	}
}

// TestMutate_OrdinaryMetadataWritesStillLand is the control for the whole family
// above. The refusal is scoped to the two keys that ride both carriers, and every
// other metadata write on every one of those operations is untouched — including
// the annotation's OWN other keys, which live only on the node and so cannot
// disagree with anything.
func TestMutate_OrdinaryMetadataWritesStillLand(t *testing.T) {
	for _, args := range []string{
		`{"operation":"update","id":"n1","metadata":{"team":"core"}}`,
		`{"operation":"update_batch","items":[{"id":"n1","metadata":{"team":"core"}}]}`,
		`{"operation":"bulk_update_metadata","updates":[{"id":"n1","metadata":{"team":"core"}}]}`,
		// The annotation's node-only keys: neither rides the edge.
		`{"operation":"update","id":"ann-1","metadata":{"reviewer_lane":"rv-2"}}`,
		`{"operation":"update","id":"ann-1","metadata":{"replacement_text":"the corrected sentence"}}`,
	} {
		t.Run(args, func(t *testing.T) {
			fc := &countingMutateCaller{}
			_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
				Name: "mutate", Arguments: json.RawMessage(args),
			})
			assert.False(t, res.IsError, "an ordinary metadata write must still land: %s", toolResultText(res))
		})
	}
}
