// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"reflect"
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
// THE ASSERTION IS DELIBERATELY ONE-DIRECTIONAL: read is a SUBSET of declared.
// That is the only direction a rejection can break. Declared-but-unread is a
// documentation nicety, and asserting it here would report phantom
// over-declarations on search, whose intercept decodes a narrow SNIFF struct
// while the rest of its declared params are consumed by downstream helpers.
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
	"ids", "older_than", "type", "session_id", "dry_run", "hard", "graph", "language", "format",
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
		{tool: "hive", def: HiveToolDef, structs: []any{hiveArgs{}}},
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
			structs: []any{searchArgs{}},
			literals: []string{
				"rerank",  // anonymous struct decoded directly from the payload
				"query",   // anonymous struct in normalizeQueriesToQuery, payload passed verbatim
				"queries", // same site as query
			},
			creditedElsewhere: map[string]string{
				"text": "query's spelling of the same concept, folded in only because search and query share codeSearchArgs; " +
					"declaring it here would publish a duplicate spelling of the declared `query`",
				"id": "consulted solely in the query arm's own gate; the shared compose path never reads it",
			},
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
		{tool: "worker", def: WorkerToolDef, structs: []any{workerArgs{}}},
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
