---
name: prefill
description: Action rulebook for the prefill — the implementer's preloaded context, stored as a plan node with no steps. The section schema, the citation rule, the what-to-test list, the census rule, and what a prefill never contains. Read by the planner before authoring and by the reviewer before auditing. Not user-invocable.
user-invocable: false
---

# PREFILL — the implementer's preloaded context

<!-- version: 2 -->
<!-- Read at: planner, before writing; reviewer, before auditing; implementer,
     before its first read of a prefill. -->

## What a prefill is for

The prefill exists so the implementer implements instead of asking. It is
everything an engineer new to this part of the codebase would need to build
the ticket correctly on the first try: where the change lands, what to reuse,
what it must not break, how it will be tested, and where it lands. It is a
plan ROOT with no phases and no steps, carrying the goal, the tree, the reads
and the citations block and no section text at all; each numbered section
below is its own section node on a positioned contains edge, written alone and
read alone.

## The citation rule

Every file, line, symbol, count and claim in a prefill carries the command that
resolved it and was opened this session at the tree the prefill names. A
citation without its command is an assertion, and an assertion in a prefill is
a T1 in review. Counts are tool output pasted beside the command, labeled as
floors; a hand-written "all N sites" does not exist in a prefill.

## The shape

The root is an INDEX, not a document. Its sections hang off it as section
nodes on `contains` edges carrying a zero-based position, in the order below;
the root's tree lists them in that order with each one's size, so a reader
chooses its pages rather than loading the whole plan. Changing a section is
ONE write to that section node: no other section and not the root is
re-sent, and a planner that re-writes the whole prefill to fix a sentence has
made the mistake this shape exists to prevent.

A reviewer attaches an ANNOTATION to the section it concerns, never to the
root: `correct`, `finding` with its tier, or `needed change` carrying the
exact replacement text in the annotation's body. The annotation's kind and
tier ride the edge that joins it to the section as well as the annotation
node, so a reader ranks a section's annotations from an edge-metadata
traverse without opening one. The section's own body is never edited by the
reviewer. A needed change is applied by exact replacement of the quoted text,
never by retyping the section.

## The sections

1. **Tree**: the sha the prefill was resolved at, the branch it lands on, and
   what it lands after.
2. **Touch points**: every site the change reaches, from a census by tool
   (`ast`, `traverse`, `search`) with the command and output; per site, one
   line on what changes there. Callers come from a traverse on CALLS plus an
   ast shape match including tests.
3. **Reuse**: the exact symbol to extend or call, file and line, opened, with
   the idiom it embodies and the practice node that names the idiom where one
   exists. A new unit is justified only by a recorded miss on both a name
   search and a shape search.
4. **Contracts and seams**: every value the change produces or consumes across
   a package or process boundary: producer, consumer, the artifact that carries
   it. Informal contracts count: metadata keys read by name, derived names,
   status and reason strings, schema versions, log lines another component
   scrapes.
5. **Performance shape**: the in-tree primitive the change rides (the batch
   helper, the parallel primitive, the index) and the scale it will meet,
   cited. Serial is fine for a single-call operation; say so in one line.
6. **What to test**: the list the implementer writes tests from and the code
   reviewer audits against. Per ticket requirement, the observation that shows
   it met. Per changed function, the input classes the specification names,
   including the real-world shapes (encodings, boundaries, real documents). Per
   changed state, the transitions (create then read, write then re-write,
   delete then read, the operation racing a background pass). Per error
   return, the arm. Per seam, the test that runs both real sides, on the
   harness the repository already has for that reach. Per changed behavior
   with sibling arms (formats, transports, read paths, callers of one seam),
   the matrix: every arm times every parameter times the input classes inside
   each cell (empty, single, at the boundary, beyond it, the wrong kind), with
   the behavior each cell must hold, so a silent drop in one cell is a missing
   row rather than a discovery. Per guard or control the change adds, the
   mutation that must turn a named test red. The list names tests; it never
   runs them.
7. **Harnesses**: the repository's test harnesses that reach this change: make
   targets, out-of-process harnesses that drive the real binaries,
   container-backed suites, fixture libraries, and the CI legs that run each.
   Two modules that cannot import each other are never a reason to leave a
   seam untested when a harness spawns both.
8. **Landing constraints**: rebase order, files the ticket puts out of scope,
   sibling work in flight on the same branch and the shared files it touches,
   and anything the change must not break that no test covers, named.
9. **Style**: the conventions the touched packages already follow, cited to a
   neighboring file, and the language patterns the ticket marks as defensive.
10. **Checks**: the existing corpus checks that cover the touched shapes, with
    the `manage_checks(run)` output over the current tree pasted (hits read,
    not counted); and, for every structural requirement on the ticket, the
    check that enforces it: an existing id, or the check to author with its
    pattern sketch and the two fixtures (the bad shape; the near-miss good
    shape). The implementer admits it; the code reviewer audits its fixture
    pair.

## What a prefill never contains

Steps, phases, ordering the implementer can choose for itself, executed
criteria, shell commands to gate on, a reference implementation, or design
the user has not decided. A question the planner could answer by reading,
running or searching is answered in the prefill, not left for the implementer.
A ticket premise the planner finds false is reported to the orchestrator as a
research finding with the evidence; the planner neither corrects the ticket
nor plans around it.

## Size

A prefill is read once by the implementer and once by the reviewer. Every line
costs both of them on every turn. Cite, do not paste; link, do not repeat; if a
section has nothing to say for this ticket, say so in one line and why. A
section that outgrows the page a reader can hold is SPLIT into two sections,
never truncated and never summarized away.
