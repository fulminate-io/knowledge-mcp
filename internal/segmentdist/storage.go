// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// ForDestination returns the independent cache owner for a bound operation.
// Unbound internal callers retain the existing owner until they bind a job.
//
// The children are CAPPED at graphclient.MaxBoundDestinations: a request names
// the account it is for, so the set of destinations this daemon is asked to
// hold is caller-driven, and each child owns a cache directory on disk. See
// bindChild for what eviction protects.
func (m *Manager) ForDestination(ctx context.Context) *Manager {
	child, _ := m.resolveDestination(ctx, false)
	return child
}

// evictedChild is a dropped child and the TOMBSTONE PATH its cache directory
// was renamed to under the lock, which is what the caller deletes off it.
type evictedChild struct {
	mgr       *Manager
	tombstone string
}

// resolveDestination is the shared body of ForDestination and forRequest: it
// binds the child (taking a reference when hold is set) and then releases what
// the bind evicted, off the lock.
func (m *Manager) resolveDestination(ctx context.Context, hold bool) (*Manager, func()) {
	noRelease := func() {}
	if m == nil {
		return nil, noRelease
	}
	d, ok := graphclient.StorageDestination(ctx)
	if !ok || (m.destination != nil && *m.destination == d) {
		// The root itself, or a child already bound to this destination: there
		// is no map entry of ours to hold, because nothing can evict it.
		return m, noRelease
	}
	if m.storageRoot != nil {
		return m.storageRoot.resolveDestination(ctx, hold)
	}
	child, evicted, release := m.bindChild(ctx, d, hold)
	// Off the lock: closing a child takes ITS mutex and its engines', and the
	// directory release is disk work that no caller of this map should wait
	// behind. The path deleted here is the TOMBSTONE the bind renamed the
	// victim's directory to, never the live one a rebind may already be using.
	for _, victim := range evicted {
		victim.mgr.Close()
		if victim.tombstone == "" {
			continue
		}
		if err := os.RemoveAll(victim.tombstone); err != nil {
			slog.Warn("segmentdist: releasing an evicted destination's cache directory failed",
				"dir", victim.tombstone, "error", err)
		}
	}
	return child, release
}

// forRequest resolves the destination's child and HOLDS it against eviction for
// the duration of the caller's request, returning the release the caller
// defers. Every serving entry point in this package binds through it.
//
// Eviction Close()s a child and deletes its cache directory, so a child a
// request is still reading or writing must never be a victim; a plain
// ForDestination gives no such promise and is for callers that resolve and use
// the child in one expression.
// THE REFERENCE IS TAKEN BY THE BIND ITSELF, under the one lock acquisition
// that put the child in the map. Taking it afterwards left a window a
// concurrent burst could evict, Close and RemoveAll the child in: forRequest
// then handed back a closed Manager whose cache directory was gone, and the
// request's read came back EMPTY with no error.
func (m *Manager) forRequest(ctx context.Context) (*Manager, func()) {
	return m.resolveDestination(ctx, true)
}

// bindChild returns the child for d, building it on first use, along with the
// children evicted to make room for it.
//
// Two children are never evicted: the one being bound (the request in flight)
// and the SELECTED account's, which the daemon's own background work uses and
// which a caller-driven burst must not push out. When nothing else can be
// dropped the map stands rather than refusing to serve.
func (m *Manager) bindChild(ctx context.Context, d graphclient.Destination, hold bool) (*Manager, []evictedChild, func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.storageManagers == nil {
		m.storageManagers = make(map[graphclient.Destination]*Manager)
		m.storageUse = make(map[graphclient.Destination]uint64)
	}
	if child := m.storageManagers[d]; child != nil {
		m.markStorageUseLocked(d)
		return child, nil, m.holdLocked(d, hold)
	}
	evicted := m.evictStorageManagersLocked(ctx, d)
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
	m.markStorageUseLocked(d)
	return child, evicted, m.holdLocked(d, hold)
}

// holdLocked takes a reference on d for the caller's request and returns the
// release, or a no-op when the caller asked for no hold. Caller must hold m.mu;
// the release takes it itself.
func (m *Manager) holdLocked(d graphclient.Destination, hold bool) func() {
	if !hold {
		return func() {}
	}
	if m.storageRefs == nil {
		m.storageRefs = make(map[graphclient.Destination]int)
	}
	m.storageRefs[d]++
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.storageRefs[d] <= 1 {
				delete(m.storageRefs, d)
				return
			}
			m.storageRefs[d]--
		})
	}
}

