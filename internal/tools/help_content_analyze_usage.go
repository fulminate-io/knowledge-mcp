// SPDX-License-Identifier: Apache-2.0

// help_content_analyze_usage.go — the help("analyze_usage") topic.
//
// Content lives in its own file per topic (help_content_ast.go, help_content_topology.go,
// help_content_manage_checks.go are the same shape) rather than appended to the shared
// content files, which are already long.

package tools

// helpAnalyzeUsage documents the analyze_usage tool, and in particular the selector: the two
// spellings a lane answers to, what a name is resolved against, and how the three answers an
// empty result can carry are told apart. That last part is why this topic exists — the
// difference between "your selector matched nothing" and "your cache is empty" is invisible
// from the schema line and used to be invisible from the response too.
const helpAnalyzeUsage = `# analyze_usage — Analyze your own coding-assistant transcripts

Runs entirely on this machine over the local parquet transcript cache. No transcript
data leaves the device.

## Operations
  run-detectors — deterministic metrics only (no inference)
  recommend     — the detectors plus LLM-synthesized recommendations when a local LLM
                  is configured; degrades to detector-only output, always naming why

## Scopes
  all          — the whole retained cache (default)
  session-tree — one main session plus every subagent lane it spawned; needs session
  single       — one lane on its own, plus a lane_detail breakdown; needs session or agent
  time-range   — records bounded by since/until; needs at least one bound

## Selecting one lane: agent takes a name OR an id

  agent accepts either spelling of a subagent lane:

    the NAME — the name the lane was spawned under, e.g. "planner"
    the ID   — the cache's own lane id, a<name>-<16 hex>, e.g. "aplanner-0123456789abcdef"

  A name is RESOLVED against the cache before the report runs, so both calls return the
  same report and the same corpus selector (the id).

  A name is resolved within session when session is given, and across the whole cache
  otherwise. session and agent may therefore be given TOGETHER for a name — that is the
  resolution scope — while a lane id is unique and refuses the pair.

  A name that matches more than one lane is ambiguous and is REFUSED, listing each
  candidate id with its session. A lane name is reused across sessions; the id is not.
  Re-run with one of the listed ids, or narrow with session.

  The spelling a spawn result reports (name@session-<hex>) is not a cache id and does not
  resolve; use the name alone or the cache id.

## Three answers to an empty result, and how to tell them apart

  a report          — the selection matched records
  a REFUSAL         — the cache holds lanes and your selection matched none of them; it
                      names the scope, what it searched, and the corpus block
                      (lane_count, record_count)
  the COLD-CACHE hint — the cache holds no lanes at all: run the transcript upload once
                      with --seed to backfill it, then re-run analyze_usage

  A wrong selector, an empty time window and an unpopulated cache are three distinct
  responses. Only the last one mentions --seed.

## Corpus
  The cache RETAINS a session after the CLI has removed that session's own transcript
  file, so its counts legitimately exceed what is on disk. Every report states the basis
  it was computed over in its corpus block.

## Examples
  analyze_usage({ "operation": "run-detectors", "scope": "single", "agent": "planner" })
  analyze_usage({ "operation": "run-detectors", "scope": "single", "agent": "planner", "session": "<session id>" })
  analyze_usage({ "operation": "recommend", "scope": "session-tree", "session": "<session id>" })
`
