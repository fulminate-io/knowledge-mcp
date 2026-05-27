// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeFirestoreDatabase(t *testing.T) {
	assert.Equal(t, "Firestore database db", summarizeFirestoreDatabase(cloud.ResourceSpec{Name: "db"}))
}

func TestSummarizeFirestoreBackupSchedule(t *testing.T) {
	assert.Equal(t, "Firestore backup schedule s", summarizeFirestoreBackupSchedule(cloud.ResourceSpec{Name: "s"}))
}
