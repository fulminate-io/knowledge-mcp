// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestQueryGraphLabelFor_PracticeSpellingsDiffer pins the tenth partition
// home, queryGraphLabelFor, at the ONE place a caller learns which practice
// corpus answered a read: the render header.
//
// THE TWO PRACTICE SPELLINGS MUST DIFFER. A bare `practice` is the whole
// combined graph and `practice:<hub>` is a read narrowed to one source hub. The
// function is unexported and both of its callers (renderQueryTool and the by-id
// render) serve practice, so no tools-package test reaches it; the tools package
// pins its own renderer, domainGraphLabel, and a mutation that drops the hub leg
// HERE left every suite green. This test is that missing observer: deleting the
// `a.Source` leg turns the hub row into a bare `practice` and fails the NotEqual.
//
// THERE WAS A THIRD SPELLING, `practice:<language>`, for a read of one
// pre-singleton graph. It went with those graphs: the field addresses none of
// them and every practice arm refuses it, so a label composed from it would have
// named a read that never ran. The row below asserts the ABSENCE, because a leg
// that came back would be a silent statement that the refusal had gone.
func TestQueryGraphLabelFor_PracticeSpellingsDiffer(t *testing.T) {
	whole := queryGraphLabelFor(queryArgs{Graph: "practice"})
	hub := queryGraphLabelFor(queryArgs{Graph: "practice", Source: "hub-1"})

	assert.Equal(t, "practice", whole, "an unselected read names the family alone")
	assert.Equal(t, "practice:hub-1", hub, "a hub-scoped read names the hub")

	// THE DISCRIMINATING ASSERTION: the two differ. A renderer that returned the
	// family name for everything satisfies the first row only by coincidence and
	// fails here.
	require.NotEqual(t, whole, hub)

	// THE RETIRED LEG, asserted absent on both practice shapes.
	assert.Equal(t, "practice", queryGraphLabelFor(queryArgs{Graph: "practice", Language: "go"}),
		"`language` qualifies no header: it addresses no practice graph")
	assert.Equal(t, hub, queryGraphLabelFor(queryArgs{Graph: "practice", Source: "hub-1", Language: "go"}),
		"and it does not displace the hub either")

	// The singleton sibling qualifies by nothing, and stays that way.
	assert.Equal(t, "checks", queryGraphLabelFor(queryArgs{Graph: "checks", Language: "go"}),
		"checks has no per-instance qualifier and ignores a stray language")
}
