// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"fmt"

	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	compute "cloud.google.com/go/compute/apiv1"
	container "cloud.google.com/go/container/apiv1"
	eventarc "cloud.google.com/go/eventarc/apiv1"
	filestore "cloud.google.com/go/filestore/apiv1"
	functions "cloud.google.com/go/functions/apiv2"
	iam "cloud.google.com/go/iam/admin/apiv1"
	iamv1 "cloud.google.com/go/iam/apiv1"
	kms "cloud.google.com/go/kms/apiv1"
	logging "cloud.google.com/go/logging/apiv2"
	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	"cloud.google.com/go/pubsub"
	redis "cloud.google.com/go/redis/apiv1"
	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	run "cloud.google.com/go/run/apiv2"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	gcs "cloud.google.com/go/storage"
	"golang.org/x/oauth2/google"
	bq "google.golang.org/api/bigquery/v2"
	cloudidentity "google.golang.org/api/cloudidentity/v1"
	cloudscheduler "google.golang.org/api/cloudscheduler/v1"
	cloudtasks "google.golang.org/api/cloudtasks/v2"
	dataflow "google.golang.org/api/dataflow/v1b3"
	dns "google.golang.org/api/dns/v1"
	firestore "google.golang.org/api/firestore/v1"
	"google.golang.org/api/option"
	sqladmin "google.golang.org/api/sqladmin/v1beta4"
	"google.golang.org/api/vpcaccess/v1"

	workflows "cloud.google.com/go/workflows/apiv1"
)

// gcpClients holds all GCP SDK clients used by subcollectors.
type gcpClients struct {
	instances          *compute.InstancesClient
	networks           *compute.NetworksClient
	subnets            *compute.SubnetworksClient
	firewalls          *compute.FirewallsClient
	container          *container.ClusterManagerClient
	run                *run.ServicesClient
	functions          *functions.FunctionClient
	sqladmin           *sqladmin.Service
	storage            *gcs.Client
	pubsub             *pubsub.Client
	secrets            *secretmanager.Client
	iam                *iam.IamClient
	resourceManager    *resourcemanager.ProjectsClient
	computeProjects    *compute.ProjectsClient
	vpcConnectors      *vpcaccess.Service
	artifactRegistry   *artifactregistry.Client
	urlMaps            *compute.UrlMapsClient
	backendServices    *compute.BackendServicesClient
	forwardingRules    *compute.GlobalForwardingRulesClient
	targetHTTPProxies  *compute.TargetHttpProxiesClient
	targetHTTPSProxies *compute.TargetHttpsProxiesClient
	redis              *redis.CloudRedisClient
	dns                *dns.Service
	securityPolicies   *compute.SecurityPoliciesClient
	routers            *compute.RoutersClient
	cloudTasks         *cloudtasks.Service
	cloudScheduler     *cloudscheduler.Service
	bigquery           *bq.Service
	loggingConfig      *logging.ConfigClient
	instanceGroups     *compute.InstanceGroupsClient
	sslCertificates    *compute.SslCertificatesClient
	alertPolicies      *monitoring.AlertPolicyClient
	eventarcClient     *eventarc.Client
	kms                *kms.KeyManagementClient
	firestore          *firestore.Service
	iamPolicy          *iamv1.IamPolicyClient
	disks              *compute.DisksClient
	filestore          *filestore.CloudFilestoreManagerClient
	workflows          *workflows.Client
	dataflow           *dataflow.Service
	cloudIdentity      *cloudidentity.Service
}

