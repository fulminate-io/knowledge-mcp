// SPDX-License-Identifier: Apache-2.0

package transcriptsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/syncgcs"
)

// shipObjectContentType is the content type the presign signs each GCS PUT URL with;
// it MUST match what syncgcs.PutObject sends or GCS rejects the V4 signature. A local
// const (the graph-sync syncObjectContentType in package tools is unexported and this
// engine must not import tools).
const shipObjectContentType = "application/octet-stream"

// shipBatch ships one assembled batch: one presign-batch request, parallel seal+PUT
// of each session parquet, one confirm-batch request for the PUT-OK objects, then a
// per-file watermark advance for every file whose object has now confirmed. It
// registers releaseBatchBudget as its FIRST deferred statement so EVERY exit path
// (presign transport error, length mismatch, confirm transport error, success,
// panic) releases one budget slot per object AND unlinks each temp parquet UNIFORMLY
// — no leak, no double release. A transport error / length mismatch fails ONLY this
// batch's files (the consumer keeps draining); positional pairing happens only AFTER
// the length assertion.
func shipBatch(ctx context.Context, cfg Config, concurrency int, trackers []fileTracker, batch []pendingObject, budget chan struct{}) {
	defer releaseBatchBudget(budget, batch)

	presign, ok := presignBatchOf(ctx, cfg, trackers, batch)
	if !ok {
		return // presign transport error / length mismatch already failed the batch.
	}
	putOK := sealAndPutBatch(ctx, concurrency, trackers, batch, presign)
	confirmBatchOf(ctx, cfg, trackers, batch, presign, putOK)
	advanceCompletedFiles(cfg, trackers, batch)
}

// presignBatchOf issues one presign-batch request for the whole batch and returns the
// agent's per-object reply. A transport error, decode error, or length mismatch fails
// every file in the batch and returns ok=false (the length guard runs BEFORE any
// positional pairing, so a mismatched reply never indexes an object against the wrong
// presigned URL).
func presignBatchOf(ctx context.Context, cfg Config, trackers []fileTracker, batch []pendingObject) (presignBatchResponse, bool) {
	presignChunks := make([]presignBatchChunk, len(batch))
	for i, po := range batch {
		f := trackers[po.fileIdx].summary
		presignChunks[i] = presignBatchChunk{
			Source:  f.Source,
			Session: f.Session,
		}
	}
	body, err := json.Marshal(presignBatchRequest{Mode: ModeTranscript, Chunks: presignChunks})
	if err != nil {
		failBatch(trackers, batch, fmt.Errorf("transcriptsync: marshal presign-batch: %w", err))
		return presignBatchResponse{}, false
	}
	raw, err := cfg.Transport.SyncControlJSON(ctx, "presign-batch", body)
	if err != nil {
		failBatch(trackers, batch, fmt.Errorf("transcriptsync: presign-batch: %w", err))
		return presignBatchResponse{}, false
	}
	var presign presignBatchResponse
	if err := json.Unmarshal(raw, &presign); err != nil {
		failBatch(trackers, batch, fmt.Errorf("transcriptsync: decode presign-batch response: %w", err))
		return presignBatchResponse{}, false
	}
	if len(presign.Chunks) != len(batch) {
		failBatch(trackers, batch, fmt.Errorf("transcriptsync: presign-batch returned %d results for %d objects", len(presign.Chunks), len(batch)))
		return presignBatchResponse{}, false
	}
	return presign, true
}

// sealAndPutBatch seals + PUTs each session parquet in PARALLEL (I/O-bound) over a
// bounded pool and returns the per-index PUT-OK mask. Each pool goroutine reads its
// object's bytes from the TEMP FILE (resident only while in-flight, bounded by
// `concurrency` — the memory-bound guarantee), then seals+PUTs. Results are captured
// by EXCLUSIVE index — no shared tracker mutation inside the pool; failures are folded
// onto their files after the pool joins (single-threaded), so a failed object is
// excluded from confirm.
func sealAndPutBatch(ctx context.Context, concurrency int, trackers []fileTracker, batch []pendingObject, presign presignBatchResponse) []bool {
	putOK := make([]bool, len(batch))
	putErr := make([]error, len(batch))
	var wg sync.WaitGroup
	pool := make(chan struct{}, concurrency)
	for i := range batch {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pool <- struct{}{}
			defer func() { <-pool }()
			elem := presign.Chunks[i]
			session := trackers[batch[i].fileIdx].summary.Session
			data, rerr := os.ReadFile(batch[i].object.path) //nolint:gosec // path is our own os.CreateTemp file.
			if rerr != nil {
				putErr[i] = fmt.Errorf("transcriptsync: read session parquet (%s): %w", session, rerr)
				return
			}
			// The object's bytes are now RESIDENT in RAM — counted for the memory-bound
			// test and released once its seal+PUT completes (bounded by `concurrency`).
			noteResidency(1)
			defer noteResidency(-1)
			envelope, serr := syncgcs.SealEnvelope(data, elem.AgentPublicKey, elem.ObjectPath)
			if serr != nil {
				putErr[i] = fmt.Errorf("transcriptsync: seal envelope (%s): %w", session, serr)
				return
			}
			if perr := syncgcs.PutObject(ctx, elem.UploadURL, envelope, shipObjectContentType); perr != nil {
				putErr[i] = fmt.Errorf("transcriptsync: upload %s to GCS: %w", session, perr)
				return
			}
			putOK[i] = true
		}(i)
	}
	wg.Wait()
	for i := range batch {
		if !putOK[i] {
			failFile(&trackers[batch[i].fileIdx], putErr[i])
		}
	}
	return putOK
}

