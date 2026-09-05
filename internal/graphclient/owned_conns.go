// SPDX-License-Identifier: Apache-2.0

package graphclient

// owned_conns.go gives a GraphClient explicit ownership of the TCP connections
// its HTTP/2 transport dials, so GraphClient.Close can tear them down without
// asking the transport's pool whether it agrees they are idle.
//
// WHY OWNERSHIP RATHER THAN A POOL RELEASE. http2.Transport hands out no handle
// on a connection and reaps one only when it is idle; this transport also sets
// no idle timeout, so a connection the pool declines to close on release stays
// up for the life of the process, holding its own read loop and the peer's serve
// goroutines with it. Recording the connection at dial time is the only place a
// caller can hold one, and it is cheap: one map insert per dial.
//
// WHY THE MAP HOLDS ONLY LIVE CONNECTIONS. A daemon client redials across the
// life of the process — every server restart the reconnect interceptor rides
// through is a new connection — so a slice that only ever appended would grow
// without bound. Each tracked connection removes itself on Close, whoever closes
// it, so the map's size tracks the connections actually open.

import (
	"net"
	"sync"
)

// ownedConns is the set of connections a client's transport has dialed and not
// yet closed. The zero value is ready to use; the map is built on first track.
type ownedConns struct {
	mu   sync.Mutex
	live map[*trackedConn]struct{}
}

// track records conn and returns the wrapper the transport should use in its
// place. The wrapper is a net.Conn in every respect except that closing it also
// forgets it here.
func (o *ownedConns) track(conn net.Conn) net.Conn {
	tc := &trackedConn{Conn: conn, owner: o}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.live == nil {
		o.live = make(map[*trackedConn]struct{})
	}
	o.live[tc] = struct{}{}
	return tc
}

// forget drops conn from the live set. Called by trackedConn.Close, so a
// connection the transport retires on its own leaves no entry behind.
func (o *ownedConns) forget(tc *trackedConn) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.live, tc)
}

// closeAll closes every live connection. Nil-safe on the receiver: a bare
// &GraphClient{} literal has no set, and Close on such a client is a no-op
// rather than a panic.
//
// The set is drained under the lock and closed outside it: trackedConn.Close
// calls forget, which takes the same lock.
func (o *ownedConns) closeAll() {
	if o == nil {
		return
	}
	o.mu.Lock()
	pending := make([]*trackedConn, 0, len(o.live))
	for tc := range o.live {
		pending = append(pending, tc)
	}
	o.live = nil
	o.mu.Unlock()

	for _, tc := range pending {
		// A connection the peer already reset closes with an error that says
		// so; there is nothing for a caller to do about it and nothing to
		// report — the connection is gone either way, which is what was asked.
		_ = tc.Conn.Close()
	}
}

// trackedConn is a net.Conn that removes itself from its owner's live set when
// closed. Embedding net.Conn keeps every other method — deadlines, addresses,
// the ReadFrom/WriteTo fast paths a *net.TCPConn carries — reachable through the
// embedded value rather than reimplemented here.
type trackedConn struct {
	net.Conn
	owner *ownedConns
}

func (c *trackedConn) Close() error {
	c.owner.forget(c)
	return c.Conn.Close()
}
