// SPDX-License-Identifier: Apache-2.0

package pdfcollector

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/testdata/fixturelib"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// emit_loudness_test.go — the gate on this emitter reporting, rather than
// absorbing, a failure to build an edge's evidence.
//
// WHAT WAS WRONG. jsonMeta returned "" on a marshal error. The empty string is
// what an edge with NO evidence carries, so the failure was byte-identical to
// the success it displaced: a CONTAINS edge shipped without its `position`
// key — the only thing that reconstructs document order — and nothing logged,
// returned or recorded that it had happened.
//
// WHY THERE IS NO END-TO-END RED FOR THE MARSHAL ITSELF, stated plainly rather
// than left for a reader to discover. jsonMeta takes a map[string]string, and
// encoding/json accepts every value of that type: invalid UTF-8 is coerced to
// U+FFFD rather than refused, and no string map can be cyclic or of an
// unsupported kind. So the error branch is unreachable through the production
// entry point BY CONSTRUCTION — which is exactly why it is an error rather
// than a fallback, and it is the same reasoning the web collector's twin
// carries at collector/web/emit_nodes.go. What is pinned here instead is
// everything around it that a future edit CAN break: the signature that forces
// callers to confront the error, the refusal that emits nothing when one
// lands, and the evidence actually arriving on every edge of a real collect.
// The three are stated as separate tests so a failure names which one moved.

// jsonMeta MUST propagate its marshal failure rather than absorb it. This
// declaration fails to COMPILE against the absorbing form — which returned a
// bare string — so the whole package goes red if anyone restores it, without
// depending on a runtime branch that a correct build cannot reach.
var _ func(map[string]string) (string, error) = jsonMeta

// TestJSONMeta_EmptyMapIsNotAFailure pins the one case that legitimately
// yields an empty string, so the refusal above cannot be satisfied by making
// every empty result an error.
func TestJSONMeta_EmptyMapIsNotAFailure(t *testing.T) {
	t.Parallel()
	got, err := jsonMeta(nil)
	if err != nil {
		t.Fatalf("jsonMeta(nil) = %v; an absent evidence map is not a failure", err)
	}
	if got != "" {
		t.Errorf("jsonMeta(nil) = %q, want the empty string", got)
	}

	// A populated map is the ordinary path and must round-trip.
	raw, err := jsonMeta(map[string]string{"position": "7"})
	if err != nil {
		t.Fatalf("jsonMeta on a populated string map returned %v; that map cannot fail to marshal", err)
	}
	var back map[string]string
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("jsonMeta produced %q, which is not JSON: %v", raw, err)
	}
	if back["position"] != "7" {
		t.Errorf("round-trip lost the position key: %v", back)
	}
}

// TestJSONMeta_MarshalRejectionIsARealBranch is the known-positive control for
// the test above.
//
// The claim being controlled is "jsonMeta's error branch guards a real
// encoding/json behavior, not an imagined one". The control drives the SAME
// function — encoding/json.Marshal — with a value it does reject, and
// separately confirms the value class jsonMeta is actually handed (a string
// map holding bytes that are not valid UTF-8) is accepted. Without the second
// half, "jsonMeta never errors" would be indistinguishable from "the marshal
// was never called".
func TestJSONMeta_MarshalRejectionIsARealBranch(t *testing.T) {
	t.Parallel()

	// The channel rides an `any` rather than appearing as a literal argument
	// because errchkjson refuses a statically-visible unsupported type at the
	// call site. Refusing it is right for production code and wrong here: an
	// unsupported type IS the subject of this control, and dodging the static
	// check is what lets the control observe the runtime behaviour it is about.
	var unsupported any = make(chan int)
	if _, err := json.Marshal(unsupported); err == nil {
		t.Error("control: encoding/json accepted a channel — the error branch jsonMeta guards would be unreachable for every input, not just for string maps")
	}

	// And the nastiest input jsonMeta can genuinely be handed still marshals:
	// invalid UTF-8 in a value is coerced to U+FFFD rather than refused. This
	// is the measurement behind the comment at the top of this file.
	if _, err := jsonMeta(map[string]string{"position": "\xff\xfe"}); err != nil {
		t.Errorf("jsonMeta rejected invalid UTF-8 (%v); the reachability claim in this file's header is wrong and the end-to-end red IS constructible", err)
	}
}

