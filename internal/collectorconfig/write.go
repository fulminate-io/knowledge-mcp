// SPDX-License-Identifier: Apache-2.0

package collectorconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// write.go — the file half of `knowledge collector add` and `remove`.
//
// THE WRITE IS ATOMIC: temp file in the target's own directory, then rename, so
// a daemon re-reading the file mid-write never observes half of it. The sequence
// is the one tools/local_json_map.go already uses for the machine-local maps
// under ~/.knowledge; what is copied is the SEQUENCE, not the signature, because
// this file's value type is a struct rather than a string.
//
// THE FILE IS READ BACK UNEXPANDED. Rewriting the whole object means the entries
// already in it are re-marshaled, and an entry read through the expanding path
// would have its ${TOKEN} replaced by the token itself — the write would bake a
// credential into a file whose whole purpose is to hold the reference instead.

// ReadRaw returns the entries of one file exactly as written, with no ${VAR}
// expansion and no shape validation. It is the read half of the write path.
func ReadRaw(path string) (map[string]Entry, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the path is the caller's own resolved config location.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]Entry{}, nil
		}
		return nil, fmt.Errorf("%s: the custom-collector config file cannot be read: %w", path, err)
	}
	return parseRaw(path, raw)
}

// Write serializes entries into path atomically, creating the enclosing
// directory on first write.
func Write(path string, entries map[string]Entry) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("collector config: mkdir %q: %w", dir, err)
	}
	data, err := json.MarshalIndent(File{Collectors: entries}, "", "  ")
	if err != nil {
		return fmt.Errorf("collector config: marshal %q: %w", path, err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, "collectors-*.json.tmp")
	if err != nil {
		return fmt.Errorf("collector config: create temp in %q: %w", dir, err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename consumes the temp file.
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("collector config: write temp for %q: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("collector config: close temp for %q: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("collector config: rename temp into place at %q: %w", path, err)
	}
	return nil
}

// Upsert writes one entry into the scoped file, replacing any entry of the same
// name in THAT file. It refuses before writing anything when the existing file
// cannot be read.
func Upsert(path, name string, e Entry) error {
	entries, err := ReadRaw(path)
	if err != nil {
		return err
	}
	entries[name] = e
	return Write(path, entries)
}

// Remove deletes one entry from the scoped file. A name the file does not carry
// is an ERROR naming the name and the file, never a silent success: an operator
// who misspelled a name would otherwise be told their collector was removed
// while it kept collecting.
func Remove(path, name string) error {
	entries, err := ReadRaw(path)
	if err != nil {
		return err
	}
	if _, ok := entries[name]; !ok {
		return fmt.Errorf("%s: collector %q is not in this file", path, name)
	}
	delete(entries, name)
	return Write(path, entries)
}
