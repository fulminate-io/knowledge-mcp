// SPDX-License-Identifier: Apache-2.0

// intercept_mutate_answer.go — the mutate(answer) arm, split out of
// intercept_mutate_create.go to keep that file inside the repo's file-length
// gate. The finding path stays there; it is that file's stated subject.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/validate"
)

// handleClientMutateAnswer handles mutate(answer): mark a research
// question as answered with a conclusion + link findings.
func handleClientMutateAnswer(ctx context.Context, deps ClientDeps, a mutateArgs) kgtools.ToolResult {
	gc := deps.GraphCaller()
	id := a.ID
	if id == "" {
		id = a.QuestionID
	}
	if strings.TrimSpace(id) == "" {
		return errorResult("mutate(answer): id (or question_id) is required")
	}
	clampedSummary, summaryWarn, serr := validate.ClampSummary("mutate(answer)", "summary", a.Summary)
	if serr != nil {
		return errorResult(serr.Error())
	}
	node, lerr := LookupNode(ctx, gc, id)
	if lerr != nil || node == nil {
		return errorResult(fmt.Sprintf("research not found: %s", id))
	}
	// Caller metadata is seeded FIRST so the conclusion key wins on a key
	// collision. Copied, never aliased — see copyCallerMetadata.
	metadata := copyCallerMetadata(a.Metadata)
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["conclusion"] = a.Conclusion
	updateArgs, merr := json.Marshal(struct {
		Operation string            `json:"operation"`
		ID        string            `json:"id"`
		Status    string            `json:"status"`
		Summary   string            `json:"summary"`
		Metadata  map[string]string `json:"metadata"`
	}{
		Operation: "update",
		ID:        id,
		Status:    "answered",
		Summary:   clampedSummary,
		Metadata:  metadata,
	})
	if merr != nil {
		return errorResult("mutate(answer): marshal: " + merr.Error())
	}
	if _, uerr := executeMutate(ctx, gc, updateArgs); uerr != nil {
		return errorResult("mutate(answer): update: " + uerr.Error())
	}
	if a.Findings != "" {
		for fid := range strings.SplitSeq(a.Findings, ",") {
			fid = strings.TrimSpace(fid)
			if fid == "" {
				continue
			}
			_ = LinkOne(ctx, gc, fid, id, kgtypes.EdgeAnswers)
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Research answered: %s [graph: knowledge/default]", node.SymbolName)
	if summaryWarn != "" {
		writeClientWarningsSection(&sb, []string{summaryWarn})
	}
	return textResult(sb.String())
}
