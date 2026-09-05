// SPDX-License-Identifier: Apache-2.0

// Package web collects raw typed graph records from HTML pages.
//
// The collector fetches a page, walks THE WHOLE DOCUMENT, and emits
// page/section/paragraph/code_block/list/list_item/table/link/image/blockquote
// nodes with contains/references/cites edges into a per-source graph keyed by
// source_name slug.
//
// IT DOES NOT STRIP CHROME. parsePage walks the raw response bytes and has no
// skip list: nav, header, footer and aside are all walked and emitted, because
// this is a RAW graph and deciding what is furniture is a consumer's job.
// go-readability runs, but only to extract the title, byline and publication
// date — it does not decide what the walker sees. The graph is ENROLLED
// EMBED-ONLY on the server: the raw content is never summarized, but the chunks
// that carry their own text in Content are embedded and BM25-indexed, so a
// downstream stage-2 translator can hybrid-search the raw graph to find what to
// extract. Which node types those are is the server's graphNodeTypeAllowList.
//
// CrawlOptions flow through context (see WithCrawlOptions) so the collector
// package remains domain-free.
package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

func init() {
	collector.Register(&WebCollector{})
}

// WebCollector collects typed raw-graph records from web pages. See package
// doc for the emitted node shape and per-source graph layout.
type WebCollector struct{}

// Name returns the collector identifier used for registry lookup.
func (c *WebCollector) Name() string { return "web" }

// Collect executes the BFS crawl configured by the CrawlOptions carried in
// ctx (see WithCrawlOptions): seed URLs feed a breadth-first queue bounded
// by MaxDepth, MaxPages and FollowPatterns; every fetched page is walked
// and emitted as typed raw-graph nodes. After the crawl completes,
// internal-link REFERENCES edges (Phase 3 emits them with a placeholder
// "web:url:<absolute>" target) are rewired to the real page node ID when
// the target was itself crawled; unvisited targets are re-classified as
// external cites so downstream consumers never see a dangling placeholder.
//
// The returned CollectResult targets GraphWebRaw keyed on opts.Source so
// collector.Collect writes to ~/.knowledge/web/<source>.bin.
//
// THE SOURCE SLUG DECIDES THE NAME; THE SERVER DECIDES THE CONTENTS. A re-collect
// of the same source resolves to the same graph and REPLACES what was there: a
// web graph is in the collector-managed full-replace set, so the server's
// Finalize retires whatever this collect did not re-emit. That replacement is
// conditional on the walk assertion this collector makes via walkCompleteFrom — a
// crawl that failed to read a unit it set out to read reports an incomplete walk,
// gets no deletion phase, and leaves the prior generation resident.
func (c *WebCollector) Collect(
	ctx context.Context,
	_ string,
	_ collector.CollectOptions,
) (*collectorwire.CollectResult, error) {
	opts, ok := crawlOptionsFrom(ctx)
	if !ok {
		return nil, fmt.Errorf("web collector: CrawlOptions missing from context (use WithCrawlOptions)")
	}
	opts = opts.ApplyDefaults()
	if err := ValidateCrawlOptions(opts); err != nil {
		return nil, fmt.Errorf("web collector: %w", err)
	}

	// The crawl's SOURCE IDENTITY, computed once before the walk from the same
	// derivation the dispatch named the graph with, so every page root of this
	// run records one agreed value rather than each deriving its own.
	seedHost, err := SeedHost(opts.SeedURLs)
	if err != nil {
		return nil, fmt.Errorf("web collector: %w", err)
	}
	src := graphSource{Name: opts.Source, SeedHost: seedHost}

	fc := newFetchClient(opts.UserAgent, opts.PolitenessMs)
	pages, urlToID, matNodes, matEdges, degraded, err := crawl(ctx, fc, opts)
	if err != nil {
		return nil, fmt.Errorf("web collector: crawl: %w", err)
	}

	// One instant for the whole run, captured before the emit loop so every
	// page root of this crawl agrees on when the graph was collected.
	collectedAt := time.Now().UTC()

	var nodes []*knowledgev1.Node
	var edges []kgwire.BatchEdge
	for _, p := range pages {
		pn, pe, err := emitFromPage(p, collectedAt, src)
		if err != nil {
			return nil, fmt.Errorf("web collector: %w", err)
		}
		nodes = append(nodes, pn...)
		edges = append(edges, pe...)
	}
	edges, downgraded, err := resolveInternalLinks(edges, urlToID)
	if err != nil {
		return nil, fmt.Errorf("web collector: %w", err)
	}
	// The downgrade happens AFTER the crawl, in a frame that holds no crawl
	// state, so it is folded into the census here rather than threaded back.
	if downgraded > 0 {
		degraded[degradeLinkDowngraded] += downgraded
	}

	// Append materialized github nodes/edges. These sit alongside the
	// HTML page nodes in the same web/<source-slug> graph so downstream
	// recipes can walk both.
	nodes = append(nodes, matNodes...)
	edges = append(edges, matEdges...)

	// Computed AFTER the materialized nodes are appended, because the
	// exclusion reads the emitted gh-root nodes.
	followUps := collectGithubFollowUps(opts.SeedURLs, pages, nodes)
	return &collectorwire.CollectResult{
		GraphType:           kgtypes.GraphWebRaw,
		GraphName:           opts.Source,
		Nodes:               nodes,
		Edges:               edges,
		NonSubstantiveNodes: countRetainedChrome(nodes),
		Degraded:            degraded,
		WalkComplete:        walkCompleteFrom(degraded),

		GithubFollowUps:      len(followUps),
		GithubFollowUpSample: sampleFollowUps(followUps),
	}, nil
}

