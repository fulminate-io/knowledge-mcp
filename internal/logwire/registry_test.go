// SPDX-License-Identifier: Apache-2.0

package logwire

import (
	"context"
	"testing"
	"time"
)

// stubProvider is a minimal Provider for registry tests.
type stubProvider struct{ id string }

func (s *stubProvider) Configure(map[string]string) error { return nil }
func (s *stubProvider) Collect(context.Context, Query, func([]LogEntry) error) error {
	return nil
}
func (s *stubProvider) ListSources(context.Context, string) ([]Source, error) {
	return nil, nil
}

func TestRegisterAndNew(t *testing.T) {
	name := "test-register-" + time.Now().Format("150405.000")
	Register(name, func() Provider { return &stubProvider{id: name} })

	p, err := New(name)
	if err != nil {
		t.Fatalf("New(%q): %v", name, err)
	}
	if p == nil {
		t.Fatalf("New(%q) returned nil provider", name)
	}
}

func TestNewReturnsFreshInstances(t *testing.T) {
	name := "test-fresh-" + time.Now().Format("150405.000")
	Register(name, func() Provider { return &stubProvider{id: name} })

	a, _ := New(name)
	b, _ := New(name)
	if a == b {
		t.Error("New should return different instances, got same pointer")
	}
}

func TestDuplicateRegisterPanics(t *testing.T) {
	name := "test-dup-" + time.Now().Format("150405.000")
	Register(name, func() Provider { return &stubProvider{} })

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on duplicate Register")
		}
	}()
	Register(name, func() Provider { return &stubProvider{} })
}

func TestNewUnknownProviderReturnsError(t *testing.T) {
	p, err := New("no-such-provider-ever")
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if p != nil {
		t.Error("expected nil provider on error")
	}
}
