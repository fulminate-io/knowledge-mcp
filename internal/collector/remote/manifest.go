// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"fmt"
	"log/slog"
	"os"
	"path"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/contribhash"
)

// manifest.go — the client half of incremental collect: the mode vocabulary, the
// fail-closed trigger table, the diff-eligibility gate and the diff arithmetic.
// The manifest fetch, the upload decision, and the shadow-mode divergence split
// — its class vocabulary and its emitter included — live beside it in
// manifest_shadow.go.
//
// THE DIFF IS AN OPTIMIZATION WITH A PROVABLE FALLBACK, NEVER A CORRECTNESS
// DEPENDENCY. Every trigger below degrades the collect to a FULL collect — it
// never degrades to a partial deletion.
//
// WHAT MAKES THAT SECOND SENTENCE TRUE IS THE WITHHELD MANIFEST ECHO — see
// fallbackReason. Until the echo was keyed on the upload plan it was a claim
// about this file's intent rather than a property of the code.

// diffMode is what a collect RESOLVES TO, declared ONCE here.
//
//	diffModeOff    — no diff at all: the whole graph uploads and no manifest is
//	                 fetched. Reached only by a graph family outside the gate.
//	diffModeShadow — compute the diff, still upload FULL, log any divergence
//	                 loudly; sends no deletions. This is the DEGRADATION LANE.
//	diffModeOn     — the diff governs the upload. This is the normal lane.
//
// THERE IS ONE PATH — THE DIFF — WITH A DEGRADATION LANE the conditions select.
// The lever below exists so a human can force the safe lane during an incident,
// not so two modes are maintained.
type diffMode string

const (
	diffModeOff    diffMode = "off"
	diffModeShadow diffMode = "shadow"
	diffModeOn     diffMode = "on"
)

// diffLever is what the operator ASKED FOR, which is a different question from
// what the collect resolved to and must not be folded into it.
//
// THE MODE IS LOSSY AND THAT IS WHY THIS TYPE EXISTS. Two distinct lever states
// — a deliberate shadow request and the kill switch — resolve to diffModeShadow,
// so the mode value alone cannot answer "why". The kill-switch trigger keys on
// the LEVER: a predicate that read the mode would fire for a deliberate shadow
// request too, and could not report the one trigger a HUMAN pulls.
type diffLever string

const (
	leverUnset  diffLever = "unset"
	leverEmpty  diffLever = "empty"
	leverOn     diffLever = "on"
	leverShadow diffLever = "shadow"
	leverOff    diffLever = "off"
)

// THERE IS NO DELETION-RATIO LEVER ANY MORE. A second environment variable
// (KNOWLEDGE_COLLECT_DELETION_RATIO_OVERRIDE) once opted a collect out of the
// server's size-based deletion refusal, for repositories whose ordinary churn the
// bound refused. The BOUND is gone — it refused a deleted directory's rows
// silently and permanently — so the opt-out went with it, along with the wire
// field it rode (FinalizeRequest tag 11, now reserved). Nothing about the diff
// lever below changed.

// collectDiffEnv is the break-glass lever: it forces the degradation lane.
const collectDiffEnv = "KNOWLEDGE_COLLECT_DIFF"