// githubFollowUpSampleCap bounds how many follow-up URLs the rendered response
// names. The COUNT beside them is exact and the render states how many were
// omitted, so the cap shortens the line without ever making the sample
// mistakable for the whole inventory.
const githubFollowUpSampleCap = 3

// githubRepoKey identifies a repository independently of how a URL spelled it.
// Owner and repo only: the ref lives in githubUnitSet, which needs to answer
// about the refs of ONE repository together.
type githubRepoKey struct{ owner, repo string }

// githubUnitSet records units of github follow-up work — (owner, repo, ref)
// triples — under the empty-ref-matches-any rule documented on
// collectGithubFollowUps. Reads on a nil map are legal in Go, so the zero value
// is a usable empty set and add() allocates on first use.
//
// anyRef holds the repositories recorded WITHOUT a ref (a bare spelling, which
// stands for every ref); refs holds the named refs of each repository.
type githubUnitSet struct {
	anyRef map[githubRepoKey]struct{}
	refs   map[githubRepoKey]map[string]struct{}
}

// add records (owner, repo, ref) as a unit held by this set. Owner and repo are
// lower-cased — every caller passes values parseGitHubURL already lower-cased,
// so this is belt-and-braces rather than a second normalizer — while ref is
// left in its original casing because git refs are case-sensitive.
func (s *githubUnitSet) add(owner, repo, ref string) {
	k := githubRepoKey{owner: strings.ToLower(owner), repo: strings.ToLower(repo)}
	if ref == "" {
		if s.anyRef == nil {
			s.anyRef = map[githubRepoKey]struct{}{}
		}
		s.anyRef[k] = struct{}{}
		return
	}
	if s.refs == nil {
		s.refs = map[githubRepoKey]map[string]struct{}{}
	}
	if s.refs[k] == nil {
		s.refs[k] = map[string]struct{}{}
	}
	s.refs[k][ref] = struct{}{}
}

