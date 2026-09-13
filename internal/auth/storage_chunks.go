// SPDX-License-Identifier: Apache-2.0
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// chunkStore keeps every byte inside the native credential backend. A small
// manifest atomically publishes a generation after all of its parts exist.
// Small legacy values remain directly readable under their original key.
type chunkStore struct {
	get    func(string) (string, error)
	set    func(string, string) error
	remove func(string) error
}
type credentialParts struct {
	Generation string           `json:"generation"`
	Count      int              `json:"count"`
	Previous   *credentialParts `json:"previous,omitempty"`
}

const credentialPrefix = "knowledge-parts-v1:"
const credentialPartBytes = 2560

func parseCredentialParts(value string) (credentialParts, error) {
	var parts credentialParts
	if !strings.HasPrefix(value, credentialPrefix) {
		return parts, nil
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(value, credentialPrefix)), &parts); err != nil || parts.Count < 1 || parts.Generation == "" {
		return parts, errors.New("auth: invalid credential manifest")
	}
	return parts, nil
}
func credentialPartKey(key string, parts credentialParts, i int) string {
	return fmt.Sprintf("%s.part.%s.%d", key, parts.Generation, i)
}
func (s chunkStore) Get(_ context.Context, key string) (string, error) {
	value, err := s.get(key)
	if err != nil {
		return "", err
	}
	parts, err := parseCredentialParts(value)
	if err != nil {
		return "", err
	}
	if parts.Count == 0 {
		return value, nil
	}
	var result strings.Builder
	for i := 0; i < parts.Count; i++ {
		part, err := s.get(credentialPartKey(key, parts, i))
		if err != nil {
			return "", errors.New("auth: credential part unavailable")
		}
		result.WriteString(part)
	}
	return result.String(), nil
}
func (s chunkStore) removeParts(key string, parts credentialParts) error {
	var failures []error
	for i := 0; i < parts.Count; i++ {
		if err := s.remove(credentialPartKey(key, parts, i)); err != nil && !errors.Is(err, ErrNotFound) {
			failures = append(failures, errors.New("auth: credential part cleanup failed"))
		}
	}
	if parts.Previous != nil {
		failures = append(failures, s.removeParts(key, *parts.Previous))
	}
	return errors.Join(failures...)
}
func (s chunkStore) Set(_ context.Context, key, value string) error {
	old, err := s.get(key)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	previous, err := parseCredentialParts(old)
	if err != nil {
		return err
	}
	parts, published, err := s.prepareParts(key, value, previous)
	if err != nil {
		return err
	}
	if err := s.set(key, published); err != nil {
		parts.Previous = nil
		return errors.Join(err, s.removeParts(key, parts))
	}
	if err := s.removeParts(key, previous); err != nil {
		return err
	}
	if parts.Previous == nil {
		return nil
	}
	parts.Previous = nil
	// Keep even a short replacement in its manifest: its parts remain
	// addressable until Delete completes or a later Set removes them.
	manifest, err := json.Marshal(parts)
	if err != nil {
		return err
	}
	return s.set(key, credentialPrefix+string(manifest))
}

func (s chunkStore) prepareParts(key, value string, previous credentialParts) (credentialParts, string, error) {
	// Bound retained cleanup generations before writing any new parts. Eight
	// generations remain well below the native credential value size limit.
	depth := 0
	for retained := &previous; retained != nil && retained.Count > 0; retained = retained.Previous {
		depth++
		if depth >= 8 {
			return credentialParts{}, "", errors.New("auth: credential cleanup backlog reached its limit; retry credential deletion before storing a replacement")
		}
	}
	if len(value) <= credentialPartBytes && !strings.HasPrefix(value, credentialPrefix) && previous.Count == 0 {
		return credentialParts{}, value, nil
	}
	generation, err := randomURLSafe(18)
	if err != nil {
		return credentialParts{}, "", err
	}
	parts := credentialParts{Generation: generation, Count: max(1, (len(value)+credentialPartBytes-1)/credentialPartBytes)}
	for i := 0; i < parts.Count; i++ {
		end := min((i+1)*credentialPartBytes, len(value))
		if err := s.set(credentialPartKey(key, parts, i), value[i*credentialPartBytes:end]); err != nil {
			return parts, "", errors.Join(err, s.removeParts(key, parts))
		}
	}
	if previous.Count > 0 {
		parts.Previous = &previous
	}
	manifest, err := json.Marshal(parts)
	if err != nil {
		parts.Previous = nil
		return parts, "", errors.Join(err, s.removeParts(key, parts))
	}
	return parts, credentialPrefix + string(manifest), nil
}

func (s chunkStore) Delete(_ context.Context, key string) error {
	value, err := s.get(key)
	if err != nil {
		return err
	}
	parts, err := parseCredentialParts(value)
	if err != nil {
		return err
	}
	// Retain the manifest on failed cleanup so a subsequent delete can retry.
	if err = s.removeParts(key, parts); err != nil {
		return err
	}
	return s.remove(key)
}
