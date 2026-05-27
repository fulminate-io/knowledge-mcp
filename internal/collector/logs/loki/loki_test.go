// SPDX-License-Identifier: Apache-2.0

package loki

import (
	"testing"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

func TestNormalizeEntry(t *testing.T) {
	t.Run("all stream labels copied to Labels", func(t *testing.T) {
		entry := normalizeEntry(
			map[string]string{"namespace": "prod", "container": "api", "pod": "api-xyz", "node": "node-1"},
			"1709000000000000000",
			"hello world",
		)
		if entry.Labels["namespace"] != "prod" {
			t.Errorf("namespace: got %q", entry.Labels["namespace"])
		}
		if entry.Labels["container"] != "api" {
			t.Errorf("container: got %q", entry.Labels["container"])
		}
		if entry.Labels["pod"] != "api-xyz" {
			t.Errorf("pod: got %q", entry.Labels["pod"])
		}
		if entry.Labels["node"] != "node-1" {
			t.Errorf("node: got %q", entry.Labels["node"])
		}
	})

	t.Run("valid nanosecond timestamp", func(t *testing.T) {
		entry := normalizeEntry(
			map[string]string{},
			"1709000000000000000", // 2024-02-27
			"test",
		)
		if entry.Timestamp.Year() < 2024 {
			t.Errorf("expected valid timestamp, got %v", entry.Timestamp)
		}
	})

	t.Run("invalid timestamp falls back to now", func(t *testing.T) {
		entry := normalizeEntry(map[string]string{}, "not-a-number", "test")
		if entry.Timestamp.Year() < 2025 {
			t.Errorf("expected fallback to ~now, got %v", entry.Timestamp)
		}
	})

	t.Run("severity from stream label", func(t *testing.T) {
		entry := normalizeEntry(
			map[string]string{"level": "error"},
			"1709000000000000000",
			"plain text message",
		)
		if entry.Severity != logwire.SeverityError {
			t.Errorf("severity: got %q, want %q", entry.Severity, logwire.SeverityError)
		}
	})

	t.Run("severity from detected_level label", func(t *testing.T) {
		entry := normalizeEntry(
			map[string]string{"detected_level": "warn"},
			"1709000000000000000",
			"some message",
		)
		if entry.Severity != logwire.SeverityWarn {
			t.Errorf("severity: got %q, want %q", entry.Severity, logwire.SeverityWarn)
		}
	})

	t.Run("severity from embedded detection", func(t *testing.T) {
		entry := normalizeEntry(
			map[string]string{},
			"1709000000000000000",
			"[ERROR] connection refused",
		)
		if entry.Severity != logwire.SeverityError {
			t.Errorf("severity: got %q, want %q", entry.Severity, logwire.SeverityError)
		}
	})

	t.Run("empty stream labels", func(t *testing.T) {
		entry := normalizeEntry(map[string]string{}, "1709000000000000000", "test")
		if len(entry.Labels) != 0 {
			t.Errorf("expected empty labels, got %v", entry.Labels)
		}
	})
}

func TestBuildLogQL(t *testing.T) {
	tests := []struct {
		name string
		q    logwire.Query
		want string
	}{
		{
			name: "source only",
			q:    logwire.Query{Source: "my-namespace"},
			want: `{namespace="my-namespace"}`,
		},
		{
			name: "source with text filter",
			q:    logwire.Query{Source: "my-ns", TextFilter: "error"},
			want: `{namespace="my-ns"} |= "error"`,
		},
		{
			name: "source with raw query",
			q:    logwire.Query{Source: "my-ns", RawQuery: `| json | level = "error"`},
			want: `{namespace="my-ns"} | json | level = "error"`,
		},
		{
			name: "no source — fallback selector",
			q:    logwire.Query{},
			want: `{namespace=~".+"}`,
		},
		{
			name: "field filter container",
			q: logwire.Query{
				Source:       "my-ns",
				FieldFilters: map[string]string{"container": "api-server"},
			},
			want: `{container="api-server", namespace="my-ns"}`,
		},
		{
			name: "field filter pod",
			q: logwire.Query{
				Source:       "my-ns",
				FieldFilters: map[string]string{"pod": "api-abc123"},
			},
			want: `{namespace="my-ns", pod="api-abc123"}`,
		},
		{
			name: "field filter level is skipped",
			q: logwire.Query{
				Source:       "my-ns",
				FieldFilters: map[string]string{"level": "error"},
			},
			want: `{namespace="my-ns"}`,
		},
		{
			name: "multiple field filters sorted",
			q: logwire.Query{
				Source:       "prod",
				FieldFilters: map[string]string{"container": "web", "pod": "web-abc"},
			},
			want: `{container="web", namespace="prod", pod="web-abc"}`,
		},
		{
			name: "service field mapped to service_name",
			q: logwire.Query{
				Source:       "ns",
				FieldFilters: map[string]string{"service": "auth-svc"},
			},
			want: `{namespace="ns", service_name="auth-svc"}`,
		},
		{
			name: "text and raw query combined",
			q:    logwire.Query{Source: "ns", TextFilter: "timeout", RawQuery: "| json"},
			want: `{namespace="ns"} |= "timeout" | json`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildLogQL(tt.q)
			if got != tt.want {
				t.Errorf("buildLogQL():\n  got:  %s\n  want: %s", got, tt.want)
			}
		})
	}
}

func TestJSONLineParsing(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		wantMsg string
		wantSev string
	}{
		{
			name:    "JSON with level and msg",
			line:    `{"level":"error","ts":"2026-01-01T00:00:00Z","msg":"Operation failed"}`,
			wantMsg: "Operation failed",
			wantSev: logwire.SeverityError,
		},
		{
			name:    "JSON with event field",
			line:    `{"event":"Request validation error","level":"warning","lineno":42}`,
			wantMsg: "Request validation error",
			wantSev: logwire.SeverityWarn,
		},
		{
			name:    "JSON with severity field",
			line:    `{"severity":"CRITICAL","message":"out of memory"}`,
			wantMsg: "out of memory",
			wantSev: logwire.SeverityCritical,
		},
		{
			name:    "JSON with lvl field",
			line:    `{"lvl":"warn","msg":"retry attempt 3"}`,
			wantMsg: "retry attempt 3",
			wantSev: logwire.SeverityWarn,
		},
		{
			name:    "JSON without message field",
			line:    `{"annotations":{"k8s":"v1"},"kind":"Event"}`,
			wantMsg: `{"annotations":{"k8s":"v1"},"kind":"Event"}`,
			wantSev: logwire.SeverityInfo,
		},
		{
			name:    "plain text preserved",
			line:    "2026-01-01 Connection timeout after 3200ms",
			wantMsg: "2026-01-01 Connection timeout after 3200ms",
			wantSev: logwire.SeverityInfo,
		},
		{
			name:    "empty line",
			line:    "",
			wantMsg: "",
			wantSev: logwire.SeverityInfo,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := normalizeEntry(map[string]string{}, "1709000000000000000", tt.line)
			if entry.Message != tt.wantMsg {
				t.Errorf("message:\n  got:  %q\n  want: %q", entry.Message, tt.wantMsg)
			}
			if entry.Severity != tt.wantSev {
				t.Errorf("severity: got %q, want %q", entry.Severity, tt.wantSev)
			}
		})
	}
}
