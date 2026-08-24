// SPDX-License-Identifier: Apache-2.0

// local_json_map.go — the shared read/atomic-write primitives over a MACHINE-LOCAL
// JSON string map under ~/.knowledge. Lifted verbatim out of repoManifest so a second
// machine-local map store does not become a second hand-written temp-file writer with its
// own subtly-different missing-file, mode and rename behaviour.
//
// EVERY ERROR STRING IS PARAMETERISED BY `label` RATHER THAN REWRITTEN. The messages are
// user-visible and may be asserted, so the repo manifest's own strings must stay
// byte-identical to what they were when they lived on its methods — hence
// `label + ": mkdir %q: %w"` with label "repo manifest", not a new generic wording.

package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// readLocalJSONMap loads and decodes a JSON string map from path. A missing file is NOT
// an error — it returns an empty map, so first-write callers and absent-key lookups both
// see the empty case. An empty file decodes the same way.
//
// Callers that guard the file with a mutex hold it across this call.
func readLocalJSONMap(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string]string{}, nil
	}
	entries := map[string]string{}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// writeLocalJSONMapAtomic serializes entries and writes them via temp-file + os.Rename in
// the target's own directory, so a reader never observes a half-written file. It ensures
// the enclosing directory exists first (0o750).
//
// tmpPattern is the os.CreateTemp pattern (e.g. "repos-*.json.tmp") and label prefixes
// every error so each caller's messages stay exactly what they were before the lift.
//
// Callers that guard the file with a mutex hold it across this call.
func writeLocalJSONMapAtomic(path, tmpPattern, label string, entries map[string]string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("%s: mkdir %q: %w", label, dir, err)
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("%s: marshal: %w", label, err)
	}
	tmp, err := os.CreateTemp(dir, tmpPattern)
	if err != nil {
		return fmt.Errorf("%s: create temp: %w", label, err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename consumes the temp file.
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("%s: write temp: %w", label, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("%s: close temp: %w", label, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("%s: rename temp into place: %w", label, err)
	}
	return nil
}
