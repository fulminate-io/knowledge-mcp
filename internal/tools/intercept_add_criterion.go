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
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
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
	Summary       string `json:"summary"`
	Command       string `json:"command,omitempty"`
	CriterionType string `json:"criterion_type,omitempty"`

	// Status/Content/Metadata are routed onto the upserted criterion node.
	// Summary is caller-authored, required and clamped like every other
	// embed-only-knowledge type. Name is deliberately absent: it is DERIVED from
	// the description's first line via projects.DeriveCriterionName, so the
	// accounting table rejects it rather than letting a caller-supplied value
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
	clampWarn, err := upsertCriterionNode(ctx, gc, criterionID, a)
	if err != nil {
		return true, errorResult(err.Error())
	}
	var warnings []string
	if clampWarn != "" {
		warnings = append(warnings, clampWarn)
	}

	// A FAILED LINK IS AN ERROR THE CALLER SEES, not a daemon-log warning. The
	// two edges are the criterion's whole attachment: without contains the
	// criterion is invisible to plan_tree (which walks contains only), and
	// without verifies the back-reference from criterion to step is gone. Either
	// way the node exists and is unwired, and the caller is the only party that
	// can repair it — reporting "Criterion added" over that state hands back a
	// success for work that did not happen. The node is NOT rolled back: this
	// path is four separate RPCs with no enclosing transaction, so the honest
	// report is the orphan's id plus the exact links to issue.
	//
	// BOTH links are attempted before reporting, so the error names the FULL
	// residual state rather than only the first failure. That is diagnostic
	// completeness, not tolerance — the call fails either way.
	var linkFailures []string
	if linkErr := callMutateLink(ctx, gc, criterionID, a.StepID, "verifies"); linkErr != nil {
		slog.Warn("failed to add verifies edge",
			"from", criterionID, "to", a.StepID, "error", linkErr)
		linkFailures = append(linkFailures, fmt.Sprintf(
			"criterion--verifies-->step (%s → %s): %v", criterionID, a.StepID, linkErr))
	}
	if linkErr := callMutateLink(ctx, gc, a.StepID, criterionID, "contains"); linkErr != nil {
		slog.Warn("failed to add contains edge",
			"from", a.StepID, "to", criterionID, "error", linkErr)
		linkFailures = append(linkFailures, fmt.Sprintf(
			"step--contains-->criterion (%s → %s): %v", a.StepID, criterionID, linkErr))
	}
	if len(linkFailures) > 0 {
		return true, errorResult(fmt.Sprintf(
			"mutate(create, type=criterion): criterion node %s was created but is NOT attached to step %s — "+
				"%d of its 2 attachment edges failed: %s. "+
				"The criterion is unwired (plan_tree walks contains, so it will not render under the step). "+
				"Re-issue the failed edge(s) with mutate(link), or delete %s and retry.",
			criterionID, a.StepID, len(linkFailures), strings.Join(linkFailures, "; "), criterionID))
	}

	// Bare textResult — see file header for the [graph: <name>] suffix divergence.
	var sb strings.Builder
	fmt.Fprintf(&sb, "Criterion added: %s → ID: %s", a.Description, criterionID)
	writeClientWarningsSection(&sb, warnings)
	return true, textResult(sb.String())
}

