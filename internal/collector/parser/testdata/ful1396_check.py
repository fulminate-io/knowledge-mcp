#!/usr/bin/env python3
"""Gate the nominal-static capture arms against the nine-root corpus artifact.

Usage:
  ful1396_check.py r2t <armoff.json> <armon.json>
  ful1396_check.py conformance <armon.json>
  ful1396_check.py pins <armon.json>
  ful1396_check.py sample <sample.json>

It is a SIBLING of ful1347_check.py rather than an extension of it, and it
imports that script's rows/row_for helpers instead of reimplementing them. The
landed script's main rejects any argument count but two, and several landed
criteria invoke it at that arity; the r2t mode below needs TWO artifacts, and
relaxing an arity check that many landed gates depend on is a wider blast radius
than a sibling that reuses the helpers.

Every mode prints the failures it found and exits non-zero, so a passing run is
a silent exit 0 — the same contract the landed script holds to.
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from ful1347_check import rows, row_for  # noqa: E402

# The six nominal-static languages this ticket armed.
ARMED = ("java", "kotlin", "scala", "csharp", "php", "groovy")

# THE TWO ROOTS THIS TICKET ADDED, restated in exactly one place. The other
# seven are pinned by the landed script and are not repeated here.
PINS = {
    "csharp": "645f3131a5b0a4bf677201cf22773990a5316c89",
    "php": "f85216c82cbd38b66d67ebd20ea762cb3751a4b4",
}

# THE RUNGS THAT RUN BEFORE THE TYPED-QUALIFIER ONE. The ladder order is
# qualified-import, then qualified-parent, then typed-qualifier, then
# qualified-path, external-qualifier and dynamic-scope. A change in either rung
# named here means the arm perturbed something upstream of where it sits, which
# no amount of downstream improvement would excuse.
ABOVE_R2T = ("qualified-import", "qualified-parent")

# THE RUNGS BELOW THE TYPED-QUALIFIER ONE. These MAY legitimately lose
# references to it, so their movement is PRINTED rather than asserted — and
# printing it is the point, because that movement is the population a spot audit
# samples from.
BELOW_R2T = ("qualified-path", "external-qualifier", "dynamic-scope")

# GROOVY'S FLOOR IS A GRAMMAR LIMIT, NOT A CAPTURE FAILURE, and the sentence is
# carried in the script rather than only in a plan so that whoever reads a zero
# reads the reason beside it. The vendored groovy grammar declares no
# `implements` token at all, so any corpus file carrying an implements clause
# misparses: the clause is swallowed by an ERROR node while the enclosing class
# still chunks. Groovy also registers no import-binding arm and resolves
# file-scoped, and a supertype resolving to a non-contract emits nothing — so
# the only groovy declaration that can produce an edge is an interface extending
# a single interface. A low or zero groovy figure must NOT be read, reported or
# gated as a capture failure.
GROOVY_LIMIT = (
    "groovy: the vendored grammar has no `implements` token, groovy has no import-binding arm "
    "and resolves file-scoped, and a non-contract supertype emits nothing — so zero is a "
    "legitimate measurement here and is NOT a capture failure"
)


def language_row(doc_rows, lang, bad):
    """The row for a language within its OWN corpus, or None with a complaint."""
    corpus = doc_rows.get(lang)
    if corpus is None:
        bad.append("%s: corpus entry missing" % lang)
        return None
    row = row_for(corpus, lang)
    if row is None:
        bad.append("%s: no resolution row in its own corpus" % lang)
    return row


def check_r2t(off, on, bad):
    """BIND-ONLY: the arm adds a rung and moves nothing above it.

    THE LIVENESS CONTROL IS THE typed-qualifier LEG. Without it every equality
    here is satisfied perfectly by an arm that never ran at all.
    """
    off_rows, on_rows = rows(off), rows(on)
    for lang in ARMED:
        a = language_row(off_rows, lang, bad)
        b = language_row(on_rows, lang, bad)
        if a is None or b is None:
            continue

        if a.get("references") != b.get("references"):
            bad.append("%s: reference population moved, %s -> %s"
                       % (lang, a.get("references"), b.get("references")))

        ra, rb = a.get("bound_by_rule", {}), b.get("bound_by_rule", {})
        for rung in ABOVE_R2T:
            if ra.get(rung, 0) != rb.get(rung, 0):
                bad.append("%s: %s runs BEFORE the typed-qualifier rung and moved, %d -> %d"
                           % (lang, rung, ra.get(rung, 0), rb.get(rung, 0)))

        if "typed-qualifier" in ra:
            bad.append("%s: the arm-off column already carries a typed-qualifier count, so it is "
                       "not a baseline" % lang)
        if rb.get("typed-qualifier", 0) <= 0:
            bad.append("%s: the arm-on column carries no positive typed-qualifier count, so the "
                       "arm never bound anything" % lang)

        if b.get("bound", 0) < a.get("bound", 0):
            bad.append("%s: bound DECREASED, %d -> %d" % (lang, a.get("bound", 0), b.get("bound", 0)))

        for rung in sorted(set(ra) - set(rb)):
            bad.append("%s: rung %s vanished entirely, %d -> absent" % (lang, rung, ra[rung]))

        moved = ["%s %d->%d" % (r, ra.get(r, 0), rb.get(r, 0))
                 for r in BELOW_R2T if ra.get(r, 0) != rb.get(r, 0)]
        print("%s: typed-qualifier %d, bound %d -> %d%s"
              % (lang, rb.get("typed-qualifier", 0), a.get("bound", 0), b.get("bound", 0),
                 (", below-rung movement: " + ", ".join(moved)) if moved else ""))


def check_conformance(on, bad):
    """DECLARED-CONFORMANCE COVERAGE, counted and never re-labelled.

    IT ASSERTS COUNTS AND NEVER AN EDGE VOCABULARY. The Method spelling belongs
    to the emitter, and a copy of it here would be a second definition free to
    drift from the one that produces the edges.
    """
    on_rows = rows(on)
    for lang in ARMED:
        row = language_row(on_rows, lang, bad)
        if row is None:
            continue
        edges = row.get("implements_edges", 0)
        members = row.get("implements_method_edges", 0)
        print("%s: implements_edges %d, implements_method_edges %d" % (lang, edges, members))
        if lang == "groovy":
            print(GROOVY_LIMIT)
            continue
        if edges <= 0:
            bad.append("%s: no declared-conformance type-level relationship at all" % lang)
        if members <= 0:
            bad.append("%s: no declared-conformance member-level relationship at all" % lang)


def check_pins(on, bad):
    """The two roots this ticket added carry their declared commits."""
    by = rows(on)
    for lang, sha in sorted(PINS.items()):
        corpus = by.get(lang)
        if corpus is None:
            bad.append("%s: corpus entry missing" % lang)
            continue
        if corpus.get("corpus_commit") != sha:
            bad.append("%s: corpus_commit %s is not the pin" % (lang, corpus.get("corpus_commit")))
        if not str(corpus.get("corpus_root", "")).endswith("/" + lang):
            bad.append("%s: corpus_root %s is not the declared root" % (lang, corpus.get("corpus_root")))


def check_sample(doc, bad):
    """The spot-audit artifact's SHAPE is complete enough to audit from.

    An audit is only possible if every sampled edge can be opened at BOTH ends
    in the corpus source, so each row owes two node IDs, two paths and two lines
    as well as the Method value being audited.
    """
    langs = doc.get("languages")
    if not isinstance(langs, dict) or not langs:
        bad.append("the artifact carries no per-language sample map")
        return
    for lang in ARMED:
        if lang not in langs:
            bad.append("%s: absent from the sample; a language producing zero is recorded AS zero, "
                       "never omitted" % lang)
            continue
        entry = langs[lang]
        for key in ("sampled", "total", "edges"):
            if key not in entry:
                bad.append("%s: sample entry is missing %s" % (lang, key))
        # A LANGUAGE THAT SAMPLED NOTHING CARRIES A JSON null RATHER THAN AN
        # EMPTY LIST, because an empty Go slice marshals to null — and a zero
        # sample is a legitimate row, so it is read rather than rejected.
        for i, edge in enumerate(entry.get("edges") or []):
            for key in ("from_id", "to_id", "from_file", "to_file", "from_line", "to_line", "method"):
                if not edge.get(key) and edge.get(key) != 0:
                    bad.append("%s edge %d: missing %s" % (lang, i, key))
        print("%s: sampled %s of %s" % (lang, entry.get("sampled"), entry.get("total")))


def main():
    if len(sys.argv) < 3:
        print(__doc__)
        return 2
    mode = sys.argv[1]
    bad = []
    if mode == "r2t":
        if len(sys.argv) != 4:
            print(__doc__)
            return 2
        check_r2t(json.load(open(sys.argv[2])), json.load(open(sys.argv[3])), bad)
    elif len(sys.argv) != 3:
        print(__doc__)
        return 2
    elif mode == "conformance":
        check_conformance(json.load(open(sys.argv[2])), bad)
    elif mode == "pins":
        check_pins(json.load(open(sys.argv[2])), bad)
    elif mode == "sample":
        check_sample(json.load(open(sys.argv[2])), bad)
    else:
        print("unknown mode %r" % mode)
        return 2
    for b in bad:
        print(b)
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
