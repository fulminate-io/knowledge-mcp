// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// intercept_query_webpdf_modules.go serves query(graph:web|pdf, mode:"modules")
// — the operator's inventory of the collected raw graphs.
//
// It lives beside intercept_query_webpdf.go rather than inside it because that
// file is already 271 lines against lefthook's 300-line warning and 500-line
// hard cap (lefthook.yml:104-125).
//
// WHY THE STAMPS COME OFF THE ROOT NODE AND NOT OFF THE CATALOG ROW. Before this
// arm the mode fell through to engineDispatch, which rendered the raw
// GRAPH_NAMES envelope: name, loaded, and sync_time. sync_time is written by a
// DIFFERENT STAMPER than the one that knows when a collect ran, on both
// backends — readSyncMeta (cmd/knowledge-server/internal/store/registry_lookup.go)
// sets SyncTime and CollectedTime for the CODE family only, so a raw graph's are
// zero locally, and the cloud lifecycle path sets SyncTime from the registry row
// with its own comment calling that meaning distinct from the collect-time one.
// Rendering it as a collect time would tell an operator a confident falsehood
// about how stale a graph is. The listing therefore reads each graph's own
// collected_at off its ROOT node, which is where the collector wrote it.

// rawGraphModulesHeader is the count line every raw listing opens with.
const rawGraphModulesHeader = "## Collected %s graphs (%d)\n\n"

// The two unstamped sentences. They are DIFFERENT sentences because they answer
// different questions: a missing schema version means the graph's SHAPE is
// unknown, while a missing collect stamp means its AGE is unknown. Collapsing
// them into one "unstamped" would tell a reader which key is absent but not
// what they have lost by its absence.
const (
	rawGraphUnstampedVersion = "unstamped (collected before versioning; nothing can be concluded about its shape)"
	rawGraphUnstampedCollect = "unstamped (collected before collect stamping; its age cannot be told from the graph)"
)

// webPDFModules serves query(graph:web|pdf, mode:"modules").
//
// COST SHAPE: one catalog read, plus at most ONE Limit-1 root read per LISTED
// GRAPH. Bounded by the number of collected graphs, never by their size. The
// code sibling at intercept_query_modules_codestats.go DRAINS its graph in
// keyset pages (listModulesForRepo) because a code listing rolls up packages
// and files; copying that here would make an inventory of N documents cost a
// full scan of every one of them, for two metadata values that live on a single
// root node.
//
// A CATALOG FAILURE IS AN ERROR AND AN EMPTY CATALOG IS A SUCCESS. The two are
// never collapsed: the cleanup flow reads an empty listing as evidence that a
// sweep worked, so an implementation that rendered a failed catalog read as
// "no graphs collected" would turn a broken read into a false all-clear.
func webPDFModules(ctx context.Context, deps ClientDeps, a queryArgs, raw json.RawMessage) kgtools.ToolResult {
	if err := accountQueryParams(armWebPDFModules, raw); err != nil {
		return errorResult(err.Error())
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult(a.Graph + " modules: graph client unavailable")
	}
	names, err := listGraphNamesOfType(ctx, deps, a.Graph)
	if err != nil {
		return errorResult(a.Graph + " modules: " + err.Error())
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, rawGraphModulesHeader, a.Graph, len(names))
	if len(names) == 0 {
		fmt.Fprintf(&sb, "No %s graphs are collected.\n", a.Graph)
		return textResult(sb.String())
	}
	rootType := rawGraphRootType(a.Graph)
	for _, name := range names {
		stamps := rawGraphRootStamps(ctx, gc.Execute,
			&knowledgev1.GraphSelector{Graph: a.Graph, Name: name}, rootType)
		fmt.Fprintf(&sb, "- **%s**\n", name)
		fmt.Fprintf(&sb, "  - collected_at: %s\n", stamps.collectedAt)
		fmt.Fprintf(&sb, "  - collector_schema_version: %s\n", stamps.schemaVersion)
		fmt.Fprintf(&sb, "  - drop: manage(operation:\"drop_graph\", graph:%q, name:%q)\n\n", a.Graph, name)
	}
	sb.WriteString("_Raw graphs are temporary: once the golden content exists, drop the raw graph._\n")
	return textResult(sb.String())
}

