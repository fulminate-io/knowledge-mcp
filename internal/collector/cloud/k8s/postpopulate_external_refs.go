// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"encoding/json"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// buildExternalEdgesFromRefs scans Secret / ConfigMap references on a
// workload's containers (env.valueFrom.{secret,configMap}KeyRef and
// envFrom.{secretRef,configMapRef}) and emits CONNECTS_TO edges for any
// cloud URIs found in the referenced data values.
//
// Idempotency / dedup: reuses the same `seen` map as
// buildExternalEdgesForWorkload so a workload → same-URI edge reached
// via both a literal env and a Secret ref collapses to one edge.
//
// Safety:
//   - Missing Secret/ConfigMap: logs at Warn with the full diagnostic
//     context (workload, container, env var, kind, namespace, name) and
//     continues. Never fails the whole resolver.
//   - Binary-only ConfigMap keys (listed in keys[] but absent from
//     data): silently skipped — expected for non-UTF8 content.
//   - Raw Secret values are NEVER written to edge evidence. Only the
//     matched-URI substring lands on Edge.Evidence/Method via
//     externalTarget.Matched, just like the literal-value path.
func buildExternalEdgesFromRefs(ctx context.Context, gc postpopulate.GraphCaller, graphName string, w *knowledgev1.Node, namespace string, containers []containerLite, seen map[string]struct{}, proxies *proxyAccumulator) ([]knowledgev1.Edge, error) {
	var edges []knowledgev1.Edge
	for _, c := range containers {
		envEdges, err := scanContainerEnvRefs(ctx, gc, graphName, w, namespace, c, seen, proxies)
		if err != nil {
			return nil, err
		}
		edges = append(edges, envEdges...)

		efEdges, err := scanContainerEnvFromRefs(ctx, gc, graphName, w, namespace, c, seen, proxies)
		if err != nil {
			return nil, err
		}
		edges = append(edges, efEdges...)
	}
	return edges, nil
}

// scanContainerEnvRefs walks a container's env[] entries whose
// ValueFrom is set and dispatches to the appropriate per-ref scanner.
func scanContainerEnvRefs(ctx context.Context, gc postpopulate.GraphCaller, graphName string, w *knowledgev1.Node, namespace string, c containerLite, seen map[string]struct{}, proxies *proxyAccumulator) ([]knowledgev1.Edge, error) {
	var edges []knowledgev1.Edge
	for _, env := range c.Env {
		if env.ValueFrom == nil {
			continue
		}
		if ref := env.ValueFrom.SecretKeyRef; ref != nil {
			e, err := scanSecretKey(ctx, gc, graphName, w, namespace, c.Name, env.Name, ref, seen, proxies)
			if err != nil {
				return nil, err
			}
			edges = append(edges, e...)
		}
		if ref := env.ValueFrom.ConfigMapKeyRef; ref != nil {
			e, err := scanConfigMapKey(ctx, gc, graphName, w, namespace, c.Name, env.Name, ref, seen, proxies)
			if err != nil {
				return nil, err
			}
			edges = append(edges, e...)
		}
	}
	return edges, nil
}

// scanContainerEnvFromRefs walks a container's envFrom[] entries and
// dispatches to per-ref scanners that iterate ALL keys in the target.
func scanContainerEnvFromRefs(ctx context.Context, gc postpopulate.GraphCaller, graphName string, w *knowledgev1.Node, namespace string, c containerLite, seen map[string]struct{}, proxies *proxyAccumulator) ([]knowledgev1.Edge, error) {
	var edges []knowledgev1.Edge
	for _, ef := range c.EnvFrom {
		if ef.SecretRef != nil {
			e, err := scanSecretAllKeys(ctx, gc, graphName, w, namespace, c.Name, ef.SecretRef.Name, seen, proxies)
			if err != nil {
				return nil, err
			}
			edges = append(edges, e...)
		}
		if ef.ConfigMapRef != nil {
			e, err := scanConfigMapAllKeys(ctx, gc, graphName, w, namespace, c.Name, ef.ConfigMapRef.Name, seen, proxies)
			if err != nil {
				return nil, err
			}
			edges = append(edges, e...)
		}
	}
	return edges, nil
}

// scanSecretKey resolves a single Secret key reference, scans the value
// for cloud URIs, and emits one edge per pattern match.
func scanSecretKey(ctx context.Context, gc postpopulate.GraphCaller, graphName string, w *knowledgev1.Node, namespace, containerName, envName string, ref *objectKeyRef, seen map[string]struct{}, proxies *proxyAccumulator) ([]knowledgev1.Edge, error) {
	data, found, err := loadSecretData(ctx, gc, graphName, namespace, ref.Name)
	if err != nil {
		return nil, err
	}
	if !found {
		logMissingRef(w, containerName, envName, "Secret", namespace, ref.Name)
		return nil, nil
	}
	value, ok := data[ref.Key]
	if !ok {
		slog.Warn("postPopulate: secretKeyRef key not found in Secret data",
			"workload", w.Id, "container", containerName, "env", envName,
			"secret", namespace+"/"+ref.Name, "key", ref.Key)
		return nil, nil
	}
	return emitRefEdges(w, value, evidenceEnv(containerName, envName), seen, proxies)
}

