// SPDX-License-Identifier: Apache-2.0

package graphclient

import "strings"

// logSafe returns s with carriage returns and line feeds replaced by their
// literal two-character escapes, so a value that came off the wire — a peer's
// tool name, its raw argument JSON, a remote server's error text — cannot end
// its own log record and open a second one that an operator or an ingestion
// pipeline will read as genuine.
//
// EVERYTHING ELSE IS LEFT ALONE, INCLUDING THE LENGTH. The value is still
// logged in full; only its ability to span records is removed.
//
// THERE IS NO FAST PATH ON PURPOSE. An `if !strings.ContainsAny(s, "\r\n")`
// early return would be a second arm through which an unescaped value reaches
// the caller, which is exactly the shape a taint analysis reads as unsanitized
// and exactly the shape a later edit gets wrong. Both replacements always run.
func logSafe(s string) string {
	s = strings.ReplaceAll(s, "\n", `\n`)
	return strings.ReplaceAll(s, "\r", `\r`)
}

// logSafeErr is logSafe over an error's message, for the errors this package
// logs that carry a remote server's own text.
//
// A NIL ERROR RENDERS AS "<nil>", which is what slog renders a nil error value
// as, so passing an error through this helper does not change what a
// nil-error log line says. The call-returned line logs a nil error on the
// success path, so that case is the common one here rather than the corner.
func logSafeErr(err error) string {
	if err == nil {
		return "<nil>"
	}
	return logSafe(err.Error())
}
