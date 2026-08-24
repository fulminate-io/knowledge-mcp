//go:build collectbench

// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// collectbench_leasetail_test.go SPLITS THE LEASE WINDOW into the three parts a
// lease TTL has to cover, and adds the file-DELETION the split needs to be
// meaningful.
//
// WHY IT EXISTS. upload_ms is the lease window, and every conductor run so far
// finished with deleted_files_on_finalize = 0 — so the finalize tail has only
// ever been measured doing nothing. The tail is where the expensive half now
// lives: the container-staleness mark, the generation bump and the prune cascade
// all run there, and chunk-time destruction moved the cheap half out of it. A TTL
// derived from tails that deleted nothing is an envelope around the wrong thing.
//
// THE THREE NUMBERS, read off the sink's own Debug lines rather than re-timed
// here, so the measurement is of the shipped path and not of a harness stopwatch:
//
//	chunk sent        sink.go            per chunk; the MAX is the chunk-RPC worst case
//	finalize accepted sink.go            handler only
//	finalize done     sink_finalize_tail handler + tail
//
// TAIL = done - accepted. Both finalize lines measure from the SAME finStart, so
// the subtraction is exact rather than an approximation across two clocks.
//
// THE TAP FORCES Enabled, copying installMarkInputTap's idiom in this package:
// these are Debug records and the ambient level would drop them, so a level-based
// approach would silently measure nothing. Reading the `dur` ATTR rather than
// parsing the rendered line means a formatting change cannot quietly break it.

// envDeleteK names how many files this run REMOVES from the tree copy before
// collecting. Zero (or unset) removes nothing, which is every pre-existing run.
const envDeleteK = "KBENCH_DELETE_K"

// leaseTailDir is where the artifacts land, per the lease plan's step shape.
const leaseTailDir = "/tmp/ful-981"

// sinkTimings is one collect's split of the upload window.
type sinkTimings struct {
	RunLabel         string  `json:"run_label"`
	DeletedFiles     int     `json:"deleted_files_from_tree"`
	ChunkSentCount   int     `json:"chunk_sent_count"`
	ChunkSentMaxMS   int64   `json:"chunk_sent_max_ms"`
	ChunkSentAllMS   []int64 `json:"chunk_sent_all_ms"`
	FinalizeAcceptMS int64   `json:"finalize_accepted_ms"`
	FinalizeDoneMS   int64   `json:"finalize_done_ms"`
	TailMS           int64   `json:"tail_ms"`
	SawAccepted      bool    `json:"saw_accepted"`
	SawDone          bool    `json:"saw_done"`
	SawNoFinalizeID  bool    `json:"saw_no_finalize_id_variant"`
}

// sinkTimingTap captures the three sink Debug lines.
type sinkTimingTap struct {
	slog.Handler
	mu sync.Mutex
	t  sinkTimings
}

func (s *sinkTimingTap) Enabled(context.Context, slog.Level) bool { return true }

func (s *sinkTimingTap) Handle(_ context.Context, r slog.Record) error {
	var msg string
	switch r.Message {
	case "remote sink: chunk sent",
		"remote sink: finalize accepted",
		"remote sink: finalize done":
		msg = r.Message
	case "remote sink: finalize done (server returned no finalize id)":
		// CAPTURED SEPARATELY rather than folded in: this variant means the server
		// returned no finalize id, so there is no tail to measure and treating it
		// as a `done` would report a tail of zero as if it were a measurement.
		s.mu.Lock()
		s.t.SawNoFinalizeID = true
		s.mu.Unlock()
		return nil
	default:
		return nil
	}

	var dur time.Duration
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "dur" {
			dur = a.Value.Duration()
			return false
		}
		return true
	})

	s.mu.Lock()
	defer s.mu.Unlock()
	ms := dur.Milliseconds()
	switch msg {
	case "remote sink: chunk sent":
		s.t.ChunkSentCount++
		s.t.ChunkSentAllMS = append(s.t.ChunkSentAllMS, ms)
		if ms > s.t.ChunkSentMaxMS {
			s.t.ChunkSentMaxMS = ms
		}
	case "remote sink: finalize accepted":
		s.t.FinalizeAcceptMS, s.t.SawAccepted = ms, true
	case "remote sink: finalize done":
		s.t.FinalizeDoneMS, s.t.SawDone = ms, true
	}
	return nil
}

// installSinkTimingTap redirects the default logger through the tap and writes
// the split to leaseTailDir on cleanup, which runs even when the test fails.
func installSinkTimingTap(t *testing.T, label string, deleted int) {
	t.Helper()
	tap := &sinkTimingTap{Handler: slog.Default().Handler()}
	tap.t.RunLabel, tap.t.DeletedFiles = label, deleted
	prev := slog.Default()
	slog.SetDefault(slog.New(tap))
	t.Cleanup(func() {
		slog.SetDefault(prev)
		tap.mu.Lock()
		defer tap.mu.Unlock()
		if tap.t.SawAccepted && tap.t.SawDone {
			tap.t.TailMS = tap.t.FinalizeDoneMS - tap.t.FinalizeAcceptMS
		}
		require.NoError(t, os.MkdirAll(leaseTailDir, 0o755))
		blob, err := json.MarshalIndent(tap.t, "", "  ")
		require.NoError(t, err)
		out := filepath.Join(leaseTailDir, leaseSlug(label)+".json")
		require.NoError(t, os.WriteFile(out, append(blob, '\n'), 0o600))
		t.Logf("lease split [%s]: deleted_from_tree=%d chunks=%d chunk_max=%dms "+
			"accepted=%dms done=%dms TAIL=%dms -> %s",
			label, tap.t.DeletedFiles, tap.t.ChunkSentCount, tap.t.ChunkSentMaxMS,
			tap.t.FinalizeAcceptMS, tap.t.FinalizeDoneMS, tap.t.TailMS, out)
	})
}

