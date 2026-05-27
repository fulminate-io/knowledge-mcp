// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// secretNode seeds a Secret-shaped cloud resource in the "default"
// namespace with type "Opaque". Content matches the Phase 1 shape
// written by sub_secrets.go: {type, keys, data}. Fixtures that need a
// different Secret type or namespace can build the node inline.
func secretNode(t *testing.T, name string, data map[string]string) *knowledgev1.Node {
	t.Helper()
	const (
		namespace  = "default"
		secretType = "Opaque"
	)
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	content := map[string]any{
		"type": secretType,
		"keys": keys,
		"data": data,
	}
	b, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("secretNode: marshal content: %v", err)
	}
	n := &knowledgev1.Node{
		Id:         resourceID(namespace, "Secret", name),
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: name,
		Source:     "cloud",
		Content:    string(b),
	}
	kgtypes.SetValue(n, "resource_type", "Secret")
	kgtypes.SetValue(n, "namespace", namespace)
	return n
}

// configMapNode seeds a ConfigMap-shaped cloud resource. Content
// matches the Phase 1 shape written by sub_configmaps.go:
// {keys, data, binary_data_keys}.
func configMapNode(t *testing.T, namespace, name string, data map[string]string, binaryKeys []string) *knowledgev1.Node {
	t.Helper()
	keys := make([]string, 0, len(data)+len(binaryKeys))
	for k := range data {
		keys = append(keys, k)
	}
	keys = append(keys, binaryKeys...)
	content := map[string]any{
		"keys":             keys,
		"data":             data,
		"binary_data_keys": binaryKeys,
	}
	b, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("configMapNode: marshal content: %v", err)
	}
	n := &knowledgev1.Node{
		Id:         resourceID(namespace, "ConfigMap", name),
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: name,
		Source:     "cloud",
		Content:    string(b),
	}
	kgtypes.SetValue(n, "resource_type", "ConfigMap")
	kgtypes.SetValue(n, "namespace", namespace)
	return n
}

// deployWithValueFromEnv builds a Deployment with a single container
// whose env[] contains one entry using env.valueFrom.<refField> =
// {name, key}. refField is "secretKeyRef" or "configMapKeyRef".
func deployWithValueFromEnv(t *testing.T, envName, refField, refName, refKey string) []byte {
	t.Helper()
	payload := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []map[string]any{{
						"name": "main",
						"env": []map[string]any{{
							"name": envName,
							"valueFrom": map[string]any{
								refField: map[string]any{"name": refName, "key": refKey},
							},
						}},
					}},
				},
			},
		},
	}
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	return b
}

// deployWithEnvFrom builds a Deployment with a single container whose
// envFrom[] references a Secret or ConfigMap by name. refField is
// "secretRef" or "configMapRef".
func deployWithEnvFrom(t *testing.T, refField, refName string) []byte {
	t.Helper()
	payload := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []map[string]any{{
						"name": "main",
						"envFrom": []map[string]any{{
							refField: map[string]any{"name": refName},
						}},
					}},
				},
			},
		},
	}
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	return b
}

// --- Step 1: valueFrom happy path ---------------------------------

// TestResolveExternalConnections_ValueFromSecretKeyRef_EmitsEdge
// inverts the former SkipsValueFrom test — a referenced Secret's
// value is now scanned for cloud URIs and surfaces as a CONNECTS_TO
// edge with valueFrom-style evidence.
func TestResolveExternalConnections_ValueFromSecretKeyRef_EmitsEdge(t *testing.T) {
	ctx := newCtx(t)

	const graph = "vf-secret-cluster"

	fake := newK8sFake()
	// Secret holds an S3 URL — aws:s3 pattern will match "s3://billing-exports/".
	secret := secretNode(t, "billing-creds", map[string]string{
		"DB_URL": "s3://billing-exports/",
	})
	content := deployWithValueFromEnv(t, "DB_URL", "secretKeyRef", "billing-creds", "DB_URL")
	d := workloadNode("Deployment", "billing-app", content)

	fake.seed(graph, secret, d)

	require.NoError(t, resolveExternalConnections(ctx, fake, graph))

	edges := collectConnectsToEdges(fake, graph, d.Id)
	require.Len(t, edges, 1, "valueFrom.secretKeyRef must emit exactly one edge")
	e := &edges[0]
	assert.Equal(t, "proxy:cloud::arn:aws:s3:::billing-exports", e.ToId)
	assert.Contains(t, e.Evidence, "container=main")
	assert.Contains(t, e.Evidence, "env=DB_URL")
	assert.Contains(t, e.Evidence, "pattern=aws:s3")
	assert.Contains(t, e.Evidence, "matched=s3://billing-exports/")
}