// collectDiffMode classifies the lever and resolves the collect's mode. FIVE
// lever states, FIVE lever values — no state without a name, because a taxonomy
// with an unnamed state cannot report that state.
//
//	unset (the normal case)   leverUnset    diffModeOn     // ARMED
//	set but EMPTY             leverEmpty    diffModeOn
//	"on"                      leverOn       diffModeOn
//	"shadow"                  leverShadow   diffModeShadow // deliberate
//	"off"                     leverOff      diffModeShadow // KILL SWITCH
//	anything else             —             ERROR, the collect does not run
//
// UNSET IS ARMED. That is the shipped behavior: the diff is the collect, not an
// opt-in.
//
// A VALUE THAT IS PRESENT AND MEANINGLESS ERRORS, and "unrecognized" is
// therefore a state this resolver REFUSES rather than one it resolves to — the
// error IS the report, so the taxonomy needs no name for it and the error path
// returns ZERO VALUES for both mode and lever, so a caller that drops the error
// cannot proceed on a plausible-looking pair. The error names the raw value and
// the valid vocabulary, because "it errored" alone leaves the operator exactly
// as stuck as a silent degrade did.
//
// ABSENCE OF INPUT IS NOT BAD INPUT. Unset means "no override of the default",
// and an explicitly cleared value is the operator saying the same thing
// deliberately; only a present, meaningless value is refused. The kill switch
// stays VALID — "off" is a deliberate operator choice that resolves to shadow.
//
// WHY os.LookupEnv IS REQUIRED, stated accurately. NOT because unset and
// set-empty resolve to different modes — they resolve to the SAME mode. The
// necessity is on the LEVER side: os.Getenv returns "" for both and cannot tell
// leverUnset from leverEmpty, and the kill-switch predicate keys on the lever. A
// resolver that cannot distinguish "no override was set" from "an override was
// set and then cleared" reports the wrong thing to the consumer that asks.
//
// AND THERE IS A BLUNTER HAZARD IT AVOIDS. With os.Getenv, an unset variable
// reaches the same default arm as a typo — so wiring "unrecognized -> shadow"
// through a default arm would send EVERY NORMAL DEPLOYMENT into shadow with
// every structural gate still green. LookupEnv is what keeps unset out of the
// garbage arm.
//
// SET-BUT-EMPTY RESOLVES ARMED, deliberately. `KNOWLEDGE_COLLECT_DIFF=` in a
// unit file is the shape of "I cleared the override", not "I typed garbage";
// reading it as unrecognized would send an operator who cleared the variable
// into shadow by a different route. It keeps its own lever value rather than
// being folded into leverUnset, per the six-states-six-values rule above.
func collectDiffMode() (diffMode, diffLever, error) {
	raw, ok := os.LookupEnv(collectDiffEnv)
	if !ok {
		return diffModeOn, leverUnset, nil
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return diffModeOn, leverEmpty, nil
	case string(diffModeOn):
		return diffModeOn, leverOn, nil
	case string(diffModeShadow):
		return diffModeShadow, leverShadow, nil
	case string(diffModeOff):
		return diffModeShadow, leverOff, nil
	default:
		return "", "", fmt.Errorf(
			"collect diff: unrecognized %s value %q; valid values are %q, %q, %q, or empty/unset",
			collectDiffEnv, raw, diffModeOn, diffModeShadow, diffModeOff)
	}
}

// fallbackReason names one fail-closed trigger. The four are the settled set;
// each degrades the current collect to a full collect.
//
// A CONDITION A FULL COLLECT CANNOT REPAIR IS NOT IN HERE. The rebuild lane is
// justified only where the upload actually fixes the server state and the NEXT
// collect returns to the diff; where it does not converge, the lane would pay
// O(repo) forever and repair nothing, so the condition is a loud error raised in
// WriteResult before any upload rather than a name in this vocabulary. A
// surviving constant for such a condition would be an affordance inviting a
// future worker to wire the lane back on.
//
// EVERY TRIGGER HERE SUPPRESSES THE MANIFEST ECHO, and that is what makes a
// fallback a re-land rather than a word. The statement was written on the
// collector-version trigger alone, where it read as a property of that one
// condition; it is a property of the FULL UPLOAD, so it is stated once, here.
// The server's decline is not keyed on diff mode (store.DeclinedFilesForChunk,
// collect_decline.go): it declines any key whose echoed manifest identity and
// per-key hash match the server's, so a trigger that only forced uploadAll would
// produce a full upload the server declines key by key, and NOTHING would
// re-land.
//
// THE COST OF GETTING IT WRONG IS NOT A WASTED UPLOAD — IT IS DATA. A fallback
// sends NO deletion carrier, so the server derives its deletion set from the
// presence rails, and a DECLINED key is accounted for only by the declined rail.
// Where that rail's coverage does not reach a declined row, the row reads as
// uncarried and is tombstoned by a collect that reported a complete successful
// emission. That shipped: a registered custom collect whose params moved fired
// the discovery trigger, echoed the identity, had every stable-id row declined,
// and lost them. The echo is the DIFF'S credential; a collect that is not
// running the diff does not present it.
//
// SO THE SUPPRESSION IS KEYED ON THE UPLOAD PLAN, NOT ON THIS TABLE.
// applyCollectDiff sets it from decision.uploadAll, which also covers the lane
// that reaches a full upload with NO trigger at all — a deliberate
// KNOWLEDGE_COLLECT_DIFF=shadow, for which evaluateManifestFallback returns
// false.
type fallbackReason string