// validateCriterionArgs runs the four validation gates in order: (1) step_id
// trim-non-empty, (2) step exists in store, (3) the found node is one of the
// criteria-owning container types, (4) description trim-non-empty. The first,
// second and fourth mirror handleAddCriterion at
// cmd/knowledge-server/tools/tools_walk.go:325-332, and the (step exists BEFORE
// description check) ordering is load-bearing for combined-violation parity.
//
// GATE 3 SITS BETWEEN THE EXISTENCE AND DESCRIPTION CHECKS deliberately: it
// reads the node the existence check just fetched, and placing it after the
// description check would reorder the documented step-exists-before-description
// pairing. It refuses a target the close-out rollup cannot hold — the rollup
// walks only clientRollupContainerTypes, so a criterion hanging off any other
// type is announced at close-out and holds nothing.
func validateCriterionArgs(ctx context.Context, gc GraphCaller, a criterionCreateArgs) error {
	if strings.TrimSpace(a.StepID) == "" {
		return fmt.Errorf("mutate(create, type=criterion): step_id is required (the parent step the criterion verifies)")
	}
	node, err := render.FetchNode(ctx, gc, a.StepID)
	if err != nil || node == nil || node.Id == "" {
		return fmt.Errorf("mutate(create, type=criterion): step %s not found", a.StepID)
	}
	if !isClientRollupContainer(kgtypes.NodeType(node.GetType())) {
		accepted := make([]string, 0, len(clientRollupContainerTypes))
		for _, ct := range clientRollupContainerTypes {
			accepted = append(accepted, string(ct))
		}
		return fmt.Errorf(
			"mutate(create, type=criterion): step_id %s is a %s, which cannot own criteria — "+
				"the close-out rollup walks only the container types, so a criterion attached here "+
				"is announced at close-out and holds nothing. Accepted types: %s",
			a.StepID, node.GetType(), strings.Join(accepted, ", "))
	}
	if strings.TrimSpace(a.Description) == "" {
		return fmt.Errorf("mutate(create, type=criterion): description is required (what the criterion verifies)")
	}
	return nil
}

// upsertCriterionNode builds the criterion payload (mirroring
// handleAddCriterion's SetValue calls at tools_walk.go:335-346) and
// fires the mutate(upsert) RPC. Returns the non-fatal summary-clamp
// warning, and the descriptive error on failure for the caller to surface.
//
// WHY THIS CLIENT CLAMP IS THE ONLY ENFORCEMENT ON THIS PATH: this arm writes
// through mutate(upsert), and upsert is on the server's create-validation
// BYPASS ALLOWLIST (cmd/knowledge-server/internal/bootstrap/
// engine_mutate_upsert_allowlist.go lists proxy, graph_type_def, log-backend
// and criterion). The server's !Summarizable non-empty-summary rule therefore
// never runs for a criterion created this way. The create_plan and
// create_test_plan paths DO reach that rule because they go through
// create_batch; this one does not. Remove the clamp and a summary-less
// criterion is written silently.
func upsertCriterionNode(ctx context.Context, gc GraphCaller, criterionID string, a criterionCreateArgs) (string, error) {
	criterionType := a.CriterionType
	if criterionType == "" {
		criterionType = "manual"
	}
	// POSITION IS DELIBERATE: the clamp sits where the derived-summary gate sat,
	// i.e. after the step-exists and description gates in validateCriterionArgs
	// and ahead of RunSelectorGuard, so the documented step-exists-before-
	// description ordering is untouched.
	summary, clampWarn, serr := validate.ClampSummary("mutate(create, type=criterion)", "criterion.summary", a.Summary)
	if serr != nil {
		return "", serr
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
		return "", err
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
		Name: projects.DeriveCriterionName(a.Description), Description: a.Description, Summary: summary,
		Status: a.Status, Content: a.Content,
		Source: "llm:claude", Metadata: metadata,
	})
	if err != nil {
		return "", fmt.Errorf("create criterion: marshal upsert: %w", err)
	}
	if _, err := executeMutate(ctx, gc, upsertArgs); err != nil {
		return "", fmt.Errorf("create criterion: %w", err)
	}
	return clampWarn, nil
}

// generateCriterionID returns a 128-bit hex string suitable for
// caller-supplied IDs to mutate(upsert). Mirrors the shape of
// cmd/knowledge-server/internal/store/graph_id_gen.go:23 generateID
// (private — duplicated 4 LoC to
// avoid exporting an ID generator across the wire boundary just for
// this intercept).
func generateCriterionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// callMutateLink issues a single mutate(link) RPC and returns the
// error (or extracted text) on failure. A non-nil return FAILS the
// enclosing create: the criterion node is already written by then, so
// the caller reports the unwired node and the edges to re-issue rather
// than logging a warning and answering "Criterion added".
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