// has is the DEDUPE-side predicate: it reports whether this set already holds a
// unit covering (owner, repo, ref), with an empty ref matching any ref on
// EITHER side. Two spellings of one repository that name no ref between them
// are one line in the inventory, and a bare spelling met beside .../tree/main
// is not a second candidate — which is what the render has always done, and
// what the follow-up test's across-spellings case pins.
//
// IT IS NOT THE EXCLUSION-SIDE PREDICATE. See hasMaterialized, and the
// paragraph on collectGithubFollowUps for why the two directions differ.
func (s *githubUnitSet) has(owner, repo, ref string) bool {
	k := githubRepoKey{owner: strings.ToLower(owner), repo: strings.ToLower(repo)}
	if _, ok := s.anyRef[k]; ok {
		// A bare spelling was recorded, and for DEDUPE purposes it stands for
		// every ref: naming the same repository twice helps nobody.
		return true
	}
	if ref == "" {
		// A bare candidate asks for the default branch, which any ref already
		// recorded for this repository satisfies.
		return len(s.refs[k]) > 0
	}
	_, ok := s.refs[k][ref]
	return ok
}

// hasMaterialized is the EXCLUSION-side predicate: a bare CANDIDATE is covered
// by any recorded ref, but a bare RECORDED root covers only a bare candidate,
// because materializing the default branch is not materializing every ref.
//
// THE ASYMMETRY IS THE POINT, and it is the one thing about this file worth
// reading twice. Dedupe asks "have I already told the caller about this?", and
// there a bare spelling standing for every ref merely avoids a duplicate line.
// Exclusion asks "does the caller already HAVE this?", and there the same
// wildcard is a false claim: a gh-root minted from a bare seed carries ref=""
// (parseGitHubURL leaves Ref empty for kindRoot; the "HEAD" substitution in the
// fetcher is local to the download), so treating it as every ref would drop a
// linked .../tree/v2-release the crawl never fetched — the exact granularity
// loss the paragraph on collectGithubFollowUps says the triple key exists to
// prevent. The follow-up test pins both spellings of it, seeding the
// materialized root once with a named ref and once bare.
func (s *githubUnitSet) hasMaterialized(owner, repo, ref string) bool {
	k := githubRepoKey{owner: strings.ToLower(owner), repo: strings.ToLower(repo)}
	if ref == "" {
		// A bare candidate asks for the default branch, and any ref this run
		// materialized satisfies it.
		if _, ok := s.anyRef[k]; ok {
			return true
		}
		return len(s.refs[k]) > 0
	}
	// A NAMED candidate is covered only by that same ref having materialized.
	_, ok := s.refs[k][ref]
	return ok
}

