// SPDX-License-Identifier: Apache-2.0

// practice_type_guard.go — the practice graph's CLOSED VOCABULARY, refused at
// the same position the hub rules run: beneath the mutate tool, the standalone
// delete tool and the direct compile-and-execute callers alike.
//
// WHY IT IS HERE AND NOT ONLY ON THE SERVER. The server's create validator is
// AUTHORITATIVE — a stale or foreign client fails closed against it — but a
// refusal that arrives only after a round trip is a refusal the author reads
// later and with less context. This position answers before the write is
// compiled, from this module's own copy of the vocabulary, and it is the
// position the recipe landing and the style-rule import pass through on their
// way to a create_batch that never touches InterceptMutate.
//
// WHY A SIBLING FILE. practice_hub_guard.go holds the HUB rules and their
// refusal constructors and is at the repository's per-file line budget; this is
// a different rule about the same graph, so it lands beside it whole rather than
// squeezing a coherent unit into the space left over.
//
// IT READS THE VOCABULARY, NEVER THE SERVER. The set is kgtypes' own copy, and a
// census pins it to the server's declaration file in both directions, which in
// turn is pinned to the enrollment table the authoritative refusal reads. A read
// here instead would put an RPC on every practice write and still not be
// authoritative, since the server checks anyway.
//
// IT RUNS AT ONE POSITION, GuardPracticeWrite's rule sequence, and not also in
// GuardPracticePayload beside the hub rules. It was written in both and measured
// to buy nothing there: every caller of the payload gate falls through to
// engine.Dispatch, which runs GuardPracticeWrite, so a payload refused at one is
// refused at the other with the same arm prefix and byte-identical text —
// removing the payload-position call turned no test red and moved no message. A
// declaration nothing can observe is one nobody can maintain. The hub rules sit
// in both positions because each has a test that reds at each; that is not a
// reason to keep a third copy that has none.

package engine

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// PracticeUnenrolledTypeRefusal renders the refusal for a body whose node type
// the practice vocabulary does not enroll. Exported on the same terms as the hub
// refusals beside it: one sentence, rendered once, so no two positions can tell a
// caller different things about one rule.
//
// It carries what every refusal in this package carries: the offending value and
// where in the payload it sits, what it was checked against, WHY it is refused
// here rather than stored and left alone, the admitted set, and that nothing was
// written.
func PracticeUnenrolledTypeRefusal(role, typ string) error {
	return fmt.Errorf(
		"`%s`=%q is not a node type the combined practice graph enrolls. Its vocabulary is CLOSED: a type nothing "+
			"enrolls would be stored but never embedded and never selected by any scan, so the graph would report "+
			"itself converged while those rows sat unreachable — which is why this is refused at the write rather "+
			"than answered with a row nobody can find. The whole write is refused; nothing was written. Types a "+
			"create may use here: %s",
		role, typ, practiceCreatableVocabulary())
}

// practiceCreatableVocabulary renders the types a create may actually use, for
// the refusal message. kgtypes returns them sorted, so one payload's refusal
// reads the same on every run — a message that named its vocabulary in map
// order could not be asserted by any gate.
//
// CREATABLE, NOT ENROLLED. The enrolled set carries five types the server's
// system-managed rule refuses one step later, so offering them here would send
// an author to a second refusal.
func practiceCreatableVocabulary() string {
	types := kgtypes.CreatableNodeTypes()
	names := make([]string, 0, len(types))
	for _, t := range types {
		names = append(names, string(t))
	}
	return strings.Join(names, ", ")
}

// practiceCreateTypes refuses any create-shaped body carrying a type the
// practice vocabulary does not enroll.
//
// THE THREE ARMS ARE THE CREATE-SHAPED ONES, and the omissions are deliberate
// rather than incidental: create, create_batch and upsert carry a node TYPE that
// decides what is written, while update, update_batch, bulk_update_metadata,
// link, unlink and delete carry no create body at all — an update names a node
// that already exists, and refusing it on a type it does not set would refuse
// repairs to rows already in the graph.
//
// AN EMPTY TYPE IS NOT THIS RULE'S TO REFUSE. A create with no type is refused
// by the required-field check on the server, which says so in the words that
// rule owns; answering here would give the caller a vocabulary lecture about a
// field they left blank.
func practiceCreateTypes(a practiceWriteArgs) error {
	if a.Operation != "create" && a.Operation != "create_batch" && a.Operation != "upsert" {
		return nil
	}
	if a.Type != "" && !kgtypes.NodeType(a.Type).IsEnrolled() {
		return PracticeUnenrolledTypeRefusal("type", a.Type)
	}
	for i, b := range a.Nodes {
		if b.Type == "" || kgtypes.NodeType(b.Type).IsEnrolled() {
			continue
		}
		return PracticeUnenrolledTypeRefusal(fmt.Sprintf("nodes[%d].type", i), b.Type)
	}
	return nil
}
