// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
)

// manifest_branch_seed_test.go — a branch's FIRST collect, driven end to end
// through WriteResult against the recording fake.
//
// WHAT IS BEING PINNED. The three per-branch baselines are keyed on graph AND
// branch, so a branch that has never been collected on this machine holds none of
// them. Absence reads as "changed" for both trigger-table rows, the collect
// degrades to a full upload, and the branch's first touch puts the WHOLE repo on
// the wire for a delta of a few lines. The resolution is to seed an absent
// branch key from the value the SAME GRAPH's other branches unanimously carry —
// those rows are what the server's clone gave this branch, so the signature was
// already true of them before this collect began.
//
// WHY THE SUITE DRIVES THE WIRE RATHER THAN THE MECHANISM. Every assertion here
// reads what the sink actually sent — uploaded node ids, the echoed manifest
// identity, the Finalize's deletion carrier — so the suite observes BEHAVIOUR and
// names no symbol the resolution introduces. That is what lets it be authored
// against the unfixed tree and compile there.
//
// THE SUBTESTS ARE FLAT AND THERE ARE EXACTLY TEN. A criterion pins the count, so
// a subtest quietly dropped to reach green cannot hide behind nesting.

// firstTouchBranch is the non-default branch every fixture below collects on. It
// is deliberately NOT "main": the sibling twins keep the constructors' own "main"
// branch, so the branch under test and its siblings differ in exactly the field
// the keys are scoped by.
const firstTouchBranch = "feature-x"

// decoyGraphName EXTENDS the twoFileResult fixture's graph name rather than
// merely resembling it. A sibling scan that matches on the bare prefix — without
// the "@" the keys are built with — admits this graph's records into the real
// graph's sibling set. The shape is not contrived: a worktree checkout keys a
// graph under its own directory basename, which is routinely the repo's name with
// a suffix appended.
const decoyGraphName = "diff-upload-repo2"

// onBranch re-keys a fixture onto the branch under test.
//
// IT MUTATES AND RETURNS rather than copying, because every caller builds its
// fixture by calling a constructor fresh — the twin is INDEPENDENTLY CONSTRUCTED,
// never derived from the result under test, which is runTriggerCase's discipline
// and for its reason: a copied signature would pass even against a
// nondeterministic one.
func onBranch(result *collectorwire.CollectResult, branch string) *collectorwire.CollectResult {
	result.CurrentBranch = branch
	return result
}

// uploadedIDsOf returns the union of node ids across every chunk the fake
// captured, under the fake's own lock.
func uploadedIDsOf(rec *recordingIngest) map[string]bool {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return uploadedNodeIDs(rec.chunks)
}

// runBranchCollect isolates the store, seeds it as the caller describes, and
// drives ONE whole collect of result through WriteResult.
//
// THE SEED RUNS AFTER THE ISOLATION AND BEFORE THE COLLECT, which is the only
// ordering that works: seeding first would write into the developer's real store,
// and seeding after would arrive too late to be the previous collect's record.
func runBranchCollect(
	t *testing.T,
	result *collectorwire.CollectResult,
	manifest *knowledgev1.CollectManifestResponse,
	seed func(),
) (*recordingIngest, *capturingSlog) {
	t.Helper()
	isolateDiscoveryStore(t)
	t.Setenv(collectDiffEnv, "on")

	client, rec := startRecordingIngest(t)
	rec.manifest = manifest
	seed()

	h := installCapturingSlog(t)
	require.NoError(t, NewUploadSink(client).WriteResult(context.Background(), "", result))
	return rec, h
}

// seedSiblingAt records one sibling's baselines at a chosen collector version,
// on a chosen branch, for a chosen graph.
//
// IT GOES THROUGH seedCollectBaselines rather than writing literals, so the
// recorded values are whatever the production signature and key functions
// produce — the same reason that helper exists.
func seedSiblingAt(branch, graphName string, version uint32) {
	sibling := twoFileResult()
	sibling.CurrentBranch = branch
	sibling.GraphName = graphName
	sibling.CollectorOutputVersion = version
	seedCollectBaselines(sibling)
}

