// SPDX-License-Identifier: Apache-2.0

package tools

// mutate_plan_annotation_attach_test.go holds the ATTACHMENT half of the
// severity-coherence suite, split out of mutate_plan_annotation_coherence_test.go
// for the repository's 500-line per-file cap.
//
// THE SPLIT IS ALONG THE READ BOUNDARY, which is the honest seam. What stays in
// the coherence file is decided from the payload alone: a metadata write that
// moves the node's copy, and an upsert that cannot write an edge at all. What
// moved here names an EXISTING annotation and therefore costs a read —
// create_batch's from_id spelling, mutate(link), and the malformed-annotation
// case both of them meet. That is also the boundary the guards themselves are
// split along.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestMutate_CreateBatchAnnotationEdgeMustCarryItsSeverity covers create_batch's
// edges[] in BOTH spellings of the source endpoint.
//
// AN EARLIER VERSION OF THIS GUARD WAS WRONG IN TWO DIRECTIONS AT ONCE, from one
// cause, and the two cases below are the two halves of that. It refused every
// annotation edge on the claim that create_batch's edges[] "cannot carry the
// annotation's kind and tier" — reasoning about the CLOSED SCHEMA rather than
// what the arm accepts, when engine.edgeBody has always decoded method and
// evidence and TestCompileMutate_CreateBatchEdgeMetadata has always asserted they
// land. And it tested only from_idx, so the from_id spelling — the same
// attachment, naming an EXISTING annotation — was waved through and compiled into
// an edge carrying neither. It refused what could be right and admitted what
// could not.
func TestMutate_CreateBatchAnnotationEdgeMustCarryItsSeverity(t *testing.T) {
	want, err := kgtypes.MarshalAnnotationEdgeSeverity(kgtypes.AnnotationKindCorrect, "")
	require.NoError(t, err)

	t.Run("from_idx: a bare edge is refused and names what to send", func(t *testing.T) {
		fc, res := annotationMutate(t, `{"operation":"create_batch",`+
			`"nodes":[{"type":"plan_section","name":"sec","summary":"s"},`+
			`{"type":"plan_annotation","name":"a","summary":"s","metadata":{"annotation_kind":"correct"}}],`+
			`"edges":[{"from_idx":1,"to_idx":0,"type":"relates-to"}]}`)
		require.True(t, res.IsError, "expected a refusal, got: %s", toolResultText(res))
		body := toolResultText(res)
		assert.Contains(t, body, "edges[0]")
		assert.Contains(t, body, "nodes[1]")
		assert.Contains(t, body, kgtypes.AnnotationEdgeMethod, "the refusal names the method to send")
		assert.Contains(t, body, want, "and the exact evidence payload")
		assert.Zero(t, fc.mutations, "a refused batch must persist nothing")
	})

	// THE HOLE. The same attachment, spelled with from_id on an EXISTING
	// annotation, was ACCEPTED before this fix and compiled into a relates-to edge
	// with empty method and evidence — which a section read then answers from.
	t.Run("from_id: the same attachment is refused too", func(t *testing.T) {
		fc := &fakeGraphCaller{
			queryResponses: map[string]kgtools.ToolResult{
				"ann-existing": nodeResultJSON(t, "ann-existing", string(kgtypes.NodePlanAnnotation),
					map[string]string{kgtypes.AnnotationKindKey: kgtypes.AnnotationKindCorrect}),
			},
			mutateIDs: []string{"sec-new"},
		}
		_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(`{"operation":"create_batch",` +
				`"nodes":[{"type":"plan_section","name":"sec","summary":"s"}],` +
				`"edges":[{"from_id":"ann-existing","to_idx":0,"type":"relates-to"}]}`),
		})
		require.True(t, res.IsError, "the from_id spelling must be refused too, got: %s", toolResultText(res))
		body := toolResultText(res)
		assert.Contains(t, body, "ann-existing", "the refusal names the endpoint in the caller's own spelling")
		assert.Contains(t, body, want)
	})

	// THE OTHER HALF: an edge that DOES carry the matching severity is ACCEPTED.
	// The old guard refused this, which is the direction nobody would have
	// noticed, because a refusal of a correct write looks like a working gate.
	t.Run("a coherent edge is accepted, in both spellings", func(t *testing.T) {
		_, byIdx := annotationMutate(t, `{"operation":"create_batch",`+
			`"nodes":[{"type":"plan_section","name":"sec","summary":"s"},`+
			`{"type":"plan_annotation","name":"a","summary":"s","metadata":{"annotation_kind":"correct"}}],`+
			`"edges":[{"from_idx":1,"to_idx":0,"type":"relates-to","method":"`+kgtypes.AnnotationEdgeMethod+`",`+
			`"evidence":`+quoteJSON(want)+`}]}`)
		assert.False(t, byIdx.IsError, "a coherent from_idx attachment must land: %s", toolResultText(byIdx))

		fc := &fakeGraphCaller{
			queryResponses: map[string]kgtools.ToolResult{
				"ann-existing": nodeResultJSON(t, "ann-existing", string(kgtypes.NodePlanAnnotation),
					map[string]string{kgtypes.AnnotationKindKey: kgtypes.AnnotationKindCorrect}),
			},
			mutateIDs: []string{"sec-new"},
		}
		_, byID := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(`{"operation":"create_batch",` +
				`"nodes":[{"type":"plan_section","name":"sec","summary":"s"}],` +
				`"edges":[{"from_id":"ann-existing","to_idx":0,"type":"relates-to","method":"` +
				kgtypes.AnnotationEdgeMethod + `","evidence":` + quoteJSON(want) + `}]}`),
		})
		assert.False(t, byID.IsError, "a coherent from_id attachment must land: %s", toolResultText(byID))
	})

	t.Run("a WRONG severity on the edge is refused", func(t *testing.T) {
		wrong, werr := kgtypes.MarshalAnnotationEdgeSeverity(kgtypes.AnnotationKindFinding, "T1")
		require.NoError(t, werr)
		_, res := annotationMutate(t, `{"operation":"create_batch",`+
			`"nodes":[{"type":"plan_section","name":"sec","summary":"s"},`+
			`{"type":"plan_annotation","name":"a","summary":"s","metadata":{"annotation_kind":"correct"}}],`+
			`"edges":[{"from_idx":1,"to_idx":0,"type":"relates-to","method":"`+kgtypes.AnnotationEdgeMethod+`",`+
			`"evidence":`+quoteJSON(wrong)+`}]}`)
		assert.True(t, res.IsError, "an edge disagreeing with its node is refused: %s", toolResultText(res))
	})

	// FAIL CLOSED on a read failure, the same rule the link guard states: an
	// unreadable source might be an annotation, and waving it through admits an
	// unstamped edge.
	t.Run("an unreadable from_id source is refused, not waved through", func(t *testing.T) {
		fc := &fakeGraphCaller{
			queryErrors: map[string]error{"ann-existing": assert.AnError},
			mutateIDs:   []string{"sec-new"},
		}
		_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(`{"operation":"create_batch",` +
				`"nodes":[{"type":"plan_section","name":"sec","summary":"s"}],` +
				`"edges":[{"from_id":"ann-existing","to_idx":0,"type":"relates-to"}]}`),
		})
		require.True(t, res.IsError, "a read failure must refuse, got: %s", toolResultText(res))
		assert.Contains(t, toolResultText(res), "could not read ann-existing")
	})

	// CONTROL ONE: an annotation with NO edge in the batch is still accepted. One
	// carrier cannot disagree with a carrier that does not exist.
	t.Run("an unattached annotation is still accepted", func(t *testing.T) {
		_, ok := annotationMutate(t,
			`{"operation":"create_batch","nodes":[{"type":"plan_annotation","name":"a","summary":"s","metadata":{"annotation_kind":"correct"}}]}`)
		assert.False(t, ok.IsError, "an unattached annotation is not refused: %s", toolResultText(ok))
	})

	// CONTROL TWO: a relates-to edge between two NON-annotation nodes is
	// untouched, in both spellings, so the rule is scoped to the type that has
	// two carriers.
	t.Run("an ordinary relates-to edge is untouched", func(t *testing.T) {
		_, plain := annotationMutate(t, `{"operation":"create_batch",`+
			`"nodes":[{"type":"finding","name":"f","summary":"s"},{"type":"document","name":"d","summary":"s"}],`+
			`"edges":[{"from_idx":0,"to_idx":1,"type":"relates-to"}]}`)
		assert.False(t, plain.IsError, "an ordinary relates-to edge is untouched: %s", toolResultText(plain))

		fc := &fakeGraphCaller{
			queryResponses: map[string]kgtools.ToolResult{
				"doc-1": nodeResultJSON(t, "doc-1", "document", nil),
			},
			mutateIDs: []string{"f-new"},
		}
		_, byID := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(`{"operation":"create_batch",` +
				`"nodes":[{"type":"finding","name":"f","summary":"s"}],` +
				`"edges":[{"from_id":"doc-1","to_idx":0,"type":"relates-to"}]}`),
		})
		assert.False(t, byID.IsError, "an ordinary from_id relates-to edge is untouched: %s", toolResultText(byID))
	})
}

