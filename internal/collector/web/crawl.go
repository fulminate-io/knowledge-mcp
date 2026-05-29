// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"runtime/debug"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// crawl performs a breadth-first web crawl starting from opts.SeedURLs,
// bounded by opts.MaxDepth (link-following hops from each seed) and
// opts.MaxPages (total pages fetched across the whole crawl). Discovered
// internal links are filtered through opts.FollowPatterns (compiled via
// ParseFollowPatterns) — an empty pattern list means "accept every
// internal link". Seed URLs are never filtered; they are always visited.
//
// The returned []*pageRecord contains one entry per page successfully
// fetched + parsed, in BFS visitation order. urlToID maps every URL that
// produced a pageRecord (both the pre-redirect URL and its FinalURL) to
// that page's stable node ID (as produced by emit_nodes.go's stableID
// with kind="page"), so Collect() can rewrite internal-link edge targets
// from the "web:url:<absolute>" placeholder to the real page-node ID.
//
// Per-URL failures (fetch / clean / parse errors) are logged via slog and
// skipped — they do not abort the crawl. The only terminal errors are
// context cancellation and a FollowPatterns compile failure, both
// returned via the third return value.
func crawl(
	ctx context.Context,
	fc *fetchClient,
	opts CrawlOptions,
) ([]*pageRecord, map[string]string, []*knowledgev1.Node, []kgwire.BatchEdge, error) {
	patterns, err := ParseFollowPatterns(opts.FollowPatterns)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("crawl: compile follow patterns: %w", err)
	}

	state := newCrawlState(opts, patterns)
	for _, seed := range opts.SeedURLs {
		state.enqueueSeed(seed)
	}

	workers := opts.MaxConcurrency
	if workers <= 0 {
		workers = 1
	}

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("web.crawl: worker panic recovered",
						"err", r, "stack", string(debug.Stack()))
					state.shutdown()
				}
			}()
			for {
				if err := ctx.Err(); err != nil {
					state.shutdown()
					return
				}
				item, ok := state.nextWork()
				if !ok {
					return
				}
				state.processURL(ctx, fc, item.url, item.depth)
				state.workerDone()
			}
		})
	}
	wg.Wait()

	// Drain materialized temp-dir cleanups before returning. The temp
	// dirs MUST outlive parser.PopulateForExternalGraph (which runs
	// inside materializeGithub during the BFS), but they MUST NOT
	// outlive the collector call.
	defer state.drainMaterializedCleanups()

	if err := ctx.Err(); err != nil {
		return state.pages, state.urlToID, state.matNodes, state.matEdges, err
	}
	if state.budgetWasExhausted() {
		slog.Warn("web.crawl: MaxPages budget exhausted — crawl truncated",
			"max_pages", state.maxPages, "fetched", len(state.pages))
	}
	return state.pages, state.urlToID, state.matNodes, state.matEdges, nil
}

// drainMaterializedCleanups runs every cleanup func registered by
// materializeGithub and clears the slice. Safe to call multiple times.
func (s *crawlState) drainMaterializedCleanups() {
	s.mu.Lock()
	cleanups := s.materializedCleanups
	s.materializedCleanups = nil
	s.mu.Unlock()
	for _, fn := range cleanups {
		if fn != nil {
			fn()
		}
	}
}

// crawlState holds the mutable bookkeeping of a single crawl invocation:
// the FIFO queue, the visited set, the accumulated pages, the URL→ID
// map, and the compiled follow-pattern allowlist. hashToURL tracks
// content-hash aliases so identical-body duplicates under different URLs
// are dropped (see processURL). hostPageCount tracks per-host fetch
// counts for the MaxPagesPerHost cap.
//
// All state mutations (queue/visited/pages/urlToID/hashToURL/hostPageCount)
// go through methods that acquire mu. The crawl() worker pool reads and
// mutates state concurrently; the per-worker fetch call itself happens
// OUTSIDE this mutex — fetchClient has its own per-host synchronization.
//
// inFlight + cond implement the termination protocol: a worker that
// finds an empty queue with inFlight > 0 waits on cond (another worker
// may still enqueue discoveries). Workers exit cleanly when queue is
// empty AND inFlight == 0 (nobody will enqueue more). shutdown() sets
// closed and broadcasts so all waiters bail on ctx cancellation.
type crawlState struct {
	mu              sync.Mutex
	cond            *sync.Cond
	queue           []crawlItem
	visited         map[string]struct{}
	pages           []*pageRecord
	urlToID         map[string]string
	patterns        []*regexp.Regexp
	hashToURL       map[string]string
	hostPageCount   map[string]int
	inFlight        int
	closed          bool
	budgetHit       bool
	maxDepth        int
	maxPages        int
	maxPathSegments int
	maxPagesPerHost int
	opts            CrawlOptions

	// github materialization state — populated by materializeGithub when
	// a github URL is dispatched. matNodes / matEdges are appended to the
	// CollectResult after the BFS drains. materializedCleanups holds the
	// os.RemoveAll funcs returned by fetchCodeloadTarball; drained at the
	// end of crawl() so temp dirs survive parser.PopulateForExternalGraph
	// but don't outlive the collector call.
	githubMat            *githubMaterializerState
	matNodes             []*knowledgev1.Node
	matEdges             []kgwire.BatchEdge
	materializedCleanups []func()
}

// crawlItem is one entry in the BFS queue.
type crawlItem struct {
	url   string
	depth int
}

