// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"

	"connectrpc.com/connect"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// manifest_shadow.go — the shadow-mode divergence split (its class vocabulary and
// its emitter included), the upload decision, and the manifest fetch. Split out of
// manifest.go, which keeps the vocabulary (modes, the fail-closed trigger table,
// the graph-family gate) and the diff arithmetic itself, so that file stays under
// the repo's file-length cap.

// shadowDivergences splits a diff into the three classes. discovered_only is
// suppressed for an EMPTY manifest, because a first collect discovering
// everything is not a divergence.
func shadowDivergences(d collectDiff) map[divergenceClass][]string {
	out := map[divergenceClass][]string{}
	for path, h := range d.presentFiles {
		prior, inManifest := d.manifestFiles[path]
		switch {
		case inManifest && prior != h:
			out[divergenceHashMismatch] = append(out[divergenceHashMismatch], path)
		case !inManifest && len(d.manifestFiles) > 0:
			out[divergenceDiscoveredOnly] = append(out[divergenceDiscoveredOnly], path)
		}
	}
	for path := range d.manifestFiles {
		if _, ok := d.presentFiles[path]; !ok {
			out[divergenceManifestOnly] = append(out[divergenceManifestOnly], path)
		}
	}
	for _, paths := range out {
		sort.Strings(paths)
	}
	return out
}

// collectDiffOutcome carries what applyCollectDiff LEARNS that its return tuple
// has no room for. It is an out-parameter rather than two more return values
// because the tuple states the collect's DECISION — the resolved mode and the
// upload plan — while these two are side facts of the same evaluation: which
// baselines this collect will owe once it succeeds, and whether the server's
// decline must be disabled for it.
type collectDiffOutcome struct {
	// baselines is what to record once the finalize tail reports DONE, captured at
	// compare time. NEVER recomputed at the commit point: the discovery signature
	// reads result.Nodes, which the diff filter narrows before the upload.
	baselines []baselineCommit
	// suppressManifestEcho withholds the served manifest identity from both the
	// chunks and the Finalize, so the server declines nothing and every uploaded
	// file genuinely re-lands. Set ONLY by fallbackCollectorVersionChange.
	suppressManifestEcho bool
}

// uploadDecision is what the rollout mode and the diff jointly decide. It is a
// value rather than a branch so a test can assert the decision directly.
type uploadDecision struct {
	// uploadAll is true when every file goes on the wire regardless of the diff.
	uploadAll bool
	// changed is the file set a diff upload sends; empty when uploadAll.
	changed []string
	// deletions is what rides the deletion carrier; ALWAYS EMPTY unless the diff
	// governs.
	deletions []string
	// keepFileless is false only when diff mode is ON and the fileless payload's
	// signature matches the last DONE-confirmed upload.
	keepFileless bool
}

// decideUpload maps the mode onto an upload plan.
//
// SHADOW COMPUTES EVERYTHING AND SENDS NOTHING: it uploads the full set exactly
// as today and withholds the deletion set entirely, so the DEGRADATION LANE
// can prove diff==full on real data before any destructive path arms.
func decideUpload(mode diffMode, d collectDiff, deletions []string, filelessChanged bool) uploadDecision {
	if mode != diffModeOn {
		return uploadDecision{uploadAll: true, keepFileless: true}
	}
	return uploadDecision{changed: d.changedFiles, deletions: deletions, keepFileless: filelessChanged}
}

