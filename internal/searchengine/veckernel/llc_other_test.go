// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package veckernel

// largestCacheBytes has no implementation on this platform, and says so rather
// than guessing.
//
// The caller turns a false here into a LOUD REFUSAL to size the corpus, not into
// a default. A default would be the exact defect the LLC-scaled corpus exists to
// remove: a benchmark that silently prices a cache-resident traverse and reports
// a kernel number that is really a cache number.
func largestCacheBytes() (int, bool) { return 0, false }

const cacheSource = "no cache-size source implemented for this platform"
