// SPDX-License-Identifier: Apache-2.0

package tools

// corpus_check_gate_declaration_test.go — a checks-graph write that names ONLY
// the test-file declaration still faces the admission gate.
//
// THE GATE HAS A CHEAP SKIP: a payload mentioning no contract key cannot change
// any check's validity, so it pays no reads. The declaration IS a contract key —
// it decides which files a check walks — and a key left off that list is a hole
// rather than an optimization: the write lands, the check now claims a wider
// scope, and nothing re-ran its fixtures.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestCorpusCheckGate_DeclarationOnlyUpdateReachesTheGate drives the exact write
// that sets the declaration on an already-admitted check.
func TestCorpusCheckGate_DeclarationOnlyUpdateReachesTheGate(t *testing.T) {
	// The stored node is a check that is INVALID once merged (no fixtures), so
	// reaching the gate is observable as a refusal. A gate that skipped this
	// payload would let the write through silently.
	stored := nodeResultJSON(t, "chk-1", "finding", map[string]string{
		"check_type": "ast_pattern", "severity": "warning", "language": "go",
		"dsl_pattern": "defer $X.Close()",
	})
	fc := fixturedCaller(t, map[string]kgtools.ToolResult{"chk-1": stored})
	_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: mutateJSON(t, map[string]any{
			"operation": "update", "graph": "checks", "language": "go", "id": "chk-1",
			"metadata": map[string]string{corpus.MetaAppliesToTests: "true"},
		}),
	})
	require.True(t, res.IsError,
		"a write naming only the declaration must re-run admission: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "check_fixture_bad",
		"and the refusal is the merged node's own, which is what proves the gate ran")
	assert.Empty(t, fc.execMutations, "nothing may be written when admission refuses")
}

// TestCorpusCheckGate_NonContractKeyStillSkips is the FALSIFYING CONTROL for the
// row above. Without it the test is satisfied by a gate that stopped skipping
// anything at all, which would be a different change with a different cost.
func TestCorpusCheckGate_NonContractKeyStillSkips(t *testing.T) {
	stored := nodeResultJSON(t, "chk-1", "finding", map[string]string{
		"check_type": "ast_pattern", "severity": "warning", "language": "go",
	})
	fc := fixturedCaller(t, map[string]kgtools.ToolResult{"chk-1": stored})
	_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: mutateJSON(t, map[string]any{
			"operation": "update", "graph": "checks", "language": "go", "id": "chk-1",
			"metadata": map[string]string{"author_note": "tidied the prose"},
		}),
	})
	require.False(t, res.IsError,
		"a payload naming no contract key cannot change a check's validity and must pay no reads: %s", toolResultText(res))
}

// TestCorpusCheckGate_ContractKeysMatchTheContract is the S9 pin on the THIRD
// copy of the check vocabulary.
//
// IT CANNOT LIVE BESIDE THE OTHER PIN. The analyzer's copy is pinned in package
// corpusscan; contractKeys is unexported in package tools and tools imports
// corpusscan, so the reverse import is a cycle. This is that test's sibling, in
// the package that owns the list, with its own hand-pinned count for the same
// reason: comparing the list against itself would stay green if a key were
// dropped from both the list and the walk.
func TestCorpusCheckGate_ContractKeysMatchTheContract(t *testing.T) {
	const wantKeys = 9
	require.Len(t, contractKeys, wantKeys,
		"a key was added to or dropped from the check contract without updating this gate")

	// Every contract constant the contract declares must be on the list, named
	// here rather than derived from it — a derivation would be the same
	// self-comparison the count above exists to avoid.
	for _, want := range []string{
		corpus.MetaCheckType, corpus.MetaSeverity, corpus.MetaLanguage,
		corpus.MetaDSLPattern, corpus.MetaCheckWhere, corpus.MetaFixtureBad,
		corpus.MetaFixtureGood, corpus.MetaLLMOnly, corpus.MetaAppliesToTests,
	} {
		assert.Contains(t, contractKeys, want,
			"a contract key missing here is a checks-graph write that skips admission")
	}
}