// TestEmit_RefusesWholeWhenAnEdgeFailureLanded pins the refusal semantics: a
// loud failure discards the emission rather than shipping its survivors.
//
// The distinction is not cosmetic on this path. A pdf collect asserts a
// complete walk and the server retires whatever the collect did not re-emit,
// so a partial emission would DELETE the nodes it failed to rebuild instead of
// leaving them alone.
//
// The failure is seeded through the emitter's own accumulator because the
// production trigger is unreachable — see this file's header. What is being
// observed is the disposition of an accumulated failure, and that is
// reachable, real, and easy for a later edit to get wrong.
func TestEmit_RefusesWholeWhenAnEdgeFailureLanded(t *testing.T) {
	t.Parallel()

	e := newPDFEmitter(fixturePath, time.Time{})
	e.emitDocumentNode(pdf.Metadata{Title: "X"}, "X", titleSourceInfoDict)
	e.emitChunk(e.docID, "", pdf.Chunk{Kind: pdf.BlockParagraph, Text: "body", PageRange: [2]int{0, 0}}, 0)

	// Sanity: without a failure this emitter yields its accumulation.
	nodes, edges, err := e.result()
	if err != nil {
		t.Fatalf("a clean emitter returned %v; the seeded case below would prove nothing", err)
	}
	if len(nodes) == 0 || len(edges) == 0 {
		t.Fatalf("clean emitter yielded %d nodes and %d edges; the refusal below needs something to withhold", len(nodes), len(edges))
	}

	e.fail(errors.New("contains edge abc -> def at position 0: marshal edge evidence: seeded"))

	nodes, edges, err = e.result()
	if err == nil {
		t.Fatal("emitter with an accumulated failure returned no error: a lost edge position ships silently again")
	}
	if nodes != nil || edges != nil {
		t.Errorf("refusal returned %d nodes and %d edges; a failed emission must withhold everything, not ship its survivors", len(nodes), len(edges))
	}
	if !strings.Contains(err.Error(), fixturePath) {
		t.Errorf("refusal error %q does not name the document it refused", err)
	}
	if !strings.Contains(err.Error(), "seeded") {
		t.Errorf("refusal error %q dropped the underlying cause", err)
	}
}

// TestCollect_EveryContainsEdgeCarriesItsPosition is the end-to-end half: on a
// REAL collect of a real document, every CONTAINS edge arrives with a decodable
// position. This is what the silent "" return used to be able to break without
// any other test noticing, and it exercises the production path the two unit
// tests above deliberately do not.
func TestCollect_EveryContainsEdgeCarriesItsPosition(t *testing.T) {
	t.Parallel()

	dst := filepath.Join(t.TempDir(), "positions.pdf")
	body := "BT /F1 18 Tf 100 750 Td (A Heading) Tj ET\n" +
		"BT /F1 12 Tf 100 700 Td (First paragraph of the body text.) Tj ET\n" +
		"BT /F1 12 Tf 100 640 Td (Second paragraph of the body text.) Tj ET\n"
	spec := fixturelib.PageSpec{
		Fonts: fixturelib.SimpleFontSpecMap(map[string]string{"F1": "Helvetica"}),
		Body:  body,
	}
	if err := fixturelib.WritePDF(dst, []fixturelib.PageSpec{spec}); err != nil {
		t.Fatalf("WritePDF: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("fixture not written: %v", err)
	}

	c := &PDFCollector{}
	res, err := c.Collect(context.Background(), dst, collector.CollectOptions{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	contains := 0
	for _, edge := range res.Edges {
		if edge.Type != kgtypes.EdgeContains {
			continue
		}
		contains++
		if edge.Evidence == "" {
			t.Fatalf("contains edge %s -> %s carries empty Evidence: its document position is gone", edge.FromID, edge.ToID)
		}
		var md map[string]string
		if err := json.Unmarshal([]byte(edge.Evidence), &md); err != nil {
			t.Fatalf("contains edge %s -> %s has undecodable Evidence %q: %v", edge.FromID, edge.ToID, edge.Evidence, err)
		}
		if _, ok := md["position"]; !ok {
			t.Errorf("contains edge %s -> %s Evidence %v carries no position key", edge.FromID, edge.ToID, md)
		}
	}
	if contains == 0 {
		t.Fatal("the fixture produced no CONTAINS edges; the loop above asserted nothing")
	}
	t.Logf("checked %d contains edges", contains)
}
