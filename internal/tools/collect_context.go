// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
)

// collect_context.go — FILLING THE COLLECT INPUT'S CONTEXT BLOCK from the
// registration's declaration, before the provider is called.
//
// THE CLIENT READS AND THE CLIENT PROJECTS. Every read rides the generic Execute
// carrier the compiled-in linker already reads code graphs through, scoped per
// call to the graph type being read. The carrier does not narrow to a declared
// slice and is not asked to: it returns whole node bodies, and "exactly the
// declared slice" is a projection on this side of the wire. That is what keeps
// this whole contract off the server, which holds no domain knowledge about what
// a collector needs.
//
// THE FAMILY VOCABULARY IS THE REGISTRY'S, NOT A LIST IN THIS FILE. A
// declaration may name `code` or any graph type registered on this daemon. That
// set is read here, at fill time, because it is the only place in the client
// that can read it: the contract package holds no client deps and the config
// loader runs with no server. A family that is neither is refused BY NAME with
// the set that would have been accepted.
//
// A DECLARATION THE CLIENT CANNOT SATISFY IS AN ERROR, NEVER A SMALLER SLICE. A
// module that declared a family or a field and received a slice without it would
// compute a wrong answer from a right-looking input, and would have no way to
// tell that from a graph that was genuinely empty. Every refusal here names the
// family and the value that is wrong with it.
//
// AN UNREADABLE REGISTRY IS AN ERROR AND NOT AN EMPTY SET. "This family is not
// registered" and "whether it is registered is unknown" are opposite answers to
// the caller, and collapsing them would refuse a correct declaration with a
// confidently wrong reason.
//
// NOTHING HERE IS BOUNDED BY SIZE. The declared slice is sent whole, however
// large. The declaration is the sizing control — a module receives what its
// entry named and nothing else — and a byte bound on this path would be a cap on
// traffic that never reaches a model context.

// fillCollectContext builds the collect input's context block for one
// registration, or returns nil when the entry declares nothing.
//
// A NIL RETURN AND AN EMPTY BLOCK ARE DIFFERENT ANSWERS and the caller must keep
// them apart: nil means the entry asked for nothing and the argument key is
// omitted entirely; an empty block means the entry asked and the store held
// nothing, which is an answer the module is entitled to see.
//
// THE SECOND RETURN IS THE FILL'S REPORT and it is produced HERE rather than
// derived from the block afterwards, because the fact it carries is not in the
// block: a graph holding zero nodes of the declared type and a graph holding
// none at all are byte-identical once the block is built, and separating them
// after the fact is impossible. It is nil exactly when the block is, so a
// caller threads one value and the render degrades to nothing.
func fillCollectContext(
	ctx context.Context, deps ClientDeps, decl externalcollector.ContextDeclaration,
) (*externalcollector.CollectContext, *contextFillReport, error) {
	if decl.IsEmpty() {
		return nil, nil, nil
	}
	if err := decl.Validate(); err != nil {
		return nil, nil, err
	}
	if err := validateContextFamilies(ctx, deps, decl); err != nil {
		return nil, nil, err
	}
	block := externalcollector.CollectContext{}
	report := &contextFillReport{}
	// SORTED KEYS, so one unchanged store produces one byte-identical block
	// across runs: a map's iteration order is randomized, and a block whose
	// family fill order varied call to call would vary the reads it issues for
	// no reason a module could see.
	for _, family := range slices.Sorted(maps.Keys(decl)) {
		graphs, fill, err := fillFamilyContext(ctx, deps, family, decl[family])
		if err != nil {
			return nil, nil, err
		}
		// ASSIGNED EVEN WHEN EMPTY. A declared family with no graphs is a present
		// key holding an empty slice, never a missing key: the module declared it
		// and is entitled to see that the answer was nothing.
		if graphs == nil {
			graphs = []externalcollector.GraphContext{}
		}
		block[family] = graphs
		report.Families = append(report.Families, contextFamilyFill{
			Family: family,
			Graphs: fill,
			// A SELECTOR-DECLARED FAMILY IS NOT NAMES-ONLY. It drains every node
			// type of the family, so it has a real count to report; reading its
			// empty NodeTypes list as "the graph names alone" would print the one
			// note that says "no node was asked for" over a family that asked for
			// all of them.
			NamesOnly: len(decl[family].NodeTypes) == 0 && !decl[family].AllNodeTypes,
		})
	}
	return &block, report, nil
}

// validateContextFamilies refuses a declared family this daemon cannot supply.
//
// A RETIRED BUILT-IN NEVER REACHES HERE, and this function deliberately does not
// re-check for one. `cloud` and `logs` were supplyable families one release ago,
// so an on-disk entry naming one is the entry most likely to exist right now,
// and reporting it as a name that was never registered would tell an operator
// upgrading a working entry that they had made a typo. That refusal is
// CollectContext.Validate's, which fillCollectContext runs immediately before
// this call — see externalcollector/context.go.
//
// IT USED TO BE CHECKED IN BOTH PLACES AND THAT WAS WORSE, not safer. Two copies
// of one decision cannot be observed apart: deleting either arm alone left the
// whole tools package green, because the other answered. One arm, reached
// through both entry points and red at both, is the shape that stays honest.
// TestFillCollectContext_ARetiredFamilyIsRefusedAsRetired drives THIS path and
// reds when the surviving arm goes; the externalcollector and collectorconfig
// suites red on the same removal from their own side.
//
// THE REGISTRY IS READ ONLY WHEN THE DECLARATION NEEDS IT. `code` is supplyable
// on every client unconditionally, so a code-only declaration is satisfied
// without a registry read — and, more to the point, without being REFUSED on a
// client whose registry is unreachable. Reading it anyway would make an
// unrelated outage break a declaration whose answer never depended on it.
func validateContextFamilies(
	ctx context.Context, deps ClientDeps, decl externalcollector.ContextDeclaration,
) error {
	var needRegistry bool
	for _, family := range slices.Sorted(maps.Keys(decl)) {
		if family != externalcollector.ContextFamilyCode {
			needRegistry = true
		}
	}
	if !needRegistry {
		return nil
	}
	registered, err := registeredGraphTypeNames(ctx, deps)
	if err != nil {
		return fmt.Errorf(
			"context declaration: the graph-type registry could not be read, so the declared families "+
				"cannot be verified: %w", err)
	}
	supplyable := append([]string{externalcollector.ContextFamilyCode}, registered...)
	sort.Strings(supplyable)
	return decl.ValidateFamilies(slices.Compact(supplyable))
}

