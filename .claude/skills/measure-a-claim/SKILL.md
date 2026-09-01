---
name: measure-a-claim
description: Action rulebook for causal and optimization claims — ground truth over narrative, investigation briefs carry instruments not mechanisms, measured-proposals-only with instrument anchor and variant×scale matrix. Read before relaying any causal claim or optimization proposal. Not user-invocable.
user-invocable: false
---

# MEASURE-A-CLAIM — ground truth for causes and optimization proposals

<!-- version: 1 -->
<!-- Read at: a defect/incident or optimization topic entering a brainstorm; before
     writing an investigation or optimization brief; before any causal claim
     reaches a user, ticket, or mitigation plan. -->

## Causal claims need ground truth

Ground truth and reproduction are the only things that prove a cause. A root
cause is an OBSERVED mechanism: reproduced under instrumentation, or watched at
the layer where the cause lives. A correlation fitted to logs one layer removed
is a LEAD; a story that predicts the data is a HYPOTHESIS; neither is a cause no
matter how many analysis rounds agree.

- BRIEF DISCIPLINE: investigation briefs carry MEASURED FACTS and INSTRUMENTS,
  never candidate mechanisms — a hypothesis menu poisons an investigation
  exactly as a named solution poisons a design brief. Hold a theory? Hand it to
  a SEPARATE test designed to falsify it.
- RELAY-AND-BUILD GATE: before a causal claim reaches a user, a ticket, or a
  mitigation plan — was the mechanism OBSERVED or inferred? Inferred → relay as
  "unproven lead"; no plan freezes on it unless the user explicitly accepts the
  trade. Label provenance in every artifact: measured / reproduced / story.
  Non-reproduction under instrumentation is a real result, reported as exactly
  that.
- THE TELL: successive investigation rounds each producing a new mechanism that
  fits the same one-layer-removed logs, each collapsing under the next closer
  measurement — curve-fitting. Stop, instrument, reproduce.

## Measured proposals only

Optimization asks ("could X be cheaper / faster / smaller?") get the same bar.
Theory does not answer them — a baseless proposal is worse than no proposal: it
spends trust and directs real work toward an unverified mechanism. No
optimization proposal reaches the user or enters a ticket unless its central
claim was MEASURED on the real system. The method, at minimum:

1. INSTRUMENT ANCHOR: reproduce the observed baseline cost at real scale before
   comparing anything. A harness that cannot reproduce the baseline is broken;
   fix it before it ranks variants.
2. A VARIANT × SCALE MATRIX actually executed — the current shape as the
   control row, two or more scales so the growth curve is visible, the
   equivalence bar stated precisely (what must remain identical for a variant
   to count as the same work).
3. ONE-TIME vs RECURRING split: which cost is paid once and which recurs —
   measured, not asserted.
4. Anything unmeasurable without code changes is reported as UNMEASURED — with
   what it would take to measure — never as a result.

The tell: a proposal whose evidence is reasoning instead of numbers, or a
ranking of variants none of which was run.
