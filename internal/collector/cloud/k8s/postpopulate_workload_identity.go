// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"log/slog"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// resolveWorkloadIdentity wires ServiceAccount nodes that carry a workload
// identity annotation (IRSA, GCP Workload Identity, Azure Workload Identity)
// to a cross-graph proxy of the cloud IAM identity they assume. Each
// ServiceAccount gets at most one ASSUMES_IDENTITY edge per provider.
//
// The annotations are captured upstream by sub_serviceaccounts.go as plain
// metadata fields (irsa_role_arn, gcp_service_account, azure_client_id);
// this resolver only reads them back and constructs the appropriate
// cross-graph proxy target:
//
//   - IRSA: proxy lives in the AWS account graph under the role ARN.
//     The account is parsed out of the ARN itself.
//   - GCP Workload Identity: proxy lives in the GCP project graph under
//     projects/{project}/serviceAccounts/{email}. Project is parsed
//     from the SA email domain.
//   - Azure Workload Identity: only the client-id GUID is available;
//     Azure subscriptions aren't derivable from a client-id without
//     cross-scanning Azure graphs (explicit plan decision says don't).
//     We emit a dangling proxy with an empty account — the identity
//     info we have (client-id) is preserved on the proxy metadata so
//     later enrichment can wire it to a real Azure identity node.
//
// ServiceAccounts without any WI annotation are skipped silently.
// This resolver is additive and does not replace linker/workload_identity.go:
// the linker operates across all loaded cloud graphs and writes into the
// shared linkage graph, while this resolver writes proxies and edges into
// the enclosing k8s cloud graph (same graph as the ServiceAccount) so the
// edges show up on the workload's own traversal without a hop through the
// linkage graph.
func resolveWorkloadIdentity(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	sas, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, k8sResourceQuery("ServiceAccount"))
	if err != nil {
		return err
	}
	if len(sas) == 0 {
		return nil
	}

	proxies := newProxyAccumulator()
	edges := make([]knowledgev1.Edge, 0, len(sas))
	for _, sa := range sas {
		next, err := buildWorkloadIdentityEdge(edges, sa, proxies)
		if err != nil {
			return err
		}
		edges = next
	}

	if len(edges) == 0 {
		return nil
	}
	// Proxy nodes + ASSUMES_IDENTITY edges land in ONE create_batch so both
	// endpoints exist for every edge.
	if err := postpopulate.LinkNodesAndEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, proxies.nodes(), edges); err != nil {
		slog.Debug("postPopulate: failed to create ASSUMES_IDENTITY edges",
			"count", len(edges), "err", err)
		return err
	}
	slog.Debug("postPopulate: created ASSUMES_IDENTITY edges", "count", len(edges))
	return nil
}

// buildWorkloadIdentityEdge inspects a single ServiceAccount for workload
// identity annotations and appends the ASSUMES_IDENTITY edge to its proxy
// to out, returning the grown slice. SAs without any WI annotation leave
// out unchanged. Providers are checked in annotation-precedence order
// (IRSA → GCP → Azure); in the unlikely case a SA has multiple
// annotations the first matching one wins (mixed-provider SAs are not a
// real pattern). The edge is a fresh knowledgev1.Edge literal at the append
// site (in emitAssumesIdentity) so the embedded proto lock is never copied.
func buildWorkloadIdentityEdge(out []knowledgev1.Edge, sa *knowledgev1.Node, proxies *proxyAccumulator) ([]knowledgev1.Edge, error) {
	if target, ok := buildIRSATarget(sa); ok {
		return emitAssumesIdentity(out, sa, target, proxies)
	}
	if target, ok := buildGCPWorkloadIdentityTarget(sa); ok {
		return emitAssumesIdentity(out, sa, target, proxies)
	}
	if target, ok := buildAzureWorkloadIdentityTarget(sa); ok {
		return emitAssumesIdentity(out, sa, target, proxies)
	}
	return out, nil
}

