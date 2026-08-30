// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/llm/precheck"
)

// TestRunPrecheck_ForwardsRerankCheck pins this package's half of the
// rerank-check wiring. It does not NAME rerank.CheckProvider — importing rerank
// here would pull the whole graph client into a provider package
// (rerank -> engine -> graphclient) — so its duty is narrower and exactly two
// things: REFUSE a nil, and FORWARD what it was given to precheck.RunAll
// unchanged.
func TestRunPrecheck_ForwardsRerankCheck(t *testing.T) {
	t.Run("nil is refused, never treated as skip", func(t *testing.T) {
		t.Cleanup(config.SetForTest(&config.Config{}))
		err := RunPrecheck(context.Background(), false, nil)
		if err == nil {
			t.Fatal("a nil rerank check must be REFUSED — a check that silently stops running is the failure this parameter exists to prevent")
		}
		if !strings.Contains(err.Error(), "rerank.CheckProvider") {
			t.Errorf("the refusal %q must name the caller's duty", err)
		}
	})

	t.Run("skip short-circuits before the nil gate", func(t *testing.T) {
		// --skip-llm-precheck is an explicit operator opt-out and predates
		// this parameter; it must not become an error just because the
		// caller had nothing to pass.
		if err := RunPrecheck(context.Background(), true, nil); err != nil {
			t.Errorf("skip=true must no-op, got %v", err)
		}
	})

	t.Run("a real check is accepted", func(t *testing.T) {
		t.Cleanup(config.SetForTest(&config.Config{}))
		noop := func(context.Context, config.RerankSection) error { return nil }
		if err := RunPrecheck(context.Background(), false, precheck.RerankCheck(noop)); err != nil {
			t.Errorf("a non-nil check must be accepted, got %v", err)
		}
	})

	// CALL-SHAPE ANCHOR. The forward itself runs inside an async goroutine
	// that makes live network calls, so the property is pinned in source:
	// RunAll must receive the forwarded parameter, not a fresh nil.
	t.Run("the check is forwarded to RunAll", func(t *testing.T) {
		raw, err := os.ReadFile("precheck.go")
		if err != nil {
			t.Fatalf("read precheck.go: %v", err)
		}
		var kept []string
		for line := range strings.SplitSeq(string(raw), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			kept = append(kept, line)
		}
		src := strings.Join(kept, "\n")
		if strings.TrimSpace(src) == "" {
			t.Fatal("precheck.go stripped to nothing — the reader is broken")
		}

		// `.*` not `[^)]*` — an argument list can contain parentheses, and
		// a class excluding ')' silently truncates at the first inner one.
		fwd := regexp.MustCompile(`precheck\.RunAll\(.*,\s*checkRerank\)`)
		if !fwd.MatchString(src) {
			t.Errorf("RunPrecheck must forward its checkRerank parameter to precheck.RunAll")
		}
		if !fwd.MatchString("precheck.RunAll(runCtx, active, consumers, checkRerank)") {
			t.Fatal("the forward matcher rejects the correct call — it would red on healthy code")
		}
		if regexp.MustCompile(`precheck\.RunAll\(.*,\s*nil\)`).MatchString(src) {
			t.Errorf("RunPrecheck passes nil to RunAll — the forwarded check would never run")
		}
		// KNOWN-NEGATIVE for the matcher.
		if fwd.MatchString("precheck.RunAll(runCtx, active, consumers, nil)") {
			t.Fatal("the forward matcher accepts nil — it cannot detect the regression it exists for")
		}
	})
}
