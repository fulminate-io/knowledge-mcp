// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// withChunkRecover wraps fn with a deferred recover so a tree-sitter panic
// (CGO segfault on malformed source, internal grammar bug, etc.) is logged
// instead of crashing the whole knowledge-server process.
func withChunkRecover(site string, fn func()) func() {
	return func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("parser goroutine panic",
					"site", site,
					"err", r,
					"stack", string(debug.Stack()))
			}
		}()
		fn()
	}
}

// The four ways chunking loses a file it was handed. A rule-based DISCOVERY
// decline is not among them: those are a SCOPED walk rather than an incomplete
// one, they are consistently absent from every collect, and they are reported
// separately by DiscoveryReport.
const (
	ChunkReasonReadError   = "chunk_read_error"
	ChunkReasonParseError  = "chunk_parse_error"
	ChunkReasonWorkerPanic = "chunk_worker_panic"
	ChunkReasonCanceled    = "chunk_canceled"
)

// chunkDropReasons is the seed set, so a zero is MEASURED rather than absent —
// the same property newDiscoveryReport gives the exclusion rules.
var chunkDropReasons = []string{
	ChunkReasonReadError, ChunkReasonParseError, ChunkReasonWorkerPanic, ChunkReasonCanceled,
}

// maxChunkDropSamples bounds the NAMES reported per reason. Counts are exact.
const maxChunkDropSamples = 5

// ChunkReport attributes every file chunking was handed but did not produce a
// result for, mirroring DiscoveryReport's shape: exact per-reason counts, capped
// name samples, and a truncation flag so a short sample list is never mistaken
// for a short loss list.
//
// IT IS A COLLECT'S DIAGNOSIS, not a flag's input. A dropped file SHOULD have
// nodes and its absence is TRANSIENT, so a collect that proceeded past one would
// either under-index the repository or name the file as a deletion. The collect
// fails on it, and this report is the failure's payload.
type ChunkReport struct {
	// DroppedByReason is the exact FILE count per reason, seeded to zero.
	DroppedByReason map[string]int
	// DroppedSamples names up to maxChunkDropSamples dropped paths per reason.
	DroppedSamples map[string][]string
	// DroppedTruncated is true for a reason whose loss set outran the sample cap.
	DroppedTruncated map[string]bool
	// seen is every path already charged to a reason. It is what lets the
	// abandonment accounting below distinguish a file nothing reached from one
	// already attributed, and it is unexported because it is bookkeeping rather
	// than part of the report.
	seen map[string]struct{}
}

// newChunkReport seeds a zero count and a false truncation flag for all four
// reasons.
func newChunkReport() ChunkReport {
	rep := ChunkReport{
		DroppedByReason:  make(map[string]int, len(chunkDropReasons)),
		DroppedSamples:   make(map[string][]string, len(chunkDropReasons)),
		DroppedTruncated: make(map[string]bool, len(chunkDropReasons)),
		seen:             make(map[string]struct{}),
	}
	for _, r := range chunkDropReasons {
		rep.DroppedByReason[r] = 0
		rep.DroppedTruncated[r] = false
	}
	return rep
}

// record charges one dropped path to its reason. The count always moves; the
// name is kept only while the per-reason sample budget lasts.
//
// NOT SAFE FOR CONCURRENT USE. ChunkFilesParallel's workers call it under the
// same mutex that guards the results slice; keeping the lock outside means this
// type stays copyable, which a sync.Mutex field would forbid the moment
// PopulateResult carried one by value.
func (r *ChunkReport) record(reason, rel string) {
	r.DroppedByReason[reason]++
	r.seen[rel] = struct{}{}
	if len(r.DroppedSamples[reason]) < maxChunkDropSamples {
		r.DroppedSamples[reason] = append(r.DroppedSamples[reason], rel)
		return
	}
	r.DroppedTruncated[reason] = true
}

