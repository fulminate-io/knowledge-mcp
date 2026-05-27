// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeWebCertificate(t *testing.T) {
	assert.Equal(t, "App Service certificate c", summarizeWebCertificate(cloud.ResourceSpec{Name: "c"}))
}

func TestSummarizeAzureCA(t *testing.T) {
	assert.Equal(t, "Azure CA ca", summarizeAzureCA(cloud.ResourceSpec{Name: "ca"}))
}
