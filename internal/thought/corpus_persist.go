// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/filecrypt"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// corpus_persist.go is the DURABLE form of the resident thought-corpus cache: one
// framed, checksummed record holding the merged node payloads plus the per-layer
// keyset cursors, so a restart resumes the delta drain from where the last process
// left off instead of re-draining the whole corpus.
//
// This file is the codec and the path only. It reads nothing and writes nothing on
// its own — the load/save calls, and the adopt-then-validate rule that decides when
// a loaded record may be SERVED, live in loop_corpus.go.

const (
	// corpusCacheDirName is the record's own subtree name under the client data
	// root. It is a SIBLING of the segment cache, never a child of it: DropGraphCache
	// enumerates every directory under the segment cache root as a storage FORMAT and
	// RemoveAll's <root>/<format>/<graph-type>/<name>, so a record parked inside that
	// tree would be read as a format named "thought" and swept by any graph drop.
	corpusCacheDirName = "thought"
	// corpusCacheFile is the record filename.
	corpusCacheFile = "corpus.bin"
	// corpusCacheMagic is the 8-byte frame magic.
	corpusCacheMagic = "KNOWCORP"
	// corpusCacheFormatVersion is the frame version. A record written under a
	// different version is REJECTED rather than migrated — the record is a cache,
	// so the correct disposition for an unreadable one is a cold drain.
	corpusCacheFormatVersion uint32 = 1
)

// CorpusCachePathFor returns the corpus record path under a client data root.
//
// The graph type and name are BOUND HERE rather than threaded through as
// parameters, matching the convention this loop's seams already state: the
// propagation loop reflects over the single (knowledge, "default") graph, and the
// pair is spelled identically to the one drainCorpusDelta sends on the wire.
//
// EXPORTED for one reason: the non-interaction test that proves a graph-cache drop
// leaves this record alone lives in another package and must plant its fixture at
// the EXACT production path. A test that recomputed the layout literal itself would
// keep passing if the layout ever changed — it would plant a file where nothing
// lives and then "prove" that nothing deleted it.
func CorpusCachePathFor(root string) string {
	return filepath.Join(root, corpusCacheDirName, string(kgtypes.GraphKnowledge), "default", corpusCacheFile)
}

// Frame layout, all integers big-endian:
//
//	magic(8)  version(u32)  typesLen(u32) typesBytes  payloadLen(u32)  sha256(32)  payload
//
// payload is a marshaled CorpusDeltaResponse carrying the merged items and the
// per-layer next_cursors. That message is the payload because it ALREADY holds
// exactly the two things the record must persist, so the record needs no new
// message — and because a decoded record can then be fed to corpusCache.MergeDelta
// verbatim, which is what keeps the tombstone/resurrect/cursor-advance semantics
// from ever drifting away from the drain path.
const (
	corpusFrameMagicLen  = 8
	corpusFrameChecksum  = sha256.Size
	corpusFrameFixedHead = corpusFrameMagicLen + 4 + 4 // magic + version + typesLen
)

// encodeCorpusRecord frames the node set and cursors into one record.
//
// nodeTypes is carried IN THE FRAME because the cursors are only meaningful for the
// node-type set they were advanced over: a record written under an older set holds
// cursors that have already advanced past rows of a type added later, which would be
// invisible forever. Reconcile would still catch that (the probe's live count over
// the wider type set exceeds the cached live count, forcing a reset and a full
// re-drain), so this field is defense in depth — it turns a caught-late case into a
// caught-at-load case with an honest error string, not a substitute for the backstop.
func encodeCorpusRecord(nodeTypes []string, items []*knowledgev1.Node, cursors []*knowledgev1.LayerCursor) ([]byte, error) {
	payload, err := proto.Marshal(&knowledgev1.CorpusDeltaResponse{
		Items:       items,
		NextCursors: cursors,
	})
	if err != nil {
		return nil, fmt.Errorf("corpus record: marshal payload: %w", err)
	}
	typesBytes := []byte(strings.Join(nodeTypes, ","))
	sum := sha256.Sum256(payload)

	var buf bytes.Buffer
	buf.Grow(corpusFrameFixedHead + len(typesBytes) + 4 + corpusFrameChecksum + len(payload))
	buf.WriteString(corpusCacheMagic)
	_ = binary.Write(&buf, binary.BigEndian, corpusCacheFormatVersion)
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(typesBytes)))
	buf.Write(typesBytes)
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(payload)))
	buf.Write(sum[:])
	buf.Write(payload)
	return buf.Bytes(), nil
}

