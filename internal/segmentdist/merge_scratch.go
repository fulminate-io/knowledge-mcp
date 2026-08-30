// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"path/filepath"
)

// mergeScratchSubdir is the directory, under a pool's L2 cache root, that the
// engine creates merge scratch files in.
//
// IT IS A SUBDIRECTORY OF THE CACHE ROOT ON PURPOSE, and the choice has one
// requirement and one consequence. The requirement: a merge's output has to land
// on the SAME FILESYSTEM as the L2 destination it is copied into. The
// consequence, disclosed rather than discovered: diskSegmentCache.scanExisting
// skips directories, so scratch bytes are invisible to the cache's own byte
// accounting and to its eviction loop. Disk under the cache root can therefore
// exceed the operator's configured byte budget, transiently, by up to one merged
// output per in-flight merge.
const mergeScratchSubdir = "merge-scratch"

// mergeScratchDir is where a pool whose cache is rooted at cacheRoot puts its
// merge scratch files.
//
// IT DOES NOT CREATE THE DIRECTORY, and that is deliberate rather than an
// omission. The engine's merge path ensures it exists per merge and reports a
// failure as an error on the merge itself, naming the path. Creating it here
// instead would move that failure to construction time, where the constructors
// have no channel to report it — they return a manager, not an error — so it
// could only be logged and stepped past.
func mergeScratchDir(cacheRoot string) string {
	return filepath.Join(cacheRoot, mergeScratchSubdir)
}

// mapBlobHook builds the Options.MapBlob the engine uses to turn a finished merge
// scratch file into readable bytes plus a release.
//
// THE ADVICE IS A PARAMETER because it is the FORMAT'S, not the package's. The
// two read-advice constants exist because the two formats' access patterns
// differ, and handing a merged segment the other format's advice quietly changes
// its physical footprint for the life of the mapping.
//
// THE ERROR IS RETURNED, NOT ABSORBED, and there is no second arm behind it.
// mapBlobFile already argues that an unadvised mapping silently carries about
// twice the physical footprint this seam exists to cut and is a condition to
// report rather than continue past; the same holds here. A merge whose output
// cannot be mapped FAILS. Reading the file onto the heap instead would look like
// a repair, would fire forever on the same cause, and would silently forfeit the
// exact property this wiring exists to deliver.
// newMapBlobHook is the indirection the pool constructors go through to obtain
// their mapping hook. It is a VARIABLE for one reason, stated plainly because it
// is a test seam: whether a merged payload is mapping-backed is only observable
// by comparing its backing address against the addresses the mapper actually
// handed out, and those can only be recorded by wrapping the mapper itself.
// Re-mapping the file to obtain a comparison pointer does not work — each mmap
// lands at a fresh address, so a pointer from a second mapping could never equal
// the one the entry holds even when everything is correct.
//
// maxBlobBytes and bm25's v2MaxBlobBytes are vars for the same class of reason:
// a property no fixture can otherwise reach is a property nobody knows is wired.
var newMapBlobHook = mapBlobHook

func mapBlobHook(advice readAdvice) func(path string) ([]byte, func(), error) {
	return func(path string) ([]byte, func(), error) {
		m, err := mapBlobFile(path, advice)
		if err != nil {
			return nil, nil, err
		}
		return m.data, func() { _ = m.release() }, nil
	}
}
