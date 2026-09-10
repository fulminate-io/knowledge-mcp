// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorconfig"
)

// collector_read_verbs_test.go — `knowledge collector get | list | remove`, the
// three verbs that read or delete rather than install.
//
// SPLIT FROM collector_subcommand_test.go along the seam it already had,
// because this commit's additions would have carried that file past this
// repository's 500-line gate: it stood at 469 lines and the add verb's new rows
// are about 170 more. The add verb owns the argv parsing, the provider dial and
// the write path; these three own the rendering and the removal.
//
// EIGHT OF THE NINE DECLARATIONS MOVED BYTE FOR BYTE.
// TestCollectorList_SaysWhenTheLegacyColumnCouldNotBeRead is the exception, and
// it changed in this same commit to drive its second arm — no catalog reader
// wired at all — beside the arm it already had. The fixtures they share
// (useTempHome, useScratchCwd, startContractProvider, the stdio stub harness)
// stay in the file they were written in, same package.

// TestCollectorGetAndRemove_NameTheScopeAndRefuseAnAbsentName covers the two
// read/delete verbs including their refusal arms.
func TestCollectorGetAndRemove_NameTheScopeAndRefuseAnAbsentName(t *testing.T) {
	userPath := useTempHome(t)
	useScratchCwd(t)
	require.NoError(t, collectorconfig.Upsert(userPath, "tickets", collectorconfig.Entry{
		Type: collectorconfig.TransportStdio, Command: "/usr/local/bin/p", Tool: "collect",
	}))

	var out bytes.Buffer
	require.NoError(t, runCollectorGet([]string{"tickets"}, &out))
	assert.Contains(t, out.String(), "tickets")
	assert.Contains(t, out.String(), "user scope")
	assert.Contains(t, out.String(), "/usr/local/bin/p")

	err := runCollectorGet([]string{"nope"}, &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope")

	out.Reset()
	require.NoError(t, runCollectorRemove([]string{"tickets"}, &out))
	assert.Contains(t, out.String(), "removed")

	err = runCollectorRemove([]string{"tickets"}, &out)
	require.Error(t, err, "removing an absent name must FAIL: reporting success would tell an operator a collector was removed while it kept collecting")
	assert.Contains(t, err.Error(), "tickets")
}

// TestCollectorGet_RendersTheFilesOwnTextAndNeverAnExpandedCredential is the
// disclosure row. An installed credentialed collector's entry carries
// `"TOKEN": "${KN_T14_GET_TOKEN}"`, so a `get` that rendered the RESOLVED entry
// would print the operator's token to a terminal, a pipe or a pasted transcript
// on every machine that holds the variable.
func TestCollectorGet_RendersTheFilesOwnTextAndNeverAnExpandedCredential(t *testing.T) {
	userPath := useTempHome(t)
	useScratchCwd(t)
	t.Setenv("KN_T14_GET_TOKEN", "the-secret-token-value")
	require.NoError(t, collectorconfig.Upsert(userPath, "tickets", collectorconfig.Entry{
		Type: collectorconfig.TransportStdio, Command: "/usr/local/bin/p", Tool: "collect",
		Env: map[string]string{"TOKEN": "${KN_T14_GET_TOKEN}"},
	}))

	var out bytes.Buffer
	require.NoError(t, runCollectorGet([]string{"tickets"}, &out))
	assert.NotContains(t, out.String(), "the-secret-token-value",
		"`collector get` printed the resolved credential; it answers what the FILE says")
	assert.Contains(t, out.String(), "${KN_T14_GET_TOKEN}",
		"and it says what the file says, which is the reference the operator wrote")

	// THE SAME-RUN CONTROL: the variable really is resolvable in this process, so
	// the absence above is `get` refusing to expand rather than a lookup that
	// could not have found anything.
	se, found, err := collectorconfig.Loader{UserPath: userPath}.Resolve("tickets")
	require.NoError(t, err)
	require.True(t, found)
	require.NoError(t, se.Unresolved)
	require.Equal(t, "the-secret-token-value", se.Entry.Env["TOKEN"],
		"control: the loader DOES resolve this reference in this process")
}

// TestCollectorReadVerbs_NameAnEntryWhoseReferenceDoesNotResolveHere covers the
// other half: an entry this process cannot resolve is still rendered — it is a
// registered entry and the operator is diagnosing it — and both verbs SAY so
// rather than printing `${VAR}` as though it were an ordinary value.
func TestCollectorReadVerbs_NameAnEntryWhoseReferenceDoesNotResolveHere(t *testing.T) {
	userPath := useTempHome(t)
	useScratchCwd(t)
	require.NoError(t, collectorconfig.Upsert(userPath, "tickets", collectorconfig.Entry{
		Type: collectorconfig.TransportStdio, Command: "/usr/local/bin/p", Tool: "collect",
		Env: map[string]string{"TOKEN": "${KN_T14_NOT_SET_ANYWHERE}"},
	}))
	require.NoError(t, collectorconfig.Upsert(userPath, "no-credential", collectorconfig.Entry{
		Type: collectorconfig.TransportStdio, Command: "/usr/local/bin/q", Tool: "collect",
	}))

	var got bytes.Buffer
	require.NoError(t, runCollectorGet([]string{"tickets"}, &got),
		"an entry whose reference is unset is still readable: it is registered")
	assert.Contains(t, got.String(), "does not resolve in this process")
	assert.Contains(t, got.String(), "KN_T14_NOT_SET_ANYWHERE")

	var listed bytes.Buffer
	require.NoError(t, renderCollectorList(&listed,
		collectorconfig.Loader{UserPath: userPath}, emptyCatalog))
	assert.Contains(t, listed.String(), "tickets", "the entry is still listed")
	assert.Contains(t, listed.String(), "no-credential",
		"and so is its sibling: one entry's unset reference is not the scope's failure")
	// REQUIRE, not assert: the slice below is taken from this substring, so a
	// missing section must fail HERE rather than panic three lines down.
	const heading = "entries whose environment does not resolve"
	require.Contains(t, listed.String(), heading,
		"the listing names the entry that does not resolve rather than rendering it as ordinary")

	// THE CONTROL: the sibling is NOT named in that section, so the section
	// reports one entry rather than every entry.
	section := listed.String()[strings.Index(listed.String(), heading):]
	assert.NotContains(t, section, "no-credential")
}

// TestCollectorWriteVerbs_RegisterNoPortAndContactNoDaemon is the observable
// behind "add, get and remove contact no daemon".
//
// THE FLAG SET IS THE INSTRUMENT: only `list` registers a port, because only
// `list` reads the server. A verb that grew a connection would need somewhere to
// aim it, and the run below — three verbs completing with no server anywhere —
// is the behavioral half beside it.
func TestCollectorWriteVerbs_RegisterNoPortAndContactNoDaemon(t *testing.T) {
	for _, verb := range []string{"add", "get", "remove"} {
		fs := newCollectorFlagSet(verb, &collectorFlags{}, verb == "add")
		assert.Nil(t, fs.Lookup("port"), "%s must not register a port flag: it makes no connection", verb)
	}
	listFS := newCollectorFlagSet("list", &collectorFlags{}, false)
	assert.NotNil(t, listFS.Lookup("port"), "list DOES read the server, which is what makes the assertions above meaningful")

	userPath := useTempHome(t)
	useScratchCwd(t)
	url := startContractProvider(t, true)
	var out bytes.Buffer
	require.NoError(t, runCollectorAdd([]string{"-t", "http", "--tool", "collect", "tickets", url}, &out))
	require.NoError(t, runCollectorGet([]string{"tickets"}, &out))
	require.NoError(t, runCollectorRemove([]string{"tickets"}, &out))
	assert.FileExists(t, userPath)
}

// TestCollectorList_ShowsBothScopesAndMarksTheWinner is the list verb's file
// half, driven through the renderer with an injected catalog so no daemon is
// contacted.
func TestCollectorList_ShowsBothScopesAndMarksTheWinner(t *testing.T) {
	userHome := t.TempDir()
	projectRoot := t.TempDir()
	userPath := collectorconfig.UserPathIn(userHome)
	projectPath := collectorconfig.ProjectPathIn(projectRoot)
	entry := collectorconfig.Entry{Type: collectorconfig.TransportStdio, Command: "/p", Tool: "collect"}
	require.NoError(t, collectorconfig.Upsert(userPath, "tickets", entry))
	require.NoError(t, collectorconfig.Upsert(userPath, "user-only", entry))
	require.NoError(t, collectorconfig.Upsert(projectPath, "tickets", entry))

	var out bytes.Buffer
	loader := collectorconfig.Loader{UserPath: userPath, ProjectPath: projectPath}
	require.NoError(t, renderCollectorList(&out, loader, emptyCatalog))

	body := out.String()
	assert.Contains(t, body, userPath)
	assert.Contains(t, body, projectPath)
	assert.Contains(t, body, "user-only")
	assert.Contains(t, body, "the project entry wins",
		"where a name is in both scopes the loser must say which one is in effect")
	assert.Contains(t, body, "none.", "with no catalog families the legacy section must say so explicitly")
}

// TestCollectorList_NamesLegacyFamilies is the migration path's surface: a
// catalog family no entry claims, with the invocation that converts it.
func TestCollectorList_NamesLegacyFamilies(t *testing.T) {
	userPath := collectorconfig.UserPathIn(t.TempDir())
	require.NoError(t, collectorconfig.Upsert(userPath, "claimed", collectorconfig.Entry{
		Type: collectorconfig.TransportStdio, Command: "/p", Tool: "collect",
	}))

	catalog := func(context.Context) ([]*knowledgev1.GraphTypeDef, error) {
		return []*knowledgev1.GraphTypeDef{
			{Name: "claimed"}, // has an entry: not legacy
			{Name: "classA", Collector: &knowledgev1.CollectorSpec{
				Tool: "collect",
				Provider: &knowledgev1.CollectorSpec_Stdio{Stdio: &knowledgev1.StdioProvider{
					Command: "/legacy/p", Env: []string{"TOKEN"},
				}},
			}},
			{Name: "classB", Collector: &knowledgev1.CollectorSpec{}},
		}, nil
	}

	var out bytes.Buffer
	require.NoError(t, renderCollectorList(&out, collectorconfig.Loader{UserPath: userPath}, catalog))
	body := out.String()

	assert.Contains(t, body, "classA — "+collectorconfig.LegacyConvertible)
	assert.Contains(t, body, "knowledge collector add -s user -t stdio --tool collect")
	assert.Contains(t, body, "-e TOKEN="+collectorconfig.EnvValuePlaceholder,
		"the env values those records never held must be placeholders, not empty assignments")
	assert.Contains(t, body, "classB — "+collectorconfig.LegacyUnconvertible)
	assert.NotContains(t, body, "claimed — ", "a family with an entry is not legacy")
}

// TestCollectorList_SaysWhenTheLegacyColumnCouldNotBeRead is the row that keeps
// the command honest with no daemon reachable. An OMITTED column would read as
// "there are no legacy families", which is the one answer it cannot give.
func TestCollectorList_SaysWhenTheLegacyColumnCouldNotBeRead(t *testing.T) {
	userPath := collectorconfig.UserPathIn(t.TempDir())
	require.NoError(t, collectorconfig.Upsert(userPath, "tickets", collectorconfig.Entry{
		Type: collectorconfig.TransportStdio, Command: "/p", Tool: "collect",
	}))

	// TWO WAYS THE COLUMN GOES UNREAD, and they are different code paths: the
	// reader FAILED, and there is no reader at all. Both must produce the same
	// answer, because the wrong one is identical for both — printing "none."
	// reports that there are no legacy families, which is the single answer this
	// column can never give.
	for _, tc := range []struct {
		name    string
		catalog catalogLister
	}{
		{"the catalog reader failed", func(context.Context) ([]*knowledgev1.GraphTypeDef, error) {
			return nil, assertNoDaemon()
		}},
		{"there is no catalog reader wired", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			require.NoError(t, renderCollectorList(&out, collectorconfig.Loader{UserPath: userPath}, tc.catalog),
				"an unreadable legacy column must not fail the command")

			body := out.String()
			assert.Contains(t, body, "tickets", "the file-backed entries must still print")
			assert.Contains(t, body, "could NOT be read")
			assert.Contains(t, body, "not a report that there are none")
			assert.NotContains(t, body, "  none.")
		})
	}
}

