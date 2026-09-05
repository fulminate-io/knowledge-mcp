package searchengine

import (
	"fmt"
	"os"
)

// OnMergeFunc is the merge-completion callback an owner installs via
// Options.OnMerge. The engine invokes it (when non-nil) exactly once per
// completed background merge, AFTER the consolidated segment is published, with
// the superseded constituent ids and the consolidated blob. It runs on the
// engine's background merge goroutine holding NO engine lock; the owner must not
// call back into the engine in a way that re-enters the merge path.
type OnMergeFunc func(MergeResult)

// Options tunes the engine's coalescing and merge policy.
type Options struct {
	// MinSegmentDocs is the coalescing threshold: Add buffers documents until at
	// least this many are pending, then seals them into one segment. Engine
	// tunable, not contract-locked.
	//
	// AN OWNER INDEXING WIDE VECTORS MUST CLAMP THIS, and the bound is the
	// format's to state, not this package's. A vector format addresses its
	// sections with fixed-width offsets, so there is a largest node count one
	// segment can hold, and it shrinks as the vector widens — the hnsw format
	// exposes it as MaxSegmentDocsForWidth. The default below is sized for
	// 32-byte vectors, where it sits far under that ceiling; at wide float32
	// widths an unclamped threshold seals segments the encoder will refuse.
	//
	// It stays ONE FIELD rather than growing a parallel maximum: withDefaults
	// fills it only when unset, so a caller-supplied clamp survives untouched —
	// the same mechanism MergeDisabledCountTarget below relies on.
	MinSegmentDocs int
	// DeletesPctAllowed is the dead-document ratio above which a segment becomes
	// merge-eligible. Contract default 0.33.
	DeletesPctAllowed float64
	// SegmentCountTarget is the soft cap on segment count that also triggers a
	// background merge (too many small segments slows fan-out). Engine tunable,
	// not contract-locked.
	SegmentCountTarget int
	// OnMerge, when non-nil, is invoked once per completed background merge with
	// the superseded constituent ids and the consolidated blob (see OnMergeFunc).
	// nil (the default) is a no-op: every existing caller that constructs Options
	// without this field gets the prior behavior. withDefaults value-copies the
	// func field unchanged and never substitutes a default.
	OnMerge OnMergeFunc
	// OnCorruptSegment, when non-nil, is invoked when a segment's stored bytes
	// are found to violate an invariant its own format guarantees. The engine has
	// already CONTAINED the violation — the query that hit it lost only that
	// segment's contribution and every other segment answered normally — so this
	// hook is the owner's chance to quarantine the stored file and re-fetch it.
	//
	// Called from the SEARCH FAN-OUT GOROUTINE holding no engine lock, on the
	// same terms as OnMerge: the owner must not call back into the engine in a
	// way that re-enters the read path. It may fire repeatedly for the same id,
	// because every concurrent query touching that segment finds the same
	// violation until the owner acts, so implementations must be idempotent.
	//
	// nil (the default) is a no-op and withDefaults value-copies the func field
	// unchanged. A nil hook still CONTAINS the panic; it only means nobody is
	// told which file to quarantine.
	OnCorruptSegment func(*CorruptSegmentError)
	// OnBuildDegrade, when non-nil, is invoked when a format reports that a build
	// CONTAINED a loss — it indexed what it could and dropped the rest. The
	// segment it produced is valid and published; this hook is the owner's chance
	// to RECORD what never made it in, which nothing else can observe: a dropped
	// document is indistinguishable downstream from a document with no indexable
	// content.
	//
	// IT FIRES ONLY FOR A NON-EMPTY CENSUS. An engine that fired on every build
	// would train an owner to ignore the hook, which is the same failure a
	// "degraded (none)" suffix on every clean response would cause.
	//
	// Called on WHICHEVER GOROUTINE DROVE THE BUILD — which may be one of
	// ReplaceBucketGroup's bounded harvest workers, several at once — so an
	// implementation must be safe for concurrent use, on the same terms as
	// OnMerge and OnCorruptSegment.
	//
	// nil (the default) is a no-op and withDefaults value-copies the func field
	// unchanged. A nil hook does not suppress the drop; it only means nobody is
	// told about it.
	OnBuildDegrade func(BuildReport)
	// ScratchDir is the directory the engine creates merge scratch files in. A
	// merge writes its output to a file here, maps it, decodes over the mapping
	// and unlinks it, so the directory holds one file per in-flight merge and
	// nothing between merges.
	//
	// EMPTY MEANS os.CreateTemp's OWN DEFAULT (os.TempDir()). There is no computed
	// default here on purpose: the production value has to be on the same
	// filesystem as the L2 destination, which is a fact only the owner knows, and
	// inventing one in this package would put a filesystem-layout decision in the
	// layer furthest from the filesystem.
	ScratchDir string
	// MapBlob turns a finished merge scratch file into readable bytes plus a
	// release. The engine Decodes the returned data and hands release to the
	// entry's cleanup, so the bytes must stay valid until release is called.
	//
	// THE DEFAULT IS NOT A FALLBACK, and the distinction is load-bearing rather
	// than pedantic. A fallback is a lane an ERROR routes into. This one is not
	// reached by an error, repairs nothing, and cannot fire twice on the same
	// cause: it is simply what an engine constructed WITHOUT a mapping hook can
	// do, which is read the file with os.ReadFile and return a nil release.
	//
	// ITS PURPOSE IS THAT THE MERGE PATH HAS NO BRANCH. Create, MergeTo, map,
	// Decode, unlink — one sequence, with the hook deciding only whether the bytes
	// land in the page cache or on the heap. A two-armed path with a heap arm
	// reachable when mapping FAILS would be a fallback and is forbidden: a MapBlob
	// error fails the merge loudly.
	//
	// THE ENGINE STILL CONTAINS NO PLATFORM CODE. os.ReadFile is stdlib; the mmap
	// hook is supplied by the distribution layer, which is where mapping ownership
	// lives.
	//
	// A CONSEQUENCE WORTH STATING, because it decides where the proof of this
	// plan's headline property belongs: under the default hook a merged payload is
	// a HEAP copy. The zero-heap property is present only in production
	// configuration, so a test asserting it in THIS package would be measuring the
	// default and would be red against correct code forever. It is measured where
	// the real hook is wired.
	MapBlob func(path string) (data []byte, release func(), err error)
}

