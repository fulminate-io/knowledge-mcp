// SPDX-License-Identifier: Apache-2.0

package logwire

import "testing"

func TestParseSeverity(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Standard values
		{"TRACE", SeverityTrace},
		{"DEBUG", SeverityDebug},
		{"INFO", SeverityInfo},
		{"WARN", SeverityWarn},
		{"ERROR", SeverityError},
		{"CRITICAL", SeverityCritical},
		// Case insensitive
		{"trace", SeverityTrace},
		{"debug", SeverityDebug},
		{"info", SeverityInfo},
		{"warn", SeverityWarn},
		{"error", SeverityError},
		{"critical", SeverityCritical},
		// Aliases
		{"WARNING", SeverityWarn},
		{"WARNI", SeverityWarn},
		{"ERR", SeverityError},
		{"FATAL", SeverityError},
		{"SEVERE", SeverityError},
		{"CRIT", SeverityCritical},
		{"ALERT", SeverityCritical},
		{"EMERGENCY", SeverityCritical},
		{"EMERG", SeverityCritical},
		{"PANIC", SeverityCritical},
		{"NOTICE", SeverityInfo},
		{"INFORMATION", SeverityInfo},
		{"DBG", SeverityDebug},
		// Unknown defaults to INFO
		{"UNKNOWN", SeverityInfo},
		{"", SeverityInfo},
		{"  ", SeverityInfo},
		// Whitespace trimming
		{"  ERROR  ", SeverityError},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ParseSeverity(tt.input)
			if got != tt.want {
				t.Errorf("ParseSeverity(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSeverityAtLeast(t *testing.T) {
	tests := []struct {
		severity    string
		minSeverity string
		want        bool
	}{
		{SeverityError, SeverityInfo, true},
		{SeverityInfo, SeverityInfo, true},
		{SeverityDebug, SeverityInfo, false},
		{SeverityCritical, SeverityError, true},
		{SeverityTrace, SeverityCritical, false},
	}

	for _, tt := range tests {
		t.Run(tt.severity+">="+tt.minSeverity, func(t *testing.T) {
			got := SeverityAtLeast(tt.severity, tt.minSeverity)
			if got != tt.want {
				t.Errorf("SeverityAtLeast(%q, %q) = %v, want %v",
					tt.severity, tt.minSeverity, got, tt.want)
			}
		})
	}
}

func TestDetectEmbeddedSeverity(t *testing.T) {
	tests := []struct {
		name     string
		msg      string
		expected string
	}{
		{"level=info", `{"level":"info","msg":"ok"}`, SeverityInfo},
		{"level=warn", `level=warn slow query`, SeverityWarn},
		{"level=warning", `level=warning deprecated API`, SeverityWarn},
		{"level=debug", `level=debug trace data`, SeverityDebug},
		{"level=error", `level=error connection refused`, SeverityError},
		{"tab-delimited info", "2026-02-26T10:00:00Z\tinfo\trequest handled", SeverityInfo},
		{"tab-delimited warn", "2026-02-26T10:00:00Z\twarn\tslow query", SeverityWarn},
		{"bracket INFO", "2026-02-26 [INFO] server started", SeverityInfo},
		{"bracket WARN", "[WARN] high latency detected", SeverityWarn},
		{"bracket WARNING", "[WARNING] deprecated endpoint", SeverityWarn},
		{"bracket DEBUG", "[DEBUG] cache miss", SeverityDebug},
		{"no embedded severity", "plain error message without level", ""},
		{"empty message", "", ""},
		{"long message checks first 200 chars",
			"level=info " + string(make([]byte, 500)), SeverityInfo},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectEmbeddedSeverity(tt.msg)
			if got != tt.expected {
				t.Errorf("DetectEmbeddedSeverity(%q) = %q, want %q",
					tt.msg, got, tt.expected)
			}
		})
	}
}

func TestExtractMessageFromMap(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		expected string
	}{
		{
			name:     "message field",
			input:    map[string]any{"message": "hello", "extra": "value"},
			expected: "hello",
		},
		{
			name:     "msg field",
			input:    map[string]any{"msg": "world"},
			expected: "world",
		},
		{
			name:     "textPayload field",
			input:    map[string]any{"textPayload": "log line"},
			expected: "log line",
		},
		{
			name:     "log field",
			input:    map[string]any{"log": "from docker"},
			expected: "from docker",
		},
		{
			name:     "body field",
			input:    map[string]any{"body": "otel body"},
			expected: "otel body",
		},
		{
			name:  "no known field falls back to JSON",
			input: map[string]any{"custom": "data"},
		},
		{
			name:     "empty message field skipped",
			input:    map[string]any{"message": "", "msg": "fallback"},
			expected: "fallback",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractMessageFromMap(tt.input)
			if tt.expected != "" && got != tt.expected {
				t.Errorf("ExtractMessageFromMap() = %q, want %q", got, tt.expected)
			}
			if tt.expected == "" && got == "" {
				t.Error("expected non-empty result from JSON fallback")
			}
		})
	}
}
