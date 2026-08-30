// SPDX-License-Identifier: Apache-2.0

// Package graphtypecrud holds the client-side CRUD surface for the
// user-registered graph-type configuration record (NodeGraphTypeDef).
// A graph-type registration is the combined record that defines how to collect
// a new arbitrary graph type (the external collector binary) AND how the system
// should treat its graph (summary/embed/sync behavior). It is persisted as a
// per-account, graph-resident config node — mirroring the NodeLogBackend
// config-node idiom — so it is the single source of truth read
// by BOTH the client (T3 collector dispatch) and the server (T2 behavior
// resolver).
//
// STORAGE SHAPE — deliberate deviation from the scattered metadata-map idiom
// the other config-node codecs use. Those scatter each field across its own
// inline metadata key because their fields are individually inspected by
// list/status renderers. A GraphTypeDef is NOT: it is pure stored config
// consumed only as a whole (by the T2 resolver and T3 dispatch), and the config
// node opts out of summary/embed/BM25 entirely (see the server eligibility
// tables), so no individual field is ever searched. We therefore persist the
// record as ONE serialized-proto blob (proto.Marshal, base64-encoded) under a
// single metadata key. This is the first place in the repo to store serialized
// proto bytes in a node; the justification is the cross-module no-duplicate-parse
// requirement: both the client and the server decode the SAME proto with a
// single proto.Unmarshal of the same key, so there is exactly one decode path
// and zero shared metadata-key vocabulary to drift on. A field->key mapping
// would re-introduce per-side key-spelling duplication — exactly the drift the
// single-accessor requirement exists to prevent.
package graphtypecrud

import (
	"encoding/base64"
	"fmt"

	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// MetaGraphTypeDefPB is the single metadata key holding the base64-encoded
// serialized GraphTypeDef proto on a NodeGraphTypeDef node. It is the
// cross-module source of truth both sides read: the client writer (this
// package) and the T2 server reader reference this one literal. The proto file's
// leading comment documents the same key for the server side, which imports
// nothing client-internal.
const MetaGraphTypeDefPB = "graph_type_def_pb"

// graphTypeSource is the backend-neutral Source string stamped on a registered
// graph-type node, following the "<record>:<verb>" config-node convention.
const graphTypeSource = "graph_type:register"

// ToNode marshals d into a *knowledgev1.Node ready for upsert. The record body
// rides as a single base64 serialized-proto blob under MetaGraphTypeDefPB; node
// identity (Id/SymbolName = Name, Type = NodeGraphTypeDef) follows the
// name-as-id config-node convention so the caller need not round-trip through ByID before deciding
// create vs update. description, when supplied, is a human-facing one-liner; the
// record body is NOT carried in Description.
func ToNode(d *knowledgev1.GraphTypeDef, description string) (*knowledgev1.Node, error) {
	if d == nil {
		return nil, fmt.Errorf("graphtypecrud: ToNode: nil GraphTypeDef")
	}
	raw, err := proto.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("graphtypecrud: ToNode: marshal proto: %w", err)
	}
	name := d.GetName()
	return &knowledgev1.Node{
		Id:          name,
		Type:        string(kgtypes.NodeGraphTypeDef),
		SymbolName:  name,
		Source:      graphTypeSource,
		Description: description,
		Metadata: map[string]string{
			MetaGraphTypeDefPB: base64.StdEncoding.EncodeToString(raw),
		},
	}, nil
}

// FromNode is the inverse of ToNode: it reads the single base64 blob key off n,
// decodes it, and proto.Unmarshals it into a *knowledgev1.GraphTypeDef. It
// type-guards the node so a mistyped node fails loudly rather
// than decoding garbage.
func FromNode(n *knowledgev1.Node) (*knowledgev1.GraphTypeDef, error) {
	if n == nil {
		return nil, fmt.Errorf("graphtypecrud: FromNode: nil node")
	}
	if kgtypes.NodeType(n.GetType()) != kgtypes.NodeGraphTypeDef {
		return nil, fmt.Errorf("graphtypecrud: FromNode: expected type %q, got %q", kgtypes.NodeGraphTypeDef, n.GetType())
	}
	enc := kgtypes.Value(n, MetaGraphTypeDefPB)
	if enc == "" {
		return nil, fmt.Errorf("graphtypecrud: FromNode: missing %q metadata key", MetaGraphTypeDefPB)
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, fmt.Errorf("graphtypecrud: FromNode: decode base64: %w", err)
	}
	d := &knowledgev1.GraphTypeDef{}
	if err := proto.Unmarshal(raw, d); err != nil {
		return nil, fmt.Errorf("graphtypecrud: FromNode: unmarshal proto: %w", err)
	}
	return d, nil
}