const (
	defaultMinSegmentDocs     = 1024
	defaultDeletesPctAllowed  = 0.33
	defaultSegmentCountTarget = 16
)

// MergeDisabledCountTarget and MergeDisabledDeadRatio are the Options values an
// owner passes to disarm the BACKGROUND merge triggers on an engine whose
// segment layout it manages itself.
//
// A bucket-partitioned owner bounds the segment count by the bucket function
// itself — which is the job the count trigger exists to do — and reclaims dead
// documents through its scoped per-bucket re-emit rather than through the
// background dead-ratio merge. Left armed, those triggers would consolidate
// segments across bucket boundaries and undo the partition.
//
// They disarm rather than add a new switch because withDefaults fills these
// fields only when unset, so a caller-supplied value survives: a count target of
// 1<<30 is never exceeded by a real segment set, and a dead ratio above 1.0 is
// unreachable because the ratio is dead documents over total. Neither value
// disables format.Merge — an owner that drives a merge explicitly still gets
// one, and OnMerge stays wired so its reclaim path is unaffected.
const (
	MergeDisabledCountTarget = 1 << 30
	MergeDisabledDeadRatio   = 2.0
)

// DefaultMinSegmentDocs is the exported mirror of defaultMinSegmentDocs (the
// withDefaults source of truth). It exists so the cross-package segment_rebuild
// driver (cmd/knowledge/internal/tools) chunks scanned nodes by EXACTLY the
// engine's seal threshold — a chunk size != the MinSegmentDocs threshold would
// mix docs from different chunks into one sealed segment and re-introduce
// nondeterministic segment membership. Reading this shared const keeps the
// driver's chunk size == the seal threshold by construction; it is a single
// compile-time value, not a per-Manager runtime tunable (the rebuild path always
// uses the default), so no accessor method is warranted.
const DefaultMinSegmentDocs = defaultMinSegmentDocs

// withDefaults returns a copy with unset (zero) fields filled. A caller-set
// DeletesPctAllowed is left untouched; only a zero value is replaced with 0.33.
func (o Options) withDefaults() Options {
	if o.MinSegmentDocs <= 0 {
		o.MinSegmentDocs = defaultMinSegmentDocs
	}
	if o.DeletesPctAllowed <= 0 {
		o.DeletesPctAllowed = defaultDeletesPctAllowed
	}
	if o.SegmentCountTarget <= 0 {
		o.SegmentCountTarget = defaultSegmentCountTarget
	}
	// MapBlob is filled ONLY WHEN UNSET, so a caller-supplied hook survives — the
	// same fill-when-zero convention every field above uses.
	//
	// THIS DEPARTS FROM OnMerge, DELIBERATELY, and the departure is worth naming
	// because OnMerge's own doc says withDefaults "value-copies the func field
	// unchanged and never substitutes a default". The two func fields differ in
	// what nil MEANS. A nil OnMerge is a complete behaviour — no callback — so
	// substituting one would invent an obligation the caller declined. A nil
	// MapBlob is not a behaviour at all: the merge path has to turn a file into
	// bytes somehow, and leaving it nil would make every merge nil-panic. Filling
	// it is what lets the path stay branchless.
	if o.MapBlob == nil {
		o.MapBlob = readBlobFromFile
	}
	return o
}

// readBlobFromFile is the default MapBlob: read the whole file onto the heap and
// return a nil release, since heap bytes need no freeing.
//
// See Options.MapBlob for why this is a declared capability rather than a
// fallback — nothing routes here on an error.
func readBlobFromFile(path string) ([]byte, func(), error) {
	data, err := os.ReadFile(path) //nolint:gosec // the path is the engine's own scratch file
	if err != nil {
		return nil, nil, fmt.Errorf("searchengine: reading merge scratch %s: %w", path, err)
	}
	return data, nil, nil
}
