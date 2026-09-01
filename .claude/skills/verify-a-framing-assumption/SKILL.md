---
name: verify-a-framing-assumption
description: Action rulebook for framing assumptions — the assumption ledger, existence-claim burden of proof, and the absence protocol. Read before spawning any researcher on a new topic. Not user-invocable.
user-invocable: false
---

# VERIFY-A-FRAMING-ASSUMPTION — wrong premises are never caught downstream

<!-- version: 1 -->
<!-- Read at: brainstorm Step 0.7 (before any researcher spawns); any researcher
     about to make an absence claim; proposal scans. -->

The exploration phase is the ONLY phase where the frame is still mutable: the
researcher describes, the planner locks, the implementer executes — none
challenges the premise. A wrong assumption formed here is never caught
downstream; it is faithfully ELABORATED.

## The assumption ledger

1. Before spawning any researcher, write the ledger via `think()`: every
   load-bearing assumption the framing rests on, each marked UNVERIFIED.
2. Flag EXISTENCE claims explicitly — "we need to build X", "there is no
   existing Y", "this is greenfield" — the class that, if wrong, reinvents what
   exists.
3. Default existence to "it ALREADY exists / the platform ALREADY handles this"
   until disproven; the burden of proof is on absence.
4. Do NOT freeze a ticket while any load-bearing assumption is UNVERIFIED — an
   unverified existence claim is a hard gate, not a footnote.

## The absence protocol

A "does not exist" conclusion requires: ≥2 different semantic `search`
phrasings + an `ast` shape-match (the thing may be named nothing you'd guess) +
repo-wide grep for plausible spellings + a look in the other
flavor/package/build-tag. A single miss is never proof of absence — it usually
means the real thing is named or shaped differently. The classic poisoning:
"there's no existing X, so we build it" enters a brief unverified while the
platform provides X under a name nobody searched.

## Signposts and the highest-risk claim

A researcher's return — claims, file:line citations, "exists / already built /
still there" — is a SIGNPOST, and the researcher may itself have trusted a
signpost. Before a load-bearing claim enters a ticket or reaches the user as
fact, VERIFY it against CURRENT source. "X already exists" is the highest-risk
claim — a reuse target the ticket assumes but that doesn't exist sends
everything downstream building on a fiction. Confirm it in the code, or write
it into the ticket as explicitly unverified.
