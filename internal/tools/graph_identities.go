// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// graph_identities.go enumerates the embed identities the LOCAL GRAPHS carry, so
// config validation can be checked against what actually exists rather than
// against what the config says it would produce.
//
// ONE Execute PER GRAPH TYPE, no per-graph fan-out: the catalog read returns
// every graph of a type with its recorded identity attached, which is the same
// read the query path already makes to resolve a query embedder.

// RecordedGraphIdentities returns every embeddable local graph and the embed
// identity it has recorded. A graph that has never embedded is returned with a
// zero identity rather than omitted — "no identity yet" is a legal state the
// validator must be able to see and pass, and omitting it would make an empty
// result indistinguishable from a failed enumeration.
//
// THE TYPES WALKED ARE THE EMBEDDABLE ONES, via rebuildableBuiltinNames() — the
// SAME derivation manage(rebuild_segments) names in its refusal, so the two cannot
// disagree about which graphs carry vectors. A type that carries no vectors has no
// identity to validate, and walking it would cost a round trip to learn nothing.
//
// IT USED TO WALK SyncEligibleGraphTypes() AND FILTER BY HasRebuildableSegments,
// which was correct only while the second set nested inside the first. Web and pdf
// are embed-only: they carry vectors AND they are deliberately never sync-eligible,
// so a sync-eligible outer loop dropped them before the filter ever ran and the
// doctor's embed-identity check silently never saw a raw graph. The filter is not
// the fix — the OUTER LOOP was the bug, and an inner predicate cannot widen a set
// the loop already narrowed.
//
// exec is the raw Execute func (gc.Execute), mirroring RecordedCodeSyncMeta, so
// callers outside this package do not need a ClientDeps to ask.
func RecordedGraphIdentities(
	ctx context.Context, exec engine.ExecuteFn,
) ([]config.LiveGraphIdentity, error) {
	if exec == nil {
		return nil, fmt.Errorf("RecordedGraphIdentities: no graph caller")
	}
	caller := execFnCaller{exec: exec}

	var out []config.LiveGraphIdentity
	for _, name := range rebuildableBuiltinNames() {
		gt := kgtypes.GraphType(name)
		infos, err := fetchGraphNamesOfType(ctx, caller, string(gt))
		if err != nil {
			return nil, fmt.Errorf("RecordedGraphIdentities: enumerate %s graphs: %w", gt, err)
		}
		for _, gi := range infos {
			out = append(out, config.LiveGraphIdentity{
				GraphType: string(gt),
				Name:      gi.GetName(),
				Identity:  identityFromProto(gi.GetEmbedIdentity()),
			})
		}
	}
	return out, nil
}

// identityFromProto translates a catalog identity into the config package's own
// vocabulary. A nil identity yields the zero value, which is how "this graph has
// not embedded" is spelled on the far side.
func identityFromProto(id *knowledgev1.EmbedIdentity) config.RecordedIdentity {
	if id == nil {
		return config.RecordedIdentity{}
	}
	return config.RecordedIdentity{
		Provider:  config.EmbedProvider(id.GetProvider()),
		Model:     id.GetModel(),
		Dimension: int(id.GetDimension()),
		Dtype:     id.GetDtype(),
	}
}

// execFnCaller adapts a raw engine.ExecuteFn to the GraphCaller interface the
// catalog fetch takes. It exists so this file's exported entry point can accept
// the same bare func RecordedCodeSyncMeta does, rather than forcing every
// caller outside this package to construct a ClientDeps.
type execFnCaller struct{ exec engine.ExecuteFn }

func (c execFnCaller) Execute(
	ctx context.Context, req *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	return c.exec(ctx, req)
}
