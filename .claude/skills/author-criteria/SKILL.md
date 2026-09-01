---
name: author-criteria
description: Action rulebook for writing plan criteria — the evidence-bearing law (red+green execution proof required), criterion shape rules, and the forbidden-shapes catalog. Read by agents at the criterion-authoring flow step; not user-invocable.
user-invocable: false
---

# AUTHOR-CRITERIA — writing a criterion that can carry weight

<!-- version: 6 -->

## WHAT CRITERIA ARE

Criteria are GUARDRAILS on how new additions or changes will work with and
affect the ecosystem, and on how the changes solve the goal. Every criterion
belongs to exactly one of THREE CATEGORIES, declared on the criterion:

1. **PERFORMANCE** — the change meets its cost shape under real scale: the
   batch stays batched, the hot path stays allocation-flat, the query rides
   its index, the measured band holds at the scales the work will meet.
2. **LOGIC FAILURES** — the finished work behaves as intended and the
   wrong-but-compiling implementation is rejected: given this input, that
   output; this invariant holds under that operation; the forbidden state
   cannot be reached; the semantics match the goal.
3. **BLAST RADIUS** — the change's effect on existing OUT-OF-SCOPE code that
   depends on what changed: dependents still behave correctly (exercise their
   paths, run their suites against the change), contracts consumers rely on
   still hold, and nothing the change retired is still load-bearing somewhere
   the scope fence didn't look.

A candidate criterion that fits none of the three is not a criterion. A plan
whose set leaves any category unexamined states why (a doc-only change has no
performance category; a leaf addition may have no blast radius — say so, with
the reasoning, rather than padding).

NEVER GREP-SATISFIABLE: a criterion satisfiable by a grep — by any inspection
of text — is definitionally not testing any of the three categories, because
text inspection observes what the code SAYS, never what it DOES. A criterion's
command EXECUTES the behavior it guards: runs the system, the suite, the
probe, the measurement. Text tools may appear inside a command only to
post-process executed output (asserting on a runner's PASS line, extracting a
measured number) — never as the thing that decides. RECONCILIATION with the
prose-deliverable rule (invariant sentences): a mandated prose artifact whose
subject no execution can observe (a shipped doc row, a locked comment phrase)
may carry a text leg that can REJECT — but GREEN always requires the
criterion's executing legs to pass. Reject-only text legs do not make a
criterion grep-satisfiable; a criterion whose green is reachable by text
inspection alone does. A text leg guarding a prose CLASS (not one pinned
string) owes a bad fixture per axis it claims to discriminate — case, word
order, interposed tokens — or the pattern is re-derived from the step's own
enumerated sites and checked against every one before storing. A
known-positive control proves the probe ran; it never proves the pattern's
shape matches the class.

Existence and naming facts — a file exists, a method is named X, a symbol is
present, a config line appears, a test file contains a literal — are
COMPILATION AND BUILD CONCERNS. The compiler, the build, and the toolchain
already guarantee them wholesale: code that references a missing file or
misnamed method does not build. A criterion asserting one is TAUTOLOGICAL —
"the sky is blue" — true the moment the code compiles, incapable of detecting
any logical defect, testing the toolchain rather than the work. Such
assertions are inadmissible in criteria entirely — not as criteria, not as
legs. A behavioral command that consumes the artifact fails on its own if the
artifact is missing; nothing needs asserting.

THE TAUTOLOGY TEST, before storing each criterion: could this criterion be
false while the code compiles, the build passes, and the work is logically
WRONG? If it cannot be false under a compiling build, it is tautological —
delete it. If it cannot catch a logically wrong implementation, it is not
testing correctness — rewrite it until the wrong-but-compiling implementation
is the thing it rejects. Then name the LOGICAL defect class this criterion
alone detects; no nameable class, or another criterion already detects it →
not a criterion.

## THE APERTURE LAW: fewer, stronger, behavior-level

Criteria are authored at the BEHAVIOR level, and the default count is the
number of DISTINCT INSTRUMENT CLASSES — never the number of enumerable
fragments. Granularity is a vacuity generator: many fragment criteria (file
exists, entry listed, config classifies it) can each run green while the
composite behavior they orbit goes ungated — the fragment set reads as rigor
precisely because it is long. One end-to-end behavioral gate (run the real
pipeline, assert the real outcome) both catches what the fragments miss and
replaces them. Before storing a criterion set: (1) name each requirement's
BEHAVIOR and gate THAT — fragments become LEGS of that one criterion, not
sibling nodes; (2) a candidate criterion whose instrument class is already
represented must justify what distinct failure it detects, or it merges;
(3) count check — a plan whose criteria outnumber its distinct instrument
classes by more than ~2x is presumptively over-granular; consolidate before
storing. Every criterion costs authoring, execution, evidence, revision, and
audit on every future round — the count is a wall-clock liability, priced
accordingly.
<!-- Read at: the flow step that authors criteria (planner, before create_plan;
     reviewer, when auditing criterion text; implementer, when a plan directs
     authoring a criterion). Lens comes from the invoking step. -->

