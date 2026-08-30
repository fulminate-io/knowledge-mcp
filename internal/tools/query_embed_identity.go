// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// query_embed_identity.go resolves the embedder a SEARCH QUERY must be embedded
// with, from the TARGET GRAPH'S recorded identity rather than from local config.
//
// THE HAZARD THIS CLOSES IS A CROSS-AUTHORITY ONE. A stored vector is stamped by
// the embedder the GRAPH recorded at its first embed; a query vector is stamped
// by whatever the CLIENT resolved. Where those two authorities differ the
// comparison is between two vector spaces, and the result is a ranking with no
// meaning rather than an error anyone can see. Reading the query side FROM the
// graph's record eliminates the divergence instead of narrowing it.
//
// AND IT IS NOT AN OPTIMISATION. Embedding once per distinct identity across a
// cross-graph search is cheaper than once per graph, but that is a side effect:
// embedding once GLOBALLY would be cheaper still and would be exactly the defect
// above, applied to every graph but one.

// graphTarget names one graph a search will run against.
type graphTarget struct {
	GraphType string
	Name      string
}

// queryEmbedders is the resolution result: one embedder per DISTINCT identity,
// plus the mapping from each target graph to the identity key it resolved to.
//
// A TARGET ABSENT FROM byTarget HAS NO RECORDED IDENTITY, which is not a
// failure: a graph that has never been embedded holds no vectors, so there is
// nothing for a query vector to be compared against and the BM25 arm is the
// whole of the correct answer. That is materially different from an identity
// this client cannot construct, which is an error — see resolveQueryEmbedders.
type queryEmbedders struct {
	byIdentity map[string]embed.BinaryEmbedder
	byTarget   map[graphTarget]string
}

// EmbedderFor returns the embedder for one target, and whether the target has a
// recorded identity at all.
func (q queryEmbedders) EmbedderFor(t graphTarget) (embed.BinaryEmbedder, bool) {
	key, ok := q.byTarget[t]
	if !ok {
		return nil, false
	}
	e, ok := q.byIdentity[key]
	return e, ok
}

// DistinctIdentities reports how many distinct identities were resolved — the
// number of embedders built, and therefore the number of embed calls a query
// against these targets will cost.
func (q queryEmbedders) DistinctIdentities() int { return len(q.byIdentity) }

// graphCatalogFetcher reads the graph catalog for one graph type. It is a
// parameter rather than a direct call so the resolution can be driven in a test
// without a live server; production passes fetchGraphNamesOfType's binding.
type graphCatalogFetcher func(ctx context.Context, graphType string) ([]*knowledgev1.GraphInfo, error)

// resolveQueryEmbedders resolves one embedder per distinct recorded identity
// among targets.
//
// ONE CATALOG READ PER GRAPH TYPE, not per graph: the catalog response carries
// every graph of a type, so N graphs of one type cost one read. The reads
// themselves are serial because they are local-daemon round trips against an
// in-memory registry; the EMBED calls the caller then makes are the network-bound
// part and are where concurrency belongs.
//
// THE EMBEDDER BUILDS ARE CONCURRENT for the same reason: constructing an
// embedder for an identity may reach a provider, and a cross-graph search over
// three identities should not pay three round trips end to end.
//
// AN UNCONSTRUCTIBLE IDENTITY IS AN ERROR, never a quiet fall back to BM25. The
// graph HAS vectors under that identity; answering a semantic search with
// keyword results while reporting success returns a worse answer than the caller
// asked for and gives them no way to know. The pre-existing "no credential at
// all → BM25-only" degrade on the WRITE side is untouched and is a different
// thing: there, no credential means this machine produces no vectors.
//
// THE ROLE IS FIXED AT QUERY and is not a parameter, deliberately. This resolver
// exists for the query side only; the document role belongs to the write path,
// which resolves its embedder from config rather than from a graph's record.
// Accepting a role here would let a caller ask this function for a DOCUMENT
// embedder and get one built from a graph's identity — the two authorities
// crossed in the other direction.
func resolveQueryEmbedders(
	ctx context.Context, fetch graphCatalogFetcher, targets []graphTarget,
) (queryEmbedders, error) {
	out := queryEmbedders{
		byIdentity: map[string]embed.BinaryEmbedder{},
		byTarget:   map[graphTarget]string{},
	}
	if len(targets) == 0 {
		return out, nil
	}

	catalog := map[string][]*knowledgev1.GraphInfo{}
	identities := map[string]*knowledgev1.EmbedIdentity{}
	for _, t := range targets {
		infos, ok := catalog[t.GraphType]
		if !ok {
			got, err := fetch(ctx, t.GraphType)
			if err != nil {
				return queryEmbedders{}, fmt.Errorf("resolve query embedder: read %s catalog: %w", t.GraphType, err)
			}
			infos, catalog[t.GraphType] = got, got
		}
		id := identityForGraph(infos, t.Name)
		if id == nil {
			// No recorded identity: nothing was ever embedded here, so there is
			// nothing to embed a query FOR. Deliberately not an error.
			continue
		}
		key := llmproviders.SortedIdentityKey(id)
		out.byTarget[t] = key
		identities[key] = id
	}
	if len(identities) == 0 {
		return out, nil
	}

	keys := make([]string, 0, len(identities))
	for k := range identities {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		errs = map[string]error{}
	)
	for _, k := range keys {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			e, err := llmproviders.BuildEmbedderForIdentity(ctx, identities[key], embed.InputRoleQuery)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs[key] = err
				return
			}
			out.byIdentity[key] = e
		}(k)
	}
	wg.Wait()

	// REPORTED IN SORTED-KEY ORDER, not in completion order. A config missing two
	// credentials must name the same one on every run; reporting whichever
	// goroutine finished first would make the error message a race, and an
	// operator fixing "the" error would see a different one next time.
	for _, k := range keys {
		if err, bad := errs[k]; bad {
			return queryEmbedders{}, err
		}
	}
	return out, nil
}