// decodeCorpusRecord validates the frame and returns the payload response.
//
// Every rejection — bad magic, a version mismatch, a header shorter than the fixed
// fields, a declared length overrunning the buffer, a checksum mismatch, an
// unmarshal failure, or a node-type set that differs from wantTypes — returns a
// DISTINCT non-nil error and a nil response. Never a partial result: half a corpus
// cache is indistinguishable from a complete one to every consumer downstream, so
// the only safe answer to a damaged record is no record at all.
func decodeCorpusRecord(raw []byte, wantTypes []string) (*knowledgev1.CorpusDeltaResponse, error) {
	if len(raw) < corpusFrameFixedHead {
		return nil, fmt.Errorf("corpus record: header truncated (%d bytes, need at least %d)", len(raw), corpusFrameFixedHead)
	}
	if string(raw[:corpusFrameMagicLen]) != corpusCacheMagic {
		return nil, fmt.Errorf("corpus record: bad magic %q", raw[:corpusFrameMagicLen])
	}
	off := corpusFrameMagicLen
	version := binary.BigEndian.Uint32(raw[off:])
	off += 4
	if version != corpusCacheFormatVersion {
		return nil, fmt.Errorf("corpus record: unsupported format version %d (want %d)", version, corpusCacheFormatVersion)
	}
	typesLen := binary.BigEndian.Uint32(raw[off:])
	off += 4
	// Length arithmetic is done in uint64 so a hostile or corrupt declared length
	// can never wrap into a valid-looking slice bound.
	if uint64(off)+uint64(typesLen) > uint64(len(raw)) {
		return nil, fmt.Errorf("corpus record: node-type list truncated (declared %d bytes, %d remain)", typesLen, len(raw)-off)
	}
	gotTypes := string(raw[off : off+int(typesLen)])
	off += int(typesLen)
	if want := strings.Join(wantTypes, ","); gotTypes != want {
		return nil, fmt.Errorf("corpus record: node-type set changed (record %q, want %q)", gotTypes, want)
	}
	if off+4+corpusFrameChecksum > len(raw) {
		return nil, fmt.Errorf("corpus record: payload header truncated (%d bytes remain)", len(raw)-off)
	}
	payloadLen := binary.BigEndian.Uint32(raw[off:])
	off += 4
	wantSum := raw[off : off+corpusFrameChecksum]
	off += corpusFrameChecksum
	if uint64(off)+uint64(payloadLen) > uint64(len(raw)) {
		return nil, fmt.Errorf("corpus record: payload truncated (declared %d bytes, %d remain)", payloadLen, len(raw)-off)
	}
	payload := raw[off : off+int(payloadLen)]
	gotSum := sha256.Sum256(payload)
	if !bytes.Equal(wantSum, gotSum[:]) {
		return nil, fmt.Errorf("corpus record: checksum mismatch (payload %d bytes)", payloadLen)
	}
	var resp knowledgev1.CorpusDeltaResponse
	if err := proto.Unmarshal(payload, &resp); err != nil {
		return nil, fmt.Errorf("corpus record: decode payload: %w", err)
	}
	return &resp, nil
}

// loadCorpusRecord reads and decodes the record at path.
//
// AN ABSENT RECORD IS NOT AN ERROR, and it is not a warning either — it is the
// ordinary first-run / wiped-cache state, reported as (nil, false, nil). Its
// disposition differs from the segment merge horizon's, which reads absence as
// "pull nothing until a horizon is seeded": the corpus cache is usable only when
// COMPLETE, so an absent record's correct disposition is the full cold drain, which
// is exactly the pre-existing behavior this record exists to make the exception
// rather than the norm. What transfers from that precedent is the duty to make
// absence distinguishable from damage and to decide it deliberately.
//
// THE RECORD IS OPENED BEFORE IT IS DECODED, and that order is what keeps the
// diagnoses apart. A record written before this cache was encrypted carries the
// old frame magic, so handing it straight to decodeCorpusRecord would report a
// generic bad-magic failure; opening first names it as a legacy plaintext record
// instead. The operator needs that difference: one is a file to drop and rebuild,
// the other is damage.
func loadCorpusRecord(path string, wantTypes []string) (*knowledgev1.CorpusDeltaResponse, bool, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path is derived from the configured client data root.
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("corpus record: read %q: %w", path, err)
	}
	plain, oerr := filecrypt.Open(path, raw)
	if oerr != nil {
		if errors.Is(oerr, filecrypt.ErrLegacyPlaintext) {
			return nil, false, fmt.Errorf("corpus record: %q is a legacy plaintext record from before this cache was encrypted; it is dropped and rebuilt, never converted: %w", path, oerr)
		}
		return nil, false, fmt.Errorf("corpus record: open %q: %w", path, oerr)
	}
	resp, derr := decodeCorpusRecord(plain, wantTypes)
	if derr != nil {
		return nil, false, derr
	}
	return resp, true, nil
}

