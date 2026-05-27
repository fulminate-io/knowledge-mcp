// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeAlertPolicy(t *testing.T) {
	assert.Equal(t, "monitoring alert policy ap", summarizeAlertPolicy(cloud.ResourceSpec{Name: "ap"}))
}

func TestSummarizeNotificationChannel(t *testing.T) {
	assert.Equal(t, "notification channel nc", summarizeNotificationChannel(cloud.ResourceSpec{Name: "nc"}))
}
