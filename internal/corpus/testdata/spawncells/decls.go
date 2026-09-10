// SPDX-License-Identifier: Apache-2.0

// decls.go carries the shared declarations the ten cell files need to
// TYPECHECK. It is not a cell: it holds no spawn shape and no record read, so
// the per-cell comparison counts ten files whether or not this one is walked.
//
// The cells must compile even though nothing builds them: Go ignores testdata,
// but the repository's hygiene pass typechecks every .go file it finds, and a
// fixture that does not typecheck is a fixture nobody can maintain.
package spawncells

// Provider stands in for a registered graph-type record. GetCommand is the
// PROVENANCE accessor every cell reads: it is what makes each cell's spawn a
// record-derived one rather than a literal.
type Provider struct{ command, dir string }

func (p *Provider) GetCommand() string { return p.command }
func (p *Provider) GetDir() string     { return p.dir }
