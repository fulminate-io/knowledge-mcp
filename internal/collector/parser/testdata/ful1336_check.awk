# ful1336 acceptance gate over the corpus artifacts.
#
# Run over BOTH files, ARM-ENABLED FIRST and the baseline SECOND:
#
#   awk -f ful1336_check.awk ful1334_corpus_verification.txt ful1337_corpus_arm_off.txt
#
# The two files are a MATCHED PAIR generated from one corpus root. Per-file
# accumulation keys on FNR==1 rather than on file names, so the argument ORDER
# is what assigns arm-enabled versus baseline — which is why the order is
# stated above and asserted below by a shape the reader can check.
#
# WHAT IS GATED AND WHAT IS ONLY RECORDED:
#   gated    — go_binds_files, go_binds_entries, go_binds_scopes_unknown,
#              go_bound_rule_qualified_import (the four controls), plus the
#              three transitions go_bound, go_uses_type_bound (must rise) and
#              go_dynamic_groups (must fall).
#   recorded — go_external, go_r2x_terminations, go_dot_scope_files and
#              go_ambiguous_group_listing: presence and parse only.
#
# go_external IS EXPLICITLY UNGATED IN BOTH DIRECTIONS. The external-qualifier
# rung converts references that previously became WRONG dynamic edges into
# honest terminations, so it can legitimately RISE — a gate demanding it fall
# would be demanding the defect back. go_r2x_terminations is ungated for the
# same class of reason: a zero is not automatically wrong, because the fix may
# terminate references before any local collision arises.

function fail(msg) {
	printf("ful1336 GATE FAILED: %s\n", msg) > "/dev/stderr"
	failed = 1
}

# numeric() returns the value of key in the given file's row set, failing loudly
# when the row is ABSENT or does not parse. A MISSING ROW IS A FAILURE, NEVER A
# ZERO: an instrument that stopped emitting a row and one that measured nothing
# are different facts, and defaulting the first to zero would silently report it
# as the second.
function numeric(store, key, which,   raw) {
	if (!((which SUBSEP key) in store)) {
		fail(sprintf("row %s is absent from the %s artifact", key, which))
		return "ABSENT"
	}
	raw = store[which SUBSEP key]
	if (raw !~ /^-?[0-9]+$/) {
		fail(sprintf("row %s in the %s artifact does not parse as a number: %s", key, which, raw))
		return "ABSENT"
	}
	return raw + 0
}

BEGIN {
	FS = "="
	failed = 0
	fileIndex = 0

	# Every row that must be PRESENT AND PARSE in both artifacts.
	n = split("go_references go_bound go_external go_ambiguous_groups " \
		"go_dynamic_groups go_dynamic_unbound go_bound_rule_qualified_import " \
		"go_bound_rule_unqualified_import go_bound_rule_dot_scope " \
		"go_uses_type_bound go_uses_type_total go_binds_files go_binds_entries " \
		"go_binds_scopes_unknown go_r2x_terminations go_dot_scope_files " \
		"go_ambiguous_group_listing", required, " ")
}

FNR == 1 {
	fileIndex++
	if (fileIndex == 1) { which = "arm-enabled" } else { which = "baseline" }
	seenFiles[which] = FILENAME
}

# Rows are key=value per line; comments and blanks are skipped. A value may
# itself contain "=" only in the listing rows, which are not read numerically,
# so the first field is always the key.
/^[a-z_0-9]+=/ {
	key = $1
	value = substr($0, index($0, "=") + 1)
	rows[which SUBSEP key] = value
}

END {
	if (fileIndex != 2) {
		fail(sprintf("expected exactly 2 artifacts (arm-enabled then baseline), got %d", fileIndex))
		exit 1
	}

	# THE MATCHED-PAIR CHECK. Both artifacts must come from ONE corpus root, and
	# files_discovered is the cheapest fingerprint of that: a pair generated from
	# two different roots would disagree here, and every comparison below would
	# then be comparing two different repositories.
	armFiles = numeric(rows, "files_discovered", "arm-enabled")
	baseFiles = numeric(rows, "files_discovered", "baseline")
	if (armFiles != "ABSENT" && baseFiles != "ABSENT" && armFiles != baseFiles) {
		fail(sprintf("the two artifacts disagree on files_discovered (%d vs %d): they were not generated from one corpus root",
			armFiles, baseFiles))
	}

	# PRESENCE AND PARSE FOR EVERY ROW, IN BOTH FILES, BEFORE ANY COMPARISON.
	for (i = 1; i <= n; i++) {
		numeric(rows, required[i], "arm-enabled")
		numeric(rows, required[i], "baseline")
	}
	if (failed) { exit 1 }

	# THE FOUR CONTROLS. Each distinguishes "the measurement is live" from "the
	# instrument reported a structural zero", and without them a flat result
	# reads as "no improvement" rather than "the arm never ran".
	bindsFiles = numeric(rows, "go_binds_files", "arm-enabled")
	if (bindsFiles <= 0) {
		fail("go_binds_files is 0 — no Go file got a non-empty Binds map, so the arm never ran (module path missing, or the pass did not run)")
	}
	bindsEntries = numeric(rows, "go_binds_entries", "arm-enabled")
	if (bindsEntries <= 0) {
		fail("go_binds_entries is 0 — the arm recorded no bind at all")
	}
	unknown = numeric(rows, "go_binds_scopes_unknown", "arm-enabled")
	if (unknown <= 0) {
		fail("go_binds_scopes_unknown is 0 — impossible on a corpus that imports stdlib nearly everywhere, so the binds/scope-set join is not being computed")
	}
	qualified = numeric(rows, "go_bound_rule_qualified_import", "arm-enabled")
	if (qualified <= 0) {
		fail("go_bound_rule_qualified_import is 0 — this row is the headline: it is structurally zero before the arm exists and must be positive after")
	}

	# THE THREE GATED TRANSITIONS, each a direct consequence of qualified
	# references reaching the qualified-import rung instead of the dynamic one.
	armBound = numeric(rows, "go_bound", "arm-enabled")
	baseBound = numeric(rows, "go_bound", "baseline")
	if (armBound <= baseBound) {
		fail(sprintf("go_bound did not rise: arm-enabled %d, baseline %d", armBound, baseBound))
	}

	armUses = numeric(rows, "go_uses_type_bound", "arm-enabled")
	baseUses = numeric(rows, "go_uses_type_bound", "baseline")
	if (armUses <= baseUses) {
		fail(sprintf("go_uses_type_bound did not rise: arm-enabled %d, baseline %d", armUses, baseUses))
	}

	armDyn = numeric(rows, "go_dynamic_groups", "arm-enabled")
	baseDyn = numeric(rows, "go_dynamic_groups", "baseline")
	if (armDyn >= baseDyn) {
		fail(sprintf("go_dynamic_groups did not fall: arm-enabled %d, baseline %d", armDyn, baseDyn))
	}

	if (failed) { exit 1 }
	printf("ful1336 gate PASSED: binds_files=%d binds_entries=%d scopes_unknown=%d qualified_import=%d bound %d>%d uses_type_bound %d>%d dynamic_groups %d<%d\n",
		bindsFiles, bindsEntries, unknown, qualified, armBound, baseBound, armUses, baseUses, armDyn, baseDyn)
}
