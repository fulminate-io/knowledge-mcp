---
name: run-a-smoke-test
description: Action rulebook for live smoke verification — the minimum battery, lifecycle probes with deterministic identifiers, probe eligibility, adversarial controls, confound elimination, and the AMBER result. Read before any live-behavior claim. Not user-invocable.
user-invocable: false
---

# RUN-A-SMOKE-TEST — exercising the running system the way a caller does

<!-- version: 1 -->
<!-- Read at: any live-behavior claim (implementer), before offering retro
     (orchestrator), and test review. Catches the defect classes invisible in a
     green build — silent acceptance, proxy divergence, lifecycle windows. -->

Runs after any rebuild or restart of a serving component, after a batch of
changes lands, during lulls, and after ANY fix whose claim is about live
behavior, regardless of unit-test color. The concrete operations are whatever
the system under test serves; the protocol is the shape.

**Minimum battery (post-rebuild):**
1. Read paths: exercise each primary read surface once against content whose
   expected result is known in advance.
2. Write round-trip: create a record through the public write path, then read
   it back BY ITS IDENTIFIER through a separate read path and confirm the
   specific field written — the write's success response is not the readback.
3. Processing observation: confirm in the serving component's own output (logs,
   metrics, traces) that the stages you expect actually fired for the operation
   you just performed. An absent stage where sibling stages are present is a
   finding; absence where NOTHING was recorded is a broken probe.
4. Record what each step is a proxy FOR. A read returning your record proves
   the serving state contains it — not that every processing stage ran;
   retrieval and derivation are different layers.

**Deterministic-identifier lifecycle probes:** create → verify → delete →
verify → recreate → verify, with a caller-supplied stable identifier so every
phase is addressable. Each verify goes through BOTH the derived/indexed read
path AND the direct by-identifier path — they can disagree, and their
disagreement is the signal. The recreate phase is what finds lifecycle defects;
skipping it reduces the probe to a happy-path smoke.

**Probe eligibility comes first:** the probe record's type or category must be
eligible for the derived surface being asserted against — an excluded category
makes every phase vacuous while producing plausible output. Establish
eligibility with a known-positive of the same category BEFORE the probe, and
state the check in the result.

**Adversarial controls:** for any check passing on a zero, an absence, or an
equality — inject the failure, observe red, revert, observe green. A check
never seen red has unknown discriminating power, including the battery itself.

**Baseline-vs-treatment asymmetry is the signal:** run the same probe through
two paths that SHOULD behave alike and compare. A fresh-create completing in
seconds against a recreate that never completes localizes the defect to what
differs; one number alone cannot distinguish slow from never. Record both.

**Confound elimination before classification**, in order, one command each: is
the running binary/build the one you think (inspect it for the change under
test); does the record exist at all (fetch by identifier); has enough time
passed for the mechanism you await (interval derived FROM SOURCE, not memory);
are you observing the right instance, environment, or build flavor.

**Preserved live fixtures:** when a probe reproduces a defect whose fix is
queued, LEAVE THE FIXTURE IN PLACE and name it as that fix's acceptance
criterion — the only artifact proving the fix works against the state that
actually occurred.

**AMBER is the third result:** verified at the storage level, NOT verified at
the serving level. Neither green nor red; its only valid disposition is
reporting the observed asymmetry for investigation. Declaring green on
storage-level evidence is the failure this result exists to prevent; declaring
red without the confound elimination above is the mirror failure.
