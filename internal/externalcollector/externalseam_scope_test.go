// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// externalseam_scope_test.go — the seam's SCOPE, proven by observing the command
// each test's registration is constructed with rather than by observing that the
// tests still pass. A scoping test that only checked the six still passed would
// pass against a complete external collector too, which is the state the seam is
// designed to produce.

// TestExternalCollectorSeam_RedirectsOnlyTheElevenDialingTests names every one of
// the seventeen tests that build a stdio registration, ONE BY ONE, and asserts
// the command each would be given with the seam on.
func TestExternalCollectorSeam_RedirectsOnlyTheElevenDialingTests(t *testing.T) {
	const external = "/ful1819/external-collector"
	t.Setenv(externalCollectorEnv, external+" --serve")

	self, err := os.Executable()
	require.NoError(t, err)

	require.NotEmpty(t, dialingTests, "the seam's subject is the provider-dialing tests")
	require.Len(t, stdioTestsKeepingTheGoStub, 6, "six further tests share the construction point and are about the Go harness")

	for _, name := range dialingTests {
		t.Run("redirected/"+name, func(t *testing.T) {
			reg := stdioDefFor(t, name, stubModeConforming, nil)
			stdio := reg.Def.GetCollector().GetStdio()
			assert.Equal(t, external, stdio.GetCommand(),
				"%s dials a provider, so with the seam on it must dial the external command", name)
			assert.Equal(t, []string{"--serve"}, stdio.GetArgs(),
				"the arguments the variable carries ride with the command, and the Go stub's argv marker does not")
		})
	}

	for _, name := range stdioTestsKeepingTheGoStub {
		t.Run("kept-on-the-go-stub/"+name, func(t *testing.T) {
			reg := stdioDefFor(t, name, stubModeConforming, nil)
			stdio := reg.Def.GetCollector().GetStdio()
			assert.Equal(t, self, stdio.GetCommand(),
				"%s is about the Go harness itself and must keep the re-execed stub whatever the seam says", name)
			assert.Equal(t, []string{stubArgvMarker}, stdio.GetArgs(),
				"and it must keep the argv marker, which is what stops a child without its mode from running the suite")
		})
	}
}

// TestExternalCollectorSeam_OffTheGoStubIsUnchanged is the seam's default arm:
// with the variable unset every one of the seventeen builds the same
// registration it built before the seam existed.
func TestExternalCollectorSeam_OffTheGoStubIsUnchanged(t *testing.T) {
	require.NoError(t, os.Unsetenv(externalCollectorEnv))
	self, err := os.Executable()
	require.NoError(t, err)

	for _, name := range append(append([]string{}, dialingTests...), stdioTestsKeepingTheGoStub...) {
		reg := stdioDefFor(t, name, stubModeConforming, map[string]string{declaredEnv: "v"})
		stdio := reg.Def.GetCollector().GetStdio()
		assert.Equal(t, self, stdio.GetCommand(), "%s with the seam off must spawn this test binary", name)
		assert.Equal(t, []string{stubArgvMarker}, stdio.GetArgs())
		assert.Equal(t, []string{declaredEnv + "=v", stubModeEnv + "=" + stubModeConforming}, stdio.GetEnv(),
			"the entry's env block is unchanged by the seam, and stays sorted")
	}
}

// TestExternalCollectorSeam_TheEnvBlockRidesToTheExternalCommand pins the half a
// port depends on: the mode reaches the external collector the same way it
// reaches the Go stub, through the entry's env block.
func TestExternalCollectorSeam_TheEnvBlockRidesToTheExternalCommand(t *testing.T) {
	t.Setenv(externalCollectorEnv, "/ful1819/external-collector")
	reg := stdioDefFor(t, dialingTests[0], stubModeEnvReport, map[string]string{declaredEnv: "v"})
	stdio := reg.Def.GetCollector().GetStdio()
	assert.Equal(t, []string{declaredEnv + "=v", stubModeEnv + "=" + stubModeEnvReport}, stdio.GetEnv(),
		"an external collector learns which mode to serve exactly as the Go stub does")
	assert.Empty(t, stdio.GetArgs(), "a command with no arguments in the variable gets none")
}

// TestExternalCollectorSeam_TheTwoListsCoverEveryTestThatBuildsAStdioStub is the
// anti-drift census: it reads this package's own test source, finds every test
// function that reaches the stdio construction point, and requires the two
// declared lists to account for exactly those. A twelfth dialing test added
// later is silently outside the seam without it.
func TestExternalCollectorSeam_TheTwoListsCoverEveryTestThatBuildsAStdioStub(t *testing.T) {
	found := testsCalling(t, "stdioDef", "stdioDefEnv")
	require.NotEmpty(t, found, "the source scan found no caller at all, which is what a broken scan looks like")

	declared := map[string]string{}
	for _, n := range dialingTests {
		declared[n] = "dialing"
	}
	for _, n := range stdioTestsKeepingTheGoStub {
		require.NotContains(t, declared, n, "%s cannot be in both lists", n)
		declared[n] = "kept"
	}

	var undeclared, missing []string
	for _, name := range found {
		if _, ok := declared[name]; !ok {
			undeclared = append(undeclared, name)
		}
	}
	seen := map[string]bool{}
	for _, name := range found {
		seen[name] = true
	}
	for name := range declared {
		if !seen[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(undeclared)
	sort.Strings(missing)
	assert.Empty(t, undeclared,
		"these tests build a stdio registration and neither list names them, so the seam's scope is undeclared for them")
	assert.Empty(t, missing,
		"these tests are declared in a seam list and no longer build a stdio registration")
}

// TestExternalCollectorSeam_TheModeVocabularyIsTheConstBlock proves the skip
// list's vocabulary is the modes the stub actually serves, read from the const
// block rather than from a copy someone remembered to update.
func TestExternalCollectorSeam_TheModeVocabularyIsTheConstBlock(t *testing.T) {
	declared := stubModeConstants(t)
	require.NotEmpty(t, declared, "the stub harness serves the modes its const block declares")
	sorted := append([]string{}, stubModes...)
	sort.Strings(sorted)
	assert.Equal(t, declared, sorted,
		"stubModes must be exactly the stubMode* constants the harness serves; the skip list is checked against it")
}

// testsCalling returns every top-level Test function in this package's test
// files whose body calls one of the named helpers, at any nesting depth.
func testsCalling(t *testing.T, helpers ...string) []string {
	t.Helper()
	wanted := map[string]bool{}
	for _, h := range helpers {
		wanted[h] = true
	}
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	require.NoError(t, err, "parsing this package's own source")

	var out []string
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			if !strings.HasSuffix(path, "_test.go") {
				continue
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") {
					continue
				}
				calls := false
				ast.Inspect(fn, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if ident, ok := call.Fun.(*ast.Ident); ok && wanted[ident.Name] {
						calls = true
					}
					return true
				})
				if calls {
					out = append(out, fn.Name.Name)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// stubModeConstants returns the values of the stubMode* constants declared in
// stubprovider_test.go, excluding stubModeEnv, which names the switch VARIABLE
// rather than a mode.
func stubModeConstants(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(".", "stubprovider_test.go"), nil, 0)
	require.NoError(t, err)

	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 {
			return true
		}
		name := spec.Names[0].Name
		if !strings.HasPrefix(name, "stubMode") || name == "stubModeEnv" {
			return true
		}
		lit, ok := spec.Values[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		out = append(out, strings.Trim(lit.Value, `"`))
		return true
	})
	sort.Strings(out)
	return out
}