// maxConfirmRollupBytes bounds the cumulative rollup JSON carried in a SINGLE
// confirm-batch POST. Each confirm chunk now carries its session's usage rollup
// (confirmBatchChunk.Rollup, ~36 KB/session typical), so a full batch of large-session
// rollups could push one confirm body past the sync backend's request-size limit. When a
// batch's attached rollups would exceed this budget, confirmBatchOf SPLITS the confirm
// into multiple POSTs — a purely CLIENT-SIDE split with NO wire-contract change: each POST
// is a well-formed confirm-batch, and a file still advances only when its OWN confirm POST
// succeeds (advanceCompletedFiles keys on the file's per-element verdict). 4 MiB. A package
// var only so a test can lower it to exercise the split without a multi-MiB fixture.
//
//nolint:gochecknoglobals // test-overridable size guard; mirrors the maxClientParquetBytes idiom.
var maxConfirmRollupBytes = 4 << 20

// confirmBatchOf confirms the PUT-OK objects, each carrying its session's usage rollup,
// and folds each per-element verdict onto its file. To keep a single confirm body bounded
// even when many large-session rollups land in one batch, it partitions the PUT-OK objects
// into byte-bounded sub-batches on their cumulative rollup JSON size and confirms each
// sub-batch as its OWN POST (confirmSubBatch). Per-file advance is unchanged: every file is
// in exactly one sub-batch, so its watermark advances iff that sub-batch's confirm returns
// OK for it.
func confirmBatchOf(ctx context.Context, cfg Config, trackers []fileTracker, batch []pendingObject, presign presignBatchResponse, putOK []bool) {
	submitted := make([]int, 0, len(batch))
	confirmChunks := make([]confirmBatchChunk, 0, len(batch))
	for i := range batch {
		if !putOK[i] {
			continue
		}
		elem := presign.Chunks[i]
		f := trackers[batch[i].fileIdx].summary
		submitted = append(submitted, i)
		confirmChunks = append(confirmChunks, confirmBatchChunk{
			ObjectPath: elem.ObjectPath,
			Source:     f.Source,
			Session:    f.Session,
			// &element.rollup is stable: trackers is fixed-size, never re-sliced after Run
			// allocates it, so the pointer lives for the request's lifetime.
			Rollup: &trackers[batch[i].fileIdx].rollup,
		})
	}
	if len(submitted) == 0 {
		return
	}
	// Partition on cumulative rollup JSON size. A chunk larger than the whole budget forms
	// its own sub-batch alone (a single file's confirm can never be split — its watermark
	// advance requires its own confirm POST to succeed).
	start, acc := 0, 0
	for i := range confirmChunks {
		b, err := json.Marshal(confirmChunks[i])
		if err != nil {
			// Unreachable for these plain DTOs; if a chunk cannot be sized, let the
			// sub-batch marshal surface the error rather than mis-splitting on a bad size.
			b = nil
		}
		sz := len(b)
		if i > start && acc+sz > maxConfirmRollupBytes {
			confirmSubBatch(ctx, cfg, trackers, batch, submitted[start:i], confirmChunks[start:i])
			start, acc = i, 0
		}
		acc += sz
	}
	confirmSubBatch(ctx, cfg, trackers, batch, submitted[start:], confirmChunks[start:])
}

