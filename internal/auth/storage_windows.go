// SPDX-License-Identifier: Apache-2.0
//go:build windows

package auth

import (
	"errors"

	keyring "github.com/zalando/go-keyring"
)

// NewStore uses Windows Credential Manager, with native parts for credentials
// exceeding its per-entry byte limit. Tests cannot open the real user store.
func NewStore() (Store, error) {
	if err := refuseRealStoreInTest(); err != nil {
		return nil, err
	}
	return chunkStore{
		get: func(key string) (string, error) {
			value, err := keyring.Get(ServiceName, key)
			if errors.Is(err, keyring.ErrNotFound) {
				err = ErrNotFound
			}
			return value, err
		},
		set: func(key, value string) error { return keyring.Set(ServiceName, key, value) },
		remove: func(key string) error {
			err := keyring.Delete(ServiceName, key)
			if errors.Is(err, keyring.ErrNotFound) {
				return ErrNotFound
			}
			return err
		},
	}, nil
}
