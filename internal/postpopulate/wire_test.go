// SPDX-License-Identifier: Apache-2.0

package postpopulate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// captureCaller records every Execute request so the wire helpers can be
// asserted: exactly-one-mutation, correct selector FIELD (Language vs Repo vs
// Name), and the create_batch nodes[]+edges[] payload.
type captureCaller struct {
	reqs []*knowledgev1.ExecuteRequest
}

func (c *captureCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	c.reqs = append(c.reqs, req)
	return &knowledgev1.ExecuteResponse{}, nil
}

// mutations returns only the requests carrying a MutationPlan.
func (c *captureCaller) mutations() []*knowledgev1.ExecuteRequest {
	var out []*knowledgev1.ExecuteRequest
	for _, r := range c.reqs {
		if r.GetMutation() != nil {
			out = append(out, r)
		}
	}
	return out
}

// TestLinkEdgesBatch_OneMutationSingletonFamily is the SINGLETON half of the
// routing claim; TestLinkEdgesBatch_CodeRoutesByRepo below is the instance-keyed
// half.
//
// PRACTICE IS THE SUBJECT AND ITS CLAIM IS INVERTED. This test read "practice
// routes by Language" while the family held eight per-language graphs; it holds
// one now, so a caller-supplied name is projected AWAY and the Target must carry
// no instance field at all. Asserting the emptiness rather than deleting the test
// is what catches a projection that starts routing a name here again — the
// server's practice policy row refuses one, so a routed name is a refused write.
func TestLinkEdgesBatch_OneMutationSingletonFamily(t *testing.T) {
	cc := &captureCaller{}
	edges := []knowledgev1.Edge{
		{FromId: "a", ToId: "b", Type: string(kgtypes.EdgeBuilds), Method: "m"},
		{FromId: "c", ToId: "d", Type: string(kgtypes.EdgeBuilds), Method: "m"},
	}
	if err := LinkEdgesBatch(context.Background(), cc, kgtypes.GraphPractice, "go", edges); err != nil {
		t.Fatalf("LinkEdgesBatch: %v", err)
	}
	muts := cc.mutations()
	if len(muts) != 1 {
		t.Fatalf("expected exactly 1 Execute mutation (no per-edge loop), got %d", len(muts))
	}
	tgt := muts[0].GetTarget()
	if tgt.GetGraph() != "practice" {
		t.Errorf("Target.Graph = %q, want practice", tgt.GetGraph())
	}
	if tgt.GetLanguage() != "" {
		t.Errorf("Target.Language = %q, want empty (practice is a singleton: a write consumes no instance field)", tgt.GetLanguage())
	}
	if tgt.GetName() != "" {
		t.Errorf("Target.Name = %q, want empty (a singleton must NOT route by Name either)", tgt.GetName())
	}
	plan := muts[0].GetMutation()
	if got := len(plan.GetEdges()); got != 2 {
		t.Errorf("plan edges = %d, want 2", got)
	}
	if len(plan.GetNodeBodies()) != 0 {
		t.Errorf("LinkEdgesBatch must carry no node bodies, got %d", len(plan.GetNodeBodies()))
	}
}

func TestLinkEdgesBatch_CodeRoutesByRepo(t *testing.T) {
	cc := &captureCaller{}
	edges := []knowledgev1.Edge{{FromId: "x", ToId: "y", Type: string(kgtypes.EdgeContains)}}
	if err := LinkEdgesBatch(context.Background(), cc, kgtypes.GraphCode, "myrepo", edges); err != nil {
		t.Fatalf("LinkEdgesBatch: %v", err)
	}
	muts := cc.mutations()
	if len(muts) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(muts))
	}
	tgt := muts[0].GetTarget()
	if tgt.GetRepo() != "myrepo" {
		t.Errorf("Target.Repo = %q, want myrepo (code routes by Repo)", tgt.GetRepo())
	}
	if tgt.GetLanguage() != "" || tgt.GetName() != "" {
		t.Errorf("code write leaked Account=%q Name=%q", tgt.GetLanguage(), tgt.GetName())
	}
}

