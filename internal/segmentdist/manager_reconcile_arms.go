// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// residentBackstopFloor is the resident doc count below which a count is too small
// for a ratio over it to mean anything, which is why it gates rather than scales.
//
// ITS SOLE SURVIVING COMPARISON IS manager_rebuild_delta.go:88, where the delta
// re-emit refuses to run against serving engines that hold no corpus. armObservation
// does NOT compare against it — that arm's entry gate went with the shipped
// denominator, and the constant appears there only as a value on the probe's Debug
// line. Stating that precisely matters: a reader who believes an entry gate still
// short-circuits in armObservation would look for a fast path that is not there.
//
// It moved here from the deleted manager_backstop.go rather than being re-pointed at
// tools.SegmentCoverageFloor. segmentdist does not import tools and must not start:
// tools sits ABOVE this package, and naming its symbols from here inverts the
// layering. residentBackstopRatio went with the shipped denominator it scaled; no
// survivor needs it.
//
// SIBLING LITERAL, AND THIS IS THE HONEST VERSION OF A SHARED CONSTANT.
// tools.SegmentCoverageFloor (tools/manage_status_coverage.go) holds the SAME value,
// 64, and a criterion asserts the two literals agree so a future edit to either is
// caught rather than discovered.
//
// THEY ARE NOT THE SAME THRESHOLD. That one gates on the EMBEDDED node count — how
// big the corpus OUGHT to be — and this one gates on a RESIDENT doc count — what an
// engine actually holds. Same number, different operands. The criterion pins the
// literals because nothing else ties them together, NOT because a change to one
// implies a change to the other.
//
// They cannot share a home either: this repo forbids a hand-written shared package
// and neither layer may import the other.
const residentBackstopFloor = 64

// coverageArm is the GENERAL PER-FORMAT READ SEAM: the non-generic view every
// per-format consumer walks the two engines through. It exists because distManager
// is generic over [Q, S] and the two live instantiations carry DIFFERENT type
// arguments — *distManager[[]byte, struct{}] for the HNSW arm (managerFor) and
// *distManager[bm25.Query, *bm25.CorpusStats] for the BM25 arm (bm25ManagerFor) —
// so Go cannot hold both in one slice. An interface whose method set mentions
// neither Q nor S can.
//
// IT HAS TWO CONSUMERS AND NEITHER OWNS IT: the per-format degeneracy probe
// (ResidentObservationsByFormat) and the re-bucket detector (ReBucketNeeded).
// The method set below is therefore the UNION of what per-format consumers read,
// not one consumer's argument list — a type described by its first caller stops
// being extendable without a lie.
//
// THREE RECOVERY/DENOMINATOR METHODS ARE GONE FROM THIS SEAM, and their absence is
// the shape of the cloud rail's collapse. All three rested on a SHIPPED doc count read
// from a remote manifest: one read the denominator, one re-imported the corpus from
// the server when the ratio over it was breached, and one was the thin wrapper that
// paired them. With the cloud rail deleted there is no such denominator — the only
// source stamps DocCount 0 by design — so the degeneracy VERDICT moved to the layer
// that still holds a real denominator (bootstrap, which reads the graph's
// embedded-node count), and this seam reports only what segmentdist can actually
// observe: whether the pool was evicted and how much is resident.
//
// The methods that remain are EXISTING distManager methods used verbatim (load,
// loadIfResident) plus four that exist FOR THE SEAM — residentDocCount, armFormat,
// distinctResidentDocCount and residentSegmentCount — solely because the underlying
// state is a FIELD on a generic struct rather than a method.
type coverageArm interface {
	load(context.Context) error
	// loadIfResident is load's DECLINING twin, for the background arms: an evicted
	// pool is skipped rather than re-materialized. load stays declared beside it —
	// the consumer arms still reach it, and dropping it would remove the drift
	// tripwire the assertions below exist for.
	loadIfResident(context.Context) (bool, error)
	residentDocCount() int
	distinctResidentDocCount() int
	residentSegmentCount() int
	armFormat() string
}

