// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"errors"
	"net/http"

	compute "cloud.google.com/go/compute/apiv1"
	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

// sharedVPCSubCollector detects Shared VPC host/service project relationships
// and generates cascade targets for related projects. Produces no resource
// nodes — this subcollector is purely for cascade discovery.
type sharedVPCSubCollector struct {
	client    *compute.ProjectsClient
	projectID string
}

func newSharedVPCSubCollector(client *compute.ProjectsClient, projectID string) *sharedVPCSubCollector {
	return &sharedVPCSubCollector{client: client, projectID: projectID}
}

func (c *sharedVPCSubCollector) Name() string { return "gcp-shared-vpc" }

func (c *sharedVPCSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	// Check if this project is a Shared VPC host by querying for XPN host info.
	hostProject, err := c.client.GetXpnHost(ctx, &computepb.GetXpnHostProjectRequest{
		Project: c.projectID,
	})
	if err != nil {
		// 400 ("project is not in a Shared VPC") and 403 ("no permission to
		// call compute.projects.getXpnHost") are the expected case for the
		// majority of GCP projects — Shared VPC is a rare configuration. Treat
		// as "not a participant" and return cleanly so RunSubCollectors
		// doesn't log a phantom failure for every regular project. Real
		// errors (network, transient 5xx, malformed request) still propagate.
		if isXpnNotApplicable(err) {
			return result, nil
		}
		return result, err
	}

	hostName := hostProject.GetName()
	hostStatus := hostProject.GetXpnProjectStatus()

	switch {
	case hostStatus == "HOST" && hostName == c.projectID:
		targets, err := c.listServiceProjects(ctx)
		if err != nil {
			return result, err
		}
		result.Targets = targets
	case hostName != "" && hostName != c.projectID:
		// This is a service project. Cascade to the host project.
		result.Targets = append(result.Targets, cloud.CollectTarget{
			Collector: "gcp",
			ID:        hostName,
		})
	}

	return result, nil
}

// isXpnNotApplicable reports whether a GetXpnHost error indicates "this
// project is not a Shared VPC participant" rather than a real failure. The
// REST API returns HTTP 400 for non-participating projects (the canonical
// "expected" case) and HTTP 403 when the caller lacks getXpnHost
// permission. Both collapse to "skip silently" semantics for this
// detection-only subcollector.
func isXpnNotApplicable(err error) bool {
	if err == nil {
		return false
	}
	if apiErr, ok := errors.AsType[*googleapi.Error](err); ok {
		return apiErr.Code == http.StatusBadRequest ||
			apiErr.Code == http.StatusForbidden
	}
	return isPermissionDenied(err)
}

// listServiceProjects lists XPN service projects attached to this host project.
func (c *sharedVPCSubCollector) listServiceProjects(ctx context.Context) ([]cloud.CollectTarget, error) {
	it := c.client.GetXpnResources(ctx, &computepb.GetXpnResourcesProjectsRequest{
		Project: c.projectID,
	})

	var targets []cloud.CollectTarget
	for {
		resource, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return targets, err
		}

		// XPN resources of type "PROJECT" are service projects.
		if resource.GetType() == "PROJECT" && resource.GetId() != "" {
			targets = append(targets, cloud.CollectTarget{
				Collector: "gcp",
				ID:        resource.GetId(),
			})
		}
	}

	return targets, nil
}
