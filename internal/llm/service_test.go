package llm

import (
	"sync"
	"testing"
)

func TestBaseService_ProviderAndZeroValue(t *testing.T) {
	bs := NewBaseService(ProviderOpenAI)
	if bs.Provider() != ProviderOpenAI {
		t.Errorf("Provider() = %s, want %s", bs.Provider(), ProviderOpenAI)
	}
	if got := bs.GetUsage(); got != (TokenUsage{}) {
		t.Errorf("zero usage = %+v, want zero TokenUsage", got)
	}
}

func TestBaseService_RecordAndGet(t *testing.T) {
	bs := NewBaseService(ProviderAnthropic)
	bs.RecordUsage(TokenUsage{InputTokens: 10, OutputTokens: 5})
	bs.RecordUsage(TokenUsage{InputTokens: 3, OutputTokens: 7})

	got := bs.GetUsage()
	want := TokenUsage{InputTokens: 13, OutputTokens: 12}
	if got != want {
		t.Errorf("GetUsage = %+v, want %+v", got, want)
	}
	if got.Total() != 25 {
		t.Errorf("Total = %d, want 25", got.Total())
	}
}

func TestBaseService_Reset(t *testing.T) {
	bs := NewBaseService(ProviderGemini)
	bs.RecordUsage(TokenUsage{InputTokens: 100, OutputTokens: 50})
	bs.ResetUsage()
	if got := bs.GetUsage(); got != (TokenUsage{}) {
		t.Errorf("after Reset = %+v, want zero", got)
	}
}

// TestBaseService_ConcurrentRecord exercises the RWMutex contract — many
// goroutines RecordUsage in parallel; the final tally is deterministic.
// `go test -race` catches data races; here we just verify the arithmetic.
func TestBaseService_ConcurrentRecord(t *testing.T) {
	bs := NewBaseService(ProviderOpenAI)

	const goroutines = 20
	const perGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range perGoroutine {
				bs.RecordUsage(TokenUsage{InputTokens: 1, OutputTokens: 2})
			}
		}()
	}
	// Concurrent readers don't have to see a specific intermediate value;
	// the assertion is only that no race occurs and the final tally is right.
	go func() {
		for range 50 {
			_ = bs.GetUsage()
		}
	}()
	wg.Wait()

	got := bs.GetUsage()
	wantInput := goroutines * perGoroutine
	wantOutput := goroutines * perGoroutine * 2
	if got.InputTokens != wantInput || got.OutputTokens != wantOutput {
		t.Errorf("GetUsage = %+v, want {Input:%d, Output:%d}", got, wantInput, wantOutput)
	}
}

func TestBaseService_NilSafe(t *testing.T) {
	var bs *BaseService
	if bs.Provider() != "" {
		t.Error("nil Provider() not empty")
	}
	if got := bs.GetUsage(); got != (TokenUsage{}) {
		t.Errorf("nil GetUsage = %+v, want zero", got)
	}
	bs.RecordUsage(TokenUsage{InputTokens: 5}) // should not panic
	bs.ResetUsage()                            // should not panic
}