// markStorageUseLocked stamps a destination as most recently used. Caller must
// hold m.mu.
func (m *Manager) markStorageUseLocked(d graphclient.Destination) {
	m.storageTick++
	m.storageUse[d] = m.storageTick
}

// evictStorageManagersLocked drops least-recently-used children until there is
// room for incoming, and returns them for the caller to close off the lock.
// Caller must hold m.mu.
func (m *Manager) evictStorageManagersLocked(ctx context.Context, incoming graphclient.Destination) []evictedChild {
	selected := graphclient.Destination{Storage: "cloud", AccountID: accountSelectionID(ctx)}
	var evicted []evictedChild
	for len(m.storageManagers) >= graphclient.MaxBoundDestinations {
		var victim graphclient.Destination
		var oldest uint64
		found := false
		for d := range m.storageManagers {
			if !m.evictableLocked(d, incoming, selected) {
				continue
			}
			if use := m.storageUse[d]; !found || use < oldest {
				victim, oldest, found = d, use, true
			}
		}
		if !found {
			return evicted
		}
		dropped := m.storageManagers[victim]
		evicted = append(evicted, evictedChild{mgr: dropped, tombstone: m.vacateCacheDirLocked(dropped)})
		delete(m.storageManagers, victim)
		delete(m.storageUse, victim)
	}
	return evicted
}

// vacateCacheDirLocked renames an evicted child's cache directory out of the
// way and returns the tombstone path, or "" when there is nothing to delete.
//
// THE DIRECTORY NAME IS DERIVED FROM THE DESTINATION — sha256(storage \x00
// account) — so the child a REBIND creates for the same destination gets the
// same path. Deleting the live path after the lock is dropped therefore reaches
// the rebind's own files, and RemoveAll reports success either way: the loss is
// silent. Renaming it here, under the lock that removed the map entry, means
// the canonical path is free the instant a rebind can see it, and the deletion
// afterwards can only touch bytes nobody can reach any more.
//
// A miss is not a failure: the directory is created lazily, so a child that
// never wrote has none. Caller must hold m.mu.
func (m *Manager) vacateCacheDirLocked(victim *Manager) string {
	if victim == nil || victim.cacheDir == "" {
		return ""
	}
	m.storageTick++
	tombstone := fmt.Sprintf("%s.evicted-%d", victim.cacheDir, m.storageTick)
	if err := os.Rename(victim.cacheDir, tombstone); err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("segmentdist: vacating an evicted destination's cache directory failed",
				"dir", victim.cacheDir, "error", err)
		}
		return ""
	}
	return tombstone
}

// evictableLocked reports whether a child may be dropped to make room.
//
// THE CAP BOUNDS CALLER-DRIVEN KEY SPACE, and only that. A destination is
// caller-driven when a REQUEST can name it — a cloud destination carrying an
// account id, which an inbound Knowledge-Account-Id or a qualified reference
// chooses. Everything else belongs to the daemon itself and is never a victim:
//
//   - {local, ""}: the local store every local-bound op uses. Evicting it
//     Close()d the child and deleted its cache directory, and the next local
//     search — through the fresh child the daemon builds — returned ZERO HITS
//     AND NO ERROR for content that was there a moment before.
//   - {cloud, ""}: the accountless machine-auth binding the daemon's own
//     background work runs on.
//   - the SELECTED account's destination, which a caller-driven burst must not
//     be able to push out.
//   - the destination being bound right now, and any destination a request is
//     still holding through forRequest — eviction closes engines and removes
//     files underneath whoever is reading them.
//
// THE KEEP-RULE IS STATED TWICE — here and inline in
// graphclient.Router.evictBoundCloudLocked, which bounds the per-destination
// CLIENT cache the same way — and the twins change together. It is not factored
// into one predicate because the dependency runs one way: this package imports
// graphclient, never the reverse, so a shared helper would have to live there
// and take a Manager-shaped argument, a worse seam than two sites that name
// each other. The caller-driven test below is the twin's own: cloud AND an
// account id.
//
// Caller must hold m.mu.
func (m *Manager) evictableLocked(d, incoming, selected graphclient.Destination) bool {
	if d == incoming || d == selected {
		return false
	}
	if d.Storage != "cloud" || d.AccountID == "" {
		return false
	}
	return m.storageRefs[d] == 0
}
