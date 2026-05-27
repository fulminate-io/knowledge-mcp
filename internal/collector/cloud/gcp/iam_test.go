// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	iampb "google.golang.org/genproto/googleapis/iam/v1"
)

func TestIAMSubCollector_Name(t *testing.T) {
	c := &iamSubCollector{}
	assert.Equal(t, "gcp-iam", c.Name())
}

func TestAuditConfigMetadata_AllServices(t *testing.T) {
	configs := []*iampb.AuditConfig{
		{
			Service: "allServices",
			AuditLogConfigs: []*iampb.AuditLogConfig{
				{LogType: iampb.AuditLogConfig_ADMIN_READ},
				{LogType: iampb.AuditLogConfig_DATA_READ},
			},
		},
		{
			Service: "cloudsql.googleapis.com",
			AuditLogConfigs: []*iampb.AuditLogConfig{
				{LogType: iampb.AuditLogConfig_DATA_WRITE},
			},
		},
	}
	meta := auditConfigMetadata(configs)
	assert.Equal(t, "ADMIN_READ,DATA_READ", meta["auditLog_allServices"])
	assert.Equal(t, "DATA_WRITE", meta["auditLog_cloudsql.googleapis.com"])
	assert.Contains(t, meta["auditLogServices"], "allServices")
	assert.Contains(t, meta["auditLogServices"], "cloudsql.googleapis.com")
}

func TestAuditConfigMetadata_Empty(t *testing.T) {
	assert.Nil(t, auditConfigMetadata(nil))
	assert.Nil(t, auditConfigMetadata([]*iampb.AuditConfig{}))
}

func TestProjectResourceSpec_WithAuditConfig(t *testing.T) {
	policy := &iampb.Policy{
		AuditConfigs: []*iampb.AuditConfig{
			{
				Service: "allServices",
				AuditLogConfigs: []*iampb.AuditLogConfig{
					{LogType: iampb.AuditLogConfig_ADMIN_READ},
				},
			},
		},
	}
	spec := projectResourceSpec("my-project", policy)
	assert.Equal(t, "projects/my-project", spec.ID)
	assert.Equal(t, "my-project", spec.Name)
	assert.Equal(t, "gcp:resourcemanager:project", spec.ResourceType)
	assert.Equal(t, "my-project", spec.Metadata["projectID"])
	assert.Equal(t, "ADMIN_READ", spec.Metadata["auditLog_allServices"])
}

func TestProjectResourceSpec_NoAuditConfig(t *testing.T) {
	spec := projectResourceSpec("my-project", &iampb.Policy{})
	assert.Equal(t, "projects/my-project", spec.ID)
	assert.Equal(t, "my-project", spec.Metadata["projectID"])
	_, hasAudit := spec.Metadata["auditLogServices"]
	assert.False(t, hasAudit)
}
