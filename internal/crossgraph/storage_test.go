// SPDX-License-Identifier: Apache-2.0

package crossgraph

import (
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

func TestStorageQualifiedLinks(t *testing.T) {
	for _, pair := range []struct{ from, to string }{{"local", "local"}, {"local", "cloud"}, {"cloud", "cloud"}, {"cloud", "local"}} {
		t.Run(pair.from+"-"+pair.to, func(t *testing.T) {
			address := func(storage, id string) string {
				d := graphclient.Destination{Storage: storage}
				if storage == "cloud" {
					d.AccountID = "a"
				}
				s, err := (graphclient.Reference{Destination: d, Graph: "knowledge", ID: id}).Encode()
				require.NoError(t, err)
				return s
			}
			f := &fakeCaller{nodesByGraph: map[string]map[string]*knowledgev1.Node{"knowledge": {
				"from": {Id: "from", Type: "finding"}, "to": {Id: "to", Type: "finding"},
			}}}
			handled, _, err := ResolveAndLink(t.Context(), f, f, LinkRequest{From: address(pair.from, "from"), To: address(pair.to, "to"), Relationship: "relates-to", Stats: f.Stats})
			require.True(t, handled)
			if pair.from == "cloud" && pair.to == "local" {
				require.Error(t, err)
				require.Empty(t, f.plans)
				return
			}
			require.NoError(t, err)
			var mutations []*knowledgev1.MutationPlan
			for _, p := range f.plans {
				if p.GetMutation() != nil {
					mutations = append(mutations, p.GetMutation())
				}
			}
			if pair.from != pair.to {
				require.Len(t, mutations, 2)
				require.Equal(t, address(pair.to, "to"), mutations[0].GetNodeBodies()[0].GetMetadata()["storage_reference"])
			} else {
				require.Len(t, mutations, 1)
			}
			require.Equal(t, "from", mutations[len(mutations)-1].GetSelection().GetIds()[0])
		})
	}
}