// Compile-time satisfaction assertions for BOTH live instantiations, so a future
// signature drift on any of the methods is a build failure here rather than a
// silently dropped arm at the probe.
var (
	_ coverageArm = (*distManager[[]byte, struct{}])(nil)
	_ coverageArm = (*distManager[bm25.Query, *bm25.CorpusStats])(nil)
)

// residentDocCount exposes the engine's resident doc count through the coverageArm
// seam. engine is a generic FIELD, so the interface cannot reach it directly.
func (m *distManager[Q, S]) residentDocCount() int { return m.engine.ResidentDocCount() }

// armFormat exposes this engine's segment format name (each format supplies its own) through the
// coverageArm seam, for per-arm verdict attribution and the per-arm debug record.
// format is a plain FIELD, hence the wrapper.
func (m *distManager[Q, S]) armFormat() string { return m.format }

// ArmObservation is ONE format arm's LOCAL OBSERVATION: whether the pool was
// evicted, how many docs were resident after the cache-first load, and this arm's
// probe error. It states facts segmentdist can measure; it does NOT state a verdict.
//
// IT USED TO BE A VERDICT AND IT USED TO DECIDE. The degenerate/shipped/disarm trio
// is gone because the comparison behind it is gone: it measured the resident count
// against a SHIPPED doc count read from a remote manifest, and there is no manifest.
// The post-recovery count went with the server re-import it named. The renaming is
// not cosmetic — a type that still said "Verdict" would invite a consumer to look
// for a decision that is no longer in it.
//
// THE VERDICT NOW LIVES WITH ITS OPERAND, one layer up: bootstrap holds the graph's
// embedded-node count and applies a PER-ARM predicate to ResidentAfterLoad. The
// denominator is per-GRAPH and this numerator is per-FORMAT, so one denominator read
// serves every arm — and the formats must stay separately answerable, because
// client_segment_bm25_gate.go exists precisely to ask the BM25 arm.
//
// THE ARMS NO LONGER APPLY THE SAME PREDICATE, and naming only one of them here was
// stale. degenerateAgainstEmbedded — the ratio band — is now the BM25 arm's rule alone.
// The HNSW arm's ratio was retired: away from pipeline quiescence it asserts only a LOST
// POOL, and its exact resident-versus-vectors verdict is formed at the quiescence edge
// where the corpus is not moving underneath it. This type is unchanged by that split, and
// deliberately so: it reports facts, and which predicate consumes them is the consumer's
// business.
//
// CONSUMER WARNING, and it is the reason Evicted is a field rather than an inference:
// {Evicted:true, ResidentAfterLoad:0, Err:nil} is INDISTINGUISHABLE FROM
// MEASURED-AND-EMPTY to any consumer that does not read Evicted. Every consumer must
// branch on it, because an evicted pool was not measured at all — its zero is a
// short-circuit artifact, and treating it as a real zero would storm a from-scratch
// rebuild on every eviction.
type ArmObservation struct {
	Format            string
	ResidentAfterLoad int
	Evicted           bool
	Err               error
}

// ResidentObservationsByFormat observes EVERY format arm of one graph independently
// and returns one observation per arm. It DECIDES NOTHING: the caller pairs these
// per-format numerators with the per-graph embedded denominator it holds.
//
// THE NAME CHANGED WITH THE CONTRACT. It previously named the degeneracy reconcile
// it performed; a method that no longer decides degeneracy must not keep a name that
// says it does.
//
// PER-ARM ERROR ISOLATION. A load failure on one arm records that arm's Err and the
// probe CONTINUES to the next arm. A top-level error is returned ONLY when EVERY arm
// errored. This matters because each format's L2 cache is rooted separately
// (graphCacheDirFor), so one arm can be cold in exactly the processes where the other
// is warm: propagating a cold arm's error would destroy the other arm's observation.
func (m *Manager) ResidentObservationsByFormat(
	ctx context.Context, gt kgtypes.GraphType, name string,
) ([]ArmObservation, error) {
	arms := []coverageArm{m.managerFor(gt, name), m.bm25ManagerFor(gt, name)}

	verdicts := make([]ArmObservation, 0, len(arms))
	var firstErr error
	errored := 0
	for _, arm := range arms {
		v := armObservation(ctx, gt, name, arm)
		if v.Err != nil {
			errored++
			if firstErr == nil {
				firstErr = v.Err
			}
		}
		verdicts = append(verdicts, v)
	}
	if errored == len(arms) {
		// Nothing was measurable — surface the failure rather than reporting a
		// healthy-looking all-zero observation set.
		return verdicts, firstErr
	}
	return verdicts, nil
}

