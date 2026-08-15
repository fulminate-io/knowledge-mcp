// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// accountKey is the top-level TOML key holding the selected Fulminate
// account. Declared once here; parser.go binds the same literal in the
// `toml:"fulminate_account_id"` struct tag.
const accountKey = "fulminate_account_id"

// accountComment is the two-line explanatory comment written above the key
// on insert. Kept verbatim so a re-run's byte-for-byte comparison is stable.
const accountComment = "# The Fulminate account this machine's cloud calls are routed to.\n" +
	"# Written by `knowledge login` and changed by `knowledge account use`."

// DefaultPath returns ~/.knowledge/config, the standard location of the
// knowledge client config file.
//
// The same expression appears in several bootstrap call sites; bootstrap
// imports config, so config cannot borrow theirs without an import cycle.
// This package owns the file, so the path expression belongs here.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config.DefaultPath: home dir: %w", err)
	}
	return filepath.Join(home, ".knowledge", "config"), nil
}

// ReadSelectedAccountID returns the fulminate_account_id stored in the config
// at path, or "" when there is no selection.
//
// Side-effect-free by construction: it calls Parse, NOT Load. Load installs
// the parsed config into the package singleton via setActive, which would
// turn a read into a process-wide mutation — unacceptable on the auth-status
// path, whose contract is "makes no network call, writes nothing".
//
// A file that does not exist answers ("", nil): no config is the same answer
// as no selection. Any other read or parse failure is returned.
func ReadSelectedAccountID(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // caller-supplied config path is the point
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("config.ReadSelectedAccountID: read %s: %w", path, err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return "", fmt.Errorf("config.ReadSelectedAccountID: %s: %w", path, err)
	}
	return cfg.FulminateAccountID, nil
}

// WriteSelectedAccountID upserts fulminate_account_id = id into the config
// at path, preserving every other byte — comments included — and preserving
// the existing file mode.
//
// The selection is CHANGEABLE but never CLEARABLE: an empty or whitespace-only
// id is refused with an error and the file is left byte-for-byte unchanged.
//
// Two client functions rewrite an EXISTING ~/.knowledge/config and both must
// preserve the entry: this one (which only ever sets a non-empty value) and
// bootstrap.renderAndWriteConfig (which rewrites the whole file and re-applies
// the prior selection afterwards). config.ensureFileExists also writes the
// file, but only when it is absent, so it cannot clear a selection. Any future
// writer that can rewrite an existing config must join that list.
//
// A whole-file TOML marshal round-trip is deliberately not used: go-toml has
// no comment-preserving round-trip, and this file is a heavily-commented
// starter the user is invited to edit.
//
// Placement matters: TOML binds a bare key to whatever table header precedes
// it, so the assignment is always written BEFORE the first table header.
func WriteSelectedAccountID(path, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("config.WriteSelectedAccountID: refusing to write an empty account id — the selection can be changed but not cleared")
	}

	assignment := fmt.Sprintf("%s = %q", accountKey, id)

	mode := os.FileMode(0o600)
	data, err := os.ReadFile(path) //nolint:gosec // caller-supplied config path is the point
	switch {
	case err == nil:
		// Preserve the existing permissions. This file may hold the five
		// [credentials] API keys, so a hard-coded 0o644 here would silently
		// widen the permissions on a file holding live secrets.
		if st, statErr := os.Stat(path); statErr == nil {
			mode = st.Mode().Perm()
		}
	case os.IsNotExist(err):
		data = nil
	default:
		return fmt.Errorf("config.WriteSelectedAccountID: read %s: %w", path, err)
	}

	out := spliceAccountID(string(data), assignment)

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("config.WriteSelectedAccountID: mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(out), mode); err != nil { //nolint:gosec // path is the caller's own config file location, not request-derived input
		return fmt.Errorf("config.WriteSelectedAccountID: write %s: %w", path, err)
	}
	return nil
}

// spliceAccountID returns body with assignment upserted at top level.
//
// Replace: a line before the first table header that already assigns the key
// is swapped for assignment, leaving every other byte untouched.
// Insert: otherwise the comment block plus assignment goes immediately before
// the first table header, or at EOF when the body has no table header at all.
func spliceAccountID(body, assignment string) string {
	lines := strings.Split(body, "\n")

	firstHeader := -1
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "#") {
			continue // a comment is never a table header
		}
		if strings.HasPrefix(trimmed, "[") {
			firstHeader = i
			break // anything past here binds to a table, not to top level
		}
		if isAccountAssignment(trimmed) {
			// Existing top-level assignment: replace in place.
			lines[i] = assignment
			return strings.Join(lines, "\n")
		}
	}

	if firstHeader == -1 {
		// No table header at all: append the block at EOF. A brand-new
		// (empty) file gets only the block, with no leading blank line.
		if strings.TrimSpace(body) == "" {
			return accountComment + "\n" + assignment + "\n"
		}
		return strings.TrimRight(body, "\n") + "\n\n" + accountComment + "\n" + assignment + "\n"
	}

	merged := make([]string, 0, len(lines)+3)
	merged = append(merged, lines[:firstHeader]...)
	merged = append(merged, "", accountComment, assignment)
	merged = append(merged, lines[firstHeader:]...)
	return strings.Join(merged, "\n")
}

// isAccountAssignment reports whether an already-trimmed line assigns the
// account key: the key name followed by optional spaces and '='.
func isAccountAssignment(trimmed string) bool {
	if !strings.HasPrefix(trimmed, accountKey) {
		return false
	}
	return strings.HasPrefix(strings.TrimLeft(trimmed[len(accountKey):], " \t"), "=")
}
