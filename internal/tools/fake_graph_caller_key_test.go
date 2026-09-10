// SPDX-License-Identifier: Apache-2.0

package tools

// fake_graph_caller_key_test.go — the fake's per-family routing key, asserted.
//
// SPLIT OUT OF fake_graph_caller_test.go because that file reached the 500-line
// cap; this test is the only thing in it that drives targetGraphKey rather than
// the fake itself, so it is the natural seam.

import (
	"testing"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// TestTargetGraphKey_PracticeKeysTheSingletonUnderDefault is what makes the
// practice arm above load-bearing.
//
// NOTHING DROVE IT, which is how it kept returning graphKey{practice, ""} for
// the unselected read the whole change makes normal — a key no fixture seals
// under, so every practice fixture routed through it would have missed while the
// suite stayed green. The rows are the two shapes the server distinguishes.
func TestTargetGraphKey_PracticeKeysTheSingletonUnderDefault(t *testing.T) {
	assert.Equal(t, graphKey{Type: "practice", Name: workingset.DefaultInstanceName},
		targetGraphKey(&knowledgev1.GraphSelector{Graph: "practice"}),
		"an unselected practice read addresses the ONE combined graph, which the collector seals under default")
	assert.Equal(t, graphKey{Type: "practice", Name: "go"},
		targetGraphKey(&knowledgev1.GraphSelector{Graph: "practice", Language: "go"}),
		"and a legacy read still names the pre-singleton graph, exactly as the server resolves it")

	// THE CONTROLS: the families whose instance field genuinely is the key must be
	// unaffected, or the rows above are satisfied by an arm that returns default
	// for everything. Code is the instance-keyed one; a name-keyed family is here
	// beside it because the two reach different arms.
	assert.Equal(t, graphKey{Type: "code", Name: "myrepo"},
		targetGraphKey(&knowledgev1.GraphSelector{Graph: "code", Repo: "myrepo"}))
	assert.Equal(t, graphKey{Type: "web", Name: "site-alpha"},
		targetGraphKey(&knowledgev1.GraphSelector{Graph: "web", Name: "site-alpha"}))

	// AND AN ACCOUNT REACHES NO KEY AT ALL. No family is account-keyed since the
	// account-keyed inventory families retired, so the fake must MISS on a Target
	// carrying one — exactly as the server refuses it — rather than quietly
	// agreeing with a client that builds a selector the server rejects.
	assert.Equal(t, graphKey{Type: "web", Name: ""},
		targetGraphKey(&knowledgev1.GraphSelector{Graph: "web", Account: "acct"}),
		"an account names a field no resolver reads: the fake must miss on it")
}
