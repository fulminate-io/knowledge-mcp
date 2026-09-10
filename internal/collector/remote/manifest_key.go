// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// manifest_key.go — THE DIFF KEY: which unit a graph family diffs on, how an
// entry's key is read through its oneof arm, and the contract a served manifest
// must satisfy over those keys. Split out of manifest.go, which keeps the mode
// vocabulary, the fail-closed trigger table and the diff arithmetic, so that
// file stays under the repo's file-length cap.

// diffKeyKind names the UNIT one graph family's diff is keyed on. It is declared
// ONCE here and carried down the whole upload path, because every consumer — the
// manifest's self-consistency check, the diff comparison, the deletion
// arithmetic, the upload filter, the chunk's echoed entries and the Finalize
// carrier — must agree about what a key IS, and a family that agreed at one of
// them and not another would diff correctly and delete the wrong rows.
type diffKeyKind string

const (
	// diffKeyNone: the family does not diff at all. No manifest is fetched, the
	// whole graph uploads, and no deletion is ever named.
	diffKeyNone diffKeyKind = ""
	// diffKeyFile: the key is a repo-relative FILE PATH. The code family.
	diffKeyFile diffKeyKind = "file"
	// diffKeyNode: the key is a NODE ID. Registered custom families.
	diffKeyNode diffKeyKind = "node"
)

// diffKeyKindFor is the GRAPH-FAMILY GATE, and it is upstream of the trigger
// table rather than a ninth trigger. It answers both questions at once — whether
// this family diffs, and on what unit — so the two can never be decided in two
// places and disagree.
//
// UploadSink.WriteResult is shared by EVERY collector family. The web collector
// emits FilePath-bearing nodes through this same sink, and a web crawl is
// BUDGET-BOUNDED (MaxPages / MaxDepth / MaxPathSegments), so a smaller or
// differently-budgeted re-crawl legitimately re-materializes only a SUBSET of the
// paths the previous crawl produced. Without this gate every absent path would be
// named as a deletion and EVERY guard would admit it: the identity echo matches
// (the client did fetch that manifest), walk_complete is true (the crawl
// completed within its budget), the ratio is under the bound for any ordinary
// budget change, and per-entry validation passes because each named path really
// does have a live collector-owned node. Nothing else in the design catches it.
// THAT ARGUMENT IS ABOUT WEB AND IT STILL HOLDS: web stays out.
//
// A REGISTERED CUSTOM GRAPH IS ADMITTED ON THE NODE KEY, under the standing
// ruling that custom graphs act under the same rules as code graphs. It is
// recognized by EXCLUSION — a non-empty type that is not one of the built-ins —
// because the built-in vocabulary is a closed list in this package while the
// registered set is per-account runtime data the sink cannot see. The exclusion
// is safe in the direction that matters: a name this client cannot resolve to a
// registration never reaches WriteResult at all, since the collect dispatch
// refuses an unknown type before a result exists (tools/collect_custom.go), and
// the built-in list is what registration itself refuses a collision with.
//
// WHY THE CUSTOM FAMILY CANNOT USE THE FILE KEY. A registered collector's node
// carries an OPTIONAL file path and usually none, and both manifest renders
// exclude fileless rows from the population by construction — so a file-keyed
// custom graph is served an EMPTY manifest, every node lands in the changed set,
// and the diff is inert while every gate stays green.
func diffKeyKindFor(gt kgtypes.GraphType) diffKeyKind {
	if gt == kgtypes.GraphCode {
		return diffKeyFile
	}
	if gt == "" || kgtypes.IsBuiltinGraphType(string(gt)) {
		return diffKeyNone
	}
	return diffKeyNode
}

// diffEligibleGraph reports whether a family diffs at all. It is the predicate
// the sink's gate reads and the one the family table pins; the KIND is what the
// path downstream carries.
func diffEligibleGraph(gt kgtypes.GraphType) bool { return diffKeyKindFor(gt) != diffKeyNone }

