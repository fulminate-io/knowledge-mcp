# Skills

## Overview

Skills are slash-command workflows that drive the knowledge graph through a
recurring task. Where an agent is a specialized assistant you delegate to, a skill
is an invocable procedure — you type `/research`, `/plan`, `/implement`, and the
skill walks the matching sequence of graph operations. They encode the project's
working discipline so each phase of work follows the same proven shape.

The skills line up with the development lifecycle: explore and research a problem,
plan and review the work, implement it, then test and retro on the result.

## When & how to use

Invoke the skill that matches what you are doing. Most take a short argument — a
topic, a plan id, a description — shown below as the invocation hint. The core
build loop is `/research` → `/plan` → `/implement`, with `/test-plan` and `/test`
for verification.

| Skill | Purpose | Invoke with |
| --- | --- | --- |
| `/research` | Investigate how something works — searches code, knowledge nodes, and prior decisions to build understanding. | a topic or question to research |
| `/explore` | Build live causal context — authors thoughts answering WHY a system exists and behaves as it does. | an optional subsystem or why-question (omit for a whole-repo sweep) |
| `/brainstorm` | Interactive exploration and requirements discovery before planning; records discoveries as research, findings, and decisions. | a topic or question to explore |
| `/plan` | Create a structured, phased implementation plan with success criteria. | a description of what to plan |
| `/implement` | Execute a plan step by step — updates status, verifies criteria, records and charges thoughts. | a plan id or name |
| `/record-decision` | Record an architectural decision with full rationale for future developers. | a brief description of the decision |
| `/reflect` | Introspect on the thought graph — personality, influence, tensions, blind spots, reasoning patterns. | an optional focus area |
| `/test-plan` | Design a test plan collaboratively — scope, steps, and pass/fail criteria. | a description of what to test |
| `/test` | Execute a test plan — runs each step and reviews pass/fail/skip results. | a test plan id or name |
| `/retro` | Capture the session feedback loop after work is verified — reproduction guide, real-world evidence, findings, ticket close. | an optional focus on what was delivered |
| `/ingest-patterns` | Ingest design patterns from an authoritative source into the design-patterns library graph. | a source slug, PDF path, or ticket id |
| `/orchestrate` | Orchestration discipline — the team hierarchy, signal routing, and drift detection that govern running work end to end. | (loads at the brainstorm-to-execute boundary) |
| `/hive` | Join a hive as a worker (claim work, do it, report the result) or coordinate (dispatch role-targeted work and read outcomes) over the cloud work-queue. | `join <hive> as <role>…` or `dispatch <hive>` |

The skills are designed to be run in order — `/retro` only makes sense after the
work it reflects on is verified, and `/implement` only after a plan exists. Follow
the lifecycle and each skill picks up the graph state the previous one left.
