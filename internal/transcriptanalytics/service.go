// SPDX-License-Identifier: Apache-2.0

// Package transcriptanalytics is the daemon-local analytical query engine over the
// persistent enriched-transcript parquet cache the sync path writes under
// ~/.knowledge/transcripts-cache/{source}/{session}.parquet.
//
// It is a PURE-GO in-memory aggregator: each run globs the cache to an EXPLICIT
// list of local parquet paths via Go-side filepath.Glob, decodes them file-by-file in
// parallel via transcripts.ReadSessionParquet, folds every kept row into a corpus-wide
// accumulator model (corpus.go — mirroring the agent's usageanalytics read tables), and
// materializes the deterministic detector families from those accumulators. There is no
// DuckDB, no CGO for analytics, no SQL, no network, and no credential — the corpus is this
// machine's own single-user cache. Account/user isolation lives only in the cloud engine
// (which reads a per-user object prefix); here there is none to enforce.
//
// The engine DEGRADES rather than panics: an EMPTY cache short-circuits to an empty result
// WITHOUT decoding, and a decode fault surfaces a typed error — a transient analytics fault
// must never crash the daemon. Memory stays bounded: one file's rows are resident per
// decode goroutine at a time, then collapsed into the accumulator.
package transcriptanalytics

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/sync/errgroup"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// Service holds the root of the local parquet cache the analyzer reads. It carries no
// connection pool or lazy state — every run decodes the cache fresh, so the zero-cost
// path (a daemon that never runs the analyzer) is simply never calling loadCorpus.
type Service struct {
	cacheRoot string
}

// NewService builds an analyzer over the given cache root. An empty cacheRoot resolves to
// the default ~/.knowledge/transcripts-cache (mirroring the sync-side cache writer).
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

// Close is a no-op retained for the UsageAnalyzerAPI interface + the defer sites that
// still call it; the pure-Go engine holds no resource to release.
func (s *Service) Close() error {
	return nil
}

// cachePaths resolves the local parquet cache to an EXPLICIT list of file paths via a
// Go-side glob over the fixed 2-level {source}/{session}.parquet layout. It returns an
// empty slice (never an error) when nothing matches — the caller short-circuits on len==0
// WITHOUT decoding. filepath.Glob has no ** recursion, but the cache layout is exactly two
// levels deep so */*.parquet is precise.
func (s *Service) cachePaths() ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(s.cacheRoot, "*", "*.parquet"))
	if err != nil {
		return nil, fmt.Errorf("transcriptanalytics: glob cache %q: %w", s.cacheRoot, err)
	}
	return matches, nil
}

// loadCorpus decodes the whole local cache into a single corpus-wide accumulator. It globs
// the explicit paths, then decodes files IN PARALLEL (parquet decode is CPU-bound) via an
// errgroup capped at runtime.NumCPU() — the same NumCPU pool shape the agent uses
// (rollup_insights.go:226). Each goroutine reads ONE file's rows, applies the baseline
// Filters.keep (synthetic-model + is_meta==true excluded; missing/false is_meta KEPT), and
// folds them into a PRIVATE partial corpus; the partials are merged AFTER Wait() (which
// establishes happens-before, so the fan-out is race-free) via the associative
// corpus.merge. An EMPTY glob short-circuits to an empty corpus and a nil error without
// decoding. Bounded memory: at most NumCPU files' rows resident at once.
func (s *Service) loadCorpus(ctx context.Context) (*corpus, error) {
	paths, err := s.cachePaths()
	if err != nil {
		return nil, err
	}
	agg := newCorpus()
	if len(paths) == 0 {
		return agg, nil
	}

	// Baseline filter only: RunDetectors is unfiltered, so a zero Filters applies just the
	// synthetic-model + is_meta baseline exclusions uniformly at intake.
	var base Filters
	partials := make([]*corpus, len(paths))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	for i, p := range paths {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			rows, err := transcripts.ReadSessionParquet(p)
			if err != nil {
				return err
			}
			part := newCorpus()
			for j := range rows {
				if base.keep(rows[j]) {
					part.add(rows[j])
				}
			}
			partials[i] = part
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	for _, part := range partials {
		if part != nil {
			agg.merge(part)
		}
	}
	return agg, nil
}
