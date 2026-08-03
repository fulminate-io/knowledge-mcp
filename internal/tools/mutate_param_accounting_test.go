// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMutateParamAccounting_EveryArmHasExactlyOneGateCallSite is the
// two-independent-measurements check: the registry's key set and the AST
// call-site census must agree. Neither a hand-maintained list nor a
// formatting-sensitive text scan is trusted — the sites are read out of the
// parsed syntax tree of the package's own non-test sources.
//
// Exactly one gate call per arm, no arm ungated, no gate naming an undeclared
// arm.
func TestMutateParamAccounting_EveryArmHasExactlyOneGateCallSite(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	fset := token.NewFileSet()
	callSites := map[string]int{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.SkipObjectResolution)
		require.NoErrorf(t, perr, "parsing %s", name)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn, ok := call.Fun.(*ast.Ident)
			if !ok || fn.Name != "accountMutateParams" || len(call.Args) == 0 {
				return true
			}
			argIdent, ok := call.Args[0].(*ast.Ident)
			if !ok {
				return true
			}
			callSites[argIdent.Name]++
			return true
		})
	}

	require.NotEmpty(t, callSites, "the AST scan must find gate call sites")
	for arm := range mutateArmRegistry {
		assert.Equalf(t, 1, callSites[string(arm)],
			"arm %q must have EXACTLY ONE gate call site, found %d", arm, callSites[string(arm)])
	}
	for site, count := range callSites {
		_, declared := mutateArmRegistry[armID(site)]
		assert.Truef(t, declared,
			"gate call site names %q which is not a declared arm (found %d times)", site, count)
	}
	assert.Len(t, callSites, len(mutateArmRegistry),
		"the call-site census and the registry must name the same arm set")
}

// withRawArgs seeds the caller's verbatim payload on a mutateArgs literal so a
// test that calls an arm handler DIRECTLY (bypassing InterceptMutate, which
// seeds it at the dispatch entry) exercises the same accounting the production
// path does. There is deliberately no production helper synthesizing raw from
// the struct: the struct cannot distinguish an absent key from an empty one,
// which is the whole reason the raw carrier exists.
func withRawArgs(a mutateArgs, raw string) mutateArgs {
	a.raw = json.RawMessage(raw)
	return a
}

// TestMutateParamAccounting_EveryOperationEnumValueHasAnArm proves the registry
// spans the live operation vocabulary in both directions: every arm names an
// operation the schema actually declares (so a renamed operation cannot leave an
// arm pointing at nothing), and every declared operation is claimed by at least
// one arm (so a newly-added operation has no dispatch path without an accounting
// cell).
func TestMutateParamAccounting_EveryOperationEnumValueHasAnArm(t *testing.T) {
	opProp, ok := mutateProperties()["operation"]
	require.True(t, ok, "operation must be a declared mutate param")
	require.NotEmpty(t, opProp.Enum, "operation must declare its enum")

	declared := make(map[string]bool, len(opProp.Enum))
	for _, op := range opProp.Enum {
		declared[op] = true
	}

	claimed := make(map[string]bool, len(declared))
	for arm, spec := range mutateArmRegistry {
		assert.Truef(t, declared[spec.operation],
			"arm %q names operation %q which is absent from the live schema enum", arm, spec.operation)
		assert.NotEmptyf(t, spec.handler, "arm %q must name its owning handler", arm)
		claimed[spec.operation] = true
	}

	for op := range declared {
		assert.Truef(t, claimed[op],
			"schema operation %q is claimed by no arm — every operation needs a dispatch path with param accounting", op)
	}
}

