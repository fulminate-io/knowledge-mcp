// SPDX-License-Identifier: Apache-2.0

package segmentdist

// store_census.go — a read-only sweep of an entire on-disk segment store,
// asking of every stored file the two questions the corruption incident raised.
//
// WHY IT EXISTS AS STORED CODE rather than as a one-off script. The incident
// produced exactly one file that a query could not read, and the population
// question — is that one file a one-off, or the visible member of a class — is
// not answerable from the one file. It is answerable by examining every other
// file the same way, and it will need answering again: after a producer change,
// after a format change, and whenever a fresh crash asks whether it is the same
// thing happening twice.
//
// WHAT IT ASKS, per stored file:
//
//   - CONTENT ADDRESS. Does the payload hash to the id in its filename? This is
//     the invariant the whole store rests on. It is the question the incident was
//     first read as failing, on a hash taken over the wrong span; it passes on
//     every preserved artifact.
//   - STRUCTURE. Does every dictionary entry and posting run resolve inside the
//     payload? This is the question the incident actually failed, and it is the
//     one that costs a full walk of every term.
//
// WHY THE STRUCTURAL HALF IS FORMAT-SPECIFIC AND SAYS SO. A format is examined
// only if it exposes a validator — bm25.ValidateSegment and hnsw.ValidateSegment
// today — and a segment in any other format is reported with StructureExamined
// false rather than as passing. A census that let an unexamined file read as
// clean would answer the population question with a number it did not measure.
//
// The two validators do NOT prove the same thing, and the difference is the
// formats' own. hnsw verifies a footer CRC at open, so bit rot there is refused
// before its walk begins; bm25 has no checksum, so its walk is the only thing
// standing between a damaged byte and a query. What both cover is the class a
// checksum cannot see: a writer that emitted inconsistent bytes and then
// checksummed exactly what it emitted.
//
// IT IS READ-ONLY. It opens files, hashes bytes and walks dictionaries. It never
// writes, renames, quarantines or deletes — a census that repaired what it found
// would destroy the evidence its own next run depends on.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// mergeScratchPrefix is the engine's merge-output temp-file prefix. Kept here as
// a named constant rather than a literal in the walk so the reason it is skipped
// travels with it.
const mergeScratchPrefix = "merge-"

// structuralValidators maps a store format directory to the validator that
// speaks for that family. The census reads the format off the PATH because that
// is how the store itself keys the families apart; a payload announces its own
// layout version, never which family it was filed under.
//
// The names come from the packages that own them rather than from literals here,
// so a family that renames its format cannot leave this dispatch silently
// pointing at a directory nothing is written to. A format absent from this map
// is checked for its content address and nothing else, and its rows say so.
var structuralValidators = map[string]func(searchengine.SegmentID, []byte) error{
	bm25.FormatName: bm25.ValidateSegment,
	hnsw.FormatName: hnsw.ValidateSegment,
}

// SegmentVerdict is one stored file's census row.
type SegmentVerdict struct {
	// Path is the absolute path of the stored file.
	Path string
	// ID is the filename's segment id — the name every reader trusts.
	ID searchengine.SegmentID
	// Format, GraphType and GraphName are the three path components above the
	// file in the store's layout: <root>/<format>/<type>/<name>/<id>.seg.
	//
	// THE LAST TWO ARE A TYPE AND A NAME, not a graph and a bucket. An earlier
	// spelling called them Graph and Bucket, which reads as though the middle
	// component identified a graph and the last one some subdivision of it. They
	// are the graph TYPE (code, knowledge, practice) and the graph's own NAME
	// (machine, default) — the pair every selector in this program is keyed on.
	Format    string
	GraphType string
	GraphName string
	// Quarantined reports that this file sits under the store's quarantine
	// directory rather than in a served bucket. Such a file is retained
	// deliberately as evidence and is NOT a live member of the corpus, so a
	// census that counted it among the served population would overstate what a
	// reader can actually reach — and would keep re-reporting a hit for a segment
	// whose disposition has already been taken.
	Quarantined bool
	// Size is the stored file's size, and PayloadSize the size beneath any
	// supersession envelope. They differ exactly when an envelope is present.
	Size        int64
	PayloadSize int
	// ModTime is the file's last modification time, which is when the producer
	// wrote it — the axis a time-clustered producer defect shows up on.
	ModTime time.Time
	// Superseded is the number of constituent ids in the supersession envelope,
	// and it is the ONLY provenance the stored bytes carry.
	//
	// READ IT AS "REPLACED SOMETHING", NOT AS "CAME FROM A MERGE". A non-zero
	// count means this file was published in place of the prior segments it
	// names, which a merge does — and so does a rebuild that publishes a
	// deterministic segment over the ones it supersedes (observed: a
	// manage(rebuild_segments) output carries an envelope). Zero means the file
	// replaced nothing, which is a fresh build or a tail write. The envelope
	// does not record WHICH producer wrote it, so a census cannot separate merge
	// from rebuild on the bytes alone.
	Superseded int
	// AddressOK reports whether the payload hashes to ID.
	AddressOK bool
	// StructureExamined reports whether a structural validator ran at all. False
	// for every format without one — the row is then silent about structure
	// rather than clean.
	StructureExamined bool
	// Err is the first failure observed for this file: an unreadable file, a
	// damaged envelope, a content-address mismatch, or a structural violation.
	// A *searchengine.CorruptSegmentError here is the incident's own shape.
	Err error
}

