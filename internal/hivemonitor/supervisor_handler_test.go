// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import (
	"context"
	"errors"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// fakeJudge returns a scripted verdict (or error) for Judge.
type fakeJudge struct {
	verdict llmproviders.Verdict
	err     error
	calls   int
}

func (f *fakeJudge) Judge(_ context.Context, _ string) (llmproviders.Verdict, error) {
	f.calls++
	return f.verdict, f.err
}

// fakeResumer records ResumeRenew calls.
type fakeResumer struct {
	resumed []string
}

func (f *fakeResumer) ResumeRenew(msgID string) { f.resumed = append(f.resumed, msgID) }

// fakeBanner records Ban calls.
type fakeBanner struct {
	banned []string
}

func (f *fakeBanner) Ban(harnessSessionID string) { f.banned = append(f.banned, harnessSessionID) }

// handlerTranscript writes a small claude transcript and returns a resolved
// handle for it (so FormatTranscript yields non-empty text — never the error
// path) with the given harness id.
func handlerTranscript(t *testing.T, harnessID string) TranscriptHandle {
	t.Helper()
	path := writeTranscript(t,
		claudeAssistantText(),
		claudeAssistantToolUse("toolu_1", "Bash"),
	)
	return TranscriptHandle{Path: path, HarnessSessionID: harnessID, Format: FormatClaude}
}

func TestSupervisorHandler_VerdictActionMatrix(t *testing.T) {
	const (
		harnessID = "harness-abc"
		hiveName  = "hive1"
		msgID     = "m1"
	)
	claim := Claim{MsgID: msgID, Hive: hiveName}

	tests := []struct {
		name       string
		verdict    llmproviders.Verdict
		judgeErr   error
		formatErr  bool // drive the transcript-format error path
		wantOp     knowledgev1.HiveOp
		wantHiveOp bool // a Hive RPC is expected
		wantBan    bool
		wantResume bool
	}{
		{
			name:       "done high-confidence acks on behalf",
			verdict:    llmproviders.Verdict{State: "done", Confidence: 0.9, Result: "shipped"},
			wantOp:     knowledgev1.HiveOp_HIVE_OP_ACK,
			wantHiveOp: true,
		},
		{
			name:       "stuck high-confidence evicts + bans",
			verdict:    llmproviders.Verdict{State: "stuck", Confidence: 0.95, Reason: "looping"},
			wantOp:     knowledgev1.HiveOp_HIVE_OP_EVICT,
			wantHiveOp: true,
			wantBan:    true,
		},
		{
			name:       "off-rails high-confidence evicts + bans",
			verdict:    llmproviders.Verdict{State: "off-rails", Confidence: 0.9, Reason: "wrong task"},
			wantOp:     knowledgev1.HiveOp_HIVE_OP_EVICT,
			wantHiveOp: true,
			wantBan:    true,
		},
		{
			name:       "working resumes only",
			verdict:    llmproviders.Verdict{State: "working", Confidence: 0.99},
			wantResume: true,
		},
		{
			name:       "done LOW-confidence resumes only (no reclaim on uncertainty)",
			verdict:    llmproviders.Verdict{State: "done", Confidence: 0.5, Result: "maybe"},
			wantResume: true,
		},
		{
			name:       "stuck LOW-confidence resumes only",
			verdict:    llmproviders.Verdict{State: "stuck", Confidence: 0.6},
			wantResume: true,
		},
		{
			name:       "judge error resumes only",
			judgeErr:   errors.New("llm down"),
			wantResume: true,
		},
		{
			name:       "format error resumes only",
			formatErr:  true,
			wantResume: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hive := &fakeHive{}
			ban := &fakeBanner{}
			resume := &fakeResumer{}
			judge := &fakeJudge{verdict: tc.verdict, err: tc.judgeErr}
			h := NewSupervisorHandler(hive, ban, resume, judge)

			handle := handlerTranscript(t, harnessID)
			if tc.formatErr {
				// An unresolvable/empty format → FormatTranscript errors on unknown format.
				handle = TranscriptHandle{Path: handle.Path, HarnessSessionID: harnessID, Format: "bogus"}
			}

			h.Handle(context.Background(), claim, "mcp-sess", StateIdle, handle)

			// Hive op expectation.
			if tc.wantHiveOp {
				if hive.count() != 1 {
					t.Fatalf("Hive calls = %d, want 1", hive.count())
				}
				req := hive.last()
				if req.GetOp() != tc.wantOp {
					t.Errorf("Hive op = %v, want %v", req.GetOp(), tc.wantOp)
				}
				if req.GetMemberSession() != harnessID {
					t.Errorf("MemberSession = %q, want %q (harness)", req.GetMemberSession(), harnessID)
				}
				if tc.wantOp == knowledgev1.HiveOp_HIVE_OP_ACK {
					if req.GetMsgId() != msgID {
						t.Errorf("ACK MsgId = %q, want %q", req.GetMsgId(), msgID)
					}
					if req.GetResult() != tc.verdict.Result {
						t.Errorf("ACK Result = %q, want %q (lifted from verdict)", req.GetResult(), tc.verdict.Result)
					}
				}
				if tc.wantOp == knowledgev1.HiveOp_HIVE_OP_EVICT && req.GetReason() != tc.verdict.Reason {
					t.Errorf("EVICT Reason = %q, want %q (verdict reason)", req.GetReason(), tc.verdict.Reason)
				}
			} else if hive.count() != 0 {
				t.Fatalf("Hive calls = %d, want 0 (no Hive op on this verdict)", hive.count())
			}

			// Ban expectation.
			if tc.wantBan {
				if len(ban.banned) != 1 || ban.banned[0] != harnessID {
					t.Errorf("Ban = %v, want [%q]", ban.banned, harnessID)
				}
			} else if len(ban.banned) != 0 {
				t.Errorf("Ban = %v, want none", ban.banned)
			}

			// Resume expectation.
			if tc.wantResume {
				if len(resume.resumed) != 1 || resume.resumed[0] != msgID {
					t.Errorf("ResumeRenew = %v, want [%q]", resume.resumed, msgID)
				}
			} else if len(resume.resumed) != 0 {
				t.Errorf("ResumeRenew = %v, want none (terminal action, not resume)", resume.resumed)
			}
		})
	}
}
