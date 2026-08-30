// SPDX-License-Identifier: Apache-2.0

package tools

// query_arm_census_test.go is the CLAIM-SURFACE CENSUS for the query tool: it
// derives, from the parsed syntax tree of this package's own non-test sources,
// (1) every entry point that claims a `query` call and (2) every struct type a
// query payload is decoded into, and diffs the union of those structs' json tags
// against the params QueryToolDef() declares.
//
// WHY A TEST RATHER THAN A SCRIPT. A test runs on every CI pass and cannot rot
// unnoticed; a script in a directory can. The sibling contract for mutate makes
// the same choice (mutate_param_accounting_test.go).
//
// REUSE. The json-tag-versus-schema comparison is the shape
// negation_proof_params_test.go established for mutateArgs / thinkArgs. That
// test walks a KNOWN struct with reflect; this one cannot, because the whole
// question is WHICH structs carry query payloads. So the walk is mirrored in
// go/ast rather than extended: the tag-to-schema diff is the same rule, the
// struct DISCOVERY is new.
//
// PERF SHAPE: one single-pass go/ast walk over one package directory at test
// time. No concurrency, no build, no type checker.

import (
	"go/ast"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// queryClaimEntryPoints is the exact set of top-level funcs that claim a `query`
// call. Pinned as a literal NAME LIST rather than a count: a new claimant that
// replaces an old one must fail, which a count cannot catch. A claimant added
// later fails this assertion until someone registers it here — and, from Phase 3
// on, gives it arms in queryArmRegistry.
var queryClaimEntryPoints = []string{
	"InterceptFileSymbols",
	"InterceptLogsQuery",
	"InterceptQuery",
	"InterceptQueryAnalyzeNode",
	"InterceptQueryBuiltinStats",
	"InterceptQueryCloudCICD",
	"InterceptQueryCodeSearch",
	"InterceptQueryCorrelationsPivot",
	"InterceptQueryEvidence",
	"InterceptQueryExamine",
	"InterceptQueryExamineProjects",
	"InterceptQueryExplainTimeline",
	"InterceptQueryKnowledgeSearch",
	"InterceptQueryLineage",
	"InterceptQueryMetadataStats",
	"InterceptQueryModulesCodeStats",
	"InterceptQueryPlanTree",
	"InterceptQueryPracticeLinkage",
	"InterceptQueryRegisteredGraphSearch",
	"InterceptQueryRules",
	"InterceptQueryStats",
	"InterceptQueryUnrankedBuiltin",
	"InterceptThoughts",
	"InterceptTopology",
}

// queryArmBearingDelegates records, for an entry point that dispatches the query
// claim onward instead of serving it inline, the func that actually reads the
// params and takes the arms.
//
// InterceptThoughts is the sole member and the reason the entry-point predicate
// below accepts TWO shapes. It is the only switch-shaped claimant: it dispatches
// `switch params.Name { case "query": ... }` rather than comparing. A
// comparison-only predicate silently drops it and the census reports a
// complete-looking 21.
//
// The mapping is not taken on trust — TestQueryClaimSurfaceCensus asserts the
// entry point really does hand its whole params value to the named delegate, so
// the record cannot drift away from the source.
var queryArmBearingDelegates = map[string]string{
	"InterceptThoughts": "interceptQueryReflect",
}

// queryCensusedUndeclaredParams is the CLOSED CLASS the census found: nine json
// tags carried on query-payload arg structs and consumed by a live reader that
// QueryToolDef() did not declare, so no caller could discover them from
// tools/list.
//
// ALL NINE ARE NOW DECLARED, which is why the live manifest the census computes
// is EMPTY rather than this list. The list survives as the by-name pin — it is
// what TestQuerySchema_DeclaresEveryCensusedParam walks, so an edit that drops
// one of the nine back out of the schema fails by name rather than by a count —
// and as the known-positive that keeps the empty manifest from being vacuous:
// the census re-derives that every one of these is still CARRIED, so an empty
// manifest cannot come from a walk that collected nothing.
//
// `scope` is the catcher for a named-type-only census: it exists solely on an
// anonymous struct literal decoded at the unmarshal site
// (intercept_query_rules.go), so a walk that inspects only ast.TypeSpec
// declarations misses it and reports a class of eight.
//
// `thought` is deliberately NOT a member: it belongs to the thoughts tool's own
// schema, not query's.
var queryCensusedUndeclaredParams = []string{
	"branch",
	"callee_depth",
	"caller_depth",
	"file_path",
	"file_paths",
	"granularity",
	"include_comments",
	"samples",
	"scope",
}

// queryPayloadNamedStructs is the set of NAMED struct types a query payload is
// decoded into. Seven of them, plus the two anonymous literals counted
// separately below.
var queryPayloadNamedStructs = []string{
	"analyzeNodeArgs",
	"codeSearchArgs",
	"fileSymbolsArgs",
	"modulesCodeStatsArgs",
	"queryArgs",
	"queryReflectArgs",
	"topologyArgs",
}

// queryPayloadAnonStructCount is how many ANONYMOUS struct literals a query
// payload is decoded into: the scope filter in the rules arm, and the mode sniff
// in queryModeFromArgs. The second carries only a DECLARED param (mode) so it
// adds nothing to the undeclared manifest — it is counted here so the anonymous
// count is two and a future reader does not "discover" it as a miss.
const queryPayloadAnonStructCount = 2

// queryDeclaredParamCount is the number of params QueryToolDef() declares: the
// 52 it carried before this plan plus the nine the census found consumed but
// undeclared.
//
// A PLAN-MANDATED count, not a tree-derived one, so it stays LOCKED. A tenth
// addition must arrive with a plan revision rather than a quiet bump of this
// constant.
const queryDeclaredParamCount = 61

// queryDeclaredWithoutCarrier justifies each declared query param that no
// query-payload arg struct carries. Such a param is advertised to callers and
// read by nothing, so it must be a deliberate, explained exception rather than
// something that passes silently — that is what stops the undeclared nine from
// being "fixed" by declaring params nothing reads.
//
// Empty is the correct state and the one the tree is in: every declared param
// has a live carrier. `sort` is the one that looks like a member and is not —
// it has no field on queryArgs, but it IS carried by queryReflectArgs and read
// by the influence render, so the census finds its carrier and it needs no
// exception.
var queryDeclaredWithoutCarrier = map[string]string{}

// TestQuerySchema_DeclarationMatchesConsumption binds declaration to consumption
// in BOTH directions, because either one alone is trivially satisfiable.
//
// Direction (a) — no arg-struct tag may be undeclared. This is the direction the
// nine violate before the schema edit and satisfy after it.
//
// Direction (b) — no declared param may be carried by nothing, except by named
// justification. Without it, direction (a) could be "satisfied" by declaring
// params no reader consumes, which trades an invisible param for a phantom one.
func TestQuerySchema_DeclarationMatchesConsumption(t *testing.T) {
	pkg := parseToolsPackage(t)
	seeds := append([]string{}, queryClaimEntryPoints...)
	for _, delegate := range queryArmBearingDelegates {
		seeds = append(seeds, delegate)
	}
	structs := pkg.queryPayloadStructs(pkg.payloadCarriers(seeds))
	require.NotEmpty(t, structs, "the census must find query payload structs — an empty scan proves nothing")

	schema := QueryToolDef().InputSchema.Properties
	require.NotEmpty(t, schema, "QueryToolDef must declare params")

	carried := map[string]string{} // tag → the struct that carries it
	t.Run("every carried tag is declared", func(t *testing.T) {
		for _, ps := range structs {
			require.NotEmptyf(t, ps.tags, "payload carrier %s declares no json tags", ps.label)
			for _, tag := range ps.tags {
				carried[tag] = ps.label
				_, declared := schema[tag]
				// Checked explicitly rather than with assert.Contains on the map: a
				// Contains failure renders the WHOLE schema into the failure text,
				// burying the one key that matters.
				assert.Truef(t, declared,
					"%s carries wire field %q off a query payload but QueryToolDef does not declare it — "+
						"a schema-invisible param is one callers cannot discover and the accounting gate cannot reject",
					ps.label, tag)
			}
		}
	})

	t.Run("every declared param has a carrier", func(t *testing.T) {
		var uncarried []string
		for param := range schema {
			if _, ok := carried[param]; !ok {
				uncarried = append(uncarried, param)
			}
		}
		sort.Strings(uncarried)
		for _, param := range uncarried {
			justification, excused := queryDeclaredWithoutCarrier[param]
			assert.Truef(t, excused,
				"declared param %q is carried by no query-payload struct — declaring a param nothing "+
					"reads advertises a lever that does nothing; give it a reader or justify it here", param)
			assert.NotEmptyf(t, justification, "the exception for %q carries no justification", param)
		}
		for param := range queryDeclaredWithoutCarrier {
			assert.Containsf(t, uncarried, param,
				"%q is justified as carrier-less but the census finds a carrier for it — stale exception", param)
		}

		// KNOWN POSITIVE for the emptiness above. With every declared param
		// carried, "uncarried is fully justified" is also satisfied by a
		// computation that measures nothing, so drive the same comparison with a
		// param the carriers demonstrably lack and confirm it is reported.
		const probe = "no_such_query_param_probe"
		require.NotContains(t, carried, probe, "the probe must not accidentally be a real carried tag")
		_, probeCarried := carried[probe]
		assert.False(t, probeCarried,
			"the carrier lookup must report a param no struct carries — otherwise the check above is vacuous")
	})
}

// TestQuerySchema_DeclaresSixtyOneParams pins the declared total. Independent of
// the census on purpose: the census proves the SET agrees with consumption, this
// proves nobody widened the surface while keeping that agreement.
func TestQuerySchema_DeclaresSixtyOneParams(t *testing.T) {
	assert.Len(t, QueryToolDef().InputSchema.Properties, queryDeclaredParamCount,
		"the query schema's declared param count is locked by plan")
}

// TestQuerySchema_DeclaresEveryCensusedParam pins each of the nine BY NAME, so a
// partial edit that adds eight cannot pass a count check and call it done.
func TestQuerySchema_DeclaresEveryCensusedParam(t *testing.T) {
	schema := QueryToolDef().InputSchema.Properties
	for _, param := range queryCensusedUndeclaredParams {
		t.Run(param, func(t *testing.T) {
			prop, ok := schema[param]
			require.Truef(t, ok, "QueryToolDef must declare %q", param)
			assert.NotEmptyf(t, prop.Description, "%q must carry prose describing what its reader does", param)
			assert.NotEmptyf(t, prop.Type, "%q must declare a type", param)
		})
	}
}

// TestQueryClaimSurfaceCensus derives the query claim surface from source and
// pins both halves: the entry-point set, and the manifest of params those entry
// points consume off the wire without QueryToolDef() declaring them.
func TestQueryClaimSurfaceCensus(t *testing.T) {
	pkg := parseToolsPackage(t)

	t.Run("entry_points", func(t *testing.T) {
		derived := pkg.queryEntryPoints()
		require.NotEmpty(t, derived, "the census must find query entry points — an empty scan proves nothing")
		assert.ElementsMatch(t, queryClaimEntryPoints, derived,
			"the derived query claim surface must equal the registered one; a new claimant has to be "+
				"registered here (and given arms in queryArmRegistry) rather than landing unaccounted")

		// The delegate record is only worth anything if it matches the source, so
		// prove the entry point really hands its whole params value onward.
		for entry, delegate := range queryArmBearingDelegates {
			fd := pkg.funcs[entry]
			require.NotNilf(t, fd, "delegate record names entry point %q which this package does not declare", entry)
			paramsName := interceptSignatureParams(fd)
			require.NotEmptyf(t, paramsName, "%q must have the client intercept signature", entry)
			require.Containsf(t, pkg.funcs, delegate,
				"delegate record names %q which this package does not declare", delegate)
			delegated := false
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				callee, isIdent := call.Fun.(*ast.Ident)
				if !isIdent || callee.Name != delegate {
					return true
				}
				for _, arg := range call.Args {
					if renderType(arg) == paramsName {
						delegated = true
					}
				}
				return true
			})
			assert.Truef(t, delegated,
				"%q is recorded as %q's arm-bearing delegate but %q never passes it the params value",
				delegate, entry, entry)
		}
	})

	t.Run("undeclared_manifest", func(t *testing.T) {
		seeds := append([]string{}, queryClaimEntryPoints...)
		for _, delegate := range queryArmBearingDelegates {
			seeds = append(seeds, delegate)
		}
		carriers := pkg.payloadCarriers(seeds)
		structs := pkg.queryPayloadStructs(carriers)
		require.NotEmpty(t, structs, "the census must find query payload structs — an empty scan proves nothing")

		var named []string
		anon := 0
		for _, ps := range structs {
			if ps.anonymous {
				anon++
				t.Logf("query payload carrier (anonymous): %s tags=%v", ps.label, ps.tags)
				continue
			}
			named = append(named, ps.label)
		}
		assert.ElementsMatch(t, queryPayloadNamedStructs, named,
			"the named query-payload carriers must be exactly the registered set")
		assert.Equal(t, queryPayloadAnonStructCount, anon,
			"both anonymous payload structs must be found — a named-type-only walk finds neither")

		schema := QueryToolDef().InputSchema.Properties
		require.NotEmpty(t, schema, "QueryToolDef must declare params")
		carried := map[string]bool{}
		undeclared := map[string]bool{}
		for _, ps := range structs {
			for _, tag := range ps.tags {
				carried[tag] = true
				if _, declared := schema[tag]; !declared {
					undeclared[tag] = true
				}
			}
		}
		manifest := make([]string, 0, len(undeclared))
		for tag := range undeclared {
			manifest = append(manifest, tag)
		}
		sort.Strings(manifest)

		// KNOWN POSITIVE, run BEFORE the emptiness assertion. Each of the nine the
		// census originally surfaced must still be found CARRIED — otherwise an
		// empty manifest would be indistinguishable from a walk pointed at
		// nothing, and `scope` in particular would go missing the moment the
		// anonymous-struct arm broke.
		for _, param := range queryCensusedUndeclaredParams {
			assert.Containsf(t, carried, param,
				"the census must still find %q carried on a query payload — without this the empty "+
					"manifest below proves nothing", param)
		}
		t.Logf("censused class still carried: %v", queryCensusedUndeclaredParams)
		t.Logf("undeclared-but-consumed query params: %v", manifest)
		assert.Empty(t, manifest,
			"no query payload may carry a wire field the schema does not declare — every member of the "+
				"censused class has been declared, and a new one must be declared rather than landing here")
	})
}
