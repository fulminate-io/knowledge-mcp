// SPDX-License-Identifier: Apache-2.0

package tools

// manage_checks_files_test.go — the FILE-LIST scope and the compact render AS
// THE CALLER MEETS THEM.
//
// Every row goes through InterceptManageChecks with a real JSON payload, because
// the two halves that break independently are both on that path: the schema must
// declare the params (or rejectUndeclaredParams refuses the call before any
// scan), and the args struct must read them (or they are accepted and dropped —
// which is exactly what format does on this same tool today).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/corpusscan"
)

// checksRunFilesFixture writes a two-package tree with one site per package and
// records it under a repo name, so the site count says which files a scope
// opened: 2 unnarrowed, 1 for either half.
func checksRunFilesFixture(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/filesfixture\n\ngo 1.22\n"), 0o600))
	for _, dir := range []string{"alpha", "beta"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0o750))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "alpha", "a.go"), []byte(
		"package alpha\n\nfunc one() { fmt.Println(1) }\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "beta", "b.go"), []byte(
		"package beta\n\nfunc two() { fmt.Println(2) }\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "beta", "notes.md"), []byte("# notes\n"), 0o600))
	m := withTestManifest(t)
	require.NoError(t, m.Record("filesfixture", root))
}

// TestManageChecks_RunFilesScopeScansOnlyTheNamedFiles is requirement 1 at the
// MCP face, with the unnarrowed run as the control.
func TestManageChecks_RunFilesScopeScansOnlyTheNamedFiles(t *testing.T) {
	checksRunFilesFixture(t)

	whole := driveChecksRun(t, runChecksArgs(t, "filesfixture", nil))
	require.False(t, whole.IsError, "the unnarrowed run must succeed: %s", whole.Content[0].Text)
	assert.Contains(t, whole.Content[0].Text, "sites_flagged=2",
		"control: the tree holds one fmt.Println site in each of its two packages")

	narrowed := driveChecksRun(t, runChecksArgs(t, "filesfixture", map[string]any{
		"files": []string{"alpha/a.go"},
	}))
	require.False(t, narrowed.IsError, "the file-list run must succeed: %s", narrowed.Content[0].Text)
	body := narrowed.Content[0].Text
	assert.Contains(t, body, "sites_flagged=1", "naming one file must scan only that file")
	assert.Contains(t, body, "alpha/a.go")
	assert.NotContains(t, body, "beta/b.go",
		"a file the caller did not name must not be scanned — a scope that resolves every path and then walks the tree defeats the param")
}

// TestManageChecks_RunFilesScopeDefaultsToTheCompactRender is requirement 2's
// default, and its control is the SAME run asking for the full form: the two
// must agree on the verdict and differ only in the body.
func TestManageChecks_RunFilesScopeDefaultsToTheCompactRender(t *testing.T) {
	checksRunFilesFixture(t)

	compact := driveChecksRun(t, runChecksArgs(t, "filesfixture", map[string]any{
		"files": []string{"alpha/a.go"},
	}))
	require.False(t, compact.IsError, "the run must succeed: %s", compact.Content[0].Text)
	full := driveChecksRun(t, runChecksArgs(t, "filesfixture", map[string]any{
		"files": []string{"alpha/a.go"}, "compact": false,
	}))
	require.False(t, full.IsError, "the run must succeed: %s", full.Content[0].Text)

	compactBody, fullBody := compact.Content[0].Text, full.Content[0].Text

	// THE VERDICT LINE IS IDENTICAL. A render form that moved a counter would be
	// a classification change wearing a render's clothes.
	assert.Equal(t, strings.SplitN(compactBody, "\n", 2)[0], strings.SplitN(fullBody, "\n", 2)[0],
		"compact and full must carry the same verdict line — the render never reaches the fold")

	assert.Contains(t, compactBody, "alpha/a.go:3\t", "the compact body carries one tab-separated row per site")
	assert.NotContains(t, compactBody, "seeded run-fixture check",
		"and it drops the check's prose description, which the full form repeats on every site")
	assert.Contains(t, fullBody, "seeded run-fixture check",
		"control: the full form DOES carry the description, so the absence above is the compact form working")
	assert.Less(t, len(compactBody), len(fullBody),
		"the compact body must actually be smaller, or the form is a rename")
}

