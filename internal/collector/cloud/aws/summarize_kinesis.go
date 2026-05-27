// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("kinesis-stream", summarizeKinesisStream)
}

func summarizeKinesisStream(spec cloud.ResourceSpec) string {
	parts := []string{"Kinesis stream", spec.Name}
	if s := spec.Metadata["status"]; s != "" {
		parts = append(parts, fmt.Sprintf("status=%s", s))
	}
	if r := spec.Metadata["retention_hours"]; r != "" {
		parts = append(parts, fmt.Sprintf("retention=%sh", r))
	}
	if e := spec.Metadata["encryption_type"]; e != "" && e != "NONE" {
		parts = append(parts, fmt.Sprintf("encryption=%s", e))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
