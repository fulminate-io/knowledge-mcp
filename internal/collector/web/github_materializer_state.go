// SPDX-License-Identifier: Apache-2.0

package web

import (
	"strconv"
	"sync"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// githubKey identifies a unique materialization target. Owner and Repo are
// lower-cased (per parseGitHubURL); Ref is preserved in original casing
// because git refs are case-sensitive.
type githubKey struct {
	Owner string
	Repo  string
	Ref   string
}

// githubMaterializerState is the per-crawl dedupe registry for github URL
// materializations. Lifecycle = constructed in newCrawlState, mutated by
// the dispatcher, garbage-collected when crawl() returns. Never persisted.
//
// materialized records the gh-root node ID for each (owner, repo, ref)
// already materialized in this crawl. inFlight is a per-key WaitGroup so a
// second URL hitting the same key blocks until the first materialization
// completes — avoids two workers tarball-fetching the same repo
// concurrently.
type githubMaterializerState struct {
	mu           sync.Mutex
	materialized map[githubKey]string
	inFlight     map[githubKey]*sync.WaitGroup
}

// newGithubMaterializerState constructs an empty registry.
func newGithubMaterializerState() *githubMaterializerState {
	return &githubMaterializerState{
		materialized: make(map[githubKey]string),
		inFlight:     make(map[githubKey]*sync.WaitGroup),
	}
}

// claim atomically registers an in-flight materialization for key. If
// another worker has already materialized this key, claim returns
// (existingRootID, claimed=false, ready=true) and the caller should
// reuse the root ID without fetching anything. If another worker is
// currently materializing, claim blocks on the in-flight WaitGroup, then
// returns the registered root ID. If neither, claim records this caller
// as the in-flight worker and returns (",", claimed=true, ready=false);
// the caller MUST call publish OR abort once finished.
func (s *githubMaterializerState) claim(key githubKey) (rootID string, claimed bool, ready bool) {
	s.mu.Lock()
	if id, ok := s.materialized[key]; ok {
		s.mu.Unlock()
		return id, false, true
	}
	if wg, ok := s.inFlight[key]; ok {
		s.mu.Unlock()
		wg.Wait()
		s.mu.Lock()
		id := s.materialized[key]
		s.mu.Unlock()
		return id, false, true
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	s.inFlight[key] = wg
	s.mu.Unlock()
	return "", true, false
}

// publish records the materialized root ID for key and wakes any waiters.
// Called by the in-flight worker after a successful materialization.
func (s *githubMaterializerState) publish(key githubKey, rootID string) {
	s.mu.Lock()
	s.materialized[key] = rootID
	wg := s.inFlight[key]
	delete(s.inFlight, key)
	s.mu.Unlock()
	if wg != nil {
		wg.Done()
	}
}

// abort releases the in-flight WaitGroup without recording a successful
// materialization. Used when the materializer hit a transport error or
// size cap. Waiters wake and observe that materialized[key] is unset, so
// they can either retry (current policy: skip — first attempt failed) or
// fall through to the warning path.
func (s *githubMaterializerState) abort(key githubKey) {
	s.mu.Lock()
	wg := s.inFlight[key]
	delete(s.inFlight, key)
	s.mu.Unlock()
	if wg != nil {
		wg.Done()
	}
}

// emitMaterializerWarning constructs a warning NodeDocument node + a
// REFERENCES edge from the parent page to the warning. The dispatcher
// invokes this when a github URL was rejected by the size cap (or any
// other documented warning reason).
//
// Warning nodes are distinct from gh-root nodes: gh-root uses
// kgtypes.NodeGithubRepo and represents a SUCCESSFUL materialization;
// warning nodes use kgtypes.NodeDocument with metadata
// materialization_skipped to flag the URL as a documented skip.
//
// parentPageID is the ID of the page node that contained the github
// link. When empty (seed URL with no parent), only the warning node is
// emitted, no edge.
func emitMaterializerWarning(parentPageID string, info githubURLInfo, w *materializerWarning) (*knowledgev1.Node, []kgwire.BatchEdge) {
	id := "gh-warn:" + info.Owner + "/" + info.Repo + "@" + info.Ref + ":" + w.Reason + ":" + stableSuffix(w.URL)
	md := map[string]string{
		"materialization_skipped": w.Reason,
		"reason":                  w.Reason,
		"url":                     w.URL,
		"bytes_seen":              strconv.FormatInt(w.BytesSeen, 10),
		"cap_bytes":               strconv.FormatInt(w.Cap, 10),
		"owner":                   info.Owner,
		"repo":                    info.Repo,
		"ref":                     info.Ref,
	}
	if w.Detail != "" {
		md["detail"] = w.Detail
	}
	node := &knowledgev1.Node{
		Id:         id,
		Type:       string(kgtypes.NodeDocument),
		SymbolName: "github materialization skipped: " + w.URL,
		Source:     "web-collect:github-materializer",
		Metadata:   md,
		CreatedAt:  time.Now().UnixNano(),
	}

	var edges []kgwire.BatchEdge
	if parentPageID != "" {
		edges = append(edges, kgwire.BatchEdge{
			FromIdx:  -1,
			ToIdx:    -1,
			FromID:   parentPageID,
			ToID:     id,
			Type:     kgtypes.EdgeReferences,
			Evidence: jsonMeta(map[string]string{"rel": "external", "url": w.URL, "materialization_skipped": w.Reason}),
		})
	}
	return node, edges
}

// stableSuffix returns a short hex digest of s, used to make warning node
// IDs unique even when the same (owner, repo, ref) triggers multiple
// warnings under different URLs.
func stableSuffix(s string) string {
	return stableID(s, "gh-warn", "", 0)
}
