// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/projects"
	"github.com/fulminate-io/knowledge-mcp/internal/validate"
)

// createTicketArgs is the complete set of create_ticket wire params — every
// field here is declared by CreateTicketToolDef and every declared property is
// read here, with no passthrough slots. The ticket's backend metadata
// (`backend`, `linear_id`, `external_url`, `linear_project_id`,
// `linear_group_id`, `linear_group_key`) is NOT caller-suppliable: BuildTicketNode
// stamps it from the backends.RemoteRef that backend.CreateTicket returned and
// from the parent project's own metadata.
type createTicketArgs struct {
	Name             string          `json:"name"`
	ProjectID        string          `json:"project_id"`
	Description      string          `json:"description"`
	Summary          string          `json:"summary"`
	ExternalID       string          `json:"external_id,omitempty"`
	Priority         string          `json:"priority,omitempty"`
	Labels           string          `json:"labels,omitempty"`
	PatternIDs       []string        `json:"pattern_ids,omitempty"`
	NoPatternsReason string          `json:"no_patterns_reason,omitempty"`
	ProposedPatterns json.RawMessage `json:"proposed_patterns,omitempty"`
	LanguagePatterns []string        `json:"language_patterns,omitempty"`
	Format           string          `json:"format,omitempty"`
}

// InterceptCreateTicket handles the create_ticket MCP call. Routes via
// the parent project's `backend` metadata key — fetched via a single
// query lookup before deciding to engage the backend write-through.
//
// Cases:
//   - parent project lookup fails / project_id empty → fall through
//     (let the server's local-only path emit its own error).
//   - parent has no `backend` metadata → fall through (local-only).
//   - parent has `backend` but ByName(...) returns nil → hard error
//     ("backend X recorded on parent but not currently configured").
//   - Linear CreateTicket fails → hard error, no local node.
//   - Linear succeeds, server forward fails → error naming the Linear
//     ticket identifier so operator can reconcile.
func InterceptCreateTicket(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "create_ticket" {
		return false, kgtools.ToolResult{}
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("create_ticket: graph caller unavailable")
	}

	var a createTicketArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return true, errorResult("create_ticket: invalid arguments: " + err.Error())
	}
	// Ahead of every validation and BEFORE any backend side-effect: the decode
	// above discards any top-level key createTicketArgs has no field for, so an
	// undeclared param would otherwise vanish into a successful create — and a
	// rejection that ran later could leave an orphan remote ticket behind.
	if err := rejectUndeclaredParams("create_ticket", "", CreateTicketToolDef().InputSchema.Properties, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	if err := validateCreateTicketArgs(a); err != nil {
		return true, errorResult(err.Error())
	}
	clamped, clampWarn, serr := validate.ClampSummary("create_ticket", "summary", a.Summary)
	if serr != nil {
		return true, errorResult(serr.Error())
	}
	a.Summary = clamped

	// Validate + resolve patterns over the wire BEFORE any backend side-effect:
	// a tristate violation must reject before the remote Linear CreateTicket
	// runs (no orphan remote ticket). The resolved effective IDs + unresolved
	// slices + warnings flow into both the backend-backed and local-only paths.
	ticketArgs := buildTicketArgsFromWire(a)
	res, presolveErr := resolvePatternFields(ctx, gc, ticketArgs.PatternIDs, ticketArgs.NoPatternsReason, ticketArgs.ProposedPatterns, ticketArgs.LanguagePatterns)
	if presolveErr != nil {
		return true, errorResult("create_ticket: " + presolveErr.Error())
	}
	ticketArgs.PatternIDs = res.effectivePatternIDs
	ticketArgs.LanguagePatterns = res.effectiveLangIDs
	if clampWarn != "" {
		res.warnings = append(res.warnings, clampWarn)
	}

	_, parentBackendName, parentURL, parentBackendID, parentMeta, lookupErr := lookupNodeBackend(ctx, gc, a.ProjectID)
	if lookupErr != nil {
		return true, errorResult("create_ticket: lookup parent project: " + lookupErr.Error())
	}
	if parentBackendName == "" {
		// Local-only parent — run the local-graph composition path
		// client-side. The server has no create_ticket handler
		// now has no server-side dispatch so we MUST claim this case. Patterns are
		// already validated + resolved above; pass the resolved ticketArgs + result.
		return true, createTicketLocalOnly(ctx, gc, a, ticketArgs, res)
	}

	backend := deps.BackendResolver().ByName(parentBackendName)
	if backend == nil {
		return true, errorResult(fmt.Sprintf(
			"create_ticket: backend %q is recorded on parent project %q but not currently configured (set the env var or use a different parent)",
			parentBackendName, a.ProjectID,
		))
	}
	return true, createTicketBackendBacked(ctx, gc, a, ticketArgs, res, backend, backendParent{
		name:     parentBackendName,
		id:       parentBackendID,
		url:      parentURL,
		groupID:  parentMeta[parentBackendName+"_group_id"],
		groupKey: parentMeta[parentBackendName+"_group_key"],
	})
}

// backendParent carries the parent project's resolved backend coordinates into
// the backend-backed ticket path (inherited group + remote ref components).
type backendParent struct {
	name, id, url, groupID, groupKey string
}

