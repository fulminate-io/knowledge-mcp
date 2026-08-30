// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"testing"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func mkGenreNode(facets map[string]string) *knowledgev1.Node {
	n := &knowledgev1.Node{Id: "t", Type: string(kgtypes.NodeThought)}
	for k, v := range facets {
		kgtypes.SetValue(n, k, v)
	}
	return n
}

// TestIsMachineGenre (FAILS-WHEN-ABSENT) asserts the classifier flags BOTH
// observed machine families — the source="dream:analyze" facet and the
// origin="worker:..." facet — plus the session-marker fallback, and classifies a
// human implementer/main node as NON-machine. Goes red if any facet is dropped.
//
// EVERY MACHINE FIXTURE BELOW IS A HISTORICAL SHAPE, not a live one: nothing
// produces these stamps any more. They are asserted precisely because thoughts
// carrying them are still stored, so the classifier must keep firing on them.
func TestIsMachineGenre(t *testing.T) {
	// (1) Historical source facet: source="dream:analyze" → machine.
	dreamSource := mkGenreNode(map[string]string{"source": "dream:analyze", "origin": "main"})
	assert.True(t, isMachineGenreThought(dreamSource, ""),
		`source="dream:analyze" must classify as machine-genre`)

	// (2) Historical origin facet: origin="worker:code-smell-scanner" → machine.
	workerOrigin := mkGenreNode(map[string]string{"origin": "worker:code-smell-scanner"})
	assert.True(t, isMachineGenreThought(workerOrigin, ""),
		`origin="worker:..." must classify as machine-genre`)

	// (3) Session-marker fallback: no machine source/origin, but enclosing session
	// is the historical "dream-code-smells" session → machine.
	noFacets := mkGenreNode(map[string]string{"origin": "main"})
	assert.True(t, isMachineGenreThought(noFacets, "dream-code-smells"),
		`an enclosing "dream-code-smells" session must classify as machine-genre`)

	// Human node: implementer/main origin, human source, ordinary session → NOT machine.
	human := mkGenreNode(map[string]string{"origin": "implementer", "source": "llm:claude"})
	assert.False(t, isMachineGenreThought(human, "ful-484-impl"),
		"a human implementer/main node is NOT machine-genre")

	// A nil node is not machine-genre (defensive).
	assert.False(t, isMachineGenreThought(nil, ""), "nil node is not machine-genre")
}
