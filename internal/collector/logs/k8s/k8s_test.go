// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

func TestProviderRegistered(t *testing.T) {
	// The init() in k8s.go self-registers under name "k8s"; importing this
	// test file pulls in the package, so New("k8s") must succeed.
	p, err := logwire.New("k8s")
	require.NoError(t, err)
	require.NotNil(t, p)
	_, ok := p.(*k8sProvider)
	assert.True(t, ok, "registered factory returned %T, want *k8sProvider", p)
}

func TestConfigure_KubeContextPrimary(t *testing.T) {
	p := &k8sProvider{}
	require.NoError(t, p.Configure(map[string]string{"kube_context": "foo"}))
	assert.Equal(t, "foo", p.kubeContext)
}

func TestConfigure_URLFallback(t *testing.T) {
	p := &k8sProvider{}
	require.NoError(t, p.Configure(map[string]string{"url": "foo"}))
	assert.Equal(t, "foo", p.kubeContext)
}

func TestConfigure_KubeContextWinsOverURL(t *testing.T) {
	p := &k8sProvider{}
	require.NoError(t, p.Configure(map[string]string{
		"kube_context": "primary",
		"url":          "fallback",
	}))
	assert.Equal(t, "primary", p.kubeContext)
}

func TestConfigure_EmptyPermitted(t *testing.T) {
	p := &k8sProvider{}
	require.NoError(t, p.Configure(map[string]string{}))
	assert.Empty(t, p.kubeContext)
}

func TestConfigure_ResetsCachedClientset(t *testing.T) {
	p := &k8sProvider{}
	// Pre-seed a cached client to prove Configure clears it.
	p.setClientset(fake.NewSimpleClientset())
	require.NotNil(t, p.clientset)

	require.NoError(t, p.Configure(map[string]string{"kube_context": "other"}))
	assert.Nil(t, p.clientset,
		"Configure should reset cached clientset so next call rebuilds under new context")
}

func TestContextForLogging_Default(t *testing.T) {
	p := &k8sProvider{}
	assert.Equal(t, "<default>", p.contextForLogging(context.TODO()))
}

func TestContextForLogging_Named(t *testing.T) {
	p := &k8sProvider{kubeContext: "prod"}
	assert.Equal(t, "prod", p.contextForLogging(context.TODO()))
}
