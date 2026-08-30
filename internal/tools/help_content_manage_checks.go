// SPDX-License-Identifier: Apache-2.0

package tools

// help_content_manage_checks.go holds the manage_checks help topic.
//
// IT IS ITS OWN FILE FOR A MEASURED REASON, not a stylistic one: the ast help
// content already sits near the package's per-file line ceiling, and seven
// where-tree traps plus the folding idiom do not fit in what is left.
//
// NOTHING PRIVATE SHIPS IN THIS TEXT. It compiles into the OSS artifact, so no
// knowledge-graph node id and no internal tracker id appears here — the traps are
// named in words and the work that produced them is described rather than cited.

// helpManageChecks documents the manage_checks tool: its operation vocabulary,
// the where-tree scoping traps that cost a debugging round each when the first
// defect-class check was authored, and the two-fixture folding idiom.
//
// IT DELIBERATELY DOES NOT RESTATE THE CHECK CONTRACT. help("patterns") already
// carries the eight metadata keys and the six consumer rules; two hand-kept
// copies of one contract is the shape that rots into two different pieces of
// advice, so this topic cites that one and adds only what is not there.
const helpManageChecks = `# manage_checks — author, inventory and run the deterministic corpus checks

A CHECK is a finding node in the checks graph carrying an executable assertion in
metadata, bound to exactly two example nodes: one it MUST fire on, one it must be
SILENT on. The contract itself — the eight metadata keys, the four-row parse
table, the six consumer rules — lives in help("patterns"); read that first. This
topic covers the TOOL, the where-tree traps, and how to fold more than two
conformance cases into the two fixture slots.

## Operations

### list — what the checks graph holds

    manage_checks({ "operation": "list" })
    manage_checks({ "operation": "list", "language": "go" })

Renders every checks-graph node under the contract's four parse rows, plus a
fifth lane the other surfaces cannot produce: EXAMPLE NODES BOUND BY NO CHECK. An
orphaned fixture is reachable by no executor and by no ranked search, so nothing
but this names it. Omitting language lists every language rather than defaulting
to one.

### create — one call, both fixtures, validated before anything is written

    manage_checks({
      "operation": "create",
      "name": "no-deferred-close-inside-a-loop",
      "summary": "a deferred Close inside a loop body runs at function exit, so handles accumulate for the whole loop",
      "description": "the rule and why it matters, in prose",
      "language": "go",
      "severity": "warning",
      "check_type": "ast_pattern",
      "dsl_pattern": "defer $X.Close()",
      "check_where": "{\"inside_pattern\": {\"of\": \"$match\", \"pattern\": \"for $$$_ { $$$_ }\"}}",
      "fixture_bad":  { "name": "...", "summary": "...", "content": "package p\n\nfunc f() { for range xs { defer x.Close() } }\n" },
      "fixture_good": { "name": "...", "summary": "...", "content": "package p\n\nfunc f() { defer x.Close() }\n" }
    })

ORDER IS THE POINT. The check and both fixtures are validated IN MEMORY — the
pattern parses and compiles, the where-tree names kinds the grammar has, the check
fires on the bad example, stays silent on the good one, and fires again on the
good one with the where-tree dropped — and nothing is written unless all of that
passes. A refusal names which leg failed and carries the per-fixture match counts.

On success the two example nodes are written first, then the check, carrying both
fixture ids and both display edges:

    check --avoid-when--> the bad fixture      (the shape the check fires on)
    check --applies-when--> the good fixture   (the conforming near-miss)

Those edges are DISPLAY-ONLY. Every executor resolves fixtures from the metadata
keys and never from an edge. If the check write fails after the fixtures land, the
error names both orphaned fixture ids: nothing is rolled back and nothing is
silently cleaned up, and list's unbound-fixture lane is where you find them again.

### run — execute checks against a working tree

    manage_checks({ "operation": "run", "repo": "knowledge", "language": "go" })
    manage_checks({ "operation": "run", "repo": "/abs/path/to/checkout", "language": "go",
                    "path_prefix": "cmd/knowledge/internal/tools" })
    manage_checks({ "operation": "run", "repo": "knowledge", "language": "go",
                    "ids": ["<check node id>"] })

repo names BOTH the code graph and the tree that is walked; an absolute checkout
path works and the graph name is taken from its basename. ids narrows to the named
checks, and an id matching no check in the corpus is an ERROR naming that id —
never a silent widening back to the whole corpus.

The output leads with one machine-readable verdict line:

    corpus_scan: CLEAN  checks_flagged=0 sites_flagged=0 checks_refused=0 llm_only_not_executed=0 truncated=false

CLEAN means every selected check executed, nothing was withheld and no site was
flagged. FLAGGED means the run completed and found something. INCONCLUSIVE means
the run did NOT deliver a complete answer — a check was refused, or output hit a
render ceiling — and it outranks FLAGGED, because a run that could not execute
part of its corpus has not established that what it flagged is all there is. Read
checks_flagged as "checks that flagged something": a check that executed and
matched nothing leaves no finding, so it is a floor rather than a completeness
measure.

## Using a check as a criterion command

The same classification is available from the shell, which is what makes a check
usable as a plan criterion. THE COPY-PASTEABLE FORM resolves its own repository
root rather than hardcoding a checkout path — a criterion pinned to one directory
measures the wrong tree the moment implementation happens in a worktree:

    cd "$(git rev-parse --show-toplevel)" || exit 1
    knowledge check run --repo "$(basename "$PWD")" --language go <check node id>

THE THREE EXIT CODES:

    exit 0  CLEAN — every selected check executed, nothing was withheld, and no
            site was flagged.
    exit 3  FLAGGED — the run completed and flagged at least one site. This is
            the answer a criterion is usually gating on.
    exit 4  INCONCLUSIVE — the run could NOT answer. A check was refused by the
            admission gate or the environment, or a render ceiling truncated the
            output. IT DOES NOT MEAN THE CORPUS IS CLEAN, and it is a separate
            code from 3 precisely so a criterion author cannot read a corpus that
            failed to run as a corpus that came back clean.

1 and 2 are reserved and never used for a verdict: 1 is any command failure and 2
is "no valid session", both of which every subcommand can return. So a criterion
can always tell "the check found something" from "the command could not run".

The exit status and the MCP verdict line are computed by the same fold over the
same findings — there is one classification, with two faces.

## The where-tree traps

Seven scoping facts, each established empirically and each costing a debugging
round. They are stated as the rule PLUS the symptom you will actually be searching
for when you hit it.

1. "$match" IN A NESTED WHERE BINDS TO THE NEAREST PATTERN SCOPE, not the
   outermost match. Symptom: an inside_pattern or contains_pattern sub-where that
   references "$match" silently constrains the sub-pattern's own node instead of
   the top-level one, and the check matches far more or far less than you meant
   with no error. Reach the outer node with "$outer.X" instead.

2. AN 'as' BINDING IS INVISIBLE INSIDE ITS OWN LEAF'S NESTED WHERE. It exists only
   for the leaf's downstream SIBLINGS. Symptom: a where-tree that names its own
   'as' binding inside the same leaf fails to resolve the capture, and the leaf
   evaluates as though the constraint were absent.

3. SUB-PATTERN CAPTURES NEVER ESCAPE TO SIBLING LEAVES — only the 'as' name does.
   Symptom: a capture bound inside a contains_pattern is unresolvable from a later
   sibling leaf, so a same_text or flows_to referencing it never fires and the
   filter reads as a silent pass.

4. flows_to REFUSES A SEQUENCE CAPTURE AS ITS 'from'. Symptom: a $$$X sequence
   capture used as the flow source is rejected rather than walked; use a single
   capture for the source and quantify with contains_pattern.

5. GO'S SHARED-TYPE PARAMETER GRAMMAR MAKES PARAMETER-POSITION ANCHORING
   IMPOSSIBLE IN A TEMPLATE. A template like ($$$_, $P $PT) parses as ONE
   parameter_declaration rather than as a trailing parameter. Symptom: a pattern
   trying to pin "the last parameter" or "a parameter after the others" matches
   nothing at all. Anchor on the parameter_list ANCESTOR with a where-leaf instead.

6. contains_pattern WITH 'as' BINDS THE FIRST MATCHING DESCENDANT AND DOES NOT
   BACKTRACK. Symptom: a check that should fire on a function's SECOND parameter
   goes silent because the binding locked onto the first one. Existential
   quantification over parameters therefore requires putting the flows_to INSIDE
   the contains_pattern's own where, using $outer refs to reach the outer capture
   — hoisting it to a sibling leaf binds only the first candidate.

7. THE FLOW ENGINE PROPAGATES CONSERVATIVELY THROUGH CALL RESULTS. A local
   assigned from a call that took a parameter-derived argument is treated as
   flowing from that parameter. Symptom: a flows_to check fires on sites you
   consider unrelated. That over-approximation is the safe direction for a
   presence gate; narrow with additional leaves rather than expecting exactness.

## Two fixture slots, and the folding idiom

The contract has exactly TWO fixture slots and requires them to differ. That is
not a limitation to route around: extra unbound example nodes are unreachable by
any executor, and adding a third slot would be a contract change.

When a check has more than one way to CONFORM, fold the additional cases in as
additional FUNCTIONS inside the bound good fixture — one function per silence
route. The admission gate then re-proves every route on every run, including the
one an edit to the where-tree would most plausibly break.

A FIXTURE PAIR PROVES ONLY THE AXES IT VARIES, and this is the lesson worth
carrying: a check written against one incident's text matches that incident and
nothing else. If the good example differs from the bad one along two axes, the
gate cannot tell you which axis the check is actually keying on — and a check
authored that way passed the gate while being far broader than its author
believed. Vary ONE axis per conformance route, and name the axis in the fixture's
summary.
`
