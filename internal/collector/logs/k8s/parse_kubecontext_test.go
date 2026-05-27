// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseGKEContext_HappyPath(t *testing.T) {
	proj, cluster := parseKubeContext("gke_myproject_us-central1_prod")
	assert.Equal(t, "myproject", proj)
	assert.Equal(t, "prod", cluster)
}

func TestParseGKEContext_FourPartNameTakesLast(t *testing.T) {
	// Regions with multiple underscores are rare, but cluster is always the
	// final token.
	proj, cluster := parseKubeContext("gke_myproj_us-central1_a_prod")
	assert.Equal(t, "myproj", proj)
	assert.Equal(t, "prod", cluster)
}

func TestParseGKEContext_NonGKEReturnsEmpty(t *testing.T) {
	tests := []string{
		"arn:aws:eks:us-east-1:123:cluster/prod",
		"minikube",
		"docker-desktop",
		"",
	}
	for _, tt := range tests {
		t.Run(tt, func(t *testing.T) {
			proj, cluster := parseKubeContext(tt)
			assert.Empty(t, proj)
			assert.Empty(t, cluster)
		})
	}
}

func TestParseGKEContext_TooFewTokens(t *testing.T) {
	tests := []string{"gke_", "gke_myproj", "gke_myproj_us-central1"}
	for _, tt := range tests {
		t.Run(tt, func(t *testing.T) {
			proj, cluster := parseKubeContext(tt)
			assert.Empty(t, proj)
			assert.Empty(t, cluster)
		})
	}
}
