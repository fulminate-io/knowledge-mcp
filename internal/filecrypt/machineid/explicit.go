// SPDX-License-Identifier: Apache-2.0
package machineid

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ConfigureCache selects an explicit installation identity before any key is
// derived. It never consults the default user cache. Call once during startup,
// before concurrent consumers; an already resolved different identity is refused.
func ConfigureCache(path string) error {
	id, err := resolveExplicitCache(path)
	if err != nil {
		return err
	}
	cachedOnce.Do(func() { cachedID = id })
	if cachedID != id {
		return fmt.Errorf("machine identity already initialized")
	}
	return nil
}

func resolveExplicitCache(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("machine identity cache must be absolute")
	}
	b, err := os.ReadFile(path)
	if err == nil {
		id := strings.TrimSpace(string(b))
		if len(id) != 16 {
			return "", fmt.Errorf("malformed machine identity cache")
		}
		if _, err := hex.DecodeString(id); err != nil {
			return "", fmt.Errorf("malformed machine identity cache: %w", err)
		}
		return strings.ToLower(id), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read machine identity cache: %w", err)
	}
	raw := resolvePlatform()
	if raw == "" {
		raw = resolveFallback()
	}
	sum := sha256.Sum256([]byte(raw))
	id := hex.EncodeToString(sum[:])[:16]
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", fmt.Errorf("create identity directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return resolveExplicitCache(path)
	}
	if err != nil {
		return "", fmt.Errorf("create identity cache: %w", err)
	}
	_, writeErr := f.WriteString(id + "\n")
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return "", fmt.Errorf("write identity cache: %w", err)
	}
	return id, nil
}
