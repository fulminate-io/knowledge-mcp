// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// drainPagingCaller answers a type browse the way the server does: ids
// ascending, cursor-exclusive, capped at the requested per-page limit. A fake
// that ignored either knob would serve a drain and a capped read identically,
// which is the whole thing under test.
type drainPagingCaller struct {
	nodes   []*knowledgev1.Node
	cursors []string
	limits  []int32
}

func (p *drainPagingCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	p.cursors = append(p.cursors, q.GetAfterId())
	p.limits = append(p.limits, q.GetLimit())

	out := make([]*knowledgev1.Node, 0, len(p.nodes))
	wantType := q.GetSelection().GetNodeType()
	for _, n := range p.nodes {
		if wantType != "" && n.GetType() != wantType {
			continue
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetId() < out[j].GetId() })
	if cursor := q.GetAfterId(); cursor != "" {
		kept := out[:0]
		for _, n := range out {
			if n.GetId() > cursor {
				kept = append(kept, n)
			}
		}
		out = kept
	}
	if lim := int(q.GetLimit()); lim > 0 && len(out) > lim {
		out = out[:lim]
	}
	return enginetest.ResponseWithNodes(out...), nil
}

// TestDrainQueryNodes_DrainsMultiplePages seeds more than one full page and
// asserts the drain returns every node — the assertion that fails if the page
// closure forgets after_id (page one repeats forever, and the seen-set masks
// that in the returned SET, so the cursor sequence is asserted too) or writes
// the page keys before the caller's args instead of after (a caller's stale
// limit would then defeat the drain).
func TestDrainQueryNodes_DrainsMultiplePages(t *testing.T) {
	wantNodes := paging.BrowsePageSize + 3

	seeded := make([]*knowledgev1.Node, 0, wantNodes)
	for i := range wantNodes {
		seeded = append(seeded, &knowledgev1.Node{
			Id:   fmt.Sprintf("n%04d", i),
			Type: string(kgtypes.NodeLogBackend),
		})
	}
	if len(seeded) != wantNodes {
		t.Fatalf("fixture built %d nodes, want %d", len(seeded), wantNodes)
	}

	pc := &drainPagingCaller{nodes: seeded}
	got, err := drainQueryNodes(context.Background(), pc, map[string]any{
		"type": string(kgtypes.NodeLogBackend),
		// A caller's stale limit must not defeat the drain.
		"limit": 0,
	})
	if err != nil {
		t.Fatalf("drainQueryNodes: %v", err)
	}
	if len(got) != wantNodes {
		t.Errorf("drain returned %d nodes, want %d (the fixture count, not a set-derived length)", len(got), wantNodes)
	}
	if len(pc.cursors) != 2 {
		t.Fatalf("a corpus of one full page plus a remainder must take exactly 2 round trips, got %d", len(pc.cursors))
	}
	if pc.cursors[0] != "" {
		t.Errorf("page one must carry a SET BUT EMPTY cursor, got %q", pc.cursors[0])
	}
	if want := seeded[paging.BrowsePageSize-1].GetId(); pc.cursors[1] != want {
		t.Errorf("page two cursor = %q, want the last id of page one (%q)", pc.cursors[1], want)
	}
	for i, lim := range pc.limits {
		if lim != int32(paging.BrowsePageSize) {
			t.Errorf("page %d asked for limit %d, want the shared page size %d — the caller's limit:0 leaked through", i+1, lim, paging.BrowsePageSize)
		}
	}
}

// TestDrainQueryNodes_RejectsPayloadWithoutSingularType is the catcher for the
// drain's hang guard: a payload that never reaches the singular type-browse arm
// threads no cursor, so every page repeats page one and the loop never ends. The
// zero-RPC assertion is what separates a guard from a fetch failure.
func TestDrainQueryNodes_RejectsPayloadWithoutSingularType(t *testing.T) {
	cases := map[string]map[string]any{
		"no type key at all": {"meta": map[string]string{"k": "v"}},
		"blank type value":   {"type": "   "},
		// The conjunction: a naive "type is non-blank" guard passes this and
		// hands the plural-types arm — which threads no cursor — to the drain.
		"type plus higher-precedence types": {
			"type":  string(kgtypes.NodeLogBackend),
			"types": []string{string(kgtypes.NodeLogBackend)},
		},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			fake := &fakeGraphCaller{}
			got, err := drainQueryNodes(context.Background(), fake, args)
			if err == nil {
				t.Fatalf("expected a refusal, got %d nodes and no error", len(got))
			}
			var unpageable *unpageablePayloadError
			if !errors.As(err, &unpageable) {
				t.Fatalf("expected a typed unpageablePayloadError, got %T: %v", err, err)
			}
			if unpageable.Key == "" {
				t.Errorf("the refusal must name the disqualifying key, got %q", unpageable.Key)
			}
			if len(fake.execRequests) != 0 {
				t.Errorf("a refused payload must issue NO RPC, got %d", len(fake.execRequests))
			}
		})
	}
}
