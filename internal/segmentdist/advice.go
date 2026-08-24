// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// readAdvice is the PLATFORM-NEUTRAL read-ahead hint a segment cache applies to
// every mapping it opens.
//
// IT IS AN ENUM RATHER THAN A unix CONSTANT because the two platform arms
// achieve this at different points in a mapping's lifecycle and by different
// mechanisms: the unix arm calls madvise AFTER mmap, while the windows arm
// passes FILE_FLAG_RANDOM_ACCESS to CreateFile BEFORE the mapping exists. There
// is no single constant both can take. Threading unix.MADV_RANDOM out of a
// neutral file would both fail to compile off unix and put a unix symbol in
// code that must stay portable, so each tagged mapBlobFile TRANSLATES this value
// itself.
type readAdvice int

const (
	// adviceRandom suppresses read-ahead. BM25's measured default: on darwin it
	// cut the bytes faulted in by a six-query cold run from 630 MB of 727 MB to
	// 287 MB, because a BM25 query reads whole posting lists across a large
	// corpus and most of what read-ahead fetches is never touched.
	adviceRandom readAdvice = iota
	// adviceNormal leaves the platform's default read-ahead in place. HNSW's
	// MEASURED choice, and it is measured for HNSW specifically rather than
	// inherited: at HNSW's segment sizes a single query touches 99.69% of the
	// segment's pages, because the v3 layout stripes each node across four
	// sections spanning the whole file while a mean segment is only ~14 pages.
	// With every page wanted, read-ahead has nothing to over-fetch — the
	// measured over-fetch factor was exactly 1.000x under BOTH advices — so
	// suppressing it forfeits prefetch and buys nothing. Suppression measured
	// 42.7% slower on the touch phase.
	adviceNormal
)

// adviceForFormat resolves the read-ahead advice for a segment format.
//
// IT ERRORS ON AN UNKNOWN FORMAT RATHER THAN DEFAULTING. The generic-format
// construction sites (branch seed, snapshot read) do not know their format at
// compile time, and a silent default there would let a NEW format inherit
// whichever advice happened to be first in this switch — an unmeasured choice
// arriving unannounced, which is exactly how HNSW ended up on BM25's number in
// the first place. Adding a format means choosing its advice here.
func adviceForFormat(format string) (readAdvice, error) {
	switch format {
	case hnsw.New().Name():
		return adviceNormal, nil
	case bm25.New().Name():
		return adviceRandom, nil
	default:
		return 0, fmt.Errorf(
			"segmentdist: no read-ahead advice is defined for segment format %q; choose one in adviceForFormat rather than inheriting another format's measurement",
			format)
	}
}

// hnswReadAdvice and bm25ReadAdvice are the two BUILT-IN formats' advice,
// resolved from the table above exactly once.
//
// THEY EXIST SO THE CONSTRUCTION SITES CARRY NO LITERAL. The generic-format
// callers (snapshot read, branch seed) already call adviceForFormat and
// propagate its error, but the per-format factories — managerFor and
// bm25ManagerFor — know their format at compile time and return no error, so
// before these they each passed a bare adviceNormal / adviceRandom. That is a
// SECOND place deciding a format's advice, and a duplicate is silent when it
// drifts: editing an arm of the table would leave the factory on the old value
// with nothing failing. Reading through these vars means the table is the only
// decider for every path in this package.
var (
	hnswReadAdvice = mustAdviceForFormat(hnsw.New().Name())
	bm25ReadAdvice = mustAdviceForFormat(bm25.New().Name())
)

// mustAdviceForFormat resolves a built-in format's advice at package
// initialisation, panicking if the table has no arm for it.
//
// THE PANIC IS AN ASSERTION, NOT A FALLBACK. Its only callers pass the two
// formats adviceForFormat is defined over, so an error here means the table lost
// an arm for a format this package constructs — a programming error, not a
// runtime condition. It refuses at startup rather than degrading: there is no
// default value it could substitute, because substituting one is precisely the
// unmeasured-inheritance bug adviceForFormat was written to prevent.
func mustAdviceForFormat(format string) readAdvice {
	advice, err := adviceForFormat(format)
	if err != nil {
		panic(err)
	}
	return advice
}
