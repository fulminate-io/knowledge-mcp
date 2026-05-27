package llm

import "testing"

func TestDefaultHTTPClient_HasTimeout(t *testing.T) {
	c := DefaultHTTPClient()
	if c == nil {
		t.Fatal("DefaultHTTPClient returned nil")
	}
	if c.Timeout != DefaultHTTPTimeout {
		t.Errorf("Timeout = %v, want %v", c.Timeout, DefaultHTTPTimeout)
	}
}

func TestDefaultHTTPClient_FreshInstancePerCall(t *testing.T) {
	a := DefaultHTTPClient()
	b := DefaultHTTPClient()
	if a == b {
		t.Error("DefaultHTTPClient returned the same pointer twice; expected fresh client per call")
	}
}