// Corrupt reports whether this row failed the structural walk specifically, as
// opposed to any other failure the census can record.
func (v SegmentVerdict) Corrupt() bool {
	var ce *searchengine.CorruptSegmentError
	return errors.As(v.Err, &ce)
}

// CensusStore walks root and returns one verdict per stored segment file, in
// directory order. Files whose name is not <id>.seg are skipped: the store keeps
// its own bookkeeping beside the segments and those are not segments.
//
// A WALK ERROR ON AN INDIVIDUAL PATH BECOMES A ROW, NOT AN ABORT, and the
// distinction is the whole contract: a census that returned at its first
// unreadable directory would report a population truncated at its first
// surprise, and would do it while looking exactly like a clean sweep of a
// smaller store. Only a failure to walk the ROOT itself — which yields no
// population at all — is returned as the call's error.
func CensusStore(root string) ([]SegmentVerdict, error) {
	var out []SegmentVerdict
	//nolint:gosec // G703: root is an operator-named store directory for a read-only diagnostic, not user text.
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			out = append(out, SegmentVerdict{Path: path, Err: fmt.Errorf("walk: %w", err)})
			// Returning nil keeps the sweep going past an unreadable subtree; the
			// row above is what keeps it from going past it SILENTLY.
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".seg") {
			return nil
		}
		// MERGE SCRATCH IS NOT A SEGMENT. A merge writes its output to a
		// merge-*.seg temp file in the same directory and unlinks it the moment
		// the mapping succeeds, so one is visible only DURING a merge — or after a
		// crash, until the next merge's sweep removes it. Censusing it reports a
		// half-written file as a segment whose bytes do not hash to its name,
		// which is a false alarm of exactly the kind that teaches a reader to
		// ignore this gate. The name is the engine's own temp pattern
		// (os.CreateTemp(dir, "merge-*.seg"), merge_entry.go).
		if strings.HasPrefix(d.Name(), mergeScratchPrefix) {
			return nil
		}
		out = append(out, censusOne(root, path, d))
		return nil
	})
	if err != nil {
		return out, fmt.Errorf("segmentdist: census of %s: %w", root, err)
	}
	return out, nil
}

// censusOne examines a single stored file. It reports the FIRST failure it hits
// and stops: a file whose envelope will not decode has no payload to hash, and a
// payload that is not the bytes it claims to be says nothing useful about the
// dictionary inside it.
func censusOne(root, path string, d fs.DirEntry) SegmentVerdict {
	v := SegmentVerdict{
		Path: path,
		ID:   strings.TrimSuffix(d.Name(), ".seg"),
	}
	v.Format, v.GraphType, v.GraphName = censusPathParts(root, path)
	v.Quarantined = quarantinedPath(root, path)

	info, err := d.Info()
	if err != nil {
		v.Err = fmt.Errorf("stat: %w", err)
		return v
	}
	v.Size, v.ModTime = info.Size(), info.ModTime()

	raw, err := os.ReadFile(path)
	if err != nil {
		v.Err = fmt.Errorf("read: %w", err)
		return v
	}

	envelope, payload, err := searchengine.SplitStoredBlob(raw)
	if err != nil {
		v.Err = fmt.Errorf("split stored blob: %w", err)
		return v
	}
	v.PayloadSize = len(payload)
	if len(envelope) > 0 {
		if superseded, _, serr := searchengine.SupersededBy(raw); serr == nil {
			v.Superseded = len(superseded)
		}
	}

	sum := sha256.Sum256(payload)
	v.AddressOK = hex.EncodeToString(sum[:]) == v.ID
	if !v.AddressOK {
		v.Err = fmt.Errorf("content address: %d-byte payload hashes to %s, not to its own filename",
			len(payload), hex.EncodeToString(sum[:]))
		return v
	}

	if validate, ok := structuralValidators[v.Format]; ok {
		v.StructureExamined = true
		v.Err = validate(v.ID, payload)
	}
	return v
}

// quarantinedPath reports whether a stored file sits in a quarantine location
// rather than a served bucket.
//
// TWO SPELLINGS ARE RECOGNIZED because the store carries two. Quarantine writes
// a <root>/quarantine/ subdirectory; the live store also holds buckets named
// "<name>.quarantine" from the segment-repair machinery that predates it. Both
// mean the same thing to a census — these bytes are evidence, not corpus — and
// recognizing only the newer one would report every older quarantined file as a
// live member.
func quarantinedPath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	for part := range strings.SplitSeq(filepath.ToSlash(rel), "/") {
		if part == quarantineDirName || strings.HasSuffix(part, "."+quarantineDirName) {
			return true
		}
	}
	return false
}

// censusPathParts splits the three layout components above a stored file. A file
// that does not sit at the store's canonical depth yields empty strings for the
// components it lacks rather than a guess — the format component in particular
// decides whether a structural validator runs, and inventing one would run the
// wrong validator over unrelated bytes.
func censusPathParts(root, path string) (format, graphType, graphName string) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", "", ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 4 {
		return "", "", ""
	}
	return parts[0], parts[1], parts[2]
}
