// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:firestore:database", summarizeFirestoreDatabase)
	cloud.Register("gcp:firestore:backupSchedule", summarizeFirestoreBackupSchedule)
}

func summarizeFirestoreDatabase(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Firestore database", spec)
}

func summarizeFirestoreBackupSchedule(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Firestore backup schedule", spec)
}
