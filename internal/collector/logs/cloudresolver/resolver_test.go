// SPDX-License-Identifier: Apache-2.0

package cloudresolver

import (
	"context"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// seedResolverAccount is the cloud-graph name used by the resolver
// fixtures. Centralized here so a future multi-account test can grow a
// new helper without renaming every call site.
const seedResolverAccount = "acct-1"

// mkCloudResource builds a NodeCloudResource with the resource_type
// metadata the resolver matches on. Returns a *knowledgev1.Node — the
// wire carrier the GraphSlice fixtures hold directly. Shared by the
// cloudresolver fixtures.
func mkCloudResource(id, name, resourceType string) *knowledgev1.Node {
	n := &knowledgev1.Node{
		Id:         id,
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: name,
		Source:     "cloud",
	}
	kgtypes.SetValue(n, "resource_type", resourceType)
	return n
}

// mkProxy builds a NodeProxy carrying the supplied metadata. Returns a
// *knowledgev1.Node so callers place the pointer directly into a
// []*knowledgev1.Node slice literal — pointers never copy the embedded
// sync-locked proto message, so there is no copylocks concern.
func mkProxy(id string, metadata map[string]string) *knowledgev1.Node {
	return &knowledgev1.Node{
		Id:       id,
		Type:     string(kgtypes.NodeProxy),
		Metadata: metadata,
	}
}

// seedResolverCloudGraph returns a single-account []GraphSlice with a
// cross-section of service-like and namespace-like resources the resolver
// should recognize: ECS service "api", K8s Deployment "api" (so precedence can
// be exercised), a K8s Service "web", a lambda "batcher", a K8s Namespace
// "prod", a VPC "shared-vpc", and a non-matching RDS instance. The slice feeds
// NewCloudSubgraph directly — no store engine.
func seedResolverCloudGraph(t *testing.T) []GraphSlice {
	t.Helper()
	nodes := []*knowledgev1.Node{
		mkCloudResource("arn:aws:ecs:us-east-1:1:service/api", "api", "ecs:service"),
		mkCloudResource("k8s:default/Deployment/api", "api", "Deployment"),
		mkCloudResource("k8s:default/Service/web", "web", "Service"),
		mkCloudResource("arn:aws:lambda:us-east-1:1:function:batcher", "batcher", "lambda:function"),
		mkCloudResource("k8s:Namespace/prod", "prod", "Namespace"),
		mkCloudResource("vpc-0123456789abcdef", "shared-vpc", "ec2:vpc"),
		mkCloudResource("arn:aws:rds:us-east-1:1:db:analytics", "analytics", "rds:db-instance"),
	}
	return []GraphSlice{{Name: seedResolverAccount, Nodes: nodes}}
}

// streamWithLabels constructs a minimal LogStream the resolver can use as
// resolution context. Both LowCardLabels and Labels carry the same values
// because candidateGraphs reads from either.
func streamWithLabels(labels map[string]string) *logwire.LogStream {
	if labels == nil {
		labels = map[string]string{}
	}
	cp := make(map[string]string, len(labels))
	maps.Copy(cp, labels)
	return &logwire.LogStream{
		Labels:        labels,
		LowCardLabels: cp,
	}
}

func TestCloudResolver_ResolveService_PrefersECSOverDeployment(t *testing.T) {
	ctx := context.Background()
	r := NewCloudResolver(NewCloudSubgraph(seedResolverCloudGraph(t)))
	got, ok := r.ResolveService(ctx, streamWithLabels(nil), "api")
	require.True(t, ok, "expected a resolver hit for 'api'")
	assert.Equal(t, "acct-1", got.Account, "should target the only loaded cloud graph")
	assert.Equal(t, "arn:aws:ecs:us-east-1:1:service/api", got.ID,
		"ecs:service should outrank Deployment in the precedence list")
}

func TestCloudResolver_ResolveService_K8sServiceMatch(t *testing.T) {
	ctx := context.Background()
	r := NewCloudResolver(NewCloudSubgraph(seedResolverCloudGraph(t)))
	got, ok := r.ResolveService(ctx, streamWithLabels(nil), "web")
	require.True(t, ok)
	assert.Equal(t, "k8s:default/Service/web", got.ID)
}

func TestCloudResolver_ResolveService_LambdaMatch(t *testing.T) {
	ctx := context.Background()
	r := NewCloudResolver(NewCloudSubgraph(seedResolverCloudGraph(t)))
	got, ok := r.ResolveService(ctx, streamWithLabels(nil), "batcher")
	require.True(t, ok)
	assert.Equal(t, "arn:aws:lambda:us-east-1:1:function:batcher", got.ID)
}

func TestCloudResolver_ResolveService_CaseInsensitive(t *testing.T) {
	ctx := context.Background()
	r := NewCloudResolver(NewCloudSubgraph(seedResolverCloudGraph(t)))
	got, ok := r.ResolveService(ctx, streamWithLabels(nil), "API")
	require.True(t, ok, "case-insensitive match should succeed")
	assert.Equal(t, "arn:aws:ecs:us-east-1:1:service/api", got.ID)
}

func TestCloudResolver_ResolveService_IgnoresNonServiceTypes(t *testing.T) {
	ctx := context.Background()
	r := NewCloudResolver(NewCloudSubgraph(seedResolverCloudGraph(t)))
	_, ok := r.ResolveService(ctx, streamWithLabels(nil), "analytics")
	assert.False(t, ok, "RDS instance is not a service-like resource")
}

func TestCloudResolver_ResolveService_Miss(t *testing.T) {
	ctx := context.Background()
	r := NewCloudResolver(NewCloudSubgraph(seedResolverCloudGraph(t)))
	got, ok := r.ResolveService(ctx, streamWithLabels(nil), "nonexistent")
	assert.False(t, ok)
	assert.Empty(t, got.ID)
	assert.Empty(t, got.Account)
}

func TestCloudResolver_ResolveNamespace_K8sNamespace(t *testing.T) {
	ctx := context.Background()
	r := NewCloudResolver(NewCloudSubgraph(seedResolverCloudGraph(t)))
	got, ok := r.ResolveNamespace(ctx, streamWithLabels(nil), "prod")
	require.True(t, ok)
	assert.Equal(t, "k8s:Namespace/prod", got.ID)
}

func TestCloudResolver_ResolveNamespace_VPCFallback(t *testing.T) {
	ctx := context.Background()
	r := NewCloudResolver(NewCloudSubgraph(seedResolverCloudGraph(t)))
	got, ok := r.ResolveNamespace(ctx, streamWithLabels(nil), "shared-vpc")
	require.True(t, ok)
	assert.Equal(t, "vpc-0123456789abcdef", got.ID)
}

func TestCloudResolver_ResolveNamespace_ServicesAreNotNamespaces(t *testing.T) {
	ctx := context.Background()
	r := NewCloudResolver(NewCloudSubgraph(seedResolverCloudGraph(t)))
	_, ok := r.ResolveNamespace(ctx, streamWithLabels(nil), "api")
	assert.False(t, ok, "ECS services must not resolve as namespaces")
}

func TestCloudResolver_EmptyInputs(t *testing.T) {
	ctx := context.Background()
	r := NewCloudResolver(NewCloudSubgraph(seedResolverCloudGraph(t)))
	_, ok := r.ResolveService(ctx, streamWithLabels(nil), "")
	assert.False(t, ok)
	_, ok = r.ResolveNamespace(ctx, streamWithLabels(nil), "")
	assert.False(t, ok)
}

func TestCloudResolver_NoCloudGraphs_ReturnsFalse(t *testing.T) {
	// No cloud graph slices at all — candidateGraphs returns nil.
	r := NewCloudResolver(NewCloudSubgraph(nil))
	_, ok := r.ResolveService(context.Background(), streamWithLabels(nil), "api")
	assert.False(t, ok, "no cloud graphs should be a clean miss")
}

func TestCloudResolver_NilStore_ReturnsFalse(t *testing.T) {
	r := NewCloudResolver(nil)
	_, ok := r.ResolveService(context.Background(), streamWithLabels(nil), "api")
	assert.False(t, ok)
	_, ok = r.ResolveNamespace(context.Background(), streamWithLabels(nil), "prod")
	assert.False(t, ok)
}

// TestCloudResolver_GKEPriorityOverProject seeds both a parent GCP
// project graph and a GKE cluster graph, both containing a Deployment
// named "api". A stream whose labels carry project_id + cluster_name
// must resolve against the GKE graph first because the workload-bearing
// graph is more specific than the parent project graph.
func TestCloudResolver_GKEPriorityOverProject(t *testing.T) {
	gkeName := "gke_fulminate-services_us-central1_main-us-central1"
	// Two cloud-graph slices, both holding a Deployment named "api": the
	// parent GCP project graph and the more-specific GKE cluster graph.
	slices := []GraphSlice{
		{Name: "fulminate-services", Nodes: []*knowledgev1.Node{
			mkCloudResource("project-api", "api", "Deployment"),
		}},
		{Name: gkeName, Nodes: []*knowledgev1.Node{
			mkCloudResource("gke-api", "api", "Deployment"),
		}},
	}

	r := NewCloudResolver(NewCloudSubgraph(slices))
	stream := streamWithLabels(map[string]string{
		"project_id":   "fulminate-services",
		"cluster_name": "main-us-central1",
	})

	got, ok := r.ResolveService(context.Background(), stream, "api")
	require.True(t, ok)
	assert.Equal(t, gkeName, got.Account, "GKE graph must outrank parent project graph")
	assert.Equal(t, "gke-api", got.ID)
}

// TestCloudResolver_ProjectFallbackWhenNoGKEMatch confirms a stream
// with project_id but no matching GKE graph still resolves against the
// parent project graph.
func TestCloudResolver_ProjectFallbackWhenNoGKEMatch(t *testing.T) {
	slices := []GraphSlice{{Name: "my-project", Nodes: []*knowledgev1.Node{
		mkCloudResource("svc-api", "api", "k8s:Service"),
	}}}

	r := NewCloudResolver(NewCloudSubgraph(slices))
	stream := streamWithLabels(map[string]string{"project_id": "my-project"})

	got, ok := r.ResolveService(context.Background(), stream, "api")
	require.True(t, ok)
	assert.Equal(t, "my-project", got.Account)
	assert.Equal(t, "svc-api", got.ID)
}

func TestGKEGraphMatches(t *testing.T) {
	cases := []struct {
		name     string
		graph    string
		project  string
		cluster  string
		expected bool
	}{
		{"happy path", "gke_my-project_us-central1_main", "my-project", "main", true},
		{"missing prefix", "my-project_us-central1_main", "my-project", "main", false},
		{"wrong project", "gke_other_us-central1_main", "my-project", "main", false},
		{"wrong cluster", "gke_my-project_us-central1_other", "my-project", "main", false},
		{"empty project", "gke__us-central1_main", "", "main", false},
		{"empty cluster", "gke_my-project_us-central1_", "my-project", "", false},
		{"empty region rejected", "gke_my-project__main", "my-project", "main", false},
		{"no region at all", "gke_my-project_main", "my-project", "main", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected,
				gkeGraphMatches(tc.graph, tc.project, tc.cluster))
		})
	}
}

