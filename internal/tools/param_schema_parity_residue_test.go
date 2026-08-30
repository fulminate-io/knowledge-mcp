// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// param_schema_parity_residue_test.go asserts the SCHEMA HONESTY contract for
// every client tool outside the mutate/create family: each key the client code
// actually READS off a caller payload must be DECLARED in that tool's own
// published ToolDef.
//
// WHY IT MATTERS MORE THAN DOCUMENTATION TIDINESS. Once a tool rejects
// undeclared top-level params, the published schema stops being a description
// and becomes the accepted-key contract. A schema that UNDER-declares — a param
// the client reads but the schema omits — turns into a refusal of correct calls
// the instant that rejection is wired.
//
// BOTH DIRECTIONS ARE ASSERTED, AND THE SECOND ONE ARRIVED LATER. Read-is-a-
// subset-of-declared is the direction a rejection can break: an under-declared
// key refuses a correct caller the instant that rejection is wired. The converse
// — declared-is-read-or-explicitly-exempt — catches the opposite defect: a
// schema that promises a param nothing consumes, which is a coerced input
// dressed as a feature.
//
// THE EARLIER RATIONALE FOR OMITTING THE CONVERSE IS PRESERVED HERE BECAUSE IT
// WAS HALF RIGHT, and the half it got right is why the second direction needs
// the table columns rather than a bare set difference. It read: "Declared-but-
// unread is a documentation nicety, and asserting it here would report phantom
// over-declarations on search, whose intercept decodes a narrow SNIFF struct
// while the rest of its declared params are consumed by downstream helpers."
// The prediction was correct — the converse direction DOES report search keys
// that are genuinely read further down the path — but the conclusion was wrong
// twice. Those reports are not phantoms, they are ROW DEFECTS, repaired by
// listing the downstream struct in the row's `structs` slice; and the residue
// after those repairs is not a documentation nicety, because keys remained that
// no decode site reads at all. A schema promising those is not untidy, it is
// false.
//
// TWO COLUMNS PER TOOL, AND THE SECOND IS LOAD-BEARING. Column one is the named
// arg structs the tool's intercepts decode, read by reflection. Column two is a
// HAND-ADDED LITERAL key list — the definitional home for every key reached
// through a shape reflection cannot see: an anonymous inline struct, or a helper
// that takes the payload as a json.RawMessage under a different name. A census
// keyed on `json.Unmarshal(params.Arguments, &x)` is blind to exactly those, and
// that blindness is what hid manage's `keys` from an earlier sweep.
//
// THE READ-SURFACE RULE, stated once: a tool's read surface is EVERY struct that
// receives params.Arguments verbatim — directly, through a verbatim handoff, or
// through a helper taking it as a json.RawMessage parameter — named or anonymous.
// A handler reached from a tool's mode whose payload the client MANUFACTURES
// (marshaling a fresh map) is NOT part of that tool's read surface, because no
// caller ever supplies those keys.

// deleteReadKeys is the LOCKED read-key set of the `delete` tool: the union of
// the json tags on the engine-side deleteArgs struct and the literal "format"
// read by both delete render paths.
//
// THIS IS SIDE A OF A TWO-SIDED HANDSHAKE. delete's arguments are decoded
// client-side but in package engine, and deleteArgs is unexported, so no test in
// this package can reflect it. The matching assertion lives in package engine as
// TestDeleteArgs_ReadKeySetIsLocked, which reflects deleteArgs and repeats this
// list. The repetition IS the contract: a field added on one side and not the
// other fails whichever side was not updated.
var deleteReadKeys = []string{
	"ids", "id", "older_than", "type", "session_id", "dry_run", "hard", "graph", "language", "repo", "account", "format",
}

// residueParityCase is one tool's row in the parity table.
type residueParityCase struct {
	// tool is both the subtest name and the tool's advertised name.
	tool string
	// def is the tool's published schema accessor — the live Properties map,
	// never a frozen copy, so a schema addition is honored with no edit here.
	def func() kgtools.MCPTool
	// structs holds zero values of every named arg struct this tool's intercepts
	// decode from a verbatim payload. Reflected for json tags.
	structs []any
	// literals holds keys read through a shape reflection cannot reach. Each
	// carries a comment naming its exact decode site.
	literals []string
	// creditedElsewhere names keys that appear on a SHARED struct but belong to a
	// DIFFERENT tool's code path, mapped to the reason. A key on a struct two
	// tools decode is credited to the tool whose code actually consults it; when
	// both consult it, both declare it.
	creditedElsewhere map[string]string
	// exactKeys, when non-empty, tightens the assertion from subset to EXACT set
	// equality against the declared properties. Used where the read surface is
	// locked rather than merely covered.
	exactKeys []string
	// declaredOnly marks a tool whose payload is consumed entirely by a renderer
	// this package cannot reflect; the row asserts only that the tool declares
	// something, so the table cannot silently carry an empty row.
	declaredOnly bool
	// declaredNotRead names keys this tool DECLARES and deliberately does not
	// read, mapped to the written justification for keeping the promise unbacked.
	// It is the converse direction's only escape hatch, and it is a written one:
	// an EMPTY value is a hard test failure, never an exemption, so the cell
	// cannot decay into a silent allowlist.
	//
	// NO ROW SETS IT TODAY, and that is the intended resting state rather than an
	// oversight: the three keys it was introduced for were removed from the search
	// schema instead of exempted. It stays because the next declared-but-unread key
	// needs somewhere to be justified out loud; with no members the empty-value
	// assertion below has nothing to range over, so a future entry is the thing
	// that puts it back to work.
	declaredNotRead map[string]string
}