// rawGraphRootType names the node type that carries a raw graph's root — and
// therefore its stamps. Web crawls emit one page root per crawled page; a pdf
// collect emits exactly one document root.
func rawGraphRootType(graph string) string {
	if graph == "pdf" {
		return "document"
	}
	return "page"
}

// rawGraphStamps is one graph's two lifecycle facts, each already rendered as
// the string the listing prints — an absent key resolves to its explicit
// unstamped sentence rather than to an empty string, so a caller cannot
// accidentally render absence as a blank.
type rawGraphStamps struct {
	collectedAt   string
	schemaVersion string
}

// rawGraphRootStamps reads BOTH lifecycle stamps off the graph's ROOT node —
// "page" for web, "document" for pdf — in EXACTLY ONE Execute.
//
// Deliberately not modeled on fetchGraphSamples, which issues one Execute PER
// NODE TYPE: that shape is right for sampling every type and wrong for reading
// two keys off one root. Both keys come from the same decoded node, so reading
// the second costs no second round trip.
//
// ABSENCE IS RENDERED, NEVER OMITTED. An older graph, or an empty one, gets the
// explicit unstamped value rather than a dropped line — the entire motivation
// for the stamps is that a reader of a pre-stamping graph currently cannot
// tell, and a silently missing line reproduces exactly that.
//
// A FAILED OR EMPTY READ IS REPORTED AS UNSTAMPED, which is the honest answer
// this signature can give: the values are absent from the caller's view either
// way, and the listing's failure-versus-empty split is decided one level up on
// the CATALOG read, which is the read that can distinguish them.
func rawGraphRootStamps(
	ctx context.Context,
	exec engine.ExecuteFn,
	sel *knowledgev1.GraphSelector,
	rootType string,
) rawGraphStamps {
	stamps := rawGraphStamps{
		collectedAt:   rawGraphUnstampedCollect,
		schemaVersion: rawGraphUnstampedVersion,
	}
	root, err := rawGraphRootNode(ctx, exec, sel, rootType)
	if err != nil || root == nil {
		return stamps
	}
	if v := kgtypes.Value(root, "collected_at"); v != "" {
		stamps.collectedAt = v
	}
	if v := kgtypes.Value(root, "collector_schema_version"); v != "" {
		stamps.schemaVersion = v
	}
	return stamps
}

// rawGraphRootNode reads a raw graph's ROOT node — "page" for web, "document"
// for pdf — in EXACTLY ONE Execute: a Limit-1, SkipTotal selection on the root
// type. It is the single read shape every consumer of a raw graph's root goes
// through, so a second caller cannot introduce a drain.
//
// IT SEPARATES ABSENCE FROM FAILURE, and that separation is the reason it is a
// function of its own. A nil node with a NIL error is the honest answer for a
// graph that holds no root: the read happened and found nothing. A nil node with
// a NON-NIL error says the read did not happen at all. Only the caller knows
// which of the two its own contract treats as fatal — the modules listing renders
// absence as an explicit unstamped line, while the collision refusal CANNOT,
// because reading a failed read as "no recorded source" would admit a collect
// nobody verified.
//
// PERF SHAPE, load-bearing: one Execute, Limit 1, SkipTotal. A drain here would
// make an inventory of N documents cost a full scan of each.
func rawGraphRootNode(
	ctx context.Context,
	exec engine.ExecuteFn,
	sel *knowledgev1.GraphSelector,
	rootType string,
) (*knowledgev1.Node, error) {
	resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			Selection: &knowledgev1.Selection{NodeType: rootType},
			Limit:     1,
			SkipTotal: true,
		}},
		Target: sel,
	})
	if err != nil {
		return nil, err
	}
	nodes, derr := engine.DecodeNodes(resp)
	if derr != nil {
		return nil, derr
	}
	if len(nodes) == 0 {
		return nil, nil
	}
	return nodes[0], nil
}
