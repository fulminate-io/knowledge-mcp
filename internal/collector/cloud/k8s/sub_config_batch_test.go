// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestConfigMapsSubCollector(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app-config",
			Namespace: "default",
		},
		Data: map[string]string{
			"config.yaml": "key: value",
			"env":         "FOO=bar",
		},
		BinaryData: map[string][]byte{
			"cert.pem": []byte("binary-data"),
		},
	})

	sub := &configMapsSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	res := result.Resources[0]
	assert.Equal(t, "default/ConfigMap/app-config", res.ID)
	assert.Equal(t, "3", res.Metadata["data_key_count"])

	// Content shape: {keys, data, binary_data_keys}.
	var content struct {
		Keys           []string          `json:"keys"`
		Data           map[string]string `json:"data"`
		BinaryDataKeys []string          `json:"binary_data_keys"`
	}
	require.NoError(t, json.Unmarshal(res.Content, &content))

	// All keys (Data + BinaryData) appear in the combined key list.
	assert.ElementsMatch(t, []string{"cert.pem", "config.yaml", "env"}, content.Keys)

	// Data values are included so the CONNECTS_TO resolver can scan them.
	assert.Equal(t, map[string]string{
		"config.yaml": "key: value",
		"env":         "FOO=bar",
	}, content.Data)

	// BinaryData keys are listed, but BinaryData values are NOT stored.
	assert.ElementsMatch(t, []string{"cert.pem"}, content.BinaryDataKeys)
	assert.NotContains(t, string(res.Content), "binary-data")

	// Metadata must not carry values (data values belong in Content only).
	for _, v := range res.Metadata {
		assert.NotContains(t, v, "key: value", "metadata must not contain data values")
		assert.NotContains(t, v, "FOO=bar", "metadata must not contain data values")
	}
}

func TestSecretsSubCollector(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "db-creds",
			Namespace: "default",
			Labels: map[string]string{
				"app": "billing",
			},
			Annotations: map[string]string{
				"external-secrets.io/backend": "aws-secretsmanager",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"username":  []byte("admin"),
			"password":  []byte("super-secret-password-123"),
			"bucket_rl": []byte("s3://prod-billing-archive/exports"),
		},
	})

	sub := &secretsSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	res := result.Resources[0]
	assert.Equal(t, "default/Secret/db-creds", res.ID)
	assert.Equal(t, "Opaque", res.Metadata["type"])
	assert.Equal(t, "3", res.Metadata["data_key_count"])
	assert.Equal(t, "default", res.Metadata["namespace"])

	// Labels and annotations are preserved in metadata.
	assert.Equal(t, "billing", res.Metadata["label/app"])
	assert.Equal(t, "aws-secretsmanager", res.Metadata["annotation/external-secrets.io/backend"])

	// Content shape: {type, keys, data} with decoded string values so the
	// CONNECTS_TO resolver can scan for cloud URIs.
	var content struct {
		Type string            `json:"type"`
		Keys []string          `json:"keys"`
		Data map[string]string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(res.Content, &content))
	assert.Equal(t, "Opaque", content.Type)
	assert.ElementsMatch(t, []string{"bucket_rl", "password", "username"}, content.Keys)
	assert.Equal(t, map[string]string{
		"username":  "admin",
		"password":  "super-secret-password-123",
		"bucket_rl": "s3://prod-billing-archive/exports",
	}, content.Data)

	// CRITICAL: secret data values must NEVER appear in Metadata. Content is
	// allowed to contain them (it is encrypted at rest, excluded from BM25 for
	// GraphCloud, and gated off the summarizer/embedder in Phase 2) but any
	// leak into Metadata would bypass those protections.
	for k, v := range res.Metadata {
		assert.NotContains(t, v, "admin",
			"metadata key %q must not contain secret values", k)
		assert.NotContains(t, v, "super-secret",
			"metadata key %q must not contain secret values", k)
		assert.NotContains(t, v, "s3://prod-billing-archive",
			"metadata key %q must not contain secret values", k)
	}

	// Only expected metadata key families are present: namespace, type,
	// data_key_count, label/*, annotation/*. No raw data-key names, no
	// data values.
	for k := range res.Metadata {
		switch k {
		case "namespace", "type", "data_key_count":
			continue
		}
		if strings.HasPrefix(k, "label/") || strings.HasPrefix(k, "annotation/") {
			continue
		}
		t.Errorf("unexpected metadata key %q — metadata must only contain "+
			"namespace, type, data_key_count, label/*, annotation/*", k)
	}
}

