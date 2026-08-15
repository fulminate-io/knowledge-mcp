// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ReadModulePath opens <rootDir>/go.mod and returns the `module ...`
// directive. Returns ("", nil) on any failure so non-Go repos don't trip the
// collector — the caller persists the empty value and the server-side topology
// analyzers gracefully skip when the metadata key is empty.
//
// IT LIVES HERE BECAUSE THE PARSER IS THE SECOND CONSUMER. The collector ships
// the value over the wire as CollectResult.ModulePath, and Populate needs the
// same value on the RepoContext it hands to the binds pass, where a language
// arm turns an import path into a repo-relative directory. One client-side
// go.mod read serves both; a second copy beside the first is what this move
// exists to avoid.
func ReadModulePath(rootDir string) (string, error) {
	f, err := os.Open(filepath.Join(rootDir, "go.mod"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "module ") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "module"))
		rest = strings.TrimPrefix(rest, `"`)
		rest = strings.TrimSuffix(rest, `"`)
		if idx := strings.Index(rest, "//"); idx >= 0 {
			rest = strings.TrimSpace(rest[:idx])
		}
		if rest == "" {
			return "", nil
		}
		return rest, nil
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", nil
}
