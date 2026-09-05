---
name: research
description: Validate a ticket or answer a discovery question through the researcher agent. Validation reproduces every ticket premise on the current tree and stamps the ticket; discovery reports what exists and how it works with provenance on every claim. Use before planning, when a finding needs reproducing, or when investigating how something works.
argument-hint: <ticket id to validate, or a topic or question to investigate>
---

# Research: $ARGUMENTS

<precedence>
User input > Skill constraints > Trained defaults

For pipeline discipline reference /orchestrate. This skill is research-specific.
</precedence>

## Step 0: Recall and freshness

`thoughts({"operation":"recall","query":"<topic>"})` and a knowledge `search`
first; what the graph holds goes into the brief as leads to reproduce, never as
facts. `manage({"operation":"status"})`; collect if the index is behind.

## Step 1: Decide the mode

- `$ARGUMENTS` names a ticket → VALIDATION: the researcher reproduces every
  premise, corrects the facts, fills the detail, stamps `metadata.validated`
  or reports what blocks it.
- Otherwise → DISCOVERY: what exists, how it works, the existing idiom, the
  call graph.

## Step 2: Spawn the researcher (background)

The brief is discovery-framed: the problem or the ticket, never a candidate
mechanism or a solution. Per the write-a-brief rulebook it carries verbatim
the user's load-bearing rules, the tree to work at, absolute paths, and the
isolation rule (probes in scratch; the operator's services untouched).

```
Agent(subagent_type: "researcher",
      prompt: "VALIDATE ticket <id> per your validation mode: reproduce every premise on <tree>, correct the facts on the ticket, fill what the draft did not know, stamp metadata.validated or report what stays unverified and what would change intent. Recall and search before every claim. Deliver the report to main.",
      description: "Validate: <ticket>",
      run_in_background: true)
```

## Step 3: On return

Read the whole report. In validation mode: confirm the stamp landed by
fetching the ticket; route intent changes to the user; a ticket left in draft
goes back to /brainstorm with the researcher's evidence. In discovery mode:
present the existing idiom, what exists, the call graph, what was decided and
what is unclear, with the provenance labels intact. Findings that confirm or
contradict a recorded thought charge it, citing the research node.

<constraint id="research-discipline" severity="hard">
  Researching inline instead of through the researcher · a brief that names a
  mechanism · presenting a claim without its provenance · treating a prior
  thought as a fact instead of a lead · a validation stamp written by anyone
  but the researcher.
</constraint>
