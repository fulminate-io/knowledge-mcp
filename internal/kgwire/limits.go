// SPDX-License-Identifier: Apache-2.0

package kgwire

// MaxCloudRequestBytes is the single source of truth for the client→cloud
// request-body budget. Every unbounded client transport path (segment Ship, the
// CollectChunk edge tail, and — via the companion ticket — sync push) byte-packs
// its payload into successive requests each at or under this cap, splitting so
// the FULL payload always lands rather than failing closed.
//
// The Cloudflare-fronted cloud endpoint 413s bodies above ~100 MiB; this 64 MiB
// budget leaves ~36 MiB of headroom under that hard limit, so proactive packing
// keeps every request comfortably accepted without an adaptive halve-on-413.
const MaxCloudRequestBytes = 64 << 20 // 67108864 bytes
