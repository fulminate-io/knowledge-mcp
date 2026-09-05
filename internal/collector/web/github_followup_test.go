// SPDX-License-Identifier: Apache-2.0

package web

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestCollect_GithubLinksAreFollowUpCandidatesNotFailures gates the two user
// rulings that shape this lane: "github links are things the user would decide
// to follow up on, its not a failure at all", and, on whether the collector
// should materialize them by itself, "Gate behind an explicit opt-in".
//
// EVERY LEG RUNS UNDER THIS ONE TEST, because the stored gate anchors its -run
// selector on this name alone; a leg in a sibling top-level test would never
// execute under it.
func TestCollect_GithubLinksAreFollowUpCandidatesNotFailures(t *testing.T) {
	// --- LEGS 1-3: the links are REPORTED, the cap is engaged, nothing reads
	// as failure ------------------------------------------------------------
	t.Run("links_are_reported_and_the_sample_cap_is_engaged", func(t *testing.T) {
		repos := []string{
			"https://github.com/fulminate-io/knowledge-mcp",
			"https://github.com/golang/go",
			"https://github.com/google/go-cmp",
			"https://github.com/hashicorp/raft",
			"https://github.com/stretchr/testify",
		}
		body := &strings.Builder{}
		body.WriteString(`<h1>Reading List</h1>
<p>A page of substantive prose that also links out to several repositories a
reader might want to look at later.</p>`)
		for _, r := range repos {
			fmt.Fprintf(body, `<p>The repository at <a href="%s">%s</a>, worth a look.</p>`, r, r)
		}
		comp := serveComposition(t, "gh-followups", fmt.Sprintf(
			`<!doctype html><html><head><title>list</title></head><body><article>%s</article></body></html>`, body.String()))

		rendered := comp.Render()
		// LEG 1 — the harvest still lands, and the links are reported.
		assert.Positive(t, comp.NodesByType["page"], "the page itself must still be harvested")
		assert.Positive(t, comp.NodesByType["paragraph"], "the page's prose must still be harvested")
		assert.Contains(t, rendered, "github follow-ups 5",
			"five distinct repositories must be reported as follow-up candidates: %s", rendered)

		// LEG 2 — THE CEILING IS ENGAGED BY THE FIXTURE, not asserted in prose.
		// Five links against a three-URL cap must render the omission count,
		// and the COUNT beside it must stay exact under truncation.
		assert.Contains(t, rendered, "+2 more",
			"the three-URL sample cap must state how many it omitted: %s", rendered)

		// LEG 3 — NOTHING HERE READS AS FAILURE. An unmaterialized repository
		// is a decision the caller gets to make, not dropped work.
		assert.Empty(t, comp.Degraded, "a github link is not a degrade, got %v", comp.Degraded)
		require.NoError(t, collector.CheckComposition("web", comp),
			"a harvest that met github links must not be reported as a failure")
	})

	// --- LEG 1b: ONE REPOSITORY IS ONE FOLLOW-UP, WHATEVER THE SPELLING ----
	//
	// github.com/a/b and github.com/a/b/tree/main are the SAME repository. A
	// URL-string key counts them twice, which inflates the count a caller acts
	// on and, on the exclusion side, misses a repository this run materialized
	// when it was met under a second spelling. Both halves key on the
	// owner/repo pair parseGitHubURL already returns.
	t.Run("one_repository_counts_once_across_spellings", func(t *testing.T) {
		body := `<h1>Spellings</h1>
<p>Substantive prose so the page is harvested on its own merits, followed by
three links that all name one and the same repository.</p>
<p>Bare: <a href="https://github.com/owner/repo">owner/repo</a>.</p>
<p>Tree: <a href="https://github.com/owner/repo/tree/main">owner/repo at main</a>.</p>
<p>Blob: <a href="https://github.com/owner/repo/blob/main/README.md">its readme</a>.</p>
<p>And one genuinely different repository: <a href="https://github.com/other/thing">other/thing</a>.</p>`
		comp := serveComposition(t, "gh-spellings", fmt.Sprintf(
			`<!doctype html><html><head><title>s</title></head><body><article>%s</article></body></html>`, body))

		rendered := comp.Render()
		t.Logf("spellings crawl rendered: %s", rendered)
		assert.Equal(t, 2, comp.GithubFollowUps,
			"three spellings of owner/repo plus one other/thing is TWO repositories, not four: %s", rendered)
		// The FIRST-SEEN spelling is the one rendered, so the caller gets a URL
		// that actually appeared on the page rather than a normalized synthetic.
		assert.Contains(t, comp.GithubFollowUpSample, "https://github.com/owner/repo",
			"the rendered sample must keep the first-seen spelling")
	})

	// --- LEG 5b: A MATERIALIZED REPOSITORY IS EXCLUDED UNDER ANY SPELLING ---
	t.Run("a_materialized_repository_is_excluded_under_a_second_spelling", func(t *testing.T) {
		initWebTestSink(t)
		codeload := fixtureCodeloadServer(t, generateFixtureTarball(t))
		t.Cleanup(codeload.Close)
		withCodeloadBaseURL(t, codeload.URL)

		// The page links the repository by its BARE spelling; the seed
		// materializes it by its TREE spelling. One repository, two strings.
		page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>p</title></head><body><article>
<h1>A Page That Links The Same Repository</h1>
<p>This page carries substantive prose and links the repository the seed below
materializes, but by a different spelling of the same owner and repo.</p>
<p>See <a href="https://github.com/owner/repo">owner/repo</a> for the source.</p>
</article></body></html>`))
		}))
		t.Cleanup(page.Close)

		opts := CrawlOptions{
			Source:            "gh-spelling-exclusion",
			SeedURLs:          []string{page.URL + "/index.html", "https://github.com/owner/repo/tree/main"},
			MaxDepth:          0,
			MaxPages:          2,
			PolitenessMs:      0,
			MaxDownloadBytes:  50 << 20,
			MaterializeGithub: true,
		}
		comp, err := collector.Collect(WithCrawlOptions(context.Background(), opts), "web", "gh-spelling-exclusion", collector.CollectOptions{Force: true})
		require.NoError(t, err)

		require.Equal(t, 1, comp.NodesByType[string(kgtypes.NodeGithubRepo)],
			"the seed must have materialized, or the exclusion below has nothing to exclude")
		assert.Zero(t, comp.GithubFollowUps,
			"the page's bare spelling names the repository the seed already materialized, so it is DONE work rather than a follow-up: %s", comp.Render())
	})

	// --- LEG 5c: A SECOND REF OF A MATERIALIZED REPOSITORY SURVIVES --------
	//
	// The materializer's unit of work is (owner, repo, ref): buildGhRoot mints
	// one gh-root PER REF, so a repository materialized at main leaves a link
	// to a DIFFERENT ref outstanding. Keyed on the owner/repo pair alone the
	// exclusion swallows that link and the caller is told there is nothing to
	// follow up on, though the ref was never fetched.
	t.Run("a_second_ref_of_a_materialized_repository_is_still_a_follow_up", func(t *testing.T) {
		initWebTestSink(t)
		codeload := fixtureCodeloadServer(t, generateFixtureTarball(t))
		t.Cleanup(codeload.Close)
		withCodeloadBaseURL(t, codeload.URL)

		// MaxDepth 0 keeps the linked ref out of the queue, so v2-release is
		// linked and never fetched — exactly the state a follow-up reports.
		page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>p</title></head><body><article>
<h1>A Page That Links A Second Ref</h1>
<p>This page carries substantive prose and links a release branch of the very
repository the seed beside it materializes at main, which is a different unit
of work the caller has not got.</p>
<p>See <a href="https://github.com/owner/repo/tree/v2-release">owner/repo at v2-release</a>.</p>
</article></body></html>`))
		}))
		t.Cleanup(page.Close)

		opts := CrawlOptions{
			Source:            "gh-second-ref",
			SeedURLs:          []string{page.URL + "/index.html", "https://github.com/owner/repo/tree/main"},
			MaxDepth:          0,
			MaxPages:          2,
			PolitenessMs:      0,
			MaxDownloadBytes:  50 << 20,
			MaterializeGithub: true,
		}
		comp, err := collector.Collect(WithCrawlOptions(context.Background(), opts), "web", "gh-second-ref", collector.CollectOptions{Force: true})
		require.NoError(t, err)

		require.Equal(t, 1, comp.NodesByType[string(kgtypes.NodeGithubRepo)],
			"the seed must have materialized at main, or this leg has no exclusion left to test")
		assert.Equal(t, 1, comp.GithubFollowUps,
			"a ref this crawl never fetched must survive the exclusion of the ref it did materialize: %s", comp.Render())
		assert.Contains(t, comp.GithubFollowUpSample, "https://github.com/owner/repo/tree/v2-release",
			"the surviving follow-up must be the linked ref itself: %s", comp.Render())
	})

	// --- LEG 5d: THE SAME, WITH THE MATERIALIZED ROOT SPELLED BARE ---------
	//
	// LEG 5c's PAIR, and the branch it cannot reach. 5c seeds a NAMED ref, so
	// it exercises only the named-ref side of the exclusion. A BARE seed
	// materializes the default branch and its gh-root carries ref="" — and
	// materializing the default branch is not materializing every ref, so a
	// link to a distinct ref must still survive. Without this leg the empty-ref
	// branch of the exclusion ships unexercised, which is how the pair-keyed
	// defect reappeared here after leg 5c had closed it for named refs.
	t.Run("a_bare_materialized_root_does_not_exclude_a_named_ref", func(t *testing.T) {
		initWebTestSink(t)
		codeload := fixtureCodeloadServer(t, generateFixtureTarball(t))
		t.Cleanup(codeload.Close)
		withCodeloadBaseURL(t, codeload.URL)

		page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>p</title></head><body><article>
<h1>A Page That Links A Named Ref</h1>
<p>This page carries substantive prose and links a release branch, while the
seed beside it materializes the repository's default branch under its bare
spelling, which is a different unit of work.</p>
<p>See <a href="https://github.com/owner/repo/tree/v2-release">owner/repo at v2-release</a>.</p>
</article></body></html>`))
		}))
		t.Cleanup(page.Close)

		opts := CrawlOptions{
			Source:            "gh-bare-root",
			SeedURLs:          []string{page.URL + "/index.html", "https://github.com/owner/repo"},
			MaxDepth:          0,
			MaxPages:          2,
			PolitenessMs:      0,
			MaxDownloadBytes:  50 << 20,
			MaterializeGithub: true,
		}
		comp, err := collector.Collect(WithCrawlOptions(context.Background(), opts), "web", "gh-bare-root", collector.CollectOptions{Force: true})
		require.NoError(t, err)

		require.Equal(t, 1, comp.NodesByType[string(kgtypes.NodeGithubRepo)],
			"the bare seed must have materialized, or this leg has no exclusion left to test")
		assert.Equal(t, 1, comp.GithubFollowUps,
			"materializing the default branch is not materializing every ref, so the linked v2-release must survive: %s", comp.Render())
		assert.Contains(t, comp.GithubFollowUpSample, "https://github.com/owner/repo/tree/v2-release",
			"the surviving follow-up must be the linked ref itself: %s", comp.Render())
	})

	// --- LEG 4: THE OPT-OUT PATH -------------------------------------------
	//
	// THE CODELOAD OVERRIDE IS INSTALLED BEFORE THIS CRAWL, and that is what
	// makes the leg discriminating rather than vacuous: a fixture tarball is
	// actually being served, so an implementation that ignores the opt-in
	// SUCCEEDS in materializing and this leg goes red. Without the override
	// the same leg would pass any time a real network fetch merely failed.
	t.Run("without_the_opt_in_nothing_is_materialized", func(t *testing.T) {
		sink := initWebTestSink(t)
		codeload := fixtureCodeloadServer(t, generateFixtureTarball(t))
		t.Cleanup(codeload.Close)
		withCodeloadBaseURL(t, codeload.URL)

		page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>p</title></head><body><article>
<h1>An Ordinary Page</h1>
<p>This page carries substantive prose so the harvest lands on its own merits,
independently of anything the github seed beside it does or does not do.</p>
</article></body></html>`))
		}))
		t.Cleanup(page.Close)

		const seed = "https://github.com/owner/repo/tree/main"
		opts := CrawlOptions{
			Source:           "gh-optout",
			SeedURLs:         []string{page.URL + "/index.html", seed},
			MaxDepth:         0,
			MaxPages:         2,
			PolitenessMs:     0,
			MaxDownloadBytes: 50 << 20,
			// MaterializeGithub deliberately UNSET.
		}
		comp, err := collector.Collect(WithCrawlOptions(context.Background(), opts), "web", "gh-optout", collector.CollectOptions{Force: true})
		require.NoError(t, err)

		assert.Zero(t, comp.NodesByType[string(kgtypes.NodeGithubRepo)],
			"no repository may be materialized without the explicit opt-in")
		assert.Zero(t, comp.NodesByType[string(kgtypes.NodeFile)],
			"no repository file may be materialized without the explicit opt-in")
		assert.Positive(t, comp.NodesByType["page"], "the HTML seed must still be harvested")
		assert.Contains(t, comp.Render(), "github follow-ups 1",
			"the unmaterialized github seed must be reported as a follow-up candidate: %s", comp.Render())
		require.NotNil(t, sink.last())
	})

	// --- LEG 5: THE OPT-IN PATH, AND ITS EXCLUSION -------------------------
	//
	// It is a PAIR with leg 4: the SAME seed URL must be REPORTED when the
	// opt-in is absent and NOT reported when it is present, so an
	// implementation that simply stopped reporting github seeds fails leg 4.
	t.Run("with_the_opt_in_the_repository_materializes_and_is_not_a_follow_up", func(t *testing.T) {
		initWebTestSink(t)
		codeload := fixtureCodeloadServer(t, generateFixtureTarball(t))
		t.Cleanup(codeload.Close)
		withCodeloadBaseURL(t, codeload.URL)

		opts := CrawlOptions{
			Source:            "gh-optin",
			SeedURLs:          []string{"https://github.com/owner/repo/tree/main"},
			MaxDepth:          0,
			MaxPages:          1,
			PolitenessMs:      0,
			MaxDownloadBytes:  50 << 20,
			MaterializeGithub: true,
		}
		comp, err := collector.Collect(WithCrawlOptions(context.Background(), opts), "web", "gh-optin", collector.CollectOptions{Force: true})
		require.NoError(t, err)

		assert.Equal(t, 1, comp.NodesByType[string(kgtypes.NodeGithubRepo)],
			"the opt-in must materialize the repository exactly as before")
		assert.GreaterOrEqual(t, comp.NodesByType[string(kgtypes.NodeFile)], 3,
			"the opt-in must materialize the repository's files")
		// A repository whose nodes are already in the graph is work that is
		// DONE, not a candidate the caller still has to follow up on.
		assert.NotContains(t, comp.Render(), "github follow-ups",
			"a materialized repository must not be reported as a follow-up candidate: %s", comp.Render())
	})

	// --- LEG 6: THE REFUSAL -------------------------------------------------
	t.Run("an_opt_in_that_could_never_fire_is_refused", func(t *testing.T) {
		err := ValidateCrawlOptions(CrawlOptions{
			Source:            "gh-refusal",
			SeedURLs:          []string{"https://example.com/page.html"},
			MaterializeGithub: true,
		})
		require.Error(t, err, "an opt-in with no github seed must be refused, not silently inert")
		assert.Contains(t, err.Error(), "MaterializeGithub",
			"the refusal must name the option so it cannot pass on another field's rejection")

		// CONTROL: the same opt-in WITH a github seed is accepted, so the
		// refusal is about the seed set rather than about the flag.
		require.NoError(t, ValidateCrawlOptions(CrawlOptions{
			Source:            "gh-refusal-control",
			SeedURLs:          []string{"https://github.com/owner/repo"},
			MaterializeGithub: true,
		}))
	})

	// --- LEGS 7-8: the per-entry unpack classes ----------------------------
	t.Run("crafted_tar_entries_drive_every_github_degrade_class", func(t *testing.T) {
		initWebTestSink(t)
		codeload := fixtureCodeloadServer(t, hostileTarball(t))
		t.Cleanup(codeload.Close)
		withCodeloadBaseURL(t, codeload.URL)

		opts := CrawlOptions{
			Source:            "gh-hostile",
			SeedURLs:          []string{"https://github.com/owner/repo/tree/main"},
			MaxDepth:          0,
			MaxPages:          1,
			PolitenessMs:      0,
			MaxDownloadBytes:  50 << 20,
			MaterializeGithub: true,
		}
		comp, err := collector.Collect(WithCrawlOptions(context.Background(), opts), "web", "gh-hostile", collector.CollectOptions{Force: true})
		require.NoError(t, err, "no github outcome may fail a harvest")

		rendered := comp.Render()
		t.Logf("hostile-tarball crawl rendered: %s", rendered)
		for _, class := range []string{
			"github_unsafe_path_rejected", "github_nonregular_entry", "github_unpack_failed",
		} {
			assert.Contains(t, rendered, class+" ",
				"the crafted tar entry for %s must reach the rendered census: %s", class, rendered)
		}
	})

	t.Run("a_truncated_archive_drives_the_tar_read_class", func(t *testing.T) {
		initWebTestSink(t)
		// Its own crawl: a read failure aborts the rest of the unpack, so this
		// class cannot share a tarball with the per-entry classes above.
		codeload := fixtureCodeloadServer(t, truncatedTarball(t))
		t.Cleanup(codeload.Close)
		withCodeloadBaseURL(t, codeload.URL)

		opts := CrawlOptions{
			Source:            "gh-truncated",
			SeedURLs:          []string{"https://github.com/owner/repo/tree/main"},
			MaxDepth:          0,
			MaxPages:          1,
			PolitenessMs:      0,
			MaxDownloadBytes:  50 << 20,
			MaterializeGithub: true,
		}
		comp, err := collector.Collect(WithCrawlOptions(context.Background(), opts), "web", "gh-truncated", collector.CollectOptions{Force: true})
		require.NoError(t, err, "no github outcome may fail a harvest")
		assert.Contains(t, comp.Render(), "github_tar_read_failed ",
			"a stream cut mid-entry must reach the rendered census: %s", comp.Render())
	})

	// --- LEG 9: the composition reversal, WITH its control -----------------
	//
	// The retired version of this criterion asserted that a failed
	// materialization must FAIL the harvest. This leg pins the reversal by
	// naming the two class keys that leg used, so reintroducing it under those
	// names is caught. A future fatal github leg under a NEW name is a review
	// duty, not something this gate can see.
	t.Run("a_github_failure_is_not_a_harvest_failure", func(t *testing.T) {
		withGithubFailures := compFrom("gh-not-fatal", map[string]int{
			"page": 1, "paragraph": 4, "raw_html": 1, "section": 1,
		})
		withGithubFailures.Degraded = map[string]int{
			"github_tarball_failed": 1,
			"github_parse_failed":   1,
		}
		require.NoError(t, (&WebCollector{}).AssertComposition(withGithubFailures),
			"a github repository that was not materialized is not a harvest failure")

		// CONTROL, through the SAME invariant: deleting AssertComposition's
		// body would satisfy the assertion above, so a composition that MUST
		// be rejected is run through it here.
		chromeOnly := compFrom("gh-control-chrome", map[string]int{
			"page": 1, "raw_html": 1, "section": 1, "link": 8,
		})
		require.Error(t, (&WebCollector{}).AssertComposition(chromeOnly),
			"the chrome-only leg must still fire; without it the acceptance above proves nothing")
	})
}