// TestResolveExternalConnections_ValueFromConfigMapKeyRef_EmitsEdge
// covers the parallel ConfigMap path with a GCS URI.
func TestResolveExternalConnections_ValueFromConfigMapKeyRef_EmitsEdge(t *testing.T) {
	ctx := newCtx(t)

	const graph = "vf-cm-cluster"

	fake := newK8sFake()
	cm := configMapNode(t, "default", "app-config", map[string]string{
		"DATA_URI": "gs://nightly-exports/partitions",
	}, nil)
	content := deployWithValueFromEnv(t, "DATA_URI", "configMapKeyRef", "app-config", "DATA_URI")
	d := workloadNode("Deployment", "etl", content)

	fake.seed(graph, cm, d)

	require.NoError(t, resolveExternalConnections(ctx, fake, graph))

	edges := collectConnectsToEdges(fake, graph, d.Id)
	require.Len(t, edges, 1)
	e := &edges[0]
	assert.Equal(t, "proxy:cloud::gs://nightly-exports", e.ToId)
	assert.Contains(t, e.Evidence, "env=DATA_URI")
	assert.Contains(t, e.Evidence, "pattern=gcp:gcs")
	assert.Contains(t, e.Evidence, "matched=gs://nightly-exports/")
}

// TestResolveExternalConnections_ValueFromDedupWithLiteral verifies
// the dedup contract promised by the shared `seen` map: a workload
// with BOTH a literal env value AND a valueFrom ref pointing to the
// same URI emits only ONE edge.
func TestResolveExternalConnections_ValueFromDedupWithLiteral(t *testing.T) {
	ctx := newCtx(t)

	const graph = "dedup-mixed-cluster"

	fake := newK8sFake()
	secret := secretNode(t, "s", map[string]string{
		"URL": "s3://shared-bucket/x",
	})
	payload := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []map[string]any{{
						"name": "main",
						"env": []map[string]any{
							{"name": "LITERAL_URL", "value": "s3://shared-bucket/y"},
							{
								"name":      "REF_URL",
								"valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "s", "key": "URL"}},
							},
						},
					}},
				},
			},
		},
	}
	content, err := json.Marshal(payload)
	require.NoError(t, err)
	d := workloadNode("Deployment", "dedup-mixed", content)

	fake.seed(graph, secret, d)

	require.NoError(t, resolveExternalConnections(ctx, fake, graph))
	edges := collectConnectsToEdges(fake, graph, d.Id)
	// Both URIs canonicalize to "arn:aws:s3:::shared-bucket" — dedup to 1.
	require.Len(t, edges, 1, "literal + ref pointing at the same bucket must dedup to one edge")
}

// --- Step 2: envFrom coverage -------------------------------------

// TestResolveExternalConnections_EnvFromSecretRef: bulk Secret import
// with multiple keys — two contain URIs, one doesn't. Expect two
// edges, one per URI, both with envFrom-style evidence naming the
// matching key.
func TestResolveExternalConnections_EnvFromSecretRef(t *testing.T) {
	ctx := newCtx(t)

	const graph = "ef-secret-cluster"

	fake := newK8sFake()
	secret := secretNode(t, "bulk-creds", map[string]string{
		"S3_URL":  "s3://bucket-a/data",
		"GCS_URL": "gs://bucket-b/data",
		"OPAQUE":  "some non-uri value",
	})
	content := deployWithEnvFrom(t, "secretRef", "bulk-creds")
	d := workloadNode("Deployment", "bulk-app", content)

	fake.seed(graph, secret, d)

	require.NoError(t, resolveExternalConnections(ctx, fake, graph))

	edges := collectConnectsToEdges(fake, graph, d.Id)
	require.Len(t, edges, 2, "two URI-bearing keys → two edges; opaque key → zero")

	byPattern := map[string]*knowledgev1.Edge{}
	for i := range edges {
		e := &edges[i]
		byPattern[e.Method] = e
		assert.Contains(t, e.Evidence, "container=main")
		assert.Contains(t, e.Evidence, "envFrom=bulk-creds")
	}
	s3Edge, ok := byPattern["aws:s3"]
	require.True(t, ok, "expected aws:s3 edge")
	assert.Contains(t, s3Edge.Evidence, "key=S3_URL")
	gcsEdge, ok := byPattern["gcp:gcs"]
	require.True(t, ok, "expected gcp:gcs edge")
	assert.Contains(t, gcsEdge.Evidence, "key=GCS_URL")
}

