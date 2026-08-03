// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// queryArgs / searchArgs / traverseArgs mirror the server-side wire shapes
// (cmd/knowledge-server/tools/tools_query_args.go,
// tools_search_args.go, tools_traverse.go). Duplicated because the
// cmd/knowledge-server tools package and cmd/knowledge/internal/tools
// cannot import each other (wrong direction for the client/server split).
//
// These mirrors were introduced so the log-tool dispatchers moved
// client-side keep parsing the same wire fields as before. Production tool
// calls land here via the InterceptLogsQuery / InterceptLogsManage /
// InterceptLogsTraversal chain steps; reaching the server with one of
// these args means an older client is talking to a newer server.

// queryArgs is the client-side mirror of the server queryArgs struct used
// by the moved log query handlers.
type queryArgs struct {
	Graph             string            `json:"graph"`
	Name              string            `json:"name"`
	ID                string            `json:"id"`
	IDs               []string          `json:"ids,omitempty"`
	Text              string            `json:"text"`
	Queries           []string          `json:"queries"`
	Type              string            `json:"type"`
	Types             []string          `json:"types,omitempty"`
	Status            string            `json:"status"`
	PathPrefix        string            `json:"path_prefix"`
	PathPrefixes      []string          `json:"path_prefixes"`
	Mode              string            `json:"mode"`
	ValenceMin        *float64          `json:"valence_min"`
	ValenceMax        *float64          `json:"valence_max"`
	MagnitudeMin      *float64          `json:"magnitude_min"`
	ConsistMax        *float64          `json:"consistency_max"`
	Session           string            `json:"session"`
	ConnectedTo       string            `json:"connected_to"`
	IncludeSource     *bool             `json:"include_source"`
	IncludeEdges      *bool             `json:"include_edges"`
	GroupByFile       *bool             `json:"group_by_file"`
	Repo              string            `json:"repo"`
	Repos             []string          `json:"repos"`
	Language          string            `json:"language"`
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
// the search payload into this as `sniff`: the graph=logs short-circuit reads
// Query / Queries / Graph / Name / Limit / Format, and the mode:"similar" claim
// additionally reads Mode (gate), NodeID (the node whose stored vector seeds the
// search), and Fields (render projection). The other wire fields are decoded into
// the per-arm arg structs downstream. Kept aligned with the wire shape so future
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
	Account             string   `json:"account"`
	Repo                string   `json:"repo"`
	Branch              string   `json:"branch"`
	IncludeEdgeMetadata bool     `json:"include_edge_metadata"`
	Format              string   `json:"format"`
	IncludeTombstones   bool     `json:"include_tombstones"`
}

// flexFloat is the client-side mirror of the server flexFloat type.
// Lives here (not flex_types.go) to keep the mirror block colocated
// with the rest of the moved-handler scaffolding.
type flexFloat float64

// UnmarshalJSON mirrors the server-side helper: accept both raw numbers
// and quoted-string forms for LLMs that double-encode.
func (f *flexFloat) UnmarshalJSON(data []byte) error {
	var n float64
	if err := json.Unmarshal(data, &n); err == nil {
		*f = flexFloat(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		parsed, perr := strconv.ParseFloat(s, 64)
		if perr != nil {
			return fmt.Errorf("flexFloat: cannot parse %q as float64", s)
		}
		*f = flexFloat(parsed)
		return nil
	}
	return fmt.Errorf("flexFloat: cannot unmarshal %s", string(data))
}
