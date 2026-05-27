// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// runS3CollectorEdges returns the edges for a single-bucket fake.
func runS3CollectorEdges(t *testing.T, fake *fakeS3API) []cloud.EdgeSpec {
	t.Helper()
	c := &s3Collector{client: fake, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)
	return result.Edges
}

func TestS3Collector_ReplicationEdges(t *testing.T) {
	name := "primary-bucket"
	fake := &fakeS3API{
		buckets: []s3types.Bucket{{Name: awssdk.String(name)}},
		replication: map[string]*s3types.ReplicationConfiguration{
			name: {
				Rules: []s3types.ReplicationRule{
					{
						ID:     awssdk.String("rule-1"),
						Status: s3types.ReplicationRuleStatusEnabled,
						Destination: &s3types.Destination{
							Bucket: awssdk.String("arn:aws:s3:::dr-bucket-eu"),
						},
					},
					{
						ID:     awssdk.String("rule-2"),
						Status: s3types.ReplicationRuleStatusDisabled,
						Destination: &s3types.Destination{
							Bucket: awssdk.String("arn:aws:s3:::archive-bucket"),
						},
					},
				},
			},
		},
	}
	edges := runS3CollectorEdges(t, fake)

	var replEdges []cloud.EdgeSpec
	for _, e := range edges {
		if e.Relationship == kgtypes.EdgeReplicatesTo {
			replEdges = append(replEdges, e)
		}
	}
	require.Len(t, replEdges, 2, "one EdgeReplicatesTo per replication rule")
	assert.Equal(t, "arn:aws:s3:::primary-bucket", replEdges[0].SourceID)
	assert.Equal(t, "arn:aws:s3:::dr-bucket-eu", replEdges[0].TargetID)
	assert.Equal(t, "rule-1", replEdges[0].Metadata["rule_id"])
	assert.Equal(t, "Enabled", replEdges[0].Metadata["rule_status"])
	assert.Equal(t, "Disabled", replEdges[1].Metadata["rule_status"])
}

func TestS3Collector_NoReplicationConfigured(t *testing.T) {
	name := "no-repl"
	fake := &fakeS3API{
		buckets: []s3types.Bucket{{Name: awssdk.String(name)}},
		// No replication map entry → fake returns ReplicationConfigurationNotFoundError.
	}
	edges := runS3CollectorEdges(t, fake)
	for _, e := range edges {
		assert.NotEqual(t, kgtypes.EdgeReplicatesTo, e.Relationship)
	}
}
