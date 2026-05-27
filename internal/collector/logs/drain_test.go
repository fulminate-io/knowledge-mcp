// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"testing"
	"time"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

func entry(msg string) wirelogs.LogEntry {
	return wirelogs.LogEntry{Message: msg, Timestamp: time.Now(), Severity: "INFO"}
}

func entryAt(msg, severity string, ts time.Time) wirelogs.LogEntry {
	return wirelogs.LogEntry{Message: msg, Severity: severity, Timestamp: ts}
}

func TestDrainEngine_BasicClustering(t *testing.T) {
	engine := NewDrainEngine(DefaultDrainConfig())

	msgs := []string{
		"GET /api/users/123 HTTP/1.1 200 45ms",
		"GET /api/users/456 HTTP/1.1 200 32ms",
		"GET /api/users/789 HTTP/1.1 200 67ms",
	}
	for _, msg := range msgs {
		engine.AddMessage(entry(msg))
	}

	templates := engine.Templates()
	if len(templates) != 1 {
		t.Errorf("expected 1 template, got %d", len(templates))
		for i, tpl := range templates {
			t.Logf("  template %d: pattern=%q count=%d", i, tpl.Pattern, tpl.Count)
		}
		return
	}
	if templates[0].Count != 3 {
		t.Errorf("expected count 3, got %d", templates[0].Count)
	}
}

func TestDrainEngine_DifferentPatterns(t *testing.T) {
	engine := NewDrainEngine(DefaultDrainConfig())

	engine.AddMessage(entry("database connection timeout after 3200ms"))
	engine.AddMessage(entry("database connection timeout after 5100ms"))
	engine.AddMessage(entry("GET /api/health HTTP/1.1 200 12ms"))
	engine.AddMessage(entry("GET /api/health HTTP/1.1 200 8ms"))
	engine.AddMessage(entry("PANIC: nil pointer dereference in handler processOrder"))

	templates := engine.Templates()
	if len(templates) < 2 || len(templates) > 4 {
		t.Errorf("expected 2-4 templates, got %d", len(templates))
		for i, tpl := range templates {
			t.Logf("  template %d: pattern=%q count=%d", i, tpl.Pattern, tpl.Count)
		}
	}
}

