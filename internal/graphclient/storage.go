// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"connectrpc.com/connect"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// Destination identifies a storage boundary independently of graph selectors.
type Destination struct {
	Storage string `json:"storage"`
	// AccountID is the authenticated selected account, never the legacy tool account argument.
	AccountID string `json:"account,omitempty"`
}

type destinationKey struct{}

type searchDestinationsKey struct{}

// WithSearchDestinations scopes federation to one search operation.
func WithSearchDestinations(ctx context.Context, destinations []Destination) context.Context {
	return context.WithValue(ctx, searchDestinationsKey{}, destinations)
}

// SearchDestinations returns the independently configured search backends.
func SearchDestinations(ctx context.Context) []Destination {
	destinations, _ := ctx.Value(searchDestinationsKey{}).([]Destination)
	return destinations
}

// BindSearch enables both configured storage copies for an unqualified search.
func (r *Router) BindSearch(ctx context.Context) (context.Context, error) {
	if r.local == nil || !r.LoggedIn(ctx) {
		return ctx, nil
	}
	cloud, err := r.BindStorage(ctx, "cloud")
	if err != nil {
		return nil, err
	}
	d, _ := StorageDestination(cloud)
	if d.AccountID == "" {
		return nil, fmt.Errorf("select a cloud account before searching both storage locations: run knowledge accounts, then knowledge account use <id|slug>")
	}
	destinations := []Destination{{Storage: "local"}, d}
	errors := make([]error, len(destinations))
	var checks sync.WaitGroup
	for i, destination := range destinations {
		checks.Go(func() {
			leg := WithDestination(ctx, destination)
			backend, err := r.pick(leg)
			if err == nil {
				health := backend.health
				if backend.probe != nil {
					health = backend.probe
				}
				_, err = health.Check(leg, connect.NewRequest(&knowledgev1.CheckRequest{}))
			}
			if err != nil {
				errors[i] = fmt.Errorf("%s storage unavailable; search cannot be complete: %w", destination.Storage, err)
			}
		})
	}
	checks.Wait()
	for _, err := range errors {
		if err != nil {
			return nil, err
		}
	}
	return WithSearchDestinations(ctx, destinations), nil
}

func (r *Router) cloudForDestination(d Destination) *GraphClient {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.boundCloud == nil {
		r.boundCloud = make(map[Destination]*GraphClient)
	}
	if client := r.boundCloud[d]; client != nil {
		return client
	}
	client := newCloudGraphClient(r.cloudURL, r.tokenSource, &d)
	r.boundCloud[d] = client
	return client
}

// StorageDestination returns the operation's bound destination, if present.
func StorageDestination(ctx context.Context) (Destination, bool) {
	d, ok := ctx.Value(destinationKey{}).(Destination)
	return d, ok
}

// WithDestination carries an already resolved destination into a sub-operation.
func WithDestination(ctx context.Context, d Destination) context.Context {
	return context.WithValue(ctx, destinationKey{}, d)
}

// BindStorage resolves the default once; subsequent RPCs cannot follow a login flip.
func (r *Router) BindStorage(ctx context.Context, storage string) (context.Context, error) {
	if d, ok := StorageDestination(ctx); ok && (storage == "" || storage == d.Storage) {
		return ctx, nil
	}
	if storage == "" {
		storage = "local"
		if r.LoggedIn(ctx) {
			storage = "cloud"
		}
	}
	d := Destination{Storage: storage}
	switch storage {
	case "local":
		if r.local == nil {
			return nil, fmt.Errorf("local storage unavailable: %w", ErrNoBackend)
		}
	case "cloud":
		if !r.LoggedIn(ctx) {
			return nil, fmt.Errorf("cloud storage requires signing in")
		}
		id, err := auth.SelectedAccount().IDForRequest(ctx)
		if err != nil {
			return nil, err
		}
		d.AccountID = id
	default:
		return nil, fmt.Errorf("invalid storage %q: use local or cloud", storage)
	}
	return WithDestination(ctx, d), nil
}

// Reference is the durable, credential-free address returned by federated reads.
type Reference struct {
	Destination
	Graph    string `json:"graph"`
	Repo     string `json:"repo,omitempty"`
	Name     string `json:"name,omitempty"`
	Language string `json:"language,omitempty"`
	Branch   string `json:"branch,omitempty"`
	ID       string `json:"id"`
}

func (r Reference) validate() error {
	if r.Storage != "local" && r.Storage != "cloud" {
		return fmt.Errorf("reference has invalid storage")
	}
	if r.Graph == "" || r.ID == "" {
		return fmt.Errorf("reference requires graph and node ID")
	}
	if r.Storage == "cloud" && r.AccountID == "" {
		return fmt.Errorf("cloud reference requires account identity")
	}
	if r.Storage == "local" && r.AccountID != "" {
		return fmt.Errorf("local reference cannot carry a cloud account")
	}
	return nil
}

// Encode writes the current reference version. Version one remains readable.
func (r Reference) Encode() (string, error) {
	if err := r.validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return "kgref:2:" + string(b), nil
}

// ParseReference preserves legacy plain IDs and rejects malformed qualified IDs.
func ParseReference(s string) (Reference, bool, error) {
	if !strings.HasPrefix(s, "kgref:") {
		return Reference{ID: s}, false, nil
	}
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 || (parts[1] != "1" && parts[1] != "2") {
		return Reference{}, true, fmt.Errorf("unsupported reference version")
	}
	var r Reference
	dec := json.NewDecoder(strings.NewReader(parts[2]))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return Reference{}, true, fmt.Errorf("malformed reference: %w", err)
	}
	if !json.Valid([]byte(parts[2])) {
		return Reference{}, true, fmt.Errorf("malformed reference payload")
	}
	return r, true, r.validate()
}
