// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeSSLCertificate(t *testing.T) {
	assert.Equal(t, "SSL certificate c", summarizeSSLCertificate(cloud.ResourceSpec{Name: "c"}))
}
