// SPDX-License-Identifier: Apache-2.0

package tools

// manage_checks_third_face_test.go — the THIRD caller face into the corpus-check
// analyzer, and the only one that is not a parameter surface.
//
// manage_checks(run) and `knowledge check run` each declare the knob as a named
// parameter, and the accounting test in package bootstrap partitions those two
// against each other. The topology dispatcher declares nothing: it forwards the
// caller's whole Extra map verbatim, so any key an analyzer reads is reachable
// there whether or not anyone designed for it. That is the safe direction — a
// caller is never handed a control the analyzer drops — but it means the
// analyzer's own strict parse, not a schema, is what keeps this face honest, and
// nothing was driving it.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// driveTopologyCorpusScan runs corpus_scan through the topology dispatcher with
// the given extra map, over the seeded corpus and the registered fixture repo.
func driveTopologyCorpusScan(t *testing.T, extra map[string]string) kgtools.ToolResult {
	t.Helper()
	args := map[string]any{
		"mode": "topology", "algorithm": "corpus_scan", "graph": "code",
		"repo": "runfixture", "language": "go",
	}
	if extra != nil {
		args["extra"] = extra
	}
	body, err := json.Marshal(args)
	require.NoError(t, err)
	deps := &repoTestDeps{rootDir: t.TempDir(), gc: newChecksGraphFake(checksRunCorpus()...)}
	handled, res := InterceptTopology(context.Background(), deps,
		kgtools.CallToolParams{Name: "query", Arguments: body})
	require.True(t, handled)
	return res
}

// TestTopologyDispatcher_ForwardsTheIncludeTestsKnob pins the reachability fact
// itself, because a comment in this change once asserted the opposite.
func TestTopologyDispatcher_ForwardsTheIncludeTestsKnob(t *testing.T) {
	registeredTestFileRepo(t)

	t.Run("no extra: the test-file site is unreachable", func(t *testing.T) {
		body := toolResultText(driveTopologyCorpusScan(t, nil))
		assert.NotContains(t, body, "sites_test.go",
			"the default walk on this face skips test files exactly as it does on the other two")
		assert.Contains(t, body, "sites.go", "control: the run did execute and did flag the non-test sites")
	})

	t.Run("include_tests true: the walk widens", func(t *testing.T) {
		body := toolResultText(driveTopologyCorpusScan(t, map[string]string{"include_tests": "true"}))
		assert.Contains(t, body, "sites_test.go",
			"the dispatcher forwards Extra verbatim, so the knob is honored on this face too")
	})

	t.Run("the analyzer's own parse is what keeps this face honest", func(t *testing.T) {
		// No schema property and no flag stands between a topology caller and
		// the analyzer, so a malformed value has exactly one gate. This is the
		// reason the third face needs no row in the parameter accounting: it
		// carries no named parameter to classify.
		body := toolResultText(driveTopologyCorpusScan(t, map[string]string{"include_tests": "sometimes"}))
		assert.Contains(t, body, "include_tests",
			"a malformed value must be refused naming the key, got %q", body)
		assert.Contains(t, body, "true, false", "and enumerate the admitted values")
	})
}
