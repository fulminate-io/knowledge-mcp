// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validK8sBackend returns a well-formed kubeconfig-auth configure call so
// positive-path tests stay concise. URL stays populated because the common
// validator still requires it — for k8s it typically matches the context
// name or a human-readable cluster identifier.
func validK8sBackend(name string) manageArgs {
	return manageArgs{
		Operation:   "configure_log_backend",
		Name:        name,
		Provider:    "k8s",
		URL:         "gke_myproject_us-central1_prod",
		AuthType:    "kubeconfig",
		KubeContext: "gke_myproject_us-central1_prod",
	}
}

// TestConfigureLogBackend_K8sKubeconfig_AcceptsEmptyCredential locks in the
// new rule: auth_type=kubeconfig means client-go resolves auth from the
// operator environment, so the graph holds no secret. The node must still
// be created and its kube_context must round-trip.
func TestConfigureLogBackend_K8sKubeconfig_AcceptsEmptyCredential(t *testing.T) {
	h, fake := newBackendHandler(t)
	ctx := context.Background()

	args := validK8sBackend("k8s-prod")
	// Explicitly zero — the whole point is that credential is optional.
	args.Credential = ""

	res := h.handleConfigureLogBackend(ctx, args)
	require.False(t, res.IsError, "k8s/kubeconfig must accept empty credential: %s", resultText(res))

	got := fake.records["k8s-prod"]
	assert.Equal(t, "k8s", got.value("provider"))
	assert.Equal(t, "kubeconfig", got.value("auth_type"))
	assert.Equal(t, "gke_myproject_us-central1_prod", got.value("kube_context"))
	assert.Empty(t, got.value("credential"), "credential must be empty for kubeconfig auth")

	// Confirmation text highlights kube_context instead of a redacted cred.
	assert.Contains(t, resultText(res), "kube_context=gke_myproject_us-central1_prod")
}

// TestConfigureLogBackend_K8s_RejectsNonKubeconfigAuth verifies the k8s
// provider is pinned to auth_type=kubeconfig. Any other auth mode has no
// wiring into client-go and would silently fail at collect time.
func TestConfigureLogBackend_K8s_RejectsNonKubeconfigAuth(t *testing.T) {
	h, _ := newBackendHandler(t)
	ctx := context.Background()

	args := validK8sBackend("bad-k8s")
	args.AuthType = "bearer"
	args.Credential = "some-token" // even with a credential, should reject
	res := h.handleConfigureLogBackend(ctx, args)
	require.True(t, res.IsError, "provider=k8s with auth_type=bearer must be rejected")
	txt := resultText(res)
	assert.Contains(t, txt, "k8s")
	assert.Contains(t, txt, "kubeconfig")
}

// TestConfigureLogBackend_Kubeconfig_RejectsBothEmpty covers the lower
// bound: with neither url nor kube_context set, validation must fail.
// The common url-required check fires first, so the error mentions url
// rather than kube_context.
func TestConfigureLogBackend_Kubeconfig_RejectsBothEmpty(t *testing.T) {
	h, fake := newBackendHandler(t)
	ctx := context.Background()

	args := validK8sBackend("nothing")
	args.URL = ""
	args.KubeContext = ""
	res := h.handleConfigureLogBackend(ctx, args)
	require.True(t, res.IsError, "empty url AND kube_context must be rejected")

	assert.Empty(t, fake.records, "invalid k8s configure must not write a record")
}

// TestConfigureLogBackend_NonK8s_StillRequiresCredential guards against
// the new code accidentally relaxing the credential requirement for
// classical backends (loki, cloudwatch, stackdriver). Only kubeconfig is
// privileged.
func TestConfigureLogBackend_NonK8s_StillRequiresCredential(t *testing.T) {
	h, _ := newBackendHandler(t)
	ctx := context.Background()

	args := validBackend("still-strict")
	args.Credential = ""
	res := h.handleConfigureLogBackend(ctx, args)
	require.True(t, res.IsError, "non-kubeconfig auth must still require credential")
	assert.Contains(t, resultText(res), "credential")
}