// recordDeletionSets writes the two deletion populations a delete-run has, so the
// gap between them can be read off a file rather than inferred from two counts.
//
// THE TWO ARE DIFFERENT POPULATIONS BY CONSTRUCTION, which is the whole reason
// they are recorded side by side: removedFromTree is every file this run unlinked
// from the tree copy, while namedOnFinalize is what the FinalizeRequest carried —
// and the client can only name a path the SERVER holds, so a removed file the
// collector never indexed (generated, vendored, under a skipped path component)
// has no manifest entry to go missing from and is correctly absent from the wire
// set. RemovedNotNamed is that difference, and it is the field to read: it is
// expected to be non-empty and every member of it must be a file discovery
// declines, never one it indexes.
func recordDeletionSets(t *testing.T, label string, removedFromTree, namedOnFinalize []string) {
	t.Helper()
	removed := append([]string(nil), removedFromTree...)
	named := append([]string(nil), namedOnFinalize...)
	sort.Strings(removed)
	sort.Strings(named)

	namedSet := make(map[string]bool, len(named))
	for _, f := range named {
		namedSet[f] = true
	}
	gap := make([]string, 0, len(removed))
	for _, f := range removed {
		if !namedSet[f] {
			gap = append(gap, f)
		}
	}

	blob, err := json.MarshalIndent(struct {
		RunLabel        string   `json:"run_label"`
		RemovedCount    int      `json:"removed_from_tree_count"`
		NamedCount      int      `json:"named_on_finalize_count"`
		RemovedFromTree []string `json:"removed_from_tree"`
		NamedOnFinalize []string `json:"named_on_finalize"`
		RemovedNotNamed []string `json:"removed_not_named"`
	}{label, len(removed), len(named), removed, named, gap}, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(leaseTailDir, 0o755))
	out := filepath.Join(leaseTailDir, leaseSlug(label)+".deletion-sets.json")
	require.NoError(t, os.WriteFile(out, append(blob, '\n'), 0o600))
	t.Logf("deletion sets [%s]: removed_from_tree=%d named_on_finalize=%d removed_not_named=%d -> %s",
		label, len(removed), len(named), len(gap), out)
}

func leaseSlug(label string) string {
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
	if s = strings.Trim(s, "-"); s == "" {
		return "run"
	}
	return s
}

// deleteKFromEnv reads how many files this run removes, defaulting to none.
func deleteKFromEnv(t *testing.T) int {
	t.Helper()
	raw := os.Getenv(envDeleteK)
	if raw == "" {
		return 0
	}
	k, err := strconv.Atoi(raw)
	require.NoError(t, err, "%s must be an integer, got %q", envDeleteK, raw)
	require.GreaterOrEqual(t, k, 0, "%s must not be negative", envDeleteK)
	return k
}

// deleteKFiles REMOVES k collected files from the tree copy, so the next collect
// carries a real deletion set and the finalize tail does real work.
//
// IT DELETES FROM THE CONDUCTOR'S TREE COPY, NEVER A REAL CHECKOUT — the copy is
// a throwaway built per phase, which is the same protection mutateKFiles relies
// on when it edits files in place.
//
// THE SELECTION MIRRORS mutateKFiles: walk, skip the trees the collector does not
// chunk, sort for determinism, take the first k. Choosing the same way means the
// deleted set is a comparable population to the mutated one rather than an
// arbitrary corner of the tree.
func deleteKFiles(t *testing.T, tree string, k int) []string {
	t.Helper()
	if k == 0 {
		return nil
	}
	var candidates []string
	require.NoError(t, filepath.Walk(tree, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if name := info.Name(); name == ".git" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			candidates = append(candidates, path)
		}
		return nil
	}), "walk the copied tree")
	sort.Strings(candidates)
	require.GreaterOrEqual(t, len(candidates), k,
		"the copied tree holds only %d deletable Go files, fewer than the requested K=%d", len(candidates), k)

	// TAKE FROM THE END so a run that both mutates and deletes does not fight over
	// the same files: mutateKFiles takes from the front of the same sorted list.
	rel := make([]string, 0, k)
	for _, path := range candidates[len(candidates)-k:] {
		require.NoError(t, os.Remove(path), "remove %s", path)
		r, rerr := filepath.Rel(tree, path)
		require.NoError(t, rerr)
		rel = append(rel, r)
	}
	return rel
}
