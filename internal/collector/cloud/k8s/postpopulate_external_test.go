// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// deployWithEnvContent builds minimal Deployment JSON carrying exactly
// one container with the given (name, value) env list. Used by the
// integration tests so we don't depend on the full deployment
// subcollector's fake-clientset flow.
func deployWithEnvContent(t *testing.T, name string, envs map[string]string) []byte {
	t.Helper()
	type envE struct {
		Name  string `json:"name"`
		Value string `json:"value,omitempty"`
	}
	var envList []envE
	for k, v := range envs {
		envList = append(envList, envE{Name: k, Value: v})
	}
	type container struct {
		Name string `json:"name"`
		Env  []envE `json:"env,omitempty"`
	}
	payload := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []container{{
						Name: "main",
						Env:  envList,
					}},
				},
			},
		},
	}
	_ = name
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	return b
}

// workloadNode seeds a workload-shaped cloud resource. All test callers
// use the "default" namespace so we hardcode it here; add a variant if
// a future test needs a different namespace.
func workloadNode(kind, name string, content []byte) *knowledgev1.Node {
	const namespace = "default"
	n := &knowledgev1.Node{
		Id:         resourceID(namespace, kind, name),
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: name,
		Source:     "cloud",
		Content:    string(content),
	}
	kgtypes.SetValue(n, "resource_type", kind)
	kgtypes.SetValue(n, "namespace", namespace)
	return n
}

func collectConnectsToEdges(fake *k8sFake, account, from string) []knowledgev1.Edge {
	return fake.outgoingEdges(account, from, kgtypes.EdgeConnectsTo)
}

// TestResolveExternalConnections_S3AndGCS: a single Deployment with two
// env URLs pointing at S3 and GCS gets two CONNECTS_TO edges, each to
// the correct dangling proxy.
func TestResolveExternalConnections_S3AndGCS(t *testing.T) {
	ctx := newCtx(t)

	const graph = "mixed-cluster"

	fake := newK8sFake()
	content := deployWithEnvContent(t, "myapp", map[string]string{
		"BUCKET_URL":    "s3://my-bucket/data",
		"GCS_DATA_URI":  "gs://my-gcs-bucket/path",
		"UNRELATED_URL": "https://example.com/api",
	})
	d := workloadNode("Deployment", "myapp", content)
	fake.seed(graph, d)

	require.NoError(t, resolveExternalConnections(ctx, fake, graph))

	edges := collectConnectsToEdges(fake, graph, d.Id)
	require.Len(t, edges, 2)

	// S3 proxy exists with the canonical ARN.
	s3Prx := "proxy:cloud::arn:aws:s3:::my-bucket"
	s3Proxy, ok := fake.nodeByID(graph, s3Prx)
	require.True(t, ok)
	assert.Equal(t, "aws:s3:bucket", kgtypes.Value(s3Proxy, "resource_type"))

	// GCS proxy exists with its canonical ID.
	gcsPrx := "proxy:cloud::gs://my-gcs-bucket"
	_, ok = fake.nodeByID(graph, gcsPrx)
	require.True(t, ok)

	// Evidence carries env + container + pattern + matched URI — NOT the raw env value.
	for i := range edges {
		e := &edges[i]
		assert.Contains(t, e.Evidence, "container=main")
		assert.Contains(t, e.Evidence, "pattern=")
		assert.Contains(t, e.Evidence, "matched=")
	}
}

// TestResolveExternalConnections_MixedLiteralAndValueFrom: a Deployment
// with BOTH a literal env value AND a valueFrom.secretKeyRef — the
// literal path emits its edge and the ref path is NOT scanned here
// (that's exercised in postpopulate_external_refs_test.go). This is the
// literal-side mirror test: ensures pass 1 does not accidentally try to
// scan valueFrom entries (env.Value is empty there so there's nothing
// to regex).
func TestResolveExternalConnections_MixedLiteralAndValueFrom(t *testing.T) {
	ctx := newCtx(t)

	const graph = "mixed-skip-cluster"

	fake := newK8sFake()
	// Deployment with one env having valueFrom (no matching Secret in the
	// graph → the refs pass logs Warn and contributes zero edges) and one
	// literal env with a real URL.
	payload := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []map[string]any{{
						"name": "main",
						"env": []map[string]any{
							{
								"name":      "DB_PASSWORD",
								"valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "missing-secret", "key": "pw"}},
							},
							{
								"name":  "PUBLIC_BUCKET",
								"value": "s3://public-bucket/data",
							},
						},
					}},
				},
			},
		},
	}
	content, err := json.Marshal(payload)
	require.NoError(t, err)

	d := workloadNode("Deployment", "app", content)
	fake.seed(graph, d)

	require.NoError(t, resolveExternalConnections(ctx, fake, graph))

	edges := collectConnectsToEdges(fake, graph, d.Id)
	require.Len(t, edges, 1, "literal value yields one edge; unresolved ref yields zero")
	assert.Contains(t, edges[0].Evidence, "env=PUBLIC_BUCKET")
	assert.NotContains(t, edges[0].Evidence, "DB_PASSWORD")
}

