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
		s.materializeGithub(ctx, fc, raw, info, depth)
		return
	}

	// Fetch + clean + parse happens WITHOUT the state lock. Per-host
	// politeness lives inside fetchClient.waitForPoliteness; that mutex
	// is per-host, so cross-host fetches run in true parallel.
	record, ok := s.fetchAndParse(ctx, fc, raw)
	if !ok {
		return
	}
	// applyPage runs alias-check + host-cap + recordPage atomically under
	// s.mu so concurrent workers don't race on the dedup maps.
	if !s.applyPage(record) {
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
			return nil, false
		}
		slog.Debug("web.crawl: fetch failed", "url", raw, "err", err)
		return nil, false
	}
	cleaned, err := cleanArticle(page.Body, page.FinalURL)
	if err != nil {
		slog.Debug("web.crawl: clean failed", "url", raw, "err", err)
		return nil, false
	}
	record, err := parsePage(page, cleaned)
	if err != nil {
		slog.Debug("web.crawl: parse failed", "url", raw, "err", err)
		return nil, false
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
		slog.Debug("web.crawl: content-hash alias",
			"url", record.URL, "alias_of", existing, "hash", record.ContentHash)
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
		slog.Debug("web.crawl: per-host cap reached",
			"host", host, "cap", s.maxPagesPerHost, "url", record.URL)
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
