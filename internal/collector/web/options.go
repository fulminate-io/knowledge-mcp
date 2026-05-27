// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"fmt"
	"regexp"
)

// CrawlOptions configures a single web-collector invocation. It is threaded
// through context (see WithCrawlOptions) rather than through
// collector.CollectOptions so the collector package remains domain-free — the
// collector registry does not need to know about web-specific fields.
//
// Source is the per-graph slug used as GraphName (e.g. "hohpe-eip",
// "go101-go-details-and-tips"). SeedURLs is the list of starting URLs the
// crawl walks from; FollowPatterns is a list of raw regex strings that the
// crawl consults when deciding whether to follow an internal link
// (ParseFollowPatterns compiles them). MaxDepth / MaxPages / PolitenessMs
// bound the crawl scope and per-host request cadence. UserAgent overrides
// the default fetcher UA.
type CrawlOptions struct {
	Source         string
	SeedURLs       []string
	FollowPatterns []string // raw regex strings; compile via ParseFollowPatterns
	MaxDepth       int
	MaxPages       int
	PolitenessMs   int
	UserAgent      string

	// MaxConcurrency caps the number of worker goroutines that pop from
	// the BFS queue in parallel. Zero selects the package default. Each
	// worker fetches/parses/enqueues independently; per-host politeness is
	// enforced by fetchClient.waitForPoliteness (which serializes fetches
	// to the same host), so cross-host parallelism is safe to stack.
	MaxConcurrency int

	// MaxPathSegments caps the number of non-empty path segments a
	// followed URL may have. Catches recursive-path traps like
	// /a/b/a/b/a/b/... that would otherwise inflate the queue without
	// bound. Zero means no cap (off by default). Negative is rejected by
	// ValidateCrawlOptions.
	MaxPathSegments int

	// MaxPagesPerHost bounds the number of pages fetched from any single
	// host within a crawl, independent of the global MaxPages budget. Zero
	// means no per-host cap. When both are set, the crawl stops for a
	// host once either cap fires first. Useful when crawling across
	// multiple hosts to prevent one host from starving the others.
	MaxPagesPerHost int

	// MaxDownloadBytes caps the bytes downloaded per (owner, repo, ref)
	// when materializing github.com URLs. The cap applies to UNCOMPRESSED
	// bytes (the actual disk footprint we'll write) — Content-Length is
	// pre-checked, and a cumulative-byte counter aborts unpack mid-stream
	// when the declared length is missing or false.
	//
	// 0  = use default (50 MiB).
	// -1 = unlimited (bypass).
	// >0 = explicit cap in bytes.
	MaxDownloadBytes int64
}

// Defaults applied when a CrawlOptions field is its zero value.
//
// MaxDepth and MaxPages intentionally DO NOT have non-zero defaults:
// zero means "no cap" in the crawl loop (see budgetExhausted and the
// enqueueDiscovered depth gate), and ApplyDefaults does not fill them
// in. The rationale is that silent truncation at an arbitrary page/depth
// cap hides catalog-size bugs — "cap at 200 pages" turned a 500-page
// Go101 crawl into a 200-page one with no warning. Callers who need a
// hard cap must pass one explicitly. The BFS always terminates when the
// queue drains, so unbounded-by-default is safe for the well-behaved
// sites we target.
//
// PolitenessMs=50 is a low-but-polite default. Per-host politeness is
// enforced by fetchClient.waitForPoliteness — concurrent fetches to the
// same host serialize through a per-host mutex so MaxConcurrency never
// breaks the 50ms floor. Multi-host crawls get full MaxConcurrency
// throughput.
const (
	defaultPolitenessMs     = 50
	defaultMaxConcurrency   = 8
	defaultMaxDownloadBytes = 50 << 20 // 50 MiB per (owner, repo, ref) materialization
)

// ApplyDefaults returns a copy of opts with zero-valued MaxDepth, MaxPages,
// PolitenessMs, and UserAgent fields replaced by the package defaults.
// Source / SeedURLs / FollowPatterns are left untouched — they carry
// crawl identity and must come from the caller.
func (opts CrawlOptions) ApplyDefaults() CrawlOptions {
	out := opts
	// MaxDepth and MaxPages are intentionally NOT filled in — zero means
	// "unbounded" in the crawl loop (see budgetExhausted and the depth
	// gate in enqueueDiscovered). Silent truncation at an arbitrary cap
	// hid catalog-size bugs in the old defaults; callers must opt into a
	// hard cap explicitly.
	if out.PolitenessMs == 0 {
		out.PolitenessMs = defaultPolitenessMs
	}
	if out.MaxConcurrency == 0 {
		out.MaxConcurrency = defaultMaxConcurrency
	}
	if out.UserAgent == "" {
		out.UserAgent = defaultUserAgent
	}
	if out.MaxDownloadBytes == 0 {
		out.MaxDownloadBytes = defaultMaxDownloadBytes
	}
	return out
}

