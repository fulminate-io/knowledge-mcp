// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// intercept_manage_drop_graph_practice_test.go — A PRACTICE GRAPH IS NEVER A
// DESTRUCTIVE TARGET, AND THE CLIENT REFUSES BEFORE THE WIRE.
//
// THE STANDING BAN. Practice graphs are never force-deleted, tombstoned or
// re-emitted over, and a drop_graph naming practice is refused by the client and
// again by the server. Two layers, deliberately: the server's refusal is what
// stops a caller that skipped the client's, and the client's is what stops the
// request being made at all.
//
// THE CLIENT LAYER WAS MISSING. handleClientDropGraph checked for an absent
// graph and for a retired family and then issued the mutation; nothing in it
// named practice. The corpus check that enforces this — the CLIENT drop_graph
// handler must carry the practice refusal in its own body — flagged the function
// by name, and the refusal was absent at the base commit too, so this closes a
// standing hole rather than one the deletion opened.
//
// WHY POSITION MATTERS AND IS PART OF THE RULE. The local L2 segment-cache
// teardown runs once the server-side Execute succeeds, so a refusal placed
// downstream produces an error message for a drop that already happened. The
// refusal has to come BEFORE the Execute, in this function.
//
// WHAT THE TEST OBSERVES, therefore, is that NO MUTATION REACHES THE WIRE —
// which is the property, rather than the message.
func TestDropGraph_APracticeGraphIsRefusedBeforeAnyMutation(t *testing.T) {
	for _, name := range []string{"go", "python", ""} {
		fc := &fakeGraphCaller{}
		args := `{"operation":"drop_graph","graph":"practice"`
		if name != "" {
			args += `,"name":"` + name + `"`
		}
		args += `}`

		handled, res := dropGraphCall(t, fc, args)
		require.True(t, handled, "the client claims the call")
		require.Truef(t, res.IsError, "drop_graph on the practice family must be REFUSED (name=%q)", name)

		body := toolResultText(res)
		assert.Contains(t, body, "practice", "the refusal names the family it rejected")
		assert.Empty(t, fc.execRequests,
			"NO MUTATION MAY REACH THE WIRE. The local segment-cache teardown runs once the "+
				"server-side Execute succeeds, so a refusal that lets the request out and reports "+
				"afterwards is a message about a drop that already happened")
	}
}

// TestDropGraph_ASurvivingFamilyStillDrops is the CONTROL. Without it, a handler
// broken to refuse every drop would satisfy every row above while removing the
// operation from the product.
func TestDropGraph_ASurvivingFamilyStillDrops(t *testing.T) {
	fc := &fakeGraphCaller{}
	handled, res := dropGraphCall(t, fc,
		`{"operation":"drop_graph","graph":"code","name":"some-repo"}`)
	require.True(t, handled)
	assert.False(t, res.IsError,
		"control: a code graph is a legitimate drop target and must still be dropped: %s",
		toolResultText(res))
	assert.NotEmpty(t, fc.execRequests,
		"control: and the mutation reaches the wire, so the emptiness assertion above is a "+
			"statement about practice rather than about a handler that never issues anything")
}
