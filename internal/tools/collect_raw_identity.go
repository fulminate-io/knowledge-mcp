// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/web"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collect_raw_identity.go holds the identity half of a RAW collect: filling in
// the id a web collect did not supply, and the pre-walk check that refuses a
// collect whose derived name already belongs to a different source.
//
// It is a separate file from collect.go deliberately. collect.go sits close to
// lefthook's 500-line hard error and its 300-line warning, and the precheck body
// is large enough that inlining it would push that file past the cap for no
// structural gain.

// applyDerivedCollectID fills in a.ID for a web collect that supplied none,
// deriving it through the one production derivation. It is a NO-OP for every
// other collector type and for a web collect that named itself.
//
// IT WRITES a.ID RATHER THAN THREADING A SECOND VALUE because a.ID is what the
// rest of the dispatch already reads: CrawlOptions.Source, the single-flight
// target key, the run label, the success text, the gate identity and the
// collision precheck all take their answer from it, and one assignment makes
// all of them agree. Threading a parallel "derived name" beside an id that
// still reads empty would give the collect two identities and let any consumer
// that read the wrong one name a different graph. runRecipeCollect already uses
// exactly this idiom to resolve a replay id.
//
// a is a POINTER here, unlike runRecipeCollect's value copy, because this runs
// on the dispatch's own args before the guard rather than inside a branch that
// owns a copy.
func applyDerivedCollectID(a *collectArgs) error {
	if a == nil || a.Type != "web" || a.ID != "" {
		return nil
	}
	name, err := CollectGateGraphName(a.Type, a.ID, a.SeedURLs)
	if err != nil {
		return err
	}
	a.ID = name
	return nil
}

// resolveCollectID settles a collect's id: it fills in the one a web collect did
// not supply, and then refuses a collect that still has none. It reports
// (result, true) when the dispatch must return that result, and (zero, false)
// when the collect may proceed.
//
// THE ORDER IS THE CONTRACT and is why the two steps share one function. The
// derivation runs STRICTLY BEFORE the id-is-required refusal, so a web collect
// carrying seed URLs and no id is named rather than refused, while every other
// collect meets exactly the guard it met before.
func resolveCollectID(a *collectArgs) (kgtools.ToolResult, bool) {
	if err := applyDerivedCollectID(a); err != nil {
		return errorResult(err.Error()), true
	}
	if a.ID == "" {
		// Prefix with "collect <type>:" so the error shape matches the other
		// collect-time errors (e.g. "collect logs: provider is required")
		// instead of the bare "'id' is required" that gave no clue which tool
		// surfaced it.
		return errorResult(fmt.Sprintf("collect %s: 'id' is required", a.Type)), true
	}
	return kgtools.ToolResult{}, false
}

// rawSourceMetaKey names the metadata key a raw family's ROOT node records its
// source under, and the empty string for every family that records none.
//
// WHO STAMPS EACH KEY: pdf's `path` is written by emitDocumentNode
// (collector/pdf/pdfcollector/emit.go), and web's `seed_host` is written by
// emitPageNode (collector/web/emit_nodes.go). Both are the collector's own
// write, which is what makes them a record of where the graph CAME FROM rather
// than a restatement of what this collect wants.
func rawSourceMetaKey(collectorType string) string {
	switch kgtypes.GraphType(collectorType) {
	case kgtypes.GraphPDFRaw:
		return "path"
	case kgtypes.GraphWebRaw:
		return "seed_host"
	default:
		return ""
	}
}

// incomingRawSource computes the source THIS collect is coming from, in the
// same spelling the collector will record.
//
// TWO STAMPERS, ONE SPELLING. The recorded side is written by the collectors;
// this side is computed here — so the two only agree because they are computed
// the same way. web goes through the very same web.SeedHost the emitter's value
// came from, and pdf is filepath.Clean of the same id the pdf collector opens.
// The Clean is load-bearing: without it a path spelled through a
// parent-directory hop would read as a different document and refuse a
// legitimate re-collect.
func incomingRawSource(collectorType, id string, seedURLs []string) (string, error) {
	switch kgtypes.GraphType(collectorType) {
	case kgtypes.GraphPDFRaw:
		return filepath.Clean(id), nil
	case kgtypes.GraphWebRaw:
		return web.SeedHost(seedURLs)
	default:
		return "", nil
	}
}

// rawGraphRecordedSource reads the source a raw graph RECORDS, off its root node.
//
// A nil GraphCaller is an ERROR rather than an empty answer: a client that
// cannot read the target cannot verify it, and reporting "nothing recorded"
// there would be a fail-open dressed as a fact.
//
// A graph holding NO ROOT returns an empty value and NO error. That is the
// honest reading of an empty graph: the read happened and found nothing to
// compare against.
func rawGraphRecordedSource(
	ctx context.Context,
	deps ClientDeps,
	collectorType, graphName, key string,
) (string, error) {
	gc := deps.GraphCaller()
	if gc == nil {
		return "", fmt.Errorf(
			"collect %s: cannot verify the target graph %q: graph client unavailable",
			collectorType, graphName)
	}
	root, err := rawGraphRootNode(ctx, gc.Execute,
		&knowledgev1.GraphSelector{Graph: collectorType, Name: graphName},
		rawGraphRootType(collectorType))
	if err != nil {
		return "", fmt.Errorf(
			"collect %s: cannot verify the target graph %q: %w", collectorType, graphName, err)
	}
	if root == nil {
		return "", nil
	}
	return kgtypes.Value(root, key), nil
}