## THE LAW: A CRITERION IS UNSUBMITTABLE WITHOUT EXECUTION EVIDENCE

A criterion does not enter a plan until its metadata records BOTH directions
executed by its author against real state:

- `evidence_red`: the stored command run against the violating state (construct
  it — the undone/wrong work the gate exists to reject), with observed exit
  status and first output line, pasted.
- `evidence_green`: the stored command run against the passing or control state,
  same recording. Where the correct artifact does not exist yet, a labeled
  `PASSES-ALREADY (characterization guard | scope fence)` or
  `FAILS-AS-EXPECTED` classification with pasted output satisfies this leg.

A criterion missing either leg is a T1 audit finding, not a style issue. A label
without pasted evidence is indistinguishable from one written without running —
the exact defect this law kills. A broken probe and a genuine red share exit
codes: READ the output ("no tests to run", "missing script", an empty echoed
filename = broken probe, not evidence).

## The shape of a criterion

(Scope note: every text-matching technique below — anchors, comment-stripping,
identifier greps, survivor lists — governs POST-PROCESSING LEGS inside
executing commands, per NEVER GREP-SATISFIABLE above. None licenses a
criterion whose deciding instrument is text inspection.)

Every criterion has symbol_name (one-line pass condition), description
(observable check), and metadata.command (automated). It is FALSIFIABLE — fails
when the work is not done — and PASSES against correct work. The command ends in
the assertion: exit status is the signal, so a trailing display filter
(`| grep`, `| wc -l`, `| tee`) replaces the real result.

- COMMANDS RESOLVE THEIR OWN ROOT: never hardcode a repository path — `cd "$(git
  rev-parse --show-toplevel)"` so the command measures whatever checkout invokes
  it. A path pinned to the primary checkout greens against a tree carrying none
  of the change, exactly during the window when the criterion is the only
  evidence anyone has.
- NAME THE CAPABILITY, NOT THE MECHANISM: write what a consumer must be able to
  DO with the output, not one delivery mechanism. The tell: a criterion most of
  the real corpus cannot satisfy is likelier mis-specified than universally
  violated. Enumerate every carrier the data already has before comparing two.
- A GATE ASSERTS THE PROPERTY, NOT A PROXY: name the SMALLEST artifact the
  property lives in and make the leg's scope equal it. Wider is satisfiable by
  something other than what you meant. Construct BOTH a correct realisation the
  step permits AND the defect; a leg that cannot separate them measures neither.
  Widening a leg to silence a false red creates the class — narrow the STEP.
- CRITERIA DEMAND ONLY WHAT A STEP MANDATES: every exact string, flag, or line a
  criterion matches points at the step sentence that requires it, or the match
  loosens to structure. Your reference implementation carrying the detail hides
  the coupling; test against an implementation built from the step text alone.
- THRESHOLDS NAME THEIR NUMBER AND BOTH MEASURED BANDS: compliant work measured
  at X, the violation at Y, the constant separating them with margin both sides.
- GATE TITLES STATE THE SCOPE THE COMMAND ENUMERATES: a universal word in a
  title requires explicit enumeration or a census leg that fails when the
  population changes size. Read the title beside the command's iteration source
  and ask whether a new member would be covered.
- SHARED VOCABULARY DECLARED ONCE: any token, spelling, or count consumed by
  more than one node gets a single authoritative declaration; every other node
  cites it. Tree-derived counts are re-derive instructions, never locked facts.
- INVARIANT SENTENCES GET GATES: sweep ticket and step text for "never", "only",
  "no", "one", "exactly", "stays", "untouched" — each maps to a criterion or
  named test that fails when violated. A negative invariant changes nothing
  observable when violated; no deliverable-driven gate catches it by accident.
  Prose obligations a step itself mandates — a doc comment, a header contract,
  a summary line — are deliverables whose omission every other gate survives:
  give each one a comment-inclusive (deliberately raw) leg on a criterion
  already running in its step. A step mandating N deliverables whose criteria
  gate fewer than N is the recurring escape class — list the deliverables and
  check some criterion goes red when each is absent.
- ABSENCE GATES NEED A SURVIVOR LIST: a gone-assertion is authored with the
  closed list of legitimate survivors (absence-asserting tests, the dropping
  migration, prose), or correct work's only route to green is deleting the
  evidence the plan asked for.
- COUNT GATES PIN SITES: a count without pinned sites is green for any
  arrangement summing to the number. When the real effect lands in a GENERATED
  artifact, gate the artifact too — regeneration is separately omissible.
- ASSERT PER NAMED REGION: N instances in N named locations gets one assertion
  per region, never a whole-file count of N. A call-site grep never verifies
  what the CALLEE wires — grep both bodies when both are mandated.
- DETECTORS KEY ON THE PROPERTY, not token shape; explicit member rule plus an
  exclusion list, each exclusion carrying a control. When the toolchain reports
  the property directly, gate on the toolchain's report, not a textual proxy of
  its input.