func TestLinkNodesAndEdgesBatch_NodesAndEdgesOneMutation(t *testing.T) {
	cc := &captureCaller{}
	nodes := []*knowledgev1.Node{
		{Id: "n1", Type: string(kgtypes.NodePattern), SymbolName: "sentinel-1"},
	}
	edges := []knowledgev1.Edge{{FromId: "src", ToId: "n1", Type: string(kgtypes.EdgeDeploys), Method: "m"}}
	if err := LinkNodesAndEdgesBatch(context.Background(), cc, kgtypes.GraphPractice, "go", nodes, edges); err != nil {
		t.Fatalf("LinkNodesAndEdgesBatch: %v", err)
	}
	muts := cc.mutations()
	if len(muts) != 1 {
		t.Fatalf("expected exactly 1 Execute mutation carrying BOTH nodes+edges, got %d", len(muts))
	}
	plan := muts[0].GetMutation()
	if len(plan.GetNodeBodies()) != 1 {
		t.Errorf("plan node bodies = %d, want 1", len(plan.GetNodeBodies()))
	}
	if plan.GetNodeBodies()[0].GetName() != "sentinel-1" {
		t.Errorf("node name = %q, want sentinel-1", plan.GetNodeBodies()[0].GetName())
	}
	if len(plan.GetEdges()) != 1 {
		t.Errorf("plan edges = %d, want 1", len(plan.GetEdges()))
	}
	if lang := muts[0].GetTarget().GetLanguage(); lang != "" {
		t.Errorf("Target.Language = %q, want empty: practice is a singleton and consumes no instance field", lang)
	}
}

// TestExecCreateBatchNodes_SetsSystemManagedCreate pins the trusted-collector
// signal: every postpopulate create_batch write (the collector-owned
// package/file/branch creates ride this path) must produce a plan with
// SystemManagedCreate==true so the server skips the user-facing
// system-managed-type guard. Without it, the relocated validation rejects
// every such create.
func TestExecCreateBatchNodes_SetsSystemManagedCreate(t *testing.T) {
	cc := &captureCaller{}
	// A package node — a system-managed shape the server's systemManagedType
	// guard rejects for unflagged user creates.
	nodes := []*knowledgev1.Node{
		{Id: "pkg:domains/store", Type: string(kgtypes.NodePackage), SymbolName: "domains/store"},
	}
	if err := LinkNodesAndEdgesBatch(context.Background(), cc, kgtypes.GraphCode, "knowledge", nodes, nil); err != nil {
		t.Fatalf("LinkNodesAndEdgesBatch: %v", err)
	}
	muts := cc.mutations()
	if len(muts) != 1 {
		t.Fatalf("expected exactly 1 create mutation, got %d", len(muts))
	}
	plan := muts[0].GetMutation()
	if plan.GetKind() != knowledgev1.MutationPlan_MUTATION_KIND_CREATE {
		t.Fatalf("expected CREATE kind, got %v", plan.GetKind())
	}
	if !plan.GetSystemManagedCreate() {
		t.Errorf("postpopulate create_batch must set SystemManagedCreate=true (trusted collector), got false")
	}
}

// TestUserMutateCompile_DoesNotSetSystemManagedCreate proves the flag is
// UNFORGEABLE through the user mutate tool: the LLM-facing engine.Compile path
// (the same compiler the mutate tool dispatches to) produces SystemManagedCreate
// ==false even when an attacker tries to smuggle the arg key into the create_batch
// payload. Only the postpopulate wire sets it PROGRAMMATICALLY on the compiled
// proto — there is no arg→field mapping.
func TestUserMutateCompile_DoesNotSetSystemManagedCreate(t *testing.T) {
	// A create_batch that even attempts to set the key via args (an attacker would
	// try this). The compiler ignores unknown arg keys; the field stays false.
	args := map[string]any{
		"operation":             "create_batch",
		"graph":                 "code",
		"repo":                  "knowledge",
		"system_managed_create": true, // attacker-supplied arg — must NOT land on the proto
		"nodes": []map[string]any{
			{"type": string(kgtypes.NodePackage), "name": "domains/store"},
		},
	}
	body, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, ok := engine.Compile("mutate", body)
	if !ok {
		t.Fatalf("create_batch args expected reducible to a MutationPlan")
	}
	if req.GetMutation().GetSystemManagedCreate() {
		t.Errorf("user mutate compile path must NOT set SystemManagedCreate (forgeable via args), got true")
	}
}

