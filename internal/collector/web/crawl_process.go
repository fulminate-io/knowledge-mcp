// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"log/slog"
)

// processURL drives the fetch → clean → parse pipeline for a single URL
// and, on success, appends the page to state.pages, records URL→ID in
// state.urlToID, and enqueues every InternalLink for further BFS.
//
// Two post-parse safety checks run BEFORE the page is appended:
//
//  1. ContentHash dedup — if another URL has already emitted a page with
//     the same sha256 body hash, this URL is a content-alias. Skip
//     emitting the duplicate page node and skip enqueueing its internal
//     links (they would rediscover the same DAG under a different path).
//  2. Per-host cap — if MaxPagesPerHost is set and the host has already
//     reached its quota, skip emitting and skip children.
//
// The URL is still in the visited set from enqueueDiscovered, so future
// rediscovery of the same URL remains suppressed.
func (s *crawlState) processURL(ctx context.Context, fc *fetchClient, raw string, depth int) {
	// GitHub URLs short-circuit the HTML pipeline and feed through the
	// materializer instead. parser.PopulateForExternalGraph (tree URLs)
	// or treesitter chunking (blob URLs) produce nodes/edges directly
	// into s.matNodes/s.matEdges; the URL is registered in s.urlToID so
	// internal-link resolution targets the gh-root. Materialized chunks
	// are leaf content — the materializer file is forbidden from
	// enqueueing new BFS work (verified by Phase 6 grep).
	if info, ok := parseGitHubURL(raw); ok {
		// MATERIALIZATION IS CALLER-REQUESTED. Without the opt-in a github URL
		// is left alone entirely: it is reported to the caller as a follow-up
		// candidate and nothing is fetched. With it, the existing lane runs
		// unchanged — same whole-repo unit, same dedupe registry, same
		// download cap.
		if s.opts.MaterializeGithub {
			s.materializeGithub(ctx, fc, raw, info, depth)
		}
		// NEITHER PATH RECORDS A PAGE, so the budget slot this item was handed
		// at dequeue goes back either way, or the page cap under-fills by one
		// for every github link on the site.
		s.releaseReservation()
		return
	}

	// Fetch + clean + parse happens WITHOUT the state lock. Per-host
	// politeness lives inside fetchClient.waitForPoliteness; that mutex
	// is per-host, so cross-host fetches run in true parallel.
	record, ok := s.fetchAndParse(ctx, fc, raw)
	if !ok {
		// Fetch, clean or parse failed: no page will come of this slot.
		s.releaseReservation()
		return
	}
	// applyPage runs alias-check + host-cap + recordPage atomically under
	// s.mu so concurrent workers don't race on the dedup maps.
	if !s.applyPage(record) {
		// Dropped as a content-hash alias or by the per-host cap — fetched,
		// but not a page, so the slot goes back.
		s.releaseReservation()
		return
	}
	for _, link := range record.InternalLinks {
		s.enqueueDiscovered(link, depth)
	}
}

// applyPage atomically runs the post-fetch dedup+cap+record critical
// section under s.mu. Returns true when the page was appended; false
// when alias-dedup or host-cap dropped it.
func (s *crawlState) applyPage(record *pageRecord) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isContentAlias(record) {
		return false
	}
	if s.hostCapReached(record) {
		return false
	}
	s.recordPage(record)
	return true
}