// createTicketBackendBacked runs the backend write-through then composes + persists
// the local-graph mirror. ticketArgs already carries the resolved effective pattern
// IDs; res supplies the unresolved-ID metadata + non-fatal warnings. The Linear
// CreateTicket runs only after pattern validation passed (the caller validated
// before reaching here), so a tristate violation never leaks a remote ticket.
func createTicketBackendBacked(ctx context.Context, gc GraphCaller, a createTicketArgs, ticketArgs projects.TicketArgs, res patternResolution, backend backends.Backend, parent backendParent) kgtools.ToolResult {
	parentGroup := backends.Group{ID: parent.groupID, Key: parent.groupKey}
	parentRef := backends.RemoteRef{ID: parent.id, URL: parent.url}

	ref, err := backend.CreateTicket(ctx, backends.TicketCreateArgs{
		GroupKey:    parentGroup.Key,
		ProjectRef:  parentRef,
		Name:        a.Name,
		Description: a.Description,
		Status:      "",
		Priority:    0,
		Labels:      a.Labels,
	})
	if err != nil {
		return errorResult("create_ticket: " + err.Error())
	}

	// Compose the local-graph mirror with backend metadata stamped onto
	// the ticket node via BuildTicketNode, then persist via PersistBatch +
	// bundle_id. Pass the real unresolved slices so the metadata lands on the node.
	nodes, edges := projects.BuildTicketNode(ticketArgs, res.unresolvedIDs, res.unresolvedLangIDs, backend.Name(), ref, parentGroup, parent.id)
	bundleID := newBundleID()
	ids, perr := PersistBatch(ctx, gc, nodes, edges, bundleID)
	if perr != nil {
		return errorResult(fmt.Sprintf(
			"create_ticket: Linear create succeeded for %q (remote_id=%s, identifier=%s, url=%s) but local mirror failed: %v",
			a.Name, ref.ID, ref.Identifier, ref.URL, perr,
		))
	}
	if len(ids) == 0 {
		return errorResult("create_ticket: persist returned no IDs")
	}
	ticketID := ids[0]
	if a.Format == "json" {
		return jsonResult(map[string]any{
			"id":       ticketID,
			"name":     a.Name,
			"warnings": orNilWarnings(res.warnings),
		})
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Ticket created: %s → ID: %s", a.Name, ticketID)
	writeClientWarningsSection(&sb, res.warnings, "\n")
	return textResult(sb.String() + " [graph: knowledge/default]")
}

// validateCreateTicketArgs runs the hard validation gates the server's
// handleCreateTicket previously ran (a prior phase stubbed those out
// server-side, so the client owns this now): name and project_id present.
// The summary is NOT gated here — InterceptCreateTicket clamps an over-cap
// author summary (with a warning) via validate.ClampSummary rather than
// hard-rejecting it.
func validateCreateTicketArgs(a createTicketArgs) error {
	if err := validate.Name("create_ticket", a.Name); err != nil {
		return err
	}
	if strings.TrimSpace(a.ProjectID) == "" {
		return fmt.Errorf("project_id is required")
	}
	return nil
}

// createTicketLocalOnly composes the local-only ticket graph (no
// backend write-through) and persists it via PersistBatch under one
// bundle_anchor. Mirrors the server-side handleCreateTicket local
// branch byte-for-byte: same validation, same BuildTicketNode call,
// same render shape. ticketArgs already carries the resolved effective
// pattern IDs (validated + resolved by the caller before this point); res
// supplies the unresolved-ID metadata + the non-fatal warnings.
func createTicketLocalOnly(ctx context.Context, gc GraphCaller, a createTicketArgs, ticketArgs projects.TicketArgs, res patternResolution) kgtools.ToolResult {
	nodes, edges := projects.BuildTicketNode(ticketArgs, res.unresolvedIDs, res.unresolvedLangIDs, "", backends.RemoteRef{}, backends.Group{}, "")
	bundleID := newBundleID()
	ids, perr := PersistBatch(ctx, gc, nodes, edges, bundleID)
	if perr != nil {
		return errorResult("create ticket: " + perr.Error())
	}
	if len(ids) == 0 {
		return errorResult("create ticket: persist returned no IDs")
	}
	ticketID := ids[0]
	if a.Format == "json" {
		return jsonResult(map[string]any{
			"id":       ticketID,
			"name":     a.Name,
			"warnings": orNilWarnings(res.warnings),
		})
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Ticket created: %s → ID: %s", a.Name, ticketID)
	writeClientWarningsSection(&sb, res.warnings, "\n")
	if section := suggestPatternsForPlanClient(a.Name, a.Description, a.NoPatternsReason); section != "" {
		sb.WriteString(section)
	}
	return textResult(sb.String() + " [graph: knowledge/default]")
}

// buildTicketArgsFromWire converts the wire ticket shape into the
// domain projects.TicketArgs. The ProposedPatterns RawMessage is
// decoded into typed projects.ProposedPatternArgs slots.
func buildTicketArgsFromWire(a createTicketArgs) projects.TicketArgs {
	ticketArgs := projects.TicketArgs{
		Name:             a.Name,
		Description:      a.Description,
		Summary:          a.Summary,
		ProjectID:        a.ProjectID,
		ExternalID:       a.ExternalID,
		Priority:         a.Priority,
		Labels:           a.Labels,
		PatternIDs:       a.PatternIDs,
		NoPatternsReason: a.NoPatternsReason,
		LanguagePatterns: a.LanguagePatterns,
	}
	if len(a.ProposedPatterns) > 0 {
		var pps []struct {
			Name   string `json:"name"`
			Sketch string `json:"sketch,omitempty"`
		}
		if err := json.Unmarshal(a.ProposedPatterns, &pps); err == nil {
			for _, pp := range pps {
				ticketArgs.ProposedPatterns = append(ticketArgs.ProposedPatterns, projects.ProposedPatternArgs{
					Name:   pp.Name,
					Sketch: pp.Sketch,
				})
			}
		}
	}
	return ticketArgs
}

// _ binds the store import — used indirectly via projects.BuildTicketNode.
var _ = kgtypes.NodeTicket