// wiProxyTarget is the intermediate representation for a workload identity
// proxy target before it becomes a knowledgev1.ProxyTarget. It also carries the
// display fields that land on the proxy metadata for readable traversal
// output without an upstream resolve.
type wiProxyTarget struct {
	Account      string // proxy graph name (empty for dangling Azure)
	ID           string // foreign_id in the cloud graph
	ResourceType string // e.g. "iam:role", "gcp:iam:serviceAccount", "azure:managedidentity"
	Provider     string // "aws", "gcp", "azure"
	Method       string // "irsa", "gcp-wi", "azure-wi" — used as edge Method
	Region       string // optional: AWS IAM is global (empty); GCP SA is global (empty)
}

// buildIRSATarget reads the IRSA role ARN off a ServiceAccount and
// extracts the account ID from the ARN. Returns ok=false if the
// annotation is missing or the ARN is malformed.
func buildIRSATarget(sa *knowledgev1.Node) (wiProxyTarget, bool) {
	arn := kgtypes.Value(sa, "irsa_role_arn")
	if arn == "" {
		return wiProxyTarget{}, false
	}
	account := extractAWSAccountFromARN(arn)
	if account == "" {
		return wiProxyTarget{}, false
	}
	return wiProxyTarget{
		Account:      account,
		ID:           arn,
		ResourceType: "iam:role",
		Provider:     "aws",
		Method:       "irsa",
	}, true
}

// buildGCPWorkloadIdentityTarget reads the GCP SA email and constructs
// the canonical GCP IAM SA resource name. The project is parsed from
// the email domain.
func buildGCPWorkloadIdentityTarget(sa *knowledgev1.Node) (wiProxyTarget, bool) {
	email := kgtypes.Value(sa, "gcp_service_account")
	if email == "" {
		return wiProxyTarget{}, false
	}
	project := extractGCPProjectFromSA(email)
	if project == "" {
		return wiProxyTarget{}, false
	}
	return wiProxyTarget{
		Account:      project,
		ID:           "projects/" + project + "/serviceAccounts/" + email,
		ResourceType: "gcp:iam:serviceAccount",
		Provider:     "gcp",
		Method:       "gcp-wi",
	}, true
}

// buildAzureWorkloadIdentityTarget reads the Azure WI client-id and
// constructs a dangling proxy — Azure subscriptions aren't derivable
// from a client-id GUID without cross-scanning Azure graphs, which is
// explicitly out of scope per the plan's OQ resolution. The client-id
// is stamped on the proxy so a future enrichment pass can wire it.
func buildAzureWorkloadIdentityTarget(sa *knowledgev1.Node) (wiProxyTarget, bool) {
	clientID := kgtypes.Value(sa, "azure_client_id")
	if clientID == "" {
		return wiProxyTarget{}, false
	}
	return wiProxyTarget{
		Account:      "", // dangling — no subscription context available
		ID:           "azure:workload-identity:client-id/" + clientID,
		ResourceType: "azure:managedidentity",
		Provider:     "azure",
		Method:       "azure-wi",
	}, true
}

// emitAssumesIdentity creates the cross-graph proxy and appends the
// ASSUMES_IDENTITY edge from the ServiceAccount to it to out, returning
// the grown slice. The proxy ID is deterministic so repeat runs are
// idempotent. When target.Account is empty (Azure client-id case) the
// proxy is upserted directly as a dangling cloud proxy —
// crossgraph.BuildCrossGraphProxy rejects empty Names for cloud targets, and
// the plan OQ resolution is to emit a breadcrumb with whatever identity
// info is available rather than cross-scan Azure graphs. The edge is a
// fresh knowledgev1.Edge literal at the append site so the embedded proto lock
// is never copied.
func emitAssumesIdentity(out []knowledgev1.Edge, sa *knowledgev1.Node, target wiProxyTarget, proxies *proxyAccumulator) ([]knowledgev1.Edge, error) {
	source := wiProxySource(target)

	var proxyID string
	if target.Account == "" {
		// Dangling proxy — the cloud graph name is unknown. We still want
		// a stable, deterministic ID so repeat runs don't duplicate, and
		// we still want ProxyInfo to recognize it as a cloud proxy so
		// later enrichment can wire up the real account.
		proxyID = addDanglingCloudProxy(target, source, proxies)
	} else {
		id, err := proxies.proxy(&knowledgev1.ProxyTarget{
			GraphType: string(kgtypes.GraphCloud),
			Name:      target.Account,
			NodeId:    target.ID,
		}, source)
		if err != nil {
			return out, err
		}
		proxyID = id
	}

	return append(out, knowledgev1.Edge{
		FromId: sa.Id,
		ToId:   proxyID,
		Type:   string(kgtypes.EdgeAssumesIdentity),
		Method: target.Method,
	}), nil
}

