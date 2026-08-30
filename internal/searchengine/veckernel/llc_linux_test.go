// SPDX-License-Identifier: Apache-2.0

//go:build linux

package veckernel

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// largestCacheBytes reports the biggest CPU cache the kernel describes, in
// bytes, and whether it could be determined at all.
//
// It reads sysfs rather than shelling out to lscpu: the benchmark boxes are
// minimal images and the numbers here decide how large a corpus the traverse
// allocates, so the answer must not depend on a tool being installed.
func largestCacheBytes() (int, bool) {
	entries, err := filepath.Glob("/sys/devices/system/cpu/cpu0/cache/index*/size")
	if err != nil || len(entries) == 0 {
		return 0, false
	}
	best := 0
	for _, p := range entries {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if n, ok := parseCacheSize(strings.TrimSpace(string(raw))); ok && n > best {
			best = n
		}
	}
	return best, best > 0
}

// parseCacheSize turns sysfs's "32K" / "105M" form into bytes.
func parseCacheSize(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	mult := 1
	switch s[len(s)-1] {
	case 'K', 'k':
		mult, s = 1<<10, s[:len(s)-1]
	case 'M', 'm':
		mult, s = 1<<20, s[:len(s)-1]
	case 'G', 'g':
		mult, s = 1<<30, s[:len(s)-1]
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n * mult, true
}

// cacheSource names where the number came from, for the log line that records
// how the corpus was sized.
const cacheSource = "/sys/devices/system/cpu/cpu0/cache/index*/size"