// armObservation runs ONE arm's local observation and returns it. It never returns
// an error: a failure is recorded in Err so the caller can keep evaluating the other
// arm.
//
// IT IS TWO STEPS NOW, and the three it lost all rested on the deleted shipped
// denominator: reading that denominator, calling a server re-import that reset the
// load floor and re-pulled the corpus, and taking the resident-vs-shipped ratio over
// the result. What survives is pure local measurement.
//
// THERE IS NO ENTRY FLOOR GATE LEFT IN THIS FUNCTION. It short-circuited when the
// resident count cleared residentBackstopFloor, sparing a healthy arm the shipped
// denominator read and the server re-import that followed it. Both of those are
// gone, so there is nothing left to skip: every arm now does exactly the same work —
// a cache-first load and a count — and the floor survives only as a value on the
// Debug line below, for a reader correlating this probe with the delta re-emit's
// refusal (manager_rebuild_delta.go:88), which is the one place it still decides
// anything.
func armObservation(
	ctx context.Context, gt kgtypes.GraphType, name string, arm coverageArm,
) (v ArmObservation) {
	v.Format = arm.armFormat()

	// Per-arm reconcile diagnostic (kept per keep-debug-logging): one record per arm
	// at its decision point, carrying the format so the observation is attributable.
	// The deferred emit covers every exit path, including the error and short-circuit
	// ones. Off the bind path (boot-delay one-shot + periodic loop only), so it never
	// touches first-search readiness.
	defer func() {
		slog.Debug("segmentdist: resident observation probe",
			"graph_type", gt, "name", name, "format", v.Format,
			"resident_after_load", v.ResidentAfterLoad,
			"floor", residentBackstopFloor,
			"evicted", v.Evicted,
			// ANNOUNCE THE SEMANTICS ON THE LINE ITSELF. The resident count above is
			// a sum of DISTINCT member ids per segment. It did not always mean that: a
			// segment carrying the same id more than once used to contribute one per
			// COPY, so on a corpus that accumulated duplicate layers these numbers were
			// inflated and now read roughly half what they did. THE DROP IS THE COUNT
			// BECOMING CORRECT, NOT DOCUMENTS DISAPPEARING — without this note the fall
			// is indistinguishable from the index loss this change repairs.
			"count_semantics", "resident counts are distinct member ids; a one-time drop here is the count being corrected, not data lost",
			"err", v.Err)
	}()

	// 1. Cache-first load: the L2-first primary path imports the warm L2 resident set,
	// so an arm whose lazy load would heal is not reported short.
	//
	// AN EVICTED POOL IS DECLINED HERE, and the decline is the whole protection. An
	// evicted pool reads a resident doc count of 0; letting it fall through would
	// report that zero as a MEASUREMENT, and the consumer's degeneracy test would then
	// read it as a collapsed pool and drive a from-scratch rebuild. Every eviction
	// would be undone on the next reconcile tick, at the highest possible cost.
	skipped, err := arm.loadIfResident(ctx)
	if err != nil {
		v.Err = err
		return v
	}
	if skipped {
		v.Evicted = true
		return v
	}
	v.ResidentAfterLoad = arm.residentDocCount()

	// 2. Report. There is no further step: the verdict this used to compute now
	// belongs to the caller, which holds the embedded denominator.
	return v
}
