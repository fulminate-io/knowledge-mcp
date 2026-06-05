// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"maps"
	"strings"

	iam "cloud.google.com/go/iam/admin/apiv1"
	adminpb "cloud.google.com/go/iam/admin/apiv1/adminpb"
	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"google.golang.org/api/iterator"
	iampb "google.golang.org/genproto/googleapis/iam/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// iamSubCollector collects IAM service accounts as nodes and IAM bindings
// as GRANTS edges. IAM bindings are NOT standalone nodes — they are modeled
// as edges from the role to the service account.
type iamSubCollector struct {
	iamClient      *iam.IamClient
	projectsClient *resourcemanager.ProjectsClient
	projectID      string
}

func newIAMSubCollector(iamClient *iam.IamClient, projectsClient *resourcemanager.ProjectsClient, projectID string) *iamSubCollector {
	return &iamSubCollector{
		iamClient:      iamClient,
		projectsClient: projectsClient,
		projectID:      projectID,
	}
}

func (c *iamSubCollector) Name() string { return "gcp-iam" }

func (c *iamSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	// 1. List service accounts as nodes.
	saEmails := map[string]string{} // email -> resource name
	it := c.iamClient.ListServiceAccounts(ctx, &adminpb.ListServiceAccountsRequest{
		Name: "projects/" + c.projectID,
	})

	for {
		sa, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, err
		}

		name := sa.GetName()
		if name == "" {
			continue
		}

		content, err := json.Marshal(sa)
		if err != nil {
			continue
		}

		spec := cloud.ResourceSpec{
			ID:           name,
			Name:         sa.GetEmail(),
			ResourceType: "gcp:iam:serviceAccount",
			Content:      content,
			Metadata: map[string]string{
				"email":       sa.GetEmail(),
				"displayName": sa.GetDisplayName(),
				"disabled":    boolStr(sa.GetDisabled()),
			},
		}
		result.Resources = append(result.Resources, spec)

		saEmails[sa.GetEmail()] = name
	}

	// 2. Get project IAM policy and create GRANTS edges.
	policy, err := c.projectsClient.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{
		Resource: "projects/" + c.projectID,
	})
	if err != nil {
		// Best-effort: return service accounts without IAM edges.
		return result, err
	}

	projectID := "projects/" + c.projectID
	for _, binding := range policy.GetBindings() {
		role := binding.GetRole()
		for _, member := range binding.GetMembers() {
			targetID := iamMemberToNodeID(c.projectID, member, saEmails)
			if targetID == "" {
				continue
			}
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     projectID,
				TargetID:     targetID,
				Relationship: kgtypes.EdgeGrants,
				Metadata:     map[string]string{"role": role, "member": member},
			})
		}
	}

	// 3. Project-level resource carrying Cloud Audit Log configuration as
	// metadata. Audit config is project-level configuration rather than a
	// standalone resource, so it's attached to the project node.
	result.Resources = append(result.Resources,
		projectResourceSpec(c.projectID, policy))

	return result, nil
}

// iamMemberToNodeID returns the canonical graph node ID for an IAM binding
// member string. Shared across project-level (iam.go) and per-resource
// (kms.go, etc.) IAM-policy emitters so every GRANTS edge points at a real
// node ID instead of the raw "serviceAccount:<email>" form. saEmails may be
// nil; when supplied, an exact-match SA name from the IAM subcollector is
// preferred over the synthesized canonical name. Foreign-project SA emails
// (cross-project IAM bindings) get the project parsed from the email, not
// the local projectID — using the local projectID would fabricate a path
// that doesn't exist anywhere. Returns "" for unhandled member types
// (user:, domain:, etc.) — those are dropped, matching the pattern
// established by the project-level IAM emitter.
func iamMemberToNodeID(projectID, member string, saEmails map[string]string) string {
	switch {
	case strings.HasPrefix(member, "serviceAccount:"):
		email := strings.TrimPrefix(member, "serviceAccount:")
		if name, ok := saEmails[email]; ok {
			return name
		}
		saProject := projectFromSAEmail(email)
		if saProject == "" {
			saProject = projectID // last-resort fallback for non-canonical emails
		}
		return saResourceName(saProject, email)
	case strings.HasPrefix(member, "group:"):
		email := strings.TrimPrefix(member, "group:")
		return "group:" + email // placeholder — resolved in PostPopulate
	default:
		return ""
	}
}

// projectResourceSpec builds a gcp:project resource with Cloud Audit Log
// configuration metadata derived from the project's IAM policy.
func projectResourceSpec(projectID string, policy *iampb.Policy) cloud.ResourceSpec {
	resourceID := "projects/" + projectID
	meta := map[string]string{
		"projectID": projectID,
	}
	maps.Copy(meta, auditConfigMetadata(policy.GetAuditConfigs()))
	return cloud.ResourceSpec{
		ID:           resourceID,
		Name:         projectID,
		ResourceType: "gcp:resourcemanager:project",
		Metadata:     meta,
	}
}

// auditConfigMetadata summarizes Cloud Audit Log configuration into a flat
// map: one "auditLog_<service>" key per service with a comma-separated list
// of enabled log types. Uses "allServices" as the wildcard service name per
// GCP conventions.
func auditConfigMetadata(configs []*iampb.AuditConfig) map[string]string {
	if len(configs) == 0 {
		return nil
	}
	meta := make(map[string]string, len(configs)+1)
	var enabledServices []string
	for _, cfg := range configs {
		svc := cfg.GetService()
		if svc == "" {
			continue
		}
		var logTypes []string
		for _, lc := range cfg.GetAuditLogConfigs() {
			logTypes = append(logTypes, lc.GetLogType().String())
		}
		meta["auditLog_"+svc] = strings.Join(logTypes, ",")
		enabledServices = append(enabledServices, svc)
	}
	if len(enabledServices) > 0 {
		meta["auditLogServices"] = strings.Join(enabledServices, ",")
	}
	return meta
}