func TestDrainEngine_PreProcess(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"UUID replacement", "Request 550e8400-e29b-41d4-a716-446655440000 failed", "Request <*> failed"},
		{"IPv4 replacement", "Connection from 192.168.1.100 refused", "Connection from <*> refused"},
		{"timestamp replacement", "2026-02-26T10:30:00Z error occurred", "<*> error occurred"},
		{"long numeric replacement", "Request ID 1234567890 processed", "Request ID <*> processed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PreProcess(tt.input)
			if got != tt.want {
				t.Errorf("PreProcess(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDrainEngine_Similarity(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want float64
	}{
		{"identical", []string{"GET", "/api", "200"}, []string{"GET", "/api", "200"}, 1.0},
		{"one difference", []string{"GET", "/api", "200"}, []string{"GET", "/api", "404"}, 2.0 / 3.0},
		{"all different", []string{"GET", "/api", "200"}, []string{"POST", "/users", "500"}, 0.0},
		{"wildcards match", []string{"GET", "<*>", "200"}, []string{"GET", "/api", "200"}, 1.0},
		{"different lengths", []string{"GET", "/api"}, []string{"GET", "/api", "200"}, 0.0},
		{"empty slices", []string{}, []string{}, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := similarity(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("similarity(%v, %v) = %f, want %f", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestDrainEngine_MergeTokens(t *testing.T) {
	template := []string{"GET", "/api/users", "<*>", "200"}
	tokens := []string{"GET", "/api/users", "789", "200"}

	result := mergeTokens(template, tokens)
	expected := []string{"GET", "/api/users", "<*>", "200"}

	if len(result) != len(expected) {
		t.Fatalf("expected %d tokens, got %d", len(expected), len(result))
	}
	for i := range result {
		if result[i] != expected[i] {
			t.Errorf("token[%d]: got %q, want %q", i, result[i], expected[i])
		}
	}
}

func TestDrainEngine_EmptyMessage(t *testing.T) {
	engine := NewDrainEngine(DefaultDrainConfig())
	result := engine.AddMessage(entry(""))
	if result != nil {
		t.Error("expected nil for empty message")
	}
}

func TestDrainEngine_MaxClusters(t *testing.T) {
	cfg := DefaultDrainConfig()
	cfg.MaxClusters = 10
	engine := NewDrainEngine(cfg)

	// Add many unique messages to exceed the cluster limit.
	for i := range 50 {
		engine.AddMessage(entry("unique_pattern_" + string(rune('A'+i%26)) + "_" + string(rune('a'+i/26%26))))
	}

	templates := engine.Templates()
	if len(templates) == 0 {
		t.Error("expected at least 1 template")
	}
	if len(templates) > cfg.MaxClusters {
		t.Errorf("expected at most %d templates, got %d", cfg.MaxClusters, len(templates))
	}
}

func TestDrainEngine_TemplateID(t *testing.T) {
	engine := NewDrainEngine(DefaultDrainConfig())

	tpl := engine.AddMessage(entry("static log message with no variables"))
	if tpl == nil {
		t.Fatal("expected non-nil template")
	}
	if tpl.ID == "" {
		t.Error("expected non-empty template ID")
	}
	// ID should be deterministic.
	id1 := templateID(tpl.Pattern)
	id2 := templateID(tpl.Pattern)
	if id1 != id2 {
		t.Errorf("templateID not deterministic: %q != %q", id1, id2)
	}
	if id1 != tpl.ID {
		t.Errorf("template ID mismatch: engine=%q computed=%q", tpl.ID, id1)
	}
}

func TestDrainEngine_FirstLastSeen(t *testing.T) {
	engine := NewDrainEngine(DefaultDrainConfig())

	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)

	engine.AddMessage(entryAt("server started on port 8080", "INFO", t1))
	engine.AddMessage(entryAt("server started on port 9090", "INFO", t2))
	engine.AddMessage(entryAt("server started on port 3000", "INFO", t3))

	templates := engine.Templates()
	if len(templates) != 1 {
		t.Fatalf("expected 1 template, got %d", len(templates))
	}
	tpl := templates[0]
	if !tpl.FirstSeen.Equal(t3) {
		t.Errorf("FirstSeen = %v, want %v", tpl.FirstSeen, t3)
	}
	if !tpl.LastSeen.Equal(t2) {
		t.Errorf("LastSeen = %v, want %v", tpl.LastSeen, t2)
	}
}

func TestDrainEngine_ExampleVars(t *testing.T) {
	engine := NewDrainEngine(DefaultDrainConfig())

	// Use numeric IDs so isWildcard routes them through the same tree path.
	engine.AddMessage(entry("user id1001 logged in successfully"))
	engine.AddMessage(entry("user id2002 logged in successfully"))
	engine.AddMessage(entry("user id3003 logged in successfully"))

	templates := engine.Templates()
	if len(templates) != 1 {
		t.Fatalf("expected 1 template, got %d", len(templates))
	}
	tpl := templates[0]

	// First message creates the template; subsequent ones extract vars.
	if len(tpl.ExampleVars) == 0 {
		t.Error("expected example vars from subsequent messages")
	}
	if tpl.Pattern == "" {
		t.Error("expected non-empty pattern")
	}
}

func TestDrainEngine_SeverityTracking(t *testing.T) {
	engine := NewDrainEngine(DefaultDrainConfig())

	t1 := time.Now()
	engine.AddMessage(entryAt("connection reset by peer", "INFO", t1))
	engine.AddMessage(entryAt("connection reset by peer", "WARN", t1))
	engine.AddMessage(entryAt("connection reset by peer", "ERROR", t1))

	templates := engine.Templates()
	if len(templates) != 1 {
		t.Fatalf("expected 1 template, got %d", len(templates))
	}
	if templates[0].Severity != "ERROR" {
		t.Errorf("expected severity ERROR (highest seen), got %q", templates[0].Severity)
	}
}
