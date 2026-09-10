// SPDX-License-Identifier: Apache-2.0

package collectorconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loader_test.go — the two scopes: precedence, the walk-up, and what a refusal
// in one scope does to the other.

// writeScope writes one file and returns its path.
func writeScope(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, ConfigDirName, FileName)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// stdioBody renders a one-entry file with the given command and env block.
func stdioBody(name, command, envJSON string) string {
	env := ""
	if envJSON != "" {
		env = `,"env":` + envJSON
	}
	return `{"collectors":{"` + name + `":{"type":"stdio","command":"` + command + `","tool":"collect"` + env + `}}}`
}

// TestLoader_ProjectBeatsUserAndTheWinnerIsWhole is the precedence row, and the
// FIELD-MERGE NEGATIVE is what gives it meaning: the project entry carries no
// env block and the user entry does, so a merged resolution would hand the child
// the user scope's variables. The winning entry is taken whole.
func TestLoader_ProjectBeatsUserAndTheWinnerIsWhole(t *testing.T) {
	userPath := writeScope(t, t.TempDir(), stdioBody("tickets", "/user/p", `{"USER_ONLY":"u"}`))
	projectPath := writeScope(t, t.TempDir(), stdioBody("tickets", "/project/p", ""))

	l := Loader{UserPath: userPath, ProjectPath: projectPath, Env: noEnv}
	got, found, err := l.Resolve("tickets")
	require.NoError(t, err)
	require.True(t, found)

	assert.Equal(t, ScopeProject, got.Scope, "the project scope wins")
	assert.Equal(t, "/project/p", got.Entry.Command)
	assert.Empty(t, got.Entry.Env,
		"NO FIELD IS MERGED ACROSS SCOPES: a project entry with no env block resolves to an empty environment, never to the user entry's")

	// KNOWN NEGATIVE, same run: with no project file the user entry resolves, so
	// the assertions above are about precedence rather than about a user scope
	// that never loads.
	userOnly := Loader{UserPath: userPath, Env: noEnv}
	got, found, err = userOnly.Resolve("tickets")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, ScopeUser, got.Scope)
	assert.Equal(t, map[string]string{"USER_ONLY": "u"}, got.Entry.Env)
}

// TestLoader_EntriesShowsBothScopes pins what `list` renders: every entry from
// both files, project first.
func TestLoader_EntriesShowsBothScopes(t *testing.T) {
	userPath := writeScope(t, t.TempDir(), stdioBody("tickets", "/user/p", ""))
	projectPath := writeScope(t, t.TempDir(), stdioBody("boards", "/project/p", ""))

	all, err := Loader{UserPath: userPath, ProjectPath: projectPath, Env: noEnv}.Entries()
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, ScopeProject, all[0].Scope, "project entries come first, which is what makes the first row per name the winner")
	assert.Equal(t, "boards", all[0].Name)
	assert.Equal(t, ScopeUser, all[1].Scope)
	assert.Equal(t, "tickets", all[1].Name)
}

// TestLoader_WinnersCollapsesToOneRowPerFamily pins the view the dispatch and
// the coverage table read.
func TestLoader_WinnersCollapsesToOneRowPerFamily(t *testing.T) {
	userPath := writeScope(t, t.TempDir(), stdioBody("tickets", "/user/p", ""))
	projectPath := writeScope(t, t.TempDir(), stdioBody("tickets", "/project/p", ""))

	winners, err := Loader{UserPath: userPath, ProjectPath: projectPath, Env: noEnv}.Winners()
	require.NoError(t, err)
	require.Len(t, winners, 1, "one family, one row")
	assert.Equal(t, "/project/p", winners[0].Entry.Command)
}

// referenceScope is one file holding an entry whose credential reference is
// unset and a credential-free sibling beside it — the shape an operator has the
// moment they install a second collector.
const referenceScope = `{"collectors":{
  "needs-token":{"type":"stdio","command":"/bin/needs","tool":"collect",
                 "env":{"TOKEN":"${KN_T14_NEVER_SET}"}},
  "no-credential":{"type":"stdio","command":"/bin/free","tool":"collect"}}}`

// TestLoader_AnUnsetReferenceRefusesOnlyItsOwnEntry is requirement 7's unit row.
//
// An unresolvable `${VAR}` used to be returned as the FILE's error, so a scope
// holding one uncollectable entry held nothing collectable at all: a sibling
// with no credential in it was refused by a message naming a different entry.
// The bad input is one entry's reference, and it errors there.
func TestLoader_AnUnsetReferenceRefusesOnlyItsOwnEntry(t *testing.T) {
	path := writeScope(t, t.TempDir(), referenceScope)
	l := Loader{UserPath: path, Env: noEnv}

	sibling, found, err := l.Resolve("no-credential")
	require.NoError(t, err,
		"a sibling entry that references nothing must resolve while another entry's variable is unset")
	require.True(t, found)
	assert.Equal(t, "/bin/free", sibling.Entry.Command)
	require.NoError(t, sibling.Unresolved, "the sibling carries no refusal of its own")

	// THE ENTRY THAT DOES CARRY ONE still resolves as a registered entry, and
	// carries its refusal: "not registered" and "registered but unusable here"
	// are different answers and a caller acts differently on each.
	broken, found, err := l.Resolve("needs-token")
	require.NoError(t, err)
	require.True(t, found)
	require.Error(t, broken.Unresolved, "the entry whose reference is unset carries the refusal")
	for _, want := range []string{path, "needs-token", "TOKEN", "KN_T14_NEVER_SET"} {
		assert.Containsf(t, broken.Unresolved.Error(), want,
			"the refusal names the file, the entry, the field and the variable; %q is missing", want)
	}
	assert.Equal(t, "${KN_T14_NEVER_SET}", broken.Entry.Env["TOKEN"],
		"the unresolved entry is carried UNEXPANDED, so what is rendered back is what the file says")

	// THE SAME-RUN CONTROL: with the variable set, the same file resolves both
	// entries and the value arrives. Without it, every row above would pass
	// against a loader that had simply stopped expanding anything.
	held := Loader{UserPath: path, Env: func(name string) (string, bool) {
		return "resolved-value", name == "KN_T14_NEVER_SET"
	}}
	got, found, err := held.Resolve("needs-token")
	require.NoError(t, err)
	require.True(t, found)
	require.NoError(t, got.Unresolved)
	assert.Equal(t, "resolved-value", got.Entry.Env["TOKEN"])
}