// fillFamilyContext reads one declared family's graphs through the Execute
// carrier and projects each to the declared slice.
//
// THE GRAPH NAMES ARE ALWAYS READ AND THE NODES ONLY WHEN DECLARED. A family
// declaring no node types and no all_node_types is a real declaration rather
// than a degenerate one: a predicate that is a membership test over graph names
// needs exactly that, so the read it costs is one name lookup and no browse.
//
// ONE ARM SERVES EVERY FAMILY, including code. The read primitives are all
// graph-type-parameterized, so a per-family arm would be the same body written
// twice — and the second copy is where the two would drift. The code family's
// one difference, the branch-overlay names, is applied as a filter on the name
// list rather than as a separate path.
func fillFamilyContext(
	ctx context.Context, deps ClientDeps, family string, decl externalcollector.FamilyDeclaration,
) ([]externalcollector.GraphContext, []contextGraphFill, error) {
	caller := deps.GraphCaller()
	if caller == nil {
		return nil, nil, fmt.Errorf(
			"context declaration: family %q is declared and this client has no graph caller to read it through",
			family)
	}
	names, err := listGraphNamesOfType(ctx, deps, family)
	if err != nil {
		return nil, nil, fmt.Errorf("context declaration: reading the %q family: %w", family, err)
	}
	if family == externalcollector.ContextFamilyCode {
		names = withoutBranchOverlays(names)
	}
	// SORTED, so one unchanged store produces one byte-identical block across
	// runs. The enumeration's order is the backend's, which is not a promise.
	sort.Strings(names)
	out := make([]externalcollector.GraphContext, 0, len(names))
	fills := make([]contextGraphFill, 0, len(names))
	for _, name := range names {
		graph := externalcollector.GraphContext{GraphName: name}
		fill := contextGraphFill{Name: name}
		// THE TWO SELECTORS DRAIN DIFFERENTLY BECAUSE THEY ASK THE STORE DIFFERENT
		// QUESTIONS. A node-type list issues one type-keyed browse per declared
		// type; all_node_types issues ONE browse with no type key at all, because a
		// family that asked for every type has no list to iterate and a client-side
		// filter over a per-type read could not see a type nobody named.
		//
		// IT REPORTS ITS COUNT LIKE ANY OTHER ROW. A selector-declared family that
		// matched nothing is the same silence the fill report exists to break — an
		// empty graph and a graph whose every node was somehow dropped render
		// identically in the block — so the drain contributes one row under the
		// every-type label rather than none.
		if decl.AllNodeTypes {
			nodes, derr := contextDrainAllNodes(ctx, caller, family, name)
			if derr != nil {
				return nil, nil, fmt.Errorf("context declaration: reading %s/%s nodes of every type: %w",
					family, name, derr)
			}
			projected := projectNodes(nodes, decl)
			fill.Types = append(fill.Types, contextTypeFill{NodeType: everyNodeTypeLabel, Matched: len(projected)})
			graph.Nodes = projected
		}
		for _, nodeType := range decl.NodeTypes {
			nodes, derr := contextDrainNodes(ctx, caller, family, name, nodeType)
			if derr != nil {
				return nil, nil, fmt.Errorf("context declaration: reading %s/%s nodes of type %q: %w",
					family, name, nodeType, derr)
			}
			// THE COUNT IS OF WHAT WAS CARRIED, not of what was fetched, and it is
			// taken HERE because this is the only frame that still knows which
			// declared type produced which nodes. A drain error is propagated rather
			// than counted as a zero: "the read failed" and "the type matched
			// nothing" are opposite answers.
			projected := projectNodes(nodes, decl)
			fill.Types = append(fill.Types, contextTypeFill{NodeType: nodeType, Matched: len(projected)})
			graph.Nodes = append(graph.Nodes, projected...)
		}
		if len(decl.EdgeFields) > 0 {
			edges, eerr := contextGraphEdges(ctx, caller, family, name, graph.Nodes, decl)
			if eerr != nil {
				return nil, nil, fmt.Errorf("context declaration: reading %s/%s edges: %w", family, name, eerr)
			}
			graph.Edges = edges
		}
		out = append(out, graph)
		fills = append(fills, fill)
	}
	return out, fills, nil
}

// withoutBranchOverlays drops the branch-overlay graph names from a code-graph
// name list.
//
// AN OVERLAY NAME IS `base@branch`, and the compiled-in predicate a module
// computes against skips one: linker/dockerfile.go's repo walk reads base graphs
// only. A block carrying overlay names would hand a module repositories the
// builtin linker never reads, so a module taking the block at face value would
// derive a different answer from a same-looking input — and it would pay one
// node browse per overlay to get there.
//
// THE DROP IS THE FILL'S RATHER THAN THE MODULE'S for the reason declared-only
// exists: what a module receives should be answerable from the entry and the
// store, not from a convention each module has to know and reimplement.
func withoutBranchOverlays(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if strings.Contains(n, "@") {
			continue
		}
		out = append(out, n)
	}
	return out
}