// TestResolveExternalConnections_Pod: Pod kind is handled via
// spec.containers (no template indirection).
func TestResolveExternalConnections_Pod(t *testing.T) {
	ctx := newCtx(t)

	const graph = "pod-cluster"

	fake := newK8sFake()
	payload := map[string]any{
		"spec": map[string]any{
			"containers": []map[string]any{{
				"name": "worker",
				"env": []map[string]any{
					{"name": "QUEUE_URL", "value": "https://sqs.us-east-1.amazonaws.com/123456789012/my-queue"},
				},
			}},
		},
	}
	content, err := json.Marshal(payload)
	require.NoError(t, err)
	pod := workloadNode("Pod", "worker-1", content)
	fake.seed(graph, pod)

	require.NoError(t, resolveExternalConnections(ctx, fake, graph))

	edges := collectConnectsToEdges(fake, graph, pod.Id)
	require.Len(t, edges, 1)
	assert.Contains(t, edges[0].Evidence, "pattern=aws:sqs")
	// SQS URL carries the account → non-dangling proxy.
	assert.Equal(t, "proxy:cloud:123456789012:arn:aws:sqs:us-east-1:123456789012:my-queue", edges[0].ToId)
}

// TestResolveExternalConnections_CronJob: CronJob kind is handled via
// spec.jobTemplate.spec.template.spec.containers.
func TestResolveExternalConnections_CronJob(t *testing.T) {
	ctx := newCtx(t)

	const graph = "cj-cluster"

	fake := newK8sFake()
	payload := map[string]any{
		"spec": map[string]any{
			"jobTemplate": map[string]any{
				"spec": map[string]any{
					"template": map[string]any{
						"spec": map[string]any{
							"containers": []map[string]any{{
								"name": "batch",
								"env": []map[string]any{
									{"name": "DATA_URI", "value": "gs://nightly/data"},
								},
							}},
						},
					},
				},
			},
		},
	}
	content, err := json.Marshal(payload)
	require.NoError(t, err)
	cj := workloadNode("CronJob", "nightly", content)
	fake.seed(graph, cj)

	require.NoError(t, resolveExternalConnections(ctx, fake, graph))

	edges := collectConnectsToEdges(fake, graph, cj.Id)
	require.Len(t, edges, 1)
	assert.Contains(t, edges[0].Evidence, "pattern=gcp:gcs")
}

// TestResolveExternalConnections_Dedup: the same URI referenced in
// multiple containers or env vars produces only one edge.
func TestResolveExternalConnections_Dedup(t *testing.T) {
	ctx := newCtx(t)

	const graph = "dedup-cluster"

	fake := newK8sFake()
	payload := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []map[string]any{
						{
							"name": "c1",
							"env": []map[string]any{
								{"name": "URL", "value": "s3://shared-bucket/x"},
							},
						},
						{
							"name": "c2",
							"env": []map[string]any{
								{"name": "URL", "value": "s3://shared-bucket/y"},
							},
						},
					},
				},
			},
		},
	}
	content, err := json.Marshal(payload)
	require.NoError(t, err)
	d := workloadNode("Deployment", "dedup", content)
	fake.seed(graph, d)

	require.NoError(t, resolveExternalConnections(ctx, fake, graph))
	edges := collectConnectsToEdges(fake, graph, d.Id)
	assert.Len(t, edges, 1, "dup env values across containers should dedupe to one edge")
}

// TestResolveExternalConnections_NoWorkloads: graph with no matching
// workload types is a silent no-op.
func TestResolveExternalConnections_NoWorkloads(t *testing.T) {
	ctx := newCtx(t)

	fake := newK8sFake()
	require.NoError(t, resolveExternalConnections(ctx, fake, "empty-cluster"))
}