// TestMutate_LinkingAnAnnotationRequiresItsOwnSeverity covers the one path that
// needs a read: an EXISTING annotation linked to a section must carry its own
// kind and tier onto that edge.
//
// THE REFUSAL IS ASSERTED TO BE ACTIONABLE, not merely present: it prints the
// exact method and evidence to send, read off the node the caller named. A
// refusal that told the caller only that they were wrong would leave them no way
// to be right, because nothing else exposes the required payload.
func TestMutate_LinkingAnAnnotationRequiresItsOwnSeverity(t *testing.T) {
	annNode := nodeResultJSON(t, "ann-1", string(kgtypes.NodePlanAnnotation), map[string]string{
		kgtypes.AnnotationKindKey: kgtypes.AnnotationKindFinding,
		kgtypes.AnnotationTierKey: "T2",
	})
	newFake := func() *fakeGraphCaller {
		return &fakeGraphCaller{
			queryResponses: map[string]kgtools.ToolResult{
				"ann-1": annNode,
				"sec-0": nodeResultJSON(t, "sec-0", string(kgtypes.NodePlanSection), nil),
			},
			mutateIDs: []string{"edge-1"},
		}
	}
	link := func(t *testing.T, extra string) kgtools.ToolResult {
		t.Helper()
		_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: newFake()}, kgtools.CallToolParams{
			Name:      "mutate",
			Arguments: json.RawMessage(`{"operation":"link","from":"ann-1","to":"sec-0","relationship":"relates-to"` + extra + `}`),
		})
		return res
	}

	t.Run("a bare link is refused and names what to send", func(t *testing.T) {
		res := link(t, "")
		require.True(t, res.IsError, "got: %s", toolResultText(res))
		body := toolResultText(res)
		assert.Contains(t, body, kgtypes.AnnotationEdgeMethod, "the refusal names the method to send")
		assert.Contains(t, body, "T2", "and the tier, read off the node the caller named")
		assert.Contains(t, body, kgtypes.AnnotationKindFinding)
	})

	t.Run("a link carrying the WRONG severity is refused", func(t *testing.T) {
		wrong, err := kgtypes.MarshalAnnotationEdgeSeverity(kgtypes.AnnotationKindFinding, "T1")
		require.NoError(t, err)
		res := link(t, `,"method":"`+kgtypes.AnnotationEdgeMethod+`","edge_evidence":`+quoteJSON(wrong))
		assert.True(t, res.IsError, "a tier that disagrees with the node is refused: %s", toolResultText(res))
	})

	t.Run("the link the refusal asked for is accepted", func(t *testing.T) {
		right, err := kgtypes.MarshalAnnotationEdgeSeverity(kgtypes.AnnotationKindFinding, "T2")
		require.NoError(t, err)
		res := link(t, `,"method":"`+kgtypes.AnnotationEdgeMethod+`","edge_evidence":`+quoteJSON(right))
		assert.False(t, res.IsError, "the coherent link must land, or the refusal names an impossible remedy: %s",
			toolResultText(res))
	})

	// THE READ ITSELF CAN FAIL, and this guard fails CLOSED when it does. An
	// unreadable from-node might be an annotation, and standing aside would admit
	// an unstamped edge — a hole in the invariant exactly the width of a flaky
	// read. An invariant with a failure mode that opens it is not an invariant.
	t.Run("an unreadable from-node is refused, not waved through", func(t *testing.T) {
		fc := &fakeGraphCaller{
			queryErrors: map[string]error{"ann-1": assert.AnError},
			mutateIDs:   []string{"edge-3"},
		}
		_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name:      "mutate",
			Arguments: json.RawMessage(`{"operation":"link","from":"ann-1","to":"sec-0","relationship":"relates-to"}`),
		})
		require.True(t, res.IsError, "a read failure must refuse, got: %s", toolResultText(res))
		assert.Contains(t, toolResultText(res), "could not read ann-1")
	})

	t.Run("linking a NON-annotation is untouched", func(t *testing.T) {
		fc := &fakeGraphCaller{
			queryResponses: map[string]kgtools.ToolResult{
				"doc-1": nodeResultJSON(t, "doc-1", "document", nil),
				"sec-0": nodeResultJSON(t, "sec-0", string(kgtypes.NodePlanSection), nil),
			},
			mutateIDs: []string{"edge-2"},
		}
		_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name:      "mutate",
			Arguments: json.RawMessage(`{"operation":"link","from":"doc-1","to":"sec-0","relationship":"relates-to"}`),
		})
		assert.False(t, res.IsError, "an ordinary relates-to link is untouched: %s", toolResultText(res))
	})
}