// newCrawlState constructs a crawlState with pre-sized maps sized off the
// MaxPages budget so the common case avoids rehashing.
func newCrawlState(opts CrawlOptions, patterns []*regexp.Regexp) *crawlState {
	capacity := opts.MaxPages
	if capacity <= 0 {
		capacity = 16
	}
	s := &crawlState{
		visited:         make(map[string]struct{}, capacity),
		urlToID:         make(map[string]string, capacity*2),
		hashToURL:       make(map[string]string, capacity),
		hostPageCount:   make(map[string]int),
		patterns:        patterns,
		maxDepth:        opts.MaxDepth,
		maxPages:        opts.MaxPages,
		maxPathSegments: opts.MaxPathSegments,
		maxPagesPerHost: opts.MaxPagesPerHost,
		opts:            opts,
		githubMat:       newGithubMaterializerState(),
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// budgetExhaustedLocked reports whether the MaxPages budget has been
// reached. MaxPages <= 0 means "unbounded." Caller must hold s.mu.
func (s *crawlState) budgetExhaustedLocked() bool {
	return s.maxPages > 0 && len(s.pages) >= s.maxPages
}

// budgetWasExhausted reports whether the crawl stopped because MaxPages
// was reached (as opposed to a naturally drained queue).
func (s *crawlState) budgetWasExhausted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.budgetHit
}

// nextWork returns the next item from the FIFO queue, blocking on cond
// when the queue is empty but in-flight workers might still enqueue
// discoveries. Returns ok=false when the queue has drained AND no
// workers are in flight, or when shutdown has been called — in which
// case the caller should exit.
//
// The inFlight counter is incremented here (not when the item is
// actually processed) so a concurrent worker that's about to finish and
// check "any more work?" sees us as an active producer.
func (s *crawlState) nextWork() (crawlItem, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if s.closed {
			s.cond.Broadcast()
			return crawlItem{}, false
		}
		if s.budgetExhaustedLocked() {
			s.budgetHit = true
			s.closed = true
			s.cond.Broadcast()
			return crawlItem{}, false
		}
		if len(s.queue) > 0 {
			head := s.queue[0]
			s.queue = s.queue[1:]
			s.inFlight++
			return head, true
		}
		if s.inFlight == 0 {
			// Queue drained and nobody else will enqueue. Done.
			s.closed = true
			s.cond.Broadcast()
			return crawlItem{}, false
		}
		s.cond.Wait()
	}
}

// workerDone decrements the in-flight counter and wakes any worker
// parked in nextWork() so it can re-check the queue/in-flight state.
func (s *crawlState) workerDone() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inFlight--
	s.cond.Broadcast()
}

// shutdown signals all parked workers to exit immediately. Used when
// ctx cancels so we don't wait for the queue to drain.
func (s *crawlState) shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.cond.Broadcast()
}

// enqueueSeed enqueues a seed URL unconditionally at depth 0. Seeds
// bypass FollowPatterns (they are the starting points of the crawl).
// Duplicate seeds are suppressed via the visited set.
func (s *crawlState) enqueueSeed(raw string) {
	norm := normalizeURL(raw)
	if norm == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, seen := s.visited[norm]; seen {
		return
	}
	s.visited[norm] = struct{}{}
	s.queue = append(s.queue, crawlItem{url: raw, depth: 0})
	s.cond.Signal()
}

// enqueueDiscovered enqueues a link discovered inside a fetched page.
// The link is dropped if it has been visited, if it would exceed the
// MaxDepth budget, or if it fails the FollowPatterns regex allowlist.
//
// MaxDepth is interpreted as the maximum hop count from any seed: with
// seeds at depth 0, MaxDepth=0 and MaxDepth=1 both fetch only the seed
// (no link-following), MaxDepth=2 fetches the seed plus its direct
// neighbors, MaxDepth=3 adds the neighbors-of-neighbors, and so on.
// MaxDepth=0 selects the seed-only single-URL path and avoids the
// off-by-one trap where MaxDepth=1 would accidentally expand a "surely
// just the seed" crawl into the full first hop.
func (s *crawlState) enqueueDiscovered(raw string, parentDepth int) {
	norm := normalizeURL(raw)
	if norm == "" {
		return
	}
	nextDepth := parentDepth + 1
	// MaxDepth <= 0 means unbounded. Caller opts into a cap explicitly.
	if s.maxDepth > 0 && nextDepth >= s.maxDepth {
		return
	}
	if !shouldFollow(raw, s.patterns) {
		return
	}
	if s.maxPathSegments > 0 && pathSegmentCount(raw) > s.maxPathSegments {
		slog.Debug("web.crawl: path-segment cap dropped URL",
			"url", raw, "segments", pathSegmentCount(raw), "cap", s.maxPathSegments)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, seen := s.visited[norm]; seen {
		return
	}
	s.visited[norm] = struct{}{}
	s.queue = append(s.queue, crawlItem{url: raw, depth: nextDepth})
	s.cond.Signal()
}

// shouldFollow returns true when the URL matches any of the compiled
// follow patterns. An empty patterns slice is treated as "accept any"
// (the allowlist is optional).
func shouldFollow(raw string, patterns []*regexp.Regexp) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, re := range patterns {
		if re.MatchString(raw) {
			return true
		}
	}
	return false
}

// normalizeURL returns a canonical form of raw suitable for use as a
// visited-set key. Delegates to canonicalizeURL (crawl_canonical.go),
// which is the single source of truth for URL key normalization.
func normalizeURL(raw string) string {
	return canonicalizeURL(raw)
}

// isContextErr reports whether err is a context cancellation or deadline
// error (including wrapped forms). Used so processURL can skip logging
// context cancellation as a per-URL failure — the outer loop observes
// ctx.Err() and exits cleanly.
func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
