---
name: explore
description: Build live causal context about a repo. Authors non-trivial thoughts that answer WHY systems exist and behave the way they do, weaving evidence across code, cloud, practice, and knowledge graphs. Distinct from /research (which describes WHAT) — /explore answers why.
argument-hint: <optional subsystem name or "<why-question>"; omit for a whole-repo sweep>
---

# Explore: $ARGUMENTS

<precedence>
User input > Skill constraints > Trained defaults

For universal orchestration discipline reference /orchestrate.
This skill is causal-investigation-specific.
</precedence>

You are building **causal context** about the repo. Output: **thoughts** — each a single "X because Y" claim — linked via `EdgeBecause`. Clusters are emergent (Leiden over thought graph + because-edges, runs via PropagationLoop). Authoring lives in session `explore-<topic-slug>`; no container node.

Other systems answer *what* and *how*; this one answers *why*.

Delegate heavy lifting to the `explorer` agent. It's optimized for causal investigation, cross-graph evidence wiring, and the 5-test non-triviality discipline. This skill orchestrates the run + user-approval loop.

## Step 0: Index Freshness

```json
manage({ "operation": "status" })
```

If behind HEAD, prompt user to reindex. Causal claims are only as trustworthy as the evidence they cite — stale index → stale claims.

## Step 0.5: Recall Existing Exploration

```json
thoughts({ "operation": "recall", "query": "$ARGUMENTS", "session": "explore-*" })
```

Three things to check:
- **Existing thoughts covering same ground** — semantic match → user may want to extend (new thought + because-link) or supersede (new with `branches_from`, prior `invalidated`).
- **Previously rejected candidates** — `session: "explore-rejected-candidates"` lists past noise; don't re-propose.
- **Invalidated thoughts with fresh evidence** — cited code/rule/decision has since changed → supersede-on-refresh candidate.

## Step 1: Choose the Mode

<modes>
  <mode id="sweep" condition="no args">
    Whole-repo causal scan. Agent runs 8 discovery signals; proposes candidate list of root why-questions.
  </mode>
  <mode id="targeted" condition="with args">
    $ARGUMENTS is either subsystem identifier (`/explore dreaming`) or literal why-question in quotes (`/explore "why does the summarizer use contains-only ancestry"`).
    Subsystem → focused sweep restricted to area-touching signals.
    Literal why-question → agent uses it as root, skips proposal.
  </mode>
</modes>

## Step 2: Spawn the Explorer Agent

<spawn id="explorer" background="true">

  <invocation>
    Agent(
      subagent_type: "explorer",
      prompt: "Build causal thoughts for: $ARGUMENTS. Discipline: every thought must take the form 'X because Y' and pass the 5 causality tests (causal structure, synthesis from ≥2 sources, non-restatement of existing rules/decisions, non-derivability from a single cited source, tension integrity). Use the discovery signals in sweep mode (decisions, rules, topology articulation/scc/dsm, cross-graph linkage proxies, dream community summary open_questions, tensions, orphan because-chains). For targeted mode, restrict signals to the named area. Propose candidates first; develop only what the user picks. Author thoughts in session explore-&lt;topic-slug&gt; via mutate(type:'thought', session:...); link thought → thought via EdgeBecause; link thought → evidence via relates-to / informed-by including cross-graph evidence via linkage proxies. Supersede stale prior thoughts via branches_from + mark prior invalidated — charges do NOT carry forward. If you author research questions (create_research open_questions) or propose patterns, each entry carries a required summary you write.",
      description: "Explore: &lt;brief topic&gt;",
      run_in_background: true
    )
  </invocation>

</spawn>

## Step 3: Walk the User Through the Candidate List

Sweep mode returns candidates. Present each:

```
1. **Why <root question>?**
   Sources: <signal types + node IDs>
   Scope: <pkg:X | cross: A+B+C>
   Why this passes the bar: <one-line>
```

Cap at top 10 by signal density × source diversity. Mention how many were filtered out.

End with: *"Which to develop into causal chains? (numbers, 'all', or 'none')"*

Targeted mode skips the candidate list.

## Step 4: Develop Picked Candidates

For each picked candidate, explorer runs causal investigation loop and returns a causal-chain summary. Surface as it lands:

```
## Why <root question>?

Session: explore-<topic-slug>
Scope: <list>
Discovery signal: <what triggered>

### Causal chain
1. <thought content> — id: <id> — informed-by <evidence>
2. <thought content> — id: <id> — because of (1) — informed-by <evidence>
3. ...

### Clustering
After next PropagationLoop pass, these share cluster_id via EdgeBecause.
```

After all chains land, ask: *"Any prior thoughts you want to supersede based on what landed? Or extend any chain with deeper investigation?"*

User-driven — never mutate prior thought status without explicit instruction.

## Step 5: Supersede Stale Thoughts

If Step 0.5 surfaced thoughts whose cited evidence has since changed AND user asked to refresh: explorer appends with supersede — new thought with `branches_from: <prior_id>`, prior `invalidated`. Charges do NOT carry forward.

## Step 6: Record Rejected Candidates

If user rejected candidates in Step 3, append tracking thought to `explore-rejected-candidates`:

```json
thoughts({
  "operation": "think",
  "content": "Rejected this run: <list + brief why>",
  "session": "explore-rejected-candidates"
})
```

Makes next sweep dedup against past noise.

<constraint id="explore-discipline" severity="hard">

  <anti-patterns>
    <pattern>Auto-developing in sweep mode — sweep proposes, user picks, agent develops only what's picked</pattern>
    <pattern>Writing summaries — per-symbol and per-package summaries exist from indexing; /explore is causal claims only</pattern>
    <pattern>Creating thought_cluster nodes — no such type; clusters emerge via Leiden over EdgeBecause</pattern>
    <pattern>Skipping the 5-test bar — failed thought is anti-value, skip rather than write</pattern>
    <pattern>Citing cross-graph evidence by text — use linkage proxy infrastructure so traversal works</pattern>
    <pattern>Re-litigating rejected candidates — check explore-rejected-candidates session first</pattern>
    <pattern>Superseding prior thoughts without user approval — branches_from + invalidated is destructive (charges don't carry forward)</pattern>
    <pattern>Ceremonializing small runs — targeted /explore "why does X work" produces one chain and stops</pattern>
  </anti-patterns>

</constraint>
