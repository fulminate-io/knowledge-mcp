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

// BindSearch selects the storage copies an unqualified search reads.
//
// THE TWO STORES ARE NOT PEERS, and the asymmetry is the whole of this
// function. The cloud store is where a signed-in user's knowledge lives, so it
// is REQUIRED: unreachable or unauthenticated, the search fails rather than
// report a partial result as complete. The local store is OPTIONAL — a user
// runs one to hold graphs they choose not to share with the cloud, the way a
// repository can stay unpushed — so not running one is a choice and not a
// fault. When its health check fails, the local store is left out of the
// destination list and the search reads cloud alone, with nothing appended to
// the result: the bound list is then exactly the single-destination list an
// explicit storage:"cloud" call binds, so the two answers are identical.
//
// A logged-out client, or one with no local client configured at all, is
// untouched here: it keeps its own single store and the caller's own gates
// report that store's absence.
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
	local := Destination{Storage: "local"}
	var localErr, cloudErr error
	var checks sync.WaitGroup
	checks.Go(func() { localErr = r.checkDestination(ctx, local) })
	checks.Go(func() { cloudErr = r.checkDestination(ctx, d) })
	checks.Wait()
	if cloudErr != nil {
		return nil, fmt.Errorf("%s storage unavailable; search cannot be complete: %w", d.Storage, cloudErr)
	}
	if localErr != nil {
		// The local store is OPTIONAL: its health failure is deliberately not
		// the search's failure — the search reads the cloud store alone.
		return WithSearchDestinations(ctx, []Destination{d}), nil //nolint:nilerr // a store the user chose not to run is not the caller's error; see the doc comment
	}
	return WithSearchDestinations(ctx, []Destination{local, d}), nil
}

// checkDestination reports whether the backend serving one destination is
// reachable and healthy, on that destination's own bound context.
func (r *Router) checkDestination(ctx context.Context, d Destination) error {
	leg := WithDestination(ctx, d)
	backend, err := r.pick(leg)
	if err != nil {
		return err
	}
	health := backend.health
	if backend.probe != nil {
		health = backend.probe
	}
	_, err = health.Check(leg, connect.NewRequest(&knowledgev1.CheckRequest{}))
	return err
}

// MaxBoundDestinations caps the per-destination structures a daemon keeps: the
// Router's cloud clients here, and the segment manager's per-destination
// children (segmentdist.Manager.ForDestination), which read this same constant.
//
// THE KEY SPACE IS CALLER-DRIVEN. A /mcp request names the account it is for,
// so the set of destinations a daemon is asked to hold is whatever its callers
// send, not the one account it selected. Without a cap, a caller sending ten
// thousand distinct account ids would leave ten thousand clients and ten
// thousand cache directories behind, each created BEFORE the gateway ever gets
// to refuse the account. The cap is small on purpose: holding more than a
// handful of accounts at once is not a shape any caller has, and the only cost
// of an eviction is rebuilding a client on the next call for that account.
const MaxBoundDestinations = 32

// cloudForDestination returns the cloud client for one destination, building it
// on first use and evicting the least recently used destination when the cache
// is full. ctx is read for the SELECTED account, whose client is never the one
// evicted — the daemon's own work routes through it.
func (r *Router) cloudForDestination(ctx context.Context, d Destination) *GraphClient {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.boundCloud == nil {
		r.boundCloud = make(map[Destination]*GraphClient)
		r.boundUse = make(map[Destination]uint64)
	}
	if client := r.boundCloud[d]; client != nil {
		r.markBoundUseLocked(d)
		return client
	}
	r.evictBoundCloudLocked(ctx, d)
	client := newCloudGraphClient(r.cloudURL, r.tokenSource, &d)
	r.boundCloud[d] = client
	r.markBoundUseLocked(d)
	return client
}

// evictBoundCloudLocked makes room for one more destination, releasing the
// IDLE connections of whatever it drops.
//
// IDLE, never Close: a request already dispatched through an evicted client
// must still complete. GraphClient.Close's own doc comment says what the other
// choice means — "a connection closed here is closed under an in-flight
// request, which surfaces to that request as a transport error" — and a cache
// bound is no reason to fail a call the caller is already waiting on. The
// evicted client's remaining connections go when it does.
//
// WHAT IS NEVER A VICTIM mirrors the segment cache's rule (see
// segmentdist.Manager.evictableLocked): the cap bounds CALLER-DRIVEN key space,
// which here is a cloud destination carrying an account id. The destination
// being bound right now, the SELECTED account's, and the ACCOUNTLESS cloud
// binding the daemon's own machine-auth work runs on belong to the daemon and
// are kept. When nothing else is left to drop, the map is allowed to stand —
// refusing a request to stay under a cache bound would trade a bounded resource
// for a broken one. Caller must hold r.mu.
//
// THE KEEP-RULE IS STATED TWICE — here and in
// segmentdist.Manager.evictableLocked — and the twins change together. It is
// not factored into one predicate because the dependency runs one way:
// segmentdist imports graphclient, never the reverse, so a shared helper would
// have to live here and take a segmentdist-shaped argument, a worse seam than
// two sites that name each other. The condition below is the same sentence its
// twin spells out: caller-driven means cloud AND carrying an account id.
func (r *Router) evictBoundCloudLocked(ctx context.Context, incoming Destination) {
	selected := Destination{Storage: "cloud", AccountID: r.SelectedAccountID(ctx)}
	for len(r.boundCloud) >= MaxBoundDestinations {
		var victim Destination
		var oldest uint64
		found := false
		for d := range r.boundCloud {
			if d == incoming || d == selected || d.Storage != "cloud" || d.AccountID == "" {
				continue
			}
			if use := r.boundUse[d]; !found || use < oldest {
				victim, oldest, found = d, use, true
			}
		}
		if !found {
			return
		}
		r.boundCloud[victim].CloseIdleConnections()
		delete(r.boundCloud, victim)
		delete(r.boundUse, victim)
	}
}

// markBoundUseLocked stamps a destination as the most recently used. Caller
// must hold r.mu.
func (r *Router) markBoundUseLocked(d Destination) {
	r.boundTick++
	r.boundUse[d] = r.boundTick
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
			return nil, loggedOutCloudError(ctx)
		}
		// THE LADDER, not the machine-wide selection: header, then the calling
		// harness session's binding, then the selection (auth/request_account.go).
		// This is the one place an account enters a Destination on an unbound
		// cloud bind, so binding it here is what makes every downstream
		// consumer — the interceptor stamp, the per-destination cloud client,
		// the per-{storage,account} segment child — follow the same answer.
		id, _, err := auth.RequestAccount(ctx)
		if err != nil {
			return nil, err
		}
		d.AccountID = id
	default:
		return nil, fmt.Errorf("invalid storage %q: use local or cloud", storage)
	}
	return WithDestination(ctx, d), nil
}

// loggedOutCloudError explains a cloud call from a daemon with no login, naming
// the account header when the caller sent one.
//
// A header NAMES an account; it never grants one. A browser page that sent an
// account id and got "cloud storage requires signing in" back would be left to
// guess whether the account was wrong or the daemon was logged out, so the
// header-bound arm says which — and the unbound arm keeps the text it has
// always had rather than naming a header nobody sent.
func loggedOutCloudError(ctx context.Context) error {
	if requestAccountHeader(ctx) != "" {
		return fmt.Errorf(
			"cloud storage requires signing in: the %s header names an account but cannot create a cloud session — run `knowledge login` on this daemon",
			auth.AccountHeaderName)
	}
	return fmt.Errorf("cloud storage requires signing in")
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