// TestLoader_EveryViewCarriesTheEntrysOwnRefusal covers the three reads a caller
// has. Entries and Winners must not drop the row — the entry IS registered — and
// must not hand it back looking ordinary, which is how a spawn would get the
// literal text `${VAR}` where a credential belongs.
func TestLoader_EveryViewCarriesTheEntrysOwnRefusal(t *testing.T) {
	path := writeScope(t, t.TempDir(), referenceScope)
	l := Loader{UserPath: path, Env: noEnv}

	all, err := l.Entries()
	require.NoError(t, err, "one entry's unset reference does not fail the listing")
	require.Len(t, all, 2, "both entries are still registered")
	byName := map[string]ScopedEntry{}
	for _, se := range all {
		byName[se.Name] = se
	}
	require.Error(t, byName["needs-token"].Unresolved)
	require.NoError(t, byName["no-credential"].Unresolved)

	winners, err := l.Winners()
	require.NoError(t, err)
	require.Len(t, winners, 2)
	for _, se := range winners {
		if se.Name == "needs-token" {
			require.Error(t, se.Unresolved, "Winners carries the refusal too")
		}
	}
}

// TestLoader_AStructuralRefusalIsStillTheFilesOwn is the boundary of the change
// above, and it is what keeps the per-entry scope from becoming "nothing fails
// the file any more". A malformed entry describes a file nobody can read
// correctly, so it still refuses the load whole.
func TestLoader_AStructuralRefusalIsStillTheFilesOwn(t *testing.T) {
	path := writeScope(t, t.TempDir(), `{"collectors":{
	  "innocent":{"type":"stdio","command":"/bin/free","tool":"collect"},
	  "broken":{"type":"stdio","command":"/bin/p","tool":"collect","binary_path":"/p"}}}`)
	_, _, err := Loader{UserPath: path, Env: noEnv}.Resolve("innocent")
	require.Error(t, err, "an unknown key is a file nobody can read correctly, not one entry's bad value")
	assert.Contains(t, err.Error(), "binary_path")
}

// TestLoader_ARefusalInOneScopeIsNotAnsweredFromTheOther pins that precedence is
// not a fallback. A malformed project file refuses the whole load even for a
// name the user file could serve: the alternative silently substitutes a
// different collector for the one the operator wrote.
func TestLoader_ARefusalInOneScopeIsNotAnsweredFromTheOther(t *testing.T) {
	userPath := writeScope(t, t.TempDir(), stdioBody("tickets", "/user/p", ""))
	projectPath := writeScope(t, t.TempDir(), `{"collectors":{"other":{"type":"nonsense"}}}`)

	l := Loader{UserPath: userPath, ProjectPath: projectPath, Env: noEnv}
	_, _, err := l.Resolve("tickets")
	require.Error(t, err, "a refusal in one scope refuses the load; falling through would serve a different entry than the file says")
	assert.Contains(t, err.Error(), "other")

	// KNOWN POSITIVE: the same user file loads on its own, so the refusal above is
	// the project file's and not a user file this test cannot read.
	_, found, err := Loader{UserPath: userPath, Env: noEnv}.Resolve("tickets")
	require.NoError(t, err)
	assert.True(t, found)
}

// TestFindProjectPath_WalksUpAndStops covers the three classes: a file in an
// ancestor is found from a nested cwd, a tree with no such file yields "" rather
// than reaching into a parent that has one, and the walk stops at the root.
func TestFindProjectPath_WalksUpAndStops(t *testing.T) {
	root := t.TempDir()
	want := writeScope(t, root, stdioBody("tickets", "/p", ""))
	nested := filepath.Join(root, "a", "b", "c")
	require.NoError(t, os.MkdirAll(nested, 0o750))

	assert.Equal(t, want, FindProjectPath(nested), "a project file in an ancestor must be found from a nested cwd")
	assert.Equal(t, want, FindProjectPath(root))

	// THE NEGATIVE: an unrelated tree resolves to no project file. It is a
	// separate TempDir rather than a subdirectory, so nothing above it holds one.
	other := t.TempDir()
	assert.Empty(t, FindProjectPath(other), "a tree with no project file must resolve to none")
	assert.Empty(t, FindProjectPath(""), "no cwd means no project scope")
}

// TestLoader_AnEmptyScopePathContributesNothing pins the degraded shape: a
// session with no project scope resolves from the user file alone rather than
// erroring, which is what "sees user-scope entries only" means.
func TestLoader_AnEmptyScopePathContributesNothing(t *testing.T) {
	userPath := writeScope(t, t.TempDir(), stdioBody("tickets", "/user/p", ""))
	all, err := Loader{UserPath: userPath, Env: noEnv}.Entries()
	require.NoError(t, err)
	require.Len(t, all, 1)

	none, err := Loader{Env: noEnv}.Entries()
	require.NoError(t, err)
	assert.Empty(t, none, "a loader with neither scope resolved has no entries and no error")
}