// applyCollectDiff runs the client half of one collect: evaluate the fail-closed
// table, and in shadow mode log every divergence class loudly. It changes nothing
// about what is uploaded — the caller's decideUpload does that.
//
// IT RETURNS THE COLLECT'S RESOLVED MODE, which is the lever's value only when no
// fail-closed trigger fired, and the returned value is what the wire's diff_mode
// flag is stamped from. Returning it rather than re-reading the lever at the call
// site is the point — the lever says what was ASKED FOR and this says what the
// collect RESOLVED TO, and a fallback is exactly where the two differ.
//
// THE LEVER IS RESOLVED BY THE CALLER AND CARRIED IN, never re-read here. It is
// resolved ONCE at the top of WriteResult so an unrecognized value errors before
// any hash pass or RPC; resolving a second time here would read the environment
// twice for one collect and reopen the possibility of the two reads disagreeing.
func (s *UploadSink) applyCollectDiff(
	result *collectorwire.CollectResult, mode diffMode, lever diffLever,
	present map[string][32]byte, resp *knowledgev1.CollectManifestResponse,
	outcome *collectDiffOutcome,
) (diffMode, uploadDecision, error) {
	// THE SEED RUNS FIRST, BEFORE ANY COMPARISON READS THE STORE. A branch that has
	// never been collected on this machine holds none of the three baselines, and
	// absence reads as "changed" for both trigger rows — so that branch's FIRST
	// touch degrades to a whole-repo upload for a delta of a few lines. Resolving
	// the absent keys from the same graph's unanimous siblings has to happen ahead
	// of the changed() calls below, because it is those reads whose answer it
	// changes. It resolves NOTHING when the siblings are absent or disagree, so
	// every fail-closed arm keeps today's meaning.
	//
	// ITS FAILURE ABORTS THE COLLECT for the same reason the comparisons' does: it
	// reads and writes through the store's own primitives, so a corrupt or
	// unwritable store surfaces here rather than being read as an empty one.
	if err := defaultDiscoveryStore.seedBranchBaselinesFromSiblings(
		result.CurrentBranch,
		discoveryKey(result), collectorVersionKey(result), filelessKey(result),
	); err != nil {
		return "", uploadDecision{}, err
	}
	// THE STORE'S FAILURE IS NOT A DEGRADE. Defaulting the boolean here — reading a
	// store error as "changed" and taking the rebuild lane — is exactly the lane the
	// error exists to replace: the store stays broken, so it would fire on every
	// collect forever. The error leaves this function.
	//
	// THE SIGNATURES ARE CAPTURED HERE AND COMMITTED LATER. changed() records
	// nothing; the pair rides outcome.baselines to the post-Finalize commit point,
	// so a collect whose upload or tail fails leaves the baseline unadvanced and
	// the next collect legitimately re-fires the trigger.
	discoverySig := discoverySignature(result)
	discoveryChanged, err := defaultDiscoveryStore.changed(discoveryKey(result), discoverySig)
	if err != nil {
		return "", uploadDecision{}, err
	}
	// A ZERO VERSION IS OUR OWN PRODUCER REGRESSING, which no full collect
	// repairs — the same class as the empty discovery fingerprint sink.go refuses
	// before the fetch, and refused the same way rather than read as "unchanged",
	// which would silently disable this mechanism for that collector.
	//
	// IT SITS AFTER THE FETCH WHILE ITS SIBLING SITS BEFORE IT, and the asymmetry
	// is worth a line because the sibling's own comment states the opposite
	// principle ("must cost no round trip"). This refusal is UNREACHABLE ahead of
	// that one: every producer that reaches here stamps both fields or neither, so
	// a result that would trip this one trips the fingerprint refusal first and
	// never gets a manifest fetched. Moving it earlier would buy nothing and would
	// split one producer-regression check across two frames.
	if result.CollectorOutputVersion == 0 {
		return "", uploadDecision{}, fmt.Errorf(
			"remote sink: unstamped collector output version on a code collect: " +
				"the collector did not stamp CollectResult.CollectorOutputVersion")
	}
	collectorSig := strconv.FormatUint(uint64(result.CollectorOutputVersion), 10)
	collectorChanged, err := defaultDiscoveryStore.changed(collectorVersionKey(result), collectorSig)
	if err != nil {
		return "", uploadDecision{}, err
	}
	// THE FILELESS SIGNATURE IS THE THIRD BASELINE, and it is computed HERE rather
	// than at the commit point because narrowAndGroupRows REASSIGNS result.Nodes to
	// the filtered subset before commitCollectBaselines runs. Digesting the narrowed
	// set would record a value the next collect can never match — the identical trap
	// baselineCommit's own doc records for discoverySignature.
	filelessSig := filelessSignature(result)
	filelessChanged, ferr := defaultDiscoveryStore.changed(filelessKey(result), filelessSig)
	if ferr != nil {
		return "", uploadDecision{}, ferr
	}
	outcome.baselines = []baselineCommit{
		{key: discoveryKey(result), sig: discoverySig},
		{key: collectorVersionKey(result), sig: collectorSig},
		{key: filelessKey(result), sig: filelessSig},
	}
	if reason, fell := evaluateManifestFallback(manifestState{
		mode:                    mode,
		lever:                   lever,
		resp:                    resp,
		discoveryChanged:        discoveryChanged,
		collectorVersionChanged: collectorChanged,
	}); fell {
		// SUPPRESSION IS SCOPED TO THE NEW TRIGGER ALONE. The other three degrade to
		// a full UPLOAD, which the server still declines file by file — correct for
		// them, because their rows genuinely match what it holds. This one fires
		// precisely because the rows DIFFER in ways no hash comparison can see, so
		// the decline must be disabled or the forced upload accomplishes nothing.
		if reason == fallbackCollectorVersionChange {
			outcome.suppressManifestEcho = true
		}
		logManifestFallback(reason, result.GraphName, result.CurrentBranch)
		// THREE TRIGGERS DEGRADE TO A FULL COLLECT; THE KILL SWITCH DEGRADES TO
		// SHADOW. Both upload everything and send no deletions, so the safety is
		// identical — what differs is the diagnostic. The kill switch is the only
		// trigger a HUMAN fires, and shadow keeps computing the diff and falls
		// THROUGH to the divergence emitter below, so the operator who reached for
		// the break-glass lever sees what the diff WOULD have done instead of
		// silence. The other three are fired by conditions and return here.
		mode = diffModeOff
		if reason == fallbackKillSwitch {
			mode = diffModeShadow
		}
	}
	d := computeCollectDiff(resp, present)
	decision := decideUpload(mode, d, deletionSet(d.manifestFiles, d.changedFiles, d.unchangedFiles), filelessChanged)
	if mode != diffModeShadow {
		return mode, decision, nil
	}
	for class, paths := range shadowDivergences(d) {
		logShadowDivergence(class, paths)
	}
	return mode, decision, nil
}