// fetchAndParse runs fetch → clean → parse, logging per-URL failures at
// debug level. Returns (record, true) on success, (nil, false) on any
// error (including context cancellation, which the outer loop observes
// separately).
func (s *crawlState) fetchAndParse(
	ctx context.Context, fc *fetchClient, raw string,
) (*pageRecord, bool) {
	page, err := fc.fetch(ctx, raw)
	if err != nil {
		if isContextErr(err) {
			// A cancelled context is the CALLER stopping the crawl, not the
			// crawl losing work to the site — it is counted nowhere and the
			// outer loop observes it separately.
			return nil, false
		}
		slog.Warn("web.crawl: fetch failed, page dropped", "url", raw, "err", err)
		s.bumpDegrade(degradeFetchFailed, 1)
		return nil, false
	}
	// POSITION IS PART OF THE CONTRACT. Before cleanArticle, because that lane
	// SUCCEEDS on binary input and would have already run; after the fetch error
	// branch, because a cancelled context and a transport failure are already
	// classified and stay in their own lanes. Both observed media types go in the
	// log line: the census class is a fixed-vocabulary counter and cannot carry a
	// sub-reason, so this is the only place an operator can tell a declined epub
	// from a lying origin.
	if v := classifyPage(page); !v.isPage {
		slog.Warn("web.crawl: not a page, resource skipped",
			"url", raw, "reason", v.reason,
			"declared_content_type", v.declared, "sniffed_content_type", v.sniffed)
		s.bumpDegrade(degradeNotAPage, 1)
		return nil, false
	}
	cleaned, err := cleanArticle(page.Body, page.FinalURL)
	if err != nil {
		slog.Warn("web.crawl: clean failed, page dropped", "url", raw, "err", err)
		s.bumpDegrade(degradeCleanFailed, 1)
		return nil, false
	}
	record, err := parsePage(page, cleaned)
	if err != nil {
		slog.Warn("web.crawl: parse failed, page dropped", "url", raw, "err", err)
		s.bumpDegrade(degradeParseFailed, 1)
		return nil, false
	}
	// The two parse-side lanes have no access to the crawl state — they run
	// inside parsePage — so they report their losses on the record and are
	// folded in here, at the first frame that can reach the census.
	s.bumpDegrade(degradeHiddenPruned, record.HiddenPruned)
	if record.RawLinkFailed {
		s.bumpDegrade(degradeRawLinkParseFailed, 1)
	}
	return record, true
}

// isContentAlias reports whether record's ContentHash has already been
// produced by a different URL in this crawl. When true, the page is a
// content-duplicate and should be dropped to keep the emitted graph
// clean. First sight of a hash records the URL → hash mapping.
//
// Caller must hold s.mu — this method runs inside the "apply-page"
// critical section that also updates pages/urlToID/hostPageCount.
func (s *crawlState) isContentAlias(record *pageRecord) bool {
	if record.ContentHash == "" {
		return false
	}
	if existing, ok := s.hashToURL[record.ContentHash]; ok && existing != record.URL {
		slog.Warn("web.crawl: content-hash alias, page dropped",
			"url", record.URL, "alias_of", existing, "hash", record.ContentHash)
		s.bumpDegradeLocked(degradeContentAlias, 1)
		return true
	}
	s.hashToURL[record.ContentHash] = record.URL
	return false
}

// hostCapReached returns true when MaxPagesPerHost is set and the host
// of record.URL has already reached its quota. The counter is
// incremented as a side-effect on the not-yet-capped path so callers see
// a single authoritative count.
//
// Caller must hold s.mu.
func (s *crawlState) hostCapReached(record *pageRecord) bool {
	if s.maxPagesPerHost <= 0 {
		return false
	}
	host := recordHost(record)
	if host == "" {
		return false
	}
	if s.hostPageCount[host] >= s.maxPagesPerHost {
		slog.Warn("web.crawl: per-host cap reached, page dropped",
			"host", host, "cap", s.maxPagesPerHost, "url", record.URL)
		s.bumpDegradeLocked(degradeHostCap, 1)
		return true
	}
	s.hostPageCount[host]++
	return false
}

// recordPage appends record to state.pages and registers URL→ID in
// state.urlToID (both for record.URL and, when distinct, record.FinalURL).
//
// Caller must hold s.mu.
func (s *crawlState) recordPage(record *pageRecord) {
	s.pages = append(s.pages, record)
	pageID := stableID(record.URL, "page", "", 0)
	s.urlToID[record.URL] = pageID
	if record.FinalURL != "" && record.FinalURL != record.URL {
		s.urlToID[record.FinalURL] = pageID
	}
}