// residueParityTable is the per-tool read surface, one row per tool outside the
// mutate/create family.
func residueParityTable() []residueParityCase {
	return []residueParityCase{
		{tool: "analyze_usage", def: AnalyzeUsageToolDef, structs: []any{analyzeUsageArgs{}}},
		// assemble has no decode of its own: the render layer owns the payload.
		{tool: "assemble", def: AssembleToolDef, declaredOnly: true},
		{tool: "ast", def: AstToolDef, structs: []any{astArgs{}}},
		{tool: "collect", def: CollectToolDef, structs: []any{collectArgs{}}},
		{tool: "custom_collector", def: GraphTypeToolDef, structs: []any{graphTypeArgs{}}},
		// delete: the struct lives in package engine and is unexported. See
		// deleteReadKeys and its handshake partner TestDeleteArgs_ReadKeySetIsLocked.
		{tool: "delete", def: DeleteToolDef, literals: deleteReadKeys, exactKeys: deleteReadKeys},
		{
			tool: "file_symbols", def: FileSymbolsToolDef,
			structs: []any{fileSymbolsArgs{}},
			creditedElsewhere: map[string]string{
				"mode":          "consulted only inside the query-mode branch of InterceptFileSymbols; declared by query",
				"path_prefix":   "consulted only inside the query-mode branch of InterceptFileSymbols; declared by query",
				"path_prefixes": "consulted only inside the query-mode branch of InterceptFileSymbols; declared by query",
				"graph":         "not consulted anywhere in InterceptFileSymbols; declared by query",
			},
		},
		{
			tool: "help", def: HelpToolDef,
			literals: []string{
				"topic", // anonymous struct in handleHelpClient, which receives the payload verbatim
			},
		},
		{
			tool: "manage", def: ManageToolDef,
			structs: []any{manageArgs{}},
			literals: []string{
				"keys", // anonymous struct in promoteMetadataKeySet, reached with the payload verbatim
			},
		},
		{
			tool: "query", def: QueryToolDef,
			structs: []any{
				queryArgs{}, queryReflectArgs{}, analyzeNodeArgs{}, codeSearchArgs{},
				modulesCodeStatsArgs{}, topologyArgs{}, fileSymbolsArgs{}, simulateClientArgs{},
			},
			literals: []string{
				"scope", // anonymous struct decoded directly from the payload in the rules arm
			},
		},
		{tool: "record_decision", def: RecordDecisionToolDef, structs: []any{recordDecisionArgs{}}},
		{
			tool: "search", def: SearchToolDef,
			// The search payload is decoded by ONE sniff struct and then again,
			// verbatim, by each arm's own view of it. All four are the read surface.
			structs: []any{
				searchArgs{}, codeSearchArgs{}, segmentSearchArgs{}, searchReducibleArgs{},
			},
			literals: []string{
				"rerank",  // anonymous struct decoded directly from the payload
				"query",   // anonymous struct in normalizeQueriesToQuery, payload passed verbatim
				"queries", // same site as query
				// The staleness opt-in and the two values it fills are read off the
				// decoded per-key map in InjectRepoIfCodeGraph, never through a
				// named struct — the shape jsonTagKeys cannot see.
				"staleness",         // decodeBoolField(args, "staleness"), search-only, intercept_repo.go
				"current_head",      // populateStaleness presence-check: a caller-supplied value is preserved
				"uncommitted_count", // same site, same explicit-value-preservation read
				// resource_type is read by engine.searchArgs, which compileSearch
				// decodes from the payload verbatim and dispatch threads into the
				// render post-filter. Unexported in package engine, so it is reached
				// here as a literal for the same reason delete's whole key set is.
				"resource_type",
			},
			creditedElsewhere: map[string]string{
				"text": "query's spelling of the same concept, folded in only because search and query share codeSearchArgs; " +
					"declaring it here would publish a duplicate spelling of the declared `query`",
				"id":        "consulted solely in the query arm's own gate; the shared compose path never reads it",
				"half_life": "tunes the query tool's recency rerank; rides segmentSearchArgs, which both tools decode, and search publishes no recency knob",
				"meta": "a QUERY-TOOL-ONLY carrier on segmentSearchArgs, stated as such at its field; the search tool " +
					"publishes no `meta` param, so it decodes empty on every search arm",
			},
			// declaredNotRead is deliberately absent on this row. It briefly held
			// include_tombstones, explain and commits_behind; all three were
			// retired from SearchToolDef instead of exempted, so search now leaves
			// the declared-but-unread population by construction rather than by
			// written permission, and the entries would have become lies.
		},
		{tool: "sync", def: SyncToolDef, structs: []any{syncArgs{}}},
		{
			tool: "thoughts", def: ThoughtsToolDef,
			structs: []any{
				thoughtsArgs{}, thinkArgs{}, chargeArgs{}, traceClientArgs{}, recallClientArgs{},
				propagateArgs{}, similarityReportArgs{}, adjacencyClientArgs{}, chargesForClientArgs{},
			},
		},
		{tool: "traverse", def: TraverseToolDef, structs: []any{traverseArgs{}}},
	}
}

