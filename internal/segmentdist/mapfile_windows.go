// SPDX-License-Identifier: Apache-2.0

//go:build windows

package segmentdist

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// mappedBlob is a read-only memory mapping of a segment blob file.
//
// This is a REAL mapping, the same shape the unix arm provides: the bytes are
// file-backed pages the OS can reclaim, not a heap copy. The
// heap-to-page-cache relocation is therefore real on this platform too.
//
// What is NOT established here is the read-ahead behaviour. Windows has no
// madvise; its analog is a hint given at OPEN time, and the measured
// cold-footprint reduction behind the unix arm's advice was taken on darwin
// with MADV_RANDOM. No number from that measurement carries over to this file.
//
// This arm was written against the pinned x/sys signatures and cross-compiled,
// but it has NOT been executed — there is no Windows machine in the loop. The
// claim that a view survives closing its handles is the POSIX behaviour this
// arm mirrors, and on Windows it is a hypothesis to confirm before being relied
// on.
type mappedBlob struct {
	data []byte
	// randomAccessHinted records that the file was opened with the
	// read-ahead-suppression hint. On this platform the hint is a file-open
	// flag rather than a per-mapping advice, so it is recorded where it is
	// actually applied.
	randomAccessHinted bool
}

// mapBlobFile maps path read-only, asking the cache manager to expect random
// access rather than sequential streaming.
func mapBlobFile(path string, advice readAdvice) (*mappedBlob, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("segmentdist: blob path %s: %w", path, err)
	}
	// FILE_FLAG_RANDOM_ACCESS is the read-ahead-suppression ANALOG of the unix
	// arm's MADV_RANDOM — a different mechanism at a different point in the
	// lifecycle, not an equivalent, and unmeasured here.
	fh, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, createFlagsFor(advice), 0)
	if err != nil {
		return nil, fmt.Errorf("segmentdist: open blob for mapping: %w", err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(fh, &info); err != nil {
		_ = windows.CloseHandle(fh)
		return nil, fmt.Errorf("segmentdist: stat blob for mapping: %w", err)
	}
	size := int64(info.FileSizeHigh)<<32 | int64(info.FileSizeLow)
	if size <= 0 {
		_ = windows.CloseHandle(fh)
		return nil, fmt.Errorf("segmentdist: blob %s is %d bytes; nothing to map", path, size)
	}
	// A zero max size maps the whole file.
	mh, err := windows.CreateFileMapping(fh, nil, windows.PAGE_READONLY, 0, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(fh)
		return nil, fmt.Errorf("segmentdist: create mapping for %s: %w", path, err)
	}
	addr, err := windows.MapViewOfFile(mh, windows.FILE_MAP_READ, 0, 0, 0)
	// Both handles go as soon as the view exists, mirroring the unix arm's
	// immediate descriptor close, so a mapped segment holds no handle either.
	_ = windows.CloseHandle(mh)
	_ = windows.CloseHandle(fh)
	if err != nil {
		return nil, fmt.Errorf("segmentdist: map view of %s: %w", path, err)
	}
	// MapViewOfFile hands back a uintptr rather than a slice — the one shape
	// difference between the arms. Everything above this file sees a plain
	// []byte on both.
	data := unsafe.Slice((*byte)(unsafe.Pointer(addr)), size) //nolint:gosec // the mapped view, bounded by the file's own size
	return &mappedBlob{data: data, randomAccessHinted: true}, nil
}

// release unmaps the blob. After it returns the data slice must not be read:
// the view is gone from the address space and touching it faults.
func (m *mappedBlob) release() error {
	if m == nil || m.data == nil {
		return nil
	}
	addr := uintptr(unsafe.Pointer(unsafe.SliceData(m.data))) //nolint:gosec // the view's base address, as MapViewOfFile returned it
	m.data = nil
	if err := windows.UnmapViewOfFile(addr); err != nil {
		return fmt.Errorf("segmentdist: unmap blob: %w", err)
	}
	return nil
}

// createFlagsFor TRANSLATES the neutral read-ahead enum into this platform's
// mechanism. FILE_FLAG_RANDOM_ACCESS is the read-ahead-suppression ANALOG of
// the unix arm's MADV_RANDOM — a different mechanism at a different point in
// the lifecycle, not an equivalent, and unmeasured here.
func createFlagsFor(advice readAdvice) uint32 {
	if advice == adviceRandom {
		return windows.FILE_FLAG_RANDOM_ACCESS
	}
	return 0
}
