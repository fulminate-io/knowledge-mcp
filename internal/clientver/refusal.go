// SPDX-License-Identifier: Apache-2.0

package clientver

import (
	"sync"
	"time"
)

// The reason vocabulary the gateway emits on a refusal. These spellings are a
// CROSS-REPO CONTRACT with the agent gateway ticket that owns the version gate;
// they are declared here so this repo's renderers and tests name them once
// rather than spelling string literals at every site.
//
//   - ReasonBelowMinimum: the claimed version parses and is older than the
//     configured minimum.
//   - ReasonHeaderAbsent: the request carried no client-version header at all,
//     which is what a client predating the header looks like.
//   - ReasonUnparseable: the claimed version is neither a version nor a
//     recognized development stamp. THE GATEWAY'S VERDICT ON THIS CLIENT'S
//     VERSION, and nothing else — see ReasonRefusalBodyUnparseable below.
//   - ReasonDevStampNotAllowlisted: a development stamp that the gateway does
//     not allow.
//   - ReasonUnverified: no live verification record — an expired one, a failed
//     proof, or a proof that never ran.
//   - ReasonUnprovable: no obtainable release artifact for the claimed version
//     and platform, so possession cannot be established either way.
//
// THIS LIST IS NOT A CLOSED SET AND MUST NOT BE TREATED AS ONE. The vocabulary
// lives in another repo with its own release cadence, so the gateway can add a
// reason at any time without a release here. An unrecognized reason is latched
// VERBATIM: a client that dropped it would report a refusal it could not
// explain, leaving a blocked user with no remedy. An unrecognized reason is
// still a refusal — never an admission.
const (
	ReasonBelowMinimum           = "below_minimum"
	ReasonHeaderAbsent           = "version_header_absent"
	ReasonUnparseable            = "version_unparseable"
	ReasonDevStampNotAllowlisted = "dev_stamp_not_allowlisted"
	ReasonUnverified             = "version_unverified"
	ReasonUnprovable             = "version_unprovable"
)

// ReasonRefusalBodyUnparseable is THIS CLIENT'S OWN marker for a refusal whose
// body it could not read. It is never sent by the gateway and never appears in
// the vocabulary above.
//
// IT EXISTS BECAUSE IT USED TO BE ReasonUnparseable, AND THE OVERLOAD MISLED A
// READER WITH REAL EVIDENCE IN FRONT OF THEM. One string meant two opposite
// things — "the gateway could not parse the version this client claimed" and
// "this client could not parse the gateway's refusal" — and the first is the one
// a reader assumes. A refused client reported version_unparseable while the
// gateway had in fact answered below_minimum with a perfectly formed body, and
// the reported reason sent the investigation into the gateway's comparator,
// which was correct all along. The two facts now have two names, and the one
// that means "the fault is on this side" says so.
//
// A refusal latched under this reason carries a [Refusal.Diagnostic] naming the
// transport, the encoding, the cause and an excerpt of what arrived — the four
// things that turn "unreadable" into a one-line diagnosis.
const ReasonRefusalBodyUnparseable = "refusal_body_unparseable"

// Refusal is what the gateway last told this client when it refused a
// cloud-bound request over the client's version.
//
// Reason carries the gateway's own reason string verbatim, recognized or not.
// UpgradeCommand is the remedy the gateway names.
//
// Diagnostic is set ONLY when Reason is [ReasonRefusalBodyUnparseable] — that
// is, only when the fault is on this side. It names the transport that lost the
// body, the content encoding the body claimed, why the read failed and a bounded
// excerpt of what arrived. On a refusal the gateway explained it stays empty,
// because there is nothing on this side to explain.
type Refusal struct {
	Minimum        string
	ClientVersion  string
	Platform       string
	UpgradeCommand string
	Reason         string
	Diagnostic     string
	At             time.Time
}

// ProofState is the outcome of this client's most recent possession-proof
// attempt against the gateway.
//
// It is deliberately SEPARATE state from [Refusal], and conflating the two is
// the defect to avoid: a refusal is what the gateway said, a proof state is
// what this client's own loop did. A refusal beside a SUCCESSFUL proof means
// the version is genuinely too old and upgrading is the remedy; a refusal
// beside a FAILED proof means the proof itself is broken and upgrading will not
// help. Rendering only one sends a user to the wrong remedy.
type ProofState struct {
	At       time.Time
	OK       bool
	Version  string
	Platform string
	Err      string
}

// versionState holds both records under ONE lock. They are read together by
// the status renderers, and a reader that took one without the other could
// render a self-contradictory status.
var versionState struct {
	mu sync.RWMutex

	refusal    Refusal
	hasRefusal bool

	proof    ProofState
	hasProof bool
}

// LatchRefusal records the gateway's refusal process-wide. At is stamped here
// when the caller left it zero, so every latch carries a time.
func LatchRefusal(r Refusal) {
	if r.At.IsZero() {
		r.At = time.Now()
	}
	versionState.mu.Lock()
	defer versionState.mu.Unlock()
	versionState.refusal = r
	versionState.hasRefusal = true
}

// CurrentRefusal reports the latched refusal, if any. ok is false when this
// client has never been refused, which is the state a healthy client stays in.
func CurrentRefusal() (Refusal, bool) {
	versionState.mu.RLock()
	defer versionState.mu.RUnlock()
	return versionState.refusal, versionState.hasRefusal
}

// ClearRefusal retires the latched refusal.
//
// Clearing is NOT an assertion that the client is now acceptable — it only
// retires a stale verdict. There is deliberately more than one caller: the
// proof loop clears after a proof SUCCEEDS, and the self-update path clears
// after it has replaced the binary. Neither can leave a false clear standing
// for long: if the client is still refused, the very next cloud request
// re-latches it, and the proof loop re-establishes the true state without a
// boot delay.
func ClearRefusal() {
	versionState.mu.Lock()
	defer versionState.mu.Unlock()
	versionState.refusal = Refusal{}
	versionState.hasRefusal = false
}

// RecordProof stores the outcome of a possession-proof attempt. At is stamped
// here when the caller left it zero.
func RecordProof(p ProofState) {
	if p.At.IsZero() {
		p.At = time.Now()
	}
	versionState.mu.Lock()
	defer versionState.mu.Unlock()
	versionState.proof = p
	versionState.hasProof = true
}

// ClearProof discards the recorded proof state. It exists for TESTS, in the
// same spirit as auth.WithAccountSelection.
//
// There is deliberately no production caller: a proof state is a RECORD of what
// this client last did, not a verdict to retire, so nothing in the running
// system has cause to erase one. But the never-proved state IS a real state —
// it is what a fresh process is in, and both status renderers are specified to
// stay silent in it — and without this a test could only reach it before any
// other test in the same process had recorded a proof. That would make the
// specified silence assertable only by luck of test ordering.
func ClearProof() {
	versionState.mu.Lock()
	defer versionState.mu.Unlock()
	versionState.proof = ProofState{}
	versionState.hasProof = false
}

// LastProof reports the most recent proof attempt, if any. ok is false when no
// proof has been attempted in this process — which is the ordinary state of a
// short-lived CLI invocation, and of a daemon in the moments after start.
func LastProof() (ProofState, bool) {
	versionState.mu.RLock()
	defer versionState.mu.RUnlock()
	return versionState.proof, versionState.hasProof
}
