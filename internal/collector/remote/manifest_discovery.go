// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/contribhash"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
)

// manifest_discovery.go — the persisted prior value the discovery-mode fallback
// trigger compares against.
//
// WHAT THE SIGNATURE COVERS. The trigger's settled wording is "the discovery
// FINGERPRINT or the language set differs from the last collect", and BOTH HALVES
// ARE FOLDED INTO ONE VALUE HERE — one stored record, one comparison, rather than
// two that can disagree.
//
//   - THE CONFIGURATION HALF is result.DiscoveryFingerprint, produced by the
//     discovery pass itself (the only place that knows it) and carried on the
//     CollectResult. It covers git-versus-filesystem discovery, lifted exclusions
//     and package-prefix scoping — none of which is derivable here, because a
//     file scoped out of discovery leaves no trace in the result.
//   - THE LANGUAGE-SET HALF is the sorted distinct set of node languages, which
//     IS derivable from the result and so is computed rather than carried.
//
// The store follows the repo-manifest idiom — a JSON map under ~/.knowledge,
// missing-file tolerant, every read re-reading from disk so a concurrent collect
// in another process is observed. It DIVERGES on one point deliberately: a
// corrupt or unwritable store is an ERROR here rather than an empty read. That
// read-as-empty behavior looked like tolerance and was a silent permanent
// degrade — the store never healed, so every collect on that machine paid a full
// upload with nothing logged.

// discoveryStore records the last-seen discovery signature per graph.
type discoveryStore struct {
	mu   sync.Mutex
	path string
}

// defaultDiscoveryStore points at ~/.knowledge/collect-discovery.json. nil only
// when the home dir cannot be resolved, which BOTH changed and record report as
// an ERROR rather than degrading: a machine that cannot resolve home cannot hold
// any of this client's state, and a collect that "succeeds" there pays O(repo)
// forever with nothing said.
var defaultDiscoveryStore = newDefaultDiscoveryStore()

func newDefaultDiscoveryStore() *discoveryStore {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return &discoveryStore{path: filepath.Join(home, ".knowledge", "collect-discovery.json")}
}

