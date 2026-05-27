// SPDX-License-Identifier: Apache-2.0

package dispatch

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// fakeBackend is a minimal scripted-response backends.Backend impl for
// dispatch tests. Records received args; returns canned errs.
type fakeBackend struct {
	name string

	updateProjErr  error
	updateTktErr   error
	archiveProjErr error
	archiveTktErr  error

	updateProjCalls  int
	updateTktCalls   int
	archiveProjCalls int
	archiveTktCalls  int

	lastUpdateProjRef backends.RemoteRef
	lastUpdateTktRef  backends.RemoteRef
	lastArchiveRef    backends.RemoteRef
}

func (f *fakeBackend) Name() string                                       { return f.name }
func (f *fakeBackend) Groups(_ context.Context) ([]backends.Group, error) { return nil, nil }
func (f *fakeBackend) SyncGroup(_ context.Context, _ string) (backends.Snapshot, error) {
	return backends.Snapshot{}, nil
}
func (f *fakeBackend) CreateProject(_ context.Context, _ backends.ProjectCreateArgs) (backends.RemoteRef, error) {
	return backends.RemoteRef{}, nil
}
func (f *fakeBackend) UpdateProject(_ context.Context, ref backends.RemoteRef, _ backends.ProjectDiff) error {
	f.updateProjCalls++
	f.lastUpdateProjRef = ref
	return f.updateProjErr
}
func (f *fakeBackend) ArchiveProject(_ context.Context, ref backends.RemoteRef) error {
	f.archiveProjCalls++
	f.lastArchiveRef = ref
	return f.archiveProjErr
}
func (f *fakeBackend) CreateTicket(_ context.Context, _ backends.TicketCreateArgs) (backends.RemoteRef, error) {
	return backends.RemoteRef{}, nil
}
func (f *fakeBackend) UpdateTicket(_ context.Context, ref backends.RemoteRef, _ backends.TicketDiff) error {
	f.updateTktCalls++
	f.lastUpdateTktRef = ref
	return f.updateTktErr
}
func (f *fakeBackend) ArchiveTicket(_ context.Context, ref backends.RemoteRef) error {
	f.archiveTktCalls++
	f.lastArchiveRef = ref
	return f.archiveTktErr
}

const fakeBackendName = "genericBackend"

func makeBackendProject() *knowledgev1.Node {
	n := &knowledgev1.Node{Type: string(kgtypes.NodeProject), SymbolName: "p"}
	kgtypes.SetValue(n, "backend", fakeBackendName)
	kgtypes.SetValue(n, "external_url", "https://example.invalid/p/proj_uuid")
	kgtypes.SetValue(n, fakeBackendName+"_id", "proj_uuid")
	return n
}

func makeBackendTicket() *knowledgev1.Node {
	n := &knowledgev1.Node{Type: string(kgtypes.NodeTicket), SymbolName: "t"}
	kgtypes.SetValue(n, "backend", fakeBackendName)
	kgtypes.SetValue(n, "external_url", "https://example.invalid/t/tkt_uuid")
	kgtypes.SetValue(n, fakeBackendName+"_id", "tkt_uuid")
	return n
}

func TestUpdate_Project_Success(t *testing.T) {
	fb := &fakeBackend{name: fakeBackendName}
	err := Update(context.Background(), makeBackendProject(), fakeBackendName, fb, UpdateArgs{NodeID: "irrelevant"})
	require.NoError(t, err)
	assert.Equal(t, 1, fb.updateProjCalls)
	assert.Equal(t, "proj_uuid", fb.lastUpdateProjRef.ID)
	assert.Equal(t, "https://example.invalid/p/proj_uuid", fb.lastUpdateProjRef.URL)
}

func TestUpdate_Ticket_Success(t *testing.T) {
	fb := &fakeBackend{name: fakeBackendName}
	err := Update(context.Background(), makeBackendTicket(), fakeBackendName, fb, UpdateArgs{NodeID: "irrelevant"})
	require.NoError(t, err)
	assert.Equal(t, 1, fb.updateTktCalls)
	assert.Equal(t, "tkt_uuid", fb.lastUpdateTktRef.ID)
}

func TestUpdate_PropagatesBackendError(t *testing.T) {
	wantErr := errors.New("linear: bad token")
	fb := &fakeBackend{name: fakeBackendName, updateProjErr: wantErr}
	err := Update(context.Background(), makeBackendProject(), fakeBackendName, fb, UpdateArgs{})
	require.ErrorIs(t, err, wantErr)
}

func TestUpdate_SkipsNonProjectNonTicket(t *testing.T) {
	fb := &fakeBackend{name: fakeBackendName}
	n := &knowledgev1.Node{Type: string(kgtypes.NodePlan), SymbolName: "plan"}
	kgtypes.SetValue(n, "backend", fakeBackendName)
	err := Update(context.Background(), n, fakeBackendName, fb, UpdateArgs{})
	require.NoError(t, err)
	assert.Zero(t, fb.updateProjCalls)
	assert.Zero(t, fb.updateTktCalls)
}

func TestArchive_Project_Success(t *testing.T) {
	fb := &fakeBackend{name: fakeBackendName}
	err := Archive(context.Background(), makeBackendProject(), fakeBackendName, fb, DeleteArgs{NodeID: "irrelevant"})
	require.NoError(t, err)
	assert.Equal(t, 1, fb.archiveProjCalls)
	assert.Equal(t, "proj_uuid", fb.lastArchiveRef.ID)
}

func TestArchive_Ticket_Success(t *testing.T) {
	fb := &fakeBackend{name: fakeBackendName}
	err := Archive(context.Background(), makeBackendTicket(), fakeBackendName, fb, DeleteArgs{NodeID: "irrelevant"})
	require.NoError(t, err)
	assert.Equal(t, 1, fb.archiveTktCalls)
	assert.Equal(t, "tkt_uuid", fb.lastArchiveRef.ID)
}

func TestArchive_PropagatesBackendError(t *testing.T) {
	wantErr := errors.New("linear: not found")
	fb := &fakeBackend{name: fakeBackendName, archiveTktErr: wantErr}
	err := Archive(context.Background(), makeBackendTicket(), fakeBackendName, fb, DeleteArgs{})
	require.ErrorIs(t, err, wantErr)
}

func TestArchive_SkipsNonProjectNonTicket(t *testing.T) {
	fb := &fakeBackend{name: fakeBackendName}
	n := &knowledgev1.Node{Type: string(kgtypes.NodeStep), SymbolName: "step"}
	err := Archive(context.Background(), n, fakeBackendName, fb, DeleteArgs{})
	require.NoError(t, err)
	assert.Zero(t, fb.archiveProjCalls)
	assert.Zero(t, fb.archiveTktCalls)
}
