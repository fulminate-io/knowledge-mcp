// SPDX-License-Identifier: Apache-2.0
//go:build windows

package auth

import (
	"errors"

	keyring "github.com/zalando/go-keyring"
)

// NewStore uses Windows Credential Manager under the given service
// identifier, which [OpenStore] derives from the credential namespace, with
// native parts for credentials exceeding its per-entry byte limit. Tests
// cannot open the real user store.
//
// The three closures capture the service once, so every operation this store
// performs names one service — the same property the darwin and linux stores
// hold with a struct field.
func NewStore(service string) (Store, error) {
	if err := refuseRealStoreInTest(); err != nil {
		return nil, err
	}
	return chunkStore{
		get: func(key string) (string, error) {
			value, err := keyring.Get(service, key)
			if errors.Is(err, keyring.ErrNotFound) {
				err = ErrNotFound
			}
			return value, err
		},
		set: func(key, value string) error { return keyring.Set(service, key, value) },
		remove: func(key string) error {
			err := keyring.Delete(service, key)
			if errors.Is(err, keyring.ErrNotFound) {
				return ErrNotFound
			}
			return err
		},
	}, nil
}
