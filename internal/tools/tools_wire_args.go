// SPDX-License-Identifier: Apache-2.0

package tools

// queryArgs / searchArgs / traverseArgs mirror the server-side wire shapes
// (cmd/knowledge-server/tools/tools_query_args.go,
// tools_search_args.go, tools_traverse.go). THEY ARE DUPLICATED RATHER THAN
// SHARED because the cmd/knowledge-server tools package and
// cmd/knowledge/internal/tools cannot import each other: that direction is
// the client/server split, and a hand-written shared Go package is the one
// thing the split forbids.
//
// THEY LIVE IN A WIRE-NAMED FILE, NOT A FEATURE-NAMED ONE. These mirrors
// first landed beside the client-side log-tool dispatchers and were named for
// them, which made a filename glob read as a dependency boundary: 47 files
// with nothing to do with logs decode their payloads through queryArgs. When
// the log tools were deleted the glob would have taken the whole client's
// query/search/traverse decode with it. The file name now says what the
// contents are, so the next deletion of a feature cannot reach them by
// accident.

// queryArgs is the client-side mirror of the server queryArgs struct. Every
// client-side interception of a query call decodes into it.
type queryArgs struct {
	Graph         string   `json:"graph"`
	Name          string   `json:"name"`
	ID            string   `json:"id"`
	IDs           []string `json:"ids,omitempty"`
	Text          string   `json:"text"`
	Queries       []string `json:"queries"`
	Type          string   `json:"type"`
	Types         []string `json:"types,omitempty"`
	Status        string   `json:"status"`
	PathPrefix    string   `json:"path_prefix"`
	PathPrefixes  []string `json:"path_prefixes"`
	Mode          string   `json:"mode"`
	ValenceMin    *float64 `json:"valence_min"`
	ValenceMax    *float64 `json:"valence_max"`
	MagnitudeMin  *float64 `json:"magnitude_min"`
	ConsistMax    *float64 `json:"consistency_max"`
	Session       string   `json:"session"`
	ConnectedTo   string   `json:"connected_to"`
	IncludeSource *bool    `json:"include_source"`
	IncludeEdges  *bool    `json:"include_edges"`
	GroupByFile   *bool    `json:"group_by_file"`
	Repo          string   `json:"repo"`
	Repos         []string `json:"repos"`
	Language      string   `json:"language"`
	// Source names a practice SOURCE HUB and narrows a practice read to the
	// nodes grouped under it; omitted means the whole combined graph. It lowers
	// onto the `source_hub` metadata key, never onto the wire selector.
	Source            string            `json:"source"`
	Account           string            `json:"account"`
	ResourceType      string            `json:"resource_type"`
	Limit             flexInt           `json:"limit"`
	Offset            flexInt           `json:"offset"`
	Cluster           string            `json:"cluster"`
	ClusterA          string            `json:"cluster_a"`
	ClusterB          string            `json:"cluster_b"`
	Since             string            `json:"since"`
	Action            string            `json:"action"`
	Target            string            `json:"target"`
	Polarity          string            `json:"polarity"`
	Weight            flexFloat         `json:"weight"`
	IncludeCrossLinks *bool             `json:"include_cross_links"`
	Algorithm         string            `json:"algorithm"`
	TopK              flexInt           `json:"top_k"`
	Extra             map[string]string `json:"extra"`
	Meta              map[string]string `json:"meta,omitempty"`
	Fields            []string          `json:"fields,omitempty"`
	Rows              string            `json:"rows"`
	Cols              string            `json:"cols"`
	Format            string            `json:"format"`
	Samples           bool              `json:"samples"`
	EdgeType          []string          `json:"edge_type"`
	TimeField         string            `json:"time_field"`
	IncludeTombstones bool              `json:"include_tombstones"`
	IncludeTests      *bool             `json:"include_tests,omitempty"`
	TestKinds         []string          `json:"test_kinds,omitempty"`
	QueryVector       string            `json:"query_vector,omitempty"`
}

// searchArgs is the client-side mirror of the server searchArgs struct
// (cmd/knowledge-server/tools/tools_search_args.go). InterceptSearch unmarshals
// the search payload into this as `sniff`: the mode:"similar" claim reads Mode
// (gate), NodeID (the node whose stored vector seeds the search), and Fields
// (render projection), and the graph-arm dispatch reads Query / Queries /
// Graph / Name / Limit / Format. The other wire fields are decoded into the
// per-arm arg structs downstream. Kept aligned with the wire shape so future
// drift is loud.
type searchArgs struct {
	Query   string   `json:"query"`
	Queries []string `json:"queries"`
	Graph   string   `json:"graph"`
	Name    string   `json:"name"`
	Limit   flexInt  `json:"limit"`
	Format  string   `json:"format"`
	Mode    string   `json:"mode"`
	NodeID  string   `json:"node_id"`
	Fields  []string `json:"fields,omitempty"`
}

// traverseArgs is the client-side mirror of the server traverseArgs struct.
type traverseArgs struct {
	Start               string   `json:"start"`
	Direction           string   `json:"direction"`
	Depth               flexInt  `json:"depth"`
	Limit               flexInt  `json:"limit"`
	EdgeTypes           []string `json:"edge_types"`
	Graph               string   `json:"graph"`
	Name                string   `json:"name"`
	Language            string   `json:"language"`
	Source              string   `json:"source"`
	Account             string   `json:"account"`
	Repo                string   `json:"repo"`
	Branch              string   `json:"branch"`
	IncludeEdgeMetadata bool     `json:"include_edge_metadata"`
	Format              string   `json:"format"`
	IncludeTombstones   bool     `json:"include_tombstones"`
}