// collectGithubFollowUps returns the DISTINCT units of github follow-up work —
// a repository AT A REF — this harvest met and did not materialize, in a stable
// order, one URL per unit.
//
// THE EXCLUSION IS NOT OPTIONAL. A follow-up candidate is work the caller
// still has to do; a repository whose nodes are already in this graph is work
// that is DONE, and naming it tells the caller to go fetch something they
// already have. The exclusion set is read off the EMITTED gh-root nodes rather
// than tracked alongside them — buildGhRoot stamps the parsed owner, repo and
// ref on every gh-root it mints — so the inventory and the materialization
// cannot disagree about what landed.
//
// THE UNIT IS THE (OWNER, REPO, REF) TRIPLE, NOT THE URL STRING AND NOT THE
// REPOSITORY, because the triple is the unit the materializer itself works in:
// githubKey carries a Ref and buildGhRoot mints one gh-root PER REF. Keyed on
// the raw string, several links to one ref render as several candidates and
// inflate the count a caller acts on, and — the worse half — a ref this run
// MATERIALIZED under one spelling is still reported as outstanding when the
// page linked it under another. Keyed on the owner/repo pair alone, the
// opposite error: a link to a ref this run never fetched vanishes from the
// inventory because a DIFFERENT ref of that repository happened to materialize,
// and the caller is told there is nothing left to follow up on.
//
// AN EMPTY REF ON THE CANDIDATE SIDE MATCHES ANY REF. A bare github.com/a/b
// names no ref: it asks for whatever the default branch resolves to, which any
// materialized ref satisfies. So the bare spelling is neither a second
// candidate beside .../a/b/tree/main nor left outstanding once main has
// materialized. Two NAMED refs are the same unit only when they are equal,
// which is what keeps .../a/b/tree/v2-release outstanding after main
// materialized. The relation is deliberately not transitive; first-seen wins,
// exactly as the render below already resolves spellings.
//
// AN EMPTY REF ON THE MATERIALIZED SIDE DOES NOT, and the two sets therefore
// read through DIFFERENT predicates — seen through has, materialized through
// hasMaterialized. Materializing the default branch is not materializing every
// ref, so a gh-root minted from a bare seed must not swallow a linked
// .../a/b/tree/v2-release; wildcarding it there reinstates, for the bare
// spelling, exactly the pair-key failure the paragraph above describes.
//
// THE DEDUPE SIDE KEEPS THE WILDCARD, AND THE TRADE IS REAL RATHER THAN
// COSMETIC. A bare spelling is treated as the default ref and stands for every
// ref, so a bare github.com/a/b met beside a DISTINCT .../a/b/tree/v2-release
// counts as one unit of work in either link order — measured — and the release
// branch is not reported as its own line. It is not merely a duplicate line
// being suppressed. That is accepted because the collector does not resolve
// default branches and so cannot tell whether the bare spelling names the same
// ref as the named one; leg 1b, one_repository_counts_once_across_spellings in
// github_followup_test.go, pins the single count. Getting the MATERIALIZED side
// wrong once is why leg 5d exists beside leg 5c.
//
// THE FIRST-SEEN SPELLING IS WHAT RENDERS. The caller gets a URL that actually
// appeared in the source rather than a normalized synthetic they cannot find on
// the page.
func collectGithubFollowUps(seeds []string, pages []*pageRecord, emitted []*knowledgev1.Node) []string {
	var materialized githubUnitSet
	for _, n := range emitted {
		if n == nil || n.Type != string(kgtypes.NodeGithubRepo) {
			continue
		}
		// THE TRIPLE IS READ OFF THE NODE, NOT RE-DERIVED FROM ITS source_url.
		// buildGhRoot stamps owner, repo and ref from the same githubURLInfo
		// that produced the materialization, so this reading is authoritative
		// and — unlike a re-parse — has no rejection branch that could silently
		// drop a materialized repository out of the exclusion set. No emptiness
		// guard is needed either: parseGitHubURL returns ok only for non-empty
		// owner and repo, so a gh-root cannot carry an empty pair, and a unit
		// with one could match no candidate for the same reason.
		materialized.add(n.Metadata["owner"], n.Metadata["repo"], n.Metadata["ref"])
	}

	var seen githubUnitSet
	var out []string
	consider := func(raw string) {
		info, ok := parseGitHubURL(raw)
		if !ok {
			return
		}
		if materialized.hasMaterialized(info.Owner, info.Repo, info.Ref) {
			return
		}
		if seen.has(info.Owner, info.Repo, info.Ref) {
			return
		}
		seen.add(info.Owner, info.Repo, info.Ref)
		out = append(out, raw)
	}

	for _, seed := range seeds {
		consider(seed)
	}
	for _, p := range pages {
		if p == nil {
			continue
		}
		for _, link := range p.InternalLinks {
			consider(link)
		}
		for _, cite := range p.ExternalCites {
			if cite != nil {
				consider(cite.URL)
			}
		}
	}
	return out
}

// sampleFollowUps returns at most githubFollowUpSampleCap of urls, preserving
// order. Returns nil for an empty input so a clean harvest carries no slice.
func sampleFollowUps(urls []string) []string {
	if len(urls) == 0 {
		return nil
	}
	if len(urls) > githubFollowUpSampleCap {
		return append([]string(nil), urls[:githubFollowUpSampleCap]...)
	}
	return append([]string(nil), urls...)
}

