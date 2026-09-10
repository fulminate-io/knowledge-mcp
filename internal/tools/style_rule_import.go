// SPDX-License-Identifier: Apache-2.0

// style_rule_import.go — manage(operation:"import_style_rules"), the generic
// rule-list import.
//
// WHAT IT WRITES, AND THE MUCH LONGER LIST OF WHAT IT DOES NOT. It writes
// PRACTICE NODES, and only practice nodes. It creates NO check, NO fixture
// example node, and it never fills the sister_check cross-link; it never calls
// manage_checks. That is the owner's ruling, RESTATED here rather than quoted
// because the repository's spell gate rewrites a word of the original: a check
// is created only by its own tool call, after an LLM has passed on the practice
// node to establish that the rule is correct, and no import, batch, landing or
// migration may create one. A rule's check shape rides its practice node as
// inert data for that later, separate, LLM-passed authoring step.
//
// ONE BATCH, AND THE HUB LANDS WITH THE NODES. The whole list is lowered into a
// single mutate(create_batch, graph:"practice", source_hub:<hub>), so
// engine.withSourceHub stamps the hub metadata key and engine.sourceHubEdges
// emits the sourced-from edge inside the SAME CreateBatch. A follow-up link call
// would leave a window in which a rule exists with no hub — invisible to the
// index, to the hub-scoped browse and to the by-hub delete alike.
//
// EVERY RULE IS VALIDATED BEFORE ANY RULE IS WRITTEN. That is what makes an
// invalid rule 3 of 5 leave ZERO nodes behind instead of two, and it is the
// reason the parse is a separate pass rather than a per-rule loop around a write.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// OpStyleRulesImport is the manage operation name, declared once so the schema
// enum, the dispatch switch, the known-operation list and the help all spell it
// the same way.
const OpStyleRulesImport = "import_style_rules"

// styleRuleNodeType is the node type a style rule is written as. A `pattern`
// body is `neverSummarize`, so the server's validateCreateNodeBody already
// refuses a summary-less or name-less one — which is why this import writes no
// validator of its own for what those two fields must contain.
const styleRuleNodeType = "pattern"

// handleImportStyleRules serves manage(operation:"import_style_rules").
func handleImportStyleRules(ctx context.Context, deps ClientDeps, a manageArgs) kgtools.ToolResult {
	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("manage(" + OpStyleRulesImport + "): graph client unavailable")
	}
	entries, err := loadStyleRuleList(a)
	if err != nil {
		return errorResult("manage(" + OpStyleRulesImport + "): " + err.Error())
	}
	existing, err := resolveExistingStyleRules(ctx, gc.Execute, entries)
	if err != nil {
		return errorResult("manage(" + OpStyleRulesImport + "): " + err.Error())
	}
	creates, updates, err := splitStyleRuleWrites(entries, existing)
	if err != nil {
		return errorResult("manage(" + OpStyleRulesImport + "): " + err.Error())
	}
	if a.DryRun {
		return styleRuleImportReport(a, creates, updates, true)
	}
	if err := writeStyleRuleCreates(ctx, gc, a.Source, creates); err != nil {
		return errorResult("manage(" + OpStyleRulesImport + "): " + err.Error())
	}
	if err := writeStyleRuleUpdates(ctx, gc, updates); err != nil {
		return errorResult("manage(" + OpStyleRulesImport + "): " + err.Error())
	}
	return styleRuleImportReport(a, creates, updates, false)
}

