package llm

import (
	"context"
	"errors"
	"sync"

	"github.com/cloudwego/eino/schema"
)

// ErrFakeExhausted is returned by FakeClient.Generate when the queued
// response slice has been drained. Tests that hit this either queued too
// few responses or a code path called Generate more than expected.
var ErrFakeExhausted = errors.New("llm: FakeClient response queue exhausted")

// FakeCall is a captured Generate invocation. Tests assert against the
// slice returned by FakeClient.Calls() to verify what their code sent.
type FakeCall struct {
	Messages []*schema.Message
	Options  *RequestOptions
}

// FakeClient is a Client implementation for tests in any package. It
// returns queued responses in order and records every call's messages and
// applied RequestOptions so assertions can run after the fact.
//
// FakeClient is concurrent-safe; Generate may be called from multiple
// goroutines. Each call pops the head of the response queue under a lock.
//
// Compile-time check below ensures FakeClient continues to satisfy Client
// as the interface evolves.
type FakeClient struct {
	mu        sync.Mutex
	responses []*Response
	err       error
	calls     []FakeCall
}

var _ Client = (*FakeClient)(nil)

// NewFakeClient returns a FakeClient that will return responses in order
// from successive Generate calls. Pass zero responses to construct a
// fake that always returns ErrFakeExhausted.
func NewFakeClient(responses ...*Response) *FakeClient {
	queued := make([]*Response, len(responses))
	copy(queued, responses)
	return &FakeClient{responses: queued}
}

// SetError configures FakeClient to return err from every subsequent
// Generate call regardless of the queued responses. Useful for testing
// error-handling branches. Pass nil to clear.
func (f *FakeClient) SetError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// Queue appends additional responses to the queue. Useful for tests that
// build up a fake across multiple setup steps.
func (f *FakeClient) Queue(responses ...*Response) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses = append(f.responses, responses...)
}

// Generate records the invocation and returns the next queued response.
// Returns ErrFakeExhausted if the queue is empty. If SetError was called
// with a non-nil error, that error is returned and the queue is left
// untouched (the call is still recorded).
func (f *FakeClient) Generate(_ context.Context, messages []*schema.Message, opts ...Option) (*Response, error) {
	applied := ApplyOptions(opts...)
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, FakeCall{
		Messages: messages,
		Options:  applied,
	})

	if f.err != nil {
		return nil, f.err
	}
	if len(f.responses) == 0 {
		return nil, ErrFakeExhausted
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp, nil
}

// Calls returns a copy of the recorded call history. The slice is safe to
// inspect or mutate without affecting subsequent recordings.
func (f *FakeClient) Calls() []FakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FakeCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// Reset clears recorded calls and queued responses. The error set via
// SetError is also cleared.
func (f *FakeClient) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
	f.responses = nil
	f.err = nil
}
