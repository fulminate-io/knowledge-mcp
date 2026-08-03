// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptAddCriterion claims mutate(create,
// type:"criterion") and orchestrates a 4-RPC sequence: (1) step lookup via query,
// (2) criterion upsert via mutate, (3) verifies edge link, (4) contains edge link.
//
// It must fire BEFORE InterceptMutate (which gates on operation in {update,
// delete} and never claims create).
//
// Validation order is intentionally STEP-EXISTS-BEFORE-DESCRIPTION: when both
// step_id AND description are missing, the path errors with the step-not-found
// message after step_id passes the trim check.
//
// Documented divergence — writeResult [graph: <name>] suffix:
//
// The server's `writeResult` (cmd/knowledge-server/tools/
// tools_write_result.go:24-30) conditionally appends ` [graph:
// <name>]` to the success message when WriteTargetGraphName(ctx) is
// non-empty. The intercept's plain textResult does NOT thread that
// suffix. This is an intentional v1 scope decision: output byte-
// matches the server for the DEFAULT (unnamed) write-target;
// named-overlay-write parity is out of scope and would require a
// per-call wire round-trip to resolve the active write-target name
// nobody currently consumes outside that mode. The default case (the
// only one most callers hit) is unaffected.

package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/projects"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
	"github.com/fulminate-io/knowledge-mcp/internal/validate"
)

// criterionCreateArgs is the local mutate(create, type:criterion)
// wire mirror. Kept narrow to the fields this intercept reads.
type criterionCreateArgs struct {
	Operation     string `json:"operation"`
	Type          string `json:"type"`
	StepID        string `json:"step_id"`
	Description   string `json:"description"`
	Command       string `json:"command,omitempty"`
	CriterionType string `json:"criterion_type,omitempty"`

	// Status/Content/Metadata are routed onto the upserted criterion node.
	// Name and summary are deliberately absent: both are DERIVED (name from
	// description, summary from criterion_type + description + command), so the
	// accounting table rejects them rather than letting a caller-supplied value
	// silently lose to the derivation.
	Status   string            `json:"status,omitempty"`
	Content  string            `json:"content,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`

	// raw is the caller's verbatim arguments payload, captured once at the
	// dispatch entry so this arm can account for exactly the params the caller
	// supplied. Never marshaled — it has no json tag and json.Unmarshal leaves
	// unexported fields untouched.
	raw json.RawMessage
}

// InterceptAddCriterion claims mutate(create, type:"criterion") and
// orchestrates the create+link sequence client-side.
func InterceptAddCriterion(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "mutate" {
		return false, kgtools.ToolResult{}
	}
	var a criterionCreateArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		// Bad args — fall through, server emits canonical error.
		return false, kgtools.ToolResult{}
	}
	a.raw = params.Arguments
	if a.Operation != "create" || a.Type != "criterion" {
		return false, kgtools.ToolResult{}
	}

	// The throwaway mutateArgs carries only raw: the gate reads nothing else off it.
	if err := accountMutateParams(armCriterionCreate, mutateArgs{raw: a.raw}); err != nil {
		return true, errorResult(err.Error())
	}

	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("mutate(create, type=criterion): graph caller unavailable")
	}

	if err := validateCriterionArgs(ctx, gc, a); err != nil {
		return true, errorResult(err.Error())
	}

	criterionID := generateCriterionID()
	if err := upsertCriterionNode(ctx, gc, criterionID, a); err != nil {
		return true, errorResult(err.Error())
	}

	// slog.Warn-and-continue on link failure mirrors server tolerance
	// at tools_walk.go:355/359.
	if linkErr := callMutateLink(ctx, gc, criterionID, a.StepID, "verifies"); linkErr != nil {
		slog.Warn("failed to add verifies edge",
			"from", criterionID, "to", a.StepID, "error", linkErr)
	}
	if linkErr := callMutateLink(ctx, gc, a.StepID, criterionID, "contains"); linkErr != nil {
		slog.Warn("failed to add contains edge",
			"from", a.StepID, "to", criterionID, "error", linkErr)
	}

	// Bare textResult — see file header for the [graph: <name>] suffix divergence.
	return true, textResult(fmt.Sprintf("Criterion added: %s → ID: %s", a.Description, criterionID))
}

