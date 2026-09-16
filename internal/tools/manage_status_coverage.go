// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"strings"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// renderLLMCoverage renders the per-graph durable LLM-coverage table surfaced by
// manage(status). For every builtin graph instance it issues a Stats RPC
// WITH IncludeCoverage:true (the only caller that does — every other Stats path
// stays O(1)) and tabulates total / summarized / embedded / failure counts.
//
// It enumerates EVERY BUILTIN graph type (coverageWalkTypes), which is a widening
// from the sync-eligible set it used to walk: web and pdf are embed-only and carry
// coverage worth reporting, and they are deliberately NOT sync-eligible, so a
// sync-eligible walk skipped them and built no row for a collected document at all.
// The claim that raw graphs "are already filtered out" was true only while they
// skipped LLM processing entirely. The DEFAULT knowledge graph reports an
// empty instance name, which listGraphNamesOfType drops, so the knowledge row is
// emitted EXPLICITLY via an empty-name GraphSelector{Graph:""} and the knowledge
// type is then skipped in the enumeration loop; all other types are enumerated
// via listGraphNamesOfType + graphsel.GraphSelectorFor.
//
// CoverageRow is the per-graph durable LLM-coverage fact, produced ONCE by
// collectCoverageRows and consumed by BOTH the markdown renderer
// (formatCoverageRow) and the manage(status) format:json emitter. The json tags
// are the wire contract the Daemon Status web Coverage card types against — they
// are PINNED snake_case, and the graph-label field's tag is `graph` (NOT `label`)
// even though the assembling variable is named label. has_segments is REQUIRED so
// a non-segment graph renders '—' on the web rather than a misleading
// 'shipped 0 · live 0'; the live-0 WARN signal is derived client-side (web) from
// live_resident vs seg_covered, only when has_segments is true.
//
// live_resident IS THE DISTINCT LIVE-SEARCHABLE COUNT, not the summed per-segment
// count it used to be. The old number was inflated 2-3x on measured graphs by
// documents resident in more than one segment, so any threshold tuned against it
// will read differently now. Three consequences for a consumer deriving a signal
// from it:
//
//   - The reporter is DELIBERATELY NO-LOAD: it must not put a cold-cache
//     List+Fetch on the serial assembly loop. A graph whose engine has not loaded
//     yet therefore reads live 0 legitimately. This is PRESERVED behavior, not
//     introduced — the previous reading was equally no-load, so boot-time live-0
//     already happens today.
//   - live can legitimately sit BELOW seg_covered without anything being wrong.
//     BOTH ARE NOW LOCAL READS OF THE SAME ENGINE, and the pair has become a
//     DUPLICATION METER rather than a coverage one: seg_covered sums the per-segment
//     doc counts WITH DUPLICATES, while live counts DISTINCT searchable membership.
//     A document resident in two segments across an un-reclaimed merge window is
//     counted twice by the first and once by the second, so seg_covered above live
//     is the duplication the pair exists to expose. It used to be a shipped-manifest
//     count read from the server; that manifest is deleted.
//   - So a live-0 WARN must be QUALIFIED — by daemon uptime, or by has_segments
//     together with a nonzero seg_covered — rather than fired on the bare zero.
//     Without that qualification the card alarms on every cold start.
type CoverageRow struct {
	Graph        string `json:"graph"`
	Total        int    `json:"total"`
	Summarized   int    `json:"summarized"`
	Embedded     int    `json:"embedded"`
	SegCovered   int    `json:"seg_covered"`
	LiveResident int    `json:"live_resident"`
	HasSegments  bool   `json:"has_segments"`
	SummaryFail  int    `json:"summary_fail"`
	EmbedFail    int    `json:"embed_fail"`
	// SegDisposition is the coverage band this row sits in — see
	// segCoverageDisposition. Additive: no pre-existing tag is renamed.
	SegDisposition string `json:"seg_disposition"`
	// ResidentSegments is how many sealed SEGMENTS each of this graph's engines
	// holds, keyed by format name (bm25v2, hnswv3). It is the ELEVENTH pinned wire
	// key, and widening that shape was a decision taken deliberately rather than a
	// field that drifted in.
	//
	// IT IS A FOURTH INDEPENDENT MEASUREMENT, not a refinement of the three above.
	// Total, SegCovered and LiveResident are all DOCUMENT counts; this is a SEGMENT
	// count, it bounds none of them and none of them bounds it. What it answers is
	// the one question the others cannot: a search fans out one goroutine per
	// resident segment, so this number IS interactive query latency while a graph is
	// being indexed, and it is the only observable outside the process that says
	// whether the resident-growth bound is holding.
	//
	// PER FORMAT, BECAUSE THE TWO ENGINES SEAL AND PUBLISH INDEPENDENTLY and the
	// budget is per format per graph. A format with no constructed engine is ABSENT
	// from the map rather than present as zero — see
	// segmentdist.Manager.ResidentSegmentCounts for why a fabricated zero would state
	// a measurement nobody took. The map is nil for a row whose pool was never
	// probed, which renders as no count at all.
	ResidentSegments map[string]int `json:"resident_segments"`
	// ResidentSegmentsPeak is the HIGH-WATER of that same count, per format: the
	// twelfth pinned wire key.
	//
	// IT IS NOT DERIVABLE FROM THE FIELD ABOVE, which is why it ships beside it. The
	// resident-growth bound acts INSIDE the write call, so the current count is
	// always the count after it acted and can never show the excursion a batch's own
	// seals made. An operator watching only the current number would see a bound
	// holding perfectly while a lease briefly fanned a search out over far more
	// segments; this is that excursion, sampled where it happens. Same per-format
	// keys and the same omission rule as ResidentSegments — a format with no
	// constructed engine is absent rather than zero.
	ResidentSegmentsPeak map[string]int `json:"resident_segments_peak"`
	// QuarantinedSegments is how many segments each of this graph's engines has
	// WITHDRAWN FROM SERVICE for corruption, keyed by format name. It is the
	// THIRTEENTH pinned wire key, and QuarantinedImpact below is the fourteenth;
	// widening the shape by two was taken deliberately rather than drifted into.
	//
	// IT IS A LOSS, NOT A CAPACITY READING, which is what earns it a key of its own
	// beside the three counts above. Those say how much this graph HAS; this says how
	// much of it this client cannot serve. A quarantined segment's file is moved aside
	// and its index entry dropped, nothing re-fetches or re-indexes it, and the
	// documents it held are unsearchable until an operator runs manage
	// rebuild_segments — so a non-zero value here is a graph that is silently short,
	// and before this key the only record of it was one log line at the moment it
	// happened.
	//
	// SAME PER-FORMAT KEYS AND THE SAME OMISSION RULE AS ITS NEIGHBORS: a format with
	// no constructed engine is ABSENT rather than present as zero, and the map is nil
	// for a row whose pool was never probed. A constructed engine that has lost nothing
	// reports zero, which is a measurement and reads as one.
	QuarantinedSegments map[string]int `json:"quarantined_segments"`
	// QuarantinedImpact states, in one sentence, what the counts above cost — empty
	// when nothing is quarantined.
	//
	// IT IS A WIRE KEY RATHER THAN A RENDERER'S SENTENCE because the JSON arm has
	// readers of its own (the Daemon Status card, the release smoke) and a count
	// without its consequence is the thing this row exists to stop: "bm25v2 2" reads
	// as a statistic, and the fact it names is that some documents cannot be found at
	// all until a rebuild runs. It is DERIVED FROM THIS ROW'S OWN COUNTS by ONE
	// function (quarantineImpactFor, which sums them into the sentence), so every
	// consumer meets one wording rather than re-deriving its own; it is the empty
	// string when nothing is withdrawn.
	QuarantinedImpact string `json:"quarantined_impact"`
	// ResidentSegmentDestinations is how many DESTINATIONS contributed to each
	// format's two readings above — the {storage,account} children a daemon holds
	// engines for, plus its root, counted per format.
	//
	// IT CARRIES json:"-" DELIBERATELY: the twelve-key wire shape above is PINNED,
	// and this is a QUALIFIER on those two values rather than a thirteenth value of
	// its own. Widening a pinned shape is a separate decision from telling a reader
	// of the table how many engines a number was folded from.
	//
	// WHY IT EXISTS AT ALL. Each of the two readings above is a MAXIMUM across the
	// destinations this client holds an engine for, because the fan-out budget is
	// per engine; a maximum of two is not the same fact as a reading of one, and an
	// operator sizing a pool against it needs to know which they have. The text
	// render names it only above one, so the single-destination cell — every
	// single-account daemon's — is byte-identical to what it was before.
	ResidentSegmentDestinations map[string]int `json:"-"`
	// RepairVerified reports whether the coverage BACKSTOP has verified this row's
	// band within its interval. It carries `json:"-"` deliberately: the ten-key wire
	// shape above is pinned, and this is an input to SegDisposition rather than a
	// value of its own.
	RepairVerified bool `json:"-"`
	// RetainedErasures / NewestErasureAgeNanos are the SERVER's view of the
	// deletion backlog: how many journal rows still await a consumer, and how old
	// the newest is. Zero-and-zero means no backlog; UNKNOWN has its own signal —
	// a negative age (see erasureAgeUnknown), and then the count is not meaningful.
	// They carry json:"-" for the reason RepairVerified does: the ten-key wire
	// shape above is PINNED by its own tests, and these are rendered cells rather
	// than values of that contract. Widening a pinned shape is a separate decision
	// from adding a column to the table.
	RetainedErasures      int   `json:"-"`
	NewestErasureAgeNanos int64 `json:"-"`
	// RebuildPosAgeNanos / MergePosAgeNanos are THIS CLIENT's view: how long since
	// each of its two erase-feed consumers last advanced. They answer the question
	// the server cannot — whether a consumer has stopped arriving at all, which is
	// precisely what an arrival-driven stall alarm can never report.
	//
	// ZERO MEANS NEVER STARTED, not "just advanced". A consumer with no recorded
	// position has not run, and rendering an age measured from the epoch would say
	// the opposite of the truth.
	RebuildPosAgeNanos int64 `json:"-"`
	MergePosAgeNanos   int64 `json:"-"`
	// Degraded is THIS CLIENT'S accumulated per-class census of input its BM25
	// builds dropped for this graph — documents that entered no count in this row,
	// which is why no other field can stand in for it.
	//
	// It carries `json:"-"` for the reason its neighbors above do: the ten-key
	// wire shape the Daemon Status web card types against is PINNED, and widening
	// it is a separate decision from adding a cell to the table. Eleven existing
	// fields already carry the tag, six of them citing that pinned shape by name;
	// this is the twelfth.
	Degraded map[string]int `json:"-"`
	// StalledSinceNanos is 0 when this graph's coverage can still recover on its own,
	// and otherwise the wall-clock nanos at which it stopped being able to: the heal
	// breaker's latch. It is what the stuck band renders an age from.
	//
	// IT USED TO BE THE EARLIER OF TWO STAMPS. The second was the publish coverage
	// gate's suppression, and it is gone with the publish — so the stuck band is
	// correspondingly narrower, and a graph that would once have rendered stuck on
	// suppression alone no longer has a state to render.
	//
	// InWorkingSet reports whether this client maintains the graph at all — a graph
	// no direct interaction has admitted is serviced by no background arm, which is
	// the intended steady state rather than a fault.
	//
	// Evicted reports that this client's residency budget has dropped the graph's
	// segment pool from memory — see DispositionEvicted (manage_status_coverage_evicted.go).
	//
	// ALL THREE carry `json:"-"` for the RepairVerified reason above: they are inputs
	// to SegDisposition, and the ten-key wire shape is pinned.
	StalledSinceNanos int64 `json:"-"`
	InWorkingSet      bool  `json:"-"`
	Evicted           bool  `json:"-"`
	// SegProbed reports whether the SEGMENT-COVERAGE probe ran for this row. It is a
	// separate question from CountsRead below, and the two now genuinely differ: a
	// row outside the working set has its counts READ (one Stats RPC, which resolves
	// no resident graph on either backend) and its segment pool NOT PROBED, because
	// the probe imports the graph's L2 pool and constructs its engine in THIS
	// process. When it is false SegCovered and LiveResident are at their zero values
	// and neither is a measurement — segmentCoverageCell renders "not read" rather
	// than "shipped 0 · live 0", which is the same discrimination HasSegments exists
	// to give a non-segment graph.
	SegProbed bool `json:"-"`
	// CountsRead reports whether this row's counts were READ AT ALL. False means the
	// walk asked and could not be answered without materializing the graph — the
	// local OSS server holds no durable counts for it (see
	// manage_status_coverage_unmanaged.go). When it is false EVERY count field on
	// this row is at its zero value and NONE of them is a measurement: Total,
	// Summarized, Embedded, SegCovered, LiveResident, SummaryFail, EmbedFail,
	// RetainedErasures and both consumer ages are absent, not zero.
	//
	// IT CARRIES json:"-" LIKE ITS NEIGHBORS, which leaves the JSON consumer reading
	// those zeros with only SegDisposition == DispositionUnmanaged to tell it they
	// are not measurements. That is the same discrimination has_segments was added to
	// give the web for the "shipped 0 · live 0" case, and it is why the band is in
	// the pinned ten rather than derived: widening a pinned wire shape is a separate
	// decision from adding a column to the table.
	CountsRead bool `json:"-"`
	// NoInstance reports that this row is a REGISTERED FAMILY WITH NO COLLECTED
	// GRAPH — an operator's registration with nothing collected under it yet. Every
	// count field is at its zero value and none of them is a measurement, the same
	// contract CountsRead states, and the row's segment cell is read from the
	// registration's declared behavior rather than from a probe.
	//
	// IT CARRIES json:"-" for the RepairVerified reason its neighbors do: the
	// ten-key wire shape the Daemon Status web card types against is PINNED, and
	// widening it is a separate decision from adding a row shape to the table. A
	// JSON consumer therefore reads this row's zeros with only SegDisposition ==
	// DispositionNotCollected to tell it they are not measurements — which is the
	// same discrimination the declined row already relies on.
	NoInstance bool `json:"-"`
	// ImageBytes is the graph image's SIZE ON DISK as the catalog enumeration
	// reported it — Registry.listGraphs takes it off os.ReadDir's DirEntry.Info and
	// loads nothing to get it. It is the only durable per-graph fact a declined row
	// can render, so it is carried for exactly that use; a row whose counts WERE read
	// renders the counts and ignores this. Zero means the catalog reported no size.
	ImageBytes int64 `json:"-"`
}