// recordAbandoned charges every offered file that produced neither a result nor
// an explicit drop, which is how a PANICKED or CANCELLED worker's losses are
// counted at all: withChunkRecover logs the panic and swallows it, and a worker
// that returns on cancellation abandons the rest of its queue silently, so
// nothing at the loss site can count them. Derived here as a set difference.
//
// THE COUNT IS A FILE COUNT, NOT AN EVENT COUNT. A panicking worker abandons its
// whole queue, so one lost worker holding four hundred files must read 400 rather
// than 1 — a signal reading 1 for a lost queue is worse than none, because it
// looks small, and under the fail-the-collect contract that number is the
// operator's whole diagnosis.
func (r *ChunkReport) recordAbandoned(files []string, results []*treesitter.Result, canceled bool) {
	handled := make(map[string]struct{}, len(results)+len(r.seen))
	for _, res := range results {
		handled[res.FilePath] = struct{}{}
	}
	for p := range r.seen {
		handled[p] = struct{}{}
	}
	reason := ChunkReasonWorkerPanic
	if canceled {
		reason = ChunkReasonCanceled
	}
	for _, f := range files {
		if _, ok := handled[f]; ok {
			continue
		}
		r.record(reason, f)
	}
}

// Dropped is the total number of files chunking lost, across every reason.
func (r ChunkReport) Dropped() int {
	total := 0
	for _, n := range r.DroppedByReason {
		total += n
	}
	return total
}

