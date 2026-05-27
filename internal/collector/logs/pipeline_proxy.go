// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"context"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// computeStreamResolutions walks every stream's service-identifying
// labels and returns the deduplicated set of (label, cloud-resource)
// resolutions the supplied CloudResolver knows how to map. The returned
// slice rides the standard collectorwire.CollectResult to the server,
// where MaterializeLogGraph (cmd/knowledge/internal/collector/logs)
// turns it into NodeProxy entries + EMITTED_BY edges.
//
// A nil resolver yields a nil slice — the common case when the pipeline
// runs without a cloud graph attached. Resolution failures for individual
// streams are no-ops; entries are only emitted for confirmed matches.
//
// Order is the order pairs are first observed in the streams slice; tests
// that need a deterministic order should sort the result.
//
// The client-side materialize path is the only writer of stream resolutions.
func computeStreamResolutions(
	ctx context.Context,
	streams []*wirelogs.LogStream,
	resolver CloudResolver,
) []wirelogs.ResolvedProxyEntry {
	if resolver == nil {
		return nil
	}
	pairs := serviceLabelPairs(streams)
	out := make([]wirelogs.ResolvedProxyEntry, 0, len(pairs))
	for _, pair := range pairs {
		resolved, ok := resolveServiceOrNamespace(ctx, resolver, pair.stream, pair.key, pair.value)
		if !ok {
			continue
		}
		out = append(out, wirelogs.ResolvedProxyEntry{
			LabelKey:   pair.key,
			LabelValue: pair.value,
			Account:    resolved.Account,
			ResourceID: resolved.ID,
		})
	}
	return out
}

// servicePair holds one (key, value) pair plus the first stream we saw
// it on. The stream supplies the label context (project_id,
// cluster_name, ...) the resolver uses to pick the right cloud graph.
//
// Pairs are deduplicated by (key, value) — two streams contributing the
// same service label produce one proxy. The first stream we encounter
// wins as the resolution context; in practice all streams sharing a
// label value also share the relevant cloud-context labels.
type servicePair struct {
	key    string
	value  string
	stream *wirelogs.LogStream
}

// serviceLabelPairs walks every stream's low-cardinality labels and
// returns the deduplicated set of service-identifying (key, value)
// pairs together with the stream the pair was first observed on.
// "Service-identifying" covers the wirelogs.FieldService / wirelogs.FieldNamespace /
// deployment / app keys most commonly used to tag log streams.
func serviceLabelPairs(streams []*wirelogs.LogStream) []servicePair {
	seen := make(map[string]struct{})
	var out []servicePair
	for _, s := range streams {
		if s == nil {
			continue
		}
		for k, v := range s.LowCardLabels {
			if !isServiceIdentifyingKey(k) {
				continue
			}
			if v == "" {
				continue
			}
			dupKey := k + "=" + v
			if _, dup := seen[dupKey]; dup {
				continue
			}
			seen[dupKey] = struct{}{}
			out = append(out, servicePair{key: k, value: v, stream: s})
		}
	}
	return out
}

// isServiceIdentifyingKey reports whether a label key is one of the
// canonical identifiers we try to resolve against the cloud graph.
// The set intentionally mirrors the field-mapping constants so new
// providers that emit their own mapped keys light up automatically.
func isServiceIdentifyingKey(key string) bool {
	switch key {
	case wirelogs.FieldService, wirelogs.FieldNamespace, "deployment", "app":
		return true
	}
	return false
}

// resolveServiceOrNamespace dispatches to the correct CloudResolver
// method based on which identifier the label key encodes. The stream
// argument supplies the cloud-context labels the resolver consults to
// pick the right target graph.
func resolveServiceOrNamespace(
	ctx context.Context,
	resolver CloudResolver,
	stream *wirelogs.LogStream,
	key, value string,
) (ResolvedResource, bool) {
	if key == wirelogs.FieldNamespace {
		return resolver.ResolveNamespace(ctx, stream, value)
	}
	return resolver.ResolveService(ctx, stream, value)
}