// repairVerifiedFrom is the three-clause formula behind CoverageRow.RepairVerified.
//
// ALL THREE CLAUSES ARE LOAD-BEARING and the middle one is the subtle one. ok is
// false when this process never loaded that graph's record, which reads as
// unverified — the honest answer rather than a degradation. Converged says the
// backstop has nothing to do for the graph. st.Scanned says something actually
// EXAMINED it: the backstop writes a converged record on two paths, and the one that
// merely DECLINED a graph leaves Scanned false. Without that clause a graph seeded
// while converged, then grown into the gap-repairing band by new embedded nodes,
// would print "gap-repairing" for up to a whole backstop interval about a row nothing
// had looked at — during exactly the window the backstop is skipping it. The last
// clause retires a verification once it is older than the interval it was good for.
func repairVerifiedFrom(st RepairVerification, ok bool, nowNanos int64) bool {
	return ok && st.Converged && st.Scanned &&
		nowNanos-st.VerifiedAtNanos < int64(SegmentRepairBackstopInterval)
}

// renderLLMCoverage renders the per-graph durable LLM-coverage MARKDOWN table
// surfaced by the manage(status) TEXT body, delegating the per-graph facts to
// collectCoverageRows. Returns "" when there are no rows (stats seam unavailable
// or no eligible graphs) so the caller appends nothing.
func renderLLMCoverage(ctx context.Context, deps ClientDeps) string {
	rows, walkErr := collectCoverageRows(ctx, deps)
	// A WALK FAILURE IS NAMED EVEN WHEN IT LEFT NO ROWS AT ALL. The only error the
	// walk produces costs the registered custom families and no builtin row, so
	// the table below still renders; but a failure the operator is not told about
	// is the silent degrade this line exists to prevent, and it is worth a section
	// of its own on the empty-table path rather than being dropped with it.
	failure := ""
	if walkErr != nil {
		failure = "\n\n## LLM coverage — INCOMPLETE\n\n⚠ " + walkErr.Error() + "\n"
	}
	if len(rows) == 0 {
		return failure
	}

	var sb strings.Builder
	sb.WriteString(failure)
	sb.WriteString("\n\n## LLM coverage (durable, per-graph)\n")
	// summarized counts any non-empty Summary, which INCLUDES deterministic
	// auto-summaries — so it is most meaningful as a coverage signal for code
	// graphs, where Summary is populated only by the summarizer.
	sb.WriteString("_summarized = node has a non-empty Summary, which INCLUDES deterministic auto-summaries for structured nodes (decisions, findings, thoughts, etc.) — most meaningful as an LLM-coverage signal for code graphs, where Summary is populated only by the summarizer._\n\n")
	sb.WriteString("_the segment-coverage cell names INDEPENDENT counts and none of them bounds another, so they are labeled rather than joined with \"of\": `shipped N` sums the resident segments' doc counts WITH DUPLICATES (a document resident in more than one segment across an un-reclaimed merge window is counted once per segment), `live M` is the DISTINCT live-searchable count — so the pair is a duplication meter, and `shipped` above `live` is that duplication — and the embedded count has its own column. The bracketed term is that graph's coverage BAND, derived from the LIVE count, NOT the shipped one, and it names which arm owns the row: `self-healing` resolves within one reconcile interval, `gap-repairing` is the band the repair arm services, `cache-aged` is that same band on a graph the coverage backstop has not verified within its interval, `stuck` is a graph this client maintains whose heal breaker has latched — no arm is servicing it, and the age is how long that has been true IN THIS PROCESS (a restart clears it), `unmanaged` is a graph this client has never searched, collected into or written to, so no background arm maintains it at all — the intended state for a graph you are not working on rather than a fault. ITS COUNTS ARE REAL, and its SEGMENT CELL IS NOT: counting is a read the backend answers from durable state it already maintains, while probing the segment pool imports that pool into this process and constructs the graph's engine here, so the counts render and the segment cell says `not read`. When even the counts cannot be produced without materializing the graph — a local server whose image predates the durable count record — the count cells say `not read` too and the row carries only the on-disk image size the catalog already reported. Search, collect into or write to the graph and the next status reports its segment coverage as well. `evicted` is a graph whose segment pool this client's residency budget dropped from RAM to stay inside its byte ceiling — the segments are intact on local disk and the next search reloads them, so it is a memory-management state that needs NO operator action. And `not collected` is a REGISTERED FAMILY WITH NO GRAPH: an operator registered the collector and nothing has been collected under it yet, so the row names the family with no instance half, every count cell is absent rather than zero, and the segment cell is read from the registration's own declared behavior. Run a collect and the row becomes an ordinary one._\n\n")
	// The segment cell reads "shipped N · live M [band]". Shipped sums the resident
	// segments' doc_counts WITH DUPLICATES; live is the DISTINCT LIVE-SEARCHABLE doc
	// count. Neither bounds the other, so the cell labels the two rather than joining
	// them with "of" — the
	// residue band is defined as live exceeding embedded, which makes the unordered
	// case a routine state rather than an anomaly. When live reads 0 the live
	// searchable pool has collapsed even though the server-shipped corpus is intact,
	// the post-restart incident the startup/periodic reconcile heals. Non-segment
	// graphs render "—".
	//
	// The embedded count is the band arms' denominator — the same BinaryVectorCount
	// the coverage-ratio auto-heal compares against (T3-2 single definition) — and it
	// keeps its own column rather than being repeated in this cell. A degenerate pool
	// therefore surfaces as the BAND, not as a ratio read off these two numbers.
	//
	// DO NOT read a live count sitting under the shipped one as a collapse. Shipped
	// counts the manifest while live counts distinct searchable membership, so live
	// sits under shipped routinely with nothing wrong. The live-0 signal is the
	// unambiguous one and is the one to keep — qualified by the fact that this read
	// does NOT load, so a graph whose engine has not warmed yet reads 0 legitimately.
	//
	// The bracketed term is the row's coverage BAND and it derives from the LIVE
	// count, not the shipped one. It also names which arm OWNS the row, and that is
	// what the vocabulary has to keep honest — a band naming an arm that is not
	// running is worse than no band at all:
	//
	//   - "self-healing" resolves itself within one reconcile interval, and
	//     "gap-repairing" is the band the repair arm services. These two are the
	//     easily-confused pair.
	//   - "stuck" is the same shortfall on a graph whose heal arm has GIVEN UP: the
	//     heal breaker latched (only a manual rebuild_segments or a restart re-arms).
	//     It renders an age, and that age is PROCESS-SCOPED — the stamp is per-process
	//     and a restart clears it, so it measures how long the graph has been stuck in
	//     THIS daemon, not since the condition first arose.
	//   - "unmanaged" is a graph outside the working set. Nothing services it because
	//     nothing is supposed to, so it gets its own band rather than borrowing one
	//     that would report a fault. NOTHING LOADS IT EITHER, which is the other half
	//     of the same rule — but loading and counting are different reads: the row
	//     takes its counts from one Stats RPC, which both backends answer from durable
	//     state without making the graph resident, and declines the segment probe,
	//     which would import the pool and construct the engine here. So the counts are
	//     real and the segment cell reads "not read". A local server holding no durable
	//     counts for the graph reports that too, and the row falls back to the on-disk
	//     image size the catalog enumeration already returned without loading anything.
	//     See manage_status_coverage_unmanaged.go.
	sb.WriteString("| graph | total | summarized | embedded | segment coverage | summary-fail | embed-fail | erasure backlog |\n")
	sb.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, r := range rows {
		sb.WriteString(formatCoverageRow(r))
		sb.WriteString("\n")
	}
	// The boot-time balance verdicts, appended as their own section. They answer
	// "what did this pool look like at startup", which the live table above cannot —
	// see renderStartupBalance.
	sb.WriteString(renderStartupBalance(deps))
	return sb.String()
}