// loadStyleRuleList checks the two required inputs and parses the file.
//
// THE PATH MUST BE ABSOLUTE. A relative path resolves against whatever directory
// the daemon happens to be running in, which is not the caller's — so a relative
// path is either a file the caller did not mean or a not-found, and neither is
// worth guessing at.
func loadStyleRuleList(a manageArgs) ([]styleRuleEntry, error) {
	if strings.TrimSpace(a.Source) == "" {
		return nil, fmt.Errorf(
			"requires source:\"<hub id>\" — every imported rule is grouped under a source hub, " +
				"which is a node of type \"source\" you create once per language")
	}
	if strings.TrimSpace(a.Path) == "" {
		return nil, fmt.Errorf("requires path:\"<absolute path to the rule list>\"")
	}
	if !filepath.IsAbs(a.Path) {
		return nil, fmt.Errorf(
			"path %q is not absolute — it would resolve against the daemon's working directory rather than yours",
			a.Path)
	}
	body, err := os.ReadFile(a.Path)
	if err != nil {
		return nil, fmt.Errorf("read rule list %q: %w", a.Path, err)
	}
	return parseStyleRuleList(body)
}

// resolveExistingStyleRules reads which of the caller-supplied ids already
// resolve, in ONE by-id read.
//
// IT IS A READ RATHER THAN AN ASSUMPTION because the two arms are different
// writes: an id that already names a node is UPDATED per field, and an id that
// names nothing yet is CREATED carrying that id. Guessing either way loses data
// — a create over an existing node stores the body whole and clears every field
// the file omitted.
//
// THE IDS RIDE QueryPlan.ids, THE READ BULK-HYDRATE CARRIER, AND NOT
// Selection.ids. The two carry the same name and do different jobs: the proto
// declares QueryPlan.ids (field 14) as the read carrier and Selection.ids
// (field 7) as the WRITE target set, and the server's newQForPlan picks its
// constructor from by_id, then QueryPlan.ids, then Selection.from_id, falling
// through to a MATCH-ALL browse when it finds none of them. Spelled on the write
// carrier this read selects nothing, pages the hub's whole corpus down to
// `limit` rows, and reports whatever it happened to page — which over any hub
// larger than the caller's list means an existing rule reads as absent and the
// import takes its CREATE arm over it, clearing the fields the file omitted.
// That is exactly the data loss the update arm exists to prevent, so the carrier
// is load-bearing rather than stylistic.
func resolveExistingStyleRules(
	ctx context.Context, exec engine.ExecuteFn, entries []styleRuleEntry,
) (map[string]bool, error) {
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.ID != "" {
			ids = append(ids, e.ID)
		}
	}
	if len(ids) == 0 {
		return map[string]bool{}, nil
	}
	resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			Ids:       ids,
			Limit:     int32(len(ids)),
			SkipTotal: true,
		}},
		Target: practiceReadTarget(""),
	})
	if err != nil {
		return nil, fmt.Errorf("resolve which rule ids already exist: %w", err)
	}
	nodes, derr := engine.DecodeNodes(resp)
	if derr != nil {
		return nil, fmt.Errorf("resolve which rule ids already exist: %w", derr)
	}
	out := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		out[n.GetId()] = true
	}
	return out, nil
}

// styleRuleWrite is one rule lowered into the shape its arm needs.
type styleRuleWrite struct {
	entry    styleRuleEntry
	metadata map[string]string
}

// splitStyleRuleWrites partitions the parsed list into the create arm and the
// update arm and renders each rule's metadata.
func splitStyleRuleWrites(
	entries []styleRuleEntry, existing map[string]bool,
) (creates, updates []styleRuleWrite, err error) {
	for _, e := range entries {
		md, merr := styleRuleMetadata(e)
		if merr != nil {
			return nil, nil, merr
		}
		w := styleRuleWrite{entry: e, metadata: md}
		if e.ID != "" && existing[e.ID] {
			updates = append(updates, w)
			continue
		}
		creates = append(creates, w)
	}
	return creates, updates, nil
}

