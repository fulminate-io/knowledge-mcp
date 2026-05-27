package pdfcpu

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	pdfapi "github.com/pdfcpu/pdfcpu/pkg/api"
	pdfcpu "github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	pdfmodel "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// ErrEncrypted is returned by Load / LoadFile when the input PDF is
// password-protected. T1 has no decryption story; consumers must surface it
// to the caller. Translated to pdf.ErrEncrypted at the public boundary.
var ErrEncrypted = errors.New("pdfcpu wrapper: encrypted PDF")

// Context wraps the parsed pdfcpu document. It is the foundation type for
// every other internal/pdfcpu function: page-metadata accessors, Info-dict
// getters, IsTagged, etc. take *Context as their receiver.
//
// Context holds no OS handles after Load / LoadFile return — pdfcpu reads
// the entire stream into memory during parse — so Close is a no-op kept
// for API symmetry with future readers that may need release semantics.
type Context struct {
	inner      *pdfmodel.Context
	structXref *xrefBundle // lazy bundle for tagged-PDF /StructTreeRoot navigation
}

// Inner exposes the wrapped pdfcpu Context for sibling files in this
// package (page.go, future structtree readers). NOT exported across the
// package boundary — package internal/pdfcpu is the confinement boundary
// for pdfcpu types.
func (c *Context) Inner() *pdfmodel.Context {
	if c == nil {
		return nil
	}
	return c.inner
}

// Load parses a PDF from r. r need not be an io.ReadSeeker; non-seekable
// readers are buffered in memory via io.ReadAll. Returns ErrEncrypted when
// the PDF is password-protected.
func Load(r io.Reader) (*Context, error) {
	if r == nil {
		return nil, errors.New("pdfcpu wrapper: nil reader")
	}
	rs, err := readSeekerFrom(r)
	if err != nil {
		return nil, fmt.Errorf("pdfcpu wrapper: buffer reader: %w", err)
	}
	return readWith(rs)
}

// LoadFile is the file-path convenience wrapper. It opens path, defers
// Close, and delegates to Load. Use Load directly when you already have a
// reader (HTTP body, in-memory blob, archive entry).
func LoadFile(path string) (*Context, error) {
	if path == "" {
		return nil, errors.New("pdfcpu wrapper: empty path")
	}
	f, err := os.Open(path) //nolint:gosec // caller-supplied path is intentional
	if err != nil {
		// os.Open already includes "open <path>: <reason>" in the error
		// — wrapping with another "open %q" produces a duplicate prefix
		// (`pdfcpu wrapper: open "/p": open /p: no such file`).
		return nil, fmt.Errorf("pdfcpu wrapper: %w", err)
	}
	defer f.Close()
	return readWith(f)
}

// Close is a no-op kept for API symmetry. pdfcpu's parse buffers the entire
// document into memory; nothing OS-level is held after Load returns.
func (c *Context) Close() error {
	return nil
}

// pdfcpuConfigInit serializes the FIRST call to
// pdfmodel.NewDefaultConfiguration in this process. pdfcpu's
// NewDefaultConfiguration lazily initializes a package-level
// `loadedDefaultConfig` global on its first invocation
// (parseConfigFile writes to it from the user config dir or a
// fallback). When two goroutines race that first call — common in
// tests that open multiple PDFs in parallel — `go test -race` flags
// the global write. Subsequent calls take a read-only fast path
// (returning a copy of the now-populated global) and are concurrent-
// safe; only the bootstrap needs serialization.
var pdfcpuConfigInit sync.Once

// newDefaultPdfcpuConfig wraps pdfmodel.NewDefaultConfiguration with a
// sync.Once guard that forces the lazy package-global init to happen
// under serialization. After Do returns, loadedDefaultConfig is
// populated and the actual call we return is the read-only fast-path
// branch — caller can mutate the returned config freely.
func newDefaultPdfcpuConfig() *pdfmodel.Configuration {
	pdfcpuConfigInit.Do(func() {
		_ = pdfmodel.NewDefaultConfiguration()
	})
	return pdfmodel.NewDefaultConfiguration()
}

// readWith is the single shared call site for pdfcpu.api.ReadContext.
// Centralizing here keeps the encryption-detection branch in one place.
func readWith(rs io.ReadSeeker) (*Context, error) {
	conf := newDefaultPdfcpuConfig()
	// ValidationRelaxed accepts a wider set of PDFs than ISO-32000 strict.
	// pdfcpu's NewDefaultConfiguration already sets relaxed; restated here
	// so the choice is visible at the wrapper level.
	conf.ValidationMode = pdfmodel.ValidationRelaxed

	// ReadAndValidate populates derived fields (PageCount, validation
	// flags). Plain ReadContext alone leaves PageCount=0 because pdfcpu
	// resolves it during the validation pass that walks the page tree.
	ctx, err := pdfapi.ReadAndValidate(rs, conf)
	if err != nil {
		if errors.Is(err, pdfcpu.ErrWrongPassword) {
			return nil, ErrEncrypted
		}
		return nil, fmt.Errorf("pdfcpu wrapper: read context: %w", err)
	}
	if ctx == nil || ctx.XRefTable == nil {
		return nil, errors.New("pdfcpu wrapper: nil context after read")
	}
	if ctx.Encrypt != nil {
		// Document is encrypted but ReadContext did not surface a password
		// error (e.g. owner-password only with no permissions check
		// triggered yet). Surface ErrEncrypted explicitly so consumers
		// see a stable signal rather than a downstream parse failure.
		return nil, ErrEncrypted
	}
	return &Context{inner: ctx}, nil
}

// readSeekerFrom returns r as an io.ReadSeeker. If r already satisfies the
// interface it is returned as-is; otherwise the full payload is buffered
// in memory via io.ReadAll.
func readSeekerFrom(r io.Reader) (io.ReadSeeker, error) {
	if rs, ok := r.(io.ReadSeeker); ok {
		return rs, nil
	}
	buf, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(buf), nil
}
