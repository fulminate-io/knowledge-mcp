// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"fmt"

	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	compute "cloud.google.com/go/compute/apiv1"
	eventarc "cloud.google.com/go/eventarc/apiv1"
	filestore "cloud.google.com/go/filestore/apiv1"
	iamv1 "cloud.google.com/go/iam/apiv1"
	kms "cloud.google.com/go/kms/apiv1"
	logging "cloud.google.com/go/logging/apiv2"
	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	redis "cloud.google.com/go/redis/apiv1"
	workflows "cloud.google.com/go/workflows/apiv1"
	bq "google.golang.org/api/bigquery/v2"
	cloudidentity "google.golang.org/api/cloudidentity/v1"
	cloudscheduler "google.golang.org/api/cloudscheduler/v1"
	cloudtasks "google.golang.org/api/cloudtasks/v2"
	dataflow "google.golang.org/api/dataflow/v1b3"
	dns "google.golang.org/api/dns/v1"
	firestore "google.golang.org/api/firestore/v1"
	"google.golang.org/api/option"
	"google.golang.org/api/vpcaccess/v1"
)

// initExtendedClients creates additional SDK clients beyond the core set.
func (c *gcpClients) initExtendedClients(ctx context.Context, opts []option.ClientOption) error { //nolint:funlen // sequential client init list
	var err error

	if c.vpcConnectors, err = vpcaccess.NewService(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("vpc access client: %w", err)
	}
	if c.artifactRegistry, err = artifactregistry.NewClient(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("artifact registry client: %w", err)
	}
	if c.urlMaps, err = compute.NewUrlMapsRESTClient(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("url maps client: %w", err)
	}
	if c.backendServices, err = compute.NewBackendServicesRESTClient(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("backend services client: %w", err)
	}
	if c.forwardingRules, err = compute.NewGlobalForwardingRulesRESTClient(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("forwarding rules client: %w", err)
	}
	if c.targetHTTPProxies, err = compute.NewTargetHttpProxiesRESTClient(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("target http proxies client: %w", err)
	}
	if c.targetHTTPSProxies, err = compute.NewTargetHttpsProxiesRESTClient(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("target https proxies client: %w", err)
	}
	if c.redis, err = redis.NewCloudRedisClient(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("redis client: %w", err)
	}
	if c.dns, err = dns.NewService(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("cloud dns client: %w", err)
	}
	if c.securityPolicies, err = compute.NewSecurityPoliciesRESTClient(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("security policies client: %w", err)
	}
	if c.routers, err = compute.NewRoutersRESTClient(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("routers client: %w", err)
	}
	if c.cloudTasks, err = cloudtasks.NewService(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("cloud tasks client: %w", err)
	}
	if c.cloudScheduler, err = cloudscheduler.NewService(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("cloud scheduler client: %w", err)
	}
	if c.bigquery, err = bq.NewService(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("bigquery client: %w", err)
	}
	if c.loggingConfig, err = logging.NewConfigClient(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("logging config client: %w", err)
	}
	if c.instanceGroups, err = compute.NewInstanceGroupsRESTClient(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("instance groups client: %w", err)
	}
	if c.sslCertificates, err = compute.NewSslCertificatesRESTClient(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("ssl certificates client: %w", err)
	}
	if c.alertPolicies, err = monitoring.NewAlertPolicyClient(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("alert policy client: %w", err)
	}
	if c.eventarcClient, err = eventarc.NewClient(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("eventarc client: %w", err)
	}
	if c.kms, err = kms.NewKeyManagementClient(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("kms client: %w", err)
	}
	if c.firestore, err = firestore.NewService(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("firestore client: %w", err)
	}
	if c.iamPolicy, err = iamv1.NewIamPolicyClient(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("iam policy client: %w", err)
	}
	if c.disks, err = compute.NewDisksRESTClient(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("disks client: %w", err)
	}
	if c.filestore, err = filestore.NewCloudFilestoreManagerClient(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("filestore client: %w", err)
	}
	if c.workflows, err = workflows.NewClient(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("workflows client: %w", err)
	}
	if c.dataflow, err = dataflow.NewService(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("dataflow client: %w", err)
	}
	if c.cloudIdentity, err = cloudidentity.NewService(ctx, opts...); err != nil {
		c.Close()
		return fmt.Errorf("cloud identity client: %w", err)
	}

	return nil
}
