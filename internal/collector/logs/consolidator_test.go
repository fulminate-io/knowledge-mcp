// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"strings"
	"testing"
	"time"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

func TestIsPythonTracebackFragment(t *testing.T) {
	tests := []struct {
		name string
		tpl  wirelogs.LogTemplate
		want bool
	}{
		{"File frame", wirelogs.LogTemplate{Pattern: `  File "/app/server.py", line 42, in handle`}, true},
		{"caret underline", wirelogs.LogTemplate{Pattern: "      ^^^^^^^^^^^^^^"}, true},
		{"tilde underline", wirelogs.LogTemplate{Pattern: "      ~~~~~~~~~~~~~~"}, true},
		{"raise statement", wirelogs.LogTemplate{Pattern: "    raise ValueError('invalid input')"}, true},
		{"await statement", wirelogs.LogTemplate{Pattern: "    await self.connection.read()"}, true},
		{"return await", wirelogs.LogTemplate{Pattern: "    return await handler(request)"}, true},
		{"httpx.ReadTimeout", wirelogs.LogTemplate{Pattern: "httpx.ReadTimeout: timed out"}, true},
		{"exception chain", wirelogs.LogTemplate{Pattern: "The above exception was the direct cause"}, true},
		{"during handling", wirelogs.LogTemplate{Pattern: "During handling of the above exception"}, true},
		{"self method call", wirelogs.LogTemplate{Pattern: "    self.gen.throw(value)"}, true},
		{"assign await", wirelogs.LogTemplate{Pattern: "    response = await transport.handle(request)"}, true},
		// ExampleVars-based detection.
		{"example vars match", wirelogs.LogTemplate{
			Pattern:     "<*>",
			ExampleVars: [][]string{{"httpcore.ReadTimeout"}},
		}, true},
		// False positives.
		{"normal log", wirelogs.LogTemplate{Pattern: "connection timeout after 3200ms"}, false},
		{"normal error", wirelogs.LogTemplate{Pattern: "failed to process request"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPythonTracebackFragment(&tt.tpl); got != tt.want {
				t.Errorf("isPythonTracebackFragment(%q) = %v, want %v",
					tt.tpl.Pattern, got, tt.want)
			}
		})
	}
}

func TestPythonTracebackConsolidator(t *testing.T) {
	c := &pythonTracebackConsolidator{}
	now := time.Now()

	t.Run("merges traceback fragments", func(t *testing.T) {
		templates := []*wirelogs.LogTemplate{
			{Pattern: "normal error log", Severity: wirelogs.SeverityError, Count: 5,
				FirstSeen: now, LastSeen: now.Add(10 * time.Second)},
			{Pattern: "Traceback (most recent call last):", Severity: wirelogs.SeverityError, Count: 1,
				FirstSeen: now, LastSeen: now},
			{Pattern: `  File "/app/server.py", line 42`, Severity: wirelogs.SeverityError, Count: 1,
				FirstSeen: now, LastSeen: now.Add(100 * time.Millisecond)},
			{Pattern: `  File "/app/client.py", line 99`, Severity: wirelogs.SeverityError, Count: 1,
				FirstSeen: now, LastSeen: now.Add(200 * time.Millisecond)},
			{Pattern: "    await self.connection.read()", Severity: wirelogs.SeverityError, Count: 1,
				FirstSeen: now, LastSeen: now.Add(300 * time.Millisecond)},
			{Pattern: "      ^^^^^^^^^^^^^^", Severity: wirelogs.SeverityError, Count: 1,
				FirstSeen: now, LastSeen: now.Add(400 * time.Millisecond)},
			{Pattern: "httpx.ReadTimeout: timed out", Severity: wirelogs.SeverityError, Count: 1,
				FirstSeen: now, LastSeen: now.Add(500 * time.Millisecond)},
		}

		result := c.Consolidate(templates)

		if len(result) != 2 {
			t.Fatalf("expected 2 templates, got %d", len(result))
		}
		if result[0].Pattern != "normal error log" {
			t.Errorf("first template should be normal, got %q", result[0].Pattern)
		}

		tb := result[1]
		if tb.Count != 6 {
			t.Errorf("merged count: got %d, want 6", tb.Count)
		}
		if tb.Severity != wirelogs.SeverityError {
			t.Errorf("merged severity: got %s, want %s", tb.Severity, wirelogs.SeverityError)
		}
		if !strings.Contains(tb.Pattern, "Python exception") {
			t.Errorf("merged pattern should contain 'Python exception', got %q", tb.Pattern)
		}
	})

	t.Run("fewer than 3 fragments left as-is", func(t *testing.T) {
		templates := []*wirelogs.LogTemplate{
			{Pattern: "normal log", Severity: wirelogs.SeverityInfo, Count: 10,
				FirstSeen: now, LastSeen: now},
			{Pattern: `  File "/app/main.py", line 1`, Severity: wirelogs.SeverityError, Count: 1,
				FirstSeen: now, LastSeen: now},
			{Pattern: "    raise SystemExit(0)", Severity: wirelogs.SeverityError, Count: 1,
				FirstSeen: now, LastSeen: now},
		}

		result := c.Consolidate(templates)

		if len(result) != 3 {
			t.Fatalf("expected 3 templates (no consolidation), got %d", len(result))
		}
	})

	t.Run("temporally separated tracebacks stay separate", func(t *testing.T) {
		templates := []*wirelogs.LogTemplate{
			{Pattern: "Traceback (most recent call last):", Severity: wirelogs.SeverityError, Count: 1,
				FirstSeen: now, LastSeen: now},
			{Pattern: `  File "/app/a.py", line 1`, Severity: wirelogs.SeverityError, Count: 1,
				FirstSeen: now, LastSeen: now.Add(100 * time.Millisecond)},
			{Pattern: "      ^^^^^^^^^^^^^^", Severity: wirelogs.SeverityError, Count: 1,
				FirstSeen: now, LastSeen: now.Add(200 * time.Millisecond)},
			{Pattern: "httpx.ReadTimeout: timed out", Severity: wirelogs.SeverityError, Count: 1,
				FirstSeen: now, LastSeen: now.Add(300 * time.Millisecond)},
			{Pattern: "Traceback (most recent call last):", Severity: wirelogs.SeverityError, Count: 1,
				FirstSeen: now.Add(30 * time.Second), LastSeen: now.Add(30 * time.Second)},
			{Pattern: `  File "/app/b.py", line 99`, Severity: wirelogs.SeverityError, Count: 1,
				FirstSeen: now.Add(30 * time.Second), LastSeen: now.Add(30100 * time.Millisecond)},
			{Pattern: "      ~~~~~~~~~~~~~~", Severity: wirelogs.SeverityError, Count: 1,
				FirstSeen: now.Add(30 * time.Second), LastSeen: now.Add(30200 * time.Millisecond)},
			{Pattern: "builtins.ValueError: bad value", Severity: wirelogs.SeverityError, Count: 1,
				FirstSeen: now.Add(30 * time.Second), LastSeen: now.Add(30300 * time.Millisecond)},
		}

		result := c.Consolidate(templates)

		tbCount := 0
		for _, tpl := range result {
			if strings.Contains(tpl.Pattern, "Python") {
				tbCount++
			}
		}
		if tbCount != 2 {
			t.Errorf("expected 2 Python traceback templates, got %d", tbCount)
		}
	})
}

