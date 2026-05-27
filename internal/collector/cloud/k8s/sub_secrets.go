// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

// secretsSubCollector lists all Secrets across all namespaces.
//
// SECURITY model:
//   - Content carries decoded secret values (as strings) so the CONNECTS_TO
//     resolver (cloud/k8s/postpopulate_external.go) can scan them for cloud
//     URIs. Content is written to the graph .bin file, which is AES-256-GCM
//     encrypted at rest with machine-bound keys.
//   - Metadata still holds only non-sensitive fields: key NAMES, type, labels,
//     annotations, data_key_count. Metadata must never contain secret values.
//   - Because Metadata is included in BM25 search indexes and because Summary
//     and Keywords (derived by the LLM summarizer from Content) are indexed,
//     Phase 2 adds a summarizer/embedder opt-out for Secret so decoded values
//     never reach the LLM or the vector index. BM25 over Content itself is
//     already excluded for GraphCloud by store/search_index_bm25.go.
type secretsSubCollector struct {
	clientset kubernetes.Interface
}

func (s *secretsSubCollector) Name() string { return "secrets" }

func (s *secretsSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	list, err := s.clientset.CoreV1().Secrets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list secrets: %w", err)
	}

	var result cloud.SubCollectorResult

	for _, secret := range list.Items {
		id := resourceID(secret.Namespace, "Secret", secret.Name)

		meta := labelsToMeta(secret.Labels)
		meta["namespace"] = secret.Namespace
		meta["type"] = string(secret.Type)
		meta["data_key_count"] = formatInt(len(secret.Data))

		// Copy annotations to metadata (annotations often contain useful info
		// like cert-manager issuer, external-secrets backend, etc.).
		// NOTE: values here are annotation values, NOT secret data values.
		for k, v := range secret.Annotations {
			meta["annotation/"+k] = v
		}

		// Content carries sorted key names AND decoded data values so the
		// CONNECTS_TO resolver can scan for cloud URIs. See SECURITY model on
		// the type declaration for how this data is kept out of BM25, Summary,
		// Keywords, and the embedder index.
		keys := make([]string, 0, len(secret.Data))
		data := make(map[string]string, len(secret.Data))
		for k, v := range secret.Data {
			keys = append(keys, k)
			// Secret.Data is map[string][]byte; client-go already base64-decoded
			// the wire format. string(v) is valid for binary payloads too —
			// downstream URI regex simply won't match non-UTF8 bytes.
			data[k] = string(v)
		}
		sort.Strings(keys)

		content := struct {
			Type string            `json:"type"`
			Keys []string          `json:"keys"`
			Data map[string]string `json:"data"`
		}{
			Type: string(secret.Type),
			Keys: keys,
			Data: data,
		}
		contentJSON, err := json.Marshal(content)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("marshal secret %s: %w", secret.Name, err)
		}

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         secret.Name,
			ResourceType: "Secret",
			Region:       secret.Namespace,
			Content:      contentJSON,
			Metadata:     meta,
		})
	}

	return result, nil
}