// legacySuffixedNames finds the graphs an EARLIER naming rule left behind for
// this same document: the graph name, a dash, and exactly eight lowercase hex
// characters — the shape pdfcollector.SourceSlug used to append.
//
// PDF ONLY, and the exclusion of web is by construction rather than by
// oversight: a web graph has ALWAYS been named by its source string, so a web
// name that merely ends in eight hex characters is somebody's chosen name and
// calling it legacy would tell an operator to drop a graph nothing replaced.
func legacySuffixedNames(collectorType, graphName string, names []string) []string {
	if kgtypes.GraphType(collectorType) != kgtypes.GraphPDFRaw || graphName == "" {
		return nil
	}
	var out []string
	for _, n := range names {
		suffix, found := strings.CutPrefix(n, graphName+"-")
		if !found || len(suffix) != 8 {
			continue
		}
		hex := true
		for _, r := range suffix {
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
				hex = false
				break
			}
		}
		if hex {
			out = append(out, n)
		}
	}
	return out
}

// precheckRawCollect runs BEFORE THE WALK and decides whether this collect may
// land on the graph its name resolves to. It returns the older suffixed graphs
// found alongside it, and an error when the collect must be refused.
//
// THREE RULES, each written down because each is a decision a reader will
// otherwise re-litigate:
//
//  1. AN UNRECORDED SOURCE IS NOT A DIFFERENT SOURCE. A graph collected before
//     its family recorded one carries no key at all. Absence means nothing is
//     known, never that it differs — refusing there would make every legacy
//     graph permanently un-re-collectable. The collect proceeds and stamps its
//     own source on the way through.
//  2. A READ FAILURE IS AN ERROR, NEVER AN ALL-CLEAR. The catalog read separates
//     absence (no such graph) from failure (the read did not happen), and only
//     absence admits the collect. A failed read rendered as "nothing there"
//     would admit a collect nobody verified.
//  3. THE REFUSAL NEVER MERGES AND NEVER MINTS A SUFFIX to dodge the collision.
//     A minted suffix would restore exactly the unreadable names this change
//     removes, and a merge would put two documents in one graph with no way to
//     tell their nodes apart.
//
// COST SHAPE, chosen and gated: ONE catalog enumeration per raw collect, plus at
// most ONE Limit-1 root read when the name is already taken. Never a drain. The
// catalog read is the same graph-names enumeration the modules listing uses,
// whose server side lists the type's directory without loading a graph.
func precheckRawCollect(
	ctx context.Context,
	deps ClientDeps,
	collectorType, graphName, incomingSource string,
) ([]string, error) {
	key := rawSourceMetaKey(collectorType)
	if key == "" || graphName == "" {
		return nil, nil
	}
	names, err := listGraphNamesOfType(ctx, deps, collectorType)
	if err != nil {
		return nil, fmt.Errorf(
			"collect %s: cannot verify the target graph %q: %w", collectorType, graphName, err)
	}
	legacy := legacySuffixedNames(collectorType, graphName, names)

	occupied := slices.Contains(names, graphName)
	if !occupied {
		return legacy, nil
	}
	recorded, err := rawGraphRecordedSource(ctx, deps, collectorType, graphName, key)
	if err != nil {
		return nil, err
	}
	if recorded == "" || recorded == incomingSource {
		return legacy, nil
	}
	return nil, fmt.Errorf(
		"collect %s: REFUSED — the graph %q was collected from a different source. "+
			"Its %s records %q; this collect comes from %q. "+
			"Two sources are never merged into one raw graph and no suffix is minted to keep them apart. "+
			"Collect the other source under an explicit name, or drop the existing graph first with:\n\n"+
			"manage({\"operation\":\"drop_graph\",\"graph\":%q,\"name\":%q})",
		collectorType, graphName, key, recorded, incomingSource, collectorType, graphName)
}

// prepareRawCollect is the dispatch's ONE call into this file: it derives the
// name the collect will land under, works out where the collect is coming from,
// runs the pre-walk collision check, and renders the legacy-graph notice the
// answer carries. It returns the derived graph name and that notice.
//
// Everything here happens BEFORE the walk, so a refusal costs no crawl and no
// parse.
func prepareRawCollect(ctx context.Context, deps ClientDeps, a collectArgs) (string, string, error) {
	graphName, err := CollectGateGraphName(a.Type, a.ID, a.SeedURLs)
	if err != nil {
		return "", "", err
	}
	incoming, err := incomingRawSource(a.Type, a.ID, a.SeedURLs)
	if err != nil {
		return "", "", err
	}
	legacy, err := precheckRawCollect(ctx, deps, a.Type, graphName, incoming)
	if err != nil {
		return "", "", err
	}
	return graphName, legacyRawGraphNotice(a.Type, legacy), nil
}

// legacyRawGraphNotice reports the older suffixed graphs this document used to
// land under, and the call that drops each. The empty string when there are none.
//
// NEVER AUTO-DROP, NEVER RENAME. The older graph is data somebody collected. The
// collect that just ran wrote a NEW graph under the new name and left the old one
// exactly where it was; the operator decides whether it is still wanted. Voice
// and call shape follow rawCollectDropDirective and the modules listing's drop
// line.
func legacyRawGraphNotice(collectorType string, legacy []string) string {
	if len(legacy) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "NOTE: %d older %s graph(s) of this document remain under the previous "+
		"hash-suffixed naming rule: %s. Nothing was renamed and nothing was dropped — this collect "+
		"wrote a new graph under the new name and left those alone. Drop each with:",
		len(legacy), collectorType, strings.Join(legacy, ", "))
	for _, n := range legacy {
		fmt.Fprintf(&sb, "\n\nmanage({\"operation\":\"drop_graph\",\"graph\":%q,\"name\":%q})", collectorType, n)
	}
	return sb.String()
}
