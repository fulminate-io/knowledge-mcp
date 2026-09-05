// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"runtime/debug"
	"slices"
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
) ([]*pageRecord, map[string]string, []*knowledgev1.Node, []kgwire.BatchEdge, map[string]int, error) {
	patterns, err := ParseFollowPatterns(opts.FollowPatterns)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("crawl: compile follow patterns: %w", err)
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
		return state.pages, state.urlToID, state.finishNodes(), state.matEdges, state.degradeCensus(), err
	}
	if state.budgetWasExhausted() {
		slog.Warn("web.crawl: MaxPages budget exhausted — crawl truncated",
			"max_pages", state.maxPages, "fetched", len(state.pages),
			"budget_declined", state.declinedByBudget())
	}
	return state.pages, state.urlToID, state.finishNodes(), state.matEdges, state.degradeCensus(), nil
}

// finishNodes returns the materialized github nodes in a FRESH slice, so
// crawl()'s two return paths cannot alias one backing array.
func (s *crawlState) finishNodes() []*knowledgev1.Node {
	return slices.Clone(s.matNodes)
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
	mu            sync.Mutex
	cond          *sync.Cond
	queue         []crawlItem
	visited       map[string]struct{}
	pages         []*pageRecord
	urlToID       map[string]string
	patterns      []*regexp.Regexp
	hashToURL     map[string]string
	hostPageCount map[string]int
	inFlight      int
	closed        bool
	budgetHit     bool
	maxDepth      int
	maxPages      int
	// reserved counts budget slots handed out at DEQUEUE. It is what the
	// budget is tested against; see budgetExhaustedLocked.
	reserved int
	// budgetDeclined is how many queued URLs the budget refused to hand out,
	// captured once when the crawl closes on an exhausted budget. Reported on
	// the truncation warning and, in the collect response, as the
	// budget_declined degrade class.
	budgetDeclined int
	// degraded counts dropped work per class. Its key set is the fixed
	// vocabulary above, so it is bounded by that list rather than by the
	// corpus, and a class with no occurrences is ABSENT rather than zero —
	// which is what lets a clean harvest render no degrade section at all.
	degraded        map[string]int
	maxPathSegments int
	maxPagesPerHost int
	opts            CrawlOptions

	// github materialization state — populated by materializeGithub when
	// a github URL is dispatched. matNodes / matEdges are appended to the
	// CollectResult after the BFS drains; the node half goes out through
	// finishNodes. materializedCleanups holds the os.RemoveAll funcs returned by
	// fetchCodeloadTarball; drained at the end of crawl() so temp dirs
	// survive parser.PopulateForExternalGraph but don't outlive the
	// collector call.
	githubMat            *githubMaterializerState
	matNodes             []*knowledgev1.Node
	matEdges             []kgwire.BatchEdge
	materializedCleanups []func()
}

// THE DEGRADE CLASS VOCABULARY. Every lane in this collector that drops a unit
// of work counts itself under exactly one of these names, and this block is the
// single authoritative declaration of the set.
//
// THEY ARE COUNTER CLASSES, NEVER URLS. A census keyed by URL grows with the
// corpus and turns a collect response into a log; a fixed class vocabulary
// makes "what did this harvest lose, and how much" answerable in one line
// whatever the crawl's size.
//
// TWO ARE WIRED AND UNREACHABLE OVER HTTP, stated here rather than left for a
// reader to wonder about. degradeParseFailed: parsePage refuses only a nil
// page, a nil cleaned article, an empty body (which clean_failed catches
// first) or an unparseable FinalURL the fetch layer cannot produce.
// degradeRawLinkParseFailed: html.Parse is a recovering parser that errors
// only on a read failure of an in-memory byte reader. Both log at Warn; neither
// has a fixture that could reach it without faking the parser rather than the
// site.
//
// degradeNotAPage IS NOT A LANE ERRORING, which separates it from its neighbors:
// the fetch SUCCEEDED and returned a body, so it is not fetch_failed, while
// clean_failed and parse_failed name a lane REFUSING its input and both of those
// lanes SUCCEED on binary — precisely how an epub was harvested into paragraphs of
// ZIP. Without this class a harvested asset is invisible to the caller. A 200
// carrying an EMPTY body keeps clean_failed, the class it already has: this one
// means bytes arrived and were not a page, and the gate deliberately admits an
// empty body so it still reaches cleanArticle rather than moving that traffic here.
const (
	degradeFetchFailed        = "fetch_failed"
	degradeCleanFailed        = "clean_failed"
	degradeParseFailed        = "parse_failed"
	degradeContentAlias       = "content_alias"
	degradeHostCap            = "host_cap"
	degradePathSegmentCap     = "path_segment_cap"
	degradeBudgetDeclined     = "budget_declined"
	degradeLinkDowngraded     = "link_downgraded_external"
	degradeRawLinkParseFailed = "raw_link_parse_failed"
	degradeHiddenPruned       = "hidden_pruned"
	degradeNotAPage           = "not_a_page"

	degradeGithubUnsafePath    = "github_unsafe_path_rejected"
	degradeGithubUnpackFailed  = "github_unpack_failed"
	degradeGithubNonregular    = "github_nonregular_entry"
	degradeGithubTarReadFailed = "github_tar_read_failed"
)

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
		degraded:        map[string]int{},
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
// RESERVED OUT. MaxPages <= 0 means "unbounded." Caller must hold s.mu.
//
// IT COUNTS RESERVATIONS, NOT LANDED PAGES, and that is the whole fix. Pages
// are appended in recordPage, long after dequeue — after fetch, clean and
// parse — so a threshold on len(s.pages) is crossed while up to W-1 items are
// already in flight, and every one of them still lands. The ceiling was
// therefore maxPages + W - 1 rather than maxPages: measured at 11 pages
// against a cap of 4 with 8 workers.
//
// A reservation is taken where an item is handed out and returned by every
// path that consumes one without producing a page, so the count tracks work
// that can still become a page rather than work that already has.
func (s *crawlState) budgetExhaustedLocked() bool {
	return s.maxPages > 0 && s.reserved >= s.maxPages
}

