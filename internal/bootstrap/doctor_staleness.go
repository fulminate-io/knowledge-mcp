// SPDX-License-Identifier: Apache-2.0

// doctor_staleness.go — the `knowledge doctor` code-index staleness check.
// Reports how far the code graph for the current repo has drifted from the
// working tree's HEAD, plus when it was last collected. A stale index still
// works (search just returns slightly old symbols), so this check never
// errors: it is info (no recorded metadata / pre-staleness graph), ok (up to
// date), or warn (N commits behind — refresh suggested).

package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/coderun"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// checkCodeStaleness resolves the current repo, reads its recorded collection
// metadata off the GraphInfo catalog, and reports drift. Never returns
// statusErr — a stale or unknown index is informational, not a failure.
func checkCodeStaleness(port int) checkResult {
	cwd, err := os.Getwd()
	if err != nil {
		return staleResult(statusInfo, "cannot resolve working dir", "")
	}
	repo := filepath.Base(cwd)

	gc := graphclient.NewGraphClient(port)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if !gc.HealthyCtx(ctx) {
		return staleResult(statusInfo, "server not running — code-index staleness unknown",
			"run `knowledge start`, then `knowledge doctor` again")
	}

	syncCommit, collectedAt, ok := tools.RecordedCodeSyncMeta(ctx, gc.Execute, repo)
	if !ok {
		return staleResult(statusInfo, "code index: last collected unknown",
			fmt.Sprintf("run `collect %s` to record collection staleness", cwd))
	}

	when := "unknown"
	if !collectedAt.IsZero() {
		when = tools.RelativeAge(collectedAt)
	}
	behind, behindErr := coderun.CommitsBehind(ctx, cwd, syncCommit)
	return assembleStaleness(cwd, when, behind, behindErr, syncCommit)
}

// assembleStaleness is the pure message-assembly half of checkCodeStaleness,
// split out so the info/ok/warn branches are table-testable without a live
// server or git tree. syncCommit empty → last-collected-only; commits-behind
// error → last-collected-only with the reason; 0 behind → ok; N behind → warn.
func assembleStaleness(cwd, when string, behind int, behindErr error, syncCommit string) checkResult {
	if syncCommit == "" {
		return staleResult(statusInfo, fmt.Sprintf("code index: last collected %s", when), "")
	}
	if behindErr != nil {
		return staleResult(statusInfo,
			fmt.Sprintf("code index: last collected %s (commits-behind unavailable)", when),
			"detached HEAD, shallow clone, or unknown revision — `collect` to refresh")
	}
	if behind == 0 {
		return staleResult(statusOK, fmt.Sprintf("code index up to date (last collected %s)", when), "")
	}
	return staleResult(statusWarn,
		fmt.Sprintf("code index %s behind HEAD (last collected %s)", pluralCommits(behind), when),
		fmt.Sprintf("run `collect %s` to refresh", cwd))
}

// staleResult builds a checkResult for the staleness check (name fixed).
func staleResult(status checkStatus, msg, detail string) checkResult {
	return checkResult{name: "code-index", status: status, msg: msg, detail: detail}
}

// pluralCommits formats "<n> commit[s]".
func pluralCommits(n int) string {
	if n == 1 {
		return "1 commit"
	}
	return fmt.Sprintf("%d commits", n)
}
