// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// help_operation_census_test.go — the BOTH-DIRECTIONS census between what an
// operation-dispatched tool ROUTES and what its help topic DOCUMENTS.
//
// WHY IT EXISTS, and why it is anchored the way it is. The forward direction
// (every operation the help names exists) was already covered per-tool: an
// operation the help invents is caught the first time anyone runs the example.
// The INVERSE had no gate at all, and the corpus shows what that costs — a
// census run over help("manage") found SIX operations the dispatch routes and
// the help never mentioned: pprof_start, pprof_stop, drop_graph,
// promote_metadata, repair_edges and set_metadata_overrides. Five of the six had
// been shipping undocumented long enough that finding them required reading the
// dispatch, which is exactly the work the help exists to save.
//
// EACH CENSUS ANCHORS ON THE TOOL'S OWN LIVE LIST, never on a copy written here.
// manageOperations is the slice InterceptManage's terminal arm consults;
// mutateDeclaredOperations is read straight off the published schema;
// thoughtsOperations is interceptThoughtsOp's vocabulary; the query modes come
// off QueryToolDef's enum. So the NEXT operation added to any of them arrives in
// this test's input set for free, and lands red until its help is written —
// which is the whole point. A hand-copied list here would go green on the day it
// drifted, and this file would then be documenting the drift rather than
// catching it.

// operationCallForm is how an operation-dispatched tool's help SHOWS a call:
// the literal `"operation": "<op>"`. The census demands that form rather than a
// bare substring for a specific reason — substring containment cannot tell
// "prune" from "prune-cache", or "status" from "pipeline_status", so a help
// documenting only the longer operation would silently satisfy the shorter one's
// row. The quoted-and-closed form cannot collide that way, and it is a STRONGER
// property besides: it means the reader is shown a real call, not merely a word.
func operationCallForm(op string) string {
	return `"operation": "` + op + `"`
}

// operationsMissingFromHelp returns every operation whose call form does not
// appear in the help text, in the input order. Empty means the help covers the
// whole routed vocabulary.
//
// It is a named helper rather than an inline loop so each census below can drive
// the SAME instrument to a non-zero result on a deliberately holed input — the
// known-positive control every emptiness assertion needs, since a zero from a
// probe pointed at nothing is indistinguishable from a zero earned honestly.
func operationsMissingFromHelp(help string, ops []string) []string {
	var missing []string
	for _, op := range ops {
		if !strings.Contains(help, operationCallForm(op)) {
			missing = append(missing, op)
		}
	}
	return missing
}

// quotedTokensMissingFromHelp is the same census for values the help names as a
// quoted token rather than as an operation call — query's mode enum, which the
// help writes both as `"mode": "stats"` and inline as `mode:"hybrid"`. Matching
// on the quoted token covers both spellings while still refusing the substring
// collisions a bare match would admit.
func quotedTokensMissingFromHelp(help string, tokens []string) []string {
	var missing []string
	for _, tok := range tokens {
		if !strings.Contains(help, `"`+tok+`"`) {
			missing = append(missing, tok)
		}
	}
	return missing
}

// TestHelpManage_DocumentsEveryDispatchedOperation is the census the manage help
// failed. manageOperations is InterceptManage's OWN vocabulary — the list its
// terminal arm consults to decide whether an operation it does not itself
// dispatch belongs to a later claimant — and
// TestInterceptManage_DeclaredOperationsAllKnown already keeps that list
// set-equal to ManageToolDef's published enum. Anchoring here therefore covers
// the schema too, transitively, without this file holding a second copy of
// either.
func TestHelpManage_DocumentsEveryDispatchedOperation(t *testing.T) {
	require.NotEmpty(t, manageOperations, "manageOperations is empty — the census would be vacuous")

	assert.Empty(t, operationsMissingFromHelp(helpManage, manageOperations),
		"help(\"manage\") does not document every operation the manage dispatch routes; "+
			"add a worked `manage({ \"operation\": \"<op>\" ... })` call for each name listed")

	// KNOWN-POSITIVE CONTROL, on the same instrument in the same run. Hole the
	// help by deleting one operation's name and the census must NAME it. Without
	// this, a census whose matcher had been broken into always-matching would
	// report the identical empty slice as a genuinely complete help, and the
	// assertion above would be decoration.
	holed := strings.ReplaceAll(helpManage, "pprof_start", "")
	assert.Equal(t, []string{"pprof_start"}, operationsMissingFromHelp(holed, manageOperations),
		"the census must report exactly the operation removed from the help — "+
			"if it reports none, the matcher cannot fail and neither can this test")
}

