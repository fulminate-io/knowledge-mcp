// SPDX-License-Identifier: Apache-2.0

package web

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// v1Snapshot caches every node produced for the v1 fixture, keyed by
// type, so the per-subtest assertions can filter without re-walking
// the graph. Also retains the set of EdgeReferences outbound from
// each page, keyed by page ID → list of absolute URLs classified as
// internal on the edge Evidence. Split out from integration_v1_test.go
// so each file stays under the 300 LOC recommended cap.
type v1Snapshot struct {
	srvBase         string
	byType          map[string][]*knowledgev1.Node
	internalRefURLs map[string][]string // pageID → absolute URLs with rel=internal
}

func (s *v1Snapshot) requireSeedPage(t *testing.T) *knowledgev1.Node {
	t.Helper()
	for i := range s.byType["page"] {
		p := s.byType["page"][i]
		if strings.HasSuffix(kgtypes.Value(p, "url"), "/seed.html") {
			return p
		}
	}
	t.Fatalf("seed page not found; pages=%v", s.pagesSummary())
	return nil
}

func (s *v1Snapshot) findSectionByHeading(heading string) *knowledgev1.Node {
	for i := range s.byType["section"] {
		n := s.byType["section"][i]
		if n.SymbolName == heading {
			return n
		}
	}
	return nil
}

// paragraphInlineEmphasisWithTags returns the raw inline_emphasis JSON
// of the first paragraph whose emphasis-tag sequence matches want
// exactly, or "" if none match. Tag order matters so callers can
// assert DOM order indirectly.
func (s *v1Snapshot) paragraphInlineEmphasisWithTags(want []string) string {
	for _, n := range s.byType["paragraph"] {
		raw := kgtypes.Value(n, "inline_emphasis")
		if raw == "" {
			continue
		}
		var emphs []struct {
			Tag string `json:"tag"`
		}
		if err := json.Unmarshal([]byte(raw), &emphs); err != nil {
			continue
		}
		if len(emphs) != len(want) {
			continue
		}
		match := true
		for i, w := range want {
			if emphs[i].Tag != w {
				match = false
				break
			}
		}
		if match {
			return raw
		}
	}
	return ""
}

func (s *v1Snapshot) findListWithDataKey(key string) *knowledgev1.Node {
	for i := range s.byType["list"] {
		n := s.byType["list"][i]
		raw := kgtypes.Value(n, "data")
		if raw == "" {
			continue
		}
		m := map[string]string{}
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			continue
		}
		if _, ok := m[key]; ok {
			return n
		}
	}
	return nil
}

func (s *v1Snapshot) firstOfType(t string) *knowledgev1.Node {
	if len(s.byType[t]) == 0 {
		return nil
	}
	return s.byType[t][0]
}

func (s *v1Snapshot) findLinkByURL(url string) *knowledgev1.Node {
	for i := range s.byType["link"] {
		n := s.byType["link"][i]
		if kgtypes.Value(n, "url") == url {
			return n
		}
	}
	return nil
}

func (s *v1Snapshot) referencedInternalURLs(pageID string) []string {
	urls := append([]string(nil), s.internalRefURLs[pageID]...)
	sort.Strings(urls)
	return urls
}

func (s *v1Snapshot) sectionsSummary() []string {
	out := make([]string, 0, len(s.byType["section"]))
	for _, n := range s.byType["section"] {
		out = append(out, n.SymbolName+"[id="+kgtypes.Value(n, "id")+"]")
	}
	return out
}

func (s *v1Snapshot) listsSummary() []string {
	out := make([]string, 0, len(s.byType["list"]))
	for _, n := range s.byType["list"] {
		out = append(out, "list[data="+kgtypes.Value(n, "data")+"]")
	}
	return out
}

func (s *v1Snapshot) pagesSummary() []string {
	out := make([]string, 0, len(s.byType["page"]))
	for _, n := range s.byType["page"] {
		out = append(out, kgtypes.Value(n, "url"))
	}
	return out
}

func (s *v1Snapshot) paragraphsSummary() []string {
	out := make([]string, 0, len(s.byType["paragraph"]))
	for _, n := range s.byType["paragraph"] {
		out = append(out, "paragraph[emph="+kgtypes.Value(n, "inline_emphasis")+"]")
	}
	return out
}

// v1CollectSnapshot walks the captured CollectResult batch once and
// returns a v1Snapshot that the per-subtest assertions consume. Also
// scans every EdgeReferences edge whose FromID is a page node,
// recording the absolute URL its Evidence claims as rel="internal" —
// that's the signal the raw-link recovery test uses to assert
// nav/followup URLs were seeded into pageRecord.InternalLinks. The
// emitter writes these edges with string FromID/ToID (FromIdx/ToIdx =
// -1), so the batch carries the same endpoint IDs a store readback
// would expose — no store engine needed.
func v1CollectSnapshot(t *testing.T, batch *collectorwire.CollectResult, srvBase string) *v1Snapshot {
	t.Helper()
	s := &v1Snapshot{
		srvBase:         srvBase,
		byType:          map[string][]*knowledgev1.Node{},
		internalRefURLs: map[string][]string{},
	}
	for _, n := range batch.Nodes {
		s.byType[n.Type] = append(s.byType[n.Type], n)
	}
	pageIDs := map[string]struct{}{}
	for _, p := range s.byType["page"] {
		pageIDs[p.Id] = struct{}{}
	}
	for _, e := range batch.Edges {
		if e.Type != kgtypes.EdgeReferences {
			continue
		}
		if _, ok := pageIDs[e.FromID]; !ok {
			continue
		}
		md := parseEdgeMeta(e.Evidence)
		if md["rel"] != "internal" {
			continue
		}
		if u := md["url"]; u != "" {
			s.internalRefURLs[e.FromID] = append(s.internalRefURLs[e.FromID], u)
		}
	}
	return s
}

// parseJSONStringMap unmarshals a JSON string→string map literal and
// fails the test on invalid JSON.
func parseJSONStringMap(t *testing.T, raw string) map[string]string {
	t.Helper()
	if raw == "" {
		return nil
	}
	m := map[string]string{}
	require.NoError(t, json.Unmarshal([]byte(raw), &m),
		"expected JSON string-map, got %q", raw)
	return m
}
