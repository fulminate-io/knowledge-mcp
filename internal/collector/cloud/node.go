// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"encoding/json"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// BuildNode creates a *knowledgev1.Node from a ResourceSpec. This is the single
// canonical way to turn a cloud resource into a graph node. All cloud
// subcollectors produce ResourceSpec values; this function maps them into
// the knowledge graph's node format. The typed proto wire is the sole node
// carrier — the client constructs the wire node directly
// and writes metadata via the kgtypes free funcs.
func BuildNode(spec ResourceSpec) *knowledgev1.Node {
	n := &knowledgev1.Node{
		Id:         spec.ID,
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: spec.Name,
		Content:    string(spec.Content),
		Source:     "cloud",
	}

	kgtypes.SetValue(n, "resource_type", spec.ResourceType)
	if spec.Region != "" {
		kgtypes.SetValue(n, "region", spec.Region)
	}
	for k, v := range spec.Metadata {
		kgtypes.SetValue(n, k, v)
	}

	// Deterministic Summary string for downstream search/embedding.
	// Helpers register per-ResourceType via init() in summarize_<basename>.go
	// siblings; unregistered types fall back to a generic "<rt> <name> in <region>".
	n.Summary = Summarize(spec)

	return n
}

// BuildEdge creates a kgwire.BatchEdge from an EdgeSpec. This is the single
// canonical way to turn a cloud relationship into a graph edge. FromIdx and
// ToIdx are set to -1 because cloud IDs are real provider identifiers known
// at collection time, not batch indices.
//
// When EdgeSpec.Metadata is non-nil and non-empty, it is JSON-marshaled into
// BatchEdge.Evidence and Method is set to "cloud-collect". Nil or empty
// Metadata leaves both fields empty for backward compatibility.
func BuildEdge(spec EdgeSpec) kgwire.BatchEdge {
	be := kgwire.BatchEdge{
		FromIdx: -1,
		ToIdx:   -1,
		FromID:  spec.SourceID,
		ToID:    spec.TargetID,
		Type:    spec.Relationship,
	}
	if len(spec.Metadata) > 0 {
		if raw, err := json.Marshal(spec.Metadata); err == nil {
			be.Evidence = string(raw)
			be.Method = "cloud-collect"
		}
	}
	return be
}
