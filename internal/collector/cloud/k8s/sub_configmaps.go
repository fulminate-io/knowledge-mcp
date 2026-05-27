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

// configMapsSubCollector lists all ConfigMaps across all namespaces.
//
// Content shape: {keys, data, binary_data_keys}.
//   - keys:             sorted list of all Data + BinaryData key names.
//   - data:             map[string]string of Data values (UTF-8 text configs).
//     The CONNECTS_TO resolver scans these values for cloud
//     URIs (e.g. s3://, gs://, https://...amazonaws.com).
//   - binary_data_keys: sorted list of BinaryData key names only. BinaryData
//     values are NOT stored — binary configs are unlikely
//     to contain scannable URIs in practice and inflating
//     Content with raw bytes bloats the graph.
//
// All Content flows into the graph .bin file, which is AES-256-GCM encrypted
// at rest. ConfigMap values are not secret by default, but Phase 2 may extend
// the summarizer opt-out to ConfigMap as well (open question on the ticket).
type configMapsSubCollector struct {
	clientset kubernetes.Interface
}

func (s *configMapsSubCollector) Name() string { return "configmaps" }

func (s *configMapsSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	list, err := s.clientset.CoreV1().ConfigMaps("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list configmaps: %w", err)
	}

	var result cloud.SubCollectorResult

	for _, cm := range list.Items {
		id := resourceID(cm.Namespace, "ConfigMap", cm.Name)

		meta := labelsToMeta(cm.Labels)
		meta["namespace"] = cm.Namespace
		meta["data_key_count"] = formatInt(len(cm.Data) + len(cm.BinaryData))

		// Combined key list (Data + BinaryData) preserves the existing
		// top-level `keys` contract for any consumer that only reads names.
		keys := make([]string, 0, len(cm.Data)+len(cm.BinaryData))
		data := make(map[string]string, len(cm.Data))
		for k, v := range cm.Data {
			keys = append(keys, k)
			data[k] = v
		}
		binaryKeys := make([]string, 0, len(cm.BinaryData))
		for k := range cm.BinaryData {
			keys = append(keys, k)
			binaryKeys = append(binaryKeys, k)
		}
		sort.Strings(keys)
		sort.Strings(binaryKeys)

		content := struct {
			Keys           []string          `json:"keys"`
			Data           map[string]string `json:"data"`
			BinaryDataKeys []string          `json:"binary_data_keys"`
		}{
			Keys:           keys,
			Data:           data,
			BinaryDataKeys: binaryKeys,
		}
		contentJSON, err := json.Marshal(content)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("marshal configmap %s: %w", cm.Name, err)
		}

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         cm.Name,
			ResourceType: "ConfigMap",
			Region:       cm.Namespace,
			Content:      contentJSON,
			Metadata:     meta,
		})
	}

	return result, nil
}
