// SPDX-License-Identifier: Apache-2.0

package bootstrap

// check_daemon_route_args_provenance_test.go — WHOSE VALUE THE ENCODE MARSHALS.
//
// callDaemonTool's three refusals each report a json.Marshal failing, and each
// row calling one of them unreachable rests on what could be in the marshaled
// value. The census reads the value's SHAPE and refuses to vouch for a name or
// for a literal reading one, so every one of the three names a test here instead
// of resting on the code alone.
//
// THE ARGUMENTS, marshaled from a parameter. Three legs, and the claim needs all
// three: the only caller of the marshaling function passes the only caller of the
// face's argument builder; the builder's input struct holds no field of a type
// json cannot encode; and the map the builder returns holds only string, []string
// and bool over a matrix that exercises every optional key.
//
// THE TWO ENVELOPES, marshaled from composite literals reading locals. Their
// literals carry identifiers, so the same three-legged shape applies to each: the
// literal in the source sets only the fields the claim names, every field of the
// struct encodes at its zero value so an unset field cannot fail, and the value
// the call site actually builds encodes — with a known positive in the same run,
// a json.RawMessage that never parsed, so the pass is the value and not the
// assertion.

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestCheckRunToolArgsCarryOnlyEncodableValues is the test the census row for
// "check run: encode the %s arguments: %w" names in valueProvenanceBy.
func TestCheckRunToolArgsCarryOnlyEncodableValues(t *testing.T) {
	t.Run("the caller chain has one link at each hop", func(t *testing.T) {
		callers := packageCallSites(t, "callDaemonTool")
		require.Len(t, callers, 1, "callDaemonTool has more than one caller, so the argument's provenance is no longer one chain: %v", callers)
		assert.Equal(t, "args", lastArg(callers[0]),
			"callDaemonTool is handed something other than runChecksOnDaemon's own parameter")

		entries := packageCallSites(t, "runChecksOnDaemon")
		require.Len(t, entries, 1, "runChecksOnDaemon has more than one caller: %v", entries)
		assert.True(t, strings.HasPrefix(lastArg(entries[0]), "checkRunToolArgs("),
			"runChecksOnDaemon is handed a map some other builder made: %q", lastArg(entries[0]))
	})

	t.Run("the builder's input carries no unencodable field", func(t *testing.T) {
		fields := structFieldTypes(t, "check_subcommand.go", "checkRunFlags")
		require.NotEmpty(t, fields, "control: the field census must find checkRunFlags at all")
		encodable := map[string]bool{"string": true, "int": true, "[]string": true, "*bool": true}
		for name, typ := range fields {
			assert.True(t, encodable[typ],
				"checkRunFlags.%s is %s, a type json.Marshal is not known here to encode — "+
					"the census row calling the encode arm unreachable rests on this set", name, typ)
		}
	})

	t.Run("every value the builder emits is a string, a string slice or a bool", func(t *testing.T) {
		yes, no := true, false
		minimal := checkRunToolArgs(checkRunFlags{repo: "/tmp/repo", language: "go"}, "/tmp/repo")
		full := checkRunToolArgs(checkRunFlags{
			repo:         "/tmp/repo",
			language:     "go",
			pathPrefix:   "cmd/knowledge",
			ids:          []string{"a", "b"},
			files:        []string{"a.go"},
			includeTests: &yes,
			compact:      &no,
		}, "/tmp/repo")

		// The known positive for this leg: the optional arms must actually have
		// run, or the type check below passes over three keys and proves nothing.
		require.Greater(t, len(full), len(minimal), "control: the optional arms must add keys, or this leg is vacuous")
		require.Len(t, minimal, 3, "control: the mandatory keys are operation, repo and language")

		for _, args := range []map[string]any{minimal, full} {
			for key, value := range args {
				switch value.(type) {
				case string, []string, bool:
				default:
					t.Errorf("checkRunToolArgs put %q in the arguments as %T, which is outside the string, []string and bool "+
						"set the census row rests on", key, value)
				}
			}
		}
	})
}