func TestRunConsolidators(t *testing.T) {
	now := time.Now()

	templates := []*wirelogs.LogTemplate{
		{Pattern: "normal log line", Severity: wirelogs.SeverityInfo, Count: 100,
			FirstSeen: now, LastSeen: now},
		// Go stack fragments.
		{Pattern: "goroutine 1 [running]:", Severity: wirelogs.SeverityError, Count: 1,
			FirstSeen: now, LastSeen: now},
		{Pattern: "runtime.gopanic(0xc000)", Severity: wirelogs.SeverityError, Count: 1,
			FirstSeen: now, LastSeen: now.Add(100 * time.Millisecond)},
		{Pattern: "  /usr/local/go/src/runtime/panic.go:1038", Severity: wirelogs.SeverityError, Count: 1,
			FirstSeen: now, LastSeen: now.Add(200 * time.Millisecond)},
		// Python traceback fragments.
		{Pattern: "Traceback (most recent call last):", Severity: wirelogs.SeverityError, Count: 1,
			FirstSeen: now, LastSeen: now},
		{Pattern: `  File "/app/main.py", line 1`, Severity: wirelogs.SeverityError, Count: 1,
			FirstSeen: now, LastSeen: now.Add(100 * time.Millisecond)},
		{Pattern: "httpx.ReadTimeout: timed out", Severity: wirelogs.SeverityError, Count: 1,
			FirstSeen: now, LastSeen: now.Add(200 * time.Millisecond)},
	}

	result := RunConsolidators(DefaultConsolidators(), templates)

	// Go stack: 3 fragments -> 1 merged
	// Python traceback: 3 fragments -> 1 merged
	// Normal: 1 pass-through
	if len(result) != 3 {
		t.Fatalf("expected 3 templates after consolidation, got %d", len(result))
	}

	var hasGoCrash, hasPyException, hasNormal bool
	for _, tpl := range result {
		switch {
		case tpl.Pattern == "Go runtime crash (goroutine dump)":
			hasGoCrash = true
		case strings.Contains(tpl.Pattern, "Python exception"):
			hasPyException = true
		case tpl.Pattern == "normal log line":
			hasNormal = true
		}
	}

	if !hasGoCrash {
		t.Error("missing Go runtime crash template")
	}
	if !hasPyException {
		t.Error("missing Python exception template")
	}
	if !hasNormal {
		t.Error("missing normal pass-through template")
	}
}

func TestNonMatchingTemplatesPassThrough(t *testing.T) {
	templates := []*wirelogs.LogTemplate{
		{Pattern: "request processed in 42ms", Severity: wirelogs.SeverityInfo, Count: 50},
		{Pattern: "connection pool exhausted", Severity: wirelogs.SeverityWarn, Count: 3},
		{Pattern: "database query timeout", Severity: wirelogs.SeverityError, Count: 1},
	}

	result := RunConsolidators(DefaultConsolidators(), templates)

	if len(result) != 3 {
		t.Fatalf("expected 3 templates (all pass-through), got %d", len(result))
	}
	for i, tpl := range result {
		if tpl.Pattern != templates[i].Pattern {
			t.Errorf("template %d changed: got %q, want %q",
				i, tpl.Pattern, templates[i].Pattern)
		}
	}
}