// Describe renders the loss for an operator: the per-reason counts that are
// non-zero, each with its capped sample of paths.
//
// IT NAMES THE FILES. A message saying only that the walk was incomplete leaves
// the operator exactly as stuck as a silent fallback did, so the samples are part
// of the contract rather than a convenience.
func (r ChunkReport) Describe() string {
	var parts []string
	for _, reason := range chunkDropReasons {
		n := r.DroppedByReason[reason]
		if n == 0 {
			continue
		}
		part := fmt.Sprintf("%s=%d %v", reason, n, r.DroppedSamples[reason])
		if r.DroppedTruncated[reason] {
			part += " (samples truncated)"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}

// ChunkFiles uses tree-sitter to chunk all discovered files, reporting every
// file it could not produce a result for.
func ChunkFiles(ctx context.Context, repoDir string, files []string) ([]*treesitter.Result, ChunkReport, error) {
	rep := newChunkReport()
	chunker := treesitter.NewChunker()
	defer chunker.Close()

	var results []*treesitter.Result
	for i, relPath := range files {
		if ctx.Err() != nil {
			// CANCELLATION ABANDONS THE WHOLE REMAINDER, and it is charged as the
			// file count it is rather than as one event.
			rep.recordAbandoned(files[i:], results, true)
			return nil, rep, ctx.Err()
		}
		absPath := filepath.Join(repoDir, relPath)
		src, err := os.ReadFile(absPath)
		if err != nil {
			slog.Warn("failed to read file", "path", relPath, "error", err)
			rep.record(ChunkReasonReadError, relPath)
			continue
		}
		result, err := chunker.ChunkFile(ctx, relPath, src)
		if err != nil {
			slog.Warn("failed to chunk file", "path", relPath, "error", err)
			rep.record(ChunkReasonParseError, relPath)
			continue
		}
		results = append(results, result)
	}
	return results, rep, nil
}

// maxChunkWorkers caps the chunker fan-out. Each worker keeps a parsed
// tree + the file's source bytes live while it works, so on a many-core
// box an uncapped NumCPU fan-out multiplies the chunker's in-flight memory
// for little throughput gain (the work is short and I/O-bound per file).
// 8 keeps the parallel speedup while bounding the footprint.
const maxChunkWorkers = 8

// ChunkFilesParallel uses tree-sitter to chunk files with up to
// maxChunkWorkers parallel workers. Each worker has its own Chunker since
// the tree-sitter parser is not thread-safe.
func ChunkFilesParallel(ctx context.Context, repoDir string, files []string) ([]*treesitter.Result, ChunkReport, error) {
	workers := min(runtime.NumCPU(), len(files), maxChunkWorkers)
	if workers <= 1 {
		// The serial path returns discovery order, not FilePath order. It is
		// sorted here as well so ChunkFilesParallel's ordering contract is a
		// property of the function rather than of the machine's core count.
		results, rep, err := ChunkFiles(ctx, repoDir, files)
		if err != nil {
			return nil, rep, err
		}
		return sortResultsByPath(results), rep, nil
	}

	fileCh := make(chan string, workers)
	go withChunkRecover("ChunkFilesParallel.feeder", func() {
		for _, f := range files {
			select {
			case fileCh <- f:
			case <-ctx.Done():
				close(fileCh)
				return
			}
		}
		close(fileCh)
	})()

	var mu sync.Mutex
	var results []*treesitter.Result
	var wg sync.WaitGroup
	// ONE MUTEX GUARDS BOTH the results slice and the report, so the report stays
	// a plain copyable struct while the workers write it concurrently.
	rep := newChunkReport()

	for range workers {
		wg.Go(withChunkRecover("ChunkFilesParallel.worker", func() {
			chunker := treesitter.NewChunker()
			defer chunker.Close()

			for relPath := range fileCh {
				if ctx.Err() != nil {
					return
				}
				absPath := filepath.Join(repoDir, relPath)
				src, err := os.ReadFile(absPath)
				if err != nil {
					slog.Warn("failed to read file", "path", relPath, "error", err)
					mu.Lock()
					rep.record(ChunkReasonReadError, relPath)
					mu.Unlock()
					continue
				}
				result, err := chunker.ChunkFile(ctx, relPath, src)
				if err != nil {
					slog.Warn("failed to chunk file", "path", relPath, "error", err)
					mu.Lock()
					rep.record(ChunkReasonParseError, relPath)
					mu.Unlock()
					continue
				}
				mu.Lock()
				results = append(results, result)
				mu.Unlock()
			}
		}))
	}

	wg.Wait()
	// After the join, whatever is neither a result nor an explicit drop was
	// abandoned by a worker that panicked or returned on cancellation. Both are
	// invisible at the loss site — withChunkRecover swallows the panic — so the
	// accounting is a set difference over the offered file list.
	rep.recordAbandoned(files, results, ctx.Err() != nil)
	if ctx.Err() != nil {
		return nil, rep, ctx.Err()
	}
	return sortResultsByPath(results), rep, nil
}

// sortResultsByPath orders chunk results by FilePath in place and returns the
// same slice.
//
// The parallel workers append under a mutex in COMPLETION order, which the OS
// scheduler decides afresh on every run. Sorting here is what makes resolution
// independent of it: the declaration index is built by walking results in
// order, so an unsorted slice would order a multi-candidate reference's emitted
// edges differently on every collect of an unchanged repo. One sort over a few
// thousand pointers at the end of a walk that already parsed every file is
// immeasurable beside the parse, and this is the only placement worker
// scheduling cannot defeat.
func sortResultsByPath(results []*treesitter.Result) []*treesitter.Result {
	sort.Slice(results, func(i, j int) bool {
		return results[i].FilePath < results[j].FilePath
	})
	return results
}

// ChunkNodeID creates a unique node ID for a chunk.
//
// THE UNNAMED BRANCH IS CONTENT-DERIVED, NOT POSITION-DERIVED, and the
// named branches are untouched. It used to render "%s:L%d-%d" from the chunk's line
// range, which made the id a function of WHERE the chunk sat: any edit ABOVE an
// unnamed chunk shifted its lines, so the collect landed a new node id and orphaned
// the old row — on every collect, forever, for every file holding unnamed chunks. A
// digest over Chunk.Content is stable under every edit that does not change the
// chunk itself, which is exactly the property the line range lacks.
//
// THE ORDINAL IS LAST, so a consumer recovers it by splitting on the final
// separator, and it is absent for the overwhelmingly common non-colliding case.
// DeduplicateChunks assigns it; see IDOrdinal for why it is a field rather than a
// suffix on Name.
//
// WHAT MAKES THIS SAFE TO LAND IS PHASE 1 OF THE SAME TICKET. Changing the id means
// every file with unnamed chunks lands new-id nodes while the server still holds
// the old-id rows, and those are precisely the orphans the file-scoped node reclaim
// removes on the SAME collect that delivers the new ids. WITHOUT THAT RECLAIM
// PRESENT THIS CHANGE MANUFACTURES THE DEFECT IT EXISTS TO FIX, at the scale of
// every affected file rather than a handful.
func ChunkNodeID(chunk treesitter.Chunk) string {
	if chunk.Name != "" {
		if chunk.ParentName != "" {
			return fmt.Sprintf("%s:%s.%s", chunk.FilePath, chunk.ParentName, chunk.Name)
		}
		return fmt.Sprintf("%s:%s", chunk.FilePath, chunk.Name)
	}
	id := fmt.Sprintf("%s:c%s", chunk.FilePath, chunkContentDigest(chunk.Content))
	if chunk.IDOrdinal > 0 {
		id += "#" + strconv.Itoa(chunk.IDOrdinal)
	}
	return id
}

// chunkContentDigestLen is how many hex characters of the content digest ride the
// id. 16 hex characters is 64 bits, which is far past collision risk WITHIN ONE
// FILE — the only scope the id has to be unique in, since the file path is already
// part of it — while keeping the id short enough to read in a log line.
const chunkContentDigestLen = 16

// chunkContentDigest renders the stable part of an unnamed chunk's id: a digest of
// the chunk's own source text.
//
// IT HASHES BYTES ALREADY IN HAND. Chunk.Content is the raw source text the walk
// just parsed, so this costs one digest per unnamed chunk and never a re-read.
func chunkContentDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])[:chunkContentDigestLen]
}

