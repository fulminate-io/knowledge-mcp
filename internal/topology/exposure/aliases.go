// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// aliases.go re-exports the foundation result/contract vocabulary into the
// exposure package namespace. The analyzer ALGORITHMS were relocated verbatim
// from pkg/topology, where Finding / Request / Severity and the severity
// constants were package-local symbols; aliasing them here lets every ported
// body keep referencing the unqualified names exactly as it did against the
// store-backed package, so the relocation changes data access only and never
// the algorithm text. Register is wrapped (not aliased) because it is a
// function, not a type.

// Finding is the exposure-package alias for foundation.Finding.
type Finding = foundation.Finding

// Request is the exposure-package alias for foundation.Request. Note the wire
// Request carries a Caller (foundation.GraphCaller), Graph, Name, TopK, Extra,
// etc. — there is no store handle. The analyzers read every node and edge
// through Caller via the cloudReader shim in reader.go.
type Request = foundation.Request

// Severity is the exposure-package alias for foundation.Severity.
type Severity = foundation.Severity

// Severity constants aliased from foundation so the ported sort/classify
// bodies keep using the unqualified names.
const (
	SeverityInfo     = foundation.SeverityInfo
	SeverityNotice   = foundation.SeverityNotice
	SeverityWarning  = foundation.SeverityWarning
	SeverityCritical = foundation.SeverityCritical
)

// Register adds an analyzer to the shared foundation registry. Thin wrapper so
// the analyzers' init() bodies keep calling the unqualified Register(...) they
// called in pkg/topology; the registry itself lives in foundation.
func Register(a foundation.Analyzer) { foundation.Register(a) }

// Get returns the registered analyzer with the given name. Thin wrapper over
// foundation.Get so the relocated registration tests keep their unqualified
// Get(...) call shape.
func Get(name string) (foundation.Analyzer, bool) { return foundation.Get(name) }

// All returns every registered analyzer sorted by Name. Thin wrapper over
// foundation.All so the relocated registration tests keep their unqualified
// All() call shape.
func All() []foundation.Analyzer { return foundation.All() }
