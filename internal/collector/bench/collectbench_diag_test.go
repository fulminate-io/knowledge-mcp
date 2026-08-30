//go:build collectbench

// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/contribhash"
	collectorwire "github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// collectbench_diag_test.go NAMES the files a K=0 collect re-uploads, and records
// the exact inputs their per-file contribution hash was folded from.
//
// WHY A NAMING INSTRUMENT EXISTS AT ALL. The harness reports
// uploaded_file_owned_files as a COUNT, and across four K=0 collects of an
// unchanged tree that count read 151, 154, 160 and 146. A varying count says
// something moves between runs but cannot say WHICH files moved or WHY, and the
// difference between two adjacent runs' SETS is the sharpest available signal —
// a file in one run's set and not the other's is a file whose hash changed with
// nothing on disk changing.
//
// WHAT IT CAPTURES, and it is deliberately the hash's OWN inputs rather than a
// summary of them: for every file in the upload set, the ordered node ids and the
// ordered edge tuples exactly as contribhash folds them — nodes by Id, edges by
// contribhash.SortFileGroupRows, which is the SAME call fileGroupHash makes — with
// each edge rendered across all seven hashed fields. Two runs' files can then be
// compared field by field instead of by their digests alone, so a moving input is
// OBSERVED rather than inferred.
//
// THE ORDERING IS BORROWED, NEVER RE-DERIVED, AND THAT RULE HAS ALREADY BEEN
// BROKEN ONCE. This file used to sort edges inline by (FromID, ToID, Type). When
// the determinism fix made the production order the seven-part lessEdgeKey, the
// two silently diverged and every listing described a fold nobody performs — for
// exactly the files that own a duplicated triple, which is the population the
// instrument exists to explain. Calling SortFileGroupRows is what keeps the
// promise; production_hash_diff is what would catch it being broken again.
//
// IT RECORDS FACTS, NOT VERDICTS. dup_key_groups counts the four-part edge
// identities a file owns more than once — see the field for why that key and not
// the triple. Whether duplicated identities are what moved these hashes is for
// the captured data to show; the field is here so the question is answerable.

// diagDir is a stable location OUTSIDE the conductor's mktemp WORKDIR, so the
// capture survives the run that produced it and two runs can be diffed after the
// fact.
const diagDir = "/tmp/collectbench-diag"

// fileHashInputs is one file's contribution-hash input, in fold order.
type fileHashInputs struct {
	Path      string `json:"path"`
	FileHash  string `json:"file_hash"`
	NodeCount int    `json:"node_count"`
	EdgeCount int    `json:"edge_count"`
	// ProductionHashDiff is EMPTY in the healthy case and carries production's own
	// hash for this file when this capture's fold disagrees with it. A non-empty
	// value means the instrument has drifted from what the collect path computes,
	// and every listing below it describes a hash nobody uses.
	ProductionHashDiff string `json:"production_hash_diff,omitempty"`
	// DupKeyGroups counts FOUR-PART edge identities — (FromID, ToID, Type,
	// Evidence) — this file owns more than once. That is the key the production
	// order's discriminating prefix uses and the key the server's unique index
	// enforces, so a non-zero value here names rows whose relative order genuinely
	// is not determined by the identity. It counted the bare TRIPLE before, which
	// reported every ordinary multi-candidate reference as undetermined.
	DupKeyGroups int `json:"dup_key_groups"`
	// NodeSeqDigest and EdgeSeqDigest are SHA-256 over the FULL ordered
	// sequences, so a difference is detectable even where the listings below are
	// truncated. A truncated listing that hid a difference would make this
	// instrument worse than the count it replaces.
	NodeSeqDigest string   `json:"node_seq_digest"`
	EdgeSeqDigest string   `json:"edge_seq_digest"`
	NodeIDs       []string `json:"node_ids"`
	EdgeTuples    []string `json:"edge_tuples"`
}

// diagRecord is one run's capture.
type diagRecord struct {
	RunLabel      string           `json:"run_label"`
	UploadedFiles []string         `json:"uploaded_files"`
	UploadedCount int              `json:"uploaded_count"`
	Inputs        []fileHashInputs `json:"inputs"`
}

// diagListingCap bounds the per-file listings. The sequence digests above are
// computed over the full sequences regardless, so the cap costs detail rather
// than detection.
const diagListingCap = 300