// manifestEntryKey reads an entry's key THROUGH ITS ARM, and it is the only
// place in this package that is allowed to.
//
// A BARE GetFilePath() ON A NODE-KEYED ENTRY RETURNS THE EMPTY STRING rather
// than failing, so an arm-blind read does not break — it silently keys every
// entry on "", which collapses the whole manifest onto one key and makes every
// node read CHANGED forever behind a green suite. Discriminating first is what
// turns that class of mistake into a refusal.
//
// ok is false for an entry whose key arm is UNSET or whose value is empty. The
// caller treats that as a manifest that broke its own contract, which aborts the
// collect rather than degrading it — the same disposition every other
// self-consistency defect gets.
func manifestEntryKey(e *knowledgev1.ManifestEntry) (key string, kind diffKeyKind, ok bool) {
	switch e.GetKey().(type) {
	case *knowledgev1.ManifestEntry_FilePath:
		v := e.GetFilePath()
		return v, diffKeyFile, v != ""
	case *knowledgev1.ManifestEntry_NodeId:
		v := e.GetNodeId()
		return v, diffKeyNode, v != ""
	default:
		return "", diffKeyNone, false
	}
}

// newManifestEntry builds an entry on the arm kind names. It is the mirror of
// manifestEntryKey and exists for the same reason: one place decides which arm a
// key rides, so a producer and a consumer cannot disagree about it.
func newManifestEntry(kind diffKeyKind, key string, hash []byte) *knowledgev1.ManifestEntry {
	e := &knowledgev1.ManifestEntry{ContributionHash: hash}
	if kind == diffKeyNode {
		e.Key = &knowledgev1.ManifestEntry_NodeId{NodeId: key}
		return e
	}
	e.Key = &knowledgev1.ManifestEntry_FilePath{FilePath: key}
	return e
}

// contribHashBytes is the per-key hash width the scheme defines.
const contribHashBytes = 32

// manifestSelfConsistent reports whether the served manifest agrees with its own
// declared contract: one entry per distinct non-empty key, every key on the arm
// THIS COLLECT'S FAMILY diffs on, each carrying a 32-byte hash.
//
// THE ARM IS PART OF THE CONTRACT, not a detail the client can adapt to. A
// manifest whose entries key on the other arm describes a different unit than
// the one this collect hashed, so every comparison against it is meaningless;
// admitting it would produce a diff in which nothing matches and a deletion set
// naming the whole graph. The render decides the arm from the same family gate
// the client does, so a disagreement is a server defect and aborts here.
func manifestSelfConsistent(resp *knowledgev1.CollectManifestResponse, kind diffKeyKind) bool {
	seen := make(map[string]struct{}, len(resp.GetEntries()))
	for _, e := range resp.GetEntries() {
		key, entryKind, ok := manifestEntryKey(e)
		if !ok || entryKind != kind || len(e.GetContributionHash()) != contribHashBytes {
			return false
		}
		if _, dup := seen[key]; dup {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}

// manifestDefect names WHICH rule a self-inconsistent manifest broke and the
// entry that broke it, for the abort's error message. Empty when the manifest is
// consistent.
//
// IT EXISTS BECAUSE "inconsistent" IS NOT ACTIONABLE. The condition it describes
// aborts the collect, and an error that names no rule and no key leaves the
// operator exactly as stuck as the silent fallback did — the same standard the
// lever errors are held to.
//
// IT READS THE ARM RATHER THAN THE FILE FIELD, and that matters here as much as
// in the comparison: a defect renderer that reached for GetFilePath() on a
// node-keyed entry would report an empty path for every entry and name the wrong
// one.
func manifestDefect(resp *knowledgev1.CollectManifestResponse, kind diffKeyKind) string {
	seen := make(map[string]struct{}, len(resp.GetEntries()))
	for i, e := range resp.GetEntries() {
		key, entryKind, ok := manifestEntryKey(e)
		if !ok {
			return fmt.Sprintf(
				"entry %d carries an EMPTY or UNSET key, so a row outside the %s-keyed render leaked into it", i, kind)
		}
		if entryKind != kind {
			return fmt.Sprintf(
				"entry %d is %s-keyed (%q) and this collect diffs on the %s key, so the manifest describes a "+
					"different unit than the rows this collect hashed", i, entryKind, key, kind)
		}
		if n := len(e.GetContributionHash()); n != contribHashBytes {
			return fmt.Sprintf("entry %q carries a %d-byte contribution hash, want %d", key, n, contribHashBytes)
		}
		if _, dup := seen[key]; dup {
			return fmt.Sprintf("%s key %q appears more than once, so per-key aggregation is broken", kind, key)
		}
		seen[key] = struct{}{}
	}
	return ""
}