// hostileTarball builds a gzipped codeload-shaped tarball carrying, in one
// archive, the three entry shapes whose losses the unpack counts:
//
//   - a path that escapes the root once the codeload top directory is stripped
//   - a symlink, which the unpacker skips as a non-regular entry
//   - a regular file whose PARENT PATH is itself a regular file, so the write
//     cannot create the directory it needs
func hostileTarball(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	write := func(hdr *tar.Header, body []byte) {
		hdr.Size = int64(len(body))
		require.NoError(t, tw.WriteHeader(hdr))
		if len(body) > 0 {
			_, err := tw.Write(body)
			require.NoError(t, err)
		}
	}

	write(&tar.Header{Name: "repo-abc/", Typeflag: tar.TypeDir, Mode: 0o755}, nil)
	// A real file, so the archive is not made entirely of hostile entries and
	// the materialization still produces something.
	write(&tar.Header{Name: "repo-abc/README.md", Typeflag: tar.TypeReg, Mode: 0o644},
		[]byte("# Fixture\n\nA regular file so the unpack is not empty.\n"))
	// github_unsafe_path_rejected.
	write(&tar.Header{Name: "repo-abc/../escaped.txt", Typeflag: tar.TypeReg, Mode: 0o644},
		[]byte("this entry escapes the unpack root"))
	// github_nonregular_entry.
	write(&tar.Header{Name: "repo-abc/link.txt", Typeflag: tar.TypeSymlink, Linkname: "README.md", Mode: 0o644}, nil)
	// github_unpack_failed: blocker.txt is a FILE, so blocker.txt/child.txt
	// cannot have its parent directory created.
	write(&tar.Header{Name: "repo-abc/blocker.txt", Typeflag: tar.TypeReg, Mode: 0o644}, []byte("i am a file, not a directory"))
	write(&tar.Header{Name: "repo-abc/blocker.txt/child.txt", Typeflag: tar.TypeReg, Mode: 0o644}, []byte("my parent is a file"))

	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

// truncatedTarball builds a gzipped tarball whose tar stream is cut off in the
// middle of an entry's body, so tar.Next fails partway through the unpack.
func truncatedTarball(t *testing.T) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "repo-abc/", Typeflag: tar.TypeDir, Mode: 0o755}))
	body := bytes.Repeat([]byte("payload-"), 512)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "repo-abc/big.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body)),
	}))
	_, err := tw.Write(body)
	require.NoError(t, err)
	require.NoError(t, tw.Close())

	// Cut the tar stream mid-entry, then gzip the truncated bytes so the gzip
	// layer itself is valid and the failure lands in tar.Next as intended.
	cut := raw.Bytes()
	cut = cut[:len(cut)-2048]

	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	_, err = gz.Write(cut)
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	return out.Bytes()
}