func TestLinkEdgesBatch_EmptyNoRPC(t *testing.T) {
	cc := &captureCaller{}
	if err := LinkEdgesBatch(context.Background(), cc, kgtypes.GraphPractice, "go", nil); err != nil {
		t.Fatalf("LinkEdgesBatch empty: %v", err)
	}
	if len(cc.reqs) != 0 {
		t.Errorf("empty edges must fire no RPC, got %d", len(cc.reqs))
	}
}

func TestBrowseEdges_ReadRoutesTheSingletonTarget(t *testing.T) {
	cc := &captureCaller{}
	if _, err := BrowseEdges(context.Background(), cc, kgtypes.GraphPractice, "go", "zone-1", OutgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeManages}); err != nil {
		t.Fatalf("BrowseEdges: %v", err)
	}
	if len(cc.reqs) != 1 {
		t.Fatalf("expected 1 Execute query, got %d", len(cc.reqs))
	}
	req := cc.reqs[0]
	tgt := req.GetTarget()
	if tgt.GetGraph() != "practice" || tgt.GetLanguage() != "" {
		t.Errorf("edge read routed to graph=%q language=%q, want practice with no instance field", tgt.GetGraph(), tgt.GetLanguage())
	}
	if tgt.GetName() != "" {
		t.Errorf("edge read must NOT route a singleton family by Name, got Name=%q", tgt.GetName())
	}
	q := req.GetQuery()
	if q.GetReturnMode() != knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		t.Errorf("edge read must use RETURN_MODE_EDGES, got %v", q.GetReturnMode())
	}
	if len(q.GetIds()) != 1 || q.GetIds()[0] != "zone-1" {
		t.Errorf("edge read must key on Ids=[zone-1], got %v", q.GetIds())
	}
	if !q.GetForward() {
		t.Errorf("edge read must be Forward=true (outgoing)")
	}
	if et := q.GetSelection().GetEdgeTypes(); len(et) != 1 || et[0] != "MANAGES" {
		t.Errorf("edge read must filter EdgeTypes=[MANAGES], got %v", et)
	}
}

func TestBrowseEdges_EmptyFromIDNoRPC(t *testing.T) {
	cc := &captureCaller{}
	if _, err := BrowseEdges(context.Background(), cc, kgtypes.GraphPractice, "go", "", OutgoingEdges, nil); err != nil {
		t.Fatalf("BrowseEdges empty: %v", err)
	}
	if len(cc.reqs) != 0 {
		t.Errorf("empty fromID must fire no RPC, got %d", len(cc.reqs))
	}
}

func TestUnlinkEdge_RoutesTheSingletonTarget(t *testing.T) {
	cc := &captureCaller{}
	if err := UnlinkEdge(context.Background(), cc, kgtypes.GraphPractice, "go", "zone-1", "dangling-runner", kgtypes.EdgeManages); err != nil {
		t.Fatalf("UnlinkEdge: %v", err)
	}
	muts := cc.mutations()
	if len(muts) != 1 {
		t.Fatalf("expected exactly 1 unlink mutation, got %d", len(muts))
	}
	tgt := muts[0].GetTarget()
	if tgt.GetGraph() != "practice" || tgt.GetLanguage() != "" {
		t.Errorf("unlink routed to graph=%q language=%q, want practice with no instance field", tgt.GetGraph(), tgt.GetLanguage())
	}
	if tgt.GetName() != "" {
		t.Errorf("unlink must NOT route a singleton family by Name, got Name=%q", tgt.GetName())
	}
	plan := muts[0].GetMutation()
	if plan.GetKind() != knowledgev1.MutationPlan_MUTATION_KIND_UNLINK {
		t.Errorf("expected UNLINK kind, got %v", plan.GetKind())
	}
}