// TestHelpMutate_DocumentsEveryDeclaredOperation censuses the mutate help
// against mutateDeclaredOperations, which is read off the live schema
// (mutateProperties()["operation"].Enum) rather than transcribed — so unlike the
// hand-copied lists the other tools keep, there is no second copy to drift.
//
// It found four undocumented at authoring time: create_batch, upsert,
// bulk_update_metadata and delete.
func TestHelpMutate_DocumentsEveryDeclaredOperation(t *testing.T) {
	require.NotEmpty(t, mutateDeclaredOperations, "the mutate schema declares no operations — the census would be vacuous")

	assert.Empty(t, operationsMissingFromHelp(helpMutate, mutateDeclaredOperations),
		"help(\"mutate\") does not document every operation the mutate schema declares")

	holed := strings.ReplaceAll(helpMutate, "create_batch", "")
	assert.Equal(t, []string{"create_batch"}, operationsMissingFromHelp(holed, mutateDeclaredOperations),
		"known-positive: holing the help must make the census name the missing operation")
}

// TestHelpThoughts_DocumentsEveryDispatchedOperation censuses the thoughts help
// against interceptThoughtsOp's own vocabulary. It found adjacency and
// charges_for documented nowhere, though both are routed and both are the
// intended path for bulk cluster/charge reads.
func TestHelpThoughts_DocumentsEveryDispatchedOperation(t *testing.T) {
	require.NotEmpty(t, thoughtsOperations, "thoughtsOperations is empty — the census would be vacuous")

	assert.Empty(t, operationsMissingFromHelp(helpThoughts, thoughtsOperations),
		"help(\"thoughts\") does not document every operation the thoughts dispatch routes")

	holed := strings.ReplaceAll(helpThoughts, "charges_for", "")
	assert.Equal(t, []string{"charges_for"}, operationsMissingFromHelp(holed, thoughtsOperations),
		"known-positive: holing the help must make the census name the missing operation")
}

// TestHelpSync_DocumentsEveryDeclaredOperation censuses the sync help against
// SyncToolDef's published enum. The enum is push/pull/list; the overview topic
// had been advertising a fourth, "promote", which InterceptSync rejects.
func TestHelpSync_DocumentsEveryDeclaredOperation(t *testing.T) {
	ops := SyncToolDef().InputSchema.Properties["operation"].Enum
	require.NotEmpty(t, ops, "the sync schema declares no operations — the census would be vacuous")

	assert.Empty(t, operationsMissingFromHelp(helpSync, ops),
		"help(\"sync\") does not document every operation the sync schema declares")

	// The inverse direction for this tool, which is where its live defect was:
	// no help topic may advertise an operation the schema does not declare.
	for _, topic := range []struct {
		name string
		body string
	}{{"overview", helpOverview}, {"sync", helpSync}} {
		assert.NotContains(t, topic.body, "promote",
			"help(%q) advertises a sync 'promote' operation; InterceptSync rejects it "+
				"and the schema enum is push/pull/list", topic.name)
	}
}

// TestHelpQuery_DocumentsEveryDeclaredMode censuses the query help against
// QueryToolDef's mode enum. It found five modes named nowhere in the topic:
// hybrid, text, evolution, simulate and metadata_stats.
func TestHelpQuery_DocumentsEveryDeclaredMode(t *testing.T) {
	modes := QueryToolDef().InputSchema.Properties["mode"].Enum
	require.NotEmpty(t, modes, "the query schema declares no modes — the census would be vacuous")

	assert.Empty(t, quotedTokensMissingFromHelp(helpQuery, modes),
		"help(\"query\") does not name every mode the query schema declares")

	holed := strings.ReplaceAll(helpQuery, `"metadata_stats"`, "")
	assert.Equal(t, []string{"metadata_stats"}, quotedTokensMissingFromHelp(holed, modes),
		"known-positive: holing the help must make the census name the missing mode")
}

