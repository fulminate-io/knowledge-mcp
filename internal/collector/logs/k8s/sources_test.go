// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

func TestListSources_ReturnsKubeContextAsSource(t *testing.T) {
	p := &k8sProvider{kubeContext: "gke_proj_us-central1_prod"}
	srcs, err := p.ListSources(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, srcs, 1)
	assert.Equal(t, "gke_proj_us-central1_prod", srcs[0].Name)
	assert.Equal(t, "k8s", srcs[0].Provider)
	assert.Contains(t, srcs[0].Description, "events.k8s.io/v1")
}

func TestListSources_EmptyContextBecomesDefault(t *testing.T) {
	p := &k8sProvider{}
	srcs, err := p.ListSources(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, srcs, 1)
	assert.Equal(t, "default", srcs[0].Name)
}

func TestListSources_PrefixFilter(t *testing.T) {
	p := &k8sProvider{kubeContext: "gke_proj_us-central1_prod"}

	// Matching prefix (case-insensitive).
	srcs, err := p.ListSources(context.Background(), "gke_")
	require.NoError(t, err)
	assert.Len(t, srcs, 1)

	srcs, err = p.ListSources(context.Background(), "GKE_")
	require.NoError(t, err)
	assert.Len(t, srcs, 1, "prefix match should be case-insensitive")

	// Non-matching prefix.
	srcs, err = p.ListSources(context.Background(), "aws")
	require.NoError(t, err)
	assert.Empty(t, srcs)

	// Prefix longer than name.
	srcs, err = p.ListSources(context.Background(),
		"gke_proj_us-central1_prod_extra")
	require.NoError(t, err)
	assert.Empty(t, srcs)
}

func TestEnsureClientset_UsesPresetFake(t *testing.T) {
	cs := fake.NewSimpleClientset()
	p := &k8sProvider{}
	p.setClientset(cs)

	got, err := p.ensureClientset()
	require.NoError(t, err)
	assert.Equal(t, cs, got,
		"preset clientset must be returned unchanged, not rebuilt from kubeconfig")
}

// TestCollect_EventWithOnlyDeprecatedLastTimestamp exercises the timestamp
// fallback chain end-to-end through Collect() — EventTime is zero, so
// normalizeEvent must fall back to DeprecatedLastTimestamp.
func TestCollect_EventWithOnlyDeprecatedLastTimestamp(t *testing.T) {
	last := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	e := &eventsv1.Event{
		ObjectMeta:              metav1.ObjectMeta{Name: "evt", Namespace: "prod"},
		Reason:                  "Started",
		Note:                    "",
		Type:                    "Normal",
		DeprecatedLastTimestamp: metav1.Time{Time: last},
	}
	cs := fake.NewSimpleClientset(e)
	p := &k8sProvider{}
	p.setClientset(cs)

	got := collectAll(t, p, logwire.Query{})
	require.Len(t, got, 1)
	assert.True(t, got[0].Timestamp.Equal(last))
}