- COMMENT-STRIP IDENTIFIER GREPS: default NO comments — strip first and grep the
  stripped copy; your own mandated doc comment otherwise reds correct work. Legs
  targeting comments grep raw, deliberately; hybrids split.
- ANCHORS MATCH THE ACTUAL SOURCE: run every grep anchor against the current
  file and confirm a hit before it enters a criterion; locked multi-word tokens
  are written unbroken on one line wherever the plan prescribes them.
- IDENTIFIER-ZERO NEEDS A CONTROL: a zero from an identifier grep is evidence
  only when a known-positive fired through the same probe in the same run.
  Re-derive identifiers by VALUE first.
- READ SURFACE IS VERBATIM DECODES: a tool's read surface is the structs
  receiving arguments verbatim; a param counts as consumed only where the
  resolver actually reads the field.
- FIXTURES SURVIVE GLOBAL OPERATIONS: a fixture must survive every sweep or
  finalize the test later runs; set-equality cannot see a fixture that vanished
  from both sides — cardinality-guard against a fixture-derived constant.
- EQUIVALENCE CLAIMS ENUMERATE OBSERVABLES: "no behavior change" is checked by
  listing the exported observables that could distinguish the two arrangements
  and probing the difference. Identity surfaces (IDs, parents, timestamps,
  ordering) are where false equivalences hide.
- QUOTE THE SUBJECT, NEVER THE INSTRUMENT: a quoted output line must be one the
  subject's own bytes can print — never your wrapper's echo. A gate silent on
  success is recorded as "exit 0 (silent on success)"; silence is a recordable
  fact, and instrument sweeps pair each silent zero with a printing control.
- FENCES DERIVE THEIR BASE AT RUN TIME: diff-fences resolve `git merge-base` at
  run time with a loud failure when the ref is missing; a pinned-commit base
  false-reds as the shared branch advances. The measured SHA lives in metadata.
- ONE APERTURE: a criterion set contributes as many chances to fail as it has
  DISTINCT INSTRUMENT CLASSES, not as many criteria. Before adding one, name
  the class it adds. A control satisfied by deleting the file controlled the
  instrument, not the artifact.
- HIDDEN SECOND CLAIM: a shelling criterion also claims how its TOOL matches,
  formats, and exits — name the tool assumption or you have not reviewed it.
- STORED COMMANDS RESPECT THE TEST CACHE: no force-rerun flags; long-running
  commands note that the executor should background them.
- CRITERIA ROT: re-verify symbol names at implementation time; a criterion's
  NAME claims only what its COMMAND falsifies; zero-counters need a case
  driving them non-zero.

## The forbidden shapes (every entry was a shipped defect)

trailing-filter · count-without-comparison (`test $(grep -c ...) -eq N`) ·
selector-matching-nothing (assert the runner's PASS line; `-run '^Name$'`
matching nothing exits 0) · prefix-match-swallowing-siblings ·
ref-less git diff · substring-collision (word boundaries or qualified names) ·
count-meets-the-test-file (scope to the owning file or exclude tests, at
authoring) · cross-plan-symbol-pin (check named sibling deletion lists) ·
aggregate-over-per-site-property · locked-identifier-vs-autofixer (spell locked
names as the auto-fixing linters will rewrite them) · stale-artifact-read
(remove the artifact first; `|| exit 1` guards) · semicolon-outside-the-and-list
(never end on a bare `exit 0`; a re-run of another plan's gate stores its bytes
verbatim) · single-shape-probe (probe beyond the target shape; known-negative
fixtures) · runner-output-format-assumption (pass the verbosity flag) ·
control-probes-a-parameterization (controls execute the STORED BYTES; brace
every expansion followed by a literal colon) · text-grep-that-cannot-see-syntax
(strip comments or use ast) · extract-region-then-grep-inside (a region with a
grammar gets a criterion that PARSES it, with a malformed known-negative) ·
comparison-mistaken-for-validation (a diff answers "identical?", not "correct?")
· name-overstating-the-instrument · self-testimony-as-evidence (manual criteria
emit a re-performable artifact) · invocation-that-does-not-exist (read the
manifest first) · green-direction-single-shape (correct implementations form a
SPACE — vary every unprescribed spelling per file, and a variant must itself
pass the repo's gates to count) · allowlist-of-substrings (name declaring
symbols, extract bodies; substrings bless future occurrences) ·
quantified-property-pinned-at-a-site (enumerate the scope's members, assert
within each, floor the enumeration count) · wrong-module scope · empty-capture
coercion · missing build tags.

## Landed gates live in the graph, not the tree

When a plan moves, renames, or deletes any literal, other plans' landed criteria
may grep it — and repo grep cannot find them, because commands live in graph
metadata. Sweep the GRAPH for every moved/renamed/deleted literal and cite
colliding criterion node IDs with an explicit disposition each (re-point, update
a pinned count, or supersede by a named successor).

## Structural criteria are checks, not shell strings

When a criterion asserts a SHAPE in source, author it as a corpus check and have
the criterion name it — see the corpus-check rulebook. Criteria that are not
statically decidable stay commands or manual, labeled honestly.
