# Agents

## Overview

Agents are specialized assistants that drive a multi-step workflow against the
knowledge graph. Each one is tuned for a single job — researching, planning,
reviewing, implementing, testing — and they compose into a pipeline: the output
one agent leaves in the graph is the input the next one reads. Rather than doing
everything in a single sprawling conversation, you hand a well-scoped task to the
agent built for it.

The agents ship with the project and are available wherever it is installed. The
core flow is **researcher → planner → plan-reviewer → implementer** for building,
and **test-planner → tester** for verifying, with `explorer` and
`infra-reviewer` serving more specialized roles.

## When & how to use

Reach for the agent that matches the phase of work you are in. They hand off
through the graph: the researcher leaves findings the planner reads, the planner
leaves a plan the reviewer audits and the implementer executes, and so on.

| Agent | Purpose | When to use |
| --- | --- | --- |
| `researcher` | Investigates a topic with semantic search, code-graph traversal, and knowledge nodes — describes WHAT exists and HOW it works. | Before planning, to gather context and surface prior decisions. |
| `explorer` | Authors thought clusters that explain WHY a system exists and behaves as it does, weaving evidence across the code, cloud, practice, and knowledge graphs. | When the question is causal — motivation and consequence rather than inventory. |
| `planner` | Researches the codebase and existing decisions, then creates a structured, phased plan with success criteria. | When starting a feature, refactor, or other multi-step task. |
| `plan-reviewer` | Adversarially audits a plan before implementation — reuse, architecture, performance, ordering, rule-compliance, and failure-mode coverage. Read-only; always produces a report. | After a plan is drafted, before any code is written. |
| `implementer` | Follows plan steps in order, updates status in the graph, verifies each step's criteria, and records findings. | After a plan is created and approved. |
| `test-planner` | Researches what needs testing, discusses scope and criteria, then creates a structured test plan. | When defining what to test and how. |
| `tester` | Executes a test plan step by step, runs the commands, and records pass/fail/skip. Read-only — reports results without fixing failures. | After a test plan exists. |
| `infra-reviewer` | Adversarially audits an infrastructure changeset before any infra command runs, grounding each claim in provider docs, current source, the live cloud graph, and runtime log graphs. Read-only; always produces a go/no-go report. | Before a deploy, apply, upgrade, provision, or image roll. |

Each agent picks up where the last one left off by reading the graph, so the
discipline is to run them in order — research before plan, plan before review,
review before implementation.