// TestListLogBackends_SurfacesKubeContext confirms the list response
// exposes the kube_context column so operators can tell at a glance which
// cluster each k8s backend targets.
func TestListLogBackends_SurfacesKubeContext(t *testing.T) {
	h, _ := newBackendHandler(t)
	ctx := context.Background()

	require.False(t, h.handleConfigureLogBackend(ctx, validK8sBackend("k8s-a")).IsError)
	require.False(t, h.handleConfigureLogBackend(ctx, validBackend("loki-b")).IsError)

	res := h.handleListLogBackends(ctx, "")
	require.False(t, res.IsError, "list: %s", resultText(res))
	txt := resultText(res)
	assert.Contains(t, txt, "kube_context", "table must include the kube_context column")
	assert.Contains(t, txt, "gke_myproject_us-central1_prod",
		"k8s row must expose the stored context name")

	// Loki backend has no kube_context — renders as "-" so columns stay aligned.
	lokiRow := extractTableRow(t, txt, "loki-b")
	assert.Contains(t, lokiRow, "| - |", "non-k8s rows must show '-' in kube_context cell")
}

// TestListLogBackends_JSONIncludesKubeContext verifies the structured
// response also carries kube_context so downstream tooling can filter by
// it without parsing the markdown table.
func TestListLogBackends_JSONIncludesKubeContext(t *testing.T) {
	h, _ := newBackendHandler(t)
	ctx := context.Background()

	require.False(t, h.handleConfigureLogBackend(ctx, validK8sBackend("k8s-j")).IsError)

	res := h.handleListLogBackends(ctx, "json")
	require.False(t, res.IsError, "json list: %s", resultText(res))
	txt := resultText(res)
	assert.Contains(t, txt, `"kube_context"`)
	assert.Contains(t, txt, "gke_myproject_us-central1_prod")
}

// TestConfigureLogBackend_DeterministicID_PreventsDuplicates verifies that
// two configure calls with the same name produce ONE record, not two. The
// canonical-id write path (id == name) is what enforces this.
func TestConfigureLogBackend_DeterministicID_PreventsDuplicates(t *testing.T) {
	h, fake := newBackendHandler(t)
	ctx := context.Background()

	res1 := h.handleConfigureLogBackend(ctx, validK8sBackend("dup-test"))
	require.False(t, res1.IsError, "first configure must succeed: %s", resultText(res1))

	res2 := h.handleConfigureLogBackend(ctx, validK8sBackend("dup-test"))
	require.False(t, res2.IsError, "second configure must succeed (idempotent update): %s", resultText(res2))

	require.Len(t, fake.records, 1, "two configures with same name must produce exactly one record")

	got := fake.records["dup-test"]
	assert.Equal(t, "dup-test", got.id, "record id must equal name")
}

// TestConfigureLogBackend_KubeContextImmutable: rebinding an existing k8s
// backend to a different cluster is the original footgun the deterministic
// ID work was meant to address. Refuse the rebind explicitly so the
// operator must register a new name.
func TestConfigureLogBackend_KubeContextImmutable(t *testing.T) {
	h, fake := newBackendHandler(t)
	ctx := context.Background()

	args := validK8sBackend("rebind-target")
	args.KubeContext = "gke_proj_us-central1_prod"
	require.False(t, h.handleConfigureLogBackend(ctx, args).IsError)

	// Attempt to re-target the same name at a different cluster.
	args.KubeContext = "gke_proj_us-east1_dev"
	res := h.handleConfigureLogBackend(ctx, args)
	require.True(t, res.IsError, "rebinding kube_context on existing k8s backend must fail")
	txt := resultText(res)
	assert.Contains(t, txt, "kube_context")
	assert.Contains(t, txt, "immutable")

	// Confirm the stored value is unchanged.
	got := fake.records["rebind-target"]
	assert.Equal(t, "gke_proj_us-central1_prod", got.value("kube_context"),
		"failed configure must not mutate the stored value")
}

// TestConfigureLogBackend_KubeContextSameValue_AllowsUpdate: an operator
// re-running an identical configure (e.g. rotating credential, adjusting
// auth_type) must still succeed when kube_context is unchanged.
func TestConfigureLogBackend_KubeContextSameValue_AllowsUpdate(t *testing.T) {
	h, _ := newBackendHandler(t)
	ctx := context.Background()

	require.False(t, h.handleConfigureLogBackend(ctx, validK8sBackend("idempotent")).IsError)
	// Re-run with same kube_context — should be a no-op-style update.
	require.False(t, h.handleConfigureLogBackend(ctx, validK8sBackend("idempotent")).IsError)
}

// extractTableRow pulls the single markdown row beginning with "| <name> |"
// from a formatted backends table so per-row assertions stay targeted
// instead of scanning the whole table.
func extractTableRow(t *testing.T, table, name string) string {
	t.Helper()
	prefix := "| " + name + " "
	for line := range strings.SplitSeq(table, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	t.Fatalf("row for %q not found in table:\n%s", name, table)
	return ""
}
