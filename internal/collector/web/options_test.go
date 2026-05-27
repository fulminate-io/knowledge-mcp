// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"strings"
	"testing"
)

// TestValidateCrawlOptions covers the validator happy path, each required-
// field rejection, and the numeric lower-bound rejections. Zero numeric
// fields are valid (ApplyDefaults fills them) — only negatives should fail.
func TestValidateCrawlOptions(t *testing.T) {
	valid := CrawlOptions{
		Source:       "hohpe-eip",
		SeedURLs:     []string{"https://www.enterpriseintegrationpatterns.com/"},
		MaxDepth:     2,
		MaxPages:     50,
		PolitenessMs: 500,
	}
	if err := ValidateCrawlOptions(valid); err != nil {
		t.Fatalf("valid opts rejected: %v", err)
	}

	zeroNumerics := valid
	zeroNumerics.MaxDepth = 0
	zeroNumerics.MaxPages = 0
	zeroNumerics.PolitenessMs = 0
	zeroNumerics.MaxPathSegments = 0
	zeroNumerics.MaxPagesPerHost = 0
	if err := ValidateCrawlOptions(zeroNumerics); err != nil {
		t.Errorf("zero numerics must be valid (ApplyDefaults fills them): %v", err)
	}

	cases := []struct {
		name    string
		mutate  func(o *CrawlOptions)
		wantSub string
	}{
		{"empty source", func(o *CrawlOptions) { o.Source = "" }, "Source is required"},
		{"nil seedURLs", func(o *CrawlOptions) { o.SeedURLs = nil }, "SeedURLs is required"},
		{"empty seedURLs", func(o *CrawlOptions) { o.SeedURLs = []string{} }, "SeedURLs is required"},
		{"empty seed string", func(o *CrawlOptions) { o.SeedURLs = []string{"https://a.example", ""} }, "SeedURLs[1] is empty"},
		{"negative maxDepth", func(o *CrawlOptions) { o.MaxDepth = -1 }, "MaxDepth must be >= 0"},
		{"negative maxPages", func(o *CrawlOptions) { o.MaxPages = -5 }, "MaxPages must be >= 0"},
		{"negative politeness", func(o *CrawlOptions) { o.PolitenessMs = -10 }, "PolitenessMs must be >= 0"},
		{"negative maxPathSegments", func(o *CrawlOptions) { o.MaxPathSegments = -3 }, "MaxPathSegments must be >= 0"},
		{"negative maxPagesPerHost", func(o *CrawlOptions) { o.MaxPagesPerHost = -2 }, "MaxPagesPerHost must be >= 0"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := valid
			tc.mutate(&bad)
			err := ValidateCrawlOptions(bad)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// TestParseFollowPatterns covers (a) empty input returns nil, (b) valid
// patterns compile in order, (c) an invalid pattern surfaces an error that
// identifies the bad entry.
func TestParseFollowPatterns(t *testing.T) {
	got, err := ParseFollowPatterns(nil)
	if err != nil {
		t.Errorf("nil input: unexpected error %v", err)
	}
	if got != nil {
		t.Errorf("nil input: expected nil slice, got %v", got)
	}

	got, err = ParseFollowPatterns([]string{})
	if err != nil {
		t.Errorf("empty input: unexpected error %v", err)
	}
	if got != nil {
		t.Errorf("empty input: expected nil slice, got %v", got)
	}

	patterns := []string{`^/patterns/`, `\.html$`, `go-details`}
	compiled, err := ParseFollowPatterns(patterns)
	if err != nil {
		t.Fatalf("valid patterns: %v", err)
	}
	if len(compiled) != len(patterns) {
		t.Fatalf("expected %d compiled patterns, got %d", len(patterns), len(compiled))
	}
	// Sanity check: each compiled pattern should match at least one
	// representative sample string.
	samples := []string{"/patterns/overview", "/page.html", "go-details-and-tips"}
	for i, re := range compiled {
		if !re.MatchString(samples[i]) {
			t.Errorf("pattern %q failed to match sample %q", patterns[i], samples[i])
		}
	}

	// Invalid regex — unbalanced bracket.
	_, err = ParseFollowPatterns([]string{`^ok$`, `[invalid`})
	if err == nil {
		t.Fatalf("expected error for invalid regex, got nil")
	}
	if !strings.Contains(err.Error(), "follow pattern 1") {
		t.Errorf("error %q does not identify the offending index", err.Error())
	}
	if !strings.Contains(err.Error(), "[invalid") {
		t.Errorf("error %q does not include the offending pattern text", err.Error())
	}
}

// TestWithCrawlOptionsRoundTrip verifies ctx plumbing carries the full
// CrawlOptions struct intact.
func TestWithCrawlOptionsRoundTrip(t *testing.T) {
	parent := context.Background()
	if _, ok := crawlOptionsFrom(parent); ok {
		t.Fatalf("background ctx must not carry CrawlOptions")
	}

	opts := CrawlOptions{
		Source:         "test",
		SeedURLs:       []string{"https://a.example/", "https://b.example/"},
		FollowPatterns: []string{`^/ok/`},
		MaxDepth:       3,
		MaxPages:       17,
		PolitenessMs:   250,
		UserAgent:      "test-agent/1.0",
	}
	ctx := WithCrawlOptions(parent, opts)
	got, ok := crawlOptionsFrom(ctx)
	if !ok {
		t.Fatalf("expected CrawlOptions in ctx, got none")
	}
	if got.Source != opts.Source ||
		got.MaxDepth != opts.MaxDepth ||
		got.MaxPages != opts.MaxPages ||
		got.PolitenessMs != opts.PolitenessMs ||
		got.UserAgent != opts.UserAgent {
		t.Errorf("round-tripped opts differ: got %+v want %+v", got, opts)
	}
	if len(got.SeedURLs) != len(opts.SeedURLs) || got.SeedURLs[0] != opts.SeedURLs[0] {
		t.Errorf("SeedURLs round-trip mismatch: %+v vs %+v", got.SeedURLs, opts.SeedURLs)
	}
	if len(got.FollowPatterns) != len(opts.FollowPatterns) || got.FollowPatterns[0] != opts.FollowPatterns[0] {
		t.Errorf("FollowPatterns round-trip mismatch: %+v vs %+v", got.FollowPatterns, opts.FollowPatterns)
	}
}

// TestApplyDefaults covers the zero-value fill path. MaxDepth and
// MaxPages are intentionally left at zero (meaning unbounded) because
// silent truncation at an arbitrary cap hid catalog-size bugs in the
// old defaults — callers opt into a hard cap explicitly.
func TestApplyDefaults(t *testing.T) {
	opts := CrawlOptions{Source: "s", SeedURLs: []string{"u"}}
	filled := opts.ApplyDefaults()
	if filled.MaxDepth != 0 {
		t.Errorf("MaxDepth: got %d, want 0 (unbounded by default)", filled.MaxDepth)
	}
	if filled.MaxPages != 0 {
		t.Errorf("MaxPages: got %d, want 0 (unbounded by default)", filled.MaxPages)
	}
	if filled.PolitenessMs != defaultPolitenessMs {
		t.Errorf("PolitenessMs: got %d, want %d", filled.PolitenessMs, defaultPolitenessMs)
	}
	if filled.MaxConcurrency != defaultMaxConcurrency {
		t.Errorf("MaxConcurrency: got %d, want %d", filled.MaxConcurrency, defaultMaxConcurrency)
	}
	if filled.UserAgent != defaultUserAgent {
		t.Errorf("UserAgent: got %q, want %q", filled.UserAgent, defaultUserAgent)
	}

	// Explicit values not clobbered.
	set := CrawlOptions{
		Source: "s", SeedURLs: []string{"u"},
		MaxDepth: 7, MaxPages: 99, PolitenessMs: 123, UserAgent: "custom/1.0",
	}
	filledSet := set.ApplyDefaults()
	if filledSet.MaxDepth != 7 ||
		filledSet.MaxPages != 99 ||
		filledSet.PolitenessMs != 123 ||
		filledSet.UserAgent != "custom/1.0" {
		t.Errorf("explicit values clobbered: got %+v want %+v", filledSet, set)
	}
}
