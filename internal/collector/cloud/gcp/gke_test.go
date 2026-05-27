// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGKESubCollector_Name(t *testing.T) {
	c := &gkeSubCollector{}
	assert.Equal(t, "gcp-gke", c.Name())
}

func TestGKEKubecontext(t *testing.T) {
	tests := []struct {
		project  string
		location string
		cluster  string
		want     string
	}{
		{"my-project", "us-central1-a", "my-cluster", "gke_my-project_us-central1-a_my-cluster"},
		{"proj", "europe-west1", "prod", "gke_proj_europe-west1_prod"},
	}
	for _, tt := range tests {
		got := gkeKubecontext(tt.project, tt.location, tt.cluster)
		assert.Equal(t, tt.want, got)
	}
}

func TestExtractRegionFromLocation(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"us-central1-a", "us-central1"},
		{"us-central1", "us-central1"},
		{"europe-west1-b", "europe-west1"},
		{"europe-west1", "europe-west1"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, extractRegionFromLocation(tt.input))
	}
}