// TestCollectorVerbDispatch_RefusesAnUnknownVerbNamingTheAdmittedSet pins the
// dispatch's own refusal, rendered from the live verb set.
func TestCollectorVerbDispatch_RefusesAnUnknownVerbNamingTheAdmittedSet(t *testing.T) {
	err := runCollectorVerb(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "add, get, list, remove")

	err = runCollectorVerb([]string{"frobnicate"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "frobnicate")
	assert.Contains(t, err.Error(), "add, get, list, remove")
}

// TestResolveScope_RefusesAnUnknownScope pins the scope vocabulary.
func TestResolveScope_RefusesAnUnknownScope(t *testing.T) {
	got, err := resolveScope("")
	require.NoError(t, err)
	assert.Equal(t, collectorconfig.ScopeUser, got, "the default scope is user")

	got, err = resolveScope("project")
	require.NoError(t, err)
	assert.Equal(t, collectorconfig.ScopeProject, got)

	_, err = resolveScope("local")
	require.Error(t, err, "Claude's `local` scope does not exist here and must be refused rather than silently read as user")
	assert.Contains(t, err.Error(), "local")
}

// emptyCatalog is a catalog reader holding nothing.
func emptyCatalog(context.Context) ([]*knowledgev1.GraphTypeDef, error) { return nil, nil }

// assertNoDaemon is the failure a list with no reachable server produces.
func assertNoDaemon() error { return errNoDaemonForTest }

// errNoDaemonForTest stands in for the health-gate failure.
var errNoDaemonForTest = errNoDaemon{}

type errNoDaemon struct{}

func (errNoDaemon) Error() string { return "knowledge-server is not running on port 15023" }