// countRetainedChrome counts the emitted nodes that carry a content node type
// while being retained chrome. Three classes today:
//
//   - the navigation strips the walker keeps as links_only paragraph records;
//   - LAYOUT TABLES, which carry the `table` type while emitTable deliberately
//     writes them no Content, because a layout table is scaffolding whose cells
//     are walked into their own records. Counting one as substantive would let
//     a page of pure table scaffolding read as a good harvest — the same way an
//     uncorrected nav strip would, and for the same reason.
//   - NAV-LIST ITEMS, the list_item nodes of a list classifyList judged to be
//     navigation. A menu entry is a label, not prose, and a page whose only
//     text is its menu is the harvest this correction exists to catch.
//
// EVERY CLASS HERE IS A NODE THE SUBSTANTIVE SUM ALREADY COUNTED. The
// subtraction is a correction to that sum, so a class whose type is not in
// webContentTypes must not be added: it would subtract something never added
// and push the harvest below zero.
//
// THE THIRD CLASS IS THE ONLY ONE NEEDING A TYPE GUARD, and it needs one for
// exactly that reason. links_only appears only on paragraphs and table_layout
// only on tables, so each of those keys identifies its own node type on its
// own. list_nav does NOT: it is stamped on the enclosing `list` node too —
// that is where the verdict was reached and where its measurements live — and
// `list` is not in webContentTypes. A metadata-only test would therefore
// subtract the list node the substantive sum never added, which is precisely
// what the invariant above forbids.
//
// It counts over THE NODES JUST EMITTED rather than tracking a running total
// through the walk, so it cannot drift out of step with what actually reached
// the result: the thing being corrected is a census of that same slice.
func countRetainedChrome(nodes []*knowledgev1.Node) int {
	n := 0
	for _, node := range nodes {
		if node == nil {
			continue
		}
		if node.Metadata["links_only"] == "true" || node.Metadata["table_layout"] == "true" {
			n++
		}
		if node.Type == webListItemType && node.Metadata["list_nav"] == "true" {
			n++
		}
	}
	return n
}

// resolveInternalLinks rewrites page-level internal-link REFERENCES edges
// emitted by emit_nodes.go's emitLinks. Those edges carry a placeholder
// ToID of the form "web:url:<absolute>" and Evidence metadata including
// rel="internal" / url=<absolute>. After a crawl we know which of those
// absolute URLs actually produced a page node, so we can:
//
//   - rewrite ToID to the real page node ID when the URL is in urlToID
//     (the link resolves to a crawled page — edge becomes an in-graph link);
//   - downgrade rel from "internal" to "external" otherwise (the URL was
//     same-host but the crawl did not visit it — budget / regex filter /
//     fetch failure — so the edge is a cite, not an in-graph reference).
//
// It returns the DOWNGRADE COUNT alongside the edges. A downgrade is a
// same-host page the harvest did not reach, which is exactly the kind of loss
// the degrade census exists to surface — and it is invisible in the edge slice
// afterwards, because a downgraded edge is indistinguishable from one that was
// external all along.
//
// External edges (rel="external") are left untouched; they are already
// cites against an unresolved URL.
func resolveInternalLinks(edges []kgwire.BatchEdge, urlToID map[string]string) ([]kgwire.BatchEdge, int, error) {
	downgraded := 0
	for i := range edges {
		e := &edges[i]
		if e.Type != kgtypes.EdgeReferences {
			continue
		}
		md := parseEdgeMeta(e.Evidence)
		if md["rel"] != "internal" {
			continue
		}
		rawURL := md["url"]
		if id, ok := urlToID[rawURL]; ok && id != "" {
			e.ToID = id
			continue
		}
		slog.Warn("web.crawl: internal link downgraded to external, never visited",
			"url", rawURL)
		md["rel"] = "external"
		evidence, err := jsonMeta(md)
		if err != nil {
			return nil, 0, fmt.Errorf("re-encode downgraded link evidence for %s: %w", rawURL, err)
		}
		e.Evidence = evidence
		downgraded++
	}
	return edges, downgraded, nil
}

// parseEdgeMeta unmarshals an edge.Evidence JSON blob into a string map.
// Empty / invalid blobs return an empty map so callers can safely index.
func parseEdgeMeta(raw string) map[string]string {
	m := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return m
	}
	_ = json.Unmarshal([]byte(raw), &m)
	return m
}