// TestManageChecks_RunCompactRowsStayWithinTheDocumentedBound is requirement 1's
// render bound, measured with len() over the string this face returns.
func TestManageChecks_RunCompactRowsStayWithinTheDocumentedBound(t *testing.T) {
	checksRunFilesFixture(t)
	res := driveChecksRun(t, runChecksArgs(t, "filesfixture", map[string]any{
		"files": []string{"alpha/a.go", "beta/b.go"},
	}))
	require.False(t, res.IsError, "the run must succeed: %s", res.Content[0].Text)

	rows := 0
	for line := range strings.SplitSeq(res.Content[0].Text, "\n") {
		if strings.Count(line, "\t") != 3 {
			continue
		}
		rows++
		assert.LessOrEqualf(t, len(line), corpusscan.CompactRowBoundAst,
			"every ast row must be at or under the documented %d-byte bound, got %d: %q",
			corpusscan.CompactRowBoundAst, len(line), line)
	}
	require.Equal(t, 2, rows, "both named files hold a site, or the bound above was asserted over nothing")
}

// TestManageChecks_RunFilesScopeRefusals walks the refusal arms at this face.
// Each asserts the message NAMES the offending value, and that NO verdict line
// was rendered — sites_flagged= is a shape no refusal prose produces, which the
// sibling scope test explains at length.
func TestManageChecks_RunFilesScopeRefusals(t *testing.T) {
	checksRunFilesFixture(t)

	for _, tc := range []struct {
		name  string
		extra map[string]any
		want  []string
	}{
		{"both scope params", map[string]any{"files": []string{"alpha/a.go"}, "path_prefix": "alpha"}, []string{"files", "path_prefix"}},
		{"present but empty", map[string]any{"files": []string{}}, []string{"files"}},
		{"absent path", map[string]any{"files": []string{"alpha/nosuch.go"}}, []string{"alpha/nosuch.go"}},
		{"absolute path", map[string]any{"files": []string{"/etc/passwd"}}, []string{"/etc/passwd"}},
		{"escaping path", map[string]any{"files": []string{"alpha/../../x.go"}}, []string{"x.go"}},
		{"not the walk's spelling", map[string]any{"files": []string{"./alpha/a.go"}}, []string{"./alpha/a.go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := driveChecksRun(t, runChecksArgs(t, "filesfixture", tc.extra))
			require.Truef(t, res.IsError, "%s must be refused: %s", tc.name, res.Content[0].Text)
			for _, want := range tc.want {
				assert.Containsf(t, res.Content[0].Text, want, "the refusal must name %q", want)
			}
			assert.NotContains(t, res.Content[0].Text, "sites_flagged=",
				"a refused call scanned nothing and must render no verdict line for it")
		})
	}
}

// TestManageChecks_RunFilesScopeDisclosesAPathOfAnotherLanguage: a real diff
// carries markdown, and the path is dropped from the walk and NAMED.
func TestManageChecks_RunFilesScopeDisclosesAPathOfAnotherLanguage(t *testing.T) {
	checksRunFilesFixture(t)
	res := driveChecksRun(t, runChecksArgs(t, "filesfixture", map[string]any{
		"files": []string{"beta/b.go", "beta/notes.md"},
	}))
	require.False(t, res.IsError, "the run proceeds over the rest: %s", res.Content[0].Text)
	body := res.Content[0].Text
	assert.Contains(t, body, "beta/notes.md", "a dropped path must be named in the report, never silently absent")
	assert.Contains(t, body, "sites_flagged=1", "and the disclosure is not counted as a flagged site")
}

// TestManageChecks_RunPathPrefixRenderIsUnchanged is the regression row: the
// other scope channel still defaults to the full body, and asking for compact
// there works too — the two axes are independent.
func TestManageChecks_RunPathPrefixRenderIsUnchanged(t *testing.T) {
	checksRunFilesFixture(t)

	full := driveChecksRun(t, runChecksArgs(t, "filesfixture", map[string]any{"path_prefix": "alpha"}))
	require.False(t, full.IsError, "the prefix run must succeed: %s", full.Content[0].Text)
	assert.Contains(t, full.Content[0].Text, "seeded run-fixture check",
		"a path_prefix scope still defaults to the FULL body — this change adds a form, it does not switch one")

	compact := driveChecksRun(t, runChecksArgs(t, "filesfixture", map[string]any{
		"path_prefix": "alpha", "compact": true,
	}))
	require.False(t, compact.IsError, "the prefix run must succeed compacted too: %s", compact.Content[0].Text)
	assert.Contains(t, compact.Content[0].Text, "alpha/a.go:3\t",
		"the compact form is available on every scope, not only on a file list")
}