// TestMutateParamAccounting_SuppliedParamsReadsCallerKeys proves
// suppliedMutateParams answers "what did the caller send", not "what is non-zero
// on the decoded struct": present-and-non-empty keys are reported, every
// semantically-empty spelling is treated as absent, keys absent from the mutate
// schema are still surfaced (the schema filter lives on the rejected-set side,
// not here), and a malformed payload returns nil so the gate can fail closed.
func TestMutateParamAccounting_SuppliedParamsReadsCallerKeys(t *testing.T) {
	t.Run("reports present non-empty keys only", func(t *testing.T) {
		got := suppliedMutateParams(json.RawMessage(
			`{"operation":"update","id":"n-1","status":"completed","metadata":{"k":"v"},"ids":["a","b"]}`))
		require.NotNil(t, got)
		assert.Equal(t, map[string]bool{
			"operation": true, "id": true, "status": true, "metadata": true, "ids": true,
		}, got)
	})

	t.Run("every empty spelling counts as absent", func(t *testing.T) {
		got := suppliedMutateParams(json.RawMessage(
			`{"operation":"update","name":"","status":null,"weight":0,"concludes":false,` +
				`"metadata":{},"links":[],"description":"  "}`))
		require.NotNil(t, got)
		// Only operation survives; description is whitespace, which is a
		// non-empty string and therefore genuinely supplied.
		assert.Equal(t, map[string]bool{"operation": true, "description": true}, got)
		for _, absent := range []string{"name", "status", "weight", "concludes", "metadata", "links"} {
			assert.NotContains(t, got, absent, "%q is semantically empty and must read as unsupplied", absent)
		}
	})

	// The reader must surface an undeclared key, because that is what lets the
	// OTHER side of the gate protect it: a key with no cell in any arm's sets can
	// never be classified rejected.
	//
	// There is no longer a LIVE instance of an undeclared-but-consumed param to
	// pin this against, and that is the point rather than a gap: `supports` was
	// closed by declaring it, and `verified_quote`/`cited_range` were closed the
	// same way and are now classified on all 21 arms. The class being empty is
	// exactly what TestNegationProofParams_NoUndeclaredWireField now enforces
	// durably. So the reader-side property is pinned against a SYNTHETIC key that
	// is not a schema param at all — which keeps the property under test without
	// requiring a live defect to exist for it to be meaningful.
	t.Run("surfaces keys the schema does not declare", func(t *testing.T) {
		const synthetic = "not_a_mutate_param_at_all"
		got := suppliedMutateParams(json.RawMessage(`{"operation":"create","` + synthetic + `":"a value"}`))
		require.NotNil(t, got)
		assert.Contains(t, got, synthetic,
			"suppliedMutateParams filters on presence, not on the schema — an undeclared key is still surfaced")
		_, declared := mutateProperties()[synthetic]
		assert.False(t, declared, "the probe key must be genuinely absent from the schema for this case to mean anything")
	})

	t.Run("malformed payload returns nil so the gate fails closed", func(t *testing.T) {
		assert.Nil(t, suppliedMutateParams(json.RawMessage(`{"operation":`)))
		assert.Nil(t, suppliedMutateParams(nil))
	})
}

// TestMutateParamAccounting_EveryDeliberateIgnoreIsJustified enforces the one
// rule that keeps the deliberately-ignored class honest: a param parked there is
// a STATED decision, never an unexamined gap. The map value IS the
// justification, so an entry with an empty value is a param someone declined to
// classify while making it look classified.
//
// The class is deliberately OPEN-ENDED — the client-terminal-arms rule alone
// puts `format` on many arms, and the rubric adds more as cells are derived. So
// this asserts a property of every entry and never a count: a size assertion
// would be a scheduled false failure the next time a correct entry is added.
func TestMutateParamAccounting_EveryDeliberateIgnoreIsJustified(t *testing.T) {
	schema := mutateProperties()
	seen := 0
	for arm, spec := range mutateArmRegistry {
		for param, justification := range spec.deliberatelyIgnored {
			seen++
			assert.NotEmpty(t, strings.TrimSpace(justification),
				"arm %q parks %q in deliberatelyIgnored with no justification — "+
					"state the mechanism that makes ignoring it correct, or reject the param",
				arm, param)
			assert.Contains(t, schema, param,
				"arm %q justifies %q, which is not a live schema param — a stale cell",
				arm, param)
		}
	}
	require.Positive(t, seen, "no deliberately-ignored entries were examined — the walk is vacuous")
}

