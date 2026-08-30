// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptCreateResearch claims the create_research
// MCP call after the relocation. The server has no create_research handler
// has no server-side dispatch post-Phase-4 so this is the only path that
// produces a real response.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/projects"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
	"github.com/fulminate-io/knowledge-mcp/internal/validate"
)

// createResearchArgs mirrors the server-side batchResearch shape.
type createResearchArgs struct {
	Name      string                `json:"name"`
	Goal      string                `json:"goal"`
	Summary   string                `json:"summary"`
	TicketID  string                `json:"ticket_id,omitempty"`
	Questions []createResearchQuest `json:"questions"`
	Format    string                `json:"format,omitempty"`
}

type createResearchQuest struct {
	Question string `json:"question"`
	Summary  string `json:"summary,omitempty"`
	Context  string `json:"context,omitempty"`
}

// InterceptCreateResearch handles the create_research MCP call.
func InterceptCreateResearch(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "create_research" {
		return false, kgtools.ToolResult{}
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("create_research: graph caller unavailable")
	}
	var a createResearchArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return true, errorResult("invalid arguments: " + decodeArgsError(params.Arguments, err))
	}
	// Ahead of every validation and every write: the decode above discards any
	// top-level key createResearchArgs has no field for, so an undeclared param
	// would otherwise vanish into a successful create.
	if err := rejectSwallowedParamValues("create_research", params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	if err := rejectUndeclaredParams("create_research", "", CreateResearchToolDef().InputSchema.Properties, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	if len(a.Questions) == 0 {
		return true, errorResult("at least one question is required")
	}
	warnings, verr := clampResearchSummaries(&a)
	if verr != nil {
		return true, errorResult(verr.Error())
	}

	researchArgs := projects.ResearchArgs{
		Name:     a.Name,
		Goal:     a.Goal,
		Summary:  a.Summary,
		TicketID: a.TicketID,
	}
	for _, q := range a.Questions {
		researchArgs.Questions = append(researchArgs.Questions, projects.QuestionArgs{
			Question: q.Question,
			Summary:  q.Summary,
			Context:  q.Context,
		})
	}

	nodes, edges := buildResearchGraph(researchArgs)
	bundleID := newBundleID()
	ids, perr := PersistBatch(ctx, gc, nodes, edges, bundleID)
	if perr != nil {
		return true, errorResult("create research: " + perr.Error())
	}
	if len(ids) == 0 {
		return true, errorResult("create research: persist returned no IDs")
	}
	researchID := ids[0]
	if a.Format == "json" {
		return true, jsonResult(map[string]any{
			"id":           researchID,
			"name":         a.Name,
			"question_ids": ids[1:],
			"warnings":     orNilWarnings(warnings),
		})
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Research created: %s → ID: %s\n\n", a.Name, researchID)
	root, ferr := render.FetchNode(ctx, gc, researchID)
	if ferr != nil || root == nil || root.Id == "" {
		var oneLine strings.Builder
		fmt.Fprintf(&oneLine, "Research created: %s → ID: %s [graph: knowledge/default]", a.Name, researchID)
		writeClientWarningsSection(&oneLine, warnings)
		return true, textResult(oneLine.String())
	}
	childIndex, byID, dependsOn, truncated := render.AssembleSubtree(ctx, gc, root.Id, 3)
	render.RenderTreeFromIndex(&sb, root, 0, 3, childIndex, dependsOn)
	sb.WriteString(" [graph: knowledge/default]")
	writeClientWarningsSection(&sb, warnings)
	return true, render.AppendTruncationNotice(textResult(sb.String()), truncated, len(byID))
}

// clampResearchSummaries validates the research name and clamps the
// author-supplied research + question summaries in place (a is a pointer so the
// clamped values flow into buildResearchGraph). Every summary is the author's:
// each is clamped at a word boundary with a non-fatal warning rather than
// hard-rejected, and emptiness hard-rejects under the indexed field path.
// Returns the accumulated clamp warnings plus the first hard validation error.
func clampResearchSummaries(a *createResearchArgs) (warnings []string, err error) {
	if err := validate.Name("create_research", a.Name); err != nil {
		return nil, err
	}
	clamped, w, cerr := validate.ClampSummary("create_research", "summary", a.Summary)
	if cerr != nil {
		return nil, cerr
	}
	a.Summary = clamped
	if w != "" {
		warnings = append(warnings, w)
	}
	for i := range a.Questions {
		c, qw, qerr := validate.ClampSummary("create_research", fmt.Sprintf("questions[%d].summary", i), a.Questions[i].Summary)
		if qerr != nil {
			return nil, qerr
		}
		a.Questions[i].Summary = c
		if qw != "" {
			warnings = append(warnings, qw)
		}
	}
	return warnings, nil
}

// buildResearchGraph constructs the research+questions node graph
// mirroring projects.CreateResearch's body (without the in-process
// store.Store() call). Returns the nodes + edges suitable for
// PersistBatch.
func buildResearchGraph(args projects.ResearchArgs) (nodes []*knowledgev1.Node, edges []kgwire.BatchEdge) {
	researchIdx := len(nodes)
	nodes = append(nodes, &knowledgev1.Node{
		Type:        string(kgtypes.NodeResearch),
		Source:      "llm:claude",
		SymbolName:  args.Name,
		Description: args.Goal,
		Summary:     args.Summary,
		Status:      kgtypes.StatusActive,
	})
	if args.TicketID != "" {
		edges = append(edges, kgwire.BatchEdge{FromIdx: -1, FromID: args.TicketID, ToIdx: researchIdx, Type: kgtypes.EdgeKGContains})
	}
	prevQIdx := -1
	for _, q := range args.Questions {
		qIdx := len(nodes)
		nodes = append(nodes, &knowledgev1.Node{
			Type:        string(kgtypes.NodeQuestion),
			Source:      "llm:claude",
			SymbolName:  q.Question,
			Description: q.Question,
			Summary:     q.Summary,
			Content:     q.Context,
			Status:      "open",
		})
		edges = append(edges, kgwire.BatchEdge{FromIdx: researchIdx, ToIdx: qIdx, Type: kgtypes.EdgeKGContains})
		if prevQIdx >= 0 {
			edges = append(edges, kgwire.BatchEdge{FromIdx: qIdx, ToIdx: prevQIdx, Type: kgtypes.EdgeDependsOn})
		}
		prevQIdx = qIdx
	}
	return nodes, edges
}
