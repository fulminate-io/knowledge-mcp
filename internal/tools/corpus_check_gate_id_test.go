// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// corpus_check_gate_id_test.go covers the check-id contract specifically: which
// writes must carry a language-qualified id and which must not. It is split from
// corpus_check_gate_test.go, which owns the fixture-admission behavior, and
// shares that file's helpers (fixturedCaller, checkMeta, mutateJSON).

// TestCorpusCheckGate_IDQualificationSeparatesEditFromMint covers the authoring
// loop the id-shape guard closed, and the reason it was a loop rather than a
// strict rule doing its job.
//
// THE DEFECT, stated as the failure: a check written through the ordinary create
// path is given a STORE-GENERATED id, which carries no language prefix — and
// correctly so, since a generated id cannot collide. Revising that check later
// means naming it, and an update must name it by the only id it has. While the
// guard fired on every caller-supplied id, that update was refused for an id the
// author never chose and cannot change, so every check created the ordinary way
// was frozen at its first write. The collision the guard prevents happens when an
// id COMES INTO EXISTENCE; addressing one that already resolves mints nothing.
//
// THE THREE ARMS ARE A CONTROL SET, not one assertion in three shapes: the mint
// arm proves the guard still fires (without it, "exists" could simply have been
// wired to true and every arm would pass), and the prefixed-mint arm proves the
// refusal is attributable to the PREFIX rather than to upsert-of-a-new-id being
// rejected wholesale.
func TestCorpusCheckGate_IDQualificationSeparatesEditFromMint(t *testing.T) {
	// Stands in for a store-generated id. The ONLY property under test is the
	// absence of a language prefix — the id an author could not have qualified
	// because they never chose it. A readable placeholder carries that property
	// exactly as a hash would, without putting a real node id into a shipped
	// file.
	const generatedID = "generated-id-carrying-no-language-prefix"

	t.Run("update of an existing generated-id check is admitted", func(t *testing.T) {
		stored := nodeResultJSON(t, generatedID, "finding", checkMeta())
		fc := fixturedCaller(t, map[string]kgtools.ToolResult{generatedID: stored})
		_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: mutateJSON(t, map[string]any{
				"operation": "update", "graph": "checks", "language": "go", "id": generatedID,
				"metadata": map[string]string{"severity": "notice"},
			}),
		})
		require.False(t, res.IsError,
			"editing a node that already exists mints no id and must not be refused for its shape: %s",
			toolResultText(res))
		require.Len(t, fc.execMutations, 1, "the admitted edit reaches the write path")
	})

	t.Run("upsert minting an UNPREFIXED id is refused", func(t *testing.T) {
		// THE KNOWN POSITIVE for the arm above. The id does not resolve, so this
		// write brings it into existence — exactly the collision the guard exists
		// to prevent.
		fc := fixturedCaller(t, nil)
		_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: mutateJSON(t, map[string]any{
				"operation": "upsert", "graph": "checks", "language": "go", "type": "finding",
				"id": "shell-interpolated-exec", "name": "P", "summary": "s", "metadata": checkMeta(),
			}),
		})
		require.True(t, res.IsError, "minting an unprefixed id must still be refused: %s", toolResultText(res))
		assert.Contains(t, toolResultText(res), "go:<name>", "the refusal must name the required shape")
		assert.Empty(t, fc.execMutations)
	})

	t.Run("upsert minting a PREFIXED id is admitted", func(t *testing.T) {
		// The second control: it separates "refused for the prefix" from "an
		// upsert naming a node that does not exist is refused at all". That
		// distinction is not hypothetical — the read this path makes used to
		// surface a missing node as a hard error, so this arm failed for a reason
		// entirely unrelated to the id.
		fc := fixturedCaller(t, nil)
		_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: mutateJSON(t, map[string]any{
				"operation": "upsert", "graph": "checks", "language": "go", "type": "finding",
				"id": "go:shell-interpolated-exec", "name": "P", "summary": "s", "metadata": checkMeta(),
			}),
		})
		require.False(t, res.IsError,
			"a language-qualified mint is what the guard asks for and must be admitted: %s", toolResultText(res))
		// Asserted on the REFUSAL rather than on a recorded write: upsert is not
		// lowered through the mutation carrier this fake records, so an
		// execMutations count here would be measuring the dispatch shape instead
		// of the gate's verdict. The pair above is what discriminates — two
		// payloads identical but for the prefix, one refused and one not.
		assert.NotContains(t, toolResultText(res), "go:<name>",
			"a prefixed mint must not be refused by the id-shape guard")
	})
}
