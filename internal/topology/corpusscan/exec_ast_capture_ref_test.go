// SPDX-License-Identifier: Apache-2.0

// exec_ast_capture_ref_test.go — the SCAN arm of the pre-walk
// capture-reference refusal.
//
// A stored check whose where-tree names a capture nothing declares would
// otherwise walk this whole corpus and report a clean zero, because capture
// references resolve only once some node matches and the reference is never
// reached over a corpus the pattern misses. On a check whose job is to report
// an ABSENCE, that zero is the answer the caller reads.

package corpusscan

import (
	"context"
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

func TestCorpusScan_AstUndeclaredCaptureRefusesTheRun(t *testing.T) {
	// The where-trees differ in one field: the second declares `as:"T"`, which
	// is what makes "T.Y" a name the tree can bind. The pair is what proves the
	// refusal discriminates rather than rejecting every sub-pattern leaf.
	const undeclared = `{"all":[
		{"contains_pattern":{"of":"$match","pattern":"$Y.Close()"}},
		{"matches":{"of":"T.Y","regex":"^db$"}}
	]}`
	const declared = `{"all":[
		{"contains_pattern":{"of":"$match","pattern":"$Y.Close()","as":"T"}},
		{"matches":{"of":"T.Y","regex":"^db$"}}
	]}`

	entry := func(id, where string) corpusEntry {
		return corpusEntry{Check: corpus.Check{
			ID:       id,
			Severity: foundation.SeverityWarning,
			Language: "go",
			Pattern:  "defer $X.Close()",
			Where:    []byte(where),
		}, Node: &knowledgev1.Node{}}
	}

	// CONTROL FIRST, on an EMPTY corpus — the exact condition under which the
	// silence would otherwise be indistinguishable from a clean result: the
	// declared form executes and returns no error.
	if _, _, err := executeAstCheck(context.Background(), scanRequest(newFakeCaller(), "repo", t.TempDir()),
		entry("chk-declared", declared), scanOptions{}, unscopedDecision()); err != nil {
		t.Fatalf("control: the declared form must execute, got %v", err)
	}

	_, _, err := executeAstCheck(context.Background(), scanRequest(newFakeCaller(), "repo", t.TempDir()),
		entry("chk-undeclared", undeclared), scanOptions{}, unscopedDecision())
	if err == nil {
		t.Fatal("a check naming a capture nothing declares must refuse the run rather than report clean")
	}
	if !strings.Contains(err.Error(), "chk-undeclared") {
		t.Errorf("the refusal must name the check, got %q", err)
	}
	if !strings.Contains(err.Error(), "T.Y") {
		t.Errorf("the refusal must name the reference the author wrote, got %q", err)
	}
}
