// SPDX-License-Identifier: Apache-2.0

package graphtypecrud

import (
	"testing"

	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fullGraphTypeDef returns a fully populated record so the round-trip covers
// every proto field at once: set + unset optional bools, repeated lists, the
// param_schema + node_types maps, and the forward-compat extra map.
func fullGraphTypeDef() *knowledgev1.GraphTypeDef {
	return &knowledgev1.GraphTypeDef{
		Name: "jira",
		Collector: &knowledgev1.CollectorSpec{
			BinaryPath:     "/usr/local/bin/jira-collector",
			ParamTransport: "flag:--config",
			ParamSchema: map[string]*knowledgev1.ParamSpec{
				"project": {Type: "string", Required: true},
				"since":   {Type: "string", Required: false},
			},
		},
		Behavior: &knowledgev1.BehaviorDefaults{
			Syncable:        new(true),
			Summarizable:    new(false),
			Embeddable:      nil, // unset — distinguishable from explicit false
			EmbedFields:     []string{"description", "comments"},
			SummarizeFields: []string{"description"},
			Bm25Fields:      []string{"title", "description"},
			Extra:           map[string]string{"overlay": "true", "ttl_days": "30"},
		},
		NodeTypes: map[string]*knowledgev1.NodeTypeOverride{
			"issue": {
				Summarizable:    new(true),
				Embeddable:      nil,
				EmbedFields:     []string{"title"},
				SummarizeFields: nil,
				Bm25Fields:      []string{"title"},
			},
			"comment": {
				Embeddable: new(false),
			},
		},
	}
}

// TestToNode_FromNode_RoundTrip pins the lossless codec contract: a fully
// populated GraphTypeDef survives ToNode -> FromNode unchanged, including
// optional-bool presence (set true, set false, and unset), repeated lists, both
// maps, and the extra map. proto.Equal compares the decoded proto by value.
func TestToNode_FromNode_RoundTrip(t *testing.T) {
	in := fullGraphTypeDef()

	node, err := ToNode(in, "Jira issue tracker graph")
	if err != nil {
		t.Fatalf("ToNode: %v", err)
	}

	// Node identity follows the name-as-id config-node convention.
	if node.GetId() != in.GetName() {
		t.Errorf("node Id = %q, want %q", node.GetId(), in.GetName())
	}
	if node.GetSymbolName() != in.GetName() {
		t.Errorf("node SymbolName = %q, want %q", node.GetSymbolName(), in.GetName())
	}
	if node.GetType() != string(kgtypes.NodeGraphTypeDef) {
		t.Errorf("node Type = %q, want %q", node.GetType(), kgtypes.NodeGraphTypeDef)
	}
	if node.GetDescription() != "Jira issue tracker graph" {
		t.Errorf("node Description = %q, want the supplied one-liner", node.GetDescription())
	}
	// The body rides as exactly one blob key — not scattered.
	if _, ok := node.GetMetadata()[MetaGraphTypeDefPB]; !ok {
		t.Fatalf("node metadata missing blob key %q", MetaGraphTypeDefPB)
	}
	if len(node.GetMetadata()) != 1 {
		t.Errorf("node metadata has %d keys, want exactly 1 (the blob)", len(node.GetMetadata()))
	}

	out, err := FromNode(node)
	if err != nil {
		t.Fatalf("FromNode: %v", err)
	}
	if !proto.Equal(in, out) {
		t.Errorf("round-trip mismatch:\n in = %v\nout = %v", in, out)
	}

	// Explicit presence assertions: unset stays nil, explicit false stays false.
	if out.GetBehavior().Embeddable != nil {
		t.Errorf("behavior.Embeddable should round-trip as unset (nil), got %v", *out.GetBehavior().Embeddable)
	}
	if out.GetBehavior().Summarizable == nil || *out.GetBehavior().Summarizable != false {
		t.Errorf("behavior.Summarizable should round-trip as explicit false")
	}
	if out.GetBehavior().Syncable == nil || *out.GetBehavior().Syncable != true {
		t.Errorf("behavior.Syncable should round-trip as explicit true")
	}
}

// TestToNode_NilRejected guards the nil input path.
func TestToNode_NilRejected(t *testing.T) {
	if _, err := ToNode(nil, ""); err == nil {
		t.Fatal("ToNode(nil) should error")
	}
}

// TestFromNode_Rejections covers the type-guard and missing-key paths.
func TestFromNode_Rejections(t *testing.T) {
	if _, err := FromNode(nil); err == nil {
		t.Fatal("FromNode(nil) should error")
	}

	// Wrong node type.
	wrongType := &knowledgev1.Node{
		Type:     string(kgtypes.NodeLogBackend),
		Metadata: map[string]string{MetaGraphTypeDefPB: "AAAA"},
	}
	if _, err := FromNode(wrongType); err == nil {
		t.Fatal("FromNode with wrong node type should error")
	}

	// Missing blob key.
	noBlob := &knowledgev1.Node{Type: string(kgtypes.NodeGraphTypeDef)}
	if _, err := FromNode(noBlob); err == nil {
		t.Fatal("FromNode without the blob key should error")
	}

	// Corrupt base64.
	badB64 := &knowledgev1.Node{
		Type:     string(kgtypes.NodeGraphTypeDef),
		Metadata: map[string]string{MetaGraphTypeDefPB: "not!valid!base64!"},
	}
	if _, err := FromNode(badB64); err == nil {
		t.Fatal("FromNode with corrupt base64 should error")
	}
}

// TestToNode_EmptyRoundTrip covers the minimal record (name only).
func TestToNode_EmptyRoundTrip(t *testing.T) {
	in := &knowledgev1.GraphTypeDef{Name: "minimal"}
	node, err := ToNode(in, "")
	if err != nil {
		t.Fatalf("ToNode: %v", err)
	}
	out, err := FromNode(node)
	if err != nil {
		t.Fatalf("FromNode: %v", err)
	}
	if !proto.Equal(in, out) {
		t.Errorf("minimal round-trip mismatch:\n in = %v\nout = %v", in, out)
	}
}
