// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeCDNProfile(t *testing.T) {
	assert.Equal(t, "CDN/Front Door profile p", summarizeCDNProfile(cloud.ResourceSpec{Name: "p"}))
}

func TestSummarizeAFDEndpoint(t *testing.T) {
	assert.Equal(t, "Front Door endpoint e", summarizeAFDEndpoint(cloud.ResourceSpec{Name: "e"}))
}
