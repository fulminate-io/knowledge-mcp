// SPDX-License-Identifier: Apache-2.0

package workers

import (
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

func validWorker() Worker {
	return Worker{
		Name:         "smoke-hello",
		Description:  "test worker",
		SystemPrompt: "You are a test worker.",
		Provider:     config.ProviderAnthropic,
		Model:        "claude-sonnet-4-5",
		Triggers: []Trigger{
			{Event: EventManual},
		},
		ToolAllowlist:       []string{"think"},
		MaxIterations:       0,
		MaxWallclockSeconds: 0,
		Enabled:             true,
	}
}

func TestWorker_Validate_AcceptsFullyPopulated(t *testing.T) {
	w := validWorker()
	if err := w.Validate(); err != nil {
		t.Fatalf("Validate(valid) returned error: %v", err)
	}
}

func TestWorker_Validate_RejectsEmptyName(t *testing.T) {
	w := validWorker()
	w.Name = ""
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "Name is required") {
		t.Fatalf("expected Name-required error, got %v", err)
	}
}

func TestWorker_Validate_RejectsNonSlugName(t *testing.T) {
	cases := []string{
		"Smoke-Hello", // uppercase
		"smoke_hello", // underscore
		"-smoke",      // leading hyphen
		"smoke-",      // trailing hyphen
		"smoke--hi",   // double hyphen
		"smoke hi",    // space
		"smoke!",      // punctuation
	}
	for _, name := range cases {
		w := validWorker()
		w.Name = name
		if err := w.Validate(); err == nil {
			t.Errorf("expected slug error for %q, got nil", name)
		}
	}
}

func TestWorker_Validate_RejectsEmptySystemPrompt(t *testing.T) {
	w := validWorker()
	w.SystemPrompt = ""
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "SystemPrompt is required") {
		t.Fatalf("expected SystemPrompt-required error, got %v", err)
	}
	w.SystemPrompt = "   \t\n  "
	err = w.Validate()
	if err == nil || !strings.Contains(err.Error(), "SystemPrompt is required") {
		t.Fatalf("expected SystemPrompt-required error for whitespace, got %v", err)
	}
}

func TestWorker_Validate_RejectsEmptyProvider(t *testing.T) {
	w := validWorker()
	w.Provider = ""
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "Provider is required") {
		t.Fatalf("expected Provider-required error, got %v", err)
	}
}

func TestWorker_Validate_RejectsInvalidProvider(t *testing.T) {
	w := validWorker()
	w.Provider = config.Provider("not-a-real-provider")
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "not a recognized provider") {
		t.Fatalf("expected invalid-provider error, got %v", err)
	}
}

func TestWorker_Validate_RejectsEmptyModel(t *testing.T) {
	w := validWorker()
	w.Model = ""
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "Model is required") {
		t.Fatalf("expected Model-required error, got %v", err)
	}
	w.Model = "   \t\n  "
	err = w.Validate()
	if err == nil || !strings.Contains(err.Error(), "Model is required") {
		t.Fatalf("expected Model-required error for whitespace, got %v", err)
	}
}

func TestWorker_Validate_AcceptsAllConfigProviders(t *testing.T) {
	for _, p := range []config.Provider{
		config.ProviderAnthropic,
		config.ProviderOpenAI,
		config.ProviderGemini,
		config.ProviderClaudeCLI,
		config.ProviderCodexCLI,
	} {
		w := validWorker()
		w.Provider = p
		if err := w.Validate(); err != nil {
			t.Errorf("Validate rejected provider %q: %v", p, err)
		}
	}
}

func TestWorker_Validate_RejectsEmptyToolAllowlist(t *testing.T) {
	w := validWorker()
	w.ToolAllowlist = nil
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "ToolAllowlist is required") {
		t.Fatalf("expected ToolAllowlist-required error for nil, got %v", err)
	}
	w.ToolAllowlist = []string{}
	err = w.Validate()
	if err == nil || !strings.Contains(err.Error(), "ToolAllowlist is required") {
		t.Fatalf("expected ToolAllowlist-required error for empty slice, got %v", err)
	}
}

