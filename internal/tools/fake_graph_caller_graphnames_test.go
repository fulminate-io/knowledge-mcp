// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// The fakeGraphCaller's RETURN_MODE_GRAPH_NAMES serving helpers live here
// (file-length cap; same package as backend_lookup_test.go).

// execGraphNames serves a per-type RETURN_MODE_GRAPH_NAMES read by decoding the
// seeded listGraphsResult body ({graphs:[{graph_type,graph_name}]}) and emitting
// only the entries matching graphType, projected to the graph_names_json
// []store.GraphInfo carrier. This bridges the old single pipeline_list_graphs
// Call seeding to the new per-type Execute reads listForeignGraphs / repo
// resolver / cloud-cicd overview now issue. An absent listGraphsResult → empty.
func (f *fakeGraphCaller) execGraphNames(graphType string) (*knowledgev1.ExecuteResponse, error) {
	var infos []*knowledgev1.GraphInfo
	if f.listGraphsResult != nil && len(f.listGraphsResult.Content) > 0 {
		var decoded struct {
			Graphs []struct {
				GraphType string `json:"graph_type"`
				GraphName string `json:"graph_name"`
			} `json:"graphs"`
		}
		_ = json.Unmarshal([]byte(f.listGraphsResult.Content[0].Text), &decoded)
		for _, g := range decoded.Graphs {
			if g.GraphType == graphType && g.GraphName != "" {
				infos = append(infos, &knowledgev1.GraphInfo{Name: g.GraphName})
			}
		}
	}
	return &knowledgev1.ExecuteResponse{GraphNames: infos}, nil
}

// execOverlayKeys serves the overlay_of RETURN_MODE_GRAPH_NAMES read: the seeded
// "base@overlay" keys for the requested base (overlayKeysByBase). Absent → empty,
// mirroring a base with no overlays.
func (f *fakeGraphCaller) execOverlayKeys(base string) (*knowledgev1.ExecuteResponse, error) {
	var infos []*knowledgev1.GraphInfo
	for _, key := range f.overlayKeysByBase[base] {
		if key != "" {
			infos = append(infos, &knowledgev1.GraphInfo{Name: key})
		}
	}
	return &knowledgev1.ExecuteResponse{GraphNames: infos}, nil
}

// targetNameDiscriminant returns the name discriminant a GraphSelector carries
// (Repo/Account/Language/Name), used by the per-target mutate-error knob to
// identify which graph a clear UPDATE is hitting. A nil selector → "".
func targetNameDiscriminant(t *knowledgev1.GraphSelector) string {
	if t == nil {
		return ""
	}
	switch {
	case t.GetRepo() != "":
		return t.GetRepo()
	case t.GetAccount() != "":
		return t.GetAccount()
	case t.GetLanguage() != "":
		return t.GetLanguage()
	default:
		return t.GetName()
	}
}
