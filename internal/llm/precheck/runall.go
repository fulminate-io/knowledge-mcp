// SPDX-License-Identifier: Apache-2.0

package precheck

import (
	"context"
	"errors"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// RunAll fans out every configured precheck (each LLM provider tuple
// plus Voyage when set) and runs them concurrently. Each check has
// its own timeout (CheckLLMs uses pingTimeout per tuple; CheckVoyage
// uses voyagePingTimeout) so total wall-clock time is bounded by the
// slowest single check, not the sum.
//
// All check errors are collected and returned as a joined error so
// the operator's startup log lists every failed precheck in one
// pass — even when several providers are broken in different ways.
//
// Returns nil only when every check succeeded (or returned nil for
// an opt-out path like missing VOYAGE_API_KEY).
func RunAll(ctx context.Context, cfg *config.Config, consumers []config.Consumer, voyageKey string) error {
	if cfg == nil {
		return errors.New("precheck.RunAll: nil config")
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

	// Voyage runs in parallel with the LLM checks. Empty key returns
	// nil immediately (BM25-only mode) so this goroutine costs nothing
	// when Voyage isn't configured.
	g.Go(func() error {
		if err := CheckVoyage(gctx, voyageKey); err != nil {
			collect(err)
		}
		return nil
	})

	// Wait can never return non-nil because no goroutine returns an
	// error — they all stuff their errors into the joined slice.
	_ = g.Wait()

	return errors.Join(errs...)
}