// Coverage-heal band constants — the SINGLE definition of the band, shared by the
// auto-heal arm (via bootstrap) and the manage(status) coverage disposition. The
// auto-heal arm does not trigger only on a ZERO-segment pool; it also heals a
// DEGENERATE-but-nonzero pool (segments present, but covering far fewer docs than
// the graph has embedded). Two thresholds keep it from flapping:
//
//   - SegmentCoverageFloor: the absolute embedded-count MAGNITUDE below which the
//     ratio probe is NEVER consulted — a small graph (e.g. a handful of embedded
//     nodes) can legitimately sit in one small segment, so the ratio is noisy
//     there. Below the floor only the zero-segments probe heals (never the ratio),
//     so a tiny healthy graph never churns.
//   - CoverageRatioThreshold: covered/embedded BELOW this fraction marks the pool
//     degenerate (the live incident was ~6-of-60 shards covering a fraction of the
//     embedded corpus). At/above it the pool is healthy and the arm disarms.
//
// They live here, beside GraphEmbeddedCount, because they are the same "one
// definition, two consumers" fact: the manage(status) coverage disposition
// classifies against these same two numbers, so the column and the heal cannot
// disagree about which graphs self-heal.
//
// SIBLING LITERAL: segmentdist.residentBackstopFloor
// (segmentdist/manager_reconcile_arms.go) holds the SAME value, 64, and the two are
// kept in step by a criterion so a future edit to either is caught rather than
// discovered.
//
// THEY ARE NOT THE SAME THRESHOLD, and the distinction is worth the sentence.
// SegmentCoverageFloor gates on the EMBEDDED node count — the size of the corpus that
// OUGHT to be indexed — so it answers "is this graph big enough for a ratio over it
// to mean anything". residentBackstopFloor gates on a RESIDENT doc count — what an
// engine actually holds — so it answers "is this engine holding enough to re-emit
// against" (manager_rebuild_delta.go:88). Same number, different operands, and a
// future change to one does NOT automatically justify the same change to the other.
// The criterion pins the literals precisely because nothing else ties them together.
//
// They also cannot share a home: this repo forbids a hand-written shared package, and
// segmentdist must not import tools (that would invert the layering).
const (
	SegmentCoverageFloor   = 64
	CoverageRatioThreshold = 0.5
	// SegmentRepairBackstopInterval is how long a converged graph's record is trusted
	// before the backstop re-verifies it. It is DURABLE (RepairState.VerifiedAtNanos)
	// rather than per-process precisely so a restart does not re-earn a scan the
	// previous process already paid for — a per-process clock would put the whole
	// corpus back on the boot path, which is the defect this gate exists to remove.
	// It lives here for the same "one definition, two consumers" reason
	// SegmentCoverageFloor and CoverageRatioThreshold do (see the block comment
	// above): the backstop gate and the manage(status) coverage column must not
	// disagree about how long a verification is good for.
	SegmentRepairBackstopInterval = 24 * time.Hour
)

