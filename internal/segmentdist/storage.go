// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// ForDestination returns the independent cache owner for a bound operation.
// Unbound internal callers retain the existing owner until they bind a job.
func (m *Manager) ForDestination(ctx context.Context) *Manager {
	if m == nil {
		return nil
	}
	d, ok := graphclient.StorageDestination(ctx)
	if !ok || (m.destination != nil && *m.destination == d) {
		return m
	}
	if m.storageRoot != nil {
		return m.storageRoot.ForDestination(ctx)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.storageManagers == nil {
		m.storageManagers = make(map[graphclient.Destination]*Manager)
	}
	if child := m.storageManagers[d]; child != nil {
		return child
	}
	dir := ""
	if m.cacheDir != "" {
		digest := sha256.Sum256([]byte(d.Storage + "\x00" + d.AccountID))
		dir = filepath.Join(m.cacheDir, fmt.Sprintf("storage-%x", digest))
	}
	child := NewManager(dir, m.maxBytes, m.storageOptions...)
	child.destination = &d
	child.boundAccountID = d.AccountID
	child.storageRoot = m
	m.storageManagers[d] = child
	return child
}
