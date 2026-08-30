// SPDX-License-Identifier: Apache-2.0

package calibration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/corpusscan"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// The locked literal this file's criterion greps. Emitted on EVERY path through
// BOTH tests — pass, skip and failure alike — because a literal emitted only on
// the skip branches produces a gate that is green when the test skips and red
// when it runs and passes, which fails precisely the operator doing the work.
const contractLogPrefix = "scan-analyzer-contract: "

// TestScanAnalyzerContract_KeysDeclared is ALWAYS ON: no env, no network, no
// daemon, no filesystem, no analyzer invocation.
//
// THE ANTI-DRIFT ASSERTION IS THE POINT. This package declares its own copies of
// the three finding-contract keys, and the producer declares theirs. A direct
// import would only catch ABSENCE; the second declaration plus this equality
// assertion is what catches a RESPELLING — the failure where both sides are
// internally consistent and join zero rows.
func TestScanAnalyzerContract_KeysDeclared(t *testing.T) {
	defer func() { t.Logf("%schecked 3 keys and the four-shape claim classifier", contractLogPrefix) }()

	for _, c := range []struct{ name, ours, theirs string }{
		{"file", MetaKeyFile, corpusscan.MetaKeyFile},
		{"line", MetaKeyLine, corpusscan.MetaKeyLine},
		{"check id", MetaKeyCheckID, corpusscan.MetaKeyCheckID},
	} {
		if c.ours != c.theirs {
			t.Errorf("the %s key has drifted: this package says %q, the producer says %q — the join would silently match nothing", c.name, c.ours, c.theirs)
		}
	}
	if MetaKeyFile == MetaKeyLine || MetaKeyFile == MetaKeyCheckID || MetaKeyLine == MetaKeyCheckID {
		t.Fatal("the three contract keys must be pairwise distinct")
	}

	// THE FOUR CONTRACT SHAPES, plus the two malformed ones. A consumer that
	// collapses any two of these loses information the read surface must show.
	for _, tc := range []struct {
		name string
		md   map[string]string
		want ClaimKind
	}{
		{"site claim", map[string]string{MetaKeyCheckID: "c", MetaKeyFile: "internal/x.go", MetaKeyLine: "12"}, ClaimSite},
		{"file claim, line key ABSENT", map[string]string{MetaKeyCheckID: "c", MetaKeyFile: "internal/x.go"}, ClaimFile},
		{"per-check non-site", map[string]string{MetaKeyCheckID: "c"}, ClaimNonSite},
		{"run-level notice, NO metadata at all", nil, ClaimNonSite},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := classifyClaim(foundation.Finding{Metadata: tc.md})
			if err != nil {
				t.Fatalf("classify: %v", err)
			}
			if got.kind != tc.want {
				t.Fatalf("kind = %v, want %v", got.kind, tc.want)
			}
			if tc.want == ClaimSite && (got.file != "internal/x.go" || got.line != 12 || got.checkID != "c") {
				t.Fatalf("site claim decoded wrong: %+v", got)
			}
			if tc.want == ClaimFile && (got.file != "internal/x.go" || got.line != 0) {
				t.Fatalf("a file claim must carry a path and no line: %+v", got)
			}
		})
	}

	for _, tc := range []struct {
		name string
		md   map[string]string
	}{
		{"line with no file", map[string]string{MetaKeyCheckID: "c", MetaKeyLine: "12"}},
		{"empty-string line", map[string]string{MetaKeyCheckID: "c", MetaKeyFile: "internal/x.go", MetaKeyLine: ""}},
	} {
		if _, err := classifyClaim(foundation.Finding{Metadata: tc.md}); err == nil {
			t.Fatalf("%s must error rather than classify", tc.name)
		}
	}
}

// headResolver reports a worktree's current HEAD. Injected so the guard's own
// test is hermetic — the same shape the mirror census uses for its existence
// oracle, and for the same reason: a test that shells out to git is a test that
// depends on a committer identity, a signing configuration and a writable home.
type headResolver func(dir string) (string, error)

