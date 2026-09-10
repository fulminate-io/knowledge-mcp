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
	for key, h := range d.presentKeys {
		prior, inManifest := d.manifestKeys[key]
		switch {
		case inManifest && prior != h:
			out[divergenceHashMismatch] = append(out[divergenceHashMismatch], key)
		case !inManifest && len(d.manifestKeys) > 0:
			out[divergenceDiscoveredOnly] = append(out[divergenceDiscoveredOnly], key)
		}
	}
	for key := range d.manifestKeys {
		if _, ok := d.presentKeys[key]; !ok {
			out[divergenceManifestOnly] = append(out[divergenceManifestOnly], key)
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
	// key genuinely re-lands. Set for EVERY collect whose plan is uploadAll — see
	// fallbackReason's doc for why that is the right key and what echoing on a
	// full upload destroyed.
	suppressManifestEcho bool
}

// uploadDecision is what the rollout mode and the diff jointly decide. It is a
// value rather than a branch so a test can assert the decision directly.
type uploadDecision struct {
	// kind is the unit `changed` and `deletions` are keyed on. It rides the
	// decision rather than being re-derived at each consumer because the upload
	// filter, the chunk's echoed entries and the Finalize carrier all have to
	// agree with the comparison that produced these two lists — and a consumer
	// that re-derived the family gate for itself is a second place for the answer
	// to be decided.
	kind diffKeyKind
	// uploadAll is true when every row goes on the wire regardless of the diff.
	uploadAll bool
	// changed is the key set a diff upload sends; empty when uploadAll.
	changed []string
	// deletions is what rides the deletion carrier; ALWAYS EMPTY unless the diff
	// governs. kind decides WHICH carrier: deleted_files for the file key,
	// deleted_node_ids for the node key.
	deletions []string
	// keepFileless is false only when a FILE-KEYED diff is ON and the fileless
	// payload's signature matches the last DONE-confirmed upload. A node-keyed
	// collect has no fileless class at all — every node is its own key — so this
	// stays true there and the filter's fileless arm is unreachable.
	keepFileless bool
}

// decideUpload maps the mode onto an upload plan.
//
// SHADOW COMPUTES EVERYTHING AND SENDS NOTHING: it uploads the full set exactly
// as today and withholds the deletion set entirely, so the DEGRADATION LANE
// can prove diff==full on real data before any destructive path arms.
//
// "SENDS NOTHING" IS ABOUT THE DELETION SET AND NOT ABOUT THE ROWS. uploadAll
// means every row goes on the wire, and the caller withholds the manifest echo
// for exactly that reason, so those rows LAND rather than being declined key by
// key. Reading this line as "shadow changes nothing on the server" is the
// mistake that let a full upload be declined wholesale while its unchanged keys
// were then derived as deletions.
func decideUpload(mode diffMode, d collectDiff, deletions []string, filelessChanged bool) uploadDecision {
	if mode != diffModeOn {
		return uploadDecision{kind: d.kind, uploadAll: true, keepFileless: true}
	}
	return uploadDecision{
		kind: d.kind, changed: d.changedKeys, deletions: deletions, keepFileless: filelessChanged,
	}
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
	kind diffKeyKind, outcome *collectDiffOutcome,
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
			"remote sink: unstamped collector output version on a %s collect of graph %q: "+
				"the collector did not stamp CollectResult.CollectorOutputVersion",
			result.GraphType, result.GraphName)
	}
	collectorSig := strconv.FormatUint(uint64(result.CollectorOutputVersion), 10)
	collectorChanged, err := defaultDiscoveryStore.changed(collectorVersionKey(result), collectorSig)
	if err != nil {
		return "", uploadDecision{}, err
	}
	outcome.baselines = []baselineCommit{
		{key: discoveryKey(result), sig: discoverySig},
		{key: collectorVersionKey(result), sig: collectorSig},
	}
	// THE FILELESS SIGNATURE IS THE THIRD BASELINE OF A FILE-KEYED COLLECT, and it
	// is computed HERE rather than at the commit point because narrowAndGroupRows
	// REASSIGNS result.Nodes to the filtered subset before commitCollectBaselines
	// runs. Digesting the narrowed set would record a value the next collect can
	// never match — the identical trap baselineCommit's own doc records for
	// discoverySignature.
	//
	// A NODE-KEYED COLLECT HAS NO FILELESS CLASS AND TAKES NO SUCH BASELINE. The
	// fileless set exists because a node belonging to no FILE is outside a
	// file-keyed manifest by construction and nothing can ever mark it changed;
	// under the node key every node IS a key, so the class is empty and the digest
	// would cover the WHOLE graph. Taking it anyway would be actively harmful: one
	// changed node moves the whole-set digest, keepFileless flips, and the arm that
	// exists to spare an undiffable set would re-upload every node of a graph the
	// diff had just narrowed to one.
	filelessChanged := true
	if kind == diffKeyFile {
		filelessSig := filelessSignature(result)
		changed, ferr := defaultDiscoveryStore.changed(filelessKey(result), filelessSig)
		if ferr != nil {
			return "", uploadDecision{}, ferr
		}
		filelessChanged = changed
		outcome.baselines = append(outcome.baselines, baselineCommit{key: filelessKey(result), sig: filelessSig})
	}
	if reason, fell := evaluateManifestFallback(manifestState{
		mode:                    mode,
		lever:                   lever,
		resp:                    resp,
		discoveryChanged:        discoveryChanged,
		collectorVersionChanged: collectorChanged,
	}); fell {
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
	d := computeCollectDiff(resp, present, kind)
	decision := decideUpload(
		mode, d, deletionSet(d.manifestKeys, d.changedKeys, d.unchangedKeys, kind), filelessChanged)
	// THE ECHO IS THE DIFF'S CREDENTIAL, so it is withheld from every collect that
	// is NOT running the diff. Keying it on the upload plan rather than on the
	// trigger that produced the plan is deliberate: the plan is the fact the
	// server's decline actually interacts with, and it is reached with no trigger
	// at all by a deliberate shadow lever. See fallbackReason's doc for the rule
	// and for the data loss the trigger-scoped form shipped.
	//
	// IT IS SET AFTER decideUpload AND NOT INSIDE THE FALLBACK ARM, which is the
	// ordering that makes the sentence above checkable: there is exactly one
	// producer of uploadAll, and this line reads it rather than re-deriving the
	// conditions that set it.
	outcome.suppressManifestEcho = decision.uploadAll
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