func TestBrowseNodes_QueryRoutesTheSingletonTarget(t *testing.T) {
	cc := &captureCaller{}
	// A POSITIVE limit: this seam serves one bounded page and refuses a payload
	// without one, so a limit:0 payload would never reach the routing this test is
	// about. The subject here is the (gt, graphName) → Target translation.
	if _, err := BrowseNodes(context.Background(), cc, kgtypes.GraphPractice, "go", map[string]any{
		"type":  string(kgtypes.NodePattern),
		"limit": 25,
	}); err != nil {
		t.Fatalf("BrowseNodes: %v", err)
	}
	if len(cc.reqs) != 1 {
		t.Fatalf("expected 1 Execute query, got %d", len(cc.reqs))
	}
	tgt := cc.reqs[0].GetTarget()
	if tgt.GetGraph() != "practice" || tgt.GetLanguage() != "" {
		t.Errorf("browse routed to graph=%q language=%q, want practice with no instance field", tgt.GetGraph(), tgt.GetLanguage())
	}
	if tgt.GetName() != "" {
		t.Errorf("browse must NOT route a singleton family by Name, got Name=%q", tgt.GetName())
	}
}

// pagingCaller is a captureCaller that answers a type browse the way the server
// does: ids ascending, cursor-exclusive, capped at the requested per-page limit.
// A fake that ignored either knob would serve a drain and a capped read
// identically, which is the whole thing under test.
type pagingCaller struct {
	captureCaller
	nodes []*knowledgev1.Node
}

func (p *pagingCaller) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if _, err := p.captureCaller.Execute(ctx, req); err != nil {
		return nil, err
	}
	q := req.GetQuery()
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

// TestBrowseAllNodes_RejectsPayloadWithoutSingularType is the catcher for the
// drain's hang guard: a payload that never reaches the singular type-browse arm
// threads no cursor, so every page repeats page one and the loop never ends. The
// zero-RPC assertion is what separates a guard from a fetch failure.
func TestBrowseAllNodes_RejectsPayloadWithoutSingularType(t *testing.T) {
	cases := map[string]map[string]any{
		"no type key at all": {"meta": map[string]string{"k": "v"}},
		"blank type value":   {"type": "   "},
		// The conjunction: a naive "type is non-blank" guard passes this and
		// hands the plural-types arm — which threads no cursor — to the drain.
		"type plus higher-precedence types": {
			"type":  string(kgtypes.NodeFile),
			"types": []string{string(kgtypes.NodeFile)},
		},
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			cc := &captureCaller{}
			got, err := BrowseAllNodes(context.Background(), cc, kgtypes.GraphCode, "myrepo", extra)
			if err == nil {
				t.Fatalf("expected a refusal, got %d nodes and no error", len(got))
			}
			var unpageable *UnpageablePayloadError
			if !errors.As(err, &unpageable) {
				t.Fatalf("expected a typed UnpageablePayloadError, got %T: %v", err, err)
			}
			if unpageable.Key == "" {
				t.Errorf("the refusal must name the disqualifying key, got %q", unpageable.Key)
			}
			if len(cc.reqs) != 0 {
				t.Errorf("a refused payload must issue NO RPC, got %d", len(cc.reqs))
			}
		})
	}
}

