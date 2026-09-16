// SPDX-License-Identifier: Apache-2.0

package tools

// rebuild_keyless_refusal_test.go is the keyless-BM25 fix's rebuild-refusal row. rebuild_segments is
// VECTOR-gated server-side ("a live row's eligibility authority is vector
// possession", composite_db_segment_rebuild.go), so on a KEYLESS graph it
// correctly scans nothing — but the sentence it answered with sent the operator
// to the LLM-coverage column of manage(status), which on a keyless install reads
// "0 of N" forever. That is a dead end presented as a next step.
//
// The ticket leaves the choice between two arms to planning; the planner
// recommended and the orchestrator settled the REFUSAL-TEXT arm, because the
// vectorless-rebuild arm moves a cloud SQL access path and a partial index, which
// this ticket's scope forbids. So the refusal must NAME THE KEYLESS PATH that
// does the rebuild — which, after this ticket's other changes, is the BM25 drain
// itself.

import (
	"context"
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestRebuildSegments_KeylessGraphRefusalNamesTheKeylessPath asserts the refusal
// by its SUBSTANCE rather than by a hand-copied literal: it must name the text
// index and the drain that builds it, and it must no longer send a keyless reader
// to the embed-coverage column.
//
// RED ON THE RELEASE THE DEFECT WAS REPRODUCED ON, verbatim (validation run1): "rebuild_segments:
// knowledge/default scanned no nodes — nothing to do. This graph holds 1 nodes
// but NONE are embedded yet, so there is nothing to build segments from. Check
// the LLM-coverage column of manage({"operation":"status"})."
func TestRebuildSegments_KeylessGraphRefusalNamesTheKeylessPath(t *testing.T) {
	depsFor := func(nodes, embedded int32) ClientDeps {
		return &rebuildStatsDeps{
			interceptDeps: &interceptDeps{},
			stats:         &knowledgev1.GraphStats{NodeCount: nodes, BinaryVectorCount: embedded},
		}
	}

	msg := rebuildScannedNothing(context.Background(), depsFor(1, 0),
		manageArgs{Graph: "knowledge", Name: "default"})

	// It must still be TRUE about what it checked: this axis rebuilds from vectors
	// and this graph has none.
	if !strings.Contains(msg, "NONE are embedded yet") {
		t.Errorf("the refusal no longer states the vector fact it actually checked:\n%s", msg)
	}
	// AND IT MUST NAME THE PATH THAT DOES WORK ON A KEYLESS INSTALL.
	for _, want := range []string{"text", "BM25"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not mention %q, so a keyless operator is told only what does NOT "+
				"work:\n%s", want, msg)
		}
	}
	if !strings.Contains(msg, "search") {
		t.Errorf("the refusal does not tell the reader that text search still serves this graph:\n%s", msg)
	}
	// AND IT MUST NO LONGER DIRECT THE READER TO A COLUMN THAT CANNOT MOVE. The
	// imperative is what made it a dead-end next step — "Check the LLM-coverage
	// column" on a keyless install points at a cell that reads 0 of N forever.
	// Naming that column as the place the VECTOR axis is tracked is a different
	// sentence and is allowed; being sent there as the remedy is not.
	if strings.Contains(msg, "Check the LLM-coverage column") {
		t.Errorf("the refusal still DIRECTS a keyless operator to the LLM-coverage column, which reads "+
			"0 of N forever on an install with no embed credential:\n%s", msg)
	}

	// KNOWN POSITIVE / CONTROL, in the same run through the same function: the
	// EMBEDDED-graph arms are unchanged, so the additions above are scoped to the
	// keyless arm rather than pasted into every message.
	drained := rebuildScannedNothing(context.Background(), depsFor(3117, 2556),
		manageArgs{Graph: "practice", Name: "design-patterns"})
	if !strings.Contains(drained, "since the stored watermark") {
		t.Errorf("CONTROL: the drained-graph arm changed; it must still name the watermark:\n%s", drained)
	}
	if strings.Contains(drained, "BM25") {
		t.Errorf("CONTROL: the keyless guidance leaked into the drained-graph arm, whose reader is not "+
			"keyless and whose cause is the watermark:\n%s", drained)
	}
}