// writeStyleRuleCreates issues the ONE create_batch.
func writeStyleRuleCreates(ctx context.Context, gc GraphCaller, hub string, creates []styleRuleWrite) error {
	if len(creates) == 0 {
		return nil
	}
	nodes := make([]map[string]any, 0, len(creates))
	for _, w := range creates {
		node := map[string]any{
			"type":        styleRuleNodeType,
			"name":        w.entry.Name,
			"summary":     w.entry.Summary,
			"description": w.entry.Text,
			"metadata":    w.metadata,
		}
		if w.entry.ID != "" {
			node["id"] = w.entry.ID
		}
		nodes = append(nodes, node)
	}
	payload, err := json.Marshal(map[string]any{
		"operation":  "create_batch",
		"graph":      "practice",
		"source_hub": hub,
		"nodes":      nodes,
	})
	if err != nil {
		return fmt.Errorf("compose the create batch: %w", err)
	}
	if _, err := executeMutate(ctx, gc, payload); err != nil {
		return fmt.Errorf("write %d style rule(s): %w", len(creates), err)
	}
	return nil
}

// writeStyleRuleUpdates issues the ONE update_batch for rules that already exist.
//
// AN UPDATE, NEVER A SECOND CREATE, and the difference is data loss. A create
// stores the node body WHOLE and copies nothing from the existing row, so
// re-creating a rule whose file entry omits an optional key clears that key;
// applyNodeFields on the update path copies only the fields the item names and
// merges metadata PER KEY. That is what makes re-importing the same list
// idempotent instead of destructive.
func writeStyleRuleUpdates(ctx context.Context, gc GraphCaller, updates []styleRuleWrite) error {
	if len(updates) == 0 {
		return nil
	}
	items := make([]map[string]any, 0, len(updates))
	for _, w := range updates {
		items = append(items, map[string]any{
			"id":          w.entry.ID,
			"summary":     w.entry.Summary,
			"description": w.entry.Text,
			"metadata":    w.metadata,
		})
	}
	payload, err := json.Marshal(map[string]any{
		"operation": "update_batch",
		"graph":     "practice",
		"items":     items,
	})
	if err != nil {
		return fmt.Errorf("compose the update batch: %w", err)
	}
	if _, err := executeMutate(ctx, gc, payload); err != nil {
		return fmt.Errorf("update %d style rule(s): %w", len(updates), err)
	}
	return nil
}

// styleRuleImportReport renders what the import did, or would do.
//
// IT NAMES WHAT IT DID NOT WRITE. A reader who supplied check shapes has to be
// able to see that no check was created from them, rather than assume it either
// way — the shapes are data on the practice node until an LLM passes on the rule
// and authors the check in its own call.
func styleRuleImportReport(a manageArgs, creates, updates []styleRuleWrite, dryRun bool) kgtools.ToolResult {
	verb := "imported"
	if dryRun {
		verb = "would import"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "manage(%s): %s %d rule(s) under hub %s from %s\n",
		OpStyleRulesImport, verb, len(creates)+len(updates), a.Source, a.Path)
	fmt.Fprintf(&sb, "  created: %d\n  updated: %d\n", len(creates), len(updates))
	if shaped := styleRuleShapedCount(creates, updates); shaped > 0 {
		fmt.Fprintf(&sb,
			"  %d rule(s) carry a check shape, written as INERT metadata on the practice node. "+
				"No check and no fixture node was created, and no %s link was written: "+
				"a sister check is authored by its own manage_checks(create) call after an LLM pass on the rule.\n",
			shaped, kgtypes.MetaKeySisterCheck)
	}
	for _, name := range styleRuleNames(creates, updates) {
		fmt.Fprintf(&sb, "  - %s\n", name)
	}
	return textResult(sb.String())
}

// styleRuleShapedCount counts the rules carrying a check shape.
func styleRuleShapedCount(creates, updates []styleRuleWrite) int {
	n := 0
	for _, w := range append(append([]styleRuleWrite{}, creates...), updates...) {
		if w.entry.Check != nil {
			n++
		}
	}
	return n
}

// styleRuleNames returns the imported rule names in a stable order.
func styleRuleNames(creates, updates []styleRuleWrite) []string {
	names := make([]string, 0, len(creates)+len(updates))
	for _, w := range append(append([]styleRuleWrite{}, creates...), updates...) {
		names = append(names, w.entry.Name)
	}
	sort.Strings(names)
	return names
}
