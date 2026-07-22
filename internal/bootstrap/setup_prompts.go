// SPDX-License-Identifier: Apache-2.0

// setup_prompts.go — tiny stdlib-only interactive prompt helpers for
// `knowledge setup`'s guided path. No third-party TUI/term dependency
// (ticket hard constraint): a bufio.Scanner over stdin is all the
// guided flow needs. The Scanner is passed in so tests drive a scripted
// io.Reader deterministically.

package bootstrap

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// promptLine prints "label [def]: " to stdout, reads one line from
// scanner, and returns the trimmed input — or def when the input is
// empty (the user pressed Enter to accept the default) or the stream is
// exhausted. This default-on-empty behavior is what lets the guided
// --reconfigure path PRESERVE a stored credential: seed def with the
// stored value and an empty answer keeps it.
func promptLine(scanner *bufio.Scanner, label, def string) string {
	fmt.Fprintf(os.Stdout, "%s [%s]: ", label, def)
	if !scanner.Scan() {
		return def
	}
	line := strings.TrimSpace(scanner.Text())
	if line == "" {
		return def
	}
	return line
}

// promptYesNo prints "label [y/N]" (default marked) to stdout, reads one
// line, and returns true for y/yes, false for n/no, and def for empty or
// unrecognized input or an exhausted stream. Used for consent gates (the
// reconfigure customization-loss confirm).
func promptYesNo(scanner *bufio.Scanner, label string, def bool) bool {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	fmt.Fprintf(os.Stdout, "%s [%s]: ", label, hint)
	if !scanner.Scan() {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(scanner.Text())) {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	default:
		return def
	}
}
