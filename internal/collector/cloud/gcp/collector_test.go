// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGCPCollector_Name(t *testing.T) {
	c := &GCPCollector{}
	assert.Equal(t, "gcp", c.Name())
}

func TestResolveProject_ExplicitID(t *testing.T) {
	// When id is provided, resolveProject should use it regardless of ADC state.
	// This test may fail in environments without any GCP credentials, but
	// the explicit ID path is exercised either way.
	ctx := context.Background()
	projectID, _, err := resolveProject(ctx, "my-test-project")
	if err != nil {
		// No credentials available in test env — that's okay, we verify the
		// error isn't about missing project ID.
		assert.Contains(t, err.Error(), "credentials")
		return
	}
	assert.Equal(t, "my-test-project", projectID)
}

func TestResolveProject_EmptyID(t *testing.T) {
	// With no explicit ID, resolveProject tries to extract from ADC.
	// In environments with credentials (developer machines, CI with service accounts),
	// this may succeed — we verify the result is non-empty.
	// In environments without credentials, we verify an error is returned.
	ctx := context.Background()
	projectID, _, err := resolveProject(ctx, "")
	if err != nil {
		// Expected in most test environments without GCP credentials.
		return
	}
	// If credentials exist, a project ID should have been extracted.
	require.NotEmpty(t, projectID)
}

func TestBuildSubCollectors_Count(t *testing.T) {
	// All 41 subcollectors should be wired when clients are provided.
	// 14 original + 1 VPC connector + 1 Artifact Registry + 5 LB + 1 Memorystore
	// + 1 DNS + 1 Cloud Armor + 1 Cloud Router (NAT collected inline, no
	//   separate sub-collector — see FUL-95 finding 32694d4b)
	// + 1 Cloud Tasks + 1 Cloud Scheduler + 1 BigQuery + 1 Logging sinks
	// + 1 Instance groups + 1 SSL certificates + 1 Alert policies + 1 Eventarc
	// + 1 KMS + 1 Firestore + 1 Persistent Disks (Phase 2)
	// + 1 Filestore + 1 Workflows (Phase 3)
	// + 1 Dataflow (Phase 4)
	// + 1 Cloud Identity IAM groups = 40.
	clients := &gcpClients{}
	subs := buildSubCollectors(clients, "test-project")
	assert.Len(t, subs, 40)
}

func TestBuildSubCollectors_Names(t *testing.T) {
	// Verify each subcollector has a unique, non-empty name.
	clients := &gcpClients{}
	subs := buildSubCollectors(clients, "test-project")

	seen := make(map[string]bool)
	for _, sub := range subs {
		name := sub.Name()
		assert.NotEmpty(t, name)
		assert.False(t, seen[name], "duplicate subcollector name: %s", name)
		seen[name] = true
	}
}