const (
	// fallbackSchemeMismatch: the server's hash scheme differs from this client's.
	// This is ALSO the delivery vehicle for the Summary/Keywords coupling
	// obligation in docs/collect-contribution-hash.md section C: a bump there
	// lands here as an ordinary mismatch and costs exactly one full collect.
	fallbackSchemeMismatch fallbackReason = "scheme_mismatch"
	// fallbackDiscoveryModeChange: this collect's discovery signature — the
	// carried configuration fingerprint folded with the language set — differs
	// from the one recorded for this graph at the previous collect.
	//
	// A changed discovery surface means a file's absence is explained by
	// discovery rather than by deletion. THIS IS THE ONE TRIGGER GUARDING A PATH
	// WHERE A LEGITIMATE USER ACTION WOULD OTHERWISE DESTROY DATA: a collect
	// scoped by package prefixes emits nothing for the out-of-scope files, and
	// every other guard admits the resulting deletion set — the walk was complete
	// (nothing was unreadable), the ratio is ordinary for a subtree, and each
	// named path really does have a live collector-owned node.
	//
	// AN EMPTY FINGERPRINT NO LONGER TRIPS IT. That disjunct named OUR OWN
	// producer regressing, which no full collect repairs, so it is a loud abort
	// raised in WriteResult instead; this trigger keeps only its scope-change
	// branch, where a full collect genuinely is the right answer.
	//
	// IT IS THE TRIGGER THE ECHO RULE WAS WRITTEN FOR AND THEN NOT APPLIED TO. A
	// registered custom collector folds its collect PARAMS into the discovery
	// fingerprint, so an ordinary re-collect under a moved window fires this one
	// and nothing else. The earlier wording held that declining this trigger's
	// keys was CORRECT because their rows match what the server holds; that is
	// what makes the decline FIRE, not a reason to allow it. Declining is correct
	// only for a collect that names its own deletions, and a full upload names
	// none.
	fallbackDiscoveryModeChange fallbackReason = "discovery_mode_change"
	// fallbackKillSwitch: an operator set KNOWLEDGE_COLLECT_DIFF to off. Keyed on
	// the LEVER, never the resolved mode — see diffLever.
	fallbackKillSwitch fallbackReason = "kill_switch"
	// fallbackCollectorVersionChange: this client's collector EMITS different rows
	// than the collector that last collected this graph@branch did.
	//
	// WHAT IT GUARDS is the one class of collector change no per-file diff can see
	// BY CONSTRUCTION: the emitted values outside the per-file contribution hash —
	// node Id, Summary, Keywords and metadata (docs/collect-contribution-hash.md
	// sections A and C). A change to any of them leaves every per-file hash
	// identical, so the server's manifest agrees with the client's rows and the
	// affected files read UNCHANGED forever. An ID migration is the live case.
	//
	// IT WAS THE FIRST TRIGGER TO SUPPRESS THE MANIFEST ECHO, and it was for a
	// while the only one — which is how the rule came to be written here as
	// though it were a property of this condition. It is not; it is a property of
	// the full upload every trigger degrades to, and it now lives on the type
	// above. What is specific to THIS trigger is only why it cannot be served by
	// the per-key hashes: the values it guards sit outside the hash, so the rows
	// read unchanged forever and no comparison this client can make would ever
	// re-land them.
	fallbackCollectorVersionChange fallbackReason = "collector_version_change"
)

// manifestState is everything the trigger table evaluates. It is a plain struct
// so the table can be driven directly by a test rather than only through a live
// RPC.
type manifestState struct {
	mode diffMode
	// lever is what the operator ASKED FOR. The kill-switch trigger keys on THIS,
	// never on mode: two lever states resolve to diffModeShadow, so a mode-keyed
	// predicate would fire for a deliberate shadow request as well as for the kill
	// switch, and could not report the one trigger a HUMAN pulls.
	lever            diffLever
	resp             *knowledgev1.CollectManifestResponse
	discoveryChanged bool
	// collectorVersionChanged is true when this client's CollectorOutputVersion
	// differs from the one recorded for this graph@branch — including the absent
	// record, which is the first collect after an upgrade and is deliberately the
	// repair.
	collectorVersionChanged bool
}

