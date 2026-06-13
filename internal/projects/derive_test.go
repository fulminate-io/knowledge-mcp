// SPDX-License-Identifier: Apache-2.0

package projects

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDeriveCriterionSummary pins the criterion-summary derivation to the
// EXACT historical inline expressions. The "want" strings are written out as
// literal concatenations matching the pre-extraction code at builders.go and
// intercept_add_criterion.go so any drift in the derive function fails here.
func TestDeriveCriterionSummary(t *testing.T) {
	tests := []struct {
		name    string
		cType   string
		desc    string
		command string
		want    string
	}{
		{
			name:    "no command branch",
			cType:   "manual",
			desc:    "the suite is green",
			command: "",
			want:    "manual" + " criterion: " + "the suite is green",
		},
		{
			name:    "with command branch",
			cType:   "automated",
			desc:    "go build passes",
			command: "go build ./...",
			want:    "automated" + " criterion: " + "go build passes" + " (" + "go build ./..." + ")",
		},
		{
			name:    "empty command is treated as no-command",
			cType:   "manual",
			desc:    "reviewer signs off",
			command: "",
			want:    "manual criterion: reviewer signs off",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, DeriveCriterionSummary(tt.cType, tt.desc, tt.command))
		})
	}
}

// TestDeriveFindingSummary pins the finding-summary derivation to the EXACT
// historical inline expression at buildFindingNode (intercept_mutate_create.go).
func TestDeriveFindingSummary(t *testing.T) {
	tests := []struct {
		name        string
		description string
		evidence    string
		want        string
	}{
		{
			name:        "empty evidence branch",
			description: "the cache is never invalidated",
			evidence:    "",
			want:        "the cache is never invalidated",
		},
		{
			name:        "with evidence branch",
			description: "the cache is never invalidated",
			evidence:    "see store.go:42",
			want:        "the cache is never invalidated" + ". Evidence: " + "see store.go:42",
		},
		{
			name:        "empty evidence yields no evidence suffix",
			description: "leak in handler",
			evidence:    "",
			want:        "leak in handler",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, DeriveFindingSummary(tt.description, tt.evidence))
		})
	}
}

// TestDeriveRuleSummary pins the rule-summary derivation to the EXACT historical
// inline expression at handleClientMutateCreateRule (intercept_mutate_create.go).
func TestDeriveRuleSummary(t *testing.T) {
	tests := []struct {
		name  string
		rule  string
		scope string
		want  string
	}{
		{
			name:  "empty scope branch",
			rule:  "no naked goroutines",
			scope: "",
			want:  "Rule: " + "no naked goroutines",
		},
		{
			name:  "with scope branch",
			rule:  "no naked goroutines",
			scope: "*.go",
			want:  "Rule: " + "no naked goroutines" + " (scope: " + "*.go" + ")",
		},
		{
			name:  "empty scope yields no scope suffix",
			rule:  "always wrap errors",
			scope: "",
			want:  "Rule: always wrap errors",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, DeriveRuleSummary(tt.rule, tt.scope))
		})
	}
}

// TestDeriveQuestionSummary pins the question-summary derivation to the EXACT
// historical inline expressions at builders.go (BuildPlanGraph open_questions)
// and intercept_create_research.go (buildResearchGraph).
func TestDeriveQuestionSummary(t *testing.T) {
	tests := []struct {
		name     string
		question string
		context  string
		want     string
	}{
		{
			name:     "empty context branch",
			question: "Should we cache the result?",
			context:  "",
			want:     "Question: " + "Should we cache the result?",
		},
		{
			name:     "with context branch",
			question: "Should we cache the result?",
			context:  "Hot path, called per request",
			want:     "Question: " + "Should we cache the result?" + ". Context: " + "Hot path, called per request",
		},
		{
			name:     "empty context yields no context suffix",
			question: "Why is this slow?",
			context:  "",
			want:     "Question: Why is this slow?",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, DeriveQuestionSummary(tt.question, tt.context))
		})
	}
}
