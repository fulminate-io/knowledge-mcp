# Guides

## Overview

These are the developer guides for the knowledge project — a persistent memory and
reasoning graph that runs as a local MCP server. They are written for the
developer using the system: someone who installs the binaries, points a coding
assistant at the MCP server, and wants to know what each tool, agent, and skill
does and when to reach for it.

Each tool guide pairs hand-written prose (what the tool is, when to use it, worked
examples) with a generated parameter table kept in sync with the source. The
binary guide does the same for the CLI. The agent and skill guides describe the
higher-level workflows built on top of the tools.

If you are just getting started, follow a [setup guide](#setup) to install
knowledge and wire it into your assistant, read the [binaries guide](binaries.md)
to understand the server, skim the [agents](agents.md) and [skills](skills.md)
overviews to see the workflows, then dip into individual [tool guides](#tools) as
you need them.

## Concepts

New to knowledge? Read [Concepts](concepts.md) first — the mental model the rest
of these guides assume: the kernel/OS framing, the ten graph families, the
selector vocabulary, the client/daemon/server topology, and how code, cloud, and
knowledge link into one connected graph.

Then read [Capabilities](capabilities.md) — the four capability pillars in
depth: the reasoning loop, unified search, workflow integration, and the
collectors.

## Setup

- [Set up with Claude Code](setup-claude.md) — first-run setup for Claude Code.
- [Set up with Codex](setup-codex.md) — first-run setup for Codex.
- [Configuration](config.md) — the `~/.knowledge/config` reference (providers,
  models, and credentials).

## Contents

- [Binaries & CLI flags](binaries.md) — the `knowledge` client and
  `knowledge-server`, their subcommands and flags.
- [Agents](agents.md) — the specialized assistants (researcher, planner,
  implementer, and the rest) and how they compose.
- [Skills](skills.md) — the slash-command workflows (`/research`, `/plan`,
  `/implement`, and more).
- [Reasoning](reasoning.md) — the thought graph: the recall→think→charge→reflect
  cycle, the evidence model, DeGroot propagation, and the reflection modes. The
  product differentiator — persistent, evidence-weighted reasoning that survives
  sessions.

### Collection guides

- [Web collection](web-collection.md) — crawl a public website into a `web` graph
  with `collect`: seed URLs, follow patterns, the unbounded-by-default crawl, and
  how to scope it.
- [PDF collection](pdf-collection.md) — collect a PDF into the graph: the
  absolute-path requirement, section-mode chunking, and why it is text extraction
  rather than OCR.
- [Recipes](recipes.md) — transform a collected raw graph into structured domain
  nodes with a graph-resident, zero-LLM recipe: authoring, dry-run, and force.

### Tools

- [assemble](tools/assemble.md) — render a plan/test-plan/agent tree.
- [ast](tools/ast.md) — structural code search-and-replace.
- [collect](tools/collect.md) — index an external source into a graph.
- [create_plan](tools/create_plan.md) — batch-create a plan tree.
- [create_project](tools/create_project.md) — create a project container.
- [create_research](tools/create_research.md) — create research with questions.
- [create_test_plan](tools/create_test_plan.md) — create a structured test plan.
- [create_ticket](tools/create_ticket.md) — create a ticket within a project.
- [custom_collector](tools/custom_collector.md) — register and manage custom external collectors / plugins.
- [delete](tools/delete.md) — remove or prune nodes.
- [file_symbols](tools/file_symbols.md) — list the symbols in a file.
- [help](tools/help.md) — built-in documentation.
- [hive](tools/hive.md) — coordinate agents over the cloud work-queue (optional paid tier).
- [manage](tools/manage.md) — server and pipeline operations.
- [mutate](tools/mutate.md) — create, update, and link nodes.
- [query](tools/query.md) — lookup, browse, and special read modes.
- [record_decision](tools/record_decision.md) — record a decision with rationale.
- [search](tools/search.md) — unified search across graphs.
- [sync](tools/sync.md) — sync the graph with Fulminate Cloud (optional paid tier).
- [thoughts](tools/thoughts.md) — the persistent reasoning graph.
- [traverse](tools/traverse.md) — edge-first graph traversal.
- [worker](tools/worker.md) — manage background workers.
