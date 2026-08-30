// SPDX-License-Identifier: Apache-2.0

// corpus_accumulators.go — the per-grain accumulator types the corpus fold writes into,
// plus the byte-wise/instant comparison helpers that exist only to serve them.
//
// Split out of corpus.go so that file stays within the repo's 500-line commit gate while the
// analyzer grows: corpus.go keeps the fold itself (the intake predicate, the histogram math
// and the add* methods) and this file holds the shapes those methods accumulate into. Each
// type mirrors one of the platform's server-side usage-analytics read tables one-for-one —
// the mirroring notes live on each type, because they are what makes a field's semantics
// checkable against the other side.
package transcriptanalytics

import "time"

// Accumulators — each mirrors one agent read table (see corpus fields).

// toolTimeAcc mirrors a usage_fact per-tool group: trustSum = SUM(trustworthy_duration_ms),
// count = SUM(record_count) over ALL rows with that tool (trustworthy or not).
type toolTimeAcc struct {
	trustSum int64
	count    int64
}

// tokenAcc is a per-dimension input/output token sum (usage_fact SUM(input/output_tokens)).
type tokenAcc struct {
	inSum  int64
	outSum int64
}

// subagentAcc mirrors a usage_fact per-agent_id group: MIN(subagent_type), the min/max
// record instants for the floor-epoch wall span, and the token sums. activeMs is the
// idle-excluded companion to that span, assigned once per file by finalizeActive and then
// summed additively across files.
type subagentAcc struct {
	subagentType  string
	minTS, maxTS  time.Time
	activeMs      int64
	inSum, outSum int64
}

// chainAgentAcc mirrors the agentChainPG inner per-(session,agent_id) grain: MIN
// subagent_type + the min/max instants for that agent's wall span, plus the same
// idle-excluded active time subagentAcc carries.
type chainAgentAcc struct {
	subagentType string
	minTS, maxTS time.Time
	activeMs     int64
}

// residencyAcc is a per-tool result-residency group: how many calls that tool made, how
// many bytes and estimated tokens their results carried, the residency-weighted total, and
// how many of those results were spilled and therefore RECOVERED rather than observed.
type residencyAcc struct {
	calls          int64
	resultBytes    int64
	resultTokens   int64
	residentTokens int64
	spilledResults int64
}

// residencyRow is one tool call's result, held per PARTIAL corpus until the file's model
// calls are known. Residency cannot be accumulated per row: it depends on how many model
// calls FOLLOW the row, which is not known until the whole lane has been read.
type residencyRow struct {
	tool    string
	ts      time.Time
	bytes   int64
	images  int64
	spilled bool
}

// dupKey is the (session,tool,hash) duplicate-command grain.
type dupKey struct {
	session, tool, hash string
}

// dupAcc mirrors a usage_duplicate_commands (session,tool,hash) group: run count, the
// trustworthy-only wasted-duration sum, and MIN(sample_preview) byte-wise.
//
// trustCount counts the rows wastedSum was summed over, which is NOT count: count includes
// interrupted and idle-guarded rows that contribute no time. A per-call figure divided by
// count would therefore be diluted by runs that were never measured.
type dupAcc struct {
	count           int64
	trustCount      int64
	backgroundCount int64
	wastedSum       int64
	preview         string
}

// minStr returns the byte-wise-lesser of two strings (MIN semantics).
func minStr(a, b string) string {
	if b < a {
		return b
	}
	return a
}

// earlier / latest return the min / max of two record instants (INSTANT comparison, never
// string comparison, which would misorder across differing UTC offsets).
func earlier(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
