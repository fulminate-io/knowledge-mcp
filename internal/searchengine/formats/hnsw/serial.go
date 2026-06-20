// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"encoding/binary"
	"fmt"
	"math"
)

// serialVersionWithVectors is the v2 binary HNSW format that appends the flat
// vector array after the topology data. Inline vectors are what make a decoded
// graph fully reconstructable — and therefore merge-eligible (the contract's
// Decode-reconstructs-concrete requirement). It is the ONLY version encode()
// writes (an empty graph still encodes as a valid zero-node v2 blob) and the only
// version decodeGraph accepts; the legacy v1 (topology-only) format is gone — a
// v1 byte is rejected on decode and never produced.
const serialVersionWithVectors byte = 2

// encode serializes the binary HNSW graph (topology + inline vectors) to a v2
// blob. The version byte is ALWAYS v2 (serialVersionWithVectors), including for a
// legitimately-empty graph (zero nodes/vectors) — that yields a valid 29-byte
// zero-node v2 blob with no trailing vector block, which decodeGraph accepts on
// reload. A populated graph is byte-identical to before (it already wrote v2 plus
// the trailing inline vectors); only the empty-graph version byte changed.
// Copied from the server's binary_serial.go Serialize.
func (h *binaryGraph) encode() []byte {
	includeVectors := len(h.vectors) > 0

	estSize := 1 + 28
	for _, node := range h.nodes {
		estSize += 2 + len(node.externalID) + 1
		for _, neighbors := range node.neighbors {
			estSize += 2 + len(neighbors)*4
		}
	}
	if includeVectors {
		estSize += len(h.vectors)
	}

	buf := make([]byte, 0, estSize)

	// Always write v2 — a populated graph carries inline vectors, and an empty
	// graph encodes as a valid zero-node v2 blob (no trailing vectors). decodeGraph
	// accepts only v2, so an empty graph must never fall back to a v1 version byte.
	buf = append(buf, serialVersionWithVectors)

	// Header: 7 × uint32. dims field stores vecBytes for binary HNSW.
	buf = binary.LittleEndian.AppendUint32(buf, uint32(h.vecBytes))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(h.m))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(h.mMax0))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(h.efConstruction))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(int32(h.maxLevel)))
	buf = binary.LittleEndian.AppendUint32(buf, h.entryPoint)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(h.nodes)))

	for _, node := range h.nodes {
		buf = binary.LittleEndian.AppendUint16(buf, uint16(len(node.externalID)))
		buf = append(buf, node.externalID...)
		buf = append(buf, byte(node.maxLevel))

		for _, neighbors := range node.neighbors {
			buf = binary.LittleEndian.AppendUint16(buf, uint16(len(neighbors)))
			for _, id := range neighbors {
				buf = binary.LittleEndian.AppendUint32(buf, id)
			}
		}
	}

	if includeVectors {
		buf = append(buf, h.vectors...)
	}

	return buf
}

// decodeGraph reconstructs a binary HNSW graph from a v2 blob (topology + inline
// vectors). Only v2 is accepted; the inline vectors are what make a decoded graph
// indistinguishable from a freshly built one and therefore merge-eligible. Copied
// from the server's binary_serial.go DeserializeBinaryIndex.
func decodeGraph(data []byte) (*binaryGraph, error) {
	if len(data) < 29 {
		return nil, fmt.Errorf("binary hnsw data too short: %d bytes", len(data))
	}

	pos := 0

	version := data[pos]
	pos++
	if version != serialVersionWithVectors {
		return nil, fmt.Errorf("unsupported binary hnsw serial version: %d (only v%d/inline-vectors is accepted; rebuild from raw vectors)", version, serialVersionWithVectors)
	}

	vecBytes := int(binary.LittleEndian.Uint32(data[pos:]))
	pos += 4
	m := int(binary.LittleEndian.Uint32(data[pos:]))
	pos += 4
	mMax0 := int(binary.LittleEndian.Uint32(data[pos:]))
	pos += 4
	efConstruction := int(binary.LittleEndian.Uint32(data[pos:]))
	pos += 4
	maxLevel := int(int32(binary.LittleEndian.Uint32(data[pos:])))
	pos += 4
	entryPoint := binary.LittleEndian.Uint32(data[pos:])
	pos += 4
	nodeCount := int(binary.LittleEndian.Uint32(data[pos:]))
	pos += 4

	h := &binaryGraph{
		vecBytes:       vecBytes,
		m:              m,
		mMax0:          mMax0,
		efConstruction: efConstruction,
		efSearch:       defaultEfSearch,
		ml:             1.0 / math.Log(float64(m)),
		maxLevel:       maxLevel,
		entryPoint:     entryPoint,
		idMap:          make(map[string]uint32, nodeCount),
		nodes:          make([]hnswNode, 0, nodeCount),
		rng:            newRand(),
	}

	if err := deserializeNodes(data, pos, nodeCount, h.idMap, &h.nodes); err != nil {
		return nil, err
	}

	vecLen := nodeCount * vecBytes
	if vecLen > 0 {
		if len(data) < vecLen {
			return nil, fmt.Errorf("binary hnsw v2: data too short for vectors: need %d trailing bytes, have %d total", vecLen, len(data))
		}
		vecs := make([]byte, vecLen)
		copy(vecs, data[len(data)-vecLen:])
		h.vectors = vecs
	}

	return h, nil
}

// deserializeNodes reads nodeCount hnswNode entries from data starting at pos,
// appending them to nodes and populating idMap.
func deserializeNodes(data []byte, pos, nodeCount int, idMap map[string]uint32, nodes *[]hnswNode) error {
	for i := range nodeCount {
		if pos+2 > len(data) {
			return fmt.Errorf("truncated at node %d", i)
		}
		idLen := int(binary.LittleEndian.Uint16(data[pos:]))
		pos += 2

		if pos+idLen > len(data) {
			return fmt.Errorf("truncated external ID at node %d", i)
		}
		externalID := string(data[pos : pos+idLen])
		pos += idLen

		if pos >= len(data) {
			return fmt.Errorf("truncated max level at node %d", i)
		}
		nodeMaxLevel := int(data[pos])
		pos++

		neighbors, newPos, err := deserializeNeighbors(data, pos, nodeMaxLevel, i)
		if err != nil {
			return err
		}
		pos = newPos

		*nodes = append(*nodes, hnswNode{
			externalID: externalID,
			maxLevel:   nodeMaxLevel,
			neighbors:  neighbors,
		})
		idMap[externalID] = uint32(i)
	}
	return nil
}

// deserializeNeighbors reads the neighbor lists for all layers of a single node.
func deserializeNeighbors(data []byte, pos, nodeMaxLevel, nodeIdx int) ([][]uint32, int, error) {
	neighbors := make([][]uint32, nodeMaxLevel+1)
	for l := 0; l <= nodeMaxLevel; l++ {
		if pos+2 > len(data) {
			return nil, pos, fmt.Errorf("truncated neighbor count at node %d layer %d", nodeIdx, l)
		}
		count := int(binary.LittleEndian.Uint16(data[pos:]))
		pos += 2

		if count > 0 {
			if pos+count*4 > len(data) {
				return nil, pos, fmt.Errorf("truncated neighbors at node %d layer %d", nodeIdx, l)
			}
			nbs := make([]uint32, count)
			for j := range count {
				nbs[j] = binary.LittleEndian.Uint32(data[pos:])
				pos += 4
			}
			neighbors[l] = nbs
		}
	}
	return neighbors, pos, nil
}