// quoteJSON renders s as a JSON string literal, so an evidence payload that is
// itself JSON can ride inside a JSON argument string without hand-escaping.
func quoteJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// TestMutate_UpsertCannotCreateAnAnnotation covers the arm the contract guard did
// not reach, and the second-order defect it let through.
//
// THE FIRST DEFECT: the guard ran on mutate(create) and mutate(create_batch), and
// upsert reaches the same node type by a different arm. A contract enforced on
// some of the arms that reach a type is not a contract on that type, and this one
// let an annotation into the graph with NO KIND — which the tree then renders as
// `annotations: 1 ( 1)`.
//
// THE ANSWER IS TO REFUSE THE TYPE ON THIS OPERATION, not to run the create
// contract here, and the reason is that upsert writes NO EDGE. Even a
// well-formed annotation upserted this way would carry a severity on the node and
// nothing on the edge — the disagreement, on every call. Telling a create from an
// update on this arm would need a read, and as an update it would move the node's
// copy and leave any existing edge behind. Refusing answers both without a read.
//
// THE COST IS NOTHING REAL: upsert exists for tool-owned config records with
// caller-chosen ids, and an annotation is created with links, which is what writes
// its edge.
func TestMutate_UpsertCannotCreateAnAnnotation(t *testing.T) {
	for _, args := range []string{
		`{"operation":"upsert","id":"ann-x","type":"plan_annotation","name":"a","summary":"s"}`,
		`{"operation":"upsert","id":"ann-x","type":"plan_annotation","name":"a","summary":"s","metadata":{"annotation_kind":""}}`,
		`{"operation":"upsert","id":"ann-x","type":"plan_annotation","name":"a","summary":"s","metadata":{"annotation_kind":"nit"}}`,
		// EVEN A WELL-FORMED ONE, which is the case that shows the rule is about
		// the operation's inability to write an edge rather than about the body.
		`{"operation":"upsert","id":"ann-x","type":"plan_annotation","name":"a","summary":"s","metadata":{"annotation_kind":"finding","annotation_tier":"T2"}}`,
	} {
		t.Run(args, func(t *testing.T) {
			fc := &countingMutateCaller{}
			_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
				Name: "mutate", Arguments: json.RawMessage(args),
			})
			require.True(t, res.IsError, "expected a refusal, got: %s", toolResultText(res))
			body := toolResultText(res)
			assert.Contains(t, body, "mutate(upsert)")
			assert.Contains(t, body, "not created or updated by upsert")
			assert.Contains(t, body, "links", "the refusal names the path that writes both carriers")
			assert.Zero(t, fc.mutations, "a refused upsert must persist nothing")
		})
	}

	// CONTROL ONE: the SAME body through the SUPPORTED path lands, so the refusal
	// is about the operation and not about the annotation.
	t.Run("the same annotation created with links is accepted", func(t *testing.T) {
		fc := &fakeGraphCaller{
			queryResponses: map[string]kgtools.ToolResult{
				"sec-0": nodeResultJSON(t, "sec-0", string(kgtypes.NodePlanSection), nil),
			},
			mutateIDs: []string{"ann-1"},
		}
		_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(`{"operation":"create","type":"plan_annotation","name":"a","summary":"s",` +
				`"links":["sec-0"],"metadata":{"annotation_kind":"finding","annotation_tier":"T2"}}`),
		})
		assert.False(t, res.IsError, "the supported path must land: %s", toolResultText(res))
	})

	// CONTROL TWO: upsert of any OTHER type is untouched, so the guard did not
	// become a rule about the operation itself.
	t.Run("an upsert of another type is untouched", func(t *testing.T) {
		fc := &countingMutateCaller{}
		_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name:      "mutate",
			Arguments: json.RawMessage(`{"operation":"upsert","id":"w-1","type":"worker","name":"w","summary":"s"}`),
		})
		assert.False(t, res.IsError, "an unrelated upsert must land: %s", toolResultText(res))
	})
}