// validateCriterionArgs runs the three validation gates in the exact
// order handleAddCriterion at cmd/knowledge-server/tools/tools_walk.go:325-332
// uses: (1) step_id trim-non-empty, (2) step exists in store, (3)
// description trim-non-empty. The (step exists BEFORE description
// check) ordering is load-bearing for combined-violation parity.
func validateCriterionArgs(ctx context.Context, gc GraphCaller, a criterionCreateArgs) error {
	if strings.TrimSpace(a.StepID) == "" {
		return fmt.Errorf("mutate(create, type=criterion): step_id is required (the parent step the criterion verifies)")
	}
	node, err := render.FetchNode(ctx, gc, a.StepID)
	if err != nil || node == nil || node.Id == "" {
		return fmt.Errorf("mutate(create, type=criterion): step %s not found", a.StepID)
	}
	if strings.TrimSpace(a.Description) == "" {
		return fmt.Errorf("mutate(create, type=criterion): description is required (what the criterion verifies)")
	}
	return nil
}

// upsertCriterionNode builds the criterion payload (mirroring
// handleAddCriterion's SetValue calls at tools_walk.go:335-346) and
// fires the mutate(upsert) RPC. Returns the descriptive error on
// failure for the caller to surface.
func upsertCriterionNode(ctx context.Context, gc GraphCaller, criterionID string, a criterionCreateArgs) error {
	criterionType := a.CriterionType
	if criterionType == "" {
		criterionType = "manual"
	}
	summary := projects.DeriveCriterionSummary(criterionType, a.Description, a.Command)
	if err := validate.DerivedSummary("mutate(create, type=criterion)", "criterion.summary", a.Description+" + command", summary); err != nil {
		return err
	}
	// Caller metadata is seeded FIRST so the derived type/command keys win on a
	// key collision. Copied, never aliased — see copyCallerMetadata.
	metadata := copyCallerMetadata(a.Metadata)
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["type"] = criterionType
	if a.Command != "" {
		metadata["command"] = a.Command
	}
	// Lint the value about to be STORED, not the `command` param: the caller
	// metadata seeded above can carry a command the param never held.
	if err := validate.RunSelectorGuard("mutate(create, type=criterion)", "criterion.command", metadata["command"]); err != nil {
		return err
	}
	upsertArgs, err := json.Marshal(struct {
		Operation   string            `json:"operation"`
		Type        string            `json:"type"`
		ID          string            `json:"id"`
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Summary     string            `json:"summary"`
		Status      string            `json:"status,omitempty"`
		Content     string            `json:"content,omitempty"`
		Source      string            `json:"source"`
		Metadata    map[string]string `json:"metadata"`
	}{
		Operation: "upsert", Type: "criterion", ID: criterionID,
		Name: a.Description, Description: a.Description, Summary: summary,
		Status: a.Status, Content: a.Content,
		Source: "llm:claude", Metadata: metadata,
	})
	if err != nil {
		return fmt.Errorf("create criterion: marshal upsert: %w", err)
	}
	if _, err := executeMutate(ctx, gc, upsertArgs); err != nil {
		return fmt.Errorf("create criterion: %w", err)
	}
	return nil
}

// generateCriterionID returns a 128-bit hex string suitable for
// caller-supplied IDs to mutate(upsert). Mirrors the shape of
// pkg/store/graph_graph.go generateID (private — duplicated 4 LoC to
// avoid exporting an ID generator across the wire boundary just for
// this intercept).
func generateCriterionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// callMutateLink issues a single mutate(link) RPC and returns the
// error (or extracted text) on failure. Non-nil return signals the
// caller should slog.Warn — server-side handleAddCriterion tolerates
// link failures the same way.
func callMutateLink(ctx context.Context, gc GraphCaller, from, to, relationship string) error {
	args, err := json.Marshal(struct {
		Operation    string `json:"operation"`
		From         string `json:"from"`
		To           string `json:"to"`
		Relationship string `json:"relationship"`
	}{Operation: "link", From: from, To: to, Relationship: relationship})
	if err != nil {
		return fmt.Errorf("marshal link: %w", err)
	}
	if _, err := executeMutate(ctx, gc, args); err != nil {
		return err
	}
	return nil
}

// extractText concatenates every text content block of a tool result.
// Used by the error-propagation paths above and by the in-package test
// suites. Mirrors toolResultText in
// cmd/knowledge/internal/projects/render/wire_fetch.go.
func extractText(r kgtools.ToolResult) string {
	var sb strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}