// TestHelpTopics_BijectWithPublishedEnum censuses the topic vocabulary in BOTH
// directions: every topic handleHelpClient serves is offered by the schema an
// LLM reads, and every topic the schema offers actually dispatches.
//
// The second direction is the one that had rotted, one level down: help("help")
// listed a "dreaming" topic that does not exist and three per-operation topics
// (think / charge / recall) that were never topics, while omitting "ast", which
// is. Those are prose rather than the map, so they are asserted separately
// below — but the map/enum bijection is the structural half and belongs here.
func TestHelpTopics_BijectWithPublishedEnum(t *testing.T) {
	enum := HelpToolDef().InputSchema.Properties["topic"].Enum
	require.NotEmpty(t, enum, "the help schema publishes no topic enum — the census would be vacuous")

	declared := make(map[string]bool, len(enum))
	for _, topic := range enum {
		declared[topic] = true
		assert.Contains(t, helpTopics, topic,
			"the help schema offers topic %q but helpTopics does not dispatch it", topic)
	}
	for topic := range helpTopics {
		assert.True(t, declared[topic],
			"helpTopics serves topic %q but the published schema enum does not offer it", topic)
	}

	// Known-positive: the same containment instrument must report a miss when
	// one exists, so an empty failure set above is earned rather than vacuous.
	assert.NotContains(t, helpTopics, "dreaming",
		"control: 'dreaming' must not resolve — it is the topic help(\"help\") used to advertise")
}

// TestHelpHelp_ListsTheRealTopics pins the prose half of the topic census: the
// "Available topics" block a reader copies from. It listed a topic that does not
// exist ("dreaming") and three that never did (think / charge / recall — those
// are thoughts OPERATIONS, all under the one "thoughts" topic), while omitting
// "ast". Each assertion below is one of those four facts.
func TestHelpHelp_ListsTheRealTopics(t *testing.T) {
	for _, phantom := range []string{"dreaming", "think, charge, recall"} {
		assert.NotContains(t, helpHelp, phantom,
			"help(\"help\") lists %q, which is not a dispatchable topic", phantom)
	}
	for _, real := range []string{"ast", "thoughts"} {
		assert.Contains(t, helpHelp, real,
			"help(\"help\") omits the real topic %q", real)
	}
	// Every topic the block claims must actually resolve. This is what turns the
	// prose from a list into a checked one.
	for topic := range helpTopics {
		assert.Contains(t, helpHelp, topic,
			"help(\"help\") omits the dispatchable topic %q from its Available topics block", topic)
	}
}

// TestHelpManageChecks_DocumentsEveryDispatchedOperation is the manage_checks
// sibling of the manage census above, in the same shape and anchored the same
// way: manageChecksOperations is the tool's OWN vocabulary — the list
// InterceptManageChecks consults before dispatching — so an operation added to
// it later arrives in this test's input set for free and lands red until its
// help is written.
func TestHelpManageChecks_DocumentsEveryDispatchedOperation(t *testing.T) {
	require.NotEmpty(t, manageChecksOperations, "manageChecksOperations is empty — the census would be vacuous")

	assert.Empty(t, operationsMissingFromHelp(helpManageChecks, manageChecksOperations),
		"help(\"manage_checks\") does not document every operation the dispatch routes; "+
			"add a worked `manage_checks({ \"operation\": \"<op>\" ... })` call for each name listed")

	// KNOWN-POSITIVE CONTROL, on the same instrument in the same run. Hole the
	// help by deleting one operation's name and the census must NAME it —
	// otherwise a matcher broken into always-matching would report the identical
	// empty slice as a complete help, and the assertion above would be decoration.
	holed := strings.ReplaceAll(helpManageChecks, OpChecksRun, "")
	assert.Equal(t, []string{OpChecksRun}, operationsMissingFromHelp(holed, manageChecksOperations),
		"the census must report exactly the operation removed from the help — "+
			"if it reports none, the matcher cannot fail and neither can this test")
}
