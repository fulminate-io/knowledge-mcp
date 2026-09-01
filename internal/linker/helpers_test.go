// SPDX-License-Identifier: Apache-2.0

package linker

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

// linkerPagingCaller answers a type browse the way the server does: ids
// ascending, cursor-exclusive, capped at the requested per-page limit. A fake
// that ignored either knob would serve a drain and a capped read identically,
// which is the whole thing under test.
type linkerPagingCaller struct {
	nodes   []*knowledgev1.Node
	cursors []string
	limits  []int32
}

func (p *linkerPagingCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
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

// TestDrainNodesViaEngine_DrainsMultiplePages seeds more than one full page and
// asserts the drain returns every node — the assertion that fails if the page
// closure forgets after_id (page one repeats forever, the seen-set masks it in
// the returned SET, so the cursor sequence is asserted too) or writes the page
// keys before the caller's args instead of after (a caller's stale limit would
// then defeat the drain).
func TestDrainNodesViaEngine_DrainsMultiplePages(t *testing.T) {
	wantNodes := paging.BrowsePageSize + 3

	seeded := make([]*knowledgev1.Node, 0, wantNodes)
	for i := range wantNodes {
		seeded = append(seeded, &knowledgev1.Node{
			Id:   fmt.Sprintf("n%04d", i),
			Type: string(kgtypes.NodePackage),
		})
	}
	if len(seeded) != wantNodes {
		t.Fatalf("fixture built %d nodes, want %d", len(seeded), wantNodes)
	}

	pc := &linkerPagingCaller{nodes: seeded}
	got, err := drainNodesViaEngine(context.Background(), pc, map[string]any{
		"graph": "code",
		"repo":  "myrepo",
		"type":  string(kgtypes.NodePackage),
		// A caller's stale limit must not defeat the drain.
		"limit": 0,
	})
	if err != nil {
		t.Fatalf("drainNodesViaEngine: %v", err)
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

// TestDrainNodesViaEngine_RejectsPayloadWithoutSingularType is the catcher for
// the drain's hang guard: a payload that never reaches the singular type-browse
// arm threads no cursor, so every page repeats page one and the loop never ends.
// The zero-RPC assertion is what separates a guard from a fetch failure.
func TestDrainNodesViaEngine_RejectsPayloadWithoutSingularType(t *testing.T) {
	cases := map[string]map[string]any{
		"no type key at all": {"graph": "code", "repo": "myrepo"},
		"blank type value":   {"graph": "code", "repo": "myrepo", "type": "   "},
		// The conjunction: a naive "type is non-blank" guard passes this and
		// hands the plural-types arm — which threads no cursor — to the drain.
		"type plus higher-precedence types": {
			"graph": "code",
			"repo":  "myrepo",
			"type":  string(kgtypes.NodeFile),
			"types": []string{string(kgtypes.NodeFile)},
		},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			fake := &fakeGraphCaller{}
			got, err := drainNodesViaEngine(context.Background(), fake, args)
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
			if len(fake.calls) != 0 {
				t.Errorf("a refused payload must issue NO RPC, got %d", len(fake.calls))
			}
		})
	}
}

// TestBrowseNodesViaEngine_RejectsNonPositiveLimit is the catcher for the
// pass-through seam's bound. browseNodesViaEngine serves ONE page, and a payload
// whose limit is absent or non-positive is the exact shape the compiler rewrites
// to browseDefaultLimit — so serving it hands back a silent default page instead
// of the set the caller asked for. The ZERO-RPC assertion is what separates a
// guard from a transport failure; the by-id case is the known positive that
// stops the guard passing by refusing everything.
func TestBrowseNodesViaEngine_RejectsNonPositiveLimit(t *testing.T) {
	refused := map[string]map[string]any{
		"limit zero": {
			"graph": "code", "repo": "myrepo",
			"type": string(kgtypes.NodePackage), "limit": 0,
		},
		"limit key absent entirely": {
			"graph": "code", "repo": "myrepo",
			"type": string(kgtypes.NodePackage),
		},
	}
	for name, args := range refused {
		t.Run(name, func(t *testing.T) {
			fake := &fakeGraphCaller{}
			got, err := browseNodesViaEngine(context.Background(), fake, args)
			if err == nil {
				t.Fatalf("expected a refusal, got %d nodes and no error", len(got))
			}
			if _, ok := errors.AsType[*unboundedBrowseError](err); !ok {
				t.Fatalf("expected a typed unboundedBrowseError, got %T: %v", err, err)
			}
			if len(fake.calls) != 0 {
				t.Errorf("a refused payload must issue NO RPC, got %d", len(fake.calls))
			}
		})
	}

	// KNOWN POSITIVE: a by-id payload takes the by-id compile arm, which never
	// reaches applyBrowseLimitOffset, so no browse default applies and no limit is
	// owed. It must still be served — and its Execute must actually be issued,
	// which is what proves the two refusals above measured the guard rather than a
	// seam that refuses everything.
	t.Run("by-id payload is served without a limit", func(t *testing.T) {
		pc := &linkerPagingCaller{}
		if _, err := browseNodesViaEngine(context.Background(), pc, map[string]any{
			"graph": "code", "repo": "myrepo",
			"ids": []string{"pkg/alpha"},
		}); err != nil {
			t.Fatalf("a by-id payload must be served, got %v", err)
		}
		if len(pc.cursors) != 1 {
			t.Fatalf("the by-id read must issue exactly 1 Execute, got %d", len(pc.cursors))
		}
	})
}
