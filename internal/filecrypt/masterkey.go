// SPDX-License-Identifier: Apache-2.0

// Package filecrypt encrypts this binary's own cache files at rest under a
// machine-bound key. Seal produces a self-describing envelope; Open reads one
// back. Callers hand it whole payloads and never see key material.
//
// UNCONDITIONAL BY CONSTRUCTION. There is no passthrough lane and no
// "encryption disabled" mode: the key is composed from per-build fragments and
// a per-host identifier that always resolve, so every Seal encrypts and every
// failure is an error rather than a plaintext write. A degraded lane here would
// mean writing readable content to disk while reporting success.
//
// The key is derived from several independent fragments rather than one
// constant, so reconstructing it from binary analysis alone is harder than
// reading a literal out of the executable.
package filecrypt

import (
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/filecrypt/keyfragment"
	"github.com/fulminate-io/knowledge-mcp/internal/filecrypt/keyfragment/storefrag"
	"github.com/fulminate-io/knowledge-mcp/internal/filecrypt/keyfragment/thoughtfrag"
	"github.com/fulminate-io/knowledge-mcp/internal/filecrypt/keyfragment/toolsfrag"
	"github.com/fulminate-io/knowledge-mcp/internal/filecrypt/machineid"
)

// MasterKey returns the 32-byte master key every per-file key is derived from.
// It combines per-build key fragments with this host's stable machine
// identifier, so a file sealed on one installation does not open on another.
//
// Computed once per process: each fragment is pure arithmetic over 32 bytes and
// the machine identifier resolution is itself cached, so there is nothing to
// recompute and nothing to batch.
//
// The fragment set and their order are the same as the server's composition of
// its own master key. Only the per-file info string differs between the two
// binaries, and that difference is what keeps their file keys disjoint.
var MasterKey = sync.OnceValue(func() []byte {
	return keyfragment.DeriveMasterKey(
		[]keyfragment.KeyFragment{
			storefrag.Fragment,
			toolsfrag.Fragment,
			thoughtfrag.Fragment,
			machineid.Fragment,
			keyfragment.Fragment,
		},
		machineid.MachineID(),
	)
})
