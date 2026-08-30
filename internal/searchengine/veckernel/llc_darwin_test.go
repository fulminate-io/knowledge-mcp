// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package veckernel

import (
	"encoding/binary"

	"golang.org/x/sys/unix"
)

// cacheSysctls are every sysctl that might describe a cache on this platform.
//
// ALL OF THEM ARE READ AND THE LARGEST WINS, rather than taking the first that
// answers. A first-hit order got this wrong in practice: hw.l3cachesize is
// absent on Apple Silicon, and the next name tried returned hw.l2cachesize's
// 4 MiB — the EFFICIENCY cluster's L2 — while hw.perflevel0.l2cachesize sat
// there reporting the performance cluster's 16 MiB. Taking the maximum cannot
// make that mistake.
var cacheSysctls = []string{
	"hw.l3cachesize",
	"hw.perflevel0.l2cachesize",
	"hw.perflevel1.l2cachesize",
	"hw.l2cachesize",
}

// largestCacheBytes reports the biggest CPU cache the kernel describes, in
// bytes, and whether it could be determined at all.
//
// APPLE SILICON DOES NOT REPORT ITS LAST-LEVEL CACHE, and the consequence runs
// in the DANGEROUS direction, so it is stated plainly rather than glossed. These
// parts have a system-level cache that no sysctl exposes, so the largest number
// available is a cluster L2 and is SMALLER than the true last-level cache. The
// corpus multiple computed from it is therefore an OVERSTATEMENT of how far
// out-of-cache the corpus really is — a 32x against a reported 4 MiB may be far
// less against the real SLC.
//
// What protects the measurement here is corpusFloorBytes, not this number: the
// 128 MiB floor is independently larger than any Apple Silicon system-level
// cache in this class. The reported multiple is logged so a reader can see which
// of the two actually bound the corpus.
func largestCacheBytes() (int, bool) {
	best := 0
	for _, name := range cacheSysctls {
		if v, ok := sysctlSize(name); ok && v > best {
			best = v
		}
	}
	return best, best > 0
}

// sysctlSize reads one sysctl that may be encoded at either 32 or 64 bits.
//
// unix.SysctlUint64 refuses a 4-byte answer, which is how hw.perflevel0
// lookups came back as failures rather than as values. Decoding by the length
// the kernel actually returned handles both without guessing which is which.
func sysctlSize(name string) (int, bool) {
	raw, err := unix.SysctlRaw(name)
	if err != nil {
		return 0, false
	}
	switch len(raw) {
	case 8:
		return int(binary.LittleEndian.Uint64(raw)), true
	case 4:
		return int(binary.LittleEndian.Uint32(raw)), true
	default:
		return 0, false
	}
}

const cacheSource = "sysctl hw.l3cachesize / hw.perflevel{0,1}.l2cachesize / hw.l2cachesize (max)"