// TestSecretsSubCollector_BinaryValues ensures non-UTF8 secret values are
// handled without error and do not produce spurious URI matches. Note that
// json.Marshal replaces invalid UTF-8 bytes with the Unicode replacement
// character (U+FFFD) — so raw byte identity is NOT preserved across the
// JSON round-trip. This is acceptable: the resolver's URI regex won't match
// replacement chars either, so true binary payloads (certs, keystores, etc.)
// scan to no matches, which is the desired behavior.
func TestSecretsSubCollector_BinaryValues(t *testing.T) {
	binary := []byte{0xff, 0xfe, 0x00, 0x01, 0x02}
	cs := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "tls", Namespace: "default"},
		Type:       corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.key": binary,
		},
	})

	sub := &secretsSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	var content struct {
		Keys []string          `json:"keys"`
		Data map[string]string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(result.Resources[0].Content, &content))

	// Key is present and the data map carries an entry for it. The exact
	// byte-level value is not asserted (JSON replacement-char behavior), but
	// no URI-shaped substrings leak out of a purely binary payload.
	assert.ElementsMatch(t, []string{"tls.key"}, content.Keys)
	assert.Contains(t, content.Data, "tls.key")
	assert.NotContains(t, content.Data["tls.key"], "://")
	assert.NotContains(t, content.Data["tls.key"], "http")
}

func TestJobsSubCollector(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "migrate-db",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{
					{Kind: "CronJob", Name: "nightly-migrate"},
				},
			},
			Spec: batchv1.JobSpec{
				Completions: int32Ptr(1),
				Parallelism: int32Ptr(1),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						ServiceAccountName: "migrate-sa",
						Containers: []corev1.Container{
							{Name: "migrate", Image: "myapp/migrate:latest"},
						},
					},
				},
			},
			Status: batchv1.JobStatus{
				Succeeded: 1,
			},
		},
		&batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "nightly-migrate",
				Namespace: "default",
			},
			Spec: batchv1.CronJobSpec{
				Schedule: "0 2 * * *",
				JobTemplate: batchv1.JobTemplateSpec{
					Spec: batchv1.JobSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								ServiceAccountName: "migrate-sa",
								Containers: []corev1.Container{
									{Name: "migrate", Image: "myapp/migrate:latest"},
								},
							},
						},
					},
				},
			},
		},
	)

	sub := &jobsSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 2)

	// Job.
	jobRes := result.Resources[0]
	assert.Equal(t, "default/Job/migrate-db", jobRes.ID)
	assert.Equal(t, "1", jobRes.Metadata["completions"])
	assert.Equal(t, "1", jobRes.Metadata["succeeded"])

	// CronJob.
	cronRes := result.Resources[1]
	assert.Equal(t, "default/CronJob/nightly-migrate", cronRes.ID)
	assert.Equal(t, "0 2 * * *", cronRes.Metadata["schedule"])

	// Job uses extractPodTemplateEdges for SA edge.
	var saEdges []string
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeUsesSA {
			saEdges = append(saEdges, e.TargetID)
		}
	}
	// Both Job and CronJob reference migrate-sa.
	count := 0
	for _, s := range saEdges {
		if strings.Contains(s, "migrate-sa") {
			count++
		}
	}
	assert.Equal(t, 2, count, "both Job and CronJob should have SA edge")

	// Job has OwnerReference edge.
	var ownerEdges []string
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeOwnedBy {
			ownerEdges = append(ownerEdges, e.TargetID)
		}
	}
	assert.Contains(t, ownerEdges, "default/CronJob/nightly-migrate")
}
