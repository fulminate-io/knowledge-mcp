// SPDX-License-Identifier: Apache-2.0

//go:build unix

package segmentdist

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// mappedBlob is a read-only memory mapping of a segment blob file.
//
// Mapping a blob instead of reading it is what moves a segment's bytes out of
// the Go heap: the pages belong to the OS page cache, which is evictable under
// pressure, shared between processes, and invisible to the garbage collector.
// It is a RELOCATION of the cost, not an elimination of it — a BM25 query reads
// the whole posting list of each of its terms, so the resident set is not a
// small fraction of the corpus.
//
// The descriptor is closed as soon as the mapping exists, so a mapped segment
// costs no file descriptor. That is load-bearing rather than tidy: the corpus
// runs to hundreds of segments against a soft descriptor limit in the hundreds.
type mappedBlob struct {
	data []byte
	// randomAccessHinted records that this mapping actually carries the
	// platform's read-ahead-suppression hint. It is set at the call site that
	// applies the hint, not on the way out: a mapping that reported no error
	// because the advice was never attempted would otherwise be
	// indistinguishable from one that was advised.
	randomAccessHinted bool
}

// mapBlobFile maps path read-only and suppresses read-ahead.
//
// Read-ahead suppression is ON by default, and it is a measured trade rather
// than a precaution: on darwin it cut the bytes faulted in by a six-query cold
// run from 630 MB of 727 MB to 287 MB — a 53% smaller physical footprint — for
// roughly 18% more cold wall time. That number is a DARWIN measurement of
// MADV_RANDOM and says nothing about any other platform's analog.
//
// A failed advice is returned, never absorbed. An unadvised mapping silently
// carries about twice the physical footprint this seam exists to cut, which is
// a condition to report rather than to continue past.
func mapBlobFile(path string, advice readAdvice) (*mappedBlob, error) {
	f, err := os.Open(path) //nolint:gosec // caller-supplied cache path
	if err != nil {
		return nil, fmt.Errorf("segmentdist: open blob for mapping: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("segmentdist: stat blob for mapping: %w", err)
	}
	size := info.Size()
	if size <= 0 {
		_ = f.Close()
		return nil, fmt.Errorf("segmentdist: blob %s is %d bytes; nothing to map", path, size)
	}
	data, err := unix.Mmap(int(f.Fd()), 0, int(size), unix.PROT_READ, unix.MAP_SHARED)
	// The mapping outlives the descriptor, so the descriptor goes now. VERIFIED
	// by running it: a mapping stays valid and fully readable after its file is
	// closed, which is what makes hundreds of resident segments cost none.
	if cerr := f.Close(); cerr != nil && err == nil {
		_ = unix.Munmap(data)
		return nil, fmt.Errorf("segmentdist: close blob after mapping: %w", cerr)
	}
	if err != nil {
		return nil, fmt.Errorf("segmentdist: map blob %s: %w", path, err)
	}
	// TRANSLATE the neutral enum here, at the one place that may name a unix
	// constant. The windows arm translates the same value into a CreateFile flag.
	flag, hinted := unix.MADV_NORMAL, false
	if advice == adviceRandom {
		flag, hinted = unix.MADV_RANDOM, true
	}
	if err := unix.Madvise(data, flag); err != nil {
		_ = unix.Munmap(data)
		return nil, fmt.Errorf("segmentdist: advise madvise(%d) on %s: %w", flag, path, err)
	}
	return &mappedBlob{data: data, randomAccessHinted: hinted}, nil
}

// release unmaps the blob. After it returns the data slice must not be read:
// the pages are gone from the address space and touching them faults.
func (m *mappedBlob) release() error {
	if m == nil || m.data == nil {
		return nil
	}
	data := m.data
	m.data = nil
	if err := unix.Munmap(data); err != nil {
		return fmt.Errorf("segmentdist: unmap blob: %w", err)
	}
	return nil
}
