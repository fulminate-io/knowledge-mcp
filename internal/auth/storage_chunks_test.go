// SPDX-License-Identifier: Apache-2.0
package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCredentialChunks(t *testing.T) {
	values := map[string]string{}
	fail := ""
	s := chunkStore{get: func(k string) (string, error) {
		v, ok := values[k]
		if !ok {
			return "", ErrNotFound
		}
		return v, nil
	}, set: func(k, v string) error {
		if k == fail {
			return errors.New("denied")
		}
		if len(v) > 2560 {
			t.Fatal("oversized native value")
		}
		values[k] = v
		return nil
	}, remove: func(k string) error {
		if k == fail {
			return errors.New("denied")
		}
		delete(values, k)
		return nil
	}}
	ctx := context.Background()
	for _, value := range []string{"legacy", strings.Repeat("x", 2560), strings.Repeat("é", 4096), "replacement"} {
		if err := s.Set(ctx, KeyRefreshToken, value); err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(ctx, KeyRefreshToken)
		if err != nil || got != value {
			t.Fatal("full credential round trip failed")
		}
	}
	parts, err := parseCredentialParts(values[KeyRefreshToken])
	if err != nil || parts.Count != 1 || parts.Previous != nil || len(values) != 2 {
		t.Fatal("replacement must retain only its manifest and one live part")
	}
	if err := s.Delete(ctx, KeyRefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, KeyRefreshToken); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	values[KeyRefreshToken] = "old"
	fail = KeyRefreshToken
	if err := s.Set(ctx, KeyRefreshToken, strings.Repeat("z", 3000)); err == nil {
		t.Fatal("write failure hidden")
	}
	if len(values) != 1 || values[KeyRefreshToken] != "old" {
		t.Fatal("failed write changed old credential or orphaned parts")
	}
	fail = ""
	if err := s.Set(ctx, KeyRefreshToken, strings.Repeat("a", 3000)); err != nil {
		t.Fatal(err)
	}
	for key := range values {
		if key != KeyRefreshToken {
			fail = key
			break
		}
	}
	if err := s.Set(ctx, KeyRefreshToken, "short"); err == nil {
		t.Fatal("old-part cleanup failure hidden")
	}
	if got, err := s.Get(ctx, KeyRefreshToken); err != nil || got != "short" {
		t.Fatal("published credential unavailable after cleanup failure")
	}
	if err := s.Delete(ctx, KeyRefreshToken); err == nil {
		t.Fatal("part deletion failure hidden")
	}
	fail = ""
	if err := s.Delete(ctx, KeyRefreshToken); err != nil {
		t.Fatal(err)
	}
	if len(values) != 0 {
		t.Fatal("retry left native parts behind")
	}
	if err := s.Set(ctx, KeyRefreshToken, strings.Repeat("a", 3000)); err != nil {
		t.Fatal(err)
	}
	for key := range values {
		if key != KeyRefreshToken {
			delete(values, key)
			break
		}
	}
	if _, err := s.Get(ctx, KeyRefreshToken); err == nil {
		t.Fatal("partial credential accepted")
	}
}

func TestCredentialShortRewriteCleanupRetry(t *testing.T) {
	values := map[string]string{}
	fail := false
	s := chunkStore{get: func(k string) (string, error) {
		v, ok := values[k]
		if !ok {
			return "", ErrNotFound
		}
		return v, nil
	}, set: func(k, v string) error { values[k] = v; return nil }, remove: func(k string) error {
		if fail && strings.Contains(k, ".part.") && values[k] == "short-secret" {
			return errors.New("denied")
		}
		delete(values, k)
		return nil
	}}
	ctx := context.Background()
	if err := s.Set(ctx, KeyRefreshToken, strings.Repeat("a", 3000)); err != nil {
		t.Fatal(err)
	}
	fail = true
	if err := s.Set(ctx, KeyRefreshToken, "short-secret"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, KeyRefreshToken); err != nil || got != "short-secret" {
		t.Fatal("short replacement unreadable", err)
	}
	if err := s.Delete(ctx, KeyRefreshToken); err == nil {
		t.Fatal("native deletion denial was hidden")
	}
	fail = false
	if err := s.Delete(ctx, KeyRefreshToken); err != nil {
		t.Fatal(err)
	}
	if len(values) != 0 {
		t.Fatalf("successful retry left %d native credential part(s)", len(values))
	}
}

func TestCredentialCleanupFailureBound(t *testing.T) {
	values := map[string]string{}
	deny := false
	s := chunkStore{get: func(k string) (string, error) {
		v, ok := values[k]
		if !ok {
			return "", ErrNotFound
		}
		return v, nil
	},
		set: func(k, v string) error {
			if len(v) > 2560 {
				t.Fatal("manifest exceeded native credential limit")
			}
			values[k] = v
			return nil
		},
		remove: func(k string) error {
			if deny {
				return errors.New("denied")
			}
			delete(values, k)
			return nil
		}}
	ctx := context.Background()
	if err := s.Set(ctx, "key", strings.Repeat("a", 3000)); err != nil {
		t.Fatal(err)
	}
	deny = true
	bounded := false
	for range 50 {
		before := values["key"]
		err := s.Set(ctx, "key", "replacement")
		if err == nil {
			t.Fatal("cleanup failure hidden")
		}
		if strings.Contains(err.Error(), "cleanup backlog") {
			bounded = true
			if values["key"] != before {
				t.Fatal("refusal replaced published credential")
			}
			break
		}
	}
	if !bounded {
		t.Fatal("cleanup backlog grew without refusal")
	}
	if got, err := s.Get(ctx, "key"); err != nil || got != "replacement" {
		t.Fatal("published credential lost", err)
	}
	deny = false
	if err := s.Delete(ctx, "key"); err != nil {
		t.Fatal(err)
	}
	if len(values) != 0 {
		t.Fatal("retained generations not cleaned")
	}
	if err := s.Set(ctx, "key", "recovered"); err != nil {
		t.Fatal(err)
	}
}
