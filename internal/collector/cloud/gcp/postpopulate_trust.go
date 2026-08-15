// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

const methodGCPCrossProjectTrust = "gcp-cross-project-trust"

// impersonationRoles are GCP IAM roles that grant cross-project service
// account impersonation capabilities.
var impersonationRoles = map[string]bool{
	"roles/iam.serviceAccountTokenCreator": true,
	"roles/iam.serviceAccountUser":         true,
}

// resolveCrossProjectTrust scans IAM binding edges (EdgeGrants) in the graph
// and emits EdgeTrusts when a service account from one project is granted an
// impersonation role on another project. The trust edge direction is FROM the
// foreign SA TO the local project resource, matching the AWS cross-account
// trust convention.
func resolveCrossProjectTrust(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	// Query all service account nodes.
	sas, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type": string(kgtypes.NodeCloudResource),
		"meta": map[string]string{"resource_type": "gcp:iam:serviceAccount"},
	})
	if err != nil {
		return err
	}
	if len(sas) == 0 {
		return nil
	}

	// Detect the current project from SA resource names.
	currentProject := detectProjectFromSAs(sas)
	if currentProject == "" {
		return nil
	}

	var edges []knowledgev1.Edge
	for _, sa := range sas {
		trustEdges := findTrustEdges(ctx, gc, graphName, sa, currentProject)
		edges = append(edges, trustEdges...)
	}

	if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, edges); err != nil {
		return err
	}

	slog.Debug("gcp cross-project-trust: emitted edges", "count", len(edges))
	return nil
}

// findTrustEdges checks if a service account has incoming GRANTS edges with
// impersonation roles from a service account in a different project.
func findTrustEdges(
	ctx context.Context, gc postpopulate.GraphCaller, graphName string, sa *knowledgev1.Node, currentProject string,
) []knowledgev1.Edge {
	var edges []knowledgev1.Edge

	// Walk incoming GRANTS edges (role -> SA) over the wire.
	incoming, err := postpopulate.BrowseEdges(ctx, gc, kgtypes.GraphCloud, graphName, sa.Id, postpopulate.IncomingEdges, []kgtypes.EdgeType{kgtypes.EdgeGrants})
	if err != nil {
		slog.Debug("gcp cross-project-trust: browse incoming edges failed", "sa", sa.Id, "err", err)
		return nil
	}
	for i := range incoming {
		e := &incoming[i]
		roleName := edgeMetaRole(e)
		if roleName == "" {
			roleName = e.FromId // fall back to edge source (the role string)
		}
		if !impersonationRoles[roleName] {
			continue
		}

		// Parse SA email to determine its project.
		saEmail := kgtypes.Value(sa, "email")
		if saEmail == "" {
			continue
		}
		saProject := projectFromSAEmail(saEmail)
		if saProject == "" || saProject == currentProject {
			continue // same project — not cross-project trust
		}

		// Foreign SA granted impersonation role on this project.
		// EdgeTrusts: FROM foreign SA TO local project.
		edges = append(edges, knowledgev1.Edge{
			FromId: sa.Id,
			ToId:   "projects/" + currentProject,
			Type:   string(kgtypes.EdgeTrusts),
			Method: methodGCPCrossProjectTrust,
		})
	}

	return edges
}

// edgeMetaRole extracts the "role_name" from an edge's Evidence field,
// where cloud-collect edges store their metadata as JSON.
func edgeMetaRole(e *knowledgev1.Edge) string {
	if e.Evidence == "" {
		return ""
	}
	var meta map[string]string
	if err := json.Unmarshal([]byte(e.Evidence), &meta); err != nil {
		return ""
	}
	return meta["role_name"]
}

// detectProjectFromSAs extracts the project ID from the first service account
// resource name. SA resource names follow: projects/{project}/serviceAccounts/{email}.
func detectProjectFromSAs(sas []*knowledgev1.Node) string {
	for _, sa := range sas {
		if p := projectFromSAResourceName(sa.Id); p != "" {
			return p
		}
	}
	return ""
}

// projectFromSAResourceName extracts the project from a SA resource name
// of the form "projects/{project}/serviceAccounts/{email}".
func projectFromSAResourceName(name string) string {
	const prefix = "projects/"
	if !strings.HasPrefix(name, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(name, prefix)
	before, _, ok := strings.Cut(rest, "/")
	if !ok {
		return ""
	}
	return before
}

// projectFromSAEmail extracts the project from a service account email
// of the form "{name}@{project}.iam.gserviceaccount.com".
func projectFromSAEmail(email string) string {
	_, after, ok := strings.Cut(email, "@")
	if !ok {
		return ""
	}
	project, _, ok := strings.Cut(after, ".iam.gserviceaccount.com")
	if !ok {
		return ""
	}
	return project
}