// knowledgeQueryEmbedder resolves the embedder for the knowledge/default arm —
// the one target this arm ever searches — from that graph's recorded identity.
//
// THREE OUTCOMES, and the middle one is the one to read carefully:
//   - resolved: the graph records an identity this client can construct.
//   - (nil, nil): the graph records NO identity, so it holds no vectors and
//     there is nothing for a query vector to be compared against. BM25 is the
//     whole of the correct answer, not a degraded one. It is also what a client
//     talking to a server that predates this field sees, which is the same
//     situation with the same correct answer.
//   - error: the graph records an identity this client CANNOT construct. The
//     caller must surface it rather than embed with something else or skip.
//
// THE CATALOG READ IS SKIPPED ENTIRELY when no graph client is wired, which is
// the bind-first startup window; the arm below already rejects that case, and
// erroring here would turn a startup race into a search failure.
func knowledgeQueryEmbedder(ctx context.Context, deps ClientDeps) (embed.BinaryEmbedder, error) {
	gc := deps.GraphCaller()
	if gc == nil {
		return nil, nil //nolint:nilnil // no client yet: not an identity failure, and the arm below rejects it
	}
	fetch := func(ctx context.Context, graphType string) ([]*knowledgev1.GraphInfo, error) {
		return fetchGraphNamesOfType(ctx, gc, graphType)
	}
	target := graphTarget{GraphType: "knowledge", Name: knowledgeDefaultName}
	res, err := resolveQueryEmbedders(ctx, fetch, []graphTarget{target})
	if err != nil {
		return nil, err
	}
	e, _ := res.EmbedderFor(target)
	return e, nil
}

// identityForGraph finds one graph's catalog entry by name and returns its
// recorded identity, or nil when the graph is absent from the catalog or records
// none.
func identityForGraph(infos []*knowledgev1.GraphInfo, name string) *knowledgev1.EmbedIdentity {
	for _, gi := range infos {
		if gi.GetName() == name {
			return gi.GetEmbedIdentity()
		}
	}
	return nil
}

// embedKnowledgeQuery embeds the query for the knowledge arm, with the embedder
// resolved from the TARGET GRAPH'S RECORDED IDENTITY rather than from this
// machine's config.
//
// WHY THE SOURCE MATTERS MORE THAN THE CALL. The stored vectors were made by
// whatever embedder the graph recorded at its first embed; a query embedded by
// anything else is a distance between two vector spaces, which ranks confidently
// and means nothing. A graph stays on its recorded identity until an explicit
// migration, so config moving underneath it is the expected case rather than an
// exceptional one — which is exactly why the query side must not read config.
//
// THREE OUTCOMES. bm25Only suppresses the embed entirely — not called rather
// than called and discarded, because on a metered embedder the difference is
// billed. A resolution ERROR is returned for the caller to surface: the graph
// HAS vectors under an identity this client cannot construct, and answering the
// semantic search with keyword results while reporting success would hand back a
// worse answer with no way for the caller to know. A graph with NO recorded
// identity yields a nil embedder and no embed, which is correct rather than
// degraded: it holds no vectors for a query vector to be compared against.
func embedKnowledgeQuery(
	ctx context.Context, deps ClientDeps, args json.RawMessage, bm25Only bool,
) (json.RawMessage, bool, error) {
	if bm25Only {
		return args, false, nil
	}
	emb, err := knowledgeQueryEmbedder(ctx, deps)
	if err != nil {
		return args, false, err
	}
	embedded, didEmbed := maybeEmbedQuery(ctx, emb, args)
	return embedded, didEmbed, nil
}