// scanConfigMapKey resolves a single ConfigMap key reference. If the
// key is listed in keys[] but missing from data (binary-only payload),
// silently skip — no Warn, that's expected.
func scanConfigMapKey(ctx context.Context, gc postpopulate.GraphCaller, graphName string, w *knowledgev1.Node, namespace, containerName, envName string, ref *objectKeyRef, seen map[string]struct{}, proxies *proxyAccumulator) ([]knowledgev1.Edge, error) {
	data, keys, found, err := loadConfigMapData(ctx, gc, graphName, namespace, ref.Name)
	if err != nil {
		return nil, err
	}
	if !found {
		logMissingRef(w, containerName, envName, "ConfigMap", namespace, ref.Name)
		return nil, nil
	}
	value, ok := data[ref.Key]
	if !ok {
		// Binary-only key (listed in keys[] but absent from data): silent skip.
		if _, isBinary := indexOf(keys, ref.Key); isBinary {
			return nil, nil
		}
		slog.Warn("postPopulate: configMapKeyRef key not found in ConfigMap data",
			"workload", w.Id, "container", containerName, "env", envName,
			"configmap", namespace+"/"+ref.Name, "key", ref.Key)
		return nil, nil
	}
	return emitRefEdges(w, value, evidenceEnv(containerName, envName), seen, proxies)
}

// scanSecretAllKeys iterates every key in a referenced Secret (via
// envFrom.secretRef) and scans its value for URIs.
func scanSecretAllKeys(ctx context.Context, gc postpopulate.GraphCaller, graphName string, w *knowledgev1.Node, namespace, containerName, refName string, seen map[string]struct{}, proxies *proxyAccumulator) ([]knowledgev1.Edge, error) {
	data, found, err := loadSecretData(ctx, gc, graphName, namespace, refName)
	if err != nil {
		return nil, err
	}
	if !found {
		logMissingRef(w, containerName, "", "Secret", namespace, refName)
		return nil, nil
	}
	var edges []knowledgev1.Edge
	for k, v := range data {
		e, err := emitRefEdges(w, v, evidenceEnvFrom(containerName, refName, k), seen, proxies)
		if err != nil {
			return nil, err
		}
		edges = append(edges, e...)
	}
	return edges, nil
}

// scanConfigMapAllKeys iterates every key in a referenced ConfigMap
// (via envFrom.configMapRef). Binary-only keys are implicitly skipped
// because they aren't present in the data map.
func scanConfigMapAllKeys(ctx context.Context, gc postpopulate.GraphCaller, graphName string, w *knowledgev1.Node, namespace, containerName, refName string, seen map[string]struct{}, proxies *proxyAccumulator) ([]knowledgev1.Edge, error) {
	data, _, found, err := loadConfigMapData(ctx, gc, graphName, namespace, refName)
	if err != nil {
		return nil, err
	}
	if !found {
		logMissingRef(w, containerName, "", "ConfigMap", namespace, refName)
		return nil, nil
	}
	var edges []knowledgev1.Edge
	for k, v := range data {
		e, err := emitRefEdges(w, v, evidenceEnvFrom(containerName, refName, k), seen, proxies)
		if err != nil {
			return nil, err
		}
		edges = append(edges, e...)
	}
	return edges, nil
}

// emitRefEdges runs the URI pattern scan on value and emits one edge
// per match, building evidence from the caller-supplied prefix.
// prefix is the "container=X env=Y" or "container=X envFrom=Y key=Z"
// portion; pattern/matched segments are appended by emitConnectsToRaw.
func emitRefEdges(w *knowledgev1.Node, value, prefix string, seen map[string]struct{}, proxies *proxyAccumulator) ([]knowledgev1.Edge, error) {
	if value == "" {
		return nil, nil
	}
	var edges []knowledgev1.Edge
	for _, target := range scanAllPatterns(value) {
		next, err := emitConnectsToRaw(edges, w, prefix, target, seen, proxies)
		if err != nil {
			return nil, err
		}
		edges = next
	}
	return edges, nil
}

// evidenceEnv builds the evidence prefix for a valueFrom.*KeyRef hit.
// Format matches the literal-env path at emitConnectsTo for caller
// consistency: "container=<c> env=<envName>".
func evidenceEnv(containerName, envName string) string {
	return "container=" + containerName + " env=" + envName
}

// evidenceEnvFrom builds the evidence prefix for an envFrom.*Ref hit.
// Format distinguishes bulk-imported refs from single-key refs:
// "container=<c> envFrom=<refName> key=<k>".
func evidenceEnvFrom(containerName, refName, key string) string {
	return "container=" + containerName + " envFrom=" + refName + " key=" + key
}

