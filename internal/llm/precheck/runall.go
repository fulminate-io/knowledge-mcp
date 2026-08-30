// SPDX-License-Identifier: Apache-2.0

package precheck

import (
	"context"
	"errors"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// RerankCheck is the rerank axis's startup check, supplied by the caller.
//
// IT IS A PARAMETER RATHER THAN A DIRECT CALL because this package does not
// import the rerank package: rerank imports engine, and engine imports
// graphclient, so naming rerank from this leaf would pull the whole graph
// client in behind a startup precheck (the chain is spelled out in
// voyage.go). The composition root supplies the real implementation,
// rerank.CheckProvider, which is the package that owns the arm.
//
// It is REQUIRED, not optional. There is deliberately no variadic or
// zero-value shape: a check that silently stops running is the failure
// mode this indirection could otherwise introduce, so RunAll REFUSES a nil
// rather than skipping the axis.
type RerankCheck func(ctx context.Context, sec config.RerankSection) error

// RunAll fans out every configured precheck — each LLM provider tuple,
// plus the EMBED axis and the RERANK axis — and runs them concurrently.
// Each check has its own timeout (CheckLLMs uses pingTimeout per tuple;
// the axis checks use their own bounds) so total wall-clock time is
// bounded by the slowest single check, not the sum.
//
// checkRerank is REQUIRED. Passing nil is a programming error and returns
// an error naming the caller's duty — it is never treated as "skip the
// rerank axis", because a check that quietly does not run is worse than no
// check at all.
//
// The axis targets are DERIVED FROM THE OTHER SECTIONS rather than from a
// precheck section of their own: RunAll resolves [embedder] and [reranker]
// off the config it already receives, the same discipline uniqueTuples
// applies to the LLM consumers.
//
// All check errors are collected and returned as a joined error so
// the operator's startup log lists every failed precheck in one
// pass — even when several providers are broken in different ways.
//
// Returns nil only when every check succeeded (or returned nil for
// an opt-out path — an axis with no credential and no base_url).
func RunAll(ctx context.Context, cfg *config.Config, consumers []config.Consumer, checkRerank RerankCheck) error {
	if cfg == nil {
		return errors.New("precheck.RunAll: nil config")
	}
	if checkRerank == nil {
		return errors.New("precheck.RunAll: nil rerank check — the caller must pass rerank.CheckProvider; the rerank axis is not optional and a nil check is never treated as skip")
	}

	tuples, err := uniqueTuples(cfg, consumers)
	if err != nil {
		// Resolve errors are independent of the runtime checks but
		// still load-bearing — surface them and skip the network calls.
		// Returning here matches the previous CheckLLMs sequential
		// behavior: bad config never gets to ping.
		return err
	}

	g, gctx := errgroup.WithContext(ctx)
	var (
		mu   sync.Mutex
		errs []error
	)
	collect := func(e error) {
		if e == nil {
			return
		}
		mu.Lock()
		errs = append(errs, e)
		mu.Unlock()
	}

	// Fan out one goroutine per LLM tuple. Errors are collected via
	// the mutex-guarded slice rather than returned from the goroutine
	// so a single failure doesn't cancel the errgroup's other in-flight
	// pings — we want EVERY broken provider named in one log line.
	for _, t := range tuples {
		g.Go(func() error {
			collect(checkOne(gctx, t))
			return nil
		})
	}

	// BOTH axis checks run IN PARALLEL with the LLM checks and with each
	// other, so adding the rerank axis costs no additional wall-clock. An
	// axis with no credential and no base_url returns nil immediately
	// without calling anything, so these goroutines cost nothing when that
	// axis isn't configured. A resolve error on either section is collected
	// like any other check failure rather than skipping the check silently.
	g.Go(func() error {
		sec, err := cfg.ResolveEmbedder()
		if err != nil {
			collect(err)
			return nil
		}
		collect(CheckEmbedProvider(gctx, sec))
		return nil
	})
	g.Go(func() error {
		sec, err := cfg.ResolveReranker()
		if err != nil {
			collect(err)
			return nil
		}
		collect(checkRerank(gctx, sec))
		return nil
	})

	// Wait can never return non-nil because no goroutine returns an
	// error — they all stuff their errors into the joined slice.
	_ = g.Wait()

	return errors.Join(errs...)
}