// newGCPClients creates all GCP SDK clients from Application Default Credentials.
func newGCPClients(ctx context.Context, creds *google.Credentials, projectID string) (*gcpClients, error) {
	opts := []option.ClientOption{option.WithCredentials(creds)}
	c := &gcpClients{}
	var err error

	if c.instances, err = compute.NewInstancesRESTClient(ctx, opts...); err != nil {
		return nil, fmt.Errorf("compute instances client: %w", err)
	}
	if c.networks, err = compute.NewNetworksRESTClient(ctx, opts...); err != nil {
		c.Close()
		return nil, fmt.Errorf("compute networks client: %w", err)
	}
	if c.subnets, err = compute.NewSubnetworksRESTClient(ctx, opts...); err != nil {
		c.Close()
		return nil, fmt.Errorf("compute subnets client: %w", err)
	}
	if c.firewalls, err = compute.NewFirewallsRESTClient(ctx, opts...); err != nil {
		c.Close()
		return nil, fmt.Errorf("compute firewalls client: %w", err)
	}
	if c.container, err = container.NewClusterManagerClient(ctx, opts...); err != nil {
		c.Close()
		return nil, fmt.Errorf("container client: %w", err)
	}
	if c.run, err = run.NewServicesClient(ctx, opts...); err != nil {
		c.Close()
		return nil, fmt.Errorf("cloud run client: %w", err)
	}
	if c.functions, err = functions.NewFunctionClient(ctx, opts...); err != nil {
		c.Close()
		return nil, fmt.Errorf("cloud functions client: %w", err)
	}
	if c.sqladmin, err = sqladmin.NewService(ctx, opts...); err != nil {
		c.Close()
		return nil, fmt.Errorf("cloud sql client: %w", err)
	}
	if c.storage, err = gcs.NewClient(ctx, opts...); err != nil {
		c.Close()
		return nil, fmt.Errorf("storage client: %w", err)
	}
	if c.pubsub, err = pubsub.NewClient(ctx, projectID, opts...); err != nil {
		c.Close()
		return nil, fmt.Errorf("pubsub client: %w", err)
	}
	if c.secrets, err = secretmanager.NewClient(ctx, opts...); err != nil {
		c.Close()
		return nil, fmt.Errorf("secret manager client: %w", err)
	}
	if c.iam, err = iam.NewIamClient(ctx, opts...); err != nil {
		c.Close()
		return nil, fmt.Errorf("iam client: %w", err)
	}
	if c.resourceManager, err = resourcemanager.NewProjectsClient(ctx, opts...); err != nil {
		c.Close()
		return nil, fmt.Errorf("resource manager client: %w", err)
	}
	if c.computeProjects, err = compute.NewProjectsRESTClient(ctx, opts...); err != nil {
		c.Close()
		return nil, fmt.Errorf("compute projects client: %w", err)
	}

	if err := c.initExtendedClients(ctx, opts); err != nil {
		return nil, err
	}

	return c, nil
}

// Close releases all SDK client connections.
func (c *gcpClients) Close() {
	c.closeCoreClients()
	c.closeExtendedClients()
}

// closeCoreClients closes the clients initialized in newGCPClients.
func (c *gcpClients) closeCoreClients() { //nolint:funlen // sequential close list
	if c.instances != nil {
		c.instances.Close()
	}
	if c.networks != nil {
		c.networks.Close()
	}
	if c.subnets != nil {
		c.subnets.Close()
	}
	if c.firewalls != nil {
		c.firewalls.Close()
	}
	if c.container != nil {
		c.container.Close()
	}
	if c.run != nil {
		c.run.Close()
	}
	if c.functions != nil {
		c.functions.Close()
	}
	// sqladmin.Service uses HTTP — no Close needed.
	if c.storage != nil {
		c.storage.Close()
	}
	if c.pubsub != nil {
		c.pubsub.Close()
	}
	if c.secrets != nil {
		c.secrets.Close()
	}
	if c.iam != nil {
		c.iam.Close()
	}
	if c.resourceManager != nil {
		c.resourceManager.Close()
	}
	if c.computeProjects != nil {
		c.computeProjects.Close()
	}
}

// closeExtendedClients closes the clients initialized in initExtendedClients.
func (c *gcpClients) closeExtendedClients() { //nolint:funlen // sequential close list
	// vpcConnectors uses HTTP — no Close needed.
	if c.artifactRegistry != nil {
		c.artifactRegistry.Close()
	}
	if c.urlMaps != nil {
		c.urlMaps.Close()
	}
	if c.backendServices != nil {
		c.backendServices.Close()
	}
	if c.forwardingRules != nil {
		c.forwardingRules.Close()
	}
	if c.targetHTTPProxies != nil {
		c.targetHTTPProxies.Close()
	}
	if c.targetHTTPSProxies != nil {
		c.targetHTTPSProxies.Close()
	}
	if c.redis != nil {
		c.redis.Close()
	}
	// dns, cloudTasks, cloudScheduler, bigquery, cloudIdentity use HTTP — no Close needed.
	if c.securityPolicies != nil {
		c.securityPolicies.Close()
	}
	if c.routers != nil {
		c.routers.Close()
	}
	if c.loggingConfig != nil {
		c.loggingConfig.Close()
	}
	if c.instanceGroups != nil {
		c.instanceGroups.Close()
	}
	if c.sslCertificates != nil {
		c.sslCertificates.Close()
	}
	if c.alertPolicies != nil {
		c.alertPolicies.Close()
	}
	if c.eventarcClient != nil {
		c.eventarcClient.Close()
	}
	if c.kms != nil {
		c.kms.Close()
	}
	if c.iamPolicy != nil {
		c.iamPolicy.Close()
	}
	if c.disks != nil {
		c.disks.Close()
	}
	if c.filestore != nil {
		c.filestore.Close()
	}
	if c.workflows != nil {
		c.workflows.Close()
	}
}