// packageCallSites renders every call to name in the package's NON-TEST files as
// "file:line: <call text>". Test files are excluded deliberately: the claim is
// about what production callers can pass.
func packageCallSites(t *testing.T, name string) []string {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	require.NoError(t, err)
	require.NotEmpty(t, paths, "control: the package's own sources must be readable from the test's working directory")

	fset := token.NewFileSet()
	var sites []string
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := censusReadSource(path)
		require.NoError(t, err)
		file, err := parser.ParseFile(fset, path, src, 0)
		require.NoError(t, err, "parse %s", path)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok || ident.Name != name {
				return true
			}
			sites = append(sites, exprText(fset, src, call))
			return true
		})
	}
	return sites
}

// lastArg reads the final argument of a rendered call site.
func lastArg(site string) string {
	open := strings.Index(site, "(")
	if open < 0 || !strings.HasSuffix(site, ")") {
		return site
	}
	inner := site[open+1 : len(site)-1]
	depth := 0
	for i := len(inner) - 1; i >= 0; i-- {
		switch inner[i] {
		case ')':
			depth++
		case '(':
			depth--
		case ',':
			if depth == 0 {
				return strings.TrimSpace(inner[i+1:])
			}
		}
	}
	return strings.TrimSpace(inner)
}

// structFieldTypes renders the declared field types of one struct as source
// spells them. It reads the DECLARATION rather than a reflect walk of a value so
// a field left at its zero value is still counted.
func structFieldTypes(t *testing.T, path, name string) map[string]string {
	t.Helper()
	src, err := censusReadSource(path)
	require.NoError(t, err)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	require.NoError(t, err, "parse %s", path)

	types := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok || spec.Name.Name != name {
			return true
		}
		structType, ok := spec.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, field := range structType.Fields.List {
			for _, fieldName := range field.Names {
				types[fieldName.Name] = exprText(fset, src, field.Type)
			}
		}
		return false
	})
	return types
}

// TestCheckRunCallParamsCarryOnlyEncodableValues is the test the census row for
// "check run: encode the %s call: %w" names in valueProvenanceBy.
func TestCheckRunCallParamsCarryOnlyEncodableValues(t *testing.T) {
	t.Run("the literal sets only the two fields the claim rests on", func(t *testing.T) {
		assert.Equal(t, []string{"Arguments", "Name"}, literalFieldKeys(t, routeSourceFile, "CallToolParams"),
			"the call params literal sets a field the unreachability claim does not account for")
	})

	t.Run("every field of the struct encodes at its zero value", func(t *testing.T) {
		assertZeroFieldsEncode(t, reflect.TypeFor[kgtools.CallToolParams]())
	})

	t.Run("the value the call site builds encodes, and one that never parsed does not", func(t *testing.T) {
		encodedArgs, err := json.Marshal(checkRunToolArgs(checkRunFlags{repo: "/tmp/repo", language: "go"}, "/tmp/repo"))
		require.NoError(t, err, "control: the arguments the face builds must encode, or the leg below tests nothing")

		_, err = json.Marshal(kgtools.CallToolParams{Name: "manage_checks", Arguments: encodedArgs})
		require.NoError(t, err, "the params the call site builds must encode, or the row calling that arm unreachable is wrong")

		// THE KNOWN POSITIVE, same run and same call: this encode CAN fail, so the
		// pass above is the RawMessage's provenance and not json.Marshal being
		// incapable of refusing a CallToolParams at all.
		_, err = json.Marshal(kgtools.CallToolParams{Name: "manage_checks", Arguments: json.RawMessage("{not json")})
		require.Error(t, err, "control: a json.RawMessage that never parsed must fail this encode")
	})
}

