// SPDX-License-Identifier: Apache-2.0

// Package kgtypes is the client-private copy of the node/edge/graph const
// vocabulary plus the NodeType classification methods and the metadata-accessor
// helpers over *knowledgev1.Node. It imports ONLY gen/knowledge/v1 so the
// client can read/write wire-node metadata and classify node types without
// reaching for any storage engine. It carries vocab + classification +
// node_value + metadata-keys + IsTombstoned — NOT the server-only
// eligibility/AutoSummary/EmbedText cluster, which the client never calls.
package kgtypes

// NodeType classifies a node in the graph. It stays a DEFINED string type (NOT a
// `= string` alias) under the value-embed flip: the ~50
// vocabulary consts below AND the NodeType methods (IsCodeType / ShouldEmbed /
// Summarizable / …) both survive — Go forbids methods on an alias to a predeclared
// type, so a true `= string` alias is incompatible with those methods. The embedded
// knowledgev1.Node.Type proto field is a plain `string`; NodeType-typed values are
// cast at that boundary (NodeType(n.Type) to read, string(t) / direct since untyped
// consts are assignable to write). This preserves the decision's actual goal — the
// const vocabulary with zero const-definition churn — while keeping the methods.
type NodeType string

// EdgeType classifies a relationship between two nodes. It stays a DEFINED string
// type (NOT a `= string` alias) under the value-embed flip — the
// ~120 vocabulary consts below survive with zero churn, and EdgeType-typed values
// are cast at the knowledgev1.Edge.Type proto string-field boundary (EdgeType(e.Type)
// to read; untyped const assignment to write). A true alias is unnecessary here since
// EdgeType is method-free, but keeping it a defined type stays symmetric with
// NodeType (which REQUIRES the defined form for its methods) and avoids silent
// cross-vocabulary assignment bugs the named type guards against.
type EdgeType string

// GraphType identifies which graph is being addressed.
type GraphType string