// evaluateManifestFallback returns the trigger that forces a full collect, and
// whether one fired. Order is deliberate: the cheapest and most decisive checks
// first, so a kill-switched or failed collect never reports a subtler reason.
func evaluateManifestFallback(s manifestState) (fallbackReason, bool) {
	if s.lever == leverOff {
		return fallbackKillSwitch, true
	}
	if s.resp.GetHashSchemeVersion() != contribhash.ContributionHashSchemeVersion {
		return fallbackSchemeMismatch, true
	}
	// AFTER the scheme mismatch, which subsumes it — a scheme bump moves every
	// hash, so nothing could decline anyway — and BEFORE the discovery check,
	// which it outranks: this is the only trigger that also suppresses the
	// manifest echo, so where both conditions hold the more decisive one must be
	// the reported reason.
	if s.collectorVersionChanged {
		return fallbackCollectorVersionChange, true
	}
	if s.discoveryChanged {
		return fallbackDiscoveryModeChange, true
	}
	return "", false
}

// logManifestFallback emits the chosen outcome. Extracted into its own function
// on the logClientSideStall precedent, so a test can drive the emission directly
// and a name-grep cannot pass on a discarded call.
//
// A FALLBACK IS NOT AN ERROR: the collect proceeds exactly as it does today, so
// this is Info rather than Warn.
func logManifestFallback(reason fallbackReason, graphName, branch string) {
	slog.Info("collect diff: falling back to a full collect",
		"reason", string(reason), "graph", graphName, "branch", branch)
}

// collectDiff is the client's view of one collect measured against the manifest
// the server served. The four sets are declared once, here, and consumed by name
// everywhere else.
//
//	manifestKeys  — diff keys in the served manifest, with their hashes.
//	presentKeys   — keys this collect DISCOVERED AND SUCCESSFULLY HANDLED, with
//	                their computed per-key hashes.
//	changedKeys   — presentKeys absent from manifestKeys, or present with a
//	                differing hash. These are the ones a diff uploads.
//	unchangedKeys — presentKeys minus changedKeys: verified present,
//	                hash-identical to the manifest, deliberately not uploaded.
//
// A KEY IS A FILE PATH OR A NODE ID, per the family's diffKeyKind, and the
// arithmetic below is identical either way — which is the whole reason it is
// expressed over a key rather than branched per family. `kind` rides along
// because the DELETION arithmetic is NOT identical: only the file key has
// ancestor directories to name.
//
// presentKeys IS NOT DERIVED FROM A DISCOVERY LIST. It is exactly the key set of
// the per-key hashes computed from the CollectResult the sink received, so a
// file that failed to read or parse contributes no node and is therefore not a
// key. That identity is what makes "present" mean "successfully handled" rather
// than "listed by a walk", and the deletion formula below rests on it.
type collectDiff struct {
	kind          diffKeyKind
	manifestKeys  map[string][32]byte
	presentKeys   map[string][32]byte
	changedKeys   []string
	unchangedKeys []string
}

// computeCollectDiff splits the present set against the served manifest.
//
// EVERY ENTRY'S KEY IS TAKEN THROUGH ITS ARM. An entry on the OTHER arm cannot
// reach here — planDiffUpload aborts on a manifest failing
// manifestSelfConsistent, which checks exactly that — so skipping one here is
// defense in depth rather than a second, laxer reading: a mismatched arm that
// somehow arrived would be dropped from the manifest set, which lands its key in
// changedKeys and re-uploads it, the fail-closed direction.
func computeCollectDiff(
	resp *knowledgev1.CollectManifestResponse, present map[string][32]byte, kind diffKeyKind,
) collectDiff {
	d := collectDiff{
		kind:         kind,
		manifestKeys: make(map[string][32]byte, len(resp.GetEntries())),
		presentKeys:  present,
	}
	for _, e := range resp.GetEntries() {
		key, entryKind, ok := manifestEntryKey(e)
		if !ok || entryKind != kind {
			continue
		}
		var h [32]byte
		copy(h[:], e.GetContributionHash())
		d.manifestKeys[key] = h
	}
	for key, h := range present {
		if prior, ok := d.manifestKeys[key]; ok && prior == h {
			d.unchangedKeys = append(d.unchangedKeys, key)
			continue
		}
		d.changedKeys = append(d.changedKeys, key)
	}
	sort.Strings(d.changedKeys)
	sort.Strings(d.unchangedKeys)
	return d
}

