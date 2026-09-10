// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHelpAnalyzeUsage_TeachesTheSelectorForms is requirement 4's help half: the topic must
// teach the thing the schema line has no room for — that a lane is selected by name or by id,
// what a name is resolved against, and that an ambiguous name is refused rather than picked.
//
// Its known-positive control is at the bottom: an unregistered topic must NOT serve this
// content, so the assertions above are about this topic rather than about a dispatch that
// returns something for anything.
func TestHelpAnalyzeUsage_TeachesTheSelectorForms(t *testing.T) {
	res := handleHelpClient(json.RawMessage(`{"topic":"analyze_usage"}`))
	require.NotEmpty(t, res.Content, "the topic dispatches to content")
	body := res.Content[0].Text
	require.False(t, res.IsError, body)

	for _, want := range []string{
		"analyze_usage",
		"agent",
		"the name the lane was spawned under",
		"a<name>-",
		"resolved within session when session is given",
		"ambiguous",
		"--seed",
	} {
		assert.Contains(t, body, want, "the topic teaches %q", want)
	}

	missing := handleHelpClient(json.RawMessage(`{"topic":"no_such_topic"}`))
	assert.NotContains(t, missing.Content[0].Text, "the name the lane was spawned under",
		"control: an unknown topic must not serve this content")
}

// TestHelpAnalyzeUsage_IsDiscoverableFromTheSchema asserts the topic is on the help tool's
// published enum. A registered-but-unadvertised topic is reachable only by guessing, which
// for a tool whose whole job is teaching is the same as not existing.
func TestHelpAnalyzeUsage_IsDiscoverableFromTheSchema(t *testing.T) {
	topic, ok := HelpToolDef().InputSchema.Properties["topic"]
	require.True(t, ok, "the help tool must declare a topic param")
	assert.Contains(t, topic.Enum, "analyze_usage",
		"the topic must be on the published enum, not merely registered in the map")
	assert.Contains(t, topic.Description, "analyze_usage",
		"and in the prose topic list the property description carries")
}