// discoverySignature derives this collect's whole discovery surface: the carried
// configuration fingerprint, plus the sorted distinct set of node languages.
// Empty languages are dropped — a fileless or language-less node says nothing
// about discovery.
//
// The two halves are joined into ONE value so a single stored record covers both
// and there is one comparison rather than two that can disagree.
func discoverySignature(result *collectorwire.CollectResult) string {
	seen := make(map[string]struct{})
	for _, n := range result.Nodes {
		if lang := n.GetLanguage(); lang != "" {
			seen[lang] = struct{}{}
		}
	}
	langs := make([]string, 0, len(seen))
	for l := range seen {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	return result.DiscoveryFingerprint + "|" + strings.Join(langs, ",")
}

// discoveryKey scopes the record to one graph AND branch: a branch collect and a
// base collect legitimately see different language sets, and comparing across
// them would fire the trigger on every branch switch.
func discoveryKey(result *collectorwire.CollectResult) string {
	return string(result.GraphType) + "/" + result.GraphName + "@" + result.CurrentBranch
}

// collectorVersionKey scopes the collector-version record to one graph AND
// branch, exactly as discoveryKey does, and carries its own "collector/" prefix
// so the two records share a store without sharing a key.
//
// IT IS A SEPARATE KEY RATHER THAN A FOURTH TERM FOLDED INTO discoverySignature.
// The trigger table's whole purpose is to report WHICH condition forced a full
// collect (diffLever's doc, manifest.go:46-53); folding two independent facts
// into one stored value would make a collect that fired for a collector change
// indistinguishable from one that fired for a scope change, and neither
// diagnosis recoverable afterwards.
func collectorVersionKey(result *collectorwire.CollectResult) string {
	return "collector/" + string(result.GraphType) + "/" + result.GraphName + "@" + result.CurrentBranch
}

// filelessSignature is this collect's whole FILELESS payload as one digest — the
// nodes belonging to no file and their edges.
//
// IT EXISTS BECAUSE THAT SET HAS NO PER-FILE DECLINE BASIS. The fileless nodes
// are outside the manifest by construction, so the server never declines them and
// a diff-mode collect re-uploads all of them on every run. This value is what the
// client compares against instead.
//
// IT IS WHOLE-SET RATHER THAN PER-HUB, deliberately. Any symbol added, removed or
// renamed moves a package hub's CONTAINS edges and re-sends the whole payload; a
// pure body edit moves no symbol id, so the payload is identical and the decline
// fires. The worst case of the whole-set form is exactly today's every-collect
// behavior, so it is a strict improvement at every point.
func filelessSignature(result *collectorwire.CollectResult) string {
	h := contribhash.FilelessContributionHash(result.Nodes, result.Edges)
	return hex.EncodeToString(h[:])
}

// filelessKey scopes the fileless record to one graph AND branch, exactly as
// discoveryKey does, and carries its own "fileless/" prefix so the three records
// share a store without sharing a key.
//
// IT IS A SEPARATE KEY for collectorVersionKey's stated reason: two independent
// facts get two keys rather than one folded value, so the condition that fired
// stays diagnosable afterwards. The graph-AND-branch scoping is discoveryKey's
// rule and applies unchanged — a branch and its base legitimately hold different
// fileless sets.
func filelessKey(result *collectorwire.CollectResult) string {
	return "fileless/" + string(result.GraphType) + "/" + result.GraphName + "@" + result.CurrentBranch
}

// baselineCommit is one key and the signature to record for it, DEFERRED until
// the collect's finalize tail reports DONE.
//
// THE SIGNATURE IS CAPTURED AT COMPARE TIME AND CARRIED, never recomputed at the
// commit point. discoverySignature derives its language-set half from
// result.Nodes, and narrowAndGroupRows REASSIGNS that slice to the diff-filtered
// subset before the upload — so recomputing after the upload would record a
// signature the next collect cannot match, and the trigger would fire forever on
// a machine whose discovery surface never moved.
type baselineCommit struct {
	key string
	sig string
}

// commitCollectBaselines advances this collect's baselines, and ONLY on a
// terminal DONE.
//
// EVERY OTHER STATE LEAVES THEM UNADVANCED — a FAILED tail, an UNKNOWN one, a
// poll error, an exhausted wait budget, a cancelled context, and a server that
// returned no finalize id at all. The next collect then legitimately re-fires
// whichever trigger this one was answering. The failure direction is deliberate
// and convergent: at worst one redundant full upload, never a missed one.
//
// IT IS NOT KEYED ON A NIL ERROR, and that distinction is the whole point.
// awaitFinalizeTail returns a nil error on every one of its branches by design —
// a failed tail must not fail the collect, because the durable half already
// committed — so nil says "the collect stands", never "the tail completed".
// Committing on nil would advance the baseline after a FAILED tail and the
// trigger could never re-fire.
func commitCollectBaselines(state knowledgev1.FinalizeState, pending []baselineCommit) error {
	// A collect that owes no baseline consults none either — the non-eligible
	// graph families. It returns before touching the store, so a machine that
	// cannot resolve a home directory does not fail a web or pdf collect over a
	// baseline that collect was never going to record.
	if len(pending) == 0 {
		return nil
	}
	if state != knowledgev1.FinalizeState_FINALIZE_STATE_DONE {
		// WARN RATHER THAN DEBUG, AND THE REASON IS THAT THIS COST IS NEW. An
		// unconfirmed tail — a poll routed to a replica that never served the
		// Finalize, or a wait budget that ran out — used to cost nothing durable.
		// It now WITHHOLDS the baseline advance, so the trigger re-fires and the
		// next collect of this graph pays another decline-suppressed full re-land.
		// A signal that stayed at Debug would leave a repeating full re-land with
		// no operator-visible cause. A collect with nothing pending never reaches
		// here — the early return above takes it — so an unconfirmed tail that
		// genuinely costs nothing stays silent without a second guard.
		slog.Warn("remote sink: finalize tail did not confirm completion — collect baselines WITHHELD, "+
			"so the next collect of this graph re-fires its trigger and pays another full re-land",
			"tail_state", state.String(), "withheld", baselineKeys(pending))
		return nil
	}
	// ONE WRITE FOR THE WHOLE SET. Recording the baselines one at a time would
	// also make them non-atomic: a failure between two writes would advance one
	// baseline and not the other, leaving the collect half-committed with no
	// state that describes it.
	//
	// A store that cannot be written keeps no baseline, so the failure is
	// reported rather than swallowed: swallowing buys a collect that looks
	// healthy and re-fires its trigger forever.
	return defaultDiscoveryStore.record(pending...)
}

// baselineKeys names the baselines a commit is withholding, for the log above.
func baselineKeys(pending []baselineCommit) string {
	keys := make([]string, 0, len(pending))
	for _, b := range pending {
		keys = append(keys, b.key)
	}
	return strings.Join(keys, ",")
}

// changed reports whether sig differs from the recorded value for key. IT
// RECORDS NOTHING — see record for the commit half, and for why the two are
// separate.
//
// ABSENCE AND FAILURE ARE DIFFERENT ANSWERS, and this function used to return one
// boolean for both. FOUR STATES, TWO DISPOSITIONS:
//
//   - a differing prior signature — a genuine scope or mode change. CLIENT INTENT,
//     not an error: it keeps the rebuild lane, because a full collect is exactly
//     the right response to a changed discovery surface.
//   - NO PRIOR RECORD, file absent — the first collect of this graph and branch on
//     this machine, or the first after an upgrade. LEGITIMATE: it also keeps the
//     rebuild lane, and it must NEVER abort, because it is the bootstrap path
//     every machine takes exactly once.
//   - the store is unreadable or malformed — the record was LOST. A client-side
//     failure no full collect repairs: the store stays corrupt, so the lane would
//     fire forever and pay O(repo) on every collect with nothing said.
//   - no store at all (nil receiver) — the home directory could not be resolved,
//     so this machine cannot hold any of the client's state.
//
// The last two ABORT. Only the read arm can tell the second from the third, which
// is why the split lives in readLocked where the os error is still in hand.
func (d *discoveryStore) changed(key, sig string) (bool, error) {
	if d == nil {
		return false, fmt.Errorf(
			"collect discovery store: no store — the home directory could not be resolved, " +
				"so this collect cannot tell a scope change from a first collect")
	}
	if key == "" {
		return true, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	entries, err := d.readLocked()
	if err != nil {
		return false, err
	}
	prior, had := entries[key]
	return !had || prior != sig, nil
}

// record commits sig as the new baseline for key. It is the WRITE half of what
// used to be one compare-and-record call.
//
// WHY THE SPLIT EXISTS: the combined form recorded the new signature during the
// UPLOAD PLANNING pass, before a single chunk was sent. A collect whose upload
// then failed had already advanced the baseline, so the next collect compared
// against work that never landed, saw no change, took the diff lane — and the
// condition that fired the trigger never propagated. The trigger fired exactly
// once and accomplished nothing. Advancing a watermark only after the persist
// succeeds is the shape the watermark-incremental-processing pattern prescribes,
// and advancing it first is the anti-pattern that pattern names first.
//
// EVERY WRITE FAILURE IS REPORTED. A store that cannot be written keeps no
// baseline, so swallowing the error buys a collect that looks healthy and
// re-fires the trigger forever.
//
// AN EMPTY KEY RECORDS NOTHING, matching what changed() answers for one: it
// reads as changed and there is no graph identity to file the signature under.
//
// IT TAKES THE WHOLE SET AND WRITES ONCE. One collect owes more than one
// baseline, and recording them one at a time would cost a full read-modify-write
// per baseline AND make the set non-atomic — a failure between two writes would
// advance one and not the other, a half-committed state nothing else can
// describe or repair.
func (d *discoveryStore) record(commits ...baselineCommit) error {
	if d == nil {
		return fmt.Errorf(
			"collect discovery store: no store — the home directory could not be resolved, " +
				"so this collect cannot record a baseline")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	entries, err := d.readLocked()
	if err != nil {
		return err
	}
	wrote := false
	for _, c := range commits {
		if c.key == "" {
			continue
		}
		entries[c.key] = c.sig
		wrote = true
	}
	if !wrote {
		return nil
	}
	return d.writeLocked(entries)
}

// readLocked returns the recorded signatures. A MISSING file is an empty record
// and no error — that is the first collect. An unreadable or malformed file is an
// ERROR: the record was lost, and reading it as empty would silently pin this
// machine in the expensive lane on every collect from now on.
func (d *discoveryStore) readLocked() (map[string]string, error) {
	raw, err := os.ReadFile(d.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("collect discovery store: read %s: %w", d.path, err)
	}
	entries := map[string]string{}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("collect discovery store: parse %s: %w", d.path, err)
	}
	return entries, nil
}

// writeLocked rewrites the store atomically (temp file + rename), creating the
// enclosing ~/.knowledge directory on first write. Every failure is REPORTED: a
// store that cannot be written keeps no baseline, so swallowing the error buys a
// collect that looks healthy and re-fires the discovery trigger forever.
func (d *discoveryStore) writeLocked(entries map[string]string) error {
	raw, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("collect discovery store: encode: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(d.path), 0o750); err != nil {
		return fmt.Errorf("collect discovery store: create %s: %w", filepath.Dir(d.path), err)
	}
	tmp := d.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("collect discovery store: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, d.path); err != nil {
		return fmt.Errorf("collect discovery store: rename into %s: %w", d.path, err)
	}
	return nil
}
