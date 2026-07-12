// SPDX-License-Identifier: Apache-2.0

// Package transcriptanalytics is the daemon-local analytical query engine over the
// persistent enriched-transcript parquet cache the sync path writes under
// ~/.knowledge/transcripts-cache/{source}/{session}.parquet.
//
// It opens ONE in-memory DuckDB (database/sql driver "duckdb") LAZILY on first use,
// so a daemon that never runs the analyzer pays no DuckDB/CGO startup cost. Each
// query resolves the cache to an EXPLICIT list of local parquet paths via Go-side
// filepath.Glob and read_parquets those local files — no httpfs, no network, no
// credential. The corpus is this machine's own single-user cache, so there is no
// account/user isolation layer here (that lives in the cloud engine, which reads a
// per-user object prefix).
//
// The engine DEGRADES rather than panics: a DuckDB open/ping failure surfaces a
// typed error and an EMPTY cache short-circuits to an empty result WITHOUT touching
// DuckDB — a transient analytics fault must never crash the daemon.
package transcriptanalytics

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	// Importing the package registers the database/sql driver named "duckdb"
	// (CGO_ENABLED=1; prebuilt static libs ship per-platform via duckdb-go-bindings).
	_ "github.com/duckdb/duckdb-go/v2"
)

// Per-query resource bounds. In DuckDB `threads` and `memory_limit` are GLOBAL (not
// session-scoped) settings — a SET on any connection changes the whole instance.
// Applying them per query is safe ONLY because every query uses these same UNIFORM
// constants, so concurrent queries cannot fight over the value.
const (
	queryThreads     = 2
	queryMemoryLimit = "512MB"
)

// Service holds the lazily-opened in-memory DuckDB pool and the root of the local
// parquet cache it reads. Zero DuckDB work happens until the first query.
type Service struct {
	cacheRoot string

	once    sync.Once
	db      *sql.DB
	openErr error
}

// NewService builds an analyzer over the given cache root. An empty cacheRoot
// resolves to the default ~/.knowledge/transcripts-cache (mirroring the sync-side
// cache writer). Opening DuckDB is deferred to the first query.
func NewService(cacheRoot string) (*Service, error) {
	root := cacheRoot
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return nil, fmt.Errorf("transcriptanalytics: resolve home dir for cache root: %w", err)
		}
		root = filepath.Join(home, ".knowledge", "transcripts-cache")
	}
	return &Service{cacheRoot: root}, nil
}

// Close releases the DuckDB pool if it was ever opened. Safe on a nil Service and on
// an analyzer whose lazy open never fired.
func (s *Service) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// conn lazily opens + pings the in-memory DuckDB the first time it is called and
// returns the shared pool. The open/ping failure is memoized so a broken driver
// degrades on every call rather than re-attempting. database/sql's pool gives safe
// concurrent reads; DuckDB does intra-query parallelism internally.
func (s *Service) conn(ctx context.Context) (*sql.DB, error) {
	s.once.Do(func() {
		db, err := sql.Open("duckdb", "")
		if err != nil {
			s.openErr = fmt.Errorf("transcriptanalytics: open duckdb: %w", err)
			return
		}
		if err := db.PingContext(ctx); err != nil {
			_ = db.Close()
			s.openErr = fmt.Errorf("transcriptanalytics: duckdb ping: %w", err)
			return
		}
		s.db = db
	})
	return s.db, s.openErr
}

// cachePaths resolves the local parquet cache to an EXPLICIT list of file paths via
// a Go-side glob over the fixed 2-level {source}/{session}.parquet layout. It returns
// an empty slice (never an error) when nothing matches — the caller short-circuits on
// len==0 WITHOUT opening DuckDB, so a zero-match glob never reaches read_parquet
// (which would throw on an empty set). filepath.Glob has no ** recursion, but the
// cache layout is exactly two levels deep so */*.parquet is precise.
func (s *Service) cachePaths() ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(s.cacheRoot, "*", "*.parquet"))
	if err != nil {
		return nil, fmt.Errorf("transcriptanalytics: glob cache %q: %w", s.cacheRoot, err)
	}
	return matches, nil
}

// fromClause renders the read_parquet table function over an EXPLICIT set of local
// parquet paths (never a remote/DuckDB glob). Paths are emitted as a SQL list literal
// of single-quote-escaped locals; union_by_name tolerates benign column-order or
// pre-enrichment column drift across files.
func fromClause(paths []string) string {
	quoted := make([]string, len(paths))
	for i, p := range paths {
		quoted[i] = quoteLiteral(p)
	}
	return "read_parquet([" + strings.Join(quoted, ", ") + "], union_by_name = true)"
}

// quoteLiteral escapes a string as a single-quoted SQL literal by doubling embedded
// quotes. Used for the local read_parquet path list + the SET memory_limit bound —
// never for row data (which is bound as `?`).
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// queryRow grabs a pooled connection, applies the uniform per-query resource bounds,
// and runs a single-row query on THAT connection.
func (s *Service) queryRow(ctx context.Context, query string, args []any, scan func(*sql.Row) error) error {
	return s.withBoundedConn(ctx, func(conn *sql.Conn) error {
		return scan(conn.QueryRowContext(ctx, query, args...))
	})
}

// queryRows grabs a pooled connection, applies the bounds, and streams rows.
func (s *Service) queryRows(ctx context.Context, query string, args []any, scan func(*sql.Rows) error) error {
	return s.withBoundedConn(ctx, func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			if err := scan(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	})
}

// withBoundedConn lazily opens the engine, checks out one pooled connection, SETs the
// uniform per-query thread/memory bounds on it, runs fn, and returns the connection
// to the pool.
func (s *Service) withBoundedConn(ctx context.Context, fn func(*sql.Conn) error) error {
	db, err := s.conn(ctx)
	if err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	for _, stmt := range []string{
		fmt.Sprintf("SET threads = %d", queryThreads),
		fmt.Sprintf("SET memory_limit = %s", quoteLiteral(queryMemoryLimit)),
	} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("transcriptanalytics: apply query bound %q: %w", stmt, err)
		}
	}
	return fn(conn)
}