// TestResolveExternalConnections_EnvFromConfigMapRef: bulk ConfigMap
// import. One URI-bearing key is present in data, one binary-only key
// is listed in binary_data_keys (absent from data). The binary-only
// key must be silently skipped (no edge, no Warn-worthy error).
func TestResolveExternalConnections_EnvFromConfigMapRef(t *testing.T) {
	ctx := newCtx(t)

	const graph = "ef-cm-cluster"

	fake := newK8sFake()
	cm := configMapNode(t, "default", "mixed-cm",
		map[string]string{
			"QUEUE_URL": "https://sqs.us-east-1.amazonaws.com/123456789012/my-queue",
		},
		[]string{"binary-blob.bin"},
	)
	content := deployWithEnvFrom(t, "configMapRef", "mixed-cm")
	d := workloadNode("Deployment", "worker", content)

	fake.seed(graph, cm, d)

	require.NoError(t, resolveExternalConnections(ctx, fake, graph))

	edges := collectConnectsToEdges(fake, graph, d.Id)
	require.Len(t, edges, 1, "one URI-bearing key produces one edge; binary-only key is silently skipped")
	e := &edges[0]
	assert.Equal(t, "aws:sqs", e.Method)
	assert.Contains(t, e.Evidence, "envFrom=mixed-cm")
	assert.Contains(t, e.Evidence, "key=QUEUE_URL")
}

// --- Step 3: miss paths -------------------------------------------

// TestResolveExternalConnections_MissingSecret: valueFrom points at a
// Secret that isn't in the graph → 0 edges, no panic, no error.
func TestResolveExternalConnections_MissingSecret(t *testing.T) {
	ctx := newCtx(t)

	const graph = "miss-secret-cluster"

	fake := newK8sFake()
	content := deployWithValueFromEnv(t, "URL", "secretKeyRef", "no-such-secret", "URL")
	d := workloadNode("Deployment", "app", content)
	fake.seed(graph, d)

	require.NoError(t, resolveExternalConnections(ctx, fake, graph))
	edges := collectConnectsToEdges(fake, graph, d.Id)
	assert.Empty(t, edges, "unresolved Secret ref must not emit edges")
}

// TestResolveExternalConnections_MissingConfigMap: mirror of the
// above for ConfigMap refs.
func TestResolveExternalConnections_MissingConfigMap(t *testing.T) {
	ctx := newCtx(t)

	const graph = "miss-cm-cluster"

	fake := newK8sFake()
	content := deployWithEnvFrom(t, "configMapRef", "no-such-cm")
	d := workloadNode("Deployment", "app", content)
	fake.seed(graph, d)

	require.NoError(t, resolveExternalConnections(ctx, fake, graph))
	edges := collectConnectsToEdges(fake, graph, d.Id)
	assert.Empty(t, edges)
}

// TestResolveExternalConnections_EmptySecretData: Secret exists but
// its data is empty → 0 edges.
func TestResolveExternalConnections_EmptySecretData(t *testing.T) {
	ctx := newCtx(t)

	const graph = "empty-secret-cluster"

	fake := newK8sFake()
	secret := secretNode(t, "empty", map[string]string{})
	content := deployWithEnvFrom(t, "secretRef", "empty")
	d := workloadNode("Deployment", "app", content)

	fake.seed(graph, secret, d)

	require.NoError(t, resolveExternalConnections(ctx, fake, graph))
	edges := collectConnectsToEdges(fake, graph, d.Id)
	assert.Empty(t, edges)
}

// TestResolveExternalConnections_SecretWithNoURIs: Secret has data
// values but none match any pattern → 0 edges.
func TestResolveExternalConnections_SecretWithNoURIs(t *testing.T) {
	ctx := newCtx(t)

	const graph = "no-uri-secret-cluster"

	fake := newK8sFake()
	secret := secretNode(t, "creds", map[string]string{
		"PASSWORD": "hunter2",
		"TOKEN":    "abcdef",
		"USERNAME": "admin",
	})
	content := deployWithEnvFrom(t, "secretRef", "creds")
	d := workloadNode("Deployment", "app", content)

	fake.seed(graph, secret, d)

	require.NoError(t, resolveExternalConnections(ctx, fake, graph))
	edges := collectConnectsToEdges(fake, graph, d.Id)
	assert.Empty(t, edges, "data values with no pattern matches must emit zero edges")
}

