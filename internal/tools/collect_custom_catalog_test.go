// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorconfig"
)

// collect_custom_catalog_test.go — the SERVER CATALOG's place at the collect
// dispatch, which is none: a catalog family with no file entry is refused rather
// than collected, an unconvertible legacy record says so, and the coverage table
// unions the two sources without making a catalog-only family collectable.
//
// SPLIT FROM collect_custom_config_test.go at the seam that file already carried
// (its own `R12` divider), because the per-entry reference refusal added there
// carried it past this repository's 500-line gate. The fixtures both halves
// share stay where they were written, same package.

// --- R12: nothing resolves from the server catalog ---

// TestCustomCollect_ACatalogFamilyWithNoEntryIsRefused is R12's core row. It
// asserts all THREE things the refusal owes an operator, not merely that it
// errored: the family, the absolute path of the file to write, and the
// invocation that writes it.
func TestCustomCollect_ACatalogFamilyWithNoEntryIsRefused(t *testing.T) {
	url := startCustomProvider(t, conformingCustomPayload())
	deps := newCustomDeps(t) // NO file entry
	deps.crud = &stubGraphTypeCRUD{defs: []*knowledgev1.GraphTypeDef{{
		Name: "tickets",
		Collector: &knowledgev1.CollectorSpec{
			Tool:     customStubTool,
			Provider: &knowledgev1.CollectorSpec_Http{Http: &knowledgev1.HttpProvider{Url: url}},
		},
	}}}

	handled, res := callCollect(deps, `{"type":"tickets","id":"board"}`)
	require.True(t, handled)
	require.True(t, res.IsError, "a catalog family with no config entry must be REFUSED: %s", resultText(res))

	body := resultText(res)
	assert.Contains(t, body, "tickets", "the refusal must name the family")
	assert.Contains(t, body, deps.scope.path, "and the absolute path of the file to write")
	assert.Contains(t, body, "knowledge collector add", "and the invocation that writes it")
	assert.Nil(t, deps.sink.last(), "and it must write nothing")
}

