// SPDX-License-Identifier: Apache-2.0

package foundation

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// GraphCaller is the narrow wire seam every analyzer reads through: a single
// Execute that carries a declarative ExecuteRequest to the graph server and
// returns the typed ExecuteResponse. foundation declares the interface locally
// (rather than importing cmd/knowledge/internal/tools) to avoid an import
// cycle — the dead_code client orchestrator declares the identical local
// interface (dead_code_client.go graphCaller) for the same reason. Any client
// graph handle whose Execute satisfies this signature can drive the suite.
type GraphCaller interface {
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// Request describes the inputs an Analyzer needs to run. Every analyzer reads
// its nodes and edges over the wire through Caller; centrality-family analyzers
// materialize their graph via NewGonumGraph(ctx, req.Caller, …) and the
// extra-access analyzers additionally call the shared wire read-helpers. There
// is exactly one Request shape across the whole suite — no analyzer receives a
// pre-fetched graph or a storage handle.
type Request struct {
	// Caller is the wire graph-client the analyzer reads through. Every read
	// (node browse, bulk edges, by-id lookup, list-graphs, knowledge findings)
	// is one Execute via this seam; analyzers never touch an in-process store.
	Caller GraphCaller
	// Graph identifies which graph to analyze, using the wire-side graph-type
	// vocabulary (knowledge / code / cloud / linkage / …).
	Graph kgtypes.GraphType
	// Name disambiguates per-graph instances: repo name for code graphs,
	// account key for cloud graphs, linkage instance name, etc. May be empty
	// for graphs that have only one instance (e.g. knowledge).
	Name string
	// RepoRoot is the local working-directory root of the code repo, set by
	// the client dispatcher from deps.RootDir(); analyzers that read repo files
	// (dsm reads go.mod + .knowledge/topology_layers.yaml) use it, others
	// ignore it.
	RepoRoot string
	// TopK caps how many findings the analyzer should return (0 = no cap).
	// Analyzers that produce ranked output (PageRank, centrality) honor this;
	// structural analyzers (SCC, orphan) may ignore it.
	TopK int
	// Language is the language code (not the full name, e.g. "go", "python",
	// "typescript") used by code-graph analyzers that need to scope their
	// analysis to a single language. Empty string means "no language filter —
	// every language in the graph participates".
	Language string
	// Subset optionally filters which nodes participate in the analysis. nil
	// means "no filter" — every node in the graph is considered. The predicate
	// runs over the wire node type, so analyzers build their filters as
	// field/metadata predicates over *knowledgev1.Node.
	Subset func(*knowledgev1.Node) bool
	// Extra carries per-analyzer configuration the core Request struct does not
	// need to know about (damping factors, convergence tolerances,
	// algorithm-specific tuning knobs). Analyzers look up their own keys via
	// extraFloat / extraString / extraInt. nil means "no overrides — every
	// analyzer uses its defaults".
	Extra map[string]string
}

// Analyzer is the unit of work in the topology suite. Implementations inspect a
// graph via Request.Caller and return zero or more Findings. Analyzers should
// be deterministic given the same graph state and Request, and must not mutate
// the graph.
type Analyzer interface {
	// Name returns the analyzer's stable identifier (e.g. "pagerank"). This is
	// the value Findings will carry in their Algorithm field.
	Name() string
	// Run executes the analyzer against the given Request. Implementations must
	// respect ctx cancellation and must not return a nil error alongside a
	// non-empty findings slice on failure.
	Run(ctx context.Context, req Request) ([]Finding, error)
}