// TestResidueToolSchemas_DeclareEveryReadKey is the parity contract: for every
// residue tool, every key the client reads is declared in the tool's schema.
//
// The failure message names the tool and every offending key, sorted, so one run
// reports the whole gap rather than the first item of it.
func TestResidueToolSchemas_DeclareEveryReadKey(t *testing.T) {
	for _, tc := range residueParityTable() {
		t.Run(tc.tool, func(t *testing.T) {
			declared := tc.def().InputSchema.Properties
			require.NotEmptyf(t, declared, "tool %q declares no params at all", tc.tool)

			if tc.declaredOnly {
				return
			}

			if len(tc.exactKeys) > 0 {
				assert.ElementsMatch(t, tc.exactKeys, sortedPropertyKeys(declared),
					"tool %q must declare EXACTLY its locked read-key set", tc.tool)
			}

			read := map[string]struct{}{}
			for _, s := range tc.structs {
				for _, k := range jsonTagKeys(t, s) {
					read[k] = struct{}{}
				}
			}
			for _, k := range tc.literals {
				read[k] = struct{}{}
			}
			for k := range tc.creditedElsewhere {
				delete(read, k)
			}
			require.NotEmptyf(t, read, "tool %q has an empty read surface — the table row reads nothing", tc.tool)

			var undeclared []string
			for k := range read {
				if _, ok := declared[k]; !ok {
					undeclared = append(undeclared, k)
				}
			}
			sort.Strings(undeclared)
			assert.Emptyf(t, undeclared,
				"tool %q READS these params but its schema does not DECLARE them: %s — "+
					"a wired undeclared-param rejection would refuse correct callers supplying any of them",
				tc.tool, strings.Join(undeclared, ", "))
		})
	}
}

// TestResidueToolSchemas_ReadEveryDeclaredKey is the converse parity contract:
// for every residue tool, every key the schema DECLARES is read by some decode
// site on that tool's path — or is classified, in writing, as declared and
// deliberately unread.
//
// The failure message names the tool and every offending key, sorted, so one run
// reports the whole over-declaration rather than the first item of it.
func TestResidueToolSchemas_ReadEveryDeclaredKey(t *testing.T) {
	for _, tc := range residueParityTable() {
		t.Run(tc.tool, func(t *testing.T) {
			declared := tc.def().InputSchema.Properties
			require.NotEmptyf(t, declared, "tool %q declares no params at all", tc.tool)

			if tc.declaredOnly {
				return
			}

			for _, k := range slices.Sorted(maps.Keys(tc.declaredNotRead)) {
				assert.NotEmptyf(t, strings.TrimSpace(tc.declaredNotRead[k]),
					"tool %q exempts %q with an EMPTY justification — declaredNotRead is a "+
						"written exemption, and a blank one is a silent allowlist", tc.tool, k)
			}

			read := map[string]struct{}{}
			for _, s := range tc.structs {
				for _, k := range jsonTagKeys(t, s) {
					read[k] = struct{}{}
				}
			}
			for _, k := range tc.literals {
				read[k] = struct{}{}
			}

			var unread []string
			for _, k := range sortedPropertyKeys(declared) {
				if _, ok := read[k]; ok {
					continue
				}
				if _, ok := tc.creditedElsewhere[k]; ok {
					continue
				}
				if _, ok := tc.declaredNotRead[k]; ok {
					continue
				}
				unread = append(unread, k)
			}
			assert.Emptyf(t, unread,
				"tool %q DECLARES these params but no decode site READS them: %s — "+
					"each is either a row defect (the struct that reads it is missing from this "+
					"row), a key credited to another tool's path, or a promise the schema makes "+
					"and nothing keeps",
				tc.tool, strings.Join(unread, ", "))
		})
	}
}

// jsonTagKeys reflects a struct's json tag names. Fields with no tag, or tagged
// "-", carry no wire key and are skipped.
func jsonTagKeys(t *testing.T, v any) []string {
	t.Helper()
	rt := reflect.TypeOf(v)
	require.Equalf(t, reflect.Struct, rt.Kind(), "%T is not a struct", v)

	keys := make([]string, 0, rt.NumField())
	for f := range rt.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		keys = append(keys, name)
	}
	return keys
}

// sortedPropertyKeys renders a properties map's key set for set-equality
// assertions.
func sortedPropertyKeys(props map[string]kgtools.Property) []string {
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