// logMissingRef emits a single Warn-level diagnostic for a referenced
// Secret / ConfigMap that can't be resolved. envName is empty for
// envFrom bulk refs; the log omits it in that case.
func logMissingRef(w *knowledgev1.Node, containerName, envName, kind, namespace, name string) {
	args := []any{
		"workload", w.Id,
		"container", containerName,
		"kind", kind,
		"namespace", namespace,
		"name", name,
	}
	if envName != "" {
		args = append(args, "env", envName)
	}
	slog.Warn("postPopulate: "+kind+" reference not resolvable", args...)
}

// indexOf returns (idx, true) if needle is in haystack, else (0, false).
// Used to distinguish binary-only ConfigMap keys (present in keys[] but
// absent from data) from truly missing keys.
func indexOf(haystack []string, needle string) (int, bool) {
	for i, s := range haystack {
		if s == needle {
			return i, true
		}
	}
	return 0, false
}

// secretContent mirrors the shape written by sub_secrets.go (Phase 1):
// {type, keys, data}. Kept local so this resolver stays decoupled from
// the collector type.
type secretContent struct {
	Type string            `json:"type"`
	Keys []string          `json:"keys"`
	Data map[string]string `json:"data"`
}

// configMapContent mirrors the shape written by sub_configmaps.go
// (Phase 1): {keys, data, binary_data_keys}. binary_data_keys lets us
// distinguish binary-only keys from genuinely missing keys.
type configMapContent struct {
	Keys           []string          `json:"keys"`
	Data           map[string]string `json:"data"`
	BinaryDataKeys []string          `json:"binary_data_keys,omitempty"`
}

// loadSecretData looks up a Secret by resourceID and decodes its
// Content. Returns found=false when the Secret doesn't exist in the
// graph (callers log at Warn + continue). "not found" from the store
// is treated as the missing-ref case, not a propagated error — the
// resolver should never abort when a workload references a Secret from
// a namespace we didn't collect.
//
// Returns only data (not keys): Secret Content has no binary_data
// section so keys and data.keys are equivalent — callers use data
// directly.
func loadSecretData(ctx context.Context, gc postpopulate.GraphCaller, graphName, namespace, name string) (map[string]string, bool, error) {
	id := resourceID(namespace, "Secret", name)
	n, found, err := lookupNode(ctx, gc, graphName, id)
	if err != nil || !found {
		return nil, false, err
	}
	if len(n.Content) == 0 {
		return nil, true, nil
	}
	var sc secretContent
	if err := json.Unmarshal([]byte(n.Content), &sc); err != nil {
		// Malformed Content: treat as empty rather than failing the resolver.
		// Note: we deliberately OMIT the unmarshal error from the log line
		// because json.Unmarshal errors can embed a small excerpt of the
		// malformed input (e.g. "invalid character 'x' looking for..."). On
		// a Secret that's NEVER safe — the excerpt could contain secret
		// bytes. The ID alone is enough to diagnose from graph contents.
		slog.Warn("postPopulate: Secret Content malformed, skipping URI scan",
			"secret", id)
		return nil, true, nil //nolint:nilerr // intentional swallow — see comment above
	}
	return sc.Data, true, nil
}

// loadConfigMapData looks up a ConfigMap by resourceID and decodes its
// Content. Returns found=false when the ConfigMap doesn't exist.
func loadConfigMapData(ctx context.Context, gc postpopulate.GraphCaller, graphName, namespace, name string) (map[string]string, []string, bool, error) {
	id := resourceID(namespace, "ConfigMap", name)
	n, found, err := lookupNode(ctx, gc, graphName, id)
	if err != nil || !found {
		return nil, nil, false, err
	}
	if len(n.Content) == 0 {
		return nil, nil, true, nil
	}
	var cc configMapContent
	if err := json.Unmarshal([]byte(n.Content), &cc); err != nil {
		// See loadSecretData note — we omit the unmarshal error to avoid
		// leaking input excerpts. ConfigMaps are not secret by default
		// but can still hold sensitive config (tokens, internal URLs).
		slog.Warn("postPopulate: ConfigMap Content malformed, skipping URI scan",
			"configmap", id)
		return nil, nil, true, nil //nolint:nilerr // intentional swallow — see comment above
	}
	return cc.Data, cc.Keys, true, nil
}

// lookupNode wraps a by-id graph read and normalizes "node not found"
// errors into found=false so callers don't need to string-match.
// The server's by-id read returns fmt.Errorf("node %s not found", id) on
// miss — other errors are propagated.
func lookupNode(ctx context.Context, gc postpopulate.GraphCaller, graphName, id string) (*knowledgev1.Node, bool, error) {
	nodes, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{"ids": []string{id}})
	if err != nil {
		if isNotFoundErr(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if len(nodes) == 0 {
		return nil, false, nil
	}
	return nodes[0], true, nil
}

// isNotFoundErr reports whether err is the "node not found" sentinel
// produced by the server's by-id read. The server emits it via fmt.Errorf
// with no exported sentinel, so we match on the message. Any future exported
// sentinel (errors.Is(ErrNotFound)) should replace this.
func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return len(msg) >= len("not found") && msg[len(msg)-len("not found"):] == "not found"
}
