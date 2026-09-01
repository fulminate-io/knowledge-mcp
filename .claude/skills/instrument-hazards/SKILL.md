---
name: instrument-hazards
description: Action rulebook of instrument blind spots — graph-read projection traps, shell semantics that are not inferable, recurring fabrications, measurement artifacts, tool-retry discipline. Read before the first load-bearing command of a session. Not user-invocable.
user-invocable: false
---

# INSTRUMENT-HAZARDS — what the tools do to your measurements

<!-- version: 1 -->
<!-- Read at: before the first load-bearing Bash command or projected graph read
     of a session. Pure instrument knowledge; applies to every role. -->

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
- A PER-METADATA-KEY PROJECTION omits the key entirely when a node lacks it —
  "empty" and "never asked" are indistinguishable in the output; an arm that
  ignores the projection returns rows shaped as if you asked for nothing.
- PREMISES NEED THE DEFINING ARTIFACT: a comment in file A about a fact defined
  in file B verifies nothing — go to the artifact that DEFINES it.

## Shell semantics are not inferable (each shipped; caught only by execution)

- `cmd && echo BAD || echo OK` always exits 0. A piped status belongs to the
  LAST element.
- Under a go.work, root `go test ./...` is module-scoped — it can test a
  gen-only module and exit 0 having tested nothing. `cd` into the module.
- `-run '^Name$'` matching nothing exits 0 — anchor a grep of the runner's
  `^--- PASS: Name ` line (trailing space; never `$`-anchor the PASS line).
- go test result lines end with a duration — `$` after a test name never
  matches; anchor with ` \(` or a trailing space.
- zsh coerces an empty capture in an integer comparison (`test "" -eq 0` → 0):
  guard every capture with `test -n`, or push the assertion into a script.
- zsh does not word-split unquoted expansions — a `set -- $var` loop can run
  garbage whose exit 1 mimics your expected red.
- zsh makes `status` a READ-ONLY alias of `$?` — `status=$?` is refused and
  later reads see the live `$?`. Syntax-check stored commands under both
  shells (`zsh -n`, `bash -n`).
- A linter/compiler not told a build tag never checks those files.
- TRUNCATED PIPES MANUFACTURE ABSENCE: a `grep | head -N` cutting before the
  disproving lines yields a confident false negative — absence claims require
  the untruncated run.

## Recurring fabrications (each shipped in a real artifact; caught in audit)

Citing a method that is package-level; nonexistent struct fields; inverted
argument order; a sibling file's line number; a package name off by one word; a
type whose only existence was a neighboring docstring's promise. Protocol: open
via file_symbols/Read; transcribe names, signatures, line numbers LITERALLY;
re-read every code sample against the source after writing it.

## A measurement can be correct while its reading is an artifact

The failure that survives every other check, because the number really was
observed: a paged/head-truncated result reads as absence; a single before/after
reads as causation; a projection omission reads as empty. Before a number
becomes load-bearing, ask what the INSTRUMENT could be doing to it; prefer a
cause you can summon on demand over one that correlated once.

## Tool-call retry discipline

A failed tool call's retry re-sends the COMPLETE parameter set — fixing the
named error while silently dropping another param is the top retry failure.
Never assert a tool defect as fact: re-read the call YOU emitted first; report a
HYPOTHESIS with your exact payload. Validation errors naming a field are
precise — believe them, and never work around one by dropping the validated
field.
