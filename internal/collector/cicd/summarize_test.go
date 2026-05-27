// SPDX-License-Identifier: Apache-2.0

package cicd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSummarize_Fallback_NoRegion(t *testing.T) {
	resetForTesting()
	t.Cleanup(resetForTesting)

	got := Summarize(ResourceSpec{
		ResourceType: "made-up-type",
		Name:         "thing",
	})
	assert.Equal(t, "made-up-type thing", got)
}

func TestSummarize_EmptyResourceType_SubstitutesUnknown(t *testing.T) {
	resetForTesting()
	t.Cleanup(resetForTesting)

	got := Summarize(ResourceSpec{Name: "stuff"})
	assert.Equal(t, "<unknown> stuff", got)
}

func TestSummarize_AllEmpty_TrimsToUnknown(t *testing.T) {
	resetForTesting()
	t.Cleanup(resetForTesting)

	got := Summarize(ResourceSpec{})
	assert.Equal(t, "<unknown>", got)
}

func TestSummarize_RegisteredHelper_Wins(t *testing.T) {
	resetForTesting()
	t.Cleanup(resetForTesting)

	Register("", "example-rt", func(spec ResourceSpec) string {
		return "EXAMPLE " + spec.Name
	})

	got := Summarize(ResourceSpec{ResourceType: "example-rt", Name: "x"})
	assert.Equal(t, "EXAMPLE x", got)
}

func TestSummarize_HelperReturnsEmpty_FallsBack(t *testing.T) {
	resetForTesting()
	t.Cleanup(resetForTesting)

	Register("", "empty-rt", func(spec ResourceSpec) string { return "" })

	got := Summarize(ResourceSpec{ResourceType: "empty-rt", Name: "x"})
	assert.Equal(t, "empty-rt x", got)
}

func TestSummarize_TruncatesAt500Bytes(t *testing.T) {
	resetForTesting()
	t.Cleanup(resetForTesting)

	long := strings.Repeat("a", 1000)
	Register("", "long-rt", func(spec ResourceSpec) string { return long })

	got := Summarize(ResourceSpec{ResourceType: "long-rt", Name: "x"})
	assert.Len(t, got, summaryMaxLen)
	assert.Equal(t, strings.Repeat("a", 500), got)
}

func TestRegister_PanicsOnDuplicate(t *testing.T) {
	resetForTesting()
	t.Cleanup(resetForTesting)

	Register("", "dup-rt", func(spec ResourceSpec) string { return "first" })
	require.Panics(t, func() {
		Register("", "dup-rt", func(spec ResourceSpec) string { return "second" })
	})
}

func TestBuildNode_PopulatesSummary(t *testing.T) {
	resetForTesting()
	t.Cleanup(resetForTesting)

	Register("github", "test-rt", func(spec ResourceSpec) string {
		return "test " + spec.Name + " summary"
	})

	n := BuildNode(ResourceSpec{
		ID:           "id-1",
		Name:         "myres",
		ResourceType: "test-rt",
		Provider:     "github",
	})
	assert.Equal(t, "test myres summary", n.Summary)
}

func TestBuildNode_PopulatesSummary_FallbackWhenUnregistered(t *testing.T) {
	resetForTesting()
	t.Cleanup(resetForTesting)

	n := BuildNode(ResourceSpec{
		ID:           "id-1",
		Name:         "anon",
		ResourceType: "unregistered-rt",
		Provider:     "github",
	})
	assert.Equal(t, "unregistered-rt anon", n.Summary)
}
