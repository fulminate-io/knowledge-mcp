// SPDX-License-Identifier: Apache-2.0

// Package tools — help topic for topology analyzers and the
// query(mode="topology") dispatch path. Split out of tools_help_content.go
// to keep that file under the 500-line hard cap.
package tools

const helpTopology = `# Topology Analyzers

Topology analyzers compute structural signals — centrality, cycles, bridges,
layering violations, hidden coupling, dead code, exposure paths — over a named
graph (code, cloud, cicd, knowledge) and emit ` + "`foundation.Finding`" + `
values. Pure functions over a ` + "`foundation.Request`" + `; no domain imports.

Analyzers are invoked on demand (graph + algorithm both required) via` + "`query(mode=\"topology\")`" + `.

## Invoke an analyzer

  query({
    "mode": "topology",
    "algorithm": "pagerank_weighted",
    "graph": "code",                  // required: code | cloud | cicd | knowledge
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

  dead_code / orphan             — unreachable nodes.

  aws_public_exposure /          — cross-graph reachability into the
  k8s_public_exposure /            public internet.
  unified_public_exposure
  aws_sg_reachability /
  k8s_reachability
  iam_escalation                 — IAM policy escalation chains.

Each analyzer supports ` + "`extra`" + ` for tuning knobs; see the file in
` + "`topology/`" + ` for the specific parameters it consumes.

## Per-analyzer parameters

  graph         required  code | cloud | cicd | knowledge
  algorithm     required  registered analyzer name
  repo          code only — REQUIRED, never inferred from cwd. It is the
                per-graph instance key the analyzer receives; topology runs over
                a NAMED code graph
  account       cloud / cicd only (account or provider-org name) — the same
                instance key for those families
  language      passed through to the analyzer's Request
  top_k         caps ranked findings
  path_prefix   honored ONLY by the corpus-scan analyzer. Supplying it for any
                other algorithm is REFUSED naming the algorithm and the analyzers
                that do honor it — it is never accepted and silently ignored
  resource_type cloud only — restrict to nodes whose 'resource_type' meta starts with prefix
  extra         map<string,string> — per-analyzer knobs

## Adding a new analyzer

  1. Create topology/your_analyzer.go implementing foundation.Analyzer
     (Name() string, Run(ctx, req foundation.Request) ([]foundation.Finding, error)).
  2. Self-register in init(): foundation.Register(YourAnalyzer{}).
  3. Add tests using the existing fixture helpers.
  4. It is then dispatchable via query(mode:"topology", algorithm:"your_analyzer").

Constraints: topology/ must not import cloud/, codegraph/, thought/, or
tools/. Files under 300 lines (soft) / 500 lines (hard). Functions under 80
lines, complexity under 30.

## Gotchas

  - 'graph' and 'algorithm' are both required — no default sweep, no
    paramless dump, no linkage fallback
  - cloud and cicd graphs require 'account' (or provider-org name)
  - top_k caps post-rank; analyzers may compute more findings internally
  - 'extra' is string→string (not nested JSON) — analyzer-specific parsing
`
