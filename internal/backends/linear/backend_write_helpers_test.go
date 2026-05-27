// SPDX-License-Identifier: Apache-2.0

package linear

import (
	"context"
	"errors"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
)

// TestEnsureLabels_NilTeamEmptyList exercises the FIRST-line return-fast
// contract: ensureLabels(ctx, nil, "") MUST return (nil, nil) without
// touching `team` (would nil-panic). This is the legitimate
// CreateProject-without-labels path.
func TestEnsureLabels_NilTeamEmptyList(t *testing.T) {
	b := &Backend{} // no Client needed; the empty path doesn't issue HTTP
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ensureLabels(nil, \"\") panicked: %v — empty-list return-fast contract violated", r)
		}
	}()
	got, err := b.ensureLabels(context.Background(), nil, "")
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if got != nil {
		t.Errorf("result = %v, want nil", got)
	}
}

// TestEnsureLabels_NilTeamWithLabels_ReturnsError exercises the
// defensive secondary check: non-empty list with nil team returns
// wrapped ErrInvalidArgument (programmer-error path; wrapped, NOT
// panicked).
func TestEnsureLabels_NilTeamWithLabels_ReturnsError(t *testing.T) {
	b := &Backend{}
	got, err := b.ensureLabels(context.Background(), nil, "foo,bar")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("errors.Is(err, ErrInvalidArgument) = false, want true. err = %v", err)
	}
	if got != nil {
		t.Errorf("result = %v, want nil", got)
	}
	// T4 Phase 0 retrofit: typed wrap surfaces ReasonInvalidArgument terminal.
	var be *backends.Error
	if !errors.As(err, &be) {
		t.Fatalf("errors.As did not find *backends.Error: %v", err)
	}
	if be.Reason != backends.ReasonInvalidArgument || be.Transient {
		t.Errorf("typed wrap misclassified: Reason=%q Transient=%v, want %q/false",
			be.Reason, be.Transient, backends.ReasonInvalidArgument)
	}
}

// TestResolveStatus_EmptyReturnsEmpty — the documented "drop the field"
// path: empty status string returns ("", nil) without touching the team.
// Caller relies on this to avoid sending an empty stateId.
func TestResolveStatus_EmptyReturnsEmpty(t *testing.T) {
	b := &Backend{}
	id, err := b.resolveStatus(nil, "", "ABC")
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if id != "" {
		t.Errorf("id = %q, want empty", id)
	}
}

// TestResolveStatus_UnknownReturnsErrUnknownState — non-empty status
// not present on the team's State map returns *ErrUnknownState with the
// given group key (issue path: GroupKey is non-empty).
func TestResolveStatus_UnknownReturnsErrUnknownState(t *testing.T) {
	b := &Backend{}
	team := &resolvedTeam{
		ID:     "team_uuid_1",
		Key:    "ABC",
		States: map[string]string{"Todo": "state_uuid_1"},
	}
	_, err := b.resolveStatus(team, "Done", "ABC")
	var u *ErrUnknownState
	if !errors.As(err, &u) {
		t.Fatalf("err = %v, want *ErrUnknownState", err)
	}
	if u.GroupKey != "ABC" || u.State != "Done" {
		t.Errorf("err = %+v, want {GroupKey:ABC State:Done}", u)
	}
	// T4 Phase 0 retrofit: typed wrap surfaces ReasonUnknownState terminal.
	var be *backends.Error
	if !errors.As(err, &be) {
		t.Fatalf("errors.As did not find *backends.Error: %v", err)
	}
	if be.Reason != backends.ReasonUnknownState || be.Transient {
		t.Errorf("typed wrap misclassified: Reason=%q Transient=%v, want %q/false",
			be.Reason, be.Transient, backends.ReasonUnknownState)
	}
}

// TestIsInvalidEnumError — table-driven unit test for the predicate
// that gates UpdateProject's invalid-enum rewrap. Positive case:
// canonical Linear shape with state-in-message + structural marker.
// Negative cases: auth-shaped, transport-shaped, 5xx-shaped, nil,
// missing state, missing marker. The non-misclassification regression
// guards over the wire (PreservesUnauthorized, 5xxNotMisclassified,
// TransportErrorNotMisclassified) live in
// backend_write_project_test.go; this file focuses on the predicate
// in isolation.
func TestIsInvalidEnumError(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		state string
		want  bool
	}{
		{
			name:  "canonical_invalid_value",
			err:   errors.New(`Variable '$input' got invalid value "completed"; Expected type 'ProjectStatusType'`),
			state: "completed",
			want:  true,
		},
		{
			name:  "auth_shaped_returns_false",
			err:   ErrUnauthorized,
			state: "completed",
			want:  false,
		},
		{
			name:  "transport_shaped_returns_false",
			err:   errors.New("connection refused"),
			state: "completed",
			want:  false,
		},
		{
			name:  "5xx_shaped_returns_false",
			err:   errors.New("503 service unavailable"),
			state: "completed",
			want:  false,
		},
		{
			name:  "nil_err_returns_false",
			err:   nil,
			state: "completed",
			want:  false,
		},
		{
			name:  "empty_state_returns_false",
			err:   errors.New(`got invalid value "completed"`),
			state: "",
			want:  false,
		},
		{
			name:  "marker_present_but_state_missing_returns_false",
			err:   errors.New("invalid value somewhere unrelated"),
			state: "completed",
			want:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isInvalidEnumError(tc.err, tc.state); got != tc.want {
				t.Errorf("isInvalidEnumError(%v, %q) = %v, want %v", tc.err, tc.state, got, tc.want)
			}
		})
	}
}

// TestNormalizeTeam — sanity-check the wire-shape → name-map flatten
// inside resolveTeamByKey/ByID. Direct unit on the helper avoids
// going through HTTP for the trivial case.
func TestNormalizeTeam(t *testing.T) {
	wire := &teamWithStatesLabels{
		ID:  "team_uuid_1",
		Key: "ABC",
	}
	wire.States.Nodes = []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}{
		{ID: "state_uuid_1", Name: "Todo"},
		{ID: "state_uuid_2", Name: "Done"},
	}
	wire.Labels.Nodes = []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}{
		{ID: "label_uuid_1", Name: "bug"},
	}
	got := normalizeTeam(wire)
	if got.ID != "team_uuid_1" || got.Key != "ABC" {
		t.Errorf("got.{ID,Key} = %q,%q, want team_uuid_1,ABC", got.ID, got.Key)
	}
	if got.States["Todo"] != "state_uuid_1" || got.States["Done"] != "state_uuid_2" {
		t.Errorf("States map mismatch: %+v", got.States)
	}
	if got.Labels["bug"] != "label_uuid_1" {
		t.Errorf("Labels map mismatch: %+v", got.Labels)
	}
}