// gitHead is the real resolver. The directory is carried as the child's working
// DIRECTORY rather than spliced into argv, so no environment value becomes a
// command argument.
func gitHead(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// requireHeadAt refuses to scan a worktree that is not at the commit being
// scored. Scoring tree X against commit Y's ground truth produces a number that
// looks exactly like a real measurement and means nothing.
func requireHeadAt(resolve headResolver, dir, want string) error {
	got, err := resolve(dir)
	if err != nil {
		return fmt.Errorf("calibration: resolve HEAD of %s: %w", filepath.Base(dir), err)
	}
	if got != want {
		return fmt.Errorf("calibration: %s is at HEAD %s but the ground truth being scored is for commit %s — scoring the wrong tree yields a number that looks real and is not", filepath.Base(dir), got, want)
	}
	return nil
}

// TestRequireHeadAt_MatchesAndRejects drives the guard in BOTH directions plus
// its error path, with no git involved.
func TestRequireHeadAt_MatchesAndRejects(t *testing.T) {
	match := func(string) (string, error) { return shaA, nil }
	if err := requireHeadAt(match, "/tmp/mirror", shaA); err != nil {
		t.Fatalf("a matching HEAD must be accepted: %v", err)
	}
	if err := requireHeadAt(match, "/tmp/mirror", shaB); err == nil {
		t.Fatal("a mismatched HEAD must be refused")
	}
	boom := func(string) (string, error) { return "", fmt.Errorf("not a repository") }
	if err := requireHeadAt(boom, "/tmp/mirror", shaA); err == nil {
		t.Fatal("an unresolvable HEAD must be refused rather than treated as a match")
	}
}

// TestScanAnalyzerContract is GATED on all three of CODEQL_CALIBRATE=1, a mirror
// worktree, and a reachable daemon.
//
// IT IS DELIBERATELY NOT GATED ON REGISTRY MEMBERSHIP. The producer
// self-registers from init(), and this file imports it non-blank, so a
// registry-only gate would fire the moment the package is imported and then fail
// for want of a daemon — scheduling a CI break in this repo and in the public
// mirror both.
//
// THE SCAN TARGET MUST BE A MIRROR WORKTREE. This gate asserts every finding's
// file classifies PathMapped, which is true only of mirror coordinates; pointing
// RepoRoot at THIS repo yields cmd/knowledge/... paths that classify
// PathMirrorOnly and reds a perfectly correct analyzer.
//
// UNVERIFIED IN CI: that the producer's REAL findings are join-compatible with
// this harness. This is the only end-to-end assertion in the package and it
// needs all three of an env gate, a reachable daemon and a mirror worktree at an
// alert commit, so it runs on no standing gate and HAS NEVER EXECUTED. Nobody
// can currently say the scanner's emitted metadata joins against the ground
// truth. What IS proven unconditionally is the SEAM: the key-declaration test
// asserts this package's three metadata constants equal the producer's exported
// ones, which catches a respelling on either side, and drives the four-shape
// claim classifier over synthetic findings.
func TestScanAnalyzerContract(t *testing.T) {
	status := "skipped before any precondition was evaluated"
	defer func() { t.Logf("%s%s", contractLogPrefix, status) }()

	if os.Getenv(envCalibrate) != "1" {
		status = "skipped: " + envCalibrate + " is not 1"
		t.Skip(status)
	}
	mirrorRoot := os.Getenv(envMirrorRoot)
	if mirrorRoot == "" {
		status = "skipped: " + envMirrorRoot + " is unset, and a mirror worktree is what makes the mirror-coordinate assertion satisfiable"
		t.Skip(status)
	}
	mirrorRoot = filepath.Clean(mirrorRoot)

	// The ground truth sits at two commits. Whichever one the operator's mirror
	// is checked out at is the one scanned; ONE commit is enough, because this
	// gate checks the SHAPE of findings, not their accuracy.
	head, err := gitHead(mirrorRoot)
	if err != nil {
		status = "skipped: could not resolve the mirror's HEAD"
		t.Skip(status)
	}
	target := ""
	for _, sha := range []string{shaA, shaB} {
		if head == sha {
			target = sha
		}
	}
	if target == "" {
		status = "skipped: the mirror is not checked out at an alert commit, so a scan of it has no ground truth to be shaped against"
		t.Skip(status)
	}
	if err := requireHeadAt(gitHead, mirrorRoot, target); err != nil {
		t.Fatalf("%s", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	gc := graphclient.NewGraphClient(graphclient.DefaultPort)
	probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Second)
	healthy := gc.HealthyCtx(probeCtx)
	probeCancel()
	if !healthy {
		status = "skipped: no reachable daemon on the default port"
		t.Skip(status)
	}

	a, ok := foundation.Get(corpusscan.AnalyzerName)
	if !ok {
		// No skip branch for this state: the import above is non-blank, so if
		// this package compiles, init() has run and the lookup resolves.
		t.Fatalf("%s is not registered even though its package is imported non-blank", corpusscan.AnalyzerName)
	}
	findings, err := a.Run(ctx, foundation.Request{
		Caller:   gc,
		Graph:    kgtypes.GraphCode,
		Name:     filepath.Base(mirrorRoot),
		RepoRoot: mirrorRoot,
		Language: "go",
	})
	if err != nil {
		// The producer returns an ERROR when every admitted check was refused,
		// so an unexecuted scan can never be scored as clean. That says nothing
		// about the metadata contract, so it is reported and skipped rather than
		// failed. A materialization fault is a different animal and fails loudly.
		if strings.Contains(err.Error(), "materializ") {
			t.Fatalf("FIXTURE MATERIALIZATION FAILED — this is an ENVIRONMENT fault (check the temp directory is writable), not a scanner defect and not a refusal: %v", err)
		}
		status = "skipped: the scan returned an error, which says nothing about the metadata contract"
		t.Skipf("%s: %v", status, err)
	}
	assertJoinCompatible(t, findings, mirrorRoot)
	status = "asserted join compatibility over " + strconv.Itoa(len(findings)) + " findings at commit " + target
}

// assertJoinCompatible is the contract the gated leg asserts, factored out so it
// is readable and so the live-run step reuses it rather than restating it.
//
// EVERY ASSERTION IS CONDITIONAL ON WHAT THE FINDING ACTUALLY CARRIES. Requiring
// all three keys everywhere would false-fail correct work, and requiring a check
// id everywhere would false-fail the run-level truncation notice, which carries
// no Metadata clause at all.
func assertJoinCompatible(t *testing.T, findings []foundation.Finding, repoRoot string) {
	t.Helper()
	sawFile := false
	for _, f := range findings {
		if f.Algorithm != corpusscan.AnalyzerName {
			t.Errorf("finding carries Algorithm %q, want %q", f.Algorithm, corpusscan.AnalyzerName)
		}
		// Keyed on the STRUCTURAL property — the run-level notice is the shape
		// with no Metadata clause — rather than on a Title prefix, because a
		// Title prefix is a literal this package has not verified.
		if len(f.Metadata) > 0 && f.Metadata[MetaKeyCheckID] == "" {
			t.Errorf("a finding carrying metadata must carry %s: %+v", MetaKeyCheckID, f.Metadata)
		}
		file, hasFile := f.Metadata[MetaKeyFile]
		line, hasLine := f.Metadata[MetaKeyLine]
		if !hasFile {
			if hasLine {
				t.Errorf("a finding with no %s must carry no %s either, got %s=%q", MetaKeyFile, MetaKeyLine, MetaKeyLine, line)
			}
			continue
		}
		sawFile = true
		if filepath.IsAbs(file) {
			t.Errorf("%s=%q is absolute; it must be repo-relative or it joins against nothing", MetaKeyFile, file)
		}
		if repoRoot != "" && strings.HasPrefix(file, repoRoot) {
			t.Errorf("%s=%q carries the worktree root as a prefix; it must be repo-relative", MetaKeyFile, file)
		}
		if _, class, err := MapMirrorPath(file); err != nil || class != PathMapped {
			t.Errorf("%s=%q does not classify as a mapped mirror path (class=%v err=%v)", MetaKeyFile, file, class, err)
		}
		if hasLine {
			n, err := strconv.Atoi(line)
			if err != nil || n <= 0 {
				t.Errorf("%s=%q must parse as a positive integer", MetaKeyLine, line)
			}
		}
	}
	if !sawFile {
		// A DATA CONDITION IS NOT A REASON TO SKIP, and this line used to be one.
		//
		// The gate exists to prove the producer's findings are join-compatible.
		// Reaching here means the operator set every gate, a scan really ran, and
		// it produced NOTHING carrying a file — so this gate verified nothing
		// while its criterion, which asserts go test's exit status, went green on
		// the skip. That is precisely the vacuous pass the whole harness was
		// built to detect, sitting inside the detector.
		//
		// The earlier reasoning was that passing here would be vacuous, and it
		// was right about passing and wrong about the remedy: a skip reads as a
		// pass to every gate above it. The honest outcome is a FAILURE naming
		// what was missing.
		t.Fatalf("%sthe scan produced %d findings and NONE carried %s, so this gate verified nothing — "+
			"either no check matched anything at this commit, or the producer stopped emitting the key the calibration join needs",
			contractLogPrefix, len(findings), MetaKeyFile)
	}
}