// vacuousSelectorCommand is a criterion command that RunSelectorGuard rejects:
// it selects tests by name with -run but asserts nothing about whether the
// selector matched, so it exits 0 identically for a passing test and an absent
// one.
const vacuousSelectorCommand = `go test ./internal/tools/ -run '^TestSomeName$'`

// TestAccountParams_ExcludesMutateSelectorGuard proves the SHARED accounting
// primitive does not carry mutate's criterion-command guard.
//
// The exclusion is load-bearing rather than cosmetic. The guard checks the
// SHAPE of a `command` metadata value on a criterion payload — a concept a READ
// does not have — and its cost is a SECOND full json.Unmarshal of the same
// bytes, so admitting it into the primitive every query arm calls would double
// the per-call parse for no reachable benefit.
//
// Both halves are measured, because either alone is weak: the behavioral half
// could pass against a guard that simply never fires, and the structural half
// could pass against a guard that moved somewhere else on the same path.
func TestAccountParams_ExcludesMutateSelectorGuard(t *testing.T) {
	const raw = `{"operation":"create_batch","nodes":[{"type":"finding","name":"n","summary":"s",` +
		`"metadata":{"command":"` + vacuousSelectorCommand + `"}}]}`

	t.Run("the mutate wrapper still rejects it", func(t *testing.T) {
		// KNOWN POSITIVE for the nil below: without this, "accountParams returned
		// nil" is equally consistent with a guard that never fires on any input.
		err := accountMutateParams(armCreateBatch, withRawArgs(mutateArgs{Operation: "create_batch"}, raw))
		require.Error(t, err, "the criterion-command guard must still fire on the mutate path")
		assert.Contains(t, err.Error(), "asserts nothing about WHETHER THE SELECTOR MATCHED",
			"and it must be the selector guard firing, not some other rejection")
	})

	t.Run("the shared primitive accepts it", func(t *testing.T) {
		assert.NoError(t, accountParams(mutateArmRegistry, "mutate", armCreateBatch, json.RawMessage(raw)),
			"accountParams classifies params and nothing else — the criterion-command guard is the "+
				"wrapper's, so the identical payload passes here")
	})

	t.Run("neither symbol appears in the primitive", func(t *testing.T) {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "mutate_param_accounting.go", nil, parser.SkipObjectResolution)
		require.NoError(t, err)

		bodies := map[string]*ast.FuncDecl{}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
				bodies[fn.Name.Name] = fn
			}
		}
		require.Contains(t, bodies, "accountParams")
		require.Contains(t, bodies, "accountMutateParams")

		mentions := func(fn *ast.FuncDecl) (payload, guard bool) {
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch target := call.Fun.(type) {
				case *ast.Ident:
					if target.Name == "payloadCommands" {
						payload = true
					}
				case *ast.SelectorExpr:
					if pkg, isIdent := target.X.(*ast.Ident); isIdent &&
						pkg.Name == "validate" && target.Sel.Name == "RunSelectorGuard" {
						guard = true
					}
				}
				return true
			})
			return payload, guard
		}

		primitivePayload, primitiveGuard := mentions(bodies["accountParams"])
		assert.False(t, primitivePayload, "accountParams must not reach payloadCommands")
		assert.False(t, primitiveGuard, "accountParams must not reach validate.RunSelectorGuard")

		// The structural walk's own known positive: it has to be able to SEE both
		// symbols, or its two false assertions above prove only that it looked in
		// the wrong place.
		wrapperPayload, wrapperGuard := mentions(bodies["accountMutateParams"])
		assert.True(t, wrapperPayload, "accountMutateParams still calls payloadCommands")
		assert.True(t, wrapperGuard, "accountMutateParams still calls validate.RunSelectorGuard")
	})
}