// TestCheckRunRequestEnvelopeCarriesOnlyEncodableValues is the test the census
// row for "check run: encode the %s request: %w" names in valueProvenanceBy.
func TestCheckRunRequestEnvelopeCarriesOnlyEncodableValues(t *testing.T) {
	t.Run("the literal sets only the four fields the claim rests on", func(t *testing.T) {
		assert.Equal(t, []string{"ID", "JSONRPC", "Method", "Params"}, literalFieldKeys(t, routeSourceFile, "JSONRPCRequest"),
			"the request literal sets a field the unreachability claim does not account for")
	})

	t.Run("every field of the struct encodes at its zero value", func(t *testing.T) {
		assertZeroFieldsEncode(t, reflect.TypeFor[kgtools.JSONRPCRequest]())
	})

	t.Run("the id constant the request carries is itself valid JSON", func(t *testing.T) {
		// The ID field is a json.RawMessage converted from this constant, so the
		// envelope encodes only if the constant parses. It is checked here rather
		// than asserted in the row's prose because the constant is one edit away
		// from being a bare word.
		assert.True(t, json.Valid([]byte(checkDaemonToolCallID)),
			"checkDaemonToolCallID is %q, which is not JSON, so the request envelope would not encode", checkDaemonToolCallID)
	})

	t.Run("the value the call site builds encodes, and one that never parsed does not", func(t *testing.T) {
		params, err := json.Marshal(kgtools.CallToolParams{Name: "manage_checks", Arguments: json.RawMessage(`{"operation":"run"}`)})
		require.NoError(t, err, "control: the params must encode, or the leg below tests nothing")

		_, err = json.Marshal(kgtools.JSONRPCRequest{
			JSONRPC: "2.0",
			ID:      json.RawMessage(checkDaemonToolCallID),
			Method:  "tools/call",
			Params:  params,
		})
		require.NoError(t, err, "the request the call site builds must encode, or the row calling that arm unreachable is wrong")

		_, err = json.Marshal(kgtools.JSONRPCRequest{JSONRPC: "2.0", ID: json.RawMessage("{not json"), Method: "tools/call"})
		require.Error(t, err, "control: a json.RawMessage that never parsed must fail this encode")
	})
}

// literalFieldKeys reads the field names one composite literal of the named type
// sets in a source file, sorted. The literal must appear EXACTLY ONCE, so a
// second one cannot make this read the wrong site.
func literalFieldKeys(t *testing.T, path, typeName string) []string {
	t.Helper()
	src, err := censusReadSource(path)
	require.NoError(t, err)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	require.NoError(t, err, "parse %s", path)

	var found [][]string
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != typeName {
			return true
		}
		var keys []string
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			require.True(t, ok, "%s carries a positional element, which this reader does not name", typeName)
			key, ok := kv.Key.(*ast.Ident)
			require.True(t, ok, "%s carries a key that is not a field name", typeName)
			keys = append(keys, key.Name)
		}
		sort.Strings(keys)
		found = append(found, keys)
		return true
	})
	require.Len(t, found, 1, "%s must build exactly one %s literal, or this leg reads the wrong one", path, typeName)
	return found[0]
}

// assertZeroFieldsEncode marshals the ZERO VALUE of every field of a struct, so a
// field the routing file never sets cannot be what makes the encode fail. It is
// executed rather than declared: a field whose type json refuses reds here.
func assertZeroFieldsEncode(t *testing.T, typ reflect.Type) {
	t.Helper()
	require.Equal(t, reflect.Struct, typ.Kind())
	require.NotZero(t, typ.NumField(), "control: the field walk must find fields at all")
	for field := range typ.Fields() {
		_, err := json.Marshal(reflect.New(field.Type).Elem().Interface())
		assert.NoError(t, err, "%s.%s is %s, whose zero value json.Marshal refuses — the census row calling the encode arm "+
			"unreachable rests on every field of this struct encoding", typ.Name(), field.Name, field.Type)
	}
}
