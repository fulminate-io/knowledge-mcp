// SPDX-License-Identifier: Apache-2.0

package tools

// param_accounting_account_ruled_ignored.go holds the `account` parameter's
// classification for BOTH client param-accounting registries: one justification
// and one applier, run over each assembled registry by its own init — query's in
// query_arm_registry.go and mutate's in mutate_param_accounting.go.
//
// IT IS ITS OWN FILE, AND NOT A SIBLING OF EITHER ARM TABLE, because what it
// carries is a RULING that spans every arm of two surfaces rather than a read set
// some arms share. Folding it into an arm-table file would put a cross-surface
// statement inside a per-arm group, where the next reader would take it for one
// more cell — and would make the second surface's use of it look like a reach
// into the first surface's table.
//
// IT WAS QUERY-ONLY FOR ONE COMMIT. The first pass fixed the two surfaces the
// reproduction named, `query` and `traverse`, and PINNED mutate's per-arm
// partition as it stood. That pin was wrong: mutate is one of the five surfaces
// the ruling names, so its knowledge-graph arms refusing the parameter was the
// same defect on a third surface rather than a local behavior to preserve. The
// pin was replaced with the assertion, and the applier moved here.

import "maps"

// justifyAccountRuledIgnored is the justification for `account` on EVERY arm of
// BOTH accounted surfaces, and the ONE place that wording is written.
//
// It is not a render param, so it is the third member of the ignored allowlist
// rather than a fourth class: the allowlist admits a param an arm may accept and
// drop WITH a stated reason, and this param's reason is an owner ruling
// (2026-09-09), quoted verbatim — "Keep the `account` parameter on the five tool
// surfaces that still advertise it, accepted and ignored, with the pinned
// wording stating it does nothing; no schema change in this project and no
// follow-on ticket."
//
// WHY IGNORING IT IS NOT THE SILENT-DISCARD DEFECT the rejected class exists to
// close. A dropped `repo` or `name` serves a DIFFERENT GRAPH than the caller
// asked for; there is no graph a dropped `account` could have meant. The
// families it keyed are retired and a collected inventory graph is a registered
// custom type addressed by `name`, so the field selects nothing on any surviving
// family — and the schema's own pinned wording tells the caller exactly that
// before they send it. Refusing a parameter the same schema documents as inert
// is the product contradicting itself, which is the defect this cell closes.
//
// THE SERVER SIDE MATCHES, and neither side is sufficient alone: a query on a
// non-knowledge family reaches the server's own selector gate, which carries no
// arm for the field either (see validateGraphSelector in
// cmd/knowledge-server/internal/tools/tools_graph_routing_selector.go). Fixing
// one gate and not the other leaves the parameter refused on half the families.
const justifyAccountRuledIgnored = "no surviving graph family is keyed by account — the families that were " +
	"are retired and a collected inventory graph is a registered custom type addressed by name; the parameter " +
	"is accepted and ignored on every surface that advertises it, by owner ruling, and each schema's pinned " +
	"wording says so"

// applyAccountRuledIgnored moves `account` out of an arm's rejected set and into
// its deliberately-ignored set, with the justification above.
//
// IT RUNS OVER EACH ASSEMBLED REGISTRY RATHER THAN BEING WRITTEN INTO THE CELLS,
// and that is the point rather than a shortcut. Both tables are authored across
// several sibling files and a NEW arm inherits nothing from its neighbors: a cell
// edited into each of the arms present today is a rule the next arm escapes
// silently on the day it lands, and the escape reads as a rejection of a
// parameter the schema documents as inert. Applied here, the ruling is one
// statement covering every arm either registry will ever hold, and one statement
// is also what keeps the two surfaces from drifting into two answers about one
// parameter. The partition stays intact — the param moves between sets rather
// than landing in none — and
// TestQueryArmRegistry_AccountIsDeliberatelyIgnoredOnEveryArm and
// TestMutateArmRegistry_AccountIsAcceptedAndIgnoredOnEveryArm assert the result
// per arm rather than trusting this loop.
//
// AN ARM THAT CONSUMES THE PARAM IS LEFT ALONE, AND "CONSUMES" HERE MEANS ONE
// SPECIFIC THING: the arm puts the value on the wire GraphSelector and sends it
// to the server, whose validateGraphSelector carries no arm for the field. The
// value reaches a gate that ignores it, no result changes, and CONSUMED is an
// accurate statement about what the client does. That is true of the
// composite-mode arms (correlations, pivot, explain, timeline), of
// engine-dispatch and metadata-stats, and on the mutate surface of
// armNonKnowledgeFallthrough, whose own cell comment says it is "the arm that
// declines to the engine, where the Target is built".
//
// A CONSUMED CELL IS NOT A LICENSE, and the first version of this comment
// claimed it was. It asserted that every consuming arm merely routes the value
// to the server's ignoring gate — and three arms did not: topology read it into
// foundation.Request.Name and handed the analyzer a different graph instance;
// metadata_stats emitted it as a json payload key; pivot and correlations
// stamped it as the GraphInstance of every hydrated row. None of those three
// reached a server gate at all. They were CLIENT reads that changed the answer,
// which is what the ruling forbids, so the reads were removed rather than the
// cells relabeled. The test to write for a NEW consuming arm is therefore a
// RESULT-level pair, not a class assertion: a class says where the value is
// declared to go, and only a result says whether it changed anything.
//
// Moving a genuinely-routing arm into the ignored class would be a false
// statement about what the client does with the value, and double-classifying it
// would break the partition outright.
//
// THE IGNORED MAP IS COPIED, NEVER MUTATED IN PLACE. Several arms take theirs
// from queryRenderIgnored() / queryFieldsIgnored(), and while each call returns a
// fresh map today, an arm that shared one would have every OTHER arm's cell
// rewritten by whichever call landed last. The rejected set is safe to delete
// from: qparams allocates one map per arm.
func applyAccountRuledIgnored(spec armSpec) armSpec {
	if spec.consumed["account"] {
		return spec
	}
	delete(spec.rejected, "account")
	ignored := make(map[string]string, len(spec.deliberatelyIgnored)+1)
	maps.Copy(ignored, spec.deliberatelyIgnored)
	ignored["account"] = justifyAccountRuledIgnored
	spec.deliberatelyIgnored = ignored
	return spec
}
