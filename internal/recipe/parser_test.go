// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// updateGoldens is the standard `-update` flag consumers are used to from
// gofmt / cmd/gopls. When set, the test regenerates testdata/*.err files
// from current parser output. Otherwise the test compares against the
// committed goldens and fails on drift. Happy-path fixtures only assert
// parse success — they do not pin AST shape, since the AST is an
// implementation detail rather than a contract.
var updateGoldens = flag.Bool("update", false, "regenerate parser golden files in testdata/")

// TestParser_Goldens walks testdata/ and exercises Parse against every
// *.recipe fixture. happy_* fixtures must parse cleanly; err_* fixtures
// must fail with the message in *.err ("line:col: msg").
func TestParser_Goldens(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	require.NoError(t, err)
	var recipes []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".recipe") {
			recipes = append(recipes, e.Name())
		}
	}
	sort.Strings(recipes)
	require.NotEmpty(t, recipes, "testdata/ must contain .recipe fixtures")

	var happyCount, errCount int
	for _, name := range recipes {
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join("testdata", name))
			require.NoError(t, err)

			recipe, parseErr := Parse(src)
			switch {
			case strings.HasPrefix(name, "happy_"):
				happyCount++
				require.NoError(t, parseErr, "happy fixture must parse")
				require.NotNil(t, recipe)
			case strings.HasPrefix(name, "err_"):
				errCount++
				require.Error(t, parseErr, "err fixture must fail")
				goldenPath := filepath.Join("testdata", strings.TrimSuffix(name, ".recipe")+".err")
				assertOrUpdateGolden(t, goldenPath, parseErr.Error()+"\n")
			default:
				t.Fatalf("fixture %q must start with happy_ or err_", name)
			}
		})
	}
	assert.GreaterOrEqual(t, happyCount, 3, "need at least 3 happy-path fixtures")
	assert.GreaterOrEqual(t, errCount, 5, "need at least 5 error fixtures")
}

// assertOrUpdateGolden compares got against the file at path, or, when
// -update is set, writes got as the new golden content.
func assertOrUpdateGolden(t *testing.T, path, got string) {
	t.Helper()
	if *updateGoldens {
		require.NoError(t, os.WriteFile(path, []byte(got), 0o600)) //nolint:gosec // path is testdata/<fixture>.err under -update
		return
	}
	want, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Fatalf("missing golden %q; run `go test -update ./transformer/recipe/`", path)
	}
	require.NoError(t, err)
	assert.Equal(t, string(want), got, "golden mismatch for %q — rerun `go test -update` to regenerate", path)
}