// captureFileHashInputs renders every file's hash inputs in the order
// contribhash folds them. It reuses the production partition and the production
// orderings rather than re-deriving either — a diagnostic that ordered rows its
// own way would describe a hash nobody computes.
func captureFileHashInputs(nodes []*knowledgev1.Node, edges []kgwire.BatchEdge) map[string]fileHashInputs {
	byFile, _ := contribhash.PartitionByOwningFile(nodes, edges)
	// THE WHOLE-RESULT PRODUCTION ENTRY POINT, computed once, as the equality
	// control below. It is what the collect path actually calls, so a per-file
	// hash this capture derives that disagrees with it means the capture drifted.
	production := contribhash.FileContributionHashes(nodes, edges)
	out := make(map[string]fileHashInputs, len(byFile))
	for path, g := range byFile {
		// PRODUCTION ORDERING, NOT A LOCAL ONE. contribhash.SortFileGroupRows is the
		// same call fileGroupHash makes, so the listings below are in the order the
		// hash is actually folded from.
		ns, es := contribhash.SortFileGroupRows(g)

		nodeIDs := make([]string, len(ns))
		nodeHashes := make([][32]byte, len(ns))
		for i, n := range ns {
			nodeIDs[i] = n.GetId()
			nodeHashes[i] = contribhash.NodeContributionHash(n)
		}
		edgeTuples := make([]string, len(es))
		edgeHashes := make([][32]byte, len(es))
		dupKeys := map[string]int{}
		for i, e := range es {
			// THE KEY IS THE FOUR-PART EDGE IDENTITY, matching the unique index the
			// server keys on and the discriminating prefix lessEdgeKey compares. A
			// three-part key counted as duplicates every pair the identity separates,
			// which is the ordinary multi-candidate reference — so the number said
			// "these rows have no determined order" about rows whose order is fully
			// determined.
			key := e.FromID + "\x00" + e.ToID + "\x00" + string(e.Type) + "\x00" + e.Evidence
			dupKeys[key]++
			edgeTuples[i] = fmt.Sprintf("%s|%s|%s|%g|%g|%s|%s",
				e.FromID, e.ToID, string(e.Type), e.Weight, e.Confidence, e.Method, e.Evidence)
			edgeHashes[i] = contribhash.EdgeContributionHash(e)
		}
		dupGroups := 0
		for _, n := range dupKeys {
			if n > 1 {
				dupGroups++
			}
		}
		fh := contribhash.FileContributionHash(nodeHashes, edgeHashes)
		// SELF-CHECK, RECORDED RATHER THAN ASSERTED. This capture folds the rows
		// itself so it can list them; production folds them through
		// FileContributionHashes. The two must agree, and if they ever do not, the
		// listing is describing a hash nobody computes — the exact failure the
		// three-part inline sort produced. It is a recorded field rather than a
		// require because this instrument runs inside the measured upload path and
		// must never fail a bench run; a mismatch is a fact the capture carries.
		mismatch := ""
		if prod, ok := production[path]; !ok || prod != fh {
			mismatch = hex.EncodeToString(prod[:])
		}
		out[path] = fileHashInputs{
			Path:               path,
			ProductionHashDiff: mismatch,
			FileHash:           hex.EncodeToString(fh[:]),
			NodeCount:          len(ns),
			EdgeCount:          len(es),
			DupKeyGroups:       dupGroups,
			NodeSeqDigest:      seqDigest(nodeIDs),
			EdgeSeqDigest:      seqDigest(edgeTuples),
			NodeIDs:            capList(nodeIDs),
			EdgeTuples:         capList(edgeTuples),
		}
	}
	return out
}

func seqDigest(seq []string) string {
	h := sha256.New()
	for _, s := range seq {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func capList(in []string) []string {
	if len(in) <= diagListingCap {
		return in
	}
	return in[:diagListingCap]
}

// writeUploadDiag persists the named upload set plus the PRE-DIFF hash inputs of
// exactly those files. Pre-diff is the load-bearing part: those are the inputs
// the diff compared against the server manifest to DECIDE the upload, so they are
// the bytes whose movement the decision followed.
func writeUploadDiag(t *testing.T, label string, pre map[string]fileHashInputs, result *collectorwire.CollectResult) {
	t.Helper()
	uploaded := map[string]struct{}{}
	for _, n := range result.Nodes {
		if n.GetFilePath() != "" {
			uploaded[n.GetFilePath()] = struct{}{}
		}
	}
	paths := make([]string, 0, len(uploaded))
	for p := range uploaded {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	rec := diagRecord{RunLabel: label, UploadedFiles: paths, UploadedCount: len(paths)}
	for _, p := range paths {
		if in, ok := pre[p]; ok {
			rec.Inputs = append(rec.Inputs, in)
		}
	}
	require.NoError(t, os.MkdirAll(diagDir, 0o750))
	blob, err := json.MarshalIndent(rec, "", "  ")
	require.NoError(t, err)
	// filepath.Base over the slug: diagSlug already reduces a run label to
	// [A-Za-z0-9-], so no separator can survive it, but the write below takes a
	// path built from caller-supplied text and the sanitisation is stated at the
	// write rather than inferred from a helper three functions away.
	out := filepath.Join(diagDir, filepath.Base(diagSlug(label)+".json"))
	require.NoError(t, os.WriteFile(out, append(blob, '\n'), 0o600))
	t.Logf("upload diag: %d file-owned files named, inputs captured for %d, written to %s",
		len(paths), len(rec.Inputs), out)
}

// diagSlug renders a run label as a filename.
func diagSlug(label string) string {
	s := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '-'
		}
	}, label)
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-")
	if s == "" {
		return "run"
	}
	return s
}
