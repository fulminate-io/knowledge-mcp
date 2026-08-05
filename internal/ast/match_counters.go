// SPDX-License-Identifier: Apache-2.0

// match_counters.go — the walk's typed skip reason and the per-cause tally it
// feeds. Split out of match_walk.go, which sits close enough to the package's
// file-size cap that it has no room for them.
//
// walkCounters is the PRODUCTION accumulator, not a reporting shim: runWorkers
// allocates one per Match call, every worker records into it concurrently, and
// Match reads it into WalkStats through applyTo. That matters for the two skip
// causes no fixture can reach — they are covered by driving this accumulator
// directly, which is only evidence because this is the same code the walk runs.
//
// FilesSkipped is DERIVED here (skippedTotal) rather than tracked as a fourth
// counter incremented beside the other three. A separate total could drift from
// its decomposition; a computed one cannot, which is what makes "files_skipped
// is the sum of its causes" a property of the code rather than a promise.

package ast

import "sync/atomic"

// skipReason names why the walk declined a file discovery handed it. The zero
// value is skipNone, so a fileResult that was not skipped carries no reason and
// no branch has to remember to set one.
type skipReason int

const (
	// skipNone means the file was read and parsed. It is not a skip.
	skipNone skipReason = iota
	// skipRead means os.ReadFile failed — the file was never seen by the parser.
	skipRead
	// skipParseError means the parser returned an error other than the
	// operation limit.
	skipParseError
	// skipParseLimit means the parser hit its wall-clock operation limit
	// (treesitter.ErrParseTimeout) and returned no tree.
	skipParseLimit
)

// walkCounters is one Match call's shared tally. Every field is an atomic
// because the worker pool records into it from NumCPU goroutines; there is
// deliberately no mutex, since each counter is an independent add.
type walkCounters struct {
	scanned           atomic.Int64
	skippedRead       atomic.Int64
	skippedParseError atomic.Int64
	skippedParseLimit atomic.Int64
	filesDegraded     atomic.Int64
	matchesDegraded   atomic.Int64
}

// recordSkip attributes one declined file to the counter for its cause.
// skipNone records nothing: a file that was not skipped must not move the
// total, which is precisely what keeps skippedTotal equal to the files the
// walk actually declined.
func (c *walkCounters) recordSkip(r skipReason) {
	switch r {
	case skipRead:
		c.skippedRead.Add(1)
	case skipParseError:
		c.skippedParseError.Add(1)
	case skipParseLimit:
		c.skippedParseLimit.Add(1)
	case skipNone:
		// Not a skip; nothing to attribute.
	}
}

// skippedTotal is the FilesSkipped figure: the exact sum of the three by-cause
// counters, computed on read.
func (c *walkCounters) skippedTotal() int64 {
	return c.skippedRead.Load() + c.skippedParseError.Load() + c.skippedParseLimit.Load()
}

// recordParsed records one file the parser returned a tree for. Such a file is
// always scanned; when its tree carried ERROR nodes it is additionally counted
// as degraded, together with every match that tree produced — those matches are
// real results read off a tree tree-sitter had to error-recover, and the caller
// is entitled to know that before trusting them.
//
// Degraded matches are counted, never filtered: the ticket's rule is report,
// do not guess.
func (c *walkCounters) recordParsed(degraded bool, matches int) {
	c.scanned.Add(1)
	if !degraded {
		return
	}
	c.filesDegraded.Add(1)
	c.matchesDegraded.Add(int64(matches))
}

// applyTo writes the tally into the caller-facing stats. It is the only place
// the counters become WalkStats numbers, so FilesSkipped is the derived sum
// everywhere it is reported.
func (c *walkCounters) applyTo(s *WalkStats) {
	s.FilesScanned = int(c.scanned.Load())
	s.FilesSkipped = int(c.skippedTotal())
	s.SkippedRead = int(c.skippedRead.Load())
	s.SkippedParseError = int(c.skippedParseError.Load())
	s.SkippedParseLimit = int(c.skippedParseLimit.Load())
	s.FilesWithParseErrors = int(c.filesDegraded.Load())
	s.MatchesFromDegradedTrees = int(c.matchesDegraded.Load())
}

// fileResult is the per-file outcome from matchFile: a typed reason when the
// file was declined, or err when the where-tree evaluation failed and must be
// surfaced upward. degraded reports whether the parse that produced results
// carried ERROR nodes.
//
// The success payload is one of two, chosen by whether the walk ran in count
// mode: matches carries the full RawMatch set on the match/replace path, while
// lightMatches carries the body-free {span, kind} results on the count path
// (a.tally != nil). Exactly one is populated; the other stays nil.
type fileResult struct {
	matches      []RawMatch
	lightMatches []lightMatch
	reason       skipReason
	degraded     bool
	err          error
	// hint is the replace-path parse hint for this file, set by matchFile only
	// when the walk ran with emitParseHint and the file produced matches. nil on
	// every other path; processFile merges it into the shared cleanHints map.
	hint *fileParseHint
}
