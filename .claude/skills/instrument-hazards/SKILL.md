---
name: instrument-hazards
description: Action rulebook of instrument blind spots — graph-read projection traps, shell semantics that are not inferable, recurring fabrications, measurement artifacts, tool-retry discipline. Read before the first load-bearing command of a session. Not user-invocable.
user-invocable: false
---

# INSTRUMENT-HAZARDS — what the tools do to your measurements

<!-- version: 6 -->
<!-- Read at: before the first load-bearing Bash command or projected graph read
     of a session. Pure instrument knowledge; applies to every role. A line
     belongs here only when it is about the knowledge tools, the shell as their
     fallback, or the reading of a measurement; a fact about a language, a build
     system, an operating system or one repository never does. -->

## Graph-read blind spots

- THE CODE GRAPH IS A SNAPSHOT — line numbers rot immediately. Use
  search/file_symbols/traverse as locators; the file:line you write comes from
  having OPENED the file. Wrong ranges cluster in NAVIGATIONAL citations.
- PLAN TREES CARRY NO METADATA: `plan_tree` omits `metadata.command` and
  truncates descriptions — it is an INDEX. Fetch criteria via
  `query(ids:[...], fields:["metadata.command","description","name"])`; a
  criteria review through a tree dump passes vacuously.
- GRAPH NODE BODIES HIDE UNDER PROJECTION: thought/finding nodes body in
  `content` — `mode:"examine"` renders no body and a `description` projection
  returns "". Read them UNPROJECTED (bare `query(id:...)`) before asserting
  anything about their contents. Plan/phase/step/criterion nodes body in
  `description`.
- AST ENCLOSING FIELDS ARE GRAPH-HYDRATED: file_path/lines are filesystem-true;
  enclosing_node_id/signature inherit index staleness. Establish containment
  structurally (contains_pattern) or by opening the file.
- CODE-GRAPH READS AUTO-FILL A BRANCH OVERLAY from the local repo manifest
  whenever the checkout is on a branch. search and file_symbols name which
  graph answered in the result header's bracket (`[repo@branch]`); traverse
  renders no tell at all, so a pasted header is never its proof. Pass
  `branch` explicitly on
  every search/file_symbols/query/traverse read that yields a citation, and
  say so where the citation lands. Read and ast walk the filesystem and take
  no branch, so they are exempt.
- A BATCHED file_symbols `limit` IS A TOTAL spent in request order, not a
  per-file cap: the last file can render its summary and zero symbols and
  read as "not indexed". Re-run that file alone before concluding the index
  is thin.
- AN AST NAME CENSUS IS AN IDENTIFIER CENSUS: `by_kind {identifier: N}` is the
  tell. It matches no comment node, so an ast zero on a name means
  not-in-code, never not-in-tree. Comment text is grep's, with a same-run
  known-positive.
- A PER-METADATA-KEY PROJECTION omits the key entirely when a node lacks it —
  "empty" and "never asked" are indistinguishable in the output; an arm that
  ignores the projection returns rows shaped as if you asked for nothing.
- PREMISES NEED THE DEFINING ARTIFACT: a comment in file A about a fact defined
  in file B verifies nothing — go to the artifact that DEFINES it.
- A DRY RUN IS A READ ON A DIFFERENT PATH, never evidence of what the real
  call does: a preview can resolve zero targets (it reads without tombstones)
  while the same call without `dry_run` deletes them. The reachability or
  effect of a write is proven only by the real call and a read-back, on a
  scratch target; a "would do nothing" preview is not a "cannot do it" finding.
- A SEARCH IMMEDIATELY AFTER A COLLECT CAN RETURN ZERO while the index is
  still being fed, and a zero-result answer echoes the query, so a reader
  that greps the answer for its own term passes on the miss. Read the
  staleness line, bound the wait, key the reader on a rendered node or a
  rank, and prove the reader can fail.

## Shell semantics are not inferable (each shipped; caught only by execution)

- `cmd && echo BAD || echo OK` always exits 0. A piped status belongs to the
  LAST element.
- TRUNCATED PIPES MANUFACTURE ABSENCE: a `grep | head -N` cutting before the
  disproving lines yields a confident false negative — absence claims require
  the untruncated run.

## Recurring fabrications (each shipped in a real artifact; caught in audit)

Citing a symbol at the wrong scope; a field that does not exist; inverted
argument order; a sibling file's line number; a module name off by one word; a
type whose only existence was a neighboring docstring's promise. Protocol: open
via file_symbols/Read; transcribe names, signatures, line numbers LITERALLY;
re-read every code sample against the source after writing it.

## A measurement can be correct while its reading is an artifact

The failure that survives every other check, because the number really was
observed: a paged/head-truncated result reads as absence; a single before/after
reads as causation; a projection omission reads as empty. Before a number
becomes load-bearing, ask what the INSTRUMENT could be doing to it; prefer a
cause you can summon on demand over one that correlated once.

- A WALL-CLOCK BOUND ON A LOADED MACHINE measures the machine, not the code:
  assert the observable the bound stood in for (a count, an order, a lock
  held) and keep the clock as a derived pathology fence with a stated ratio.
- AN INSTRUMENT THAT CAN RETURN EMPTY gets a non-empty control in the same
  run: two empty extractions compare as identical, and a dry run that
  succeeds without the gate it stands in for says nothing about the gate.

## Tool-call retry discipline

A failed tool call's retry re-sends the COMPLETE parameter set — fixing the
named error while silently dropping another param is the top retry failure.
Never assert a tool defect as fact: re-read the call YOU emitted first; report a
HYPOTHESIS with your exact payload. Validation errors naming a field are
precise — believe them, and never work around one by dropping the validated
field.
