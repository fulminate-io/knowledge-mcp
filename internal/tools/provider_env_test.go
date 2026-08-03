// SPDX-License-Identifier: Apache-2.0

package tools

// provider_env_test.go clears the provider credential env vars for the whole
// package before any test runs, and pins that clearing with a control test.
//
// WHY THIS IS NEEDED. config.VoyageAPIKey resolves [credentials].voyage_api_key
// and falls through to the VOYAGE_API_KEY env var whenever the config value is
// empty — which is exactly what an unloaded config singleton produces under
// `go test`. Two call sites in this package read that key and build a real
// Voyage client aimed at the production endpoint: the rerank gate in search.go
// and the recall rerank seam in intercept_thoughts_recall.go. On a developer
// machine with the variable exported, that produced nine live round trips to
// api.voyageai.com per package run — eight of them billed against the
// developer's own key — from tests whose graph client, segment engine and
// embedder are all fakes. Every one of them passed either way, so the only
// symptoms were wall-clock time, a dependence on the network, and the bill.
// (The ninth supplies its own fake key; see WHAT THIS DOES NOT COVER below.)
//
// WHY AT THE SUITE BOUNDARY rather than per test. Per-test t.Setenv is the same
// fix applied by hand, and it is opt-in per author: it protects the file being
// written and leaves every sibling — and every test added later — exposed by
// default. TestMain inverts that default for the package. A test that genuinely
// wants the keyed path still opts in with t.Setenv to a fake value, which
// overrides for that test's duration and restores afterwards.
//
// WHAT THIS DOES NOT COVER. A test that sets its own non-empty key still
// reaches the live endpoint, because the reranker hardcodes the production URL
// and its baseURL override is package-private to internal/rerank. Neutralizing
// the environment cannot reach that case; an endpoint seam would.

import (
	"os"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// providerKeyEnv is every credential env var the config accessors fall through
// to. Cleared wholesale rather than Voyage-only: the fall-through is identical
// for each one, so a test that reaches a different provider tomorrow inherits
// the same defect for the same reason.
var providerKeyEnv = []string{
	"VOYAGE_API_KEY",
	"LINEAR_API_KEY",
	"ANTHROPIC_API_KEY",
	"OPENAI_API_KEY",
	"GEMINI_API_KEY",
}

// TestMain clears the provider credentials before the suite runs. Unset rather
// than set-empty: absent is the state a clean CI machine has, and no resolver
// in config or bootstrap distinguishes the two (all of them read os.Getenv).
func TestMain(m *testing.M) {
	for _, k := range providerKeyEnv {
		if err := os.Unsetenv(k); err != nil {
			panic("clearing " + k + " for the test suite: " + err.Error())
		}
	}
	os.Exit(m.Run())
}

// TestProviderEnvCleared is the control for TestMain above. Asserting only that
// the key reads empty would pass just as happily on a machine that never had
// one, on a build where TestMain was deleted, and on a resolver broken to
// always return "" — three very different states sharing one observation. The
// keyed subtest is the known positive that separates them: it proves the
// resolver does read the environment, so the empty read in the first subtest is
// evidence of neutralization rather than of nothing being there to neutralize.
func TestProviderEnvCleared(t *testing.T) {
	t.Run("suite runs keyless", func(t *testing.T) {
		t.Cleanup(config.SetForTest(nil)) // read the environment, not a singleton a sibling test loaded
		// Report the length, never the value: on the machine this is meant to
		// catch, the value IS a live credential, and a failing test's output
		// goes to CI logs.
		if got := config.VoyageAPIKey(); got != "" {
			t.Fatalf("config.VoyageAPIKey() returned a %d-character key under the suite, want empty — TestMain's neutralization is not engaging, and these tests are making live Voyage calls", len(got))
		}
	})

	t.Run("resolver still reads the environment", func(t *testing.T) {
		t.Cleanup(config.SetForTest(nil))
		t.Setenv("VOYAGE_API_KEY", "sentinel-not-a-real-key")
		if got := config.VoyageAPIKey(); got != "sentinel-not-a-real-key" {
			t.Fatalf("config.VoyageAPIKey() returned a %d-character key with the env var set to the sentinel, want the sentinel — the resolver no longer reads the environment, so the keyless assertion above proves nothing", len(got))
		}
	})
}