// bumpDegrade adds n to a degrade class. A non-positive n is a no-op, so a
// caller folding a count it computed elsewhere does not have to guard.
func (s *crawlState) bumpDegrade(class string, n int) {
	if n <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.degraded[class] += n
}

// bumpDegradeLocked is bumpDegrade for callers already holding s.mu.
func (s *crawlState) bumpDegradeLocked(class string, n int) {
	if n <= 0 {
		return
	}
	s.degraded[class] += n
}

// degradeCensus returns a COPY of the per-class counts, so the caller cannot
// alias the state's map after the crawl returns. A crawl that dropped nothing
// returns an empty map, and an empty map renders no degrade section.
func (s *crawlState) degradeCensus() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.degraded))
	maps.Copy(out, s.degraded)
	return out
}

// releaseReservation returns a budget slot taken at dequeue by a path that
// produced no page, and wakes any worker parked on the exhausted branch.
//
// WITHOUT THE RELEASES THE CAP UNDER-FILLS ON ANY FAILING CRAWL: a fetch that
// errors, a page dropped as a content-hash alias or by the host cap, and a
// github URL that records no pageRecord at all each consume a reservation and
// never return it, so a site with twenty dead links reaches one page under a
// cap of four instead of four.
func (s *crawlState) releaseReservation() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reserved--
	s.cond.Broadcast()
}

// budgetWasExhausted reports whether the crawl stopped because MaxPages
// was reached AND queued work was actually declined as a result.
//
// THE SECOND CONJUNCT IS WHAT MAKES THE WARNING TRUTHFUL. Keyed on the
// threshold alone it fired on a single-page site whose queue had drained
// naturally — "max_pages=1 fetched=1" — announcing a truncation that did not
// happen. A crawl that declined nothing now says nothing.
func (s *crawlState) budgetWasExhausted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.budgetHit
}

// declinedByBudget returns how many queued URLs the budget refused to hand
// out. It is the same number Phase 4's response census reports under the
// budget_declined class.
func (s *crawlState) declinedByBudget() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.budgetDeclined
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
			// DO NOT CLOSE WHILE WORK IS IN FLIGHT. An in-flight worker whose
			// fetch fails, whose page is an alias, or whose URL was a github
			// link still has a reservation to return, and closing now would
			// strand that slot and under-fill the cap. Park on the existing
			// cond — workerDone and releaseReservation both broadcast — and
			// re-test, so a returned slot is picked up rather than lost.
			if s.inFlight > 0 {
				s.cond.Wait()
				continue
			}
			s.budgetDeclined = len(s.queue)
			s.budgetHit = s.budgetDeclined > 0
			s.bumpDegradeLocked(degradeBudgetDeclined, s.budgetDeclined)
			s.closed = true
			s.cond.Broadcast()
			return crawlItem{}, false
		}
		if len(s.queue) > 0 {
			head := s.queue[0]
			s.queue = s.queue[1:]
			s.reserved++
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
		slog.Warn("web.crawl: path-segment cap dropped URL",
			"url", raw, "segments", pathSegmentCount(raw), "cap", s.maxPathSegments)
		s.bumpDegrade(degradePathSegmentCap, 1)
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
