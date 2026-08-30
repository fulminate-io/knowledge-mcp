// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The rerank startup check reaches precheck.RunAll through a REQUIRED
// function parameter, because package llm/precheck cannot import rerank
// without closing an import cycle. That indirection has one failure mode
// worth a standing gate: a caller passing nil, or passing something other
// than the real check, and the rerank axis quietly never running again.
//
// RunAll and RunPrecheck both REFUSE nil at runtime, which catches it on
// the first boot. These tests catch it at commit time instead, by pinning
// the CALL SHAPE at each of the two supply sites — the places where
// rerank.CheckProvider is actually named. A future edit that drops the
// argument, renames it, or replaces it with nil reds these.
//
// They read the source rather than executing the call because both sites
// are inside boot paths that make live network calls; the shape is the
// property under test, and the runtime refusals cover the behavior.

// readSource returns a source file's text with full-line comments
// stripped, so a call shape mentioned in prose cannot satisfy the anchor.
func readSource(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // fixed in-repo test path
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var kept []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		kept = append(kept, line)
	}
	out := strings.Join(kept, "\n")
	if strings.TrimSpace(out) == "" {
		t.Fatalf("%s stripped to nothing — the reader is broken, not the call site", path)
	}
	return out
}

// TestDoctorDeep_PassesRealRerankCheck pins the doctor's --deep supply
// site: precheck.RunAll must receive rerank.CheckProvider, not nil.
func TestDoctorDeep_PassesRealRerankCheck(t *testing.T) {
	src := readSource(t, "doctor_deep.go")

	// `.*` rather than `[^)]*`: an argument list can itself contain
	// parentheses (context.Background()), and a character class excluding
	// ')' silently stops at the first inner one. Go's `.` excludes newline,
	// so the match still cannot span lines.
	call := regexp.MustCompile(`precheck\.RunAll\(.*,\s*rerank\.CheckProvider\)`)
	if !call.MatchString(src) {
		t.Errorf("doctor_deep.go must call precheck.RunAll(..., rerank.CheckProvider); the rerank axis silently stops running otherwise")
	}
	if regexp.MustCompile(`precheck\.RunAll\(.*,\s*nil\)`).MatchString(src) {
		t.Errorf("doctor_deep.go passes nil as the rerank check — that is refused at runtime and must not be written")
	}
	// The import must be real, or the symbol above could not resolve.
	if !strings.Contains(src, `"github.com/fulminate-io/knowledge-mcp/internal/rerank"`) {
		t.Errorf("doctor_deep.go does not import rerank")
	}

	// KNOWN-NEGATIVE for the matcher: it must NOT match a nil argument.
	// Without this a broken regex would pass every assertion above.
	if call.MatchString("precheck.RunAll(ctx, cfg, consumers, nil)") {
		t.Fatal("the call-shape matcher accepts a nil rerank check — it cannot detect the regression it exists for")
	}
	if !call.MatchString("precheck.RunAll(ctx, cfg, consumers, rerank.CheckProvider)") {
		t.Fatal("the call-shape matcher rejects the correct call — it would red on healthy code")
	}
}

// TestDaemon_PassesRealRerankCheckToRunPrecheck pins the OTHER supply
// site. The async precheck runs through llmproviders.RunPrecheck, which
// FORWARDS the check to precheck.RunAll — llmproviders does not name
// rerank.CheckProvider itself (importing rerank there would pull the whole
// graph client in behind it, through engine -> graphclient), so the value
// originates here at the composition root.
func TestDaemon_PassesRealRerankCheckToRunPrecheck(t *testing.T) {
	src := readSource(t, "daemon.go")

	// `.*` for the same reason as the sibling test: this call's argument
	// list contains context.Background(), whose ')' truncates a `[^)]*`
	// class — the exact defect this test's own known-positive caught.
	call := regexp.MustCompile(`llmproviders\.RunPrecheck\(.*,\s*rerank\.CheckProvider\)`)
	if !call.MatchString(src) {
		t.Errorf("daemon.go must call llmproviders.RunPrecheck(..., rerank.CheckProvider); the rerank axis silently stops running otherwise")
	}
	if regexp.MustCompile(`llmproviders\.RunPrecheck\(.*,\s*nil\)`).MatchString(src) {
		t.Errorf("daemon.go passes nil as the rerank check — that is refused at runtime and must not be written")
	}
	if !strings.Contains(src, `"github.com/fulminate-io/knowledge-mcp/internal/rerank"`) {
		t.Errorf("daemon.go does not import rerank")
	}

	// KNOWN-NEGATIVE for the matcher, as above.
	if call.MatchString("llmproviders.RunPrecheck(context.Background(), f.SkipLLMPrecheck, nil)") {
		t.Fatal("the call-shape matcher accepts a nil rerank check — it cannot detect the regression it exists for")
	}
	if !call.MatchString("llmproviders.RunPrecheck(context.Background(), f.SkipLLMPrecheck, rerank.CheckProvider)") {
		t.Fatal("the call-shape matcher rejects the correct call — it would red on healthy code")
	}
}
