package llm

import "sync"

// BaseService provides shared bookkeeping for provider implementations.
//
// Each provider sub-package embeds *BaseService in its Service struct so
// the substrate keeps a single source of truth for Provider() identity and
// aggregate token-usage tracking across Generate calls. Implementations
// call RecordUsage at the end of every successful Generate to keep the
// running totals fresh.
//
// Concurrent-safe: Generate calls may run in parallel; RecordUsage takes a
// write lock while GetUsage/ResetUsage take read/write locks as needed.
type BaseService struct {
	provider Provider

	mu    sync.RWMutex
	usage TokenUsage
}

// NewBaseService returns a BaseService bound to provider. The returned
// pointer is safe to embed directly in a provider Service struct.
func NewBaseService(provider Provider) *BaseService {
	return &BaseService{provider: provider}
}

// Provider returns the provider this service is bound to.
func (b *BaseService) Provider() Provider {
	if b == nil {
		return ""
	}
	return b.provider
}

// RecordUsage adds usage into the running total. Implementations call this
// at the tail of every successful Generate.
func (b *BaseService) RecordUsage(usage TokenUsage) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.usage.Add(usage)
}

// GetUsage returns a snapshot of the running totals. Safe to call
// concurrently with RecordUsage.
func (b *BaseService) GetUsage() TokenUsage {
	if b == nil {
		return TokenUsage{}
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.usage
}

// ResetUsage zeroes the running totals. Useful for tests and for callers
// that want to bound a tally to a specific work unit.
func (b *BaseService) ResetUsage() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.usage = TokenUsage{}
}