// saveCorpusRecord frames the node set and cursors, seals the frame, and writes
// the sealed bytes atomically.
//
// THE RECORD IS CIPHERTEXT ON DISK, unconditionally. No branch writes the frame
// in the clear: a seal failure returns an error and nothing reaches disk, so a
// caller cannot leave readable node content behind by ignoring one. The atomic
// writer below neither knows nor cares that the bytes it commits are ciphertext.
func saveCorpusRecord(path string, nodeTypes []string, items []*knowledgev1.Node, cursors []*knowledgev1.LayerCursor) error {
	raw, err := encodeCorpusRecord(nodeTypes, items, cursors)
	if err != nil {
		return err
	}
	sealed, serr := filecrypt.Seal(path, raw)
	if serr != nil {
		return fmt.Errorf("corpus record: seal: %w", serr)
	}
	return atomicWriteCorpusRecord(path, sealed)
}

// atomicWriteCorpusRecord writes the record through a temp file in the same
// directory, fsyncs it, renames it into place, and fsyncs the parent directory —
// so a crash mid-write leaves the PREVIOUS record intact rather than a truncated
// one. A truncated record would be rejected at load and cost one cold drain,
// which is survivable; the rename makes even that unnecessary.
//
// All four steps are present deliberately. Stopping after the rename leaves the
// directory ENTRY possibly not durable, so a crash right after a successful
// rename can lose the record even though its bytes are on disk.
func atomicWriteCorpusRecord(path string, raw []byte) (err error) {
	dir := filepath.Dir(path)
	// 0o750 matches the mode the sibling client-side cache records take: this is
	// derived local state, group-readable and world-closed.
	if mkerr := os.MkdirAll(dir, 0o750); mkerr != nil {
		return fmt.Errorf("corpus record: create dir %q: %w", dir, mkerr)
	}
	tmp, err := os.CreateTemp(dir, corpusCacheFile+".*.tmp")
	if err != nil {
		return fmt.Errorf("corpus record: create temp: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename consumes the temp file.
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, werr := tmp.Write(raw); werr != nil {
		_ = tmp.Close()
		return fmt.Errorf("corpus record: write temp: %w", werr)
	}
	if serr := tmp.Sync(); serr != nil {
		_ = tmp.Close()
		return fmt.Errorf("corpus record: fsync temp: %w", serr)
	}
	if cerr := tmp.Close(); cerr != nil {
		return fmt.Errorf("corpus record: close temp: %w", cerr)
	}
	if rerr := os.Rename(tmpName, path); rerr != nil {
		return fmt.Errorf("corpus record: commit temp into place: %w", rerr)
	}
	// Fsync the parent directory so the rename is durable across a crash.
	// Non-critical: the rename itself already succeeded, so the file is on disk;
	// the directory fsync is only needed for crash durability guarantees. Warn
	// rather than fail so a durability shortfall is visible without turning a
	// completed write into a reported failure.
	if d, openErr := os.Open(dir); openErr == nil { //nolint:gosec // dir is the parent of the configured record path.
		if syncErr := d.Sync(); syncErr != nil {
			slog.Warn("corpus record: parent directory fsync failed (rename is durable, crash recovery may lag)",
				"path", path, "error", syncErr)
		}
		_ = d.Close()
	} else {
		slog.Warn("corpus record: could not open parent directory for fsync",
			"path", path, "error", openErr)
	}
	return nil
}
