// SPDX-License-Identifier: Apache-2.0

package projects

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDeriveCriterionName pins the criterion-NAME derivation: the description's
// first non-blank line, trimmed.
//
// THE SINGLE-LINE CASES ARE THE COMPATIBILITY HALF and are not filler. Every
// criterion the three derivation sites wrote before the clamp used the whole
// description as the name, so a clamp that altered a single-line derivation
// would silently rename the existing corpus's shape on next touch. They must
// come back byte-identical.
//
// The multi-line cases are the behavior the clamp adds: a description carrying
// the assertion, then its command, then the reasoning yields a name that is the
// assertion alone rather than the whole block.
func TestDeriveCriterionName(t *testing.T) {
	tests := []struct {
		name string
		desc string
		want string
	}{
		{
			name: "single line is returned byte-identical",
			desc: "go build ./... exits 0",
			want: "go build ./... exits 0",
		},
		{
			name: "single line with inner punctuation is untouched",
			desc: "the suite is green: no FAIL lines, exit 0",
			want: "the suite is green: no FAIL lines, exit 0",
		},
		{
			name: "multi-line clamps to the first line",
			desc: "TestFoo passes\n\nRun: go test ./pkg -run TestFoo\nRationale: pins the pair convention.",
			want: "TestFoo passes",
		},
		{
			name: "leading blank lines are skipped",
			desc: "\n\n  the gate rejects a lone verifies edge  \nsecond line",
			want: "the gate rejects a lone verifies edge",
		},
		{
			name: "trailing whitespace on the first line is trimmed",
			desc: "criterion holds   \nmore",
			want: "criterion holds",
		},
		{
			name: "windows line ending does not leak a carriage return",
			desc: "criterion holds\r\nmore",
			want: "criterion holds",
		},
		{
			name: "empty description is returned verbatim (the caller's validator owns it)",
			desc: "",
			want: "",
		},
		{
			name: "whitespace-only description is returned verbatim, not blanked",
			desc: "   \n  ",
			want: "   \n  ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, DeriveCriterionName(tt.desc))
		})
	}
}

// TestBuildCriterionNode_NameClampedDescriptionWhole proves the clamp reaches
// the node BuildCriterionNode actually emits — the create_plan / create_test_plan
// path — and, in the same case, that the DESCRIPTION keeps every line. A clamp
// applied to the wrong field would truncate the criterion's content, which no
// assertion on the name alone would catch.
func TestBuildCriterionNode_NameClampedDescriptionWhole(t *testing.T) {
	const desc = "the census reports zero remaining sites\n\nRun: scripts/census.sh\nWhy: the sweep gate."

	const authored = "the sweep leaves no remaining census sites"

	node := BuildCriterionNode(CriterionArgs{
		Description: desc, Summary: authored, Type: "automated", Command: "scripts/census.sh",
	})

	assert.Equal(t, "the census reports zero remaining sites", node.GetSymbolName(),
		"the node name is the description's first line")
	assert.Equal(t, desc, node.GetDescription(),
		"the description keeps every line — only the NAME is clamped")
	assert.Equal(t, authored, node.GetSummary(),
		"the summary is the caller's, stored verbatim and never derived from the description")
}
