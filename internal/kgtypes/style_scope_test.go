// SPDX-License-Identifier: Apache-2.0

// style_scope_test.go — the shared scope vocabulary, held to its refusals.
//
// THE REFUSAL TABLE IS THE POINT OF THESE ROWS. A decode that returned an empty
// scope on a value it could not read would mean "applies everywhere", which is
// the widest possible answer to a question the data could not answer — and on
// the check side that reading runs a subtree's rule over a whole tree. Each row
// below is a value that must not be resolved into a wider scope.

package kgtypes

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStyleScopeFromMetadata_AbsentKeysMeanEverywhere pins the default, which is
// the majority case: a node declaring no scope is not narrowed by anything.
func TestStyleScopeFromMetadata_AbsentKeysMeanEverywhere(t *testing.T) {
	sc, err := StyleScopeFromMetadata(map[string]string{})
	require.NoError(t, err)
	assert.False(t, sc.RepoSet, "an absent repo key is not an empty repo scope")
	assert.False(t, sc.PathsSet, "an absent paths key is not an empty path scope")
	assert.False(t, sc.Scoped(), "a node declaring neither key narrows nothing")

	// THE SET/UNSET PAIR IS NOT DERIVABLE FROM THE VALUES, which is why both
	// booleans exist: an empty repo scope is a value somebody wrote, and it is
	// distinguishable from never having written one.
	empty, err := StyleScopeFromMetadata(map[string]string{MetaKeyStyleScopeRepo: ""})
	require.NoError(t, err)
	assert.True(t, empty.RepoSet, "a present-but-empty repo key is SET, and its refusal is a separate question")
	assert.True(t, empty.Scoped())
}

// TestStyleScopeFromMetadata_RoundTripsEveryByteAPathCanHold is the encoding's
// own justification: the array carries the bytes a separated list would split.
func TestStyleScopeFromMetadata_RoundTripsEveryByteAPathCanHold(t *testing.T) {
	in := []string{"cmd/knowledge", "a,b/with-comma.go", "we\nird/newline.go"}
	encoded, err := StyleScopePathsEncode(in)
	require.NoError(t, err)
	assert.NotContains(t, encoded, "\n", "a raw newline would not survive a JSON string; the encoder escapes it")

	sc, err := StyleScopeFromMetadata(map[string]string{MetaKeyStyleScopePaths: encoded})
	require.NoError(t, err)
	assert.Equal(t, in, sc.Paths, "every path arrives byte-identical, comma and newline included")
	assert.True(t, sc.PathsSet)
}

// TestStyleScopeFromMetadata_RefusesAValueItCannotRead is the bad-input leg.
func TestStyleScopeFromMetadata_RefusesAValueItCannotRead(t *testing.T) {
	for _, raw := range []string{"lib", "", "[", `{"lib":true}`, `[1,2]`} {
		sc, err := StyleScopeFromMetadata(map[string]string{MetaKeyStyleScopePaths: raw})
		require.Error(t, err, "%q is not a JSON array of paths and must be refused, never read as an empty scope", raw)
		assert.False(t, sc.Scoped(), "a refused decode returns the zero value, never a scope a caller could act on")
		assert.Contains(t, err.Error(), MetaKeyStyleScopePaths, "the refusal names the key")
		// The value is named QUOTED, so the expectation is quoted too: a bare
		// Contains would pass for values with no quotes in them and fail for the
		// object row, which is the refusal doing its job rather than skipping it.
		assert.Contains(t, err.Error(), fmt.Sprintf("%q", raw),
			"the refusal names the value, which is the only thing its author can fix")
	}
}

// TestStyleScopePathRefusal_NamesEverySpellingThatCanNeverMatch drives the three
// refusals, each with the legitimate near-miss beside it.
func TestStyleScopePathRefusal_NamesEverySpellingThatCanNeverMatch(t *testing.T) {
	for _, good := range []string{"lib", "cmd/knowledge/internal", "a,b/with-comma.go", "we\nird.go", "..hidden"} {
		assert.Empty(t, StyleScopePathRefusal(good), "%q is a legitimate repo-relative path", good)
	}
	for _, tc := range []struct{ path, want string }{
		{"", "is empty"},
		{"   ", "is empty"},
		{"/lib", "absolute"},
		{"..", ".."},
		{"../lib", ".."},
		{"lib/../other", ".."},
		{"./lib", "canonicalize"},
		{"lib/", "canonicalize"},
		{"lib//x", "canonicalize"},
	} {
		refusal := StyleScopePathRefusal(tc.path)
		require.NotEmpty(t, refusal, "%q can never match a walk path and must be refused", tc.path)
		assert.Contains(t, refusal, tc.want, "the refusal for %q must say why", tc.path)
	}
}

// TestStyleScopeRepoRefusal_HoldsARepoToBeingAName is the repo leg's own table.
//
// A REPO SCOPE IS COMPARED AGAINST A GRAPH INSTANCE NAME, which is the
// checkout's basename, so a value carrying a separator can never equal one. It
// is refused rather than trimmed: a silently rewritten scope is a scope its
// author cannot see they got wrong.
func TestStyleScopeRepoRefusal_HoldsARepoToBeingAName(t *testing.T) {
	for _, good := range []string{"knowledge", "my-repo", "repo.with.dots", "repo_1"} {
		assert.Empty(t, StyleScopeRepoRefusal(good), "%q is a legitimate repo name", good)
	}
	for _, tc := range []struct{ repo, want string }{
		{"", "is empty"},
		{"  ", "is empty"},
		{"org/knowledge", "path separator"},
		{"/abs/knowledge", "path separator"},
		{`org\knowledge`, "path separator"},
		{".", "not a repository name"},
		{"..", "not a repository name"},
	} {
		refusal := StyleScopeRepoRefusal(tc.repo)
		require.NotEmpty(t, refusal, "%q cannot be a graph instance name and must be refused", tc.repo)
		assert.Contains(t, refusal, tc.want,
			"the refusal for %q must say why, got %q", tc.repo, refusal)
	}
}
