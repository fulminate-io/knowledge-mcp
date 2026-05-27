package pdf

import "errors"

// ErrNotImplemented is returned by methods whose subsystem ticket is
// still pending. T1 ships the public type surface but only the open /
// page-count / page-metadata path actually does work; every other
// entry point returns ErrNotImplemented until its owning ticket lands.
var ErrNotImplemented = errors.New("cmd/knowledge/internal/collector/pdf: not implemented (subsystem ticket pending)")

// ErrEncrypted is returned by Open when the input PDF is
// password-protected. T1 has no decryption story; consumers must
// surface it to the caller (or skip the document).
var ErrEncrypted = errors.New("cmd/knowledge/internal/collector/pdf: encrypted PDF requires a password")

// ErrUnsupportedFontType is returned by font-resolution paths when the
// PDF embeds a font type we cannot decode (notably PDF Type 3 user
// fonts, which are programmatic content streams). Wired up in T3.
var ErrUnsupportedFontType = errors.New("cmd/knowledge/internal/collector/pdf: unsupported font type (e.g. Type 3)")
