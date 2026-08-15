#!/usr/bin/env python3
"""Gate the ful1347 seven-language corpus artifact.

Usage: ful1347_check.py <pins|stamps|nonvacuity|collided|split|arms> <artifact.json>

The checks live here rather than inline in a criterion because a criterion
command is stored in the knowledge graph and cannot carry a program; a checked-in
script is re-runnable and reviewable after this plan ships. Every mode prints the
failures it found and exits non-zero, so a passing run is a silent exit 0.
"""
import json
import subprocess
import sys

# THE PINS ARE THE PLAN'S, restated in exactly one place.
PINS = {
    "rust": "3fce3b5bb0236da2df6d99672afb8a719642eca7",
    "cpp": "7ee830d02b623e8ffe0b95d59a74db1e58da04c5",
    "elixir": "52aa2cbd3b9283830ddfaf03173e20350c0b1d22",
    "groovy": "a83f87480e0c0138caa5a7191a053b1e59a64066",
    "java": "94f39958baf7ad51ddf9c70e406ed6b188194daa",
    "kotlin": "5bcbc3bbe90210e8f2064b733db02a16aa66ef35",
    "scala": "1c13619674b9f879a5cffa8879fb731e923a06b8",
}
# NON-VACUITY FLOORS, far under the plan-time measurement so a legitimate
# resolution change cannot false-fail them: (calls_edges, rung firing).
FLOORS = {
    "rust": (1000, 500),
    "cpp": (200, 200),
    "elixir": (1000, 500),
    "groovy": (1000, 500),
    "java": (5000, 2000),
    "kotlin": (1000, 500),
    "scala": (1000, 500),
}


def rows(doc):
    out = {}
    for corpus in doc.get("corpora", []):
        out[corpus.get("primary_language")] = corpus
    return out


def row_for(corpus, lang):
    for r in corpus.get("languages", []):
        if r.get("language") == lang:
            return r
    return None


def check_pins(doc, bad, head=False):
    by = rows(doc)
    if head:
        cur = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
        if doc.get("commit") != cur:
            bad.append("knowledge stamp %s is not HEAD %s" % (doc.get("commit"), cur))
    for lang, sha in sorted(PINS.items()):
        c = by.get(lang)
        if c is None:
            bad.append("%s: corpus entry missing" % lang)
            continue
        if c.get("corpus_commit") != sha:
            bad.append("%s: corpus_commit %s is not the pin" % (lang, c.get("corpus_commit")))
        if not str(c.get("corpus_root", "")).endswith("/" + lang):
            bad.append("%s: corpus_root %s is not the declared root" % (lang, c.get("corpus_root")))
        if not c.get("files_by_language"):
            bad.append("%s: files_by_language is empty" % lang)


def check_nonvacuity(doc, bad):
    """Each gated language participated non-vacuously in its own corpus.

    THE SECOND FLOOR'S SUBJECT IS RUNG FIRING — bound plus ambiguous — rather
    than bound alone, because a corpus that ships its library in two build
    flavors declares every class twice under one package, so a rung that fires
    on every reference finds two equally-valid declarations and binds none of
    them; the ful1347 java corpus fires 3,545 times through its unqualified-
    import rung and binds 9. The floor VALUES are unchanged.
    """
    by = rows(doc)
    for lang, (cf, ff) in sorted(FLOORS.items()):
        c = by.get(lang)
        if c is None:
            bad.append("%s: corpus entry missing" % lang)
            continue
        if c.get("files_by_language", {}).get(lang, 0) <= 0:
            bad.append("%s: zero files of its own language" % lang)
        r = row_for(c, lang)
        if r is None:
            bad.append("%s: no resolution row" % lang)
            continue
        if r.get("calls_edges", 0) < cf:
            bad.append("%s: calls_edges %d < %d" % (lang, r.get("calls_edges", 0), cf))
        # AN ABSENT AMBIGUOUS MAP IS ITS OWN COMPLAINT. Without this leg a row
        # from an unwired census degrades silently to bound-only firing and
        # reports as "below floor", which is the wrong diagnosis for the reader.
        if "ambiguous_by_rule" not in r:
            bad.append("%s: row carries no ambiguous_by_rule map, so firing is unmeasurable" % lang)
            continue
        firing = sum(r.get("bound_by_rule", {}).values()) + sum(r["ambiguous_by_rule"].values())
        if firing < ff:
            bad.append("%s: rung firing %d < %d" % (lang, firing, ff))