// TestCustomCollect_NothingResolvesFromTheCatalog is the NEGATIVE CONTROL the
// row above rests on. The catalog record it seeds is COMPLETE and DIALABLE — the
// provider is live and would serve the collect — so the refusal is evidence that
// the catalog is not consulted for resolution, rather than evidence of a record
// the fixture failed to build.
//
// THE SAME-RUN KNOWN POSITIVE is the second half: writing the entry into the
// file makes the very same provider collect. Without it, the refusal is equally
// explained by a provider that never worked.
func TestCustomCollect_NothingResolvesFromTheCatalog(t *testing.T) {
	url := startCustomProvider(t, conformingCustomPayload())
	deps := newCustomDeps(t)
	deps.crud = &stubGraphTypeCRUD{defs: []*knowledgev1.GraphTypeDef{{
		Name: "tickets",
		Collector: &knowledgev1.CollectorSpec{
			Tool:     customStubTool,
			Provider: &knowledgev1.CollectorSpec_Http{Http: &knowledgev1.HttpProvider{Url: url}},
		},
	}}}

	handled, res := callCollect(deps, `{"type":"tickets","id":"board"}`)
	require.True(t, handled)
	require.True(t, res.IsError, "a dialable catalog record must still not serve a collect")
	assert.Nil(t, deps.sink.last())

	deps.scope.write(t, namedCustomDef("tickets", url))
	handled, res = callCollect(deps, `{"type":"tickets","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))
	require.NotNil(t, deps.sink.last(),
		"the SAME provider collects once a file entry names it, so the refusal above was about the catalog and not about the provider")
}

// TestCustomCollect_AnUnconvertibleLegacyRecordSaysSo pins the class-B message.
// Such a record names no tool, so there is no invocation to print — and it was
// ALREADY unusable before this contract, which the message must not obscure.
func TestCustomCollect_AnUnconvertibleLegacyRecordSaysSo(t *testing.T) {
	deps := newCustomDeps(t)
	deps.crud = &stubGraphTypeCRUD{defs: []*knowledgev1.GraphTypeDef{{
		Name:      "legacy-exec",
		Collector: &knowledgev1.CollectorSpec{},
	}}}

	handled, res := callCollect(deps, `{"type":"legacy-exec","id":"board"}`)
	require.True(t, handled)
	require.True(t, res.IsError, resultText(res))
	body := resultText(res)
	assert.Contains(t, body, collectorconfig.LegacyUnconvertible)
	assert.Contains(t, body, "names no tool")
	assert.NotContains(t, body, "knowledge collector add -", "there is no invocation to synthesize for a record naming no tool")
}

// TestCoverageRegisteredCustomFamilies_UnionsTheFileAndTheCatalog is R1's coverage
// clause and the union rule beside it: an entry gets its row with NO COLLECT
// EVER RUN, a catalog-only family keeps its rows, and a family in both is
// counted once from the ENTRY.
func TestCoverageRegisteredCustomFamilies_UnionsTheFileAndTheCatalog(t *testing.T) {
	deps := newCustomDeps(t,
		namedCustomDef("from-file", "https://p.example/mcp"),
		namedCustomDef("in-both", "https://p.example/mcp"),
	)
	yes := true
	deps.crud = &stubGraphTypeCRUD{defs: []*knowledgev1.GraphTypeDef{
		{Name: "in-both", Behavior: &knowledgev1.BehaviorDefaults{Syncable: &yes}},
		{Name: "catalog-only", Behavior: &knowledgev1.BehaviorDefaults{Syncable: &yes}},
	}}

	got, err := coverageRegisteredCustomFamilies(opCtx(), deps)
	require.NoError(t, err)

	names := make([]string, 0, len(got))
	for _, g := range got {
		names = append(names, string(g.gt))
	}
	assert.ElementsMatch(t, []string{"from-file", "in-both", "catalog-only"}, names,
		"an entry is walked with no collect ever run, and a catalog-only family keeps its rows")
	assert.Len(t, names, 3, "a family present in both must be counted ONCE, from the entry")

	// THE DEGRADED-CLIENT ARM. A nil catalog is capability absence, not a
	// failure: the FILE half must still be walked, and returning nothing here
	// would render a coverage table missing every registered family on a client
	// that legitimately has no catalog wired.
	deps.crud = nil
	got, err = coverageRegisteredCustomFamilies(opCtx(), deps)
	require.NoError(t, err, "a nil catalog yields no catalog types and NO error")
	fileOnly := make([]string, 0, len(got))
	for _, g := range got {
		fileOnly = append(fileOnly, string(g.gt))
	}
	assert.ElementsMatch(t, []string{"from-file", "in-both"}, fileOnly,
		"the file half survives a client with no catalog at all")
}

// TestCoverageRegisteredCustomFamilies_AnUnreadableFileIsAHardFailure pins the split
// this function's error contract turns on: the catalog half is OPTIONAL and its
// failure is reported beside the rows it did obtain, while the FILE is the
// record, so a file it cannot read means the set of families is unknown.
func TestCoverageRegisteredCustomFamilies_AnUnreadableFileIsAHardFailure(t *testing.T) {
	deps := newCustomDeps(t)
	deps.scope.writeRaw(t, `{"collectors":`)

	got, err := coverageRegisteredCustomFamilies(opCtx(), deps)
	require.Error(t, err, "an unreadable config file must fail rather than render a table that looks complete")
	assert.Nil(t, got)

	// THE CONTRAST, in the same test: a CATALOG failure returns the file half
	// beside the error rather than dropping everything.
	deps2 := newCustomDeps(t, namedCustomDef("from-file", "https://p.example/mcp"))
	deps2.crud = &failingGraphTypeCRUD{}
	got, err = coverageRegisteredCustomFamilies(opCtx(), deps2)
	require.Error(t, err)
	require.Len(t, got, 1, "the file half survives an optional catalog failure")
	assert.Equal(t, "from-file", string(got[0].gt))
}

// TestCollectorLoader_ReportsAnUnresolvableHomeLoudly pins that an unresolvable
// home directory FAILS rather than quietly yielding a user scope with no
// entries, which would silently unregister every collector on the machine.
func TestCollectorLoader_ReportsAnUnresolvableHomeLoudly(t *testing.T) {
	prevPath, prevErr := collectorUserConfigPath, collectorUserConfigErr
	collectorUserConfigPath, collectorUserConfigErr = "", assertHomeFailure()
	t.Cleanup(func() { collectorUserConfigPath, collectorUserConfigErr = prevPath, prevErr })

	_, err := collectorLoader(context.Background(), &customDeps{})
	require.Error(t, err, "an unresolvable home must be reported, never degraded to an empty scope")
}

// assertHomeFailure is the sentinel the row above installs.
func assertHomeFailure() error { return os.ErrNotExist }

// TestCustomCollectorList_ShowsAFamilyCollectedUnderAFileEntry is the reason the
// read-only tool is kept, and without this row that reason is untested.
//
// IT IS THE CHAIN, NOT TWO HALVES. A family registered only by a config entry is
// invisible to the server until something is collected under it; after a collect
// the loader's behavior record is there, and `custom_collector(list)` is the only
// surface that reports what the SERVER holds — which is what the routing gate and
// the whole summarize / embed / sync pipeline actually read.
func TestCustomCollectorList_ShowsAFamilyCollectedUnderAFileEntry(t *testing.T) {
	url := startCustomProvider(t, conformingCustomPayload())
	deps := newCustomDeps(t, namedCustomDef("tickets", url))

	// BEFORE: the entry exists and nothing has been collected, so the server
	// holds nothing. Without this arm the assertion below is satisfied by a list
	// that reports every family it can find anywhere.
	handled, body, isErr := callGraphType(t, deps, `{"operation":"list"}`)
	require.True(t, handled)
	require.False(t, isErr, body)
	assert.NotContains(t, body, "tickets",
		"the server holds no record for a family nothing has been collected under yet")

	handled, res := callCollect(deps, `{"type":"tickets","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	handled, body, isErr = callGraphType(t, deps, `{"operation":"list"}`)
	require.True(t, handled)
	require.False(t, isErr, body)
	assert.Contains(t, body, "tickets", "after a collect the family's behavior record is what the server holds")
	assert.Contains(t, body, "true", "and its behavior flags surface, which is the half the pipeline gates on")
}

// TestCoverageTable_ListsALegacyFamilyWithoutMakingItCollectable is E52's point,
// and both halves belong in ONE test: a status row and a resolution are
// different things, and keeping the catalog half of the coverage union is only
// defensible if it changes nothing about what a collect will do.
func TestCoverageTable_ListsALegacyFamilyWithoutMakingItCollectable(t *testing.T) {
	url := startCustomProvider(t, conformingCustomPayload())
	deps := newCustomDeps(t) // NO file entry for this family
	yes := true
	deps.crud = &stubGraphTypeCRUD{defs: []*knowledgev1.GraphTypeDef{{
		Name:     "legacy-family",
		Behavior: &knowledgev1.BehaviorDefaults{Syncable: &yes},
		Collector: &knowledgev1.CollectorSpec{
			Tool:     customStubTool,
			Provider: &knowledgev1.CollectorSpec_Http{Http: &knowledgev1.HttpProvider{Url: url}},
		},
	}}}

	walked, err := coverageRegisteredCustomFamilies(opCtx(), deps)
	require.NoError(t, err)
	names := make([]string, 0, len(walked))
	for _, g := range walked {
		names = append(names, string(g.gt))
	}
	assert.Contains(t, names, "legacy-family",
		"a family collected under an entry since removed keeps its coverage rows")

	handled, res := callCollect(deps, `{"type":"legacy-family","id":"board"}`)
	require.True(t, handled)
	require.True(t, res.IsError,
		"and appearing on the coverage table must NOT make it collectable: the table dispatches nothing")
	assert.Nil(t, deps.sink.last())
}