// TestMutate_AMalformedAnnotationCannotBeAttached is the second-order half: an
// annotation already in the graph with an unusable severity cannot be attached to
// a section, and the refusal NAMES IT rather than printing an empty payload as
// the value to send.
//
// THE FIXTURE IS A NODE THE GUARDED PATHS CAN NO LONGER CREATE, which is the
// point: such nodes exist in graphs written before the gate, and the attach-time
// guards are what meet them.
func TestMutate_AMalformedAnnotationCannotBeAttached(t *testing.T) {
	kindless := nodeResultJSON(t, "ann-kindless", string(kgtypes.NodePlanAnnotation), map[string]string{})

	t.Run("link refuses and names the annotation, not a payload", func(t *testing.T) {
		fc := &fakeGraphCaller{
			queryResponses: map[string]kgtools.ToolResult{"ann-kindless": kindless},
			mutateIDs:      []string{"edge-1"},
		}
		_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name:      "mutate",
			Arguments: json.RawMessage(`{"operation":"link","from":"ann-kindless","to":"sec-0","relationship":"relates-to"}`),
		})
		require.True(t, res.IsError, "got: %s", toolResultText(res))
		body := toolResultText(res)
		assert.Contains(t, body, "ann-kindless")
		assert.Contains(t, body, "severity is malformed")
		assert.NotContains(t, body, `{"annotation_kind":""}`,
			"the refusal must not offer an empty severity as the value to send")
	})

	t.Run("create_batch refuses the same way", func(t *testing.T) {
		fc := &fakeGraphCaller{
			queryResponses: map[string]kgtools.ToolResult{"ann-kindless": kindless},
			mutateIDs:      []string{"sec-new"},
		}
		_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(`{"operation":"create_batch",` +
				`"nodes":[{"type":"plan_section","name":"sec","summary":"s"}],` +
				`"edges":[{"from_id":"ann-kindless","to_idx":0,"type":"relates-to"}]}`),
		})
		require.True(t, res.IsError, "got: %s", toolResultText(res))
		assert.Contains(t, toolResultText(res), "severity is malformed")
	})

	// THE SAME-RUN CONTROL: a WELL-FORMED annotation still attaches through the
	// same guards, so the refusals above are about the malformed severity and not
	// about attachment.
	t.Run("a well-formed annotation still attaches", func(t *testing.T) {
		want, err := kgtypes.MarshalAnnotationEdgeSeverity(kgtypes.AnnotationKindFinding, "T2")
		require.NoError(t, err)
		fc := &fakeGraphCaller{
			queryResponses: map[string]kgtools.ToolResult{
				"ann-ok": nodeResultJSON(t, "ann-ok", string(kgtypes.NodePlanAnnotation), map[string]string{
					kgtypes.AnnotationKindKey: kgtypes.AnnotationKindFinding,
					kgtypes.AnnotationTierKey: "T2",
				}),
			},
			mutateIDs: []string{"edge-2"},
		}
		_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(`{"operation":"link","from":"ann-ok","to":"sec-0","relationship":"relates-to",` +
				`"method":"` + kgtypes.AnnotationEdgeMethod + `","edge_evidence":` + quoteJSON(want) + `}`),
		})
		assert.False(t, res.IsError, "a coherent attachment of a well-formed annotation must land: %s", toolResultText(res))
	})
}
