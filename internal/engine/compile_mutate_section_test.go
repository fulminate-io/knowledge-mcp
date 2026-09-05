// SPDX-License-Identifier: Apache-2.0

package engine

// compile_mutate_section_test.go covers R2: a planner revising ONE section of a
// chunked plan issues ONE write against ONE node, and every other node is
// untouched.
//
// THE OBSERVATION IS THE COMPILED MutationPlan, which is the write itself: its
// Selection is the blast radius and its set-fields are the payload. A test that
// only read the response could not tell a write that touched one node from one
// that rewrote the plan and happened to report success.
//
// THESE ARE CHARACTERIZATION GUARDS, green before and after this change, and
// they are labeled as such rather than claimed as red-first. The single-id update
// already scoped its write to one node; what is NEW is the PLAN SHAPE that makes
// a one-section write possible at all — before it, a section was a paragraph
// inside one 40 KB description and there was no id to name. These tests pin the
// property the new shape depends on, so a later change to the update primitive's
// scope fails here rather than silently reintroducing the whole-plan rewrite.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// sectionNode is the minimal stand-in for a persisted plan node: the fields a
// digest covers.
type sectionNode struct {
	ID          string
	Name        string
	Description string
	Position    string
}

// digest hashes the FIELDS A WRITE CAN MOVE. It deliberately excludes any
// timestamp: updated_at legitimately changes on the node a write names, so a
// digest over it would make the changed node's assertion vacuous and the
// unchanged nodes' assertions fragile. What is asserted is that no OTHER node's
// content moved.
func digest(n sectionNode) string {
	sum := sha256.Sum256([]byte(n.ID + "\x00" + n.Name + "\x00" + n.Description + "\x00" + n.Position))
	return hex.EncodeToString(sum[:])
}

func planFixture() map[string]sectionNode {
	out := map[string]sectionNode{
		"plan-1": {ID: "plan-1", Name: "the plan", Description: "the goal, the tree stamp and the reads"},
	}
	for i := range 5 {
		id := fmt.Sprintf("sec-%d", i)
		out[id] = sectionNode{
			ID: id, Name: fmt.Sprintf("Section %d", i),
			Description: fmt.Sprintf("BODY-%d", i), Position: fmt.Sprint(i),
		}
	}
	return out
}

func digestAll(nodes map[string]sectionNode) map[string]string {
	out := make(map[string]string, len(nodes))
	for id, n := range nodes {
		out[id] = digest(n)
	}
	return out
}

// applyPlan writes EXACTLY what the compiled plan says: the named fields onto
// the named ids, and nothing else. It is the faithful executor a store would be,
// so what the assertions measure is the plan's own blast radius.
func applyPlan(t *testing.T, nodes map[string]sectionNode, m *knowledgev1.MutationPlan) {
	t.Helper()
	require.NotNil(t, m)
	for _, id := range m.GetSelection().GetIds() {
		n, ok := nodes[id]
		require.True(t, ok, "the plan names an id the fixture does not hold: %s", id)
		for field, value := range m.GetSetFields() {
			switch field {
			case "description":
				n.Description = value
			case "name":
				n.Name = value
			default:
				t.Fatalf("the plan sets an unexpected field %q", field)
			}
		}
		nodes[id] = n
	}
}

// R2-a / R2-b. One update of one section moves that section and nothing else,
// and the plan carries exactly one id with exactly one field.
func TestCompileMutate_SectionUpdateTouchesOneNode(t *testing.T) {
	nodes := planFixture()
	before := digestAll(nodes)

	req, ok := compileMutate(json.RawMessage(
		`{"operation":"update","id":"sec-2","description":"BODY-2 REVISED after the review round"}`))
	require.True(t, ok)
	m := req.GetMutation()
	require.NotNil(t, m)

	// R2-b: the blast radius IS the plan.
	assert.Equal(t, []string{"sec-2"}, m.GetSelection().GetIds(),
		"the write carries exactly one node id")
	assert.Equal(t, map[string]string{"description": "BODY-2 REVISED after the review round"}, m.GetSetFields(),
		"and exactly one field — no other section's body is on the wire")

	applyPlan(t, nodes, m)
	after := digestAll(nodes)

	// R2-a: every other section node and the root are byte-identical.
	for id := range before {
		if id == "sec-2" {
			assert.NotEqual(t, before[id], after[id], "the named section changed")
			continue
		}
		assert.Equal(t, before[id], after[id], "%s must be byte-identical across a sibling's write", id)
	}

	// SAME-RUN CONTROL: a second update to a DIFFERENT section moves that one and
	// not the first, so the equalities above are a real per-node scope rather than
	// a fixture nothing ever writes to.
	req2, ok2 := compileMutate(json.RawMessage(`{"operation":"update","id":"sec-4","description":"BODY-4 REVISED"}`))
	require.True(t, ok2)
	applyPlan(t, nodes, req2.GetMutation())
	final := digestAll(nodes)
	assert.NotEqual(t, after["sec-4"], final["sec-4"], "the second write moved its own section")
	assert.Equal(t, after["sec-2"], final["sec-2"], "and left the first one alone")
}

// R2-c. A section TITLE change is also ONE write, and the root stays
// byte-identical.
//
// THIS IS THE TEST THAT BINDS THE NO-STORED-INDEX RULE. If the root carried a
// section name list, renaming a section would have to write the root too — and
// the whole point of the chunked shape is that it does not.
func TestCompileMutate_SectionRenameLeavesTheRootAlone(t *testing.T) {
	nodes := planFixture()
	before := digestAll(nodes)

	req, ok := compileMutate(json.RawMessage(`{"operation":"update","id":"sec-1","name":"Reuse targets"}`))
	require.True(t, ok)
	m := req.GetMutation()
	assert.Equal(t, []string{"sec-1"}, m.GetSelection().GetIds())
	assert.Equal(t, map[string]string{"name": "Reuse targets"}, m.GetSetFields())

	applyPlan(t, nodes, m)
	after := digestAll(nodes)
	assert.Equal(t, before["plan-1"], after["plan-1"],
		"the ROOT is byte-identical across a section rename — the positioned edges are the index, and an index that is not stored cannot go stale")
	assert.NotEqual(t, before["sec-1"], after["sec-1"])
}

// T1. The transitions: create then read, write then re-write the same section,
// write section A then read section B unchanged. Each is a property of the plan's
// scope, which is what the transitions are actually about.
func TestCompileMutate_SectionWriteTransitions(t *testing.T) {
	nodes := planFixture()

	t.Run("write then re-write the same section", func(t *testing.T) {
		for _, body := range []string{"first revision", "second revision"} {
			req, ok := compileMutate(json.RawMessage(`{"operation":"update","id":"sec-0","description":"` + body + `"}`))
			require.True(t, ok)
			applyPlan(t, nodes, req.GetMutation())
			assert.Equal(t, body, nodes["sec-0"].Description, "a re-write lands, it does not append or refuse")
		}
	})

	t.Run("write section A then read section B unchanged", func(t *testing.T) {
		bBefore := digest(nodes["sec-3"])
		req, ok := compileMutate(json.RawMessage(`{"operation":"update","id":"sec-0","description":"third revision"}`))
		require.True(t, ok)
		applyPlan(t, nodes, req.GetMutation())
		assert.Equal(t, bBefore, digest(nodes["sec-3"]))
	})
}
