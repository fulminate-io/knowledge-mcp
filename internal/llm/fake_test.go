package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestFakeClient_SatisfiesClient(t *testing.T) {
	var _ Client = NewFakeClient()
}

func TestFakeClient_GenerateReturnsQueuedResponses(t *testing.T) {
	r1 := &Response{Content: "first"}
	r2 := &Response{Content: "second"}
	f := NewFakeClient(r1, r2)

	got1, err := f.Generate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Generate 1 error: %v", err)
	}
	if got1.Content != "first" {
		t.Errorf("got1.Content = %q, want first", got1.Content)
	}

	got2, err := f.Generate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Generate 2 error: %v", err)
	}
	if got2.Content != "second" {
		t.Errorf("got2.Content = %q, want second", got2.Content)
	}
}

func TestFakeClient_ExhaustedQueueReturnsSentinel(t *testing.T) {
	f := NewFakeClient()
	_, err := f.Generate(context.Background(), nil)
	if !errors.Is(err, ErrFakeExhausted) {
		t.Errorf("err = %v, want ErrFakeExhausted", err)
	}
}

func TestFakeClient_RecordsMessagesAndOptions(t *testing.T) {
	f := NewFakeClient(&Response{}, &Response{})

	messages := []*schema.Message{{Role: schema.User, Content: "hello"}}
	_, _ = f.Generate(context.Background(), messages,
		WithModel("gpt-5-mini"),
		WithTemperature(0.7),
	)

	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("Calls() len = %d, want 1", len(calls))
	}
	got := calls[0]
	if len(got.Messages) != 1 || got.Messages[0].Content != "hello" {
		t.Errorf("Messages = %+v", got.Messages)
	}
	if got.Options == nil {
		t.Fatal("Options nil")
	}
	if got.Options.Model != "gpt-5-mini" {
		t.Errorf("Options.Model = %s", got.Options.Model)
	}
	if got.Options.Temperature == nil || *got.Options.Temperature != 0.7 {
		t.Errorf("Options.Temperature = %v", got.Options.Temperature)
	}
}

func TestFakeClient_SetErrorOverridesQueue(t *testing.T) {
	wantErr := errors.New("simulated upstream failure")
	f := NewFakeClient(&Response{Content: "should not be returned"})
	f.SetError(wantErr)

	resp, err := f.Generate(context.Background(), nil)
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
	if resp != nil {
		t.Errorf("resp = %+v, want nil", resp)
	}
	// Call still recorded.
	if len(f.Calls()) != 1 {
		t.Errorf("Calls() len = %d, want 1", len(f.Calls()))
	}
	// Queue not consumed by an errored call.
	f.SetError(nil)
	resp, err = f.Generate(context.Background(), nil)
	if err != nil {
		t.Fatalf("after SetError(nil): %v", err)
	}
	if resp.Content != "should not be returned" {
		t.Errorf("queued response not recovered: %s", resp.Content)
	}
}

func TestFakeClient_QueueAppends(t *testing.T) {
	f := NewFakeClient()
	f.Queue(&Response{Content: "a"}, &Response{Content: "b"})

	first, _ := f.Generate(context.Background(), nil)
	if first.Content != "a" {
		t.Errorf("first = %q", first.Content)
	}
	second, _ := f.Generate(context.Background(), nil)
	if second.Content != "b" {
		t.Errorf("second = %q", second.Content)
	}
}

func TestFakeClient_Reset(t *testing.T) {
	f := NewFakeClient(&Response{Content: "x"})
	_, _ = f.Generate(context.Background(), nil)
	f.SetError(errors.New("boom"))

	f.Reset()
	if len(f.Calls()) != 0 {
		t.Error("calls not cleared")
	}
	// After Reset, error cleared and queue empty -> ErrFakeExhausted.
	_, err := f.Generate(context.Background(), nil)
	if !errors.Is(err, ErrFakeExhausted) {
		t.Errorf("err = %v, want ErrFakeExhausted", err)
	}
}

// TestFakeClient_CallsSliceIsSnapshot verifies that an earlier Calls()
// snapshot keeps its length even after subsequent Generate calls extend
// the recorded history. (The contract is slice-level snapshot — FakeCall
// values are shallow-copied; mutating the pointed-to RequestOptions is
// off-contract.)
func TestFakeClient_CallsSliceIsSnapshot(t *testing.T) {
	f := NewFakeClient(&Response{}, &Response{})
	_, _ = f.Generate(context.Background(), nil, WithModel("m1"))

	snap1 := f.Calls()
	if len(snap1) != 1 {
		t.Fatalf("snap1 len = %d, want 1", len(snap1))
	}

	_, _ = f.Generate(context.Background(), nil, WithModel("m2"))
	if len(snap1) != 1 {
		t.Errorf("snap1 length changed to %d after a second Generate", len(snap1))
	}
	snap2 := f.Calls()
	if len(snap2) != 2 {
		t.Errorf("snap2 len = %d, want 2", len(snap2))
	}
}
