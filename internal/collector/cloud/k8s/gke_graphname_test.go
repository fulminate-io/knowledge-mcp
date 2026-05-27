// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestParseGKEGraphName drives the parser against every boundary case the
// user decisions surface: happy paths, non-GKE prefixes, empty / malformed
// inputs, project ids with hyphens, zone-qualified regions, and cluster
// names containing underscores (which naive Split on "_" would mis-parse).
func TestParseGKEGraphName(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		wantProject string
		wantRegion  string
		wantCluster string
		wantOK      bool
	}{
		{
			name:        "happy path — simple project, region, cluster",
			input:       "gke_fulminate-services_us-central1_main-us-central1",
			wantProject: "fulminate-services",
			wantRegion:  "us-central1",
			wantCluster: "main-us-central1",
			wantOK:      true,
		},
		{
			name:        "project with multiple hyphens",
			input:       "gke_my-prod-project_europe-west4_prod-cluster",
			wantProject: "my-prod-project",
			wantRegion:  "europe-west4",
			wantCluster: "prod-cluster",
			wantOK:      true,
		},
		{
			name:        "zone-qualified location (us-central1-a)",
			input:       "gke_proj_us-central1-a_zonal",
			wantProject: "proj",
			wantRegion:  "us-central1-a",
			wantCluster: "zonal",
			wantOK:      true,
		},
		{
			name:        "cluster name with underscore",
			input:       "gke_acme_us-central1_main_cluster",
			wantProject: "acme",
			wantRegion:  "us-central1",
			wantCluster: "main_cluster",
			wantOK:      true,
		},
		{
			name:        "cluster name with multiple underscores",
			input:       "gke_acme_asia-southeast1_tenant_a_cluster",
			wantProject: "acme",
			wantRegion:  "asia-southeast1",
			wantCluster: "tenant_a_cluster",
			wantOK:      true,
		},
		{
			name:   "non-GKE prefix (bare EKS context)",
			input:  "eks_acme_prod-cluster",
			wantOK: false,
		},
		{
			name:   "non-GKE prefix (AKS)",
			input:  "aks_prod_main",
			wantOK: false,
		},
		{
			name:   "empty string",
			input:  "",
			wantOK: false,
		},
		{
			name:   "gke_ prefix only",
			input:  "gke_",
			wantOK: false,
		},
		{
			name:   "missing region token (no region-shaped substring)",
			input:  "gke_proj_main",
			wantOK: false,
		},
		{
			name:   "missing region token between tokens",
			input:  "gke_proj_mainonly_cluster",
			wantOK: false,
		},
		{
			name:   "region-shaped token but no cluster segment after",
			input:  "gke_proj_us-central1",
			wantOK: false,
		},
		{
			name:   "region-shaped token but no project segment before",
			input:  "gke__us-central1_main",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			project, region, cluster, ok := parseGKEGraphName(tc.input)
			assert.Equal(t, tc.wantOK, ok, "ok mismatch")
			if tc.wantOK {
				assert.Equal(t, tc.wantProject, project, "project mismatch")
				assert.Equal(t, tc.wantRegion, region, "region mismatch")
				assert.Equal(t, tc.wantCluster, cluster, "cluster mismatch")
			} else {
				assert.Empty(t, project, "project should be empty when ok=false")
				assert.Empty(t, region, "region should be empty when ok=false")
				assert.Empty(t, cluster, "cluster should be empty when ok=false")
			}
		})
	}
}

// TestGKEClusterSelfLink pins the exact URL format the GCP collector
// stores as the Cluster node ID. If this format ever drifts, every proxy
// created by resolveClusterLinkage will dangle — this test is the one
// place that anchors the convention.
func TestGKEClusterSelfLink(t *testing.T) {
	cases := []struct {
		name     string
		project  string
		location string
		cluster  string
		want     string
	}{
		{
			name:     "regional cluster",
			project:  "fulminate-services",
			location: "us-central1",
			cluster:  "main-us-central1",
			want:     "https://container.googleapis.com/v1/projects/fulminate-services/locations/us-central1/clusters/main-us-central1",
		},
		{
			name:     "zonal cluster",
			project:  "proj",
			location: "us-central1-a",
			cluster:  "zonal",
			want:     "https://container.googleapis.com/v1/projects/proj/locations/us-central1-a/clusters/zonal",
		},
		{
			name:     "european region",
			project:  "acme",
			location: "europe-west4",
			cluster:  "prod",
			want:     "https://container.googleapis.com/v1/projects/acme/locations/europe-west4/clusters/prod",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := gkeClusterSelfLink(tc.project, tc.location, tc.cluster)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestParseGKEGraphName_RoundTrip composes parse(selfLink-reconstruction)
// end-to-end for a couple of real-world shapes, ensuring the parser and
// the selfLink builder agree on what constitutes the triple.
func TestParseGKEGraphName_RoundTrip(t *testing.T) {
	cases := []struct {
		project  string
		location string
		cluster  string
	}{
		{"fulminate-services", "us-central1", "main-us-central1"},
		{"my-prod-project", "europe-west4", "prod"},
		{"acme", "us-central1-a", "zonal"},
	}
	for _, tc := range cases {
		name := "gke_" + tc.project + "_" + tc.location + "_" + tc.cluster
		t.Run(name, func(t *testing.T) {
			p, r, c, ok := parseGKEGraphName(name)
			if !ok {
				t.Fatalf("parseGKEGraphName(%q) returned ok=false; want true", name)
			}
			assert.Equal(t, tc.project, p)
			assert.Equal(t, tc.location, r)
			assert.Equal(t, tc.cluster, c)

			// Round-trip the selfLink builder as well for good measure.
			want := "https://container.googleapis.com/v1/projects/" + tc.project +
				"/locations/" + tc.location + "/clusters/" + tc.cluster
			assert.Equal(t, want, gkeClusterSelfLink(p, r, c))
		})
	}
}