// TestCloudResolver_ProxyDoesNotShadowCloudResource is a T2-1 regression
// guard: a NodeProxy with matching name + resource_type must NOT shadow
// the underlying NodeCloudResource. CreateCrossGraphProxy at
// domains/store/proxy.go:163-177 copies resource_type/region/provider
// onto cloud proxies, so a proxy can otherwise pass nameMatches +
// prefixRank for the wrong account if the resolver doesn't type-filter
// to NodeCloudResource only. Build the subgraph by hand (no store
// fixture) so this test is stable against future store-side changes.
func TestCloudResolver_ProxyDoesNotShadowCloudResource(t *testing.T) {
	const account = "acct-1"

	// Build the two nodes directly as values in the slice literal — a
	// NodeCloudResource "api" and a same-named NodeProxy. Metadata is set
	// inline (not via SetValue) so neither node needs a named pointer that
	// would trip the copylocks vet analyzer on deref.
	sg := NewCloudSubgraph([]GraphSlice{{
		Name: account,
		Nodes: []*knowledgev1.Node{
			mkCloudResource("real-api", "api", "ecs:service"),
			// Match the production CreateCrossGraphProxy GraphCloud branch
			// (domains/store/proxy.go:163-177): foreign_graph + foreign_id
			// + account + foreign_type, plus the resource_type metadata
			// copied from the source node.
			{
				Id:         "proxy:cloud:other-account:foreign-api",
				Type:       string(kgtypes.NodeProxy),
				SymbolName: "api",
				Source:     "proxy:cloud:other-account",
				Metadata: map[string]string{
					"foreign_graph": "cloud",
					"foreign_id":    "foreign-api",
					"account":       "other-account",
					"foreign_type":  string(kgtypes.NodeCloudResource),
					"resource_type": "ecs:service",
				},
			},
		},
	}})

	r := NewCloudResolver(sg)
	got, ok := r.ResolveService(
		context.Background(),
		&logwire.LogStream{Labels: map[string]string{"service": "api"}},
		"api",
	)
	require.True(t, ok, "expected to resolve to the real CloudResource")
	assert.Equal(t, "real-api", got.ID,
		"NodeProxy with matching name+resource_type must NOT shadow the NodeCloudResource")
	assert.Equal(t, account, got.Account)
}
