// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeKMSKeyRing(t *testing.T) {
	assert.Equal(t, "Cloud KMS key ring kr", summarizeKMSKeyRing(cloud.ResourceSpec{Name: "kr"}))
}

func TestSummarizeKMSCryptoKey(t *testing.T) {
	assert.Equal(t, "Cloud KMS crypto key ck", summarizeKMSCryptoKey(cloud.ResourceSpec{Name: "ck"}))
}
