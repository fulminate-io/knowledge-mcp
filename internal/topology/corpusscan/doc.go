// SPDX-License-Identifier: Apache-2.0

// Package corpusscan is the executor for the corpus: the corpus_scan topology
// analyzer, which reads fixture-validated check nodes out of a CHECKS graph
// (the single checks graph, narrowed to one language by a metadata predicate,
// never practice/<language>) and runs them against a target
// code graph, emitting render-only findings.
//
// THE PACKAGE IS corpusscan, NOT corpus. The check CONTRACT is a separate pure
// package at cmd/knowledge/internal/corpus (package corpus) that this one
// imports for ParseCheck, ValidateFixtures and the three error sentinels. Two
// packages named corpus would force an import alias at every call site.
//
// The four things below are recorded here because a later reader cannot
// re-derive them cheaply, and because getting any of them wrong produces a scan
// that looks clean for the wrong reason.
//
// # 1. The tool-call budget's owner is located; its value is not
//
// The budget that bounds a tool call is owned by the MCP host, not by this
// binary, and no number anywhere in this repo names its value. The census
// behind that: no context.WithTimeout, context.WithDeadline, WriteTimeout or
// http.TimeoutHandler exists on the tool-call dispatch path in
// cmd/knowledge/internal/{graphclient,tools,bootstrap} or in
// cmd/knowledge-server. graphclient/mcp_http.go:162-171 sets only
// ReadHeaderTimeout, and deliberately no ReadTimeout. bootstrap/dream.go:55-58
// documents its ctx as the caller's request context. So the budget arrives as a
// ctx cancellation from outside this binary.
//
// STATE BOTH HALVES rather than one: the owner is located, the VALUE is not.
// The consequence is the design rule this package follows — corpus_scan bounds
// ITSELF and discloses every truncation, and is never sized against a ceiling
// nobody has measured. Do not write "fits in 180s" anywhere in this family.
//
// # 2. The measured per-check cost, which has two terms
//
// SCAN: one ast walk over 2,306 Go files measures 167ms engine-reported. The
// check-contract reviewer independently measured 155ms over the same file
// count, i.e. 0.067ms per file.
//
// RE-VALIDATION: the check contract deliberately persists NO fixture-validation
// marker, so every check is re-validated on every run, and each validation is a
// real ast walk over a materialized temp file. One single-file walk measures
// 16ms, so two fixtures cost roughly 32-46ms per check. A check carrying a
// where-tree pays one more single-file walk for the contract's calibration
// probe, so those land nearer 48-69ms.
//
// TOTAL: about 199-213ms per check; a 100-check corpus costs roughly 20-21
// seconds, of which 3-5 seconds is re-validation. BOTH TERMS ARE WRITTEN DOWN
// deliberately — an earlier draft called validation negligible, and recording
// only the scan term is what made that error possible.
//
// WHERE THE FIXED PER-CALL COST GOES: discovery plus process variance, NOT
// pattern compile. That was measured by the planning lane and is recorded in
// the knowledge graph under the corpus re-validation cost finding; the
// consequence for this package is one sentence — the repo walk already performs
// one pattern compile per worker by design, so no pre-compiled-pattern
// parameter anywhere could reduce it. Do not re-propose that optimization.
//
// HEADROOM, stated without a ceiling because none is known. The 56-node figure
// this paragraph used to cite was the practice/go FINDING population, measured
// before checks moved to their own graph; it no longer describes this reader's
// input, because practice/go now holds prose and models that this analyzer never
// reads. The bound it produced survives as a conservative over-estimate: a
// checks graph holds only checks, so its population is at most that, and at
// 199-213ms per check a whole-corpus scan costs at most about 12 seconds with no
// collision under any ceiling above roughly 15 seconds. Re-measure against a
// populated checks graph rather than trusting the pre-split number.
//
// PERF DECISION AND ITS REASON: per-check walks run SERIALLY, because the
// parallelism already lives inside each walk and N concurrent walks contend for
// the same worker pool. The scaling shape, stated honestly: N checks means N
// traversals of the same tree, and the ast engine's patterns[] alternation does
// not fix it — its own schema states that each pattern triggers an independent
// repo walk.
//
// # 3. What the render ceilings are derived from
//
// foundation.RenderFindings (foundation/render.go:13-19) is a plain
// json.MarshalIndent with no cap, and the MCP tool-result surface observed
// during planning refused a 51,918-character payload. A god_object finding with
// 6 evidence entries and 9 metrics renders at roughly 870 characters; a
// corpus_scan finding carries 1-3 evidence entries and a handful of metadata
// keys, so it sits nearer 350. That is the derivation behind
// MaxFindingsPerCheck and MaxFindingsTotal in vocabulary.go.
//
// # 4. The per-language fixture-fidelity limit for graph checks
//
// This limit is FAMILY-SCOPED — it constrains which languages admit which check
// types — so it lives here rather than only in the executor that consumes it.
//
// Go code graphs carry rich CALLS and CONTAINS edges, while Python code graphs
// carry zero CALLS edges and near-zero CONTAINS edges until the collector arms
// it, so a graph_assertion fixture is high-fidelity for Go and degraded for
// Python. That constrains which languages admit graph checks today, and it is
// not a reason the mechanism cannot exist.
package corpusscan
