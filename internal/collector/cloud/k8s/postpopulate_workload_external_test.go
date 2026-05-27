// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestPostPopulate_WorkloadExternalEdges_EndToEnd: a single cluster
// graph with a ServiceAccount (WI-annotated), a PV (cloud-backed), and
// a Deployment (env URIs) produces all three edge types in one
// postPopulate pass with no interaction between resolvers.
func TestPostPopulate_WorkloadExternalEdges_EndToEnd(t *testing.T) {
	ctx := newCtx(t)

	const (
		gkeGraph = "gke_prod-project_us-central1-a_main"
		saEmail  = "workloads@prod-project.iam.gserviceaccount.com"
	)

	fake := newK8sFake()

	// 1. ServiceAccount with GKE Workload Identity annotation.
	sa := saResNode("default", "my-sa", map[string]string{
		"gcp_service_account": saEmail,
	})

	// 2. PersistentVolume backed by a GCE PD (pre-extracted disk metadata).
	pv := pvResNode("my-pd-pv", map[string]string{
		"disk_provider": "gcp",
		"disk_handle":   "prod-pd-1",
	})

	// 3. Deployment with an env URL pointing at Cloud SQL.
	deployPayload := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []map[string]any{{
						"name": "app",
						"env": []map[string]any{
							{"name": "DB_SOCKET", "value": "host=/cloudsql/prod-project:us-central1:maindb"},
							{"name": "BUCKET", "value": "gs://prod-data/input"},
						},
					}},
				},
			},
		},
	}
	content, err := json.Marshal(deployPayload)
	require.NoError(t, err)
	deploy := workloadNode("Deployment", "app", content)

	fake.seed(gkeGraph, sa, pv, deploy)

	// Run the full postPopulate — exercises all three new resolvers in order.
	require.NoError(t, postPopulate(ctx, fake, gkeGraph))

	// --- ASSUMES_IDENTITY edge from SA --------------------------------
	assumes := collectAssumesIdentityTargets(fake, gkeGraph, sa.Id)
	require.Len(t, assumes, 1, "SA must assume exactly one identity")
	wantSAProxy := "proxy:cloud:prod-project:projects/prod-project/serviceAccounts/" + saEmail
	assert.Equal(t, wantSAProxy, assumes[0])

	// --- USES_DISK edge from PV ---------------------------------------
	usesDisk := collectUsesDiskTargets(fake, gkeGraph, pv.Id)
	require.Len(t, usesDisk, 1, "PV must use exactly one disk")
	wantDiskID := "https://www.googleapis.com/compute/v1/projects/prod-project/zones/us-central1-a/disks/prod-pd-1"
	assert.Equal(t, "proxy:cloud:prod-project:"+wantDiskID, usesDisk[0])

	// --- CONNECTS_TO edges from Deployment ----------------------------
	connects := collectConnectsToEdges(fake, gkeGraph, deploy.Id)
	require.Len(t, connects, 2, "Deployment must connect to Cloud SQL + GCS")
	// Check that both patterns are represented.
	patterns := map[string]bool{}
	for i := range connects {
		patterns[connects[i].Method] = true
	}
	assert.True(t, patterns["gcp:cloudsql"])
	assert.True(t, patterns["gcp:gcs"])

	// Idempotency: re-run the full postPopulate and ensure each edge
	// count is unchanged.
	require.NoError(t, postPopulate(ctx, fake, gkeGraph))
	assert.Len(t, collectAssumesIdentityTargets(fake, gkeGraph, sa.Id), 1)
	assert.Len(t, collectUsesDiskTargets(fake, gkeGraph, pv.Id), 1)
	assert.Len(t, collectConnectsToEdges(fake, gkeGraph, deploy.Id), 2)
}

