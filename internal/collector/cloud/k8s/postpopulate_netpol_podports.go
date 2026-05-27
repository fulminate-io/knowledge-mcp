// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// podContainerPort captures a single container-port declaration on a pod.
// Named ports in NetworkPolicy rules are resolved by matching on (name,
// protocol) against this set for the relevant target pod.
type podContainerPort struct {
	name     string
	port     int
	protocol string // "TCP" | "UDP" | "SCTP"; empty means unset (defaults to TCP per K8s)
}

// buildPodPortIndex queries every Pod node and decodes the raw Pod content
// into a pod-id → containerPort-list map. Container-port entries that lack
// a name are skipped because they cannot be referenced by a named-port
// rule. Pods with no declared ports are simply absent from the map — the
// caller must tolerate lookup misses.
func buildPodPortIndex(ctx context.Context, gc postpopulate.GraphCaller, graphName string) (map[string][]podContainerPort, error) {
	pods, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, k8sResourceQuery("Pod"))
	if err != nil {
		return nil, err
	}

	idx := make(map[string][]podContainerPort, len(pods))
	for _, node := range pods {
		if len(node.Content) == 0 {
			continue
		}
		ports, err := parsePodContainerPorts([]byte(node.Content))
		if err != nil {
			slog.Debug("buildPodPortIndex: failed to parse pod content",
				"pod", node.Id, "err", err)
			continue
		}
		if len(ports) == 0 {
			continue
		}
		idx[node.Id] = ports
	}
	return idx, nil
}

// podContentPorts mirrors just the containers[*].ports[*] slice of a
// corev1.Pod so parsePodContainerPorts can decode without pulling in the
// full Kubernetes type graph.
type podContentPorts struct {
	Spec struct {
		Containers []struct {
			Ports []struct {
				Name          string `json:"name,omitempty"`
				ContainerPort int    `json:"containerPort,omitempty"`
				Protocol      string `json:"protocol,omitempty"`
			} `json:"ports,omitempty"`
		} `json:"containers,omitempty"`
	} `json:"spec"`
}

// parsePodContainerPorts extracts the set of named container ports from
// a raw Pod JSON payload. Named ports declared without a containerPort
// are skipped because they cannot be resolved to a number.
func parsePodContainerPorts(raw []byte) ([]podContainerPort, error) {
	var pc podContentPorts
	if err := json.Unmarshal(raw, &pc); err != nil {
		return nil, err
	}
	var out []podContainerPort
	for _, c := range pc.Spec.Containers {
		for _, p := range c.Ports {
			if p.Name == "" || p.ContainerPort == 0 {
				continue
			}
			out = append(out, podContainerPort{
				name:     p.Name,
				port:     p.ContainerPort,
				protocol: canonicalProtocol(p.Protocol),
			})
		}
	}
	return out, nil
}
