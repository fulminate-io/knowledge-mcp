// SPDX-License-Identifier: Apache-2.0

package logs

// StreamIDMap assigns sequential uint32 IDs to hex wirelogs.LogStream ID strings.
// Roaring bitmaps operate on uint32 values, so this mapping bridges
// the hex-encoded SHA-256 stream IDs to bitmap-compatible integers.
type StreamIDMap struct {
	forward map[string]uint32
	reverse []string
	next    uint32
}

// NewStreamIDMap creates an empty StreamIDMap ready for use.
func NewStreamIDMap() *StreamIDMap {
	return &StreamIDMap{
		forward: make(map[string]uint32),
	}
}

// Add assigns the next sequential uint32 to id if it has not been seen.
// If id is already mapped, the existing uint32 is returned.
func (m *StreamIDMap) Add(id string) uint32 {
	if uid, ok := m.forward[id]; ok {
		return uid
	}
	uid := m.next
	m.forward[id] = uid
	m.reverse = append(m.reverse, id)
	m.next++
	return uid
}

// Get looks up the uint32 for a hex stream ID.
func (m *StreamIDMap) Get(id string) (uint32, bool) {
	uid, ok := m.forward[id]
	return uid, ok
}

// Resolve converts a uint32 back to its hex stream ID.
// Returns the empty string if uid is out of range.
func (m *StreamIDMap) Resolve(uid uint32) string {
	if int(uid) >= len(m.reverse) {
		return ""
	}
	return m.reverse[uid]
}

// Len returns the number of mapped stream IDs.
func (m *StreamIDMap) Len() int {
	return len(m.reverse)
}