// addDanglingCloudProxy builds a proxy node with empty account metadata and
// adds it to the accumulator (to be emitted in the create_batch). Deterministic
// ID scheme: "proxy:cloud::<NodeID>" mirrors the
// "proxy:cloud:<account>:<NodeID>" shape BuildCrossGraphProxy produces but with
// an empty account segment so it can coexist with a future non-dangling proxy
// for the same foreign_id without colliding on upsert. The proxy is recognizable
// by ProxyInfo as a cloud proxy (foreign_graph=cloud, account="", foreign_id=<id>).
func addDanglingCloudProxy(target wiProxyTarget, source *knowledgev1.Node, proxies *proxyAccumulator) string {
	proxyID := "proxy:cloud::" + target.ID
	proxy := &knowledgev1.Node{
		Id:          proxyID,
		Type:        string(kgtypes.NodeProxy),
		SymbolName:  source.GetSymbolName(),
		Source:      "proxy:cloud:dangling",
		Description: source.GetDescription(),
	}
	kgtypes.SetValue(proxy, "foreign_graph", string(kgtypes.GraphCloud))
	kgtypes.SetValue(proxy, "foreign_id", target.ID)
	kgtypes.SetValue(proxy, "account", "")
	kgtypes.SetValue(proxy, "resource_type", target.ResourceType)
	kgtypes.SetValue(proxy, "provider", target.Provider)
	kgtypes.SetValue(proxy, "dangling", "true")
	// TODO(workload-identity): enrich dangling Azure WI proxies once the
	// Azure subscription is collected — the client-id stored in
	// foreign_id can be matched to an Azure managed identity node.
	proxies.byID[proxyID] = proxy
	return proxyID
}

// wiProxySource builds the proxy-source Node carrying display fields so
// the proxy is readable in traversal output without resolving upstream.
// None of the fields are load-bearing for resolution (that goes by
// foreign_id). For Azure dangling proxies the Account is empty, which
// CreateCrossGraphProxy tolerates as a degenerate cloud target when we
// bypass it — see below.
func wiProxySource(target wiProxyTarget) *knowledgev1.Node {
	name := lastSegmentAfterSlash(target.ID)
	n := &knowledgev1.Node{
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: name,
		Source:     "cloud",
		Summary:    target.ResourceType + " " + name,
	}
	kgtypes.SetValue(n, "resource_type", target.ResourceType)
	kgtypes.SetValue(n, "provider", target.Provider)
	if target.Region != "" {
		kgtypes.SetValue(n, "region", target.Region)
	}
	return n
}

// lastSegmentAfterSlash returns the suffix after the final '/' — used as
// a human-friendly display name on the proxy. Unlike lastPathSegment in
// postpopulate_nodes.go this one also handles IDs with no slash by
// returning the whole input (no defensive return-empty), so Azure
// client-id paths like "azure:workload-identity:client-id/<guid>"
// collapse to the GUID itself.
func lastSegmentAfterSlash(id string) string {
	if id == "" {
		return ""
	}
	i := strings.LastIndex(id, "/")
	if i < 0 || i == len(id)-1 {
		return id
	}
	return id[i+1:]
}