// confirmSubBatch issues ONE confirm-batch POST for a sub-batch of PUT-OK objects (in
// request order) and folds each per-element verdict onto its file. A whole-request
// transport error, decode error, or length mismatch fails every object in the sub-batch;
// positional pairing of results to submitted objects runs only AFTER the length guard.
// subSubmitted holds the batch indices parallel to subChunks.
func confirmSubBatch(ctx context.Context, cfg Config, trackers []fileTracker, batch []pendingObject, subSubmitted []int, subChunks []confirmBatchChunk) {
	body, err := json.Marshal(confirmBatchRequest{Mode: ModeTranscript, Chunks: subChunks})
	if err != nil {
		failSubmitted(trackers, batch, subSubmitted, fmt.Errorf("transcriptsync: marshal confirm-batch: %w", err))
		return
	}
	raw, err := cfg.Transport.SyncControlJSON(ctx, "confirm-batch", body)
	if err != nil {
		failSubmitted(trackers, batch, subSubmitted, fmt.Errorf("transcriptsync: confirm-batch: %w", err))
		return
	}
	var confirm confirmBatchResponse
	if err := json.Unmarshal(raw, &confirm); err != nil {
		failSubmitted(trackers, batch, subSubmitted, fmt.Errorf("transcriptsync: decode confirm-batch response: %w", err))
		return
	}
	if len(confirm.Results) != len(subSubmitted) {
		failSubmitted(trackers, batch, subSubmitted, fmt.Errorf("transcriptsync: confirm-batch returned %d results for %d objects", len(confirm.Results), len(subSubmitted)))
		return
	}
	for j, res := range confirm.Results {
		tr := &trackers[batch[subSubmitted[j]].fileIdx]
		if res.OK {
			tr.okCount++
			continue
		}
		failFile(tr, fmt.Errorf("transcriptsync: confirm rejected (%s): %s",
			tr.summary.Session, errOrUnknown(res.Error)))
	}
}

// releaseBatchBudget is shipBatch's FIRST deferred call: it unlinks each object's temp
// parquet (freeing the disk) and releases one in-flight budget slot per object.
// Registering it as the first defer means it runs on EVERY exit path uniformly — the
// structural guarantee that no shipped-batch exit path leaks a budget slot (which
// would block producers on acquire and hang the consumer) OR a temp file. It covers
// ONLY objects that reached a shipped batch; the pre-ship paths (prepareFile
// write-error / over-cap, produceFile ctx.Done cancellation) reap their own temps.
func releaseBatchBudget(budget chan struct{}, batch []pendingObject) {
	for i := range batch {
		_ = os.Remove(batch[i].object.path)
		<-budget
	}
}

// failBatch marks every file in the batch failed with err — a WHOLE-batch failure
// (presign transport error or length mismatch), where no object reached confirm.
func failBatch(trackers []fileTracker, batch []pendingObject, err error) {
	for i := range batch {
		failFile(&trackers[batch[i].fileIdx], err)
	}
}

// failSubmitted marks the PUT-OK objects' files failed — a confirm-batch whole-request
// failure or length mismatch, where the submitted objects have no per-element verdict.
func failSubmitted(trackers []fileTracker, batch []pendingObject, submitted []int, err error) {
	for _, i := range submitted {
		failFile(&trackers[batch[i].fileIdx], err)
	}
}

// failFile records the FIRST error for a file and marks it failed so its watermark
// never advances. Idempotent — a later failure does not overwrite the first.
func failFile(tr *fileTracker, err error) {
	if tr.failed {
		return
	}
	tr.failed = true
	tr.err = err
	if tr.summary.Err == "" {
		tr.summary.Err = err.Error()
	}
}

func errOrUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// advanceCompletedFiles advances the watermark of every DISTINCT file in the batch
// whose object has now confirmed (okCount == total, no failure). The completed flag
// makes it idempotent per file. A failed or missing object leaves the file
// unadvanced; next run re-converts the same session.
func advanceCompletedFiles(cfg Config, trackers []fileTracker, batch []pendingObject) {
	seen := map[int]bool{}
	for _, po := range batch {
		if seen[po.fileIdx] {
			continue
		}
		seen[po.fileIdx] = true
		tr := &trackers[po.fileIdx]
		if tr.failed || tr.completed || tr.total == 0 || tr.okCount != tr.total {
			continue
		}
		if err := cfg.Watermarks.Advance(tr.key, tr.advanced); err != nil {
			failFile(tr, fmt.Errorf("transcriptsync: advance watermark %s: %w", tr.key, err))
			continue
		}
		tr.completed = true
	}
}

// mergeTrackers aggregates the per-file trackers into the batch Summary and joins the
// per-file errors. A file counts toward FilesUploaded only when it completed (its
// object confirmed, or — under DryRun — its session WOULD ship) with no error.
func mergeTrackers(cfg Config, trackers []fileTracker) (Summary, error) {
	summary := Summary{DryRun: cfg.DryRun, FilesScanned: len(trackers)}
	var errs []error
	for i := range trackers {
		tr := &trackers[i]
		summary.Files = append(summary.Files, tr.summary)
		if tr.err != nil {
			errs = append(errs, tr.err)
			continue
		}
		if tr.completed && tr.summary.Rows > 0 {
			summary.FilesUploaded++
			summary.RowsShipped += tr.summary.Rows
		}
	}
	return summary, errors.Join(errs...)
}
