// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeForwardingRule(t *testing.T) {
	assert.Equal(t, "forwarding rule fr", summarizeForwardingRule(cloud.ResourceSpec{Name: "fr"}))
}

func TestSummarizeTargetHTTPProxy(t *testing.T) {
	assert.Equal(t, "target HTTP proxy hp", summarizeTargetHTTPProxy(cloud.ResourceSpec{Name: "hp"}))
}

func TestSummarizeTargetHTTPSProxy(t *testing.T) {
	assert.Equal(t, "target HTTPS proxy hsp", summarizeTargetHTTPSProxy(cloud.ResourceSpec{Name: "hsp"}))
}

func TestSummarizeURLMap(t *testing.T) {
	assert.Equal(t, "URL map um", summarizeURLMap(cloud.ResourceSpec{Name: "um"}))
}

func TestSummarizeBackendService(t *testing.T) {
	assert.Equal(t, "backend service bs", summarizeBackendService(cloud.ResourceSpec{Name: "bs"}))
}
