// SPDX-License-Identifier: Apache-2.0

package tools

// help_content_manage_checks_test.go pins the manage_checks help topic: that it
// resolves at all, and that it teaches each pinned where-tree trap individually.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHelpManageChecks_TeachesEveryPinnedWhereTreeTrap asserts the topic resolves
// through the real help dispatch and teaches all seven traps.
//
// EACH TRAP IS ASSERTED SEPARATELY BY ITS OWN DISTINGUISHING PHRASE, never as a
// count of seven: a topic that repeated one trap seven times would satisfy a
// count while teaching one thing. The phrases are chosen to be the part a reader
// hitting the trap would actually search for.
func TestHelpManageChecks_TeachesEveryPinnedWhereTreeTrap(t *testing.T) {
	// Resolved through the dispatch rather than read off the constant, so a topic
	// that exists in the file but was never registered fails here.
	res := handleHelpClient(json.RawMessage(`{"topic":"manage_checks"}`))
	require.False(t, res.IsError, "the manage_checks topic must resolve: %s", res.Content[0].Text)
	body := res.Content[0].Text
	require.NotEmpty(t, body)

	for _, trap := range []struct {
		name   string
		phrase string
	}{
		{"$match binds to the nearest pattern scope", `"$match" IN A NESTED WHERE BINDS TO THE NEAREST PATTERN SCOPE`},
		{"an as binding is invisible in its own leaf", "INVISIBLE INSIDE ITS OWN LEAF'S NESTED WHERE"},
		{"sub-pattern captures do not escape to siblings", "SUB-PATTERN CAPTURES NEVER ESCAPE TO SIBLING LEAVES"},
		{"flows_to refuses a sequence capture as from", "REFUSES A SEQUENCE CAPTURE AS ITS 'from'"},
		{"shared-type parameter grammar defeats position anchoring", "SHARED-TYPE PARAMETER GRAMMAR MAKES PARAMETER-POSITION ANCHORING"},
		{"contains_pattern with as does not backtrack", "BINDS THE FIRST MATCHING DESCENDANT AND DOES NOT"},
		{"the flow engine propagates through call results", "PROPAGATES CONSERVATIVELY THROUGH CALL RESULTS"},
	} {
		t.Run(trap.name, func(t *testing.T) {
			assert.Contains(t, body, trap.phrase,
				"the topic must teach this trap; it cost a debugging round and is documented nowhere else in the shipped surface")
		})
	}

	// A TRAP STATED WITHOUT ITS SYMPTOM IS NOT FINDABLE by the person who needs
	// it — they are searching for what they SAW, not for the rule they do not yet
	// know. Seven rules means seven symptoms.
	assert.GreaterOrEqual(t, strings.Count(body, "Symptom:"), 7,
		"each trap must carry the symptom an author sees when they hit it")

	// The folding idiom and the two-slot contract, which are the other half of
	// the phase and are documented nowhere else either.
	assert.Contains(t, body, "additional FUNCTIONS inside the bound good fixture")
	assert.Contains(t, body, "A FIXTURE PAIR PROVES ONLY THE AXES IT VARIES")

	// The contract itself is CITED rather than restated, so the two copies cannot
	// drift into two different pieces of advice.
	assert.Contains(t, body, `help("patterns")`,
		"the check contract lives in the patterns topic and must be cited, not duplicated here")

	// KNOWN-POSITIVE CONTROL on the instrument: an unregistered topic does NOT
	// resolve, so the assertions above are about this topic rather than about a
	// dispatch that returns something for anything.
	missing := handleHelpClient(json.RawMessage(`{"topic":"no_such_topic"}`))
	assert.NotContains(t, missing.Content[0].Text, "manage_checks — author, inventory and run",
		"an unknown topic must not serve this content")
}

// TestHelpManageChecks_IsDiscoverableFromTheSchema asserts the topic is on the
// help tool's published enum.
//
// A REGISTERED-BUT-UNADVERTISED TOPIC IS REACHABLE ONLY BY GUESSING, which for a
// tool whose whole job is teaching is the same as not existing.
func TestHelpManageChecks_IsDiscoverableFromTheSchema(t *testing.T) {
	topic, ok := HelpToolDef().InputSchema.Properties["topic"]
	require.True(t, ok, "the help tool must declare a topic param")
	assert.Contains(t, topic.Enum, "manage_checks",
		"the topic must be on the published enum, not merely registered in the map")

	// Every enum entry must actually resolve — the inverse drift, an advertised
	// topic with no content behind it.
	for _, name := range topic.Enum {
		_, registered := helpTopics[name]
		assert.True(t, registered, "help topic %q is advertised but has no registered content", name)
	}
}

// TestHelpManageChecks_TeachesTheCriterionCommandForm asserts the topic teaches
// the shell form a plan criterion uses, and all three exit codes.
//
// EACH CODE IS ASSERTED SEPARATELY WITH ITS MEANING. A topic that mentioned only
// the flagged code would satisfy a bare "the codes are documented" check while
// leaving the distinction that matters — 4 means the run could not answer, not
// that the corpus is clean — undocumented.
func TestHelpManageChecks_TeachesTheCriterionCommandForm(t *testing.T) {
	body := helpManageChecks

	for _, code := range []struct {
		name    string
		phrase  string
		meaning string
	}{
		{"clean", "exit 0", "CLEAN"},
		{"flagged", "exit 3", "FLAGGED"},
		{"inconclusive", "exit 4", "INCONCLUSIVE"},
	} {
		t.Run(code.name, func(t *testing.T) {
			assert.Contains(t, body, code.phrase, "the topic must name the exit code itself")
			assert.Contains(t, body, code.meaning, "the topic must name what the code means")
		})
	}

	// The distinction the two non-zero codes exist for, stated rather than left
	// to a reader to infer from two numbers.
	assert.Contains(t, body, "IT DOES NOT MEAN THE CORPUS IS CLEAN",
		"a criterion author reading 4 as a clean corpus is the exact confusion the separate code prevents")

	// THE COMMAND FORM RESOLVES ITS OWN ROOT. A criterion that hardcoded a
	// checkout path measures the wrong tree the moment implementation happens in
	// a worktree, which is the normal case here.
	assert.Contains(t, body, `git rev-parse --show-toplevel`,
		"the criterion example must resolve its own repository root")
	assert.Contains(t, body, "knowledge check run --repo",
		"the topic must give the copy-pasteable shell form")

	// KNOWN-POSITIVE CONTROL for the exit-code scan: a code the surface does NOT
	// use must be absent, so the three assertions above are reading real content
	// rather than matching any two-character string.
	assert.NotContains(t, body, "exit 5",
		"control: the topic must not document a code the subcommand cannot return")
}