def check_collided(doc, bad):
    by = rows(doc)
    for lang in sorted(PINS):
        c = by.get(lang)
        r = row_for(c, lang) if c else None
        if r is None:
            bad.append("%s: no resolution row" % lang)
            continue
        if r.get("bound", 0) <= 0:
            bad.append("%s: bound is zero, so the collision check has no denominator" % lang)
        unexplained = r.get("collided_key_resolutions", 0) - r.get("collided_alias_narrowed", 0)
        if unexplained != 0:
            bad.append("%s: %d collided binds with no deliberate narrowing" % (lang, unexplained))
    # THE KNOWN-POSITIVE CONTROL. rust is the one language measured to produce
    # alias-narrowed binds (274 at plan time); a zero here means the split
    # counter never fired and every zero above is unreadable.
    c = by.get("rust")
    r = row_for(c, "rust") if c else None
    if r is None or r.get("collided_alias_narrowed", 0) <= 0:
        bad.append("control: rust reports no alias-narrowed bind, so the split counter never fired")


def check_arms(doc, bad):
    """POST-c25e9df2 ONLY: the five arm-bearing languages must have moved off zero.

    Unsatisfiable by correct work until the arm-fix ticket merges, which is why
    it is a mode of its own rather than a leg of nonvacuity: it runs in the
    release measurement, after that ticket is in.

    IT COVERS FIVE LANGUAGES RATHER THAN THE RUST AND CPP PAIR IT WAS AUTHORED
    WITH, because c25e9df2 consolidated from two inert arms to six across three
    families — rust, C/C++ and the JVM trio. elixir and groovy register no arm
    at all and are the control below.
    """
    by = rows(doc)
    # ALL FIVE ARM-BEARING GATED LANGUAGES. elixir and groovy register no arm at
    # all and are the control below; the other five each have an arm the
    # path-model fix repairs, so each owes the same two-part proof.
    for lang in ("rust", "cpp", "java", "kotlin", "scala"):
        c = by.get(lang)
        r = row_for(c, lang) if c else None
        if r is None:
            bad.append("%s: no resolution row" % lang)
            continue
        if r.get("binds_entries", 0) <= 0:
            bad.append("%s: binds_entries is zero, so the arm filled nothing" % lang)
        rung = r.get("bound_by_rule", {})
        through_import = rung.get("qualified-import", 0) + rung.get("unqualified-import", 0)
        if through_import <= 0:
            bad.append("%s: zero references bound through an import rung — the arm is still inert" % lang)
    # THE KNOWN-POSITIVE CONTROL that the rung names are spelled as the ladder
    # emits them: a language with no arm at all must still carry the map, and a
    # typo in either rung key above would otherwise read as "still inert".
    for lang in ("elixir", "groovy"):
        c = by.get(lang)
        r = row_for(c, lang) if c else None
        if r is not None and "bound_by_rule" not in r:
            bad.append("control: %s carries no bound_by_rule map, so the rung keys above are unverifiable" % lang)


def check_split(doc, bad):
    by = rows(doc)
    for lang in sorted(PINS):
        c = by.get(lang)
        r = row_for(c, lang) if c else None
        if r is None:
            bad.append("%s: no resolution row" % lang)
            continue
        for key in ("references", "binds_files", "binds_entries", "binds_scopes_unknown", "bound_by_rule"):
            if key not in r:
                bad.append("%s: row is missing %s" % (lang, key))
        if r.get("bound", 0) <= 0:
            bad.append("%s: bound is zero, so the split accounts for nothing" % lang)
        if "bound_by_rule" in r:
            total = sum(r["bound_by_rule"].values())
            if total != r.get("bound", 0):
                bad.append("%s: per-rule bound %d != bound %d" % (lang, total, r.get("bound", 0)))


def main():
    if len(sys.argv) != 3:
        print(__doc__)
        return 2
    mode, path = sys.argv[1], sys.argv[2]
    doc = json.load(open(path))
    bad = []
    if mode == "pins":
        check_pins(doc, bad)
    elif mode == "stamps":
        check_pins(doc, bad, head=True)
    elif mode == "nonvacuity":
        check_nonvacuity(doc, bad)
    elif mode == "collided":
        check_collided(doc, bad)
    elif mode == "split":
        check_split(doc, bad)
    elif mode == "arms":
        check_arms(doc, bad)
    else:
        print("unknown mode %r" % mode)
        return 2
    for b in bad:
        print(b)
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
