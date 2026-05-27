// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	iampb "cloud.google.com/go/iam/apiv1/iampb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	firestore "google.golang.org/api/firestore/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestFirestoreSubCollector_Name(t *testing.T) {
	c := &firestoreSubCollector{}
	assert.Equal(t, "gcp-firestore", c.Name())
}

func TestFirestoreDatabaseMetadata(t *testing.T) {
	db := &firestore.GoogleFirestoreAdminV1Database{
		Type:                          "FIRESTORE_NATIVE",
		LocationId:                    "us-central1",
		ConcurrencyMode:               "OPTIMISTIC",
		DeleteProtectionState:         "DELETE_PROTECTION_ENABLED",
		DatabaseEdition:               "STANDARD",
		PointInTimeRecoveryEnablement: "POINT_IN_TIME_RECOVERY_ENABLED",
	}
	meta := firestoreDatabaseMetadata(db)
	assert.Equal(t, "FIRESTORE_NATIVE", meta["type"])
	assert.Equal(t, "us-central1", meta["locationId"])
	assert.Equal(t, "OPTIMISTIC", meta["concurrencyMode"])
	assert.Equal(t, "DELETE_PROTECTION_ENABLED", meta["deleteProtectionState"])
	assert.Equal(t, "STANDARD", meta["databaseEdition"])
	assert.Equal(t, "POINT_IN_TIME_RECOVERY_ENABLED", meta["pointInTimeRecovery"])
}

func TestFirestoreDatabaseEdges_CMEK(t *testing.T) {
	kmsKey := "projects/p/locations/us-central1/keyRings/r/cryptoKeys/k"
	db := &firestore.GoogleFirestoreAdminV1Database{
		Name: "projects/p/databases/(default)",
		CmekConfig: &firestore.GoogleFirestoreAdminV1CmekConfig{
			KmsKeyName: kmsKey,
		},
	}
	edges := firestoreDatabaseEdges(db)
	assert.Len(t, edges, 1)
	assert.Equal(t, db.Name, edges[0].SourceID)
	assert.Equal(t, kmsKey, edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeEncryptsWith, edges[0].Relationship)
	assert.Equal(t, "database", edges[0].Metadata["encryption_scope"])
}

func TestFirestoreDatabaseEdges_NoCMEK(t *testing.T) {
	db := &firestore.GoogleFirestoreAdminV1Database{
		Name: "projects/p/databases/(default)",
	}
	assert.Nil(t, firestoreDatabaseEdges(db))

	db = &firestore.GoogleFirestoreAdminV1Database{
		Name:       "projects/p/databases/(default)",
		CmekConfig: &firestore.GoogleFirestoreAdminV1CmekConfig{},
	}
	assert.Nil(t, firestoreDatabaseEdges(db))
}

// --- EdgeGrants (IAM) ---

func TestFirestoreIAMGrantsEdges(t *testing.T) {
	dbName := "projects/p/databases/(default)"
	policy := &iampb.Policy{
		Bindings: []*iampb.Binding{
			{
				Role:    "roles/datastore.user",
				Members: []string{"serviceAccount:sa@proj.iam.gserviceaccount.com"},
			},
			{
				Role:    "roles/datastore.viewer",
				Members: []string{"user:alice@example.com", "user:bob@example.com"},
			},
		},
	}
	edges := firestoreIAMGrantsEdges(dbName, policy)
	require.Len(t, edges, 3)
	assert.Equal(t, kgtypes.EdgeGrants, edges[0].Relationship)
	assert.Equal(t, dbName, edges[0].SourceID)
	assert.Equal(t, "roles/datastore.user", edges[0].Metadata["role"])

	assert.Equal(t, "user:alice@example.com", edges[1].TargetID)
	assert.Equal(t, "user:bob@example.com", edges[2].TargetID)
}

func TestFirestoreIAMGrantsEdges_NilPolicy(t *testing.T) {
	assert.Empty(t, firestoreIAMGrantsEdges("db", nil))
}

func TestFirestoreIAMGrantsEdges_EmptyBindings(t *testing.T) {
	assert.Empty(t, firestoreIAMGrantsEdges("db", &iampb.Policy{}))
}

// --- EdgeBackedUpBy (backup schedules) ---

func TestFirestoreBackupScheduleResults_Daily(t *testing.T) {
	dbName := "projects/p/databases/(default)"
	resp := &firestore.GoogleFirestoreAdminV1ListBackupSchedulesResponse{
		BackupSchedules: []*firestore.GoogleFirestoreAdminV1BackupSchedule{
			{
				Name:            "projects/p/databases/(default)/backupSchedules/daily-1",
				Retention:       "604800s",
				DailyRecurrence: &firestore.GoogleFirestoreAdminV1DailyRecurrence{},
			},
		},
	}
	specs, edges := firestoreBackupScheduleResults(dbName, resp)
	require.Len(t, specs, 1)
	require.Len(t, edges, 1)

	assert.Equal(t, "gcp:firestore:backupSchedule", specs[0].ResourceType)
	assert.Equal(t, "604800s", specs[0].Metadata["retention"])
	assert.Equal(t, "daily", specs[0].Metadata["recurrence_type"])

	assert.Equal(t, dbName, edges[0].SourceID)
	assert.Equal(t, specs[0].ID, edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeBackedUpBy, edges[0].Relationship)
}

func TestFirestoreBackupScheduleResults_Weekly(t *testing.T) {
	dbName := "projects/p/databases/mydb"
	resp := &firestore.GoogleFirestoreAdminV1ListBackupSchedulesResponse{
		BackupSchedules: []*firestore.GoogleFirestoreAdminV1BackupSchedule{
			{
				Name:             "projects/p/databases/mydb/backupSchedules/weekly-1",
				Retention:        "1209600s",
				WeeklyRecurrence: &firestore.GoogleFirestoreAdminV1WeeklyRecurrence{},
			},
		},
	}
	specs, edges := firestoreBackupScheduleResults(dbName, resp)
	require.Len(t, specs, 1)
	require.Len(t, edges, 1)
	assert.Equal(t, "weekly", specs[0].Metadata["recurrence_type"])
}

func TestFirestoreBackupScheduleResults_NoSchedules(t *testing.T) {
	specs, edges := firestoreBackupScheduleResults("db", nil)
	assert.Empty(t, specs)
	assert.Empty(t, edges)

	specs, edges = firestoreBackupScheduleResults("db",
		&firestore.GoogleFirestoreAdminV1ListBackupSchedulesResponse{})
	assert.Empty(t, specs)
	assert.Empty(t, edges)
}