// requireNoFallback asserts the collect took the diff lane with no trigger
// recorded, naming the trigger when one fired so a failure is diagnosable.
func requireNoFallback(t *testing.T, h *capturingSlog) {
	t.Helper()
	got, fired := h.recordedFallback()
	require.False(t, fired,
		"a seeded first touch must take the diff lane and record NO fallback, got %q", got)
}

// requireFallback asserts a specific trigger was RECORDED, not merely acted on —
// an unwired predicate is silent, so silence has to be distinguishable from a name.
func requireFallback(t *testing.T, h *capturingSlog, want fallbackReason) {
	t.Helper()
	got, fired := h.recordedFallback()
	require.True(t, fired, "the %s trigger must be RECORDED, not merely acted on", want)
	require.Equal(t, string(want), got)
}

// TestBranchFirstTouch_SeedsFromSiblingBaselines is the whole branch-first-touch
// contract: the delta lane where the siblings agree, and today's full re-land
// preserved everywhere they do not.
func TestBranchFirstTouch_SeedsFromSiblingBaselines(t *testing.T) {
	// DELIBERATELY MIXED, because this is the shape production actually takes. The
	// fixture carries a fileless set and the sibling seeds only the discovery and
	// collector keys, so the FILELESS key is left absent on both sides — which is
	// the real state on a developer machine, where fileless signatures legitimately
	// differ per branch and their siblings are therefore never unanimous. The
	// file-bearing rows take the diff lane while the fileless payload still rides.
	t.Run("zero_delta_takes_the_diff_lane_and_echoes", func(t *testing.T) {
		result := onBranch(filelessResult(), firstTouchBranch)
		rec, h := runBranchCollect(t, result, manifestMatching(result), func() {
			seedCollectBaselines(filelessResult())
		})

		requireNoFallback(t, h)
		require.Equal(t, "manifest-matching", rec.finalizeRequest(t).GetManifestId(),
			"nothing was re-landed, so the served identity must still be echoed")
		// A SET EQUALITY OVER IDS, NOT A COUNT: a count of two is satisfied by
		// uploading the two file-bearing nodes, which is the exact defect.
		require.Equal(t,
			map[string]bool{"pkg": true, "language:go": true},
			uploadedIDsOf(rec),
			"the fileless payload must ride while NEITHER file-bearing node uploads")
	})

	// The headline case: a one-file delta on a branch's first touch ships one
	// file's rows rather than the repo.
	t.Run("one_file_delta_uploads_only_that_file", func(t *testing.T) {
		result := onBranch(twoFileResult(), firstTouchBranch)
		rec, h := runBranchCollect(t, result, manifestOmitting(result, "pkg/b.go"), func() {
			seedCollectBaselines(twoFileResult())
		})

		requireNoFallback(t, h)
		require.Equal(t, 1, uploadedNodeCount(rec),
			"a seeded first touch uploads the DELTA, never the whole node set")
		require.Equal(t, map[string]bool{"pkg/b.go:Beta": true}, uploadedIDsOf(rec),
			"and it uploads exactly the file the manifest never served")
	})

	// THE ARM THAT MUST NOT WEAKEN. The seed changes WHICH prior an absent key
	// resolves to; it does not change the comparison. A collector whose emitted
	// output genuinely moved still re-lands with the decline suppressed, because the
	// siblings unanimously carry the OLD version and the client carries the new one.
	t.Run("genuine_collector_upgrade_still_re_lands_with_echo_suppressed", func(t *testing.T) {
		result := onBranch(twoFileResult(), firstTouchBranch)
		result.CollectorOutputVersion = testCollectorOutputVersion + 1
		rec, h := runBranchCollect(t, result, manifestMatching(result), func() {
			seedCollectBaselines(twoFileResult())
		})

		requireFallback(t, h, fallbackCollectorVersionChange)
		chunkIDs, finalizeID := echoedManifestIDs(t, rec)
		for i, id := range chunkIDs {
			require.Empty(t, id,
				"chunk %d must withhold the identity, or the forced upload is declined file by file "+
					"and the re-land accomplishes nothing", i)
		}
		require.Empty(t, finalizeID, "the Finalize must withhold it too, or the collect is half-declined")
	})

	// BOOTSTRAP PRESERVED. With no sibling to resolve against, absence keeps
	// today's meaning and the machine takes its one full re-land — the branch-scoped
	// twin of TestCollectorVersionChange_FirstCollectReLands.
	t.Run("no_sibling_records_still_re_lands", func(t *testing.T) {
		result := onBranch(twoFileResult(), firstTouchBranch)
		rec, h := runBranchCollect(t, result, manifestMatching(result), func() {})

		requireFallback(t, h, fallbackCollectorVersionChange)
		chunkIDs, finalizeID := echoedManifestIDs(t, rec)
		for i, id := range chunkIDs {
			require.Empty(t, id, "chunk %d of an unseedable first touch must withhold the identity", i)
		}
		require.Empty(t, finalizeID)
	})

	// NON-UNANIMOUS SIBLINGS RESOLVE NOTHING. Two branches of the same graph at two
	// different collector versions is a real machine state, not a contrived one, and
	// there is no honest answer to inherit from it — so absence keeps today's
	// meaning and the trigger fires.
	t.Run("disagreeing_sibling_versions_still_re_land", func(t *testing.T) {
		result := onBranch(twoFileResult(), firstTouchBranch)
		rec, h := runBranchCollect(t, result, manifestMatching(result), func() {
			seedSiblingAt("main", result.GraphName, testCollectorOutputVersion)
			seedSiblingAt("other-branch", result.GraphName, testCollectorOutputVersion+1)
		})

		requireFallback(t, h, fallbackCollectorVersionChange)
		_, finalizeID := echoedManifestIDs(t, rec)
		require.Empty(t, finalizeID, "a re-land withholds the identity")
	})

	// AN EMPTY BRANCH IS A BASE FULL-REPLACE, not an overlay — there is no clone and
	// so no sibling whose rows this collect inherited. It must seed nothing even
	// though same-graph records are sitting right there.
	t.Run("empty_branch_never_seeds", func(t *testing.T) {
		result := onBranch(twoFileResult(), "")
		_, h := runBranchCollect(t, result, manifestMatching(result), func() {
			seedCollectBaselines(twoFileResult())
		})

		requireFallback(t, h, fallbackCollectorVersionChange)
	})

	// THE OTHER TRIGGER, and its ECHO is the discriminator. A discovery-mode change
	// degrades to a full upload the server still declines file by file — correct,
	// because those rows genuinely match what it holds — so the identity must still
	// be echoed. Only the collector-version trigger suppresses it.
	t.Run("branch_discovery_signature_differs_still_falls_back", func(t *testing.T) {
		result := onBranch(twoFileResult(), firstTouchBranch)
		result.DiscoveryFingerprint = "fp-a-different-discovery-configuration"
		rec, h := runBranchCollect(t, result, manifestMatching(result), func() {
			seedCollectBaselines(twoFileResult())
		})

		requireFallback(t, h, fallbackDiscoveryModeChange)
		chunkIDs, finalizeID := echoedManifestIDs(t, rec)
		for i, id := range chunkIDs {
			require.Equal(t, "manifest-matching", id,
				"chunk %d must still echo the identity: suppression is scoped to the collector-version trigger", i)
		}
		require.Equal(t, "manifest-matching", finalizeID)
	})

	// DISCRIMINATES THE CHOSEN DESIGN FROM A READ-THROUGH-ONLY ALTERNATIVE, which
	// every other subtest here passes.
	//
	// The first collect's finalize tail never confirms — this fake reports no
	// finalize id — so commitCollectBaselines WITHHOLDS and the only thing that can
	// have persisted the branch's baseline is the seed itself. The siblings then
	// advance to a new collector version, as a base re-collect after an upgrade
	// moves them. A read-through design leaves the branch key absent, resolves it
	// against the NOW-ADVANCED siblings, reads "unchanged", and misses the upgrade
	// permanently; a persisted seed pinned what the rows actually came from, so the
	// upgrade differs from it and the trigger fires.
	t.Run("seed_persists_so_an_unconfirmed_collect_re_fires_after_an_upgrade", func(t *testing.T) {
		isolateDiscoveryStore(t)
		t.Setenv(collectDiffEnv, "on")

		first := onBranch(twoFileResult(), firstTouchBranch)
		client, rec := startRecordingIngest(t)
		rec.manifest = manifestMatching(first)
		seedSiblingAt("main", first.GraphName, testCollectorOutputVersion)

		h := installCapturingSlog(t)
		require.NoError(t, NewUploadSink(client).WriteResult(context.Background(), "", first))
		// KNOWN POSITIVE for the seed itself. Without it the assertion below is
		// satisfied by a store in which nothing was ever written, which is exactly the
		// state this subtest exists to distinguish from.
		requireNoFallback(t, h)
		// PREMISE CONTROL. This fake returns no finalize id, so the sink never polls,
		// the tail never confirms and commitCollectBaselines withholds. Without that
		// being true the branch key could have been persisted by the COMMIT rather
		// than by the seed, and the subtest would prove nothing about either.
		require.NotNil(t, rec.finalizeRequest(t), "control: the collect must have reached Finalize")
		rec.mu.Lock()
		reportedFinalizeID := rec.finalizeID
		rec.mu.Unlock()
		require.Empty(t, reportedFinalizeID,
			"control: the fake must report NO finalize id, or the tail confirms and the commit — not the seed — persists the baseline")

		// The upgrade: the client moves, and a base re-collect moves every sibling
		// with it.
		seedSiblingAt("main", first.GraphName, testCollectorOutputVersion+1)
		second := onBranch(twoFileResult(), firstTouchBranch)
		second.CollectorOutputVersion = testCollectorOutputVersion + 1

		client2, rec2 := startRecordingIngest(t)
		rec2.manifest = manifestMatching(second)
		h2 := installCapturingSlog(t)
		require.NoError(t, NewUploadSink(client2).WriteResult(context.Background(), "", second))

		requireFallback(t, h2, fallbackCollectorVersionChange)
	})

	// THE DESTRUCTIVE-PATH GATE. Reaching diffModeOn on a first touch arms
	// decideUpload's deletion carrier in a state that is unreachable by construction
	// today, and sink.go puts that slice on the Finalize as DeletedFiles.
	//
	// The branch's discovered population is a STRICT SUBSET of the manifest the
	// server renders from the base, which is what a clone plus a deleted file looks
	// like — and the client cannot tell that from a checkout that simply does not
	// contain the file, because the discovery signature is content-blind. The
	// assertion is an EXACT set rather than "non-empty" so a future widening that
	// also names the surviving file or an ancestor directory is caught here.
	t.Run("strict_subset_branch_names_exactly_the_missing_file_as_a_deletion", func(t *testing.T) {
		sibling := twoFileResult()
		result := onBranch(twoFileResult(), firstTouchBranch)
		// The manifest is rendered from the SIBLING, so the server describes both
		// files exactly as a clone of the base would.
		manifest := manifestMatching(sibling)
		result.Nodes = result.Nodes[:1]
		result.Edges = nil

		rec, h := runBranchCollect(t, result, manifest, func() {
			seedCollectBaselines(sibling)
		})

		requireNoFallback(t, h)
		fin := rec.finalizeRequest(t)
		require.True(t, fin.GetDiffMode(), "control: the carrier only arms when the diff governs")
		require.Equal(t, []string{"pkg/b.go"}, fin.GetDeletedFiles(),
			"a seeded first touch names EXACTLY the file the branch does not carry")
	})

	// THE SECOND HALF OF THE DELETION FENCE, and the only subtest that fails against
	// a seed which inherits any unanimous value it finds.
	//
	// The language-set fold in discoverySignature normally fences an empty walk on
	// its own: a zero-file branch collapses to the "<fingerprint>|" shape, differs
	// from any prior carrying languages, and the discovery trigger fires before a
	// deletion set is computed. It CANNOT fence a prior that is itself
	// language-less — the two values agree, the collect reaches diffModeOn, and
	// MEASURED against an unguarded seed this exact fixture put DeletedFiles
	// ["." "pkg" "pkg/a.go" "pkg/b.go"] on the Finalize, naming the repo root. The
	// guard refuses to inherit a language-less discovery value at all, which is why
	// the discovery trigger fires here instead.
	t.Run("language_less_unanimous_sibling_is_not_seeded", func(t *testing.T) {
		// The server describes both files, as a clone of the base would.
		manifest := manifestMatching(twoFileResult())
		require.Len(t, manifest.Entries, 2,
			"control: the manifest must name files the diff could delete, or an empty "+
				"DeletedFiles assertion below passes for the wrong reason")

		sibling := twoFileResult()
		sibling.Nodes, sibling.Edges = nil, nil
		result := onBranch(twoFileResult(), firstTouchBranch)
		result.Nodes, result.Edges = nil, nil

		rec, h := runBranchCollect(t, result, manifest, func() {
			seedCollectBaselines(sibling)
		})

		// The COLLECTOR key still seeds — sibling and branch agree at the same
		// version — so the trigger that fires can only be the discovery one, which
		// is what pins the refusal to the language-less DISCOVERY value.
		requireFallback(t, h, fallbackDiscoveryModeChange)
		fin := rec.finalizeRequest(t)
		require.False(t, fin.GetDiffMode(), "the refusal must degrade the collect off the diff lane")
		require.Empty(t, fin.GetDeletedFiles(),
			"an empty walk must name NO deletions — this is the assertion the unguarded seed failed")
	})

	// DISCRIMINATES A BARE-PREFIX SIBLING SCAN, which every other subtest passes
	// because their fixtures involve a single graph. The decoy is a DIFFERENT graph
	// whose name merely extends this one's, recorded at a different collector
	// version: a scan matching on prefix+"@" excludes it and the sibling set stays
	// unanimous, while a bare-prefix scan admits it, sees two values and falls back.
	t.Run("sibling_match_requires_the_at_separator", func(t *testing.T) {
		result := onBranch(twoFileResult(), firstTouchBranch)
		rec, h := runBranchCollect(t, result, manifestMatching(result), func() {
			seedSiblingAt("main", result.GraphName, testCollectorOutputVersion)
			seedSiblingAt("main", decoyGraphName, testCollectorOutputVersion+1)
		})

		requireNoFallback(t, h)
		require.Equal(t, "manifest-matching", rec.finalizeRequest(t).GetManifestId(),
			"the decoy graph's record must not reach this graph's sibling set")

		// PREMISE CONTROL: the decoy is genuinely recorded and genuinely disagrees.
		// A decoy that was never written, or was written at the same version as the
		// true sibling, would leave the sibling set unanimous under BOTH predicates
		// and the subtest would pass without discriminating anything.
		decoy := twoFileResult()
		decoy.GraphName = decoyGraphName
		differs, err := defaultDiscoveryStore.changed(
			collectorVersionKey(decoy), strconv.Itoa(testCollectorOutputVersion))
		require.NoError(t, err)
		require.True(t, differs,
			"control: the decoy must hold a DIFFERENT collector version, or a bare-prefix scan would still read unanimous")
	})
}