// deletionSet names what this collect asserts is gone: the manifest keys this
// collect did not handle, plus — for a FILE-keyed collect only — the directory
// ids whose entire subtree is gone.
//
//	deleted = manifestKeys - (chunkedKeys UNION unchangedKeys)
//
// THE APPROVED RULE IS "manifest minus SUCCESSFULLY-CHUNKED, never minus
// discovered", and this GENERALIZES it rather than replacing it. Under a full
// upload every present key is chunked and the expression reduces to that rule
// character for character. Under a diff, unchanged keys are deliberately NOT
// chunked, so manifest-minus-chunked alone would name essentially the whole
// corpus as deleted — a feature permanently inert behind a green ratio guard,
// which is the harder defect to see. The direction is unchanged: a key is named
// only on POSITIVE evidence that this collect handled it, and "hashed it and it
// matched" is positive evidence, because producing that hash required reading
// the row it covers.
//
// THE DIRECTORY HALF IS FILE-KEYED ONLY, and its absence under a node key is
// structural rather than an omission. A directory id is named because a package
// or repo-root node HAS no file of its own and can be deleted no other way; a
// node-keyed graph has no such node — every row is addressable by its own id,
// which is exactly what the node key names — so ancestor arithmetic there would
// invent ids no row carries, and every one of them would fail the server's
// per-entry validation and refuse the WHOLE deletion set.
//
// WHERE THE READ-FAILED FILE IS ACTUALLY PROTECTED, said plainly because the set
// arithmetic does not do it: a file that failed to read is in neither set, so
// this expression WOULD name it. TWO MECHANISMS KEEP IT FROM ARRIVING HERE, and
// they sit on opposite sides of the wire. Client-side, a dropped file FAILS the
// collect outright in the code collector (codesync/collector.go reads the
// parser's ChunkReport), so a well-behaved client never reaches this expression
// holding one; a custom collector's analog is the completeness assertion its
// contract requires. Server-side, the walk-completeness guard still disables the
// whole deletion phase when the client asserts an incomplete walk — the server
// never trusts the client, so that guard defends against a hostile or buggy one
// rather than serving as a routine lane.
func deletionSet(manifestKeys map[string][32]byte, chunkedKeys, unchangedKeys []string, kind diffKeyKind) []string {
	// SIZED FROM ONE LENGTH, NOT A SUM: a make() capacity is a hint —
	// the unchanged keys cost at most a growth — while a size built by
	// addition is a value the allocator has to take on trust.
	handled := make(map[string]struct{}, len(chunkedKeys))
	for _, p := range chunkedKeys {
		handled[p] = struct{}{}
	}
	for _, p := range unchangedKeys {
		handled[p] = struct{}{}
	}
	var deleted []string
	surviving := make([]string, 0, len(handled))
	manifestList := make([]string, 0, len(manifestKeys))
	for key := range manifestKeys {
		manifestList = append(manifestList, key)
		if _, ok := handled[key]; ok {
			surviving = append(surviving, key)
			continue
		}
		deleted = append(deleted, key)
	}
	if kind == diffKeyFile {
		// deletedDirs = dirsOf(manifestKeys) - dirsOf(chunked UNION unchanged). A
		// directory is named only when NO file survives anywhere BENEATH it — a
		// prefix rule, not a direct-children one, because package nodes exist for
		// ancestor directories that hold no file of their own.
		survivors := dirsOf(surviving)
		for dir := range dirsOf(manifestList) {
			if _, alive := survivors[dir]; !alive {
				deleted = append(deleted, dir)
			}
		}
	}
	sort.Strings(deleted)
	return deleted
}

// dirsOf returns every PROPER ANCESTOR directory of every path, by repeated
// PATH-SEGMENT truncation, with the repo root rendered as the literal "." to
// match the repo-root node id.
//
// SEGMENT BOUNDARIES ARE LOAD-BEARING: "a/b" is an ancestor of "a/b/c.go" and
// NEVER of "a/bc/d.go". A raw string-prefix test gets that wrong and would name a
// live sibling directory as deleted.
//
// THE CLIENT IS THE ONLY SIDE THAT COMPUTES THIS. The server evaluates the same
// boundary rule against its OWN live set to answer a different question — whether
// a NAMED directory is valid — and no directory set is ever transmitted, so the
// manifest stays strictly per-file.
func dirsOf(paths []string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, p := range paths {
		dir := path.Dir(p)
		for {
			if dir == "" || dir == "/" {
				dir = "."
			}
			out[dir] = struct{}{}
			if dir == "." {
				break
			}
			dir = path.Dir(dir)
		}
	}
	return out
}