// ValidateCrawlOptions checks that opts is internally consistent. The
// validator is intentionally strict on the identity fields (Source,
// SeedURLs) so the MCP tool handler can surface the error early; numeric
// fields reject negatives but allow zeros so ApplyDefaults can pick them up.
//
// Returns nil on a valid opts. Returns a single descriptive error on the
// first violation; the caller is expected to show the message to the user.
func ValidateCrawlOptions(opts CrawlOptions) error {
	if opts.Source == "" {
		return fmt.Errorf("crawl options: Source is required")
	}
	if len(opts.SeedURLs) == 0 {
		return fmt.Errorf("crawl options: SeedURLs is required (at least one URL)")
	}
	for i, u := range opts.SeedURLs {
		if u == "" {
			return fmt.Errorf("crawl options: SeedURLs[%d] is empty", i)
		}
	}
	if opts.MaxDepth < 0 {
		return fmt.Errorf("crawl options: MaxDepth must be >= 0 (got %d)", opts.MaxDepth)
	}
	if opts.MaxPages < 0 {
		return fmt.Errorf("crawl options: MaxPages must be >= 0 (got %d)", opts.MaxPages)
	}
	if opts.PolitenessMs < 0 {
		return fmt.Errorf("crawl options: PolitenessMs must be >= 0 (got %d)", opts.PolitenessMs)
	}
	if opts.MaxPathSegments < 0 {
		return fmt.Errorf("crawl options: MaxPathSegments must be >= 0 (got %d)", opts.MaxPathSegments)
	}
	if opts.MaxPagesPerHost < 0 {
		return fmt.Errorf("crawl options: MaxPagesPerHost must be >= 0 (got %d)", opts.MaxPagesPerHost)
	}
	if opts.MaxConcurrency < 0 {
		return fmt.Errorf("crawl options: MaxConcurrency must be >= 0 (got %d)", opts.MaxConcurrency)
	}
	if opts.MaxDownloadBytes < -1 {
		return fmt.Errorf("crawl options: MaxDownloadBytes must be >= -1 (got %d)", opts.MaxDownloadBytes)
	}
	return nil
}

// ParseFollowPatterns compiles every raw regex in patterns. Returns the
// compiled list in the same order so callers can associate each compiled
// pattern back to its source string for logging. Returns an error on the
// first pattern that fails to compile, identifying the offending index and
// pattern text.
//
// Empty patterns slice returns (nil, nil) so callers can treat "no
// allowlist" as "accept any internal link" without branching on len.
func ParseFollowPatterns(patterns []string) ([]*regexp.Regexp, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	out := make([]*regexp.Regexp, 0, len(patterns))
	for i, raw := range patterns {
		re, err := regexp.Compile(raw)
		if err != nil {
			return nil, fmt.Errorf("follow pattern %d (%q): %w", i, raw, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// ctx-plumbing. Matches the cloud.WithCascadeSet / cascadeSetFrom precedent:
// unexported key struct, public WithCrawlOptions, unexported
// crawlOptionsFrom.

type crawlOptionsKey struct{}

// WithCrawlOptions returns a copy of ctx carrying opts so the web collector
// (Collect) and its helpers can read the crawl configuration without
// threading an extra parameter through every callsite. Callers that want to
// clear the value can pass a zero CrawlOptions; ValidateCrawlOptions will
// reject it at the entry point.
func WithCrawlOptions(ctx context.Context, opts CrawlOptions) context.Context {
	return context.WithValue(ctx, crawlOptionsKey{}, opts)
}

// crawlOptionsFrom returns the CrawlOptions stored in ctx by WithCrawlOptions
// plus an `ok` flag. When ok is false the returned CrawlOptions is the zero
// value, which ValidateCrawlOptions treats as invalid.
func crawlOptionsFrom(ctx context.Context) (CrawlOptions, bool) {
	opts, ok := ctx.Value(crawlOptionsKey{}).(CrawlOptions)
	return opts, ok
}
