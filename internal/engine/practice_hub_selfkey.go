// SPDX-License-Identifier: Apache-2.0

// practice_hub_selfkey.go — the hub's self-key, stamped by the engine on every
// practice `source` node it creates.
//
// WHY IT IS AN ENGINE STAMP AND NOT A CALLER CONVENTION. The by-hub delete is ONE
// metadata predicate: every practice node whose `source_hub` equals H. It sweeps
// the hub itself only because the hub carries its own id under that key. The
// recipe landing and the migration driver both wrote that key by hand, so every
// hub in the corpus had it and the convention read as an invariant — but a hub
// created through mutate(create, graph:"practice", type:"source") has a
// SERVER-ASSIGNED id and no key at all, and its collection's delete removed the
// members and left it behind, which is the empty shell the arm exists to remove.
//
// THE ID IS MINTED WHEN THE CALLER SUPPLIED NONE, because a node cannot carry its
// own id under a key before that id exists. The mint is the same shape the store
// assigns (128 random bits, hex), and the create lowering already honors a
// caller-supplied id verbatim, so a stamped body is an ordinary create body.
//
// A BODY KEYED TO SOME OTHER NODE IS REFUSED rather than overwritten, by
// practiceHubNestedCreate in the guard beside this: a caller who typed a
// different hub asked for something this surface does not offer, and silently
// rewriting it would be the coercion this repo refuses.

package engine

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"maps"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// stampPracticeHubSelfKeys gives every `source` body in a practice create its own
// id under the hub key, minting the id when the payload carried none.
//
// IT IS SCOPED TO THE PRACTICE FAMILY AND TO THE SOURCE TYPE. No other family has
// hubs, and a member's hub comes from the call's parameter rather than from its
// own id — stamping either would invent a membership nobody asked for.
func stampPracticeHubSelfKeys(graph string, bodies []*knowledgev1.NodeBody) {
	if graph != string(kgtypes.GraphPractice) {
		return
	}
	for _, b := range bodies {
		if b.GetType() != string(kgtypes.NodeSource) {
			continue
		}
		if b.GetId() == "" {
			b.Id = mintPracticeHubID()
		}
		meta := make(map[string]string, len(b.GetMetadata())+1)
		maps.Copy(meta, b.GetMetadata())
		meta[kgtypes.MetaKeySourceHub] = b.GetId()
		b.Metadata = meta
	}
}

// mintPracticeHubID mints the id a hub needs at compile time.
//
// IT PANICS RATHER THAN RETURNING AN ERROR, for the reason the store's own minter
// does: a failing crypto/rand is not a condition a caller can act on, and an id
// from a degraded source is worse than none because it would collide silently.
func mintPracticeHubID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand.Read failed while minting a practice hub id: %v", err))
	}
	return hex.EncodeToString(b)
}
