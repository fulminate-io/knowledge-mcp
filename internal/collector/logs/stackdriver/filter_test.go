// SPDX-License-Identifier: Apache-2.0

package stackdriver

import (
	"strings"
	"testing"
	"time"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

func TestBuildStackdriverFilter(t *testing.T) {
	now := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		projectID   string
		query       logwire.Query
		contains    []string
		notContains []string
	}{
		{
			name:      "time range",
			projectID: "my-project",
			query: logwire.Query{
				StartTime: now.Add(-15 * time.Minute),
				EndTime:   now,
			},
			contains: []string{
				`timestamp >= "2024-06-15T11:45:00Z"`,
				`timestamp <= "2024-06-15T12:00:00Z"`,
			},
		},
		{
			name:      "severity filter",
			projectID: "my-project",
			query: logwire.Query{
				SeverityMin: logwire.SeverityError,
				StartTime:   now.Add(-15 * time.Minute),
				EndTime:     now,
			},
			contains: []string{"severity >= ERROR"},
		},
		{
			name:      "source short form",
			projectID: "my-project",
			query: logwire.Query{
				Source:    "stderr",
				StartTime: now.Add(-15 * time.Minute),
				EndTime:   now,
			},
			contains: []string{`logName="projects/my-project/logs/stderr"`},
		},
		{
			name:      "source full form",
			projectID: "my-project",
			query: logwire.Query{
				Source:    "projects/other/logs/stdout",
				StartTime: now.Add(-15 * time.Minute),
				EndTime:   now,
			},
			contains: []string{`logName="projects/other/logs/stdout"`},
		},
		{
			name:      "text filter",
			projectID: "my-project",
			query: logwire.Query{
				TextFilter: "connection timeout",
				StartTime:  now.Add(-15 * time.Minute),
				EndTime:    now,
			},
			contains: []string{`"connection timeout"`},
		},
		{
			name:      "field filters",
			projectID: "my-project",
			query: logwire.Query{
				FieldFilters: map[string]string{"pod": "web-abc123"},
				StartTime:    now.Add(-15 * time.Minute),
				EndTime:      now,
			},
			contains: []string{`resource.labels.pod_name="web-abc123"`},
		},
		{
			name:      "raw query",
			projectID: "my-project",
			query: logwire.Query{
				RawQuery:  `resource.type="k8s_container"`,
				StartTime: now.Add(-15 * time.Minute),
				EndTime:   now,
			},
			contains: []string{`resource.type="k8s_container"`},
		},
		{
			name:      "source injection neutralized",
			projectID: "my-project",
			query: logwire.Query{
				Source:    `evil" OR severity=DEFAULT OR foo="`,
				StartTime: now.Add(-15 * time.Minute),
				EndTime:   now,
			},
			contains: []string{
				`logName="projects/my-project/logs/evilORseverityDEFAULTORfoo"`,
			},
			notContains: []string{
				`severity=DEFAULT`,
				`OR `,
				`""`,
			},
		},
		{
			name:      "combined",
			projectID: "my-project",
			query: logwire.Query{
				SeverityMin:  logwire.SeverityWarn,
				Source:       "stderr",
				TextFilter:   "error",
				FieldFilters: map[string]string{"service": "api"},
				StartTime:    now.Add(-1 * time.Hour),
				EndTime:      now,
			},
			contains: []string{
				"severity >= WARNING",
				`logName="projects/my-project/logs/stderr"`,
				`"error"`,
				`resource.labels.service_name="api"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildStackdriverFilter(tt.projectID, tt.query)
			for _, s := range tt.contains {
				if !strings.Contains(got, s) {
					t.Errorf("filter should contain %q, got:\n%s", s, got)
				}
			}
			for _, s := range tt.notContains {
				if strings.Contains(got, s) {
					t.Errorf("filter should NOT contain %q, got:\n%s", s, got)
				}
			}
		})
	}
}
