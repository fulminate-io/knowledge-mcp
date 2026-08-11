// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import (
	"context"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// confidenceThreshold is the minimum verdict confidence at which the supervisor
// acts TERMINALLY (ack-on-behalf or evict). Below it, every verdict — including
// done/stuck/off-rails — degrades to the conservative resume-renew path. v1
// constant (0.8); promotable to config later.
const confidenceThreshold = 0.8

// supervisorTranscriptBytes bounds how much of the worker transcript tail is
// rendered for the judge — enough recent context to classify progress without an
// unbounded LLM input.
const supervisorTranscriptBytes = 32 * 1024

// Judge is the local seam the SupervisorHandler dispatches through: it returns a
// verdict for a formatted transcript. The concrete llmproviders.Supervisor
// satisfies it; tests inject a fake. It returns llmproviders.Verdict directly
// (not a local mirror) because Go interface satisfaction is by exact type — a
// mirror would mean the concrete Supervisor no longer satisfied this seam. There
// is no import cycle (llmproviders does not import hivemonitor).
type Judge interface {
	Judge(ctx context.Context, transcript string) (llmproviders.Verdict, error)
}

// banner is the local seam for marking a harness session-id banned (the *BanSet
// satisfies it). Kept narrow so the handler's eviction path is testable with a
// fake.
type banner interface {
	Ban(harnessSessionID string)
}

// resumer is the local seam for un-escalating a claim so renewal resumes (the
// *Monitor's ResumeRenew satisfies it).
type resumer interface {
	ResumeRenew(msgID string)
}

// SupervisorHandler is the Tier-2 escalation callback: on monitor ambiguity it
// formats the worker transcript, asks the Judge for a verdict, and drives the
// VERDICT→ACTION matrix with a cost-asymmetry bias — terminal action only on a
// high-confidence done / stuck / off-rails verdict; every other outcome (still
// working, low confidence, or a format/judge error) resumes renewing and NEVER
// reclaims on uncertainty.
//
// It owns NO cloud logic: it CALLS the injected HiveCaller (the Router) for
// HIVE_OP_ACK / HIVE_OP_EVICT, never reimplementing the op.
type SupervisorHandler struct {
	hive   HiveCaller
	ban    banner
	resume resumer
	judge  Judge
}

// NewSupervisorHandler builds the handler from its four seams. Production wires
// hive=the daemon's stamping hive caller, ban=c.banSet, resume=monitor.ResumeRenew's
// receiver, judge=the llmproviders.Supervisor; tests inject fakes.
func NewSupervisorHandler(hive HiveCaller, ban banner, resume resumer, judge Judge) *SupervisorHandler {
	return &SupervisorHandler{hive: hive, ban: ban, resume: resume, judge: judge}
}

// Handle is the EscalationFunc the monitor invokes once per escalated claim. It
// formats the transcript, judges it, and dispatches on the verdict:
//
//   - done   + confidence>=threshold → HIVE_OP_ACK on behalf of the worker
//     (MsgId=claim.MsgID, Result lifted from the verdict, MemberSession=the
//     harness session-id so the cloud ack-on-behalf arm accepts the daemon caller).
//   - stuck / off-rails + confidence>=threshold → HIVE_OP_EVICT (Reason from the
//     verdict) + local Ban of the harness id. Terminal DNF — no recycle.
//   - everything else (still working, any low-confidence verdict, or a
//     format/judge error) → ResumeRenew only, no Hive op. Conservative: the lease
//     stays alive; uncertainty never reclaims.
func (h *SupervisorHandler) Handle(ctx context.Context, claim Claim, sessionID string, state LivenessState, handle TranscriptHandle) {
	transcript, err := FormatTranscript(handle, supervisorTranscriptBytes)
	if err != nil {
		slog.Warn("hive supervisor: transcript format failed; resuming renew (conservative)",
			"session", sessionID, "msg", claim.MsgID, "error", err)
		h.resume.ResumeRenew(claim.MsgID)
		return
	}

	verdict, err := h.judge.Judge(ctx, transcript)
	if err != nil {
		slog.Warn("hive supervisor: judge failed; resuming renew (conservative)",
			"session", sessionID, "msg", claim.MsgID, "error", err)
		h.resume.ResumeRenew(claim.MsgID)
		return
	}

	switch {
	case verdict.State == "done" && verdict.Confidence >= confidenceThreshold:
		h.ackOnBehalf(ctx, claim, handle, verdict)
	case (verdict.State == "stuck" || verdict.State == "off-rails") && verdict.Confidence >= confidenceThreshold:
		h.evict(ctx, claim, handle, verdict)
	default:
		// working, or any low-confidence verdict — keep the lease alive. Log only
		// when the verdict was terminal-but-uncertain (a low-conf done/stuck/
		// off-rails) so the operator can see near-misses; a plain "working" verdict
		// resumes silently.
		if verdict.State != "working" {
			slog.Warn("hive supervisor: low-confidence verdict; resuming renew (conservative)",
				"session", sessionID, "msg", claim.MsgID, "state", verdict.State, "confidence", verdict.Confidence)
		}
		h.resume.ResumeRenew(claim.MsgID)
	}
}

// ackOnBehalf issues one HIVE_OP_ACK for the worker's claim, lifting the verdict
// Result as the completion result and passing the harness session-id as
// member_session so the cloud daemon arm acks on the worker's behalf.
func (h *SupervisorHandler) ackOnBehalf(ctx context.Context, claim Claim, handle TranscriptHandle, v llmproviders.Verdict) {
	_, err := h.hive.Hive(ctx, &knowledgev1.HiveRequest{
		Op:            knowledgev1.HiveOp_HIVE_OP_ACK,
		Target:        &knowledgev1.GraphSelector{Graph: "knowledge"},
		Hive:          claim.Hive,
		MsgId:         claim.MsgID,
		Result:        v.Result,
		MemberSession: handle.HarnessSessionID,
	})
	if err != nil {
		slog.Warn("hive supervisor: ack-on-behalf failed", "hive", claim.Hive, "msg", claim.MsgID, "error", err)
		return
	}
	slog.Info("hive supervisor: acked done on behalf of worker",
		"hive", claim.Hive, "msg", claim.MsgID, "member", handle.HarnessSessionID)
}

// evict issues one HIVE_OP_EVICT carrying the verdict Reason and bans the harness
// session-id locally. Terminal DNF — the worker is NOT recycled.
func (h *SupervisorHandler) evict(ctx context.Context, claim Claim, handle TranscriptHandle, v llmproviders.Verdict) {
	_, err := h.hive.Hive(ctx, &knowledgev1.HiveRequest{
		Op:            knowledgev1.HiveOp_HIVE_OP_EVICT,
		Target:        &knowledgev1.GraphSelector{Graph: "knowledge"},
		Hive:          claim.Hive,
		MemberSession: handle.HarnessSessionID,
		Reason:        v.Reason,
	})
	if err != nil {
		slog.Warn("hive supervisor: evict failed", "hive", claim.Hive, "member", handle.HarnessSessionID, "error", err)
		// Ban locally even if the cloud evict RPC failed — the local gate must
		// still reject this harness id.
	}
	h.ban.Ban(handle.HarnessSessionID)
	slog.Info("hive supervisor: evicted worker (terminal DNF, no recycle)",
		"hive", claim.Hive, "member", handle.HarnessSessionID, "state", v.State, "reason", v.Reason)
}
