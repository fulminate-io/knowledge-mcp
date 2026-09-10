// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestEmbedReasonLiteral_IsPinnedAtItsProducingSide makes the rename-plus-new-
// repair discipline executable on the side that PRODUCES the literal.
//
// WHY THE PIN LIVES HERE AND NOT ON THE SERVER. The server's retirements match
// superseded copies of this string by value, because the two binaries are
// separate Go modules and a hand-written shared package is forbidden. Typing the
// CURRENT literal into the server module as a third copy would be exactly the
// resynchronisation the retirement's own comment forbids: it would have to be
// kept in step by hand, and a stale copy is indistinguishable from agreement.
// So the constant is pinned where it is written, and a rename reds THIS test —
// which is the moment the author owes a server-side retirement keyed to the
// string being replaced.
func TestEmbedReasonLiteral_IsPinnedAtItsProducingSide(t *testing.T) {
	const want = "embed-text-empty: the server composed no embed text for this node from its " +
		"declared or default field set, after cold-text hydration"
	if embedTextEmptyReason != want {
		t.Fatalf("the current terminal embed reason changed.\n got: %q\nwant: %q\n\n"+
			"A rename here is legitimate — it is how each narrowing of what 'empty embed text' means "+
			"closes the previous population. What it OWES is a server-side retirement keyed to the "+
			"string this one replaced (see cmd/knowledge-server/internal/store/embed_marker_recovery.go), "+
			"with a conjunct naming which nodes the narrowing made wrong. Update this literal once that "+
			"retirement exists, never before.", embedTextEmptyReason, want)
	}
}

// TestEmbedReasonLiteral_IsWhatTheStampWrites closes the gap between the constant
// and the write: a pin on a constant nothing uses would be a pin on nothing.
func TestEmbedReasonLiteral_IsWhatTheStampWrites(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	fe := &fakeEmbedder{vectors: map[string][]byte{}}
	p := New(Config{}, wc, nil, fe.call)

	runEmbedWorkerBatch(ctx, p, []EmbedWork{embedWork("empty", "  \n ")})

	for _, batchItems := range wc.recordedWrites {
		for _, it := range batchItems {
			if got := it.Metadata[kgtypes.MetaKeyEmbedFailureReason]; got != "" {
				if got != embedTextEmptyReason {
					t.Fatalf("the stamped reason %q is not the pinned constant %q", got, embedTextEmptyReason)
				}
				return
			}
		}
	}
	t.Fatal("no terminal embed marker was stamped for a whitespace-only item — " +
		"the pin above would then be pinning a constant nothing writes")
}
