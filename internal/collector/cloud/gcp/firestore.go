// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

	iamv1 "cloud.google.com/go/iam/apiv1"
	iampb "cloud.google.com/go/iam/apiv1/iampb"
	firestore "google.golang.org/api/firestore/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// firestoreSubCollector collects Firestore databases via the REST admin API.
// Uses the same REST pattern as bigquery.go, dns.go, and sqladmin.go.
type firestoreSubCollector struct {
	service   *firestore.Service
	iamClient *iamv1.IamPolicyClient
	projectID string
}

func newFirestoreSubCollector(
	service *firestore.Service,
	iamClient *iamv1.IamPolicyClient,
	projectID string,
) *firestoreSubCollector {
	return &firestoreSubCollector{
		service:   service,
		iamClient: iamClient,
		projectID: projectID,
	}
}

func (c *firestoreSubCollector) Name() string { return "gcp-firestore" }

func (c *firestoreSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	parent := "projects/" + c.projectID
	resp, err := c.service.Projects.Databases.List(parent).Context(ctx).Do()
	if err != nil {
		return result, fmt.Errorf("firestore: list databases: %w", err)
	}

	bsSvc := firestore.NewProjectsDatabasesBackupSchedulesService(c.service)

	for _, db := range resp.Databases {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if db.Name == "" {
			continue
		}
		content, _ := json.Marshal(db) //nolint:errchkjson // best-effort content envelope
		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           db.Name,
			Name:         extractLast(db.Name),
			ResourceType: "gcp:firestore:database",
			Region:       db.LocationId,
			Content:      content,
			Metadata:     firestoreDatabaseMetadata(db),
		})
		result.Edges = append(result.Edges, firestoreDatabaseEdges(db)...)

		// Best-effort IAM policy via iam/apiv1 (separate RPC).
		if c.iamClient != nil {
			if policy, perr := c.iamClient.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{
				Resource: db.Name,
			}); perr == nil && policy != nil {
				result.Edges = append(result.Edges,
					firestoreIAMGrantsEdges(db.Name, policy)...)
			} else if perr != nil {
				slog.Debug("gcp-firestore: iam policy unavailable",
					"database", db.Name, "error", perr)
			}
		}

		// Best-effort backup schedules (separate RPC).
		if bsResp, bsErr := bsSvc.List(db.Name).Context(ctx).Do(); bsErr == nil {
			specs, edges := firestoreBackupScheduleResults(db.Name, bsResp)
			result.Resources = append(result.Resources, specs...)
			result.Edges = append(result.Edges, edges...)
		} else {
			slog.Debug("gcp-firestore: backup schedules unavailable",
				"database", db.Name, "error", bsErr)
		}
	}

	return result, nil
}

// firestoreDatabaseMetadata extracts searchable metadata from a Firestore database.
func firestoreDatabaseMetadata(db *firestore.GoogleFirestoreAdminV1Database) map[string]string {
	meta := map[string]string{
		"type":                  db.Type,
		"locationId":            db.LocationId,
		"concurrencyMode":       db.ConcurrencyMode,
		"deleteProtectionState": db.DeleteProtectionState,
	}
	if db.DatabaseEdition != "" {
		meta["databaseEdition"] = db.DatabaseEdition
	}
	if db.PointInTimeRecoveryEnablement != "" {
		meta["pointInTimeRecovery"] = db.PointInTimeRecoveryEnablement
	}
	return meta
}

// firestoreDatabaseEdges emits ENCRYPTS_WITH edges for CMEK-enabled databases.
func firestoreDatabaseEdges(db *firestore.GoogleFirestoreAdminV1Database) []cloud.EdgeSpec {
	if db.CmekConfig == nil || db.CmekConfig.KmsKeyName == "" {
		return nil
	}
	return []cloud.EdgeSpec{{
		SourceID:     db.Name,
		TargetID:     db.CmekConfig.KmsKeyName,
		Relationship: kgtypes.EdgeEncryptsWith,
		Metadata:     map[string]string{"encryption_scope": "database"},
	}}
}

// firestoreIAMGrantsEdges turns an iampb.Policy into EdgeGrants edges from
// the database resource to each IAM member. Pure function for testability.
func firestoreIAMGrantsEdges(dbName string, policy *iampb.Policy) []cloud.EdgeSpec {
	if policy == nil {
		return nil
	}
	var edges []cloud.EdgeSpec
	for _, binding := range policy.GetBindings() {
		role := binding.GetRole()
		members := make([]string, len(binding.GetMembers()))
		copy(members, binding.GetMembers())
		sort.Strings(members)
		for _, member := range members {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     dbName,
				TargetID:     member,
				Relationship: kgtypes.EdgeGrants,
				Metadata:     map[string]string{"role": role},
			})
		}
	}
	return edges
}

// firestoreBackupScheduleResults builds resource specs and edges for
// backup schedules associated with a Firestore database.
func firestoreBackupScheduleResults(
	dbName string,
	resp *firestore.GoogleFirestoreAdminV1ListBackupSchedulesResponse,
) ([]cloud.ResourceSpec, []cloud.EdgeSpec) {
	if resp == nil || len(resp.BackupSchedules) == 0 {
		return nil, nil
	}
	var specs []cloud.ResourceSpec
	var edges []cloud.EdgeSpec
	for _, sched := range resp.BackupSchedules {
		if sched.Name == "" {
			continue
		}
		meta := map[string]string{
			"retention": sched.Retention,
		}
		if sched.DailyRecurrence != nil {
			meta["recurrence_type"] = "daily"
		} else if sched.WeeklyRecurrence != nil {
			meta["recurrence_type"] = "weekly"
		}
		specs = append(specs, cloud.ResourceSpec{
			ID:           sched.Name,
			Name:         extractLast(sched.Name),
			ResourceType: "gcp:firestore:backupSchedule",
			Metadata:     meta,
		})
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     dbName,
			TargetID:     sched.Name,
			Relationship: kgtypes.EdgeBackedUpBy,
		})
	}
	return specs, edges
}