// newCoverageRow projects a per-graph GraphStats + segment-coverage triple into
// the shared CoverageRow. The embedded count is GraphStats.BinaryVectorCount —
// the SAME denominator the coverage-ratio auto-heal compares against (T3-2 single
// definition; do not fork it) and the segment-coverage cell's denominator.
//
// IT IS THE READ-COUNTS CONSTRUCTOR, and it is the only one: reaching it means a
// Stats RPC was issued and answered for this graph, so it stamps CountsRead. The
// declined rows are built by newUnmanagedCoverageRow instead
// (manage_status_coverage_unmanaged.go), which never touches a GraphStats because
// none was fetched.
func newCoverageRow(
	label string, st *knowledgev1.GraphStats, segCovered, liveResident int,
	hasSeg, repairVerified, inWorkingSet, evicted bool, stalledSinceNanos int64,
	rebuildPosAgeNanos, mergePosAgeNanos int64,
) CoverageRow {
	row := CoverageRow{
		Graph:                 label,
		RetainedErasures:      int(st.GetRetainedErasureCount()),
		NewestErasureAgeNanos: st.GetNewestErasureAgeNanos(),
		RebuildPosAgeNanos:    rebuildPosAgeNanos,
		MergePosAgeNanos:      mergePosAgeNanos,
		Total:                 int(st.GetNonProxyNodeCount()),
		Summarized:            int(st.GetSummarizedCount()),
		Embedded:              int(st.GetBinaryVectorCount()),
		SegCovered:            segCovered,
		LiveResident:          liveResident,
		HasSegments:           hasSeg,
		// SEG-PROBED COINCIDES WITH hasSeg ON THIS CONSTRUCTOR AND ONLY ON THIS ONE.
		// Here HasSegments comes from segCoveredFor, which returns true only after the
		// probe actually ran and answered — so "this row has a pool" and "we looked" are
		// the same fact. newUnmanagedCountedCoverageRow sets HasSegments from the graph
		// TYPE with no probe at all, which is precisely why the two are separate fields.
		SegProbed:         hasSeg,
		SummaryFail:       int(st.GetSummaryFailureCount()),
		EmbedFail:         int(st.GetEmbedFailureCount()),
		RepairVerified:    repairVerified,
		InWorkingSet:      inWorkingSet,
		Evicted:           evicted,
		StalledSinceNanos: stalledSinceNanos,
		CountsRead:        true,
	}
	// Assemble first, then classify, so the disposition reads exactly the values
	// that render.
	row.SegDisposition = segCoverageDisposition(row)
	return row
}

// formatCoverageRow and segmentCoverageCell — the per-row RENDERERS — live in
// manage_status_coverage_render.go. They were moved there unchanged when this file
// reached the repo's hard 500-line cap; what stays here is the row's SHAPE (the
// pinned wire contract), the table's own preamble, and the constants the band arms
// classify against.

// coverageBandTerm lives in manage_status_coverage_evicted.go, beside the band
// vocabulary's newest member — this file is against its 500-line cap.
