// SPDX-License-Identifier: Apache-2.0

// Package tools — help topic for topology analyzers and the
// query(mode="topology") dispatch path. Split out of tools_help_content.go
// to keep that file under the 500-line hard cap.
package tools

const helpTopology = `# Topology Analyzers

Topology analyzers compute structural signals — centrality, cycles, bridges,
layering violations, hidden coupling, dead code — over a named
graph (code, knowledge, or a registered custom type) and emit ` + "`foundation.Finding`" + `
values. Pure functions over a ` + "`foundation.Request`" + `; no domain imports.

Analyzers are invoked on demand (graph + algorithm both required) via` + "`query(mode=\"topology\")`" + `.

## Invoke an analyzer

  query({
    "mode": "topology",
    "algorithm": "pagerank_weighted",
    "graph": "code",                  // required: code | knowledge | a registered custom type
    "repo": "knowledge",              // required for the code graph — never inferred
    "top_k": 10,
    "extra": { "damping": "0.85" }    // per-analyzer knobs (string→string)
  })

Output is a JSON array of ` + "`foundation.Finding`" + ` objects.

## Discover registered analyzers

The registry is built at init time from each analyzer file's ` + "`Register(...)`" + `
call. Both 'graph' and 'algorithm' are REQUIRED; omit the algorithm to get
the registered list in the error:

  query({ "mode": "topology", "graph": "code" })
  // → error: query(mode:"topology") requires "algorithm".
  //          Available analyzers: articulation, betweenness, ...

The same listing appears in the error returned for any unknown algorithm name.

## Notable analyzers

  pagerank / pagerank_weighted   — architectural importance. Weighted
                                   variant uses per-edge call-site counts
                                   (Go/TS only) to surface "hot helpers".
                                   Incremental Dynamic Frontier path on
                                   warm runs.

  betweenness                    — bridge nodes / single points of failure.
                                   Auto-dispatches: exact → sampled Brandes
                                   BFS → per-package for large graphs.

  dsm                            — Dependency Structure Matrix. Emits
                                   findings for upward IMPORTS edges
                                   (layering violations) and package cycles.
                                   Layers come from .knowledge/topology_layers.yaml
                                   or path heuristics.

  god_object / fan_in / fan_out  — degree-based outliers.

  cycles / scc                   — directed cycle detection.

  dead_code                      — unreachable nodes.

The cloud and exposure analyzers (public-exposure, security-group and
IAM-escalation reachability, unreferenced resources, certificate expiry,
monitoring coverage, serverless depth, cross-provider blast and event chains) were removed
with the built-in cloud collectors. They are being rebuilt on the contrib
collectors, against the graph types those collectors register.

Each analyzer supports ` + "`extra`" + ` for tuning knobs; see the file in
` + "`topology/`" + ` for the specific parameters it consumes.

## Per-analyzer parameters

  graph         required  code | knowledge | a registered custom type
  algorithm     required  registered analyzer name
  repo          code only — REQUIRED, never inferred from cwd. It is the
                per-graph instance key the analyzer receives; topology runs over
                a NAMED code graph
  language      passed through to the analyzer's Request
  top_k         caps ranked findings
  path_prefix   honored ONLY by the corpus-scan analyzer. Supplying it for any
                other algorithm is REFUSED naming the algorithm and the analyzers
                that do honor it — it is never accepted and silently ignored.
                For corpus_scan, a prefix that reached NO FILE of the corpus
                language is REFUSED naming the prefix rather than returned as an
                empty findings slice: prefixes match whole path SEGMENTS, so
                "pkg" is the pkg directory and never pkgextra, and a scan that
                opened no file is not a clean scan
  extra         map<string,string> — per-analyzer knobs

## Adding a new analyzer

  1. Create topology/your_analyzer.go implementing foundation.Analyzer
     (Name() string, Run(ctx, req foundation.Request) ([]foundation.Finding, error)).
  2. Self-register in init(): foundation.Register(YourAnalyzer{}).
  3. Add tests using the existing fixture helpers.
  4. It is then dispatchable via query(mode:"topology", algorithm:"your_analyzer").

Constraints: topology/ must not import thought/ or tools/. Files under 300
lines (soft) / 500 lines (hard). Functions under 80
lines, complexity under 30.

## Gotchas

  - 'graph' and 'algorithm' are both required — no default sweep, no
    paramless dump, no linkage fallback
  - top_k caps post-rank; analyzers may compute more findings internally
  - 'extra' is string→string (not nested JSON) — analyzer-specific parsing
`
