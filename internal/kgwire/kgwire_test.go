// SPDX-License-Identifier: Apache-2.0

package kgwire

import (
	"testing"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestBatchEdgeToProto_FullFieldSet(t *testing.T) {
	ts := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	e := BatchEdge{
		FromIdx:       3,
		ToIdx:         -1,
		FromID:        "from-node",
		ToID:          "to-node",
		Type:          kgtypes.EdgeCalls,
		Weight:        1.5,
		Confidence:    0.75,
		Method:        "cloud-collect",
		Evidence:      "{\"k\":\"v\"}",
		LastValidated: ts,
	}
	p := e.ToProto()
	if p.FromIdx != 3 || p.ToIdx != -1 {
		t.Errorf("idx mismatch: got from=%d to=%d", p.FromIdx, p.ToIdx)
	}
	if p.FromId != "from-node" || p.ToId != "to-node" {
		t.Errorf("id mismatch: got from=%q to=%q", p.FromId, p.ToId)
	}
	if p.Type != string(kgtypes.EdgeCalls) {
		t.Errorf("type mismatch: got %q want %q", p.Type, kgtypes.EdgeCalls)
	}
	if p.Weight != 1.5 || p.Confidence != 0.75 {
		t.Errorf("weight/confidence mismatch: got w=%v c=%v", p.Weight, p.Confidence)
	}
	if p.Method != "cloud-collect" || p.Evidence != "{\"k\":\"v\"}" {
		t.Errorf("method/evidence mismatch: got m=%q e=%q", p.Method, p.Evidence)
	}
	if p.LastValidated != ts.UnixNano() {
		t.Errorf("last_validated mismatch: got %d want %d", p.LastValidated, ts.UnixNano())
	}
}

func TestBatchEdgeToProto_ZeroTimeIsUnset(t *testing.T) {
	if got := (BatchEdge{}).ToProto().LastValidated; got != 0 {
		t.Errorf("zero time should encode to 0, got %d", got)
	}
}

func TestBatchEdgesToProto_EmptyIsNil(t *testing.T) {
	if got := BatchEdgesToProto(nil); got != nil {
		t.Errorf("nil input should yield nil, got %v", got)
	}
	if got := BatchEdgesToProto([]BatchEdge{}); got != nil {
		t.Errorf("empty input should yield nil, got %v", got)
	}
}

func TestBatchEdgesToProto_PreservesOrder(t *testing.T) {
	in := []BatchEdge{{FromID: "a"}, {FromID: "b"}}
	out := BatchEdgesToProto(in)
	if len(out) != 2 || out[0].FromId != "a" || out[1].FromId != "b" {
		t.Fatalf("order not preserved: %+v", out)
	}
}

func TestEdgeDirectionString(t *testing.T) {
	cases := map[EdgeDirection]string{
		OutgoingEdges:    "outgoing",
		IncomingEdges:    "incoming",
		BothEdges:        "both",
		EdgeDirection(9): "unknown",
	}
	for d, want := range cases {
		if got := d.String(); got != want {
			t.Errorf("EdgeDirection(%d).String() = %q, want %q", int(d), got, want)
		}
	}
}

// proxyNode builds a wire proxy node carrying the given metadata.
func proxyNode(id string, meta map[string]string) *knowledgev1.Node {
	return &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeProxy), Metadata: meta}
}

func TestIsProxy(t *testing.T) {
	if IsProxy(nil) {
		t.Error("nil node should not be a proxy")
	}
	if IsProxy(&knowledgev1.Node{Type: "finding"}) {
		t.Error("non-proxy node should not be a proxy")
	}
	if !IsProxy(proxyNode("p1", nil)) {
		t.Error("proxy node should be a proxy")
	}
}

