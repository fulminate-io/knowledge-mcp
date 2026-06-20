package searchengine

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
}

const (
	defaultMinSegmentDocs     = 1024
	defaultDeletesPctAllowed  = 0.33
	defaultSegmentCountTarget = 16
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
	return o
}