func TestWorker_Validate_RejectsEmptyEntryInToolAllowlist(t *testing.T) {
	w := validWorker()
	w.ToolAllowlist = []string{"search", ""}
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "ToolAllowlist[1]") {
		t.Fatalf("expected ToolAllowlist[1] error, got %v", err)
	}
}

func TestWorker_Validate_RejectsUnknownTriggerEvent(t *testing.T) {
	w := validWorker()
	w.Triggers = []Trigger{{Event: "not-a-real-event"}}
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "not recognized") {
		t.Fatalf("expected unknown-event error, got %v", err)
	}
}

func TestWorker_Validate_AcceptsAllKnownEvents(t *testing.T) {
	knownEvents := []string{
		EventToolStarted, EventToolCompleted,
		EventWorkerStarted, EventWorkerCompleted,
		EventManual,
	}
	for _, e := range knownEvents {
		w := validWorker()
		w.Triggers = []Trigger{{Event: e}}
		if err := w.Validate(); err != nil {
			t.Errorf("Validate rejected event %q: %v", e, err)
		}
	}
	// Cron requires a Schedule — tested separately.
	w := validWorker()
	w.Triggers = []Trigger{{Event: EventCron, Schedule: "*/5 * * * *"}}
	if err := w.Validate(); err != nil {
		t.Errorf("Validate rejected well-formed cron trigger: %v", err)
	}
}

func TestWorker_Validate_RejectsCronWithoutSchedule(t *testing.T) {
	w := validWorker()
	w.Triggers = []Trigger{{Event: EventCron, Schedule: ""}}
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "Schedule is required") {
		t.Fatalf("expected Schedule-required error, got %v", err)
	}
}

func TestWorker_Validate_RejectsMalformedCronExpression(t *testing.T) {
	cases := []string{
		"not a cron",      // wrong field count + word characters in 3 fields
		"*/5",             // 1 field
		"*/5 *",           // 2 fields
		"*/5 * * *",       // 4 fields
		"*/5 * * * * * *", // 7 fields
		"*/5 * * * !",     // illegal char in last field
		"*/5 * * * mon@",  // @ not allowed mid-field
	}
	for _, expr := range cases {
		w := validWorker()
		w.Triggers = []Trigger{{Event: EventCron, Schedule: expr}}
		if err := w.Validate(); err == nil {
			t.Errorf("expected error for cron expr %q, got nil", expr)
		}
	}
}

func TestWorker_Validate_AcceptsWellFormedCronExpressions(t *testing.T) {
	cases := []string{
		"*/5 * * * *",
		"0 0 * * *",
		"0 9 * * mon-fri",
		"0 0 1 1 *",
		"0 0 * * 0",
		"0,15,30,45 * * * *",
		"0 0 0 * * *", // 6-field with leading second
		"@daily",
		"@hourly",
		"@yearly",
	}
	for _, expr := range cases {
		w := validWorker()
		w.Triggers = []Trigger{{Event: EventCron, Schedule: expr}}
		if err := w.Validate(); err != nil {
			t.Errorf("Validate rejected cron expr %q: %v", expr, err)
		}
	}
}

func TestWorker_Validate_NilReceiverReturnsError(t *testing.T) {
	var w *Worker
	if err := w.Validate(); err == nil {
		t.Fatalf("expected error for nil Worker, got nil")
	}
}

func TestDefaults_ExposeRuntimeConstants(t *testing.T) {
	if got := DefaultMaxIterations(); got != 10 {
		t.Errorf("DefaultMaxIterations = %d; want 10", got)
	}
	if got := DefaultMaxWallclockSeconds(); got != 300 {
		t.Errorf("DefaultMaxWallclockSeconds = %d; want 300", got)
	}
}