func TestIsBranchProxy(t *testing.T) {
	// Only foreign_graph="main" is a branch proxy.
	if !IsBranchProxy(proxyNode("p1", map[string]string{"foreign_graph": "main"})) {
		t.Error("foreign_graph=main should be a branch proxy")
	}
	if IsBranchProxy(proxyNode("p2", map[string]string{"foreign_graph": "code"})) {
		t.Error("foreign_graph=code should NOT be a branch proxy")
	}
	if IsBranchProxy(proxyNode("p3", nil)) {
		t.Error("proxy without foreign_graph should NOT be a branch proxy")
	}
	if IsBranchProxy(&knowledgev1.Node{Type: "finding", Metadata: map[string]string{"foreign_graph": "main"}}) {
		t.Error("non-proxy node should never be a branch proxy")
	}
}

func TestProxyInfo_FivePatterns(t *testing.T) {
	tests := []struct {
		name string
		node *knowledgev1.Node
		want *knowledgev1.ProxyTarget
	}{
		{
			name: "main->overlay+id",
			node: proxyNode("ov-1", map[string]string{"foreign_graph": "main", "overlay": "feature-x"}),
			want: &knowledgev1.ProxyTarget{Name: "feature-x", NodeId: "ov-1"},
		},
		{
			name: "code->GraphCode+repo+foreign_id",
			node: proxyNode("p", map[string]string{"foreign_graph": "code", "repo": "knowledge", "foreign_id": "fn-1"}),
			want: &knowledgev1.ProxyTarget{GraphType: string(kgtypes.GraphCode), Name: "knowledge", NodeId: "fn-1"},
		},
		{
			name: "cloud->GraphCloud+account+foreign_id",
			node: proxyNode("p", map[string]string{"foreign_graph": "cloud", "account": "acct-1", "foreign_id": "res-1"}),
			want: &knowledgev1.ProxyTarget{GraphType: string(kgtypes.GraphCloud), Name: "acct-1", NodeId: "res-1"},
		},
		{
			name: "practice->GraphPractice+foreign_id",
			node: proxyNode("p", map[string]string{"foreign_graph": "practice", "foreign_id": "pat-1"}),
			want: &knowledgev1.ProxyTarget{GraphType: string(kgtypes.GraphPractice), NodeId: "pat-1"},
		},
		{
			name: "repo-fallback->GraphCode",
			node: proxyNode("p", map[string]string{"repo": "knowledge", "foreign_id": "sym-1"}),
			want: &knowledgev1.ProxyTarget{GraphType: string(kgtypes.GraphCode), Name: "knowledge", NodeId: "sym-1"},
		},
		{
			name: "foreign_id-fallback->GraphKnowledge",
			node: proxyNode("p", map[string]string{"foreign_id": "kn-1"}),
			want: &knowledgev1.ProxyTarget{GraphType: string(kgtypes.GraphKnowledge), NodeId: "kn-1"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ProxyInfo(tc.node)
			if got == nil {
				t.Fatalf("ProxyInfo returned nil, want %+v", tc.want)
			}
			if got.GetGraphType() != tc.want.GetGraphType() || got.GetName() != tc.want.GetName() || got.GetNodeId() != tc.want.GetNodeId() {
				t.Errorf("ProxyInfo mismatch: got {gt=%q name=%q id=%q} want {gt=%q name=%q id=%q}",
					got.GetGraphType(), got.GetName(), got.GetNodeId(),
					tc.want.GetGraphType(), tc.want.GetName(), tc.want.GetNodeId())
			}
		})
	}
}

func TestProxyInfo_NilForNonProxyOrUnrecognized(t *testing.T) {
	if got := ProxyInfo(nil); got != nil {
		t.Errorf("nil node should yield nil, got %+v", got)
	}
	if got := ProxyInfo(&knowledgev1.Node{Type: "finding"}); got != nil {
		t.Errorf("non-proxy node should yield nil, got %+v", got)
	}
	if got := ProxyInfo(proxyNode("p", nil)); got != nil {
		t.Errorf("proxy with no recognizable metadata should yield nil, got %+v", got)
	}
}
