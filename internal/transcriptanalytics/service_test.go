// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestService_LazyOpenAndCachePaths proves the Phase 2 contract: an EMPTY cache
// short-circuits to zero paths WITHOUT opening DuckDB (no throw, no panic); a
// populated cache resolves to the explicit local file list; and the lazily-opened
// engine runs SELECT 1 and read_parquets the explicit paths back to rows.
func TestService_LazyOpenAndCachePaths(t *testing.T) {
	root := t.TempDir()
	svc, err := NewService(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	// Empty cache: filepath.Glob zero-match → empty result, and DuckDB is never
	// opened (the lazy pool is still nil).
	paths, err := svc.cachePaths()
	require.NoError(t, err)
	require.Empty(t, paths, "an empty cache resolves to zero explicit paths")
	require.Nil(t, svc.db, "cachePaths never touched DuckDB")

	// Populate the fixed {source}/{session}.parquet layout with one fixture file.
	srcDir := filepath.Join(root, "src")
	require.NoError(t, os.MkdirAll(srcDir, 0o750))
	writeFixtureParquet(t, filepath.Join(srcDir, "S.parquet"))

	paths, err = svc.cachePaths()
	require.NoError(t, err)
	require.Len(t, paths, 1, "the glob returns the one explicit cache file")

	// Lazy open + SELECT 1 through the bounded-conn path.
	var one int
	require.NoError(t, svc.queryRow(context.Background(), "SELECT 1", nil, func(r *sql.Row) error {
		return r.Scan(&one)
	}))
	require.Equal(t, 1, one)
	require.NotNil(t, svc.db, "the first query lazily opened DuckDB")

	// read_parquet over the EXPLICIT paths returns the fixture rows.
	var n int
	require.NoError(t, svc.queryRow(context.Background(),
		"SELECT COUNT(*) FROM "+fromClause(paths), nil, func(r *sql.Row) error {
			return r.Scan(&n)
		}))
	require.Positive(t, n, "read_parquet over the explicit path list returned rows")
}

// writeFixtureParquet writes a tiny one-row parquet to path via a throwaway DuckDB
// (the "duckdb" driver is registered by service.go's blank import).
func writeFixtureParquet(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	//nolint:gosec // path is a t.TempDir() fixture location, not user input.
	_, err = db.ExecContext(context.Background(),
		"COPY (SELECT 'S' AS session_id, 100 AS input_tokens) TO "+quoteLiteral(path)+" (FORMAT parquet)")
	require.NoError(t, err)
}
