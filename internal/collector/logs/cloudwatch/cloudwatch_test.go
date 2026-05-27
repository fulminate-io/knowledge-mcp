// SPDX-License-Identifier: Apache-2.0

package cloudwatch

import (
	"testing"
	"time"

	cwTypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

func TestNormalizeEntry(t *testing.T) {
	ts := int64(1709049600000) // 2024-02-27T16:00:00Z
	stream := "ecs/my-service/abc123"
	group := "/ecs/prod/api-server"

	tests := []struct {
		name    string
		event   cwTypes.FilteredLogEvent
		wantSev string
		wantMsg string
		labels  map[string]string
	}{
		{
			name: "plain text",
			event: cwTypes.FilteredLogEvent{
				Timestamp:     &ts,
				LogStreamName: &stream,
				Message:       new("some plain log message"),
			},
			wantSev: logwire.SeverityInfo,
			wantMsg: "some plain log message",
			labels: map[string]string{
				"log_group":  group,
				"log_stream": stream,
				"service":    "api-server",
			},
		},
		{
			name: "JSON with level and msg",
			event: cwTypes.FilteredLogEvent{
				Timestamp:     &ts,
				LogStreamName: &stream,
				Message:       new(`{"level":"error","msg":"database connection failed"}`),
			},
			wantSev: logwire.SeverityError,
			wantMsg: "database connection failed",
		},
		{
			name: "nested JSON container wrapper",
			event: cwTypes.FilteredLogEvent{
				Timestamp: &ts,
				Message:   new(`{"time":"2026-02-27T18:47:41Z","stream":"stderr","log":"{\"event\":\"Starting request\",\"level\":\"info\"}"}`),
			},
			wantSev: logwire.SeverityInfo,
			wantMsg: "Starting request",
		},
		{
			name: "severity prefix ERROR",
			event: cwTypes.FilteredLogEvent{
				Timestamp: &ts,
				Message:   new("ERROR some operation failed badly"),
			},
			wantSev: logwire.SeverityError,
			wantMsg: "some operation failed badly",
		},
		{
			name: "severity detection from embedded level=warning",
			event: cwTypes.FilteredLogEvent{
				Timestamp: &ts,
				Message:   new(`time="2026-02-27T17:49:01Z" level=warning msg="Watch channel closed"`),
			},
			wantSev: logwire.SeverityWarn,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := normalizeEntry(tt.event, group)

			if tt.wantSev != "" && entry.Severity != tt.wantSev {
				t.Errorf("severity: got %q, want %q", entry.Severity, tt.wantSev)
			}
			if tt.wantMsg != "" && entry.Message != tt.wantMsg {
				t.Errorf("message:\n  got:  %q\n  want: %q", entry.Message, tt.wantMsg)
			}
			if entry.Timestamp != time.UnixMilli(ts) {
				t.Errorf("timestamp: got %v, want %v", entry.Timestamp, time.UnixMilli(ts))
			}
			for k, v := range tt.labels {
				if entry.Labels[k] != v {
					t.Errorf("label %q: got %q, want %q", k, entry.Labels[k], v)
				}
			}
		})
	}
}

func TestParseJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantMsg string
		wantSev string
		wantOk  bool
	}{
		{
			name:    "simple JSON with msg and level",
			input:   `{"level":"error","msg":"Operation failed"}`,
			wantMsg: "Operation failed",
			wantSev: logwire.SeverityError,
			wantOk:  true,
		},
		{
			name:    "structlog event field",
			input:   `{"event":"S3 data file not found","level":"error","lineno":90}`,
			wantMsg: "S3 data file not found",
			wantSev: logwire.SeverityError,
			wantOk:  true,
		},
		{
			name:    "nested container wrapper",
			input:   `{"time":"2026-02-27T18:06:21Z","stream":"stderr","log":"{\"level\":\"error\",\"msg\":\"Reconciler error\"}"}`,
			wantMsg: "Reconciler error",
			wantSev: logwire.SeverityError,
			wantOk:  true,
		},
		{
			name:   "depth limit stops recursion",
			input:  `{"msg":"{\"msg\":\"{\\\"msg\\\":\\\"{\\\\\\\"msg\\\\\\\":\\\\\\\"deep\\\\\\\"}\\\"}\"}"}`,
			wantOk: true, // parses but won't recurse past depth 3
		},
		{
			name:   "invalid JSON",
			input:  `not json at all`,
			wantOk: false,
		},
		{
			name:    "nested message object",
			input:   `{"component":"alertmanager","message":{"level":"ERROR","log":"Notify failed"}}`,
			wantMsg: "Notify failed",
			wantSev: logwire.SeverityError,
			wantOk:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, sev, ok := parseJSON(tt.input, 0)
			if ok != tt.wantOk {
				t.Fatalf("ok: got %v, want %v", ok, tt.wantOk)
			}
			if !ok {
				return
			}
			if tt.wantMsg != "" && msg != tt.wantMsg {
				t.Errorf("message:\n  got:  %q\n  want: %q", msg, tt.wantMsg)
			}
			if tt.wantSev != "" && sev != tt.wantSev {
				t.Errorf("severity: got %q, want %q", sev, tt.wantSev)
			}
		})
	}
}

func TestExtractService(t *testing.T) {
	tests := []struct {
		logGroup string
		want     string
	}{
		{"/ecs/prod/api-server", "api-server"},
		{"/aws/lambda/my-function", "my-function"},
		{"/myapp/platform-staging/application", "application"},
		{"/aws/rds/cluster/myapp-aurora/postgresql", "postgresql"},
		{"my-log-group", "my-log-group"},
		{"/aws/eks/cluster/namespace", "namespace"},
	}

	for _, tt := range tests {
		t.Run(tt.logGroup, func(t *testing.T) {
			got := extractService(tt.logGroup)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildFilterPattern(t *testing.T) {
	tests := []struct {
		name string
		q    logwire.Query
		want string
	}{
		{
			name: "text filter only",
			q:    logwire.Query{TextFilter: "connection timeout"},
			want: `"connection timeout"`,
		},
		{
			name: "no filters",
			q:    logwire.Query{},
			want: "",
		},
		{
			name: "severity only - no server side pattern",
			q:    logwire.Query{SeverityMin: logwire.SeverityError},
			want: "",
		},
		{
			name: "text filter with severity",
			q: logwire.Query{
				TextFilter:  "ERROR",
				SeverityMin: logwire.SeverityError,
			},
			want: `"ERROR"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildFilterPattern(tt.q)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveLogGroup(t *testing.T) {
	tests := []struct {
		name string
		q    logwire.Query
		dflt string
		want string
	}{
		{
			name: "source with path",
			q:    logwire.Query{Source: "/aws/lambda/my-func"},
			want: "/aws/lambda/my-func",
		},
		{
			name: "source without path + default prefix",
			q:    logwire.Query{Source: "my-func"},
			dflt: "/aws/lambda/",
			want: "/aws/lambda/my-func",
		},
		{
			name: "no source uses default",
			dflt: "/aws/ecs/prod",
			want: "/aws/ecs/prod",
		},
		{
			name: "no source no default",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveLogGroup(tt.q, tt.dflt)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConfigure(t *testing.T) {
	p := &cloudwatchProvider{}

	// Missing region should error.
	err := p.Configure(map[string]string{})
	if err == nil {
		t.Fatal("expected error for missing region")
	}

	// Valid config.
	err = p.Configure(map[string]string{
		"region":    "us-east-1",
		"profile":   "prod",
		"log_group": "/aws/ecs/prod",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.region != "us-east-1" {
		t.Errorf("region: got %q, want %q", p.region, "us-east-1")
	}
	if p.profile != "prod" {
		t.Errorf("profile: got %q, want %q", p.profile, "prod")
	}
	if p.logGroup != "/aws/ecs/prod" {
		t.Errorf("logGroup: got %q, want %q", p.logGroup, "/aws/ecs/prod")
	}
}
