// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"log/slog"
	"strings"

	iampb "cloud.google.com/go/iam/apiv1/iampb"
	gax "github.com/googleapis/gax-go/v2"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// saIAMPolicyGetter abstracts iamv1.IamPolicyClient.GetIamPolicy so tests can
// supply a fake without depending on the full GCP SDK client.
type saIAMPolicyGetter interface {
	GetIamPolicy(ctx context.Context, req *iampb.GetIamPolicyRequest, opts ...gax.CallOption) (*iampb.Policy, error)
}

// gkeWorkloadIdentityEdges discovers GKE Workload Identity bindings by
// inspecting the IAM policy on each GCP service account. For every binding
// with role "roles/iam.workloadIdentityUser", each member matching the WI
// pool format is turned into an EdgeWorkloadIdentity edge from the K8s SA
// to the GCP SA.
func gkeWorkloadIdentityEdges(
	ctx context.Context,
	iamClient saIAMPolicyGetter,
	projectID string,
	saEmails []string,
) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec
	for _, email := range saEmails {
		gsaName := saResourceName(projectID, email)
		policyEdges := wiEdgesForSA(ctx, iamClient, projectID, gsaName)
		edges = append(edges, policyEdges...)
	}
	return edges
}

// wiEdgesForSA fetches the IAM policy for a single GSA and returns WI edges.
func wiEdgesForSA(
	ctx context.Context,
	iamClient saIAMPolicyGetter,
	projectID, gsaResourceName string,
) []cloud.EdgeSpec {
	policy, err := iamClient.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{
		Resource: gsaResourceName,
	})
	if err != nil {
		slog.Debug("gke-wi: iam policy unavailable",
			"sa", gsaResourceName, "error", err)
		return nil
	}

	var edges []cloud.EdgeSpec
	for _, binding := range policy.GetBindings() {
		if binding.GetRole() != "roles/iam.workloadIdentityUser" {
			continue
		}
		for _, member := range binding.GetMembers() {
			ns, ksa, ok := parseWorkloadIdentityMember(member)
			if !ok {
				continue
			}
			ksaNodeID := ns + "/ServiceAccount/" + ksa
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     ksaNodeID,
				TargetID:     gsaResourceName,
				Relationship: kgtypes.EdgeWorkloadIdentity,
				Metadata: map[string]string{
					"gcp_project": projectID,
					"role":        binding.GetRole(),
					"member":      member,
				},
			})
		}
	}
	return edges
}

// parseWorkloadIdentityMember extracts the namespace and KSA name from a GKE
// workload identity pool member string. The expected format is:
//
//	serviceAccount:{project}.svc.id.goog[{namespace}/{ksa}]
//
// Returns ("", "", false) when the member does not match the WI pool format.
func parseWorkloadIdentityMember(member string) (namespace, ksaName string, ok bool) {
	// Strip "serviceAccount:" prefix.
	const prefix = "serviceAccount:"
	if !strings.HasPrefix(member, prefix) {
		return "", "", false
	}
	rest := member[len(prefix):]

	// Find the WI pool domain suffix followed by the square-bracket payload.
	idx := strings.Index(rest, ".svc.id.goog[")
	if idx < 0 {
		return "", "", false
	}

	// Extract the payload between [ and ].
	payloadStart := idx + len(".svc.id.goog[")
	if !strings.HasSuffix(rest, "]") {
		return "", "", false
	}
	payload := rest[payloadStart : len(rest)-1]

	// Split into namespace/ksa.
	parts := strings.SplitN(payload, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
