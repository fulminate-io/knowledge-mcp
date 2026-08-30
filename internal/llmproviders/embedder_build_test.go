// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
)

// TestBuildEmbedder_WarnsOnNonDefaultModel proves the withheld-flip
// hazard is surfaced to the operator who can actually hit it: setting
// [embedder].model by hand today mixes vector spaces on an existing graph,
// silently, because nothing re-embeds.
//
// The warning is captured off a swapped slog handler. The DEFAULT-model
// and empty-model cases are the known-positive controls: both must stay
// SILENT, so a warn-on-everything implementation fails this test.
func TestBuildEmbedder_WarnsOnNonDefaultModel(t *testing.T) {
	ctx := context.Background()
	t.Setenv("VOYAGE_API_KEY", "k")

	build := func(t *testing.T, model string) string {
		t.Helper()
		var buf bytes.Buffer
		prior := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
		t.Cleanup(func() { slog.SetDefault(prior) })

		sec := &config.EmbedSection{
			Provider: config.EmbedProviderVoyage, Model: model,
			Dimension: 256, Dtype: "ubinary",
		}
		t.Cleanup(config.SetForTest(&config.Config{Embedder: sec}))
		if _, err := BuildEmbedder(ctx, embed.InputRoleDocument); err != nil {
			t.Fatalf("BuildEmbedder(model=%q): %v", model, err)
		}
		return buf.String()
	}

	t.Run("a non-default model warns", func(t *testing.T) {
		out := build(t, "voyage-code-4")
		if !strings.Contains(out, "does NOT re-embed an existing graph") {
			t.Errorf("no re-embed warning for a non-default model; log was:\n%s", out)
		}
		if !strings.Contains(out, "voyage-code-4") {
			t.Errorf("the warning does not name the configured model; log was:\n%s", out)
		}
		// The message must not imply a check the code cannot perform.
		if !strings.Contains(out, "cannot tell which model produced the existing vectors") {
			t.Errorf("the warning must state its own limitation; log was:\n%s", out)
		}
	})

	t.Run("the arm default is silent", func(t *testing.T) {
		if out := build(t, embed.DefaultModel); strings.Contains(out, "does NOT re-embed") {
			t.Errorf("the arm's own default model must not warn; log was:\n%s", out)
		}
	})

	t.Run("an empty model is silent", func(t *testing.T) {
		if out := build(t, ""); strings.Contains(out, "does NOT re-embed") {
			t.Errorf("the no-config case must not warn; log was:\n%s", out)
		}
	})
}

// TestBuildEmbedder_ErrorsOnMisconfig pins the split this step introduces:
// a MISSING CREDENTIAL is the documented BM25-only degrade (nil, nil), and
// every other misconfiguration is now an ERROR where before there was
// nothing to be wrong about.
//
// The degrade case is the known-positive control for the error cases: if
// BuildEmbedder simply errored on everything, the error assertions below
// would pass vacuously and this one would not.
func TestBuildEmbedder_ErrorsOnMisconfig(t *testing.T) {
	ctx := context.Background()
	// No credentials anywhere, so the resolved API provider has neither a
	// key nor a base_url.
	t.Setenv("VOYAGE_API_KEY", "")
	t.Setenv("COHERE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	t.Run("unloaded config degrades", func(t *testing.T) {
		t.Cleanup(config.SetForTest(nil))
		e, err := BuildEmbedder(ctx, embed.InputRoleDocument)
		if err != nil {
			t.Fatalf("an unloaded config must degrade, not error: %v", err)
		}
		if e != nil {
			t.Errorf("an unloaded config must yield a nil embedder; got %T", e)
		}
	})

	t.Run("missing credential degrades", func(t *testing.T) {
		t.Cleanup(config.SetForTest(&config.Config{}))
		e, err := BuildEmbedder(ctx, embed.InputRoleDocument)
		if err != nil {
			t.Fatalf("a missing credential is the documented degrade, not an error: %v", err)
		}
		if e != nil {
			t.Errorf("a missing credential must yield a nil embedder; got %T", e)
		}
	})

	t.Run("credential present builds", func(t *testing.T) {
		t.Setenv("VOYAGE_API_KEY", "k")
		t.Cleanup(config.SetForTest(&config.Config{}))
		e, err := BuildEmbedder(ctx, embed.InputRoleDocument)
		if err != nil {
			t.Fatalf("BuildEmbedder with a credential: %v", err)
		}
		if e == nil {
			t.Fatal("a resolved credential must produce a real embedder")
		}
		if !e.Available() {
			t.Error("the built embedder reports itself unavailable")
		}
	})

	t.Run("malformed section errors", func(t *testing.T) {
		// A section that never came from TOML and so never met the
		// parser's admission gate: this is exactly the hole the in-Go
		// Validate layer closes.
		t.Setenv("VOYAGE_API_KEY", "k")
		// 1024 IS NOW AN ACCEPTED WIDTH, so this fixture moved to one that is
		// genuinely outside the set. The property under test is unchanged — an
		// off-set section must ERROR rather than degrade — but a fixture whose
		// values have become valid tests nothing.
		t.Cleanup(config.SetForTest(&config.Config{
			Embedder: &config.EmbedSection{Provider: config.EmbedProviderVoyage, Dimension: 3072, Dtype: "ubinary"},
		}))
		_, err := BuildEmbedder(ctx, embed.InputRoleDocument)
		if err == nil {
			t.Fatal("an off-width section must ERROR, not degrade silently")
		}
		if !strings.Contains(err.Error(), "3072") {
			t.Errorf("error %q does not name the offending dimension", err)
		}
	})

	t.Run("unknown provider errors", func(t *testing.T) {
		t.Setenv("VOYAGE_API_KEY", "k")
		t.Cleanup(config.SetForTest(&config.Config{
			Embedder: &config.EmbedSection{Provider: config.EmbedProvider("anthropic"), Dimension: 256, Dtype: "ubinary"},
		}))
		_, err := BuildEmbedder(ctx, embed.InputRoleDocument)
		if err == nil {
			t.Fatal("an unknown provider must ERROR")
		}
		if !strings.Contains(err.Error(), "anthropic") {
			t.Errorf("error %q does not name the offending provider", err)
		}
	})

	t.Run("role reaches the arm", func(t *testing.T) {
		t.Setenv("VOYAGE_API_KEY", "k")
		t.Cleanup(config.SetForTest(&config.Config{}))
		for _, role := range []embed.InputRole{embed.InputRoleDocument, embed.InputRoleQuery} {
			if _, err := BuildEmbedder(ctx, role); err != nil {
				t.Errorf("BuildEmbedder(%q): %v", role, err)
			}
		}
	})
}
