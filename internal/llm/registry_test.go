package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/schema"
)

// fakeRegistryClient is a minimal Client used only to verify registration
// + dispatch. Independent of FakeClient (added in step 11) so this test
// file has zero forward dependencies on later substrate work.
type fakeRegistryClient struct{ provider Provider }

func (f *fakeRegistryClient) Generate(_ context.Context, _ []*schema.Message, _ ...Option) (*Response, error) {
	return &Response{Provider: f.provider}, nil
}

func TestRegistry_RegisterAndLookup(t *testing.T) {
	t.Cleanup(SnapshotRegistryForTest())
	resetRegistryForTest()

	called := false
	factory := func(_ context.Context, cfg *Config) (Client, error) {
		called = true
		return &fakeRegistryClient{provider: cfg.Provider}, nil
	}

	RegisterProvider(ProviderOpenAI, factory)

	if !HasProvider(ProviderOpenAI) {
		t.Fatal("HasProvider(openai) = false after RegisterProvider")
	}

	client, err := NewClient(context.Background(), &Config{
		Provider: ProviderOpenAI,
		APIKey:   "sk-test",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client == nil {
		t.Fatal("NewClient returned nil client")
	}
	if !called {
		t.Error("factory not invoked")
	}
}

func TestRegistry_NewClient_UnknownProvider(t *testing.T) {
	t.Cleanup(SnapshotRegistryForTest())
	resetRegistryForTest()

	_, err := NewClient(context.Background(), &Config{
		Provider: ProviderOpenAI,
		APIKey:   "sk-test",
	})
	if err == nil {
		t.Fatal("expected ErrProviderNotRegistered")
	}
	if !errors.Is(err, ErrProviderNotRegistered) {
		t.Errorf("err = %v, want errors.Is(ErrProviderNotRegistered)", err)
	}
}

func TestRegistry_NewClient_InvalidConfig(t *testing.T) {
	t.Cleanup(SnapshotRegistryForTest())
	resetRegistryForTest()

	// Even when a factory is registered, missing required fields short-circuit.
	RegisterProvider(ProviderAnthropic, func(_ context.Context, _ *Config) (Client, error) {
		t.Fatal("factory should not run when Validate fails")
		return nil, nil
	})

	_, err := NewClient(context.Background(), &Config{Provider: ProviderAnthropic})
	if err == nil {
		t.Fatal("expected ErrInvalidConfig")
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want errors.Is(ErrInvalidConfig)", err)
	}
}

func TestRegistry_ListProviders_Sorted(t *testing.T) {
	t.Cleanup(SnapshotRegistryForTest())
	resetRegistryForTest()

	noopFactory := func(_ context.Context, _ *Config) (Client, error) { return nil, nil }
	RegisterProvider(ProviderGemini, noopFactory)
	RegisterProvider(ProviderOpenAI, noopFactory)
	RegisterProvider(ProviderAnthropic, noopFactory)

	got := ListProviders()
	want := []Provider{ProviderAnthropic, ProviderGemini, ProviderOpenAI}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (got %v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestRegistry_RegisterNilFactory_NoOp(t *testing.T) {
	t.Cleanup(SnapshotRegistryForTest())
	resetRegistryForTest()

	RegisterProvider(ProviderOpenAI, nil)
	if HasProvider(ProviderOpenAI) {
		t.Error("nil factory got registered")
	}
}