// TestBrowseAllNodes_DrainsMultiplePages seeds more than one full page and
// asserts the drain returns every node — and that page one carried a SET BUT
// EMPTY cursor, which is what fails if after_id is omitted or marshaled nil.
func TestBrowseAllNodes_DrainsMultiplePages(t *testing.T) {
	wantNodes := paging.BrowsePageSize + 3

	seeded := make([]*knowledgev1.Node, 0, wantNodes)
	for i := range wantNodes {
		seeded = append(seeded, &knowledgev1.Node{
			Id:   fmt.Sprintf("n%04d", i),
			Type: string(kgtypes.NodeFile),
		})
	}
	if len(seeded) != wantNodes {
		t.Fatalf("fixture built %d nodes, want %d", len(seeded), wantNodes)
	}

	pc := &pagingCaller{nodes: seeded}
	got, err := BrowseAllNodes(context.Background(), pc, kgtypes.GraphCode, "myrepo", map[string]any{
		"type": string(kgtypes.NodeFile),
		// A caller's stale limit must not defeat the drain.
		"limit": 0,
	})
	if err != nil {
		t.Fatalf("BrowseAllNodes: %v", err)
	}
	if len(got) != wantNodes {
		t.Errorf("drain returned %d nodes, want %d (the fixture count, not a set-derived length)", len(got), wantNodes)
	}
	if len(pc.reqs) < 2 {
		t.Fatalf("a corpus larger than one page must take at least 2 round trips, got %d", len(pc.reqs))
	}
	first := pc.reqs[0].GetQuery()
	if first.AfterId == nil {
		t.Errorf("page 1 must SET the cursor: presence is what selects the keyset browse")
	} else if first.GetAfterId() != "" {
		t.Errorf("page 1's cursor must be EMPTY, got %q", first.GetAfterId())
	}
	if first.GetLimit() != int32(paging.BrowsePageSize) {
		t.Errorf("page limit = %d, want the shared page size %d", first.GetLimit(), paging.BrowsePageSize)
	}
}

// TestBrowseNodes_RejectsNonPositiveLimit is the catcher for the pass-through
// seam's bound. BrowseNodes serves ONE page, and a payload whose limit is absent
// or non-positive is the exact shape the compiler rewrites to browseDefaultLimit
// — so serving it hands back a silent default page instead of the set the caller
// asked for. The ZERO-RPC assertion is what separates a guard from a transport
// failure: a test asserting only "an error is returned" would not tell them apart.
func TestBrowseNodes_RejectsNonPositiveLimit(t *testing.T) {
	cases := map[string]map[string]any{
		"limit zero": {
			"type":  string(kgtypes.NodePattern),
			"limit": 0,
		},
		"limit key absent entirely": {
			"type": string(kgtypes.NodePattern),
		},
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			cc := &captureCaller{}
			got, err := BrowseNodes(context.Background(), cc, kgtypes.GraphPractice, "go", extra)
			if err == nil {
				t.Fatalf("expected a refusal, got %d nodes and no error", len(got))
			}
			if _, ok := errors.AsType[*UnboundedBrowseError](err); !ok {
				t.Fatalf("expected a typed UnboundedBrowseError, got %T: %v", err, err)
			}
			if len(cc.reqs) != 0 {
				t.Errorf("a refused payload must issue NO RPC, got %d", len(cc.reqs))
			}
		})
	}
}

// TestBrowseNodes_AllowsByIDPayload is the catcher for the guard's by-id
// exemption: "ids" and "id" take the bulk-ids and by-id compile arms, which never
// reach applyBrowseLimitOffset, so no browse default applies and no limit is
// owed. Without the exemption the k8s external-refs lookupNode read breaks in
// production while every other test stays green. It is also the KNOWN POSITIVE
// for the refusal test above — the case that proves the seam refuses a shape
// rather than refusing everything.
func TestBrowseNodes_AllowsByIDPayload(t *testing.T) {
	cc := &captureCaller{}
	if _, err := BrowseNodes(context.Background(), cc, kgtypes.GraphPractice, "go", map[string]any{
		"ids": []string{"i-0abc"},
	}); err != nil {
		t.Fatalf("a by-id payload must be served without a limit, got %v", err)
	}
	if len(cc.reqs) != 1 {
		t.Fatalf("the by-id read must issue exactly 1 Execute, got %d", len(cc.reqs))
	}
}