// TestPostPopulate_WorkloadExternalEdges_SecretBacked_EndToEnd covers
// the Phase 3 addition: a Deployment with BOTH a literal env value AND
// a valueFrom.secretKeyRef pointing at a distinct cloud URI, plus an
// envFrom.configMapRef bulk-importing multiple URIs. Exercises the
// full postPopulate chain with Secret/ConfigMap resolution enabled and
// asserts deterministic dedup across literal + ref passes.
func TestPostPopulate_WorkloadExternalEdges_SecretBacked_EndToEnd(t *testing.T) {
	ctx := newCtx(t)

	const graph = "secret-e2e-cluster"

	fake := newK8sFake()

	// Secret: one URI-bearing key → one edge via valueFrom.
	secret := secretNode(t, "billing-creds", map[string]string{
		"DB_BUCKET_URL": "s3://billing-exports/",
	})
	// ConfigMap: two URI-bearing keys + one opaque → two envFrom edges.
	cm := configMapNode(t, "default", "app-config", map[string]string{
		"QUEUE_URL":  "https://sqs.us-east-1.amazonaws.com/123456789012/billing-jobs",
		"EXPORT_URI": "gs://billing-nightly/partitions",
		"REGION":     "us-east-1",
	}, nil)

	// Deployment:
	//   - literal env `LITERAL_DATA_URI` → gs://billing-nightly/partitions
	//     (SAME URI as CM EXPORT_URI — must dedup to one edge)
	//   - env.valueFrom.secretKeyRef → s3://billing-exports/
	//   - envFrom.configMapRef → QUEUE_URL + EXPORT_URI
	// Expected edges: s3 (from Secret) + gs (from literal AND CM, dedup
	// to one) + sqs (from CM) = 3 edges total.
	payload := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []map[string]any{{
						"name": "billing",
						"env": []map[string]any{
							{"name": "LITERAL_DATA_URI", "value": "gs://billing-nightly/partitions"},
							{
								"name":      "DB_BUCKET_URL",
								"valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "billing-creds", "key": "DB_BUCKET_URL"}},
							},
						},
						"envFrom": []map[string]any{
							{"configMapRef": map[string]any{"name": "app-config"}},
						},
					}},
				},
			},
		},
	}
	content, err := json.Marshal(payload)
	require.NoError(t, err)
	deploy := workloadNode("Deployment", "billing-app", content)

	fake.seed(graph, secret, cm, deploy)

	require.NoError(t, postPopulate(ctx, fake, graph))

	connects := collectConnectsToEdges(fake, graph, deploy.Id)
	require.Len(t, connects, 3, "expected 3 distinct edges after literal+CM dedup")

	// Check the distinct canonical targets and evidence shape per source.
	type edgeFacts struct {
		target  string
		evHints []string
	}
	want := map[string]edgeFacts{
		"aws:s3": {
			target:  "proxy:cloud::arn:aws:s3:::billing-exports",
			evHints: []string{"container=billing", "env=DB_BUCKET_URL", "matched=s3://billing-exports/"},
		},
		"gcp:gcs": {
			target: "proxy:cloud::gs://billing-nightly",
			// The literal-pass runs FIRST (deterministic ordering in
			// buildExternalEdgesForWorkload: pass 1 literals → pass 2
			// refs), so the surviving edge must come from the literal
			// env var.
			evHints: []string{"container=billing", "env=LITERAL_DATA_URI"},
		},
		"aws:sqs": {
			target:  "proxy:cloud:123456789012:arn:aws:sqs:us-east-1:123456789012:billing-jobs",
			evHints: []string{"envFrom=app-config", "key=QUEUE_URL"},
		},
	}
	got := map[string]*knowledgev1.Edge{}
	for i := range connects {
		got[connects[i].Method] = &connects[i]
	}
	for pattern, w := range want {
		e, ok := got[pattern]
		require.True(t, ok, "missing edge for pattern %s", pattern)
		assert.Equal(t, w.target, e.ToId, "pattern %s target", pattern)
		for _, hint := range w.evHints {
			assert.Contains(t, e.Evidence, hint,
				"pattern %s evidence missing %q in %q", pattern, hint, e.Evidence)
		}
	}

	// Idempotency: re-run and confirm the count and Methods are stable.
	require.NoError(t, postPopulate(ctx, fake, graph))
	connects2 := collectConnectsToEdges(fake, graph, deploy.Id)
	assert.Len(t, connects2, 3)
}
