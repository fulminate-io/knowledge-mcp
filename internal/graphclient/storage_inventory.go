// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"fmt"
)

// InventoryContexts enumerates configured destinations without testing their
// availability. Each inventory read can report its own store's failure.
func (r *Router) InventoryContexts(ctx context.Context) ([]context.Context, error) {
	var contexts []context.Context
	if r.local != nil {
		contexts = append(contexts, WithDestination(WithSearchDestinations(ctx, nil), Destination{Storage: "local"}))
	}
	if r.LoggedIn(ctx) {
		cloud, err := r.BindStorage(ctx, "cloud")
		if err != nil {
			return nil, err
		}
		if _, _, err := r.InventoryPlacement(cloud); err != nil {
			return nil, err
		}
		contexts = append(contexts, WithSearchDestinations(cloud, nil))
	}
	if len(contexts) == 0 {
		return nil, ErrNoBackend
	}
	return contexts, nil
}

// InventoryPlacement exposes a bound destination without exporting it through
// the server wire or requiring inventory consumers to depend on its Go type.
func (r *Router) InventoryPlacement(ctx context.Context) (string, string, error) {
	bound, err := r.BindStorage(ctx, "")
	if err != nil {
		return "", "", err
	}
	d, _ := StorageDestination(bound)
	if d.Storage == "cloud" && d.AccountID == "" {
		return "", "", fmt.Errorf("select a cloud account: run knowledge accounts, then knowledge account use <id|slug>")
	}
	return d.Storage, d.AccountID, nil
}
