# analyze_usage

## Overview

`analyze_usage` analyzes your own AI coding-assistant usage transcripts for
agent-flow optimization — where and why your agents spend the most time and
tokens — entirely on your machine. No transcript data leaves your device: the
analysis runs over a local parquet cache the transcript upload writes, queried by
an embedded DuckDB engine.

It is op-dispatched: one tool with an `operation` enum.

- `run-detectors` returns the deterministic agent-flow metrics: redundantly-rerun
  commands (with wasted time), per-tool latency percentiles and total wall-time,
  per-session and per-subagent token hotspots, prompt-cache efficiency, subagent
  wall-time, agent-chain over-orchestration, and waste (API errors, interrupts,
  max-token truncations). Zero inference.
- `recommend` runs the detectors AND synthesizes ranked, actionable
  recommendations from the report using your locally-configured LLM. It degrades
  to detector-only output when no LLM is configured.

## When & how to use

Reach for `analyze_usage` to find optimization opportunities in your agent flows —
slow tools, redundant commands, token-heavy subagent types, cache-inefficient
sessions, or over-orchestration.

```jsonc
// Deterministic metrics only
analyze_usage({ "operation": "run-detectors" })

// Metrics + ranked, LLM-synthesized recommendations
analyze_usage({ "operation": "recommend" })
```

Cold-cache note: the local cache is populated on transcript upload, and unchanged
sessions are skipped. On an established install, run the transcript upload once
with `--seed` to backfill the cache before analyzing.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `agent` | string |  |  | Agent id selecting one subagent lane, for scope single. |
| `format` | string |  |  | Output format: 'json' (default). |
| `operation` | string | yes | run-detectors, recommend | Operation to perform: run-detectors (deterministic metrics only) or recommend (detectors + LLM-synthesized recommendations) |
| `scope` | string |  | all, session-tree, single, time-range | Population to analyze. all: the whole retained cache (default). session-tree: one main session plus every subagent lane it spawned — requires session. single: one lane on its own, which additionally returns a lane_detail breakdown — requires exactly one of session or agent. time-range: records bounded by since/until — requires at least one of them. |
| `session` | string |  |  | Session id selecting the population, for scope session-tree or single. |
| `since` | string |  |  | RFC3339 timestamp; records at or after it are included (inclusive). For scope time-range. |
| `until` | string |  |  | RFC3339 timestamp; records before it are included (exclusive). For scope time-range. |
<!-- END GENERATED: params -->