// fetchManifest asks the server for its per-file contribution hashes.
//
// It is a NAMED METHOD rather than an inlined RPC call because an ORDERING claim
// needs a symbol to anchor on: the graph-family gate must be consulted BEFORE
// this runs, and there is no way to state that about an inlined call.
//
// THE CLIENT IS RESOLVED THROUGH THE PER-CALL PICKER, like every other call on
// this sink, so a mid-session login flip re-routes the fetch; it must not cache a
// resolved client.
func (s *UploadSink) fetchManifest(
	ctx context.Context, result *collectorwire.CollectResult,
) (*knowledgev1.CollectManifestResponse, error) {
	client, err := s.picker(ctx)
	if err != nil {
		return nil, err
	}
	return fetchManifestWith(ctx, client, result)
}

// fetchManifestWith is the picker-free half, so a test can drive the request
// shape against a stub client without standing up a picker.
func fetchManifestWith(
	ctx context.Context, client knowledgev1connect.IngestServiceClient, result *collectorwire.CollectResult,
) (*knowledgev1.CollectManifestResponse, error) {
	resp, err := client.CollectManifest(ctx, connect.NewRequest(&knowledgev1.CollectManifestRequest{
		GraphType:     string(result.GraphType),
		GraphName:     result.GraphName,
		CurrentBranch: result.CurrentBranch,
		Promote:       result.Promote,
	}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// filterToChangedFiles keeps only the nodes and edges the diff says must go on
// the wire: the changed files' rows, plus every FILELESS node when keepFileless
// says so.
//
// FILELESS HAS NO PER-FILE DECLINE BASIS. A node belonging to no file — the
// hierarchy package nodes, the repo root, the language hub — is outside the
// manifest by construction, so it is never diffed and nothing there would ever
// mark it changed. keepFileless carries the caller's WHOLE-SET answer instead: it
// is false only when diff mode is ON and the fileless payload's signature matches
// the last DONE-confirmed upload, and true in every other lane.
//
// THE SET IS ALL-OR-NOTHING, and it must be. Uploading fileless NODES without
// their EDGES is the node-present/edge-absent shape that makes the server's
// residual reclaim delete that source's ENTIRE outbound set — every package hub's
// CONTAINS edges. That safety is STRUCTURAL here rather than a convention: edges
// ride on keptIDs[e.FromID], so a dropped fileless node takes its edges with it.
// Do NOT add a second edge predicate.
//
// EDGES FOLLOW THEIR FROM NODE, never their reference site. The owning file of
// an edge is the file_path of its FROM node, so an edge whose source survives
// the filter rides with it; one whose source was filtered out would have landed
// on the wrong file's upload and left its true owner's stale edge uncleared.
// nodeHashes is the per-row digest array index-aligned with nodes, narrowed by
// the SAME predicate and returned alongside the kept nodes. It travels through
// this filter rather than being recomputed after it because the wire contract
// for node_contribution_hashes is index alignment with the chunked node slice —
// re-deriving the surviving subset from a second copy of this predicate is the
// drift this parameter exists to prevent. A nil array narrows to nil, so callers
// with no digests to carry are unaffected.
func filterToChangedFiles(
	nodes []*knowledgev1.Node, nodeHashes [][32]byte, edges []kgwire.BatchEdge, changed []string, keepFileless bool,
) (
	[]*knowledgev1.Node, [][32]byte, []kgwire.BatchEdge, error,
) {
	// A MISALIGNED DIGEST ARRAY IS AN ERROR, NEVER A TRUNCATION. Narrowing to
	// whichever array is shorter would hand the chunker digests belonging to other
	// nodes, and the server would then decline files against hashes that were
	// never theirs. Absent (nil) is the one legitimate non-matching length.
	if nodeHashes != nil && len(nodeHashes) != len(nodes) {
		return nil, nil, nil, fmt.Errorf(
			"remote sink: diff filter: %d per-row node digests for %d nodes — the array is index-aligned "+
				"with the node slice by contract, so a differing length means the two came from different passes",
			len(nodeHashes), len(nodes))
	}
	keep := make(map[string]bool, len(changed))
	for _, p := range changed {
		keep[p] = true
	}
	keptIDs := make(map[string]bool, len(nodes))
	outNodes := make([]*knowledgev1.Node, 0, len(nodes))
	var outHashes [][32]byte
	// Re-slicing to len(nodes) after the equality check above ties the digest
	// array's length to the node loop's bound in the code itself, rather than only
	// in the guard, so indexing it by the node index is locally provable.
	var alignedHashes [][32]byte
	if nodeHashes != nil {
		alignedHashes = nodeHashes[:len(nodes):len(nodes)]
		outHashes = make([][32]byte, 0, len(nodes))
	}
	for i, n := range nodes {
		path := n.GetFilePath()
		if path == "" && !keepFileless {
			continue
		}
		if path != "" && !keep[path] {
			continue
		}
		keptIDs[n.GetId()] = true
		outNodes = append(outNodes, n)
		if alignedHashes != nil {
			outHashes = append(outHashes, alignedHashes[i])
		}
	}
	outEdges := make([]kgwire.BatchEdge, 0, len(edges))
	for i, e := range edges {
		// AN EDGE THIS FILTER CANNOT PLACE IS AN ERROR, NEVER A SILENT DROP.
		// Placement resolves an edge's owning file through its FROM NODE ID, so an
		// INDEX-ADDRESSED edge — FromIdx/ToIdx pointing into the node slice, with no
		// FromID — has nothing to resolve and would simply vanish here: no error, no
		// log, one lost link. Collector edges are ID-addressed by contract
		// (parser.ToBatchEdges always emits -1/-1 with both IDs, pinned by
		// TestToBatchEdges_AlwaysIDAddressed), so reaching this arm means that
		// contract broke upstream. Dropping information is not an available
		// response to that.
		if e.FromID == "" {
			return nil, nil, nil, fmt.Errorf(
				"remote sink: diff filter: edge %d of %d is INDEX-ADDRESSED (FromIdx=%d, ToIdx=%d, Type=%q, ToID=%q) "+
					"and carries no FromID, so its owning file cannot be resolved and the diff would drop it silently; "+
					"collector edges must be ID-ADDRESSED (FromIdx and ToIdx == -1, with FromID and ToID both set)",
				i+1, len(edges), e.FromIdx, e.ToIdx, e.Type, e.ToID)
		}
		if keptIDs[e.FromID] {
			outEdges = append(outEdges, e)
		}
	}
	return outNodes, outHashes, outEdges, nil
}

// narrowAndGroupRows applies the diff filter and then the file grouping to a
// collect result in place, returning the per-row node digests that match
// result.Nodes afterwards.
//
// THE TWO STEPS SHARE ONE HELPER BECAUSE THEY ARE ONE OBLIGATION: each transforms
// the node slice — one narrows it, the other permutes it — and the per-row digest
// array must undergo the SAME transformation, because the chunk builder slices
// that array by position. A caller that ran one of them without carrying the
// digests through would store every node under a neighbour's digest, and the
// server's length check cannot see a permutation. Keeping both here means there
// is exactly one place where the pair can drift apart.
//
// THE GROUPING RUNS UNCONDITIONALLY, the filter only under a narrowed decision:
// the chunker packs whole files on every collect, diff or full, so the digests
// must be in file-grouped order on every collect too.
//
// Split out of WriteResult so that function stays inside the package's length
// ceiling, the same reason planDiffUpload and uploadChunks are separate.
func narrowAndGroupRows(
	result *collectorwire.CollectResult, nodeHashes [][32]byte, decision uploadDecision,
) ([][32]byte, error) {
	if !decision.uploadAll {
		keptNodes, keptHashes, keptEdges, fErr := filterToChangedFiles(
			result.Nodes, nodeHashes, result.Edges, decision.changed, decision.keepFileless)
		if fErr != nil {
			return nil, fErr
		}
		result.Nodes, result.Edges, nodeHashes = keptNodes, keptEdges, keptHashes
	}
	groupedNodes, groupedHashes, gErr := groupNodesAndHashesByFile(result.Nodes, nodeHashes)
	if gErr != nil {
		return nil, gErr
	}
	result.Nodes = groupedNodes
	return groupedHashes, nil
}

// divergenceClass names one way a shadow-mode run can disagree with the server.
// The three fail DIFFERENTLY, which is why they are logged separately.
type divergenceClass string

const (
	// divergenceHashMismatch — the parity-bug class: a file present on both sides
	// whose hashes differ.
	divergenceHashMismatch divergenceClass = "hash_mismatch"
	// divergenceManifestOnly — the exclusion-predicate disagreement class: a
	// manifest file the client never discovered.
	divergenceManifestOnly divergenceClass = "manifest_only"
	// divergenceDiscoveredOnly — the server-render class: a discovered file absent
	// from a WARM graph's manifest.
	divergenceDiscoveredOnly divergenceClass = "discovered_only"
)

// shadowDivergenceSampleMax bounds the paths one divergence line carries.
const shadowDivergenceSampleMax = 5

// logShadowDivergence emits ONE Error line for one divergence class.
//
// ERROR RATHER THAN WARN IS DELIBERATE, and matches the repo's precedent for a
// signal that must not be trained away: a divergence here means the contribution
// hash is wrong somewhere across three implementations.
//
// THE MESSAGE IS DISTINCT PER CLASS rather than one generic message with a class
// field — a generic message makes the three indistinguishable to the operator the
// logging exists for.
func logShadowDivergence(class divergenceClass, paths []string) {
	if len(paths) == 0 {
		return
	}
	sample := paths
	if len(sample) > shadowDivergenceSampleMax {
		sample = sample[:shadowDivergenceSampleMax]
	}
	switch class {
	case divergenceHashMismatch:
		slog.Error("collect diff shadow: contribution hash DISAGREES with the server for files present on both sides",
			"count", len(paths), "sample", sample)
	case divergenceManifestOnly:
		slog.Error("collect diff shadow: the server's manifest names files this collect never discovered",
			"count", len(paths), "sample", sample)
	case divergenceDiscoveredOnly:
		slog.Error("collect diff shadow: this collect discovered files absent from a populated manifest",
			"count", len(paths), "sample", sample)
	}
}
