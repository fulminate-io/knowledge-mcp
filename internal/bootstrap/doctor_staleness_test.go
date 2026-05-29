// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"errors"
	"strings"
	"testing"
)

// TestAssembleStaleness covers the info/ok/warn branches of the code-index
// staleness message assembly via synthetic recorded values — no live server
// or git tree. assembleStaleness is the pure half of checkCodeStaleness.
func TestAssembleStaleness(t *testing.T) {
	cases := []struct {
		name       string
		when       string
		behind     int
		behindErr  error
		syncCommit string
		wantStatus checkStatus
		wantMsg    string
	}{
		{
			name:       "no recorded commit → info last-collected-only",
			when:       "2 hours ago",
			syncCommit: "",
			wantStatus: statusInfo,
			wantMsg:    "last collected 2 hours ago",
		},
		{
			name:       "commits-behind error → info with unavailable note",
			when:       "1 day ago",
			behindErr:  errors.New("unknown revision"),
			syncCommit: "abc123",
			wantStatus: statusInfo,
			wantMsg:    "commits-behind unavailable",
		},
		{
			name:       "zero behind → ok up to date",
			when:       "5 minutes ago",
			behind:     0,
			syncCommit: "abc123",
			wantStatus: statusOK,
			wantMsg:    "up to date",
		},
		{
			name:       "one behind → warn singular",
			when:       "3 days ago",
			behind:     1,
			syncCommit: "abc123",
			wantStatus: statusWarn,
			wantMsg:    "1 commit behind HEAD",
		},
		{
			name:       "many behind → warn plural",
			when:       "1 week ago",
			behind:     12,
			syncCommit: "abc123",
			wantStatus: statusWarn,
			wantMsg:    "12 commits behind HEAD",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := assembleStaleness("/repo", tc.when, tc.behind, tc.behindErr, tc.syncCommit)
			if got.status != tc.wantStatus {
				t.Fatalf("status = %v, want %v (msg=%q)", got.status, tc.wantStatus, got.msg)
			}
			if got.name != "code-index" {
				t.Fatalf("name = %q, want code-index", got.name)
			}
			if got.status == statusErr {
				t.Fatal("staleness check must never return statusErr")
			}
			if !strings.Contains(got.msg, tc.wantMsg) {
				t.Fatalf("msg = %q, want substring %q", got.msg, tc.wantMsg)
			}
		})
	}
}
