// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type ebsCollector struct {
	client    *ec2.Client
	region    string
	accountID string
}

// newEBSCollector creates an EBS subcollector. EBS uses the EC2 API (DescribeVolumes).
func newEBSCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &ebsCollector{
		client:    ec2.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *ebsCollector) Name() string { return "ebs" }

func (c *ebsCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	paginator := ec2.NewDescribeVolumesPaginator(c.client, &ec2.DescribeVolumesInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("ebs: describe volumes: %w", err)
		}

		for _, volume := range page.Volumes {
			content, err := json.Marshal(volume)
			if err != nil {
				return cloud.SubCollectorResult{}, fmt.Errorf("ebs: marshal: %w", err)
			}

			volumeID := awssdk.ToString(volume.VolumeId)
			volumeARN := ec2ARN(c.region, c.accountID, "volume", volumeID)

			resources = append(resources, cloud.ResourceSpec{
				ID:           volumeARN,
				Name:         nameTag(volume.Tags, volumeID),
				ResourceType: "ebs-volume",
				Region:       c.region,
				Content:      content,
				Metadata:     ebsVolumeMetadata(volume),
			})

			// EBS → KMS key (server-side encryption)
			if kmsKeyID := awssdk.ToString(volume.KmsKeyId); kmsKeyID != "" {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     volumeARN,
					TargetID:     kmsKeyID,
					Relationship: kgtypes.EdgeEncryptsWith,
					Metadata:     map[string]string{"encryption_scope": "volume"},
				})
			}

			// EBS → EC2 instance (volume attachment)
			for _, attachment := range volume.Attachments {
				if attachment.InstanceId != nil {
					instanceARN := ec2ARN(c.region, c.accountID, "instance", awssdk.ToString(attachment.InstanceId))
					edges = append(edges, cloud.EdgeSpec{
						SourceID:     volumeARN,
						TargetID:     instanceARN,
						Relationship: kgtypes.EdgeBoundTo,
					})
				}
			}
		}
	}

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
	}, nil
}

// ebsVolumeMetadata extracts discriminating fields from an EBS volume.
func ebsVolumeMetadata(v ec2types.Volume) map[string]string {
	m := make(map[string]string, 5)
	if vt := string(v.VolumeType); vt != "" {
		m["volume_type"] = vt
	}
	if s := string(v.State); s != "" {
		m["state"] = s
	}
	if v.Size != nil {
		m["size_gib"] = fmt.Sprintf("%d", awssdk.ToInt32(v.Size))
	}
	if v.Encrypted != nil {
		m["encrypted"] = fmt.Sprintf("%t", awssdk.ToBool(v.Encrypted))
	}
	if az := awssdk.ToString(v.AvailabilityZone); az != "" {
		m["availability_zone"] = az
	}
	return m
}
