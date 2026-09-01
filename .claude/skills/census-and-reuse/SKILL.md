---
name: census-and-reuse
description: Action rulebook for surface censuses and reuse — programmatic enumeration, counts-are-floors, caller censuses before touching symbols, and the reuse-before-new discipline. Not user-invocable.
user-invocable: false
---

# CENSUS-AND-REUSE — enumerating surfaces and citing what already exists

<!-- version: 1 -->
<!-- Read at: any step judging a surface >~15 sites / ~5 files / pattern-defined,
     any deletion/rename/signature change, and before any proposed new unit. -->

## Programmatic census

Any surface larger than ~15 sites or ~5 files, or pattern-defined, is enumerated
PROGRAMMATICALLY (ast/grep/script, commands recorded, run during planning) —
hand counts rot and do not converge under review. The census output IS the
surface: per-file lists are floors; every sweep completion criterion RE-RUNS the
census and asserts remainder-by-kind = 0. Multi-kind migrations get a small
checked-in census script emitting a manifest ({file, line, kind}) with judgment
sites marked manual. Pattern breadth: aliased forms, template literals, comment
occurrences (state whether they count), indirect flows via callers — every kind
the site definition matches needs a classification, or the gates are permanently
unsatisfiable.

UNIFORM structural edits across many files are prescribed as
`ast operation:"replace"` (dry-run preview, where-tree scoping, re-parse gate) —
never sed/perl, never enumerate-then-Edit when one template covers every site.
Sweep size is NOT an architecture constraint: never pick a lesser design to
dodge a uniform sweep.

## Counts are floors

Before any count or completeness claim becomes load-bearing, re-derive it with a
pattern strictly BROADER than the one that produced it, then subtract. An
exact-phrase sweep is a floor: one inflection, one casing, one quoting
difference hides members. Helper indirection defeats literal-pattern censuses in
three disguises: a helper taking the payload under another name, an anonymous
inline struct, a synthetic internally-manufactured payload — after the literal
pass, re-derive keyed on the CONSUMED TYPE and reconcile. A denominator you
cannot rebuild row by row is an estimate wearing a measurement's clothes.

## Caller census before touching a symbol

Before any step deletes, moves, or changes the signature/return type of a
symbol: enumerate EVERY call site with `traverse(edge_types:["CALLS"],
direction:"in")` PLUS `ast(pattern:"<Symbol>($$$_)", include_tests:true)` —
never assert a caller count from reading; test-file callers break exactly like
production ones. Exported symbols need BOTH call shapes: bare (`Fn($$$A)`) and
selector (`$PKG.Fn($$$A)`) — bare-only under-reports the callers most likely to
break. A public-entry-point census is a floor: a private helper you also change
has its own caller set the census never enumerated.

## Enumeration is the work

Writing a consequence down is not handling it. When a step claims coverage, the
enumeration IS the deliverable: greps of the actual corpus, a complete cut list
where every member gets a side, a caller census run to the end. Treat any file
split or surface move as a MIGRATION: list every top-level declaration and
assign each explicitly. For every test-harness detail a step mandates, NAME THE
CATCHER: which specific test goes red if it is omitted — and trace what actually
fails under omission before naming it.

## Reuse census (locked rule: snowflakes are unacceptable)

For every proposed new unit, BEFORE it lands in a step: (1) state the unit in
one sentence; (2) search BOTH axes — naming/concept via `search`, structure via
`ast` (a duplicate under a different name is what search misses and ast
catches); (3) read top candidates with file_symbols/Read, never summaries;
(4) classify DELEGATE / EXTEND / GENUINELY-NEW — genuinely-new requires both
axes missed plus a written justification; (5) embed the reuse target as
file:line:symbol in the step. Emit a reuse_check node per code-touching step;
`copy-paste-modify` is forbidden; no static reuse tables — search every time.

CITING AN ANALOG IS A CLAIM ABOUT ALL OF IT: naming an analog asserts its WIRING
(grep its distinguishing identifier repo-wide — every hit is a place your unit
needs an equivalent) and its CONTROL FLOW (an `*IfNotExists`/`Ensure*`
skip-vs-merge branch decides whether your field is ever applied). Read past the
lines you quote; if the analog's test is your model, confirm it exercises what
you need. Cite the DECLARATION, not a construction site — open it and state its
actual shape.