// TestResolveExternalConnections_SecretKeyMissing: valueFrom points at
// a key that the Secret doesn't have → 0 edges.
func TestResolveExternalConnections_SecretKeyMissing(t *testing.T) {
	ctx := newCtx(t)

	const graph = "key-miss-cluster"

	fake := newK8sFake()
	secret := secretNode(t, "s", map[string]string{
		"DB_URL": "s3://bucket-present/",
	})
	content := deployWithValueFromEnv(t, "URL", "secretKeyRef", "s", "ABSENT_KEY")
	d := workloadNode("Deployment", "app", content)

	fake.seed(graph, secret, d)

	require.NoError(t, resolveExternalConnections(ctx, fake, graph))
	edges := collectConnectsToEdges(fake, graph, d.Id)
	assert.Empty(t, edges, "missing key in Secret must not emit edges")
}

// --- Step 4: security invariant -----------------------------------

// TestResolveExternalConnections_NoRawValueInEvidence pins the
// security contract: when a Secret's value carries embedded credentials
// (userinfo in a DB URL), the resulting edge fields MUST NOT contain
// the raw password substring anywhere. The only on-edge surface is the
// pattern-matched URI substring, which the pattern library is
// responsible for restricting to public endpoint coordinates.
//
// If this test ever fails, it is a real security finding — the
// pattern library is capturing userinfo and the fix is to tighten the
// pattern, NOT to relax this assertion.
func TestResolveExternalConnections_NoRawValueInEvidence(t *testing.T) {
	ctx := newCtx(t)

	const graph = "secret-safety-cluster"

	fake := newK8sFake()
	// Aurora-style cluster endpoint — exercises both the hyphen-allowing
	// RDS regex AND the security invariant (userinfo stripped from
	// Matched by the regex's non-capturing shape).
	const secretPW = "SUPER_SECRET_PW_4242"
	rawValue := "postgres://admin:" + secretPW + "@db1.cluster-abcd1234.us-east-1.rds.amazonaws.com:5432/billing?sslmode=require"

	secret := secretNode(t, "db-creds", map[string]string{
		"DB_URL": rawValue,
	})
	content := deployWithValueFromEnv(t, "DB_URL", "secretKeyRef", "db-creds", "DB_URL")
	d := workloadNode("Deployment", "billing", content)

	fake.seed(graph, secret, d)

	require.NoError(t, resolveExternalConnections(ctx, fake, graph))

	edges := collectConnectsToEdges(fake, graph, d.Id)
	require.NotEmpty(t, edges, "RDS pattern must match db1.clusterxyz.us-east-1.rds.amazonaws.com")

	for i := range edges {
		e := &edges[i]
		assert.NotContains(t, e.Evidence, secretPW,
			"edge.Evidence must not contain the raw password: %q", e.Evidence)
		assert.NotContains(t, e.Method, secretPW,
			"edge.Method must not contain the raw password: %q", e.Method)
		assert.NotContains(t, e.FromId, secretPW, "edge.FromID leaked password")
		assert.NotContains(t, e.ToId, secretPW, "edge.ToID leaked password")
		// Sanity check: also ensure the userinfo substring in general
		// (everything before the RDS hostname) is absent from evidence.
		assert.NotContains(t, e.Evidence, "admin:",
			"edge.Evidence must not contain URL userinfo: %q", e.Evidence)
	}

	// Also check the created proxy node does not carry raw userinfo in
	// any of its user-visible fields — this is a defense-in-depth
	// assertion against future edits to upsertDanglingExternalProxy.
	require.Len(t, edges, 1)
	p, ok := fake.nodeByID(graph, edges[0].ToId)
	require.True(t, ok)
	for _, field := range []string{p.Id, p.SymbolName, p.Source, p.Description, p.Content, p.Summary, p.Keywords} {
		assert.NotContains(t, field, secretPW, "proxy field leaked password")
		assert.NotContains(t, field, "admin:", "proxy field leaked userinfo")
	}
}
