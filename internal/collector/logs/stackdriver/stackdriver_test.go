// SPDX-License-Identifier: Apache-2.0

package stackdriver

import (
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/logging"
	mrpb "google.golang.org/genproto/googleapis/api/monitoredres"
	"google.golang.org/protobuf/types/known/structpb"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

func TestMapGCPSeverity(t *testing.T) {
	tests := []struct {
		input logging.Severity
		want  string
	}{
		{logging.Emergency, logwire.SeverityCritical},
		{logging.Alert, logwire.SeverityCritical},
		{logging.Critical, logwire.SeverityCritical},
		{logging.Error, logwire.SeverityError},
		{logging.Warning, logwire.SeverityWarn},
		{logging.Notice, logwire.SeverityInfo},
		{logging.Info, logwire.SeverityInfo},
		{logging.Debug, logwire.SeverityDebug},
		{logging.Default, logwire.SeverityInfo},
	}

	for _, tt := range tests {
		t.Run(tt.input.String(), func(t *testing.T) {
			got := mapGCPSeverity(tt.input)
			if got != tt.want {
				t.Errorf("mapGCPSeverity(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestMapSeverityToGCP(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{logwire.SeverityTrace, "DEBUG"},
		{logwire.SeverityDebug, "DEBUG"},
		{logwire.SeverityInfo, "INFO"},
		{logwire.SeverityWarn, "WARNING"},
		{logwire.SeverityError, "ERROR"},
		{logwire.SeverityCritical, "CRITICAL"},
		{"unknown", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := mapSeverityToGCP(tt.input)
			if got != tt.want {
				t.Errorf("mapSeverityToGCP(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractLogID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"full path", "projects/my-project/logs/stderr", "stderr"},
		{"url encoded", "projects/my-project/logs/cloudaudit.googleapis.com%2Factivity", "cloudaudit.googleapis.com/activity"},
		{"no /logs/ prefix", "some-random-string", "some-random-string"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractLogID(tt.input)
			if got != tt.want {
				t.Errorf("extractLogID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestEscapeGCPValue(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`clean`, `clean`},
		{`has "quotes"`, `has \"quotes\"`},
		{`has \backslash`, `has \\backslash`},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := escapeGCPValue(tt.input)
			if got != tt.want {
				t.Errorf("escapeGCPValue(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestEscapeGCPTextFilter(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"already quoted", `"exact match"`, `"exact match"`},
		{"unquoted wraps", `connection timeout`, `"connection timeout"`},
		{"special chars", `has "quotes" inside`, `"has \"quotes\" inside"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := escapeGCPTextFilter(tt.input)
			if got != tt.want {
				t.Errorf("escapeGCPTextFilter(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestConfigure(t *testing.T) {
	t.Run("missing project_id returns error", func(t *testing.T) {
		p := &stackdriverProvider{}
		err := p.Configure(map[string]string{})
		if err == nil {
			t.Fatal("expected error for missing project_id")
		}
		if !strings.Contains(err.Error(), "project_id is required") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("project_id falls back to url", func(t *testing.T) {
		p := &stackdriverProvider{}
		err := p.Configure(map[string]string{"url": "my-project"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.projectID != "my-project" {
			t.Errorf("projectID = %q, want %q", p.projectID, "my-project")
		}
	})

	t.Run("explicit project_id wins over url", func(t *testing.T) {
		p := &stackdriverProvider{}
		err := p.Configure(map[string]string{
			"project_id": "explicit-project",
			"url":        "url-project",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.projectID != "explicit-project" {
			t.Errorf("projectID = %q, want %q", p.projectID, "explicit-project")
		}
	})

	t.Run("impersonate_service_account rejected", func(t *testing.T) {
		p := &stackdriverProvider{}
		err := p.Configure(map[string]string{
			"project_id":                  "my-project",
			"impersonate_service_account": "sa@project.iam.gserviceaccount.com",
		})
		if err == nil {
			t.Fatal("expected error for impersonate_service_account")
		}
		if !strings.Contains(err.Error(), "not yet supported") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("credentials_json explicit key", func(t *testing.T) {
		p := &stackdriverProvider{}
		err := p.Configure(map[string]string{
			"project_id":       "my-project",
			"credentials_json": `{"type":"service_account"}`,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.projectID != "my-project" {
			t.Errorf("projectID = %q, want %q", p.projectID, "my-project")
		}
		if p.credsJSON == "" {
			t.Errorf("expected credsJSON populated from explicit key")
		}
	})

	t.Run("universal credential key", func(t *testing.T) {
		p := &stackdriverProvider{}
		err := p.Configure(map[string]string{
			"project_id": "my-project",
			"credential": `{"type":"service_account"}`,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.credsJSON == "" {
			t.Errorf("expected credsJSON populated from universal credential key")
		}
	})

	t.Run("service_account_path", func(t *testing.T) {
		p := &stackdriverProvider{}
		err := p.Configure(map[string]string{
			"project_id":           "my-project",
			"service_account_path": "/tmp/sa.json",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.saPath != "/tmp/sa.json" {
			t.Errorf("saPath = %q, want %q", p.saPath, "/tmp/sa.json")
		}
	})

	t.Run("ADC fallback — project_id only", func(t *testing.T) {
		p := &stackdriverProvider{}
		err := p.Configure(map[string]string{"project_id": "my-project"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.credsJSON != "" || p.saPath != "" {
			t.Errorf("expected empty creds for ADC fallback, got credsJSON=%q saPath=%q",
				p.credsJSON, p.saPath)
		}
	})
}

func TestNormalizeEntry(t *testing.T) {
	ts := time.Date(2024, 3, 1, 10, 30, 0, 0, time.UTC)
	projectID := "my-project"

	t.Run("text payload, default severity", func(t *testing.T) {
		entry := &logging.Entry{
			Timestamp: ts,
			Severity:  logging.Info,
			Payload:   "simple log message",
			LogName:   "projects/my-project/logs/stderr",
		}
		got := normalizeEntry(entry, projectID)
		if got.Message != "simple log message" {
			t.Errorf("message: got %q", got.Message)
		}
		if got.Severity != logwire.SeverityInfo {
			t.Errorf("severity: got %q", got.Severity)
		}
		if got.Labels["project_id"] != projectID {
			t.Errorf("project_id label: got %q", got.Labels["project_id"])
		}
		if got.Labels["log_name"] != "stderr" {
			t.Errorf("log_name label: got %q", got.Labels["log_name"])
		}
	})

	t.Run("JSON payload (structpb.Struct)", func(t *testing.T) {
		payload, err := structpb.NewStruct(map[string]any{
			"level":   "error",
			"message": "database connection failed",
		})
		if err != nil {
			t.Fatal(err)
		}
		entry := &logging.Entry{
			Timestamp: ts,
			Severity:  logging.Error,
			Payload:   payload,
		}
		got := normalizeEntry(entry, projectID)
		if got.Message != "database connection failed" {
			t.Errorf("message: got %q", got.Message)
		}
		if got.Severity != logwire.SeverityError {
			t.Errorf("severity: got %q", got.Severity)
		}
	})

	t.Run("GKE stderr downgrade — ERROR + embedded INFO", func(t *testing.T) {
		// GKE tags all stderr as ERROR. The message clearly says level=info
		// — normalize should downgrade to INFO.
		entry := &logging.Entry{
			Timestamp: ts,
			Severity:  logging.Error,
			Payload:   `time="2026-02-27T17:49:01Z" level=info msg="server ready"`,
		}
		got := normalizeEntry(entry, projectID)
		if got.Severity != logwire.SeverityInfo {
			t.Errorf("expected downgrade to INFO, got %q", got.Severity)
		}
	})

	t.Run("never upgrade severity", func(t *testing.T) {
		// Embedded severity is ERROR but GCP already classified as INFO.
		// The reclassifier must NOT upgrade.
		entry := &logging.Entry{
			Timestamp: ts,
			Severity:  logging.Info,
			Payload:   `some text with [ERROR] in the middle`,
		}
		got := normalizeEntry(entry, projectID)
		if got.Severity != logwire.SeverityInfo {
			t.Errorf("severity should stay at INFO (never upgrade), got %q", got.Severity)
		}
	})

	t.Run("resource labels → service/host/namespace", func(t *testing.T) {
		entry := &logging.Entry{
			Timestamp: ts,
			Severity:  logging.Info,
			Payload:   "hello",
			Resource: &mrpb.MonitoredResource{
				Type: "k8s_container",
				Labels: map[string]string{
					"container_name": "api",
					"pod_name":       "api-xyz-123",
					"namespace_name": "prod",
					"cluster_name":   "gke-main",
				},
			},
		}
		got := normalizeEntry(entry, projectID)
		if got.Labels["service"] != "api" {
			t.Errorf("service: got %q", got.Labels["service"])
		}
		if got.Labels["host"] != "api-xyz-123" {
			t.Errorf("host: got %q", got.Labels["host"])
		}
		if got.Labels["namespace_name"] != "prod" {
			t.Errorf("namespace_name: got %q", got.Labels["namespace_name"])
		}
		if got.Labels["cluster_name"] != "gke-main" {
			t.Errorf("cluster_name: got %q", got.Labels["cluster_name"])
		}
		if got.Labels["resource_type"] != "k8s_container" {
			t.Errorf("resource_type: got %q", got.Labels["resource_type"])
		}
	})

	t.Run("k8s-pod/app entry label promotes to service", func(t *testing.T) {
		entry := &logging.Entry{
			Timestamp: ts,
			Severity:  logging.Info,
			Payload:   "hello",
			Labels:    map[string]string{"k8s-pod/app": "web"},
		}
		got := normalizeEntry(entry, projectID)
		if got.Labels["service"] != "web" {
			t.Errorf("service: got %q, want %q", got.Labels["service"], "web")
		}
	})

	t.Run("nil payload → empty message", func(t *testing.T) {
		entry := &logging.Entry{
			Timestamp: ts,
			Severity:  logging.Info,
			Payload:   nil,
		}
		got := normalizeEntry(entry, projectID)
		if got.Message != "" {
			t.Errorf("expected empty message, got %q", got.Message)
		}
	})
}
