// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

const methodIRSA = "aws-irsa"

// resolveIRSA matches EKS OIDC providers against IAM role trust policies
// and emits EdgeWorkloadIdentity edges from k8s ServiceAccounts to IAM roles.
//
// Algorithm:
//  1. Scan eks-cluster nodes, extract OIDC issuer URLs, build lookup maps.
//  2. Scan iam-role nodes, parse trust policies for Federated principals.
//  3. For each Federated principal matching an EKS OIDC provider, parse the
//     Condition block to extract k8s SA subjects and emit edges.
//
// Single-graph resolver: reads + writes ONLY the current account's cloud graph
// (graphName) over the wire.
func resolveIRSA(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	oidcMap, err := buildOIDCIssuerMap(ctx, gc, graphName)
	if err != nil {
		return err
	}
	if len(oidcMap) == 0 {
		return nil
	}

	roles, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, iamRoleQuery())
	if err != nil {
		return err
	}

	var edges []knowledgev1.Edge
	for _, role := range roles {
		roleEdges := matchRoleFederatedPrincipals(role, oidcMap)
		edges = append(edges, roleEdges...)
	}

	if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, edges); err != nil {
		return err
	}
	slog.Debug("aws irsa: emitted edges", "count", len(edges))
	return nil
}

// oidcMapping holds the OIDC issuer URL and the EKS cluster ARN.
type oidcMapping struct {
	issuerURL  string
	clusterARN string
}

// buildOIDCIssuerMap scans eks-cluster nodes and builds a map from
// OIDC provider ARN to the issuer URL and cluster ARN.
func buildOIDCIssuerMap(ctx context.Context, gc postpopulate.GraphCaller, graphName string) (map[string]oidcMapping, error) {
	clusters, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type":  string(kgtypes.NodeCloudResource),
		"meta":  map[string]string{"resource_type": "eks-cluster"},
		"limit": 0,
	})
	if err != nil {
		return nil, err
	}
	if len(clusters) == 0 {
		return nil, nil
	}

	out := make(map[string]oidcMapping)
	for _, cluster := range clusters {
		issuer := extractOIDCIssuer(cluster.Content)
		if issuer == "" {
			continue
		}
		acct := accountFromARN(cluster.Id)
		if acct == "" {
			continue
		}
		providerARN := oidcProviderARN(acct, issuer)
		out[providerARN] = oidcMapping{
			issuerURL:  issuer,
			clusterARN: cluster.Id,
		}
	}
	return out, nil
}

// extractOIDCIssuer extracts the OIDC issuer URL from an EKS cluster
// node's Content JSON. The SDK shape is Identity.Oidc.Issuer.
func extractOIDCIssuer(content string) string {
	if content == "" {
		return ""
	}
	var raw struct {
		Identity *struct {
			Oidc *struct {
				Issuer *string `json:"Issuer"`
			} `json:"Oidc"`
		} `json:"Identity"`
	}
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return ""
	}
	if raw.Identity == nil || raw.Identity.Oidc == nil || raw.Identity.Oidc.Issuer == nil {
		return ""
	}
	return *raw.Identity.Oidc.Issuer
}

// oidcProviderARN converts an account ID and OIDC issuer URL into the
// IAM OIDC provider ARN: arn:aws:iam::{account}:oidc-provider/{host}{path}.
func oidcProviderARN(accountID, issuerURL string) string {
	// Strip the https:// scheme.
	suffix := strings.TrimPrefix(issuerURL, "https://")
	suffix = strings.TrimPrefix(suffix, "http://")
	return "arn:aws:iam::" + accountID + ":oidc-provider/" + suffix
}

// matchRoleFederatedPrincipals checks a role's trust policy for Federated
// principals that match known OIDC providers and emits edges for matching
// SA subjects found in the Condition block.
func matchRoleFederatedPrincipals(
	role *knowledgev1.Node, oidcMap map[string]oidcMapping,
) []knowledgev1.Edge {
	policy := extractTrustPolicy(role)
	if policy == nil {
		return nil
	}

	var edges []knowledgev1.Edge
	for i := range policy.Statements {
		stmt := &policy.Statements[i]
		if !strings.EqualFold(stmt.Effect, "Allow") || stmt.Principal == nil {
			continue
		}
		for _, fedARN := range stmt.Principal.Federated {
			mapping, ok := oidcMap[fedARN]
			if !ok {
				continue
			}
			subjects := extractIRSASubjects(stmt.Condition, mapping.issuerURL)
			for _, subj := range subjects {
				edges = appendIRSAEdge(edges, subj, role.Id, mapping.clusterARN, fedARN)
			}
		}
	}
	return edges
}

// extractIRSASubjects parses the Condition block of a trust policy statement
// to find k8s SA subjects. It looks for StringEquals or StringLike keys
// ending in ":sub" with values matching "system:serviceaccount:{ns}:{sa}".
func extractIRSASubjects(
	cond map[string]map[string]stringOrSlice, issuerURL string,
) []string {
	if len(cond) == 0 {
		return nil
	}
	// The condition key is "{oidc-issuer-host-path}:sub".
	issuerKey := strings.TrimPrefix(issuerURL, "https://")
	issuerKey = strings.TrimPrefix(issuerKey, "http://")
	subKey := issuerKey + ":sub"

	var subjects []string
	for _, opValues := range cond {
		vals, ok := opValues[subKey]
		if !ok {
			continue
		}
		for _, v := range vals {
			if strings.HasPrefix(v, "system:serviceaccount:") {
				subjects = append(subjects, v)
			}
		}
	}
	return subjects
}

// buildIRSAEdge creates an EdgeWorkloadIdentity edge from a k8s SA to an
// IAM role. For wildcard subjects, uses a synthetic node ID.
func appendIRSAEdge(out []knowledgev1.Edge, subject, roleID, clusterARN, providerARN string) []knowledgev1.Edge {
	fromID := irsaSubjectToNodeID(subject, clusterARN)
	evidence := "IRSA: " + subject + " via " + providerARN + " (cluster " + clusterARN + ")"
	return append(out, knowledgev1.Edge{
		FromId:   fromID,
		ToId:     roleID,
		Type:     string(kgtypes.EdgeWorkloadIdentity),
		Method:   methodIRSA,
		Evidence: evidence,
	})
}

// irsaSubjectToNodeID converts a "system:serviceaccount:{ns}:{sa}" subject
// to a graph node ID. Concrete subjects become "{ns}/ServiceAccount/{sa}";
// wildcard subjects become "aws:eks:irsa-wildcard/{cluster}/{subject}".
func irsaSubjectToNodeID(subject, clusterARN string) string {
	// "system:serviceaccount:ns:sa-name" → ns, sa-name
	parts := strings.SplitN(subject, ":", 4)
	if len(parts) != 4 {
		return "aws:eks:irsa-wildcard/" + clusterARN + "/" + subject
	}
	ns := parts[2]
	sa := parts[3]
	if strings.Contains(ns, "*") || strings.Contains(sa, "*") {
		return "aws:eks:irsa-wildcard/" + clusterARN + "/" + subject
	}
	return ns + "/ServiceAccount/" + sa
}
