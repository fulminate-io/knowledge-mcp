// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorconfig"
	"github.com/fulminate-io/knowledge-mcp/internal/session"
)

// collect_custom_config_test.go — the config file IS the registration record at
// the collect dispatch: a file entry registers a family with no tool call, the
// project scope resolves from the session's own working directory, the behavior
// half reaches the server, and NOTHING resolves from the server catalog.

// TestCustomCollect_AFileEntryIsARegisteredFamily is R1's first clause. No
// registration tool call is made anywhere in this test: the only thing that
// exists is a file.
func TestCustomCollect_AFileEntryIsARegisteredFamily(t *testing.T) {
	url := startCustomProvider(t, conformingCustomPayload())
	deps := newCustomDeps(t, namedCustomDef("tickets", url))

	handled, res := callCollect(deps, `{"type":"tickets","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	got := deps.sink.last()
	require.NotNil(t, got, "an entry in the user-scope file must dispatch to its provider")
	assert.Equal(t, "board", got.GraphName)
	assert.Equal(t, "ISSUE-1", got.Nodes[0].GetId())
}

// TestCustomCollect_ProjectScopeResolvesFromTheSessionCwd is R1's project half.
//
// THE WALK-UP IS THE SUBJECT: the session's working directory is a nested
// subdirectory, and the file sits at the repository root above it. The negative
// arm is a session in an unrelated tree, which must NOT see the entry — without
// it, a loader that read every project file on the machine would pass.
func TestCustomCollect_ProjectScopeResolvesFromTheSessionCwd(t *testing.T) {
	url := startCustomProvider(t, conformingCustomPayload())
	deps := newCustomDeps(t) // the user scope is empty

	root := t.TempDir()
	projectPath := filepath.Join(root, collectorconfig.ConfigDirName, collectorconfig.FileName)
	require.NoError(t, collectorconfig.Write(projectPath, map[string]collectorconfig.Entry{
		"tickets": namedCustomDef("tickets", url).entry,
	}))
	nested := filepath.Join(root, "a", "b")
	require.NoError(t, os.MkdirAll(nested, 0o750))

	inNested := session.ContextWithWorkspaceCwd(opCtx(), nested)
	reg, err := lookupCustomCollector(inNested, deps, "tickets")
	require.NoError(t, err)
	require.NotNil(t, reg, "a project-scope entry must be found from a session standing under its repository root")
	assert.Equal(t, "tickets", reg.Def.GetName())

	// THE NEGATIVE, same run: a session in an unrelated tree sees nothing, and
	// says which of the two reasons applies — it HAS a working directory, and
	// nothing above it holds a project file.
	other := t.TempDir()
	elsewhere := session.ContextWithWorkspaceCwd(opCtx(), other)
	reg, err = lookupCustomCollector(elsewhere, deps, "tickets")
	require.Error(t, err, "a session in another tree must not see this repository's project entries")
	assert.Nil(t, reg, "a session in another tree must not see this repository's project entries")
	assert.Contains(t, err.Error(), "no ancestor of this session's working directory")
	assert.Contains(t, err.Error(), other, "and it must name the directory it walked up from")

	// AND A SESSION WITH NO WORKSPACE CWD sees the user scope only, which is what
	// the fall-back to --root amounts to for a deps whose RootDir is empty.
	reg, err = lookupCustomCollector(opCtx(), deps, "tickets")
	require.Error(t, err, "a project-only name from a session with no cwd must SAY the project scope went unread")
	assert.Nil(t, reg)
}

// TestCustomCollect_AProjectOnlyNameFromASessionWithNoCwdSaysTheProjectScopeWentUnREAD
// is the second clause of the which-repository ruling, and it is about a message
// rather than a capability.
//
// THE TWO ANSWERS ARE OTHERWISE IDENTICAL. A family registered in a repository's
// project file, collected from a session carrying no workspace cwd, used to fall
// to the builtin path and come back `collector: unknown collector "projonly"` —
// byte for byte what a typo produces. The operator can see the entry in their
// own repository and is told it does not exist.
//
// THE SAME-RUN CONTROL is the first half: from a session standing IN the
// repository the very same entry resolves. Without it, the refusal below is
// equally explained by an entry that was never written.
func TestCustomCollect_AProjectOnlyNameFromASessionWithNoCwdSaysTheProjectScopeWentUnread(t *testing.T) {
	url := startCustomProvider(t, conformingCustomPayload())
	deps := newCustomDeps(t) // the user scope is empty

	root := t.TempDir()
	require.NoError(t, collectorconfig.Write(
		filepath.Join(root, collectorconfig.ConfigDirName, collectorconfig.FileName),
		map[string]collectorconfig.Entry{"projonly": namedCustomDef("projonly", url).entry}))

	// CONTROL: inside the repository, the entry resolves.
	inRepo := session.ContextWithWorkspaceCwd(opCtx(), root)
	reg, err := lookupCustomCollector(inRepo, deps, "projonly")
	require.NoError(t, err)
	require.NotNil(t, reg, "the entry must be real, or the refusal below is about nothing")

	// SUBJECT: the same collect from a session with no workspace cwd.
	handled, res := callCollect(deps, `{"type":"projonly","id":"board"}`)
	require.True(t, handled)
	require.True(t, res.IsError, resultText(res))
	body := resultText(res)
	assert.Contains(t, body, "projonly", "the refusal must name the family")
	assert.Contains(t, body, deps.scope.path, "and the user-scope file it DID read")
	assert.Contains(t, body, "no workspace cwd", "and that this session carries none")
	assert.Contains(t, body, "NO project-scope file was read", "and that no project file was read at all")
	assert.NotContains(t, body, "unknown collector",
		"the generic unknown-type refusal is the answer this case must NOT get: it is what a typo produces")
	assert.Nil(t, deps.sink.last(), "a refused collect must write nothing")
}

// TestCollect_ABuiltinCollectorNameStillFallsThroughWithNoCwd is the guard on the
// row above, and it is the one that matters most: the new refusal must not reach
// a name a COMPILED-IN collector serves. A session with no workspace cwd is the
// ordinary state for a daemon call, so firing there would break every builtin
// collect that is not also a builtin graph type.
func TestCollect_ABuiltinCollectorNameStillFallsThroughWithNoCwd(t *testing.T) {
	registerShadowStub(t)
	deps := newCustomDeps(t) // no entries, no project scope

	reg, err := lookupCustomCollector(opCtx(), deps, shadowStubType)
	require.NoError(t, err, "a compiled-in collector's name must still fall through to it")
	assert.Nil(t, reg)
}

// TestCustomCollect_ForwardsTheBehaviorHalfToTheServer is the behavior half at
// the dispatch: what an entry with no behavior block reaches the server as.
//
// PRESENCE AND VALUE ARE ASSERTED SEPARATELY, and the reason survives the change
// of defaults. The server's cascade coalesces an unset boolean to FALSE, so
// reading the value alone cannot tell a deliberate false from a field nobody
// wrote — and the difference is what an operator sees in `collector get` and in
// the catalog. The loader therefore writes all three explicitly.
//
// THE TWO LLM AXES DEFAULT OFF, syncable ON. Summarizing and embedding are LLM
// spend and are opted into per collector; the family is keyword-searchable either
// way, because a registered graph is admitted to the keyword corpus regardless of
// its embed setting.
func TestCustomCollect_ForwardsTheBehaviorHalfToTheServer(t *testing.T) {
	url := startCustomProvider(t, conformingCustomPayload())
	deps := newCustomDeps(t, namedCustomDef("tickets", url))

	handled, res := callCollect(deps, `{"type":"tickets","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	crud, ok := deps.crud.(*stubGraphTypeCRUD)
	require.True(t, ok)
	written := crud.lastUpserted()
	require.NotNil(t, written, "the loader must forward the family's behavior record to the server")
	assert.Equal(t, "tickets", written.GetName())

	b := written.GetBehavior()
	require.NotNil(t, b)
	require.NotNil(t, b.Syncable, "syncable must be SET, not left for the server to coalesce to false")
	require.NotNil(t, b.Summarizable, "summarizable must be SET: an explicit false and an unset field "+
		"behave the same on the server but read differently to an operator")
	require.NotNil(t, b.Embeddable, "embeddable must be SET")
	assert.True(t, *b.Syncable, "syncable defaults ON: it costs no LLM call")
	assert.False(t, *b.Summarizable, "summarization is LLM spend and is OPT-IN per collector")
	assert.False(t, *b.Embeddable, "embedding is LLM spend and is OPT-IN per collector")

	// THE SECRET-LEAK ROW: the record that crosses the wire carries no connection
	// half at all, so no env value and no header value is ever stored in a
	// graph-resident node.
	assert.Nil(t, written.GetCollector(), "the persisted record must carry NO CollectorSpec")
}

// collectFlagWant pairs one untouched flag's pointer with the default it must
// still carry. The three no longer share one default, so the rest-loop cannot
// assert a single value across them.
type collectFlagWant struct {
	p    *bool
	want bool
}

// TestCustomCollect_BehaviorBlockOverridesReachTheServer is the other half: the
// default is a default. Without this row a loader that hard-coded the three
// flags to true would pass the test above.
//
// ONE CASE PER FIELD, for the reason its collectorconfig sibling gives: each of
// the three flags is overridden in its own branch, and a single-field test
// leaves two of them unobserved on the path that actually crosses the wire.
func TestCustomCollect_BehaviorBlockOverridesReachTheServer(t *testing.T) {
	no := false
	for _, tc := range []struct {
		name      string
		set       func(*collectorconfig.Behavior, *bool)
		get       func(*knowledgev1.BehaviorDefaults) *bool
		restNames func(*knowledgev1.BehaviorDefaults) []collectFlagWant
	}{
		{
			"syncable",
			func(b *collectorconfig.Behavior, v *bool) { b.Syncable = v },
			func(d *knowledgev1.BehaviorDefaults) *bool { return d.Syncable },
			func(d *knowledgev1.BehaviorDefaults) []collectFlagWant {
				return []collectFlagWant{{d.Summarizable, false}, {d.Embeddable, false}}
			},
		},
		{
			"summarizable",
			func(b *collectorconfig.Behavior, v *bool) { b.Summarizable = v },
			func(d *knowledgev1.BehaviorDefaults) *bool { return d.Summarizable },
			func(d *knowledgev1.BehaviorDefaults) []collectFlagWant {
				return []collectFlagWant{{d.Syncable, true}, {d.Embeddable, false}}
			},
		},
		{
			"embeddable",
			func(b *collectorconfig.Behavior, v *bool) { b.Embeddable = v },
			func(d *knowledgev1.BehaviorDefaults) *bool { return d.Embeddable },
			func(d *knowledgev1.BehaviorDefaults) []collectFlagWant {
				return []collectFlagWant{{d.Syncable, true}, {d.Summarizable, false}}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url := startCustomProvider(t, conformingCustomPayload())
			block := &collectorconfig.Behavior{}
			tc.set(block, &no)
			entry := namedCustomDef("tickets", url)
			entry.entry.Behavior = block
			deps := newCustomDeps(t, entry)

			handled, res := callCollect(deps, `{"type":"tickets","id":"board"}`)
			require.True(t, handled)
			require.False(t, res.IsError, resultText(res))

			crud, ok := deps.crud.(*stubGraphTypeCRUD)
			require.True(t, ok)
			b := crud.lastUpserted().GetBehavior()
			require.NotNil(t, b)

			got := tc.get(b)
			require.NotNil(t, got, "%s must reach the server SET", tc.name)
			assert.False(t, *got, "an explicit false in the entry must be honored")
			for i, other := range tc.restNames(b) {
				require.NotNil(t, other.p, "the flags the entry did not mention must still be set (%d)", i)
				assert.Equal(t, other.want, *other.p,
					"and they keep their own default: syncable ON, the two LLM axes OFF")
			}
		})
	}
}

// TestCustomCollect_AnUnreadableConfigFileIsAnErrorNotAnAllClear pins the
// failed-lookup rule against the new source of truth. "Not registered" and
// "could not tell" are different answers with different consequences: the first
// sends the collect to the builtin path to be refused for the wrong reason.
func TestCustomCollect_AnUnreadableConfigFileIsAnErrorNotAnAllClear(t *testing.T) {
	deps := newCustomDeps(t)
	deps.scope.writeRaw(t, `{"collectors":`)

	reg, err := lookupCustomCollector(opCtx(), deps, "tickets")
	require.Error(t, err, "a file that exists and cannot be read means the registrations are UNKNOWN, not absent")
	assert.Contains(t, err.Error(), "tickets")
	assert.Nil(t, reg)
}

// TestCustomCollect_AnUnsetReferenceRefusesOnlyItsOwnEntry is requirement 7 at
// the dispatch, which is where an operator meets it.
//
// A CREDENTIALED ENTRY NAMES A VARIABLE THE SERVING PROCESS SUPPLIES. When that
// process does not hold it, the collect is refused BY NAME — never handed to the
// child as an empty value — and the refusal stops at that entry: a second
// collector in the same file, credential-free, collects in the same run. The
// file-wide version of this refusal made one operator's unset variable an outage
// for every collector they had registered in that scope.
func TestCustomCollect_AnUnsetReferenceRefusesOnlyItsOwnEntry(t *testing.T) {
	url := startCustomProvider(t, conformingCustomPayload())
	needsToken := namedCustomDef("needs-token", url)
	needsToken.entry.Headers = map[string]string{"Authorization": "Bearer ${KN_T14_NEVER_SET_IN_THIS_PROCESS}"}
	deps := newCustomDeps(t, needsToken, namedCustomDef("no-credential", url))

	handled, res := callCollect(deps, `{"type":"needs-token","id":"board"}`)
	require.True(t, handled)
	require.True(t, res.IsError, "an unset reference must refuse rather than dial with an empty credential")
	for _, want := range []string{"needs-token", "KN_T14_NEVER_SET_IN_THIS_PROCESS", "Authorization"} {
		assert.Containsf(t, resultText(res), want,
			"the refusal names the entry, the variable and the field; %q is missing", want)
	}
	assert.Contains(t, resultText(res), "no other entry in that file is affected")

	// THE SIBLING, IN THE SAME FILE AND THE SAME RUN. This is the whole
	// requirement: it collects while the entry beside it cannot.
	handled, res = callCollect(deps, `{"type":"no-credential","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))
	require.NotNil(t, deps.sink.last(), "the sibling entry collected while its neighbor was refused")
	assert.Equal(t, "board", deps.sink.last().GraphName)
}