// DeduplicateChunks detects node ID collisions within each file's chunks and
// disambiguates them. Chunks with unique IDs are unchanged.
//
// TWO MECHANISMS, CHOSEN BY WHETHER THE CHUNK IS NAMED, because the two cases have
// different constraints:
//
//   - NAMED chunks keep the pre-existing behavior: append "#"+PathHash to Name
//     (e.g. the same variable name in sibling scopes).
//   - UNNAMED chunks take an ORDINAL instead, and must NOT take the Name append.
//     Appending to an empty Name would move the chunk to the NAMED branch of
//     ChunkNodeID and give its node a non-empty SymbolName, which changes what the
//     chunk IS to every symbol-addressed consumer: an unnamed span would start
//     presenting as a named declaration. Keeping Name empty is what holds this
//     disambiguation inside the unnamed branch, so it may not break that bound.
//
// THE ORDINAL STARTS AT 2 SO THE FIRST OCCURRENCE KEEPS THE BARE ID. Adding a
// duplicate of an existing chunk then leaves the existing chunk's id untouched,
// which is one fewer id to re-land. Removing an occurrence still renumbers the ones
// after it — the one case this scheme still churns, and the file-scoped node reclaim
// is what keeps that churn from accumulating as orphans.
func DeduplicateChunks(results []*treesitter.Result) {
	for _, result := range results {
		// Count occurrences of each ID.
		idCount := make(map[string]int, len(result.Chunks))
		for i := range result.Chunks {
			id := ChunkNodeID(result.Chunks[i])
			idCount[id]++
		}

		// For any ID that appears more than once, disambiguate every instance:
		// named chunks by PathHash, unnamed chunks by ordinal.
		occurrence := make(map[string]int, len(result.Chunks))
		for i := range result.Chunks {
			id := ChunkNodeID(result.Chunks[i])
			if idCount[id] <= 1 {
				continue
			}
			if result.Chunks[i].Name == "" {
				occurrence[id]++
				if n := occurrence[id]; n > 1 {
					result.Chunks[i].IDOrdinal = n
				}
				continue
			}
			if result.Chunks[i].PathHash != "" {
				result.Chunks[i].Name += "#" + result.Chunks[i].PathHash
			}
		}
	}
}

// Edge conversion is no longer a step of its own. resolveEdges consumes
// treesitter edges directly and builds the wire edge itself, because the
// reference site a treesitter.Edge carries cannot ride a wire edge — converting
// first would strip the carrier before the resolution walk ever saw it.
