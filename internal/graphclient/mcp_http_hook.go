// SPDX-License-Identifier: Apache-2.0

// mcp_http_hook.go — the daemon's PreToolUse hook endpoint and the bounded,
// short-TTL correlation map it feeds.
//
// Neither harness can put its session id on the MCP request itself in a way we
// can rely on, so each one's PreToolUse hook POSTs its own hook JSON here a few
// milliseconds BEFORE the tools/call it precedes. The daemon files the delivery
// under the harness's per-call id and the following request's `_meta` names that
// same id, which is how a request is joined to a session (see
// mcp_http_harness_session.go for the join).
//
// ONE MAP, TWO PATHS. /hook/claude and /hook/codex carry identical payload
// field names, so one body shape serves both; the PATH is what records WHICH
// harness delivered, and the resolved source is read back off the stored
// delivery rather than inferred from which `_meta` key a request happened to
// carry.
//
// A DELIVERY IS NOT A PROMISE OF A CALL. The hook fires before the permission
// decision, so a denied call leaves a delivery with no request to claim it.
// That is why the map carries TWO independent bounds — a TTL sweep and a size
// cap — rather than only one: the TTL alone does not bound a burst of denied
// calls between sweeps.
//
// REFUSALS ARE FATAL AND LOUD, never fail-open: a non-loopback peer, an
// oversized body, a body that is not a JSON object, or a delivery missing
// either identifier is REFUSED and NOTHING is stored. The endpoint is
// unauthenticated by design — it is loopback-only and carries no secret — so
// there is no credential here to compare, and no error message names any part
// of the body.

package graphclient

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

// The hook delivery paths, one per harness. The installers render these into
// the hook definitions they write, so the spelling is a contract with
// bootstrap's asset text (cmd/knowledge/internal/bootstrap/mcp_register.go
// daemonHookURL) — changing one without the other silently stops correlation.
const (
	hookPathClaude = "/hook/claude"
	hookPathCodex  = "/hook/codex"
)

// The harness labels stored on a delivery. They are the session package's
// resolution-source vocabulary, reused rather than re-literaled.
const (
	hookHarnessClaude = "claude"
	hookHarnessCodex  = "codex"
)

// hookCorrelationTTL is how long a delivery stays claimable. The observed lead
// time between a delivery and the call it precedes is 3-8 ms for Claude and
// 3-25 ms for Codex, so a minute is generous by three orders of magnitude while
// still being far shorter than the 30-minute session idle window — a tool-call
// id is not a session and must not live like one.
const hookCorrelationTTL = time.Minute

// hookCorrelationMax caps the map independently of the TTL. A harness that
// fires hooks for calls that are then denied grows the map between sweeps at a
// rate the TTL does not bound; at the cap the OLDEST delivery is evicted, so a
// burst costs the stalest entries rather than unbounded memory.
const hookCorrelationMax = 512

// hookBodyMaxBytes bounds the delivery body. A hook payload is a handful of
// short fields plus the tool's own input; 64 KiB is far above any observed
// delivery and far below anything worth absorbing from an unbounded reader.
const hookBodyMaxBytes = 64 << 10

// hookKey is the correlation map's key: the harness AND its per-call id, never
// the id alone.
//
// THE HARNESS IS PART OF THE KEY BECAUSE THE ID SPACES ARE NOT SHARED. Claude's
// toolUseId is a global `toolu_…`; Codex's callId is shaped `exec-<uuid>` and is
// only observed to be unique within a session. Two concurrent Codex sessions
// inside one TTL window could therefore present the same id, and a single
// namespace would let the second delivery overwrite the first — the first
// session's next call would then resolve to the SECOND session's identity. That
// is an account misbinding once a session id selects an account, so the key
// carries the harness and the namespaces stay separate.
type hookKey struct {
	harness string
	id      string
}

// hookDelivery is one PreToolUse hook delivery, filed under its hookKey. Cwd
// and TranscriptPath ride along because the hook is the only carrier that
// reports them; nothing derives the session id from either.
type hookDelivery struct {
	sessionID      string
	cwd            string
	transcriptPath string
	receivedAt     time.Time
}

// hookPayload models the fields the daemon reads out of a hook delivery. BOTH
// harnesses spell them identically (Claude Code 2.1.272, Codex 0.154.0), so one
// concrete struct serves both paths. It is deliberately PARTIAL: each harness
// sends more (tool_input, permission_mode, model, prompt_id, turn_id, ...) and
// none of it is read, stored or logged. Decoding into a concrete struct rather
// than a map is what makes an unexpected shape fail instead of propagating.
type hookPayload struct {
	SessionID      string `json:"session_id"`
	ToolUseID      string `json:"tool_use_id"`
	Cwd            string `json:"cwd"`
	TranscriptPath string `json:"transcript_path"`
}

// errHookNotObject is the refusal for a body that parses as JSON but is not an
// object (a bare array, string, number, or null). Decoding those into a struct
// either errors or silently yields a zero value, and a zero value would be
// refused for the wrong reason — so the shape is checked first, by name.
var errHookNotObject = errors.New("hook delivery body must be a JSON object")

// handleHookDelivery returns the handler for one harness's hook path. The
// harness is bound at registration from the PATH rather than read out of the
// body, because the body carries no harness field and guessing one would put a
// fabricated value into the resolution source manage(status) reports.
func (h *HTTPServer) handleHookDelivery(harness string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if browserOriginated(r) {
			// A BROWSER PAGE IS NOT A LOOPBACK PEER, even though its socket is.
			// A cross-origin POST carrying Content-Type: text/plain is a CORS
			// SIMPLE request: no preflight, so nothing the daemon returns can
			// stop it being sent, and RemoteAddr is 127.0.0.1 because the
			// browser runs here. Neither harness sends an Origin or a
			// Sec-Fetch-Site, so refusing anything that does costs nothing and
			// shuts the one door the peer check cannot.
			http.Error(w, "hook delivery refused: browser-originated request", http.StatusForbidden)
			return
		}
		if !requestIsLoopback(r) {
			// Belt-and-braces over an already-loopback listener (Run binds
			// 127.0.0.1 and refuses any other bind): this is the second guard,
			// not the only one. RemoteAddr is set by net/http from the accepted
			// connection and is not client-settable, and the daemon is
			// direct-facing with no proxy in front of it, so it is the right
			// value to decide on here — no forwarded header is consulted.
			http.Error(w, "hook delivery refused: peer is not loopback", http.StatusForbidden)
			return
		}
		payload, err := decodeHookDelivery(w, r)
		if err != nil {
			// The condition is named; no part of the body is echoed. A hook
			// payload carries the user's cwd and transcript path.
			http.Error(w, "hook delivery refused: "+err.Error(), http.StatusBadRequest)
			return
		}
		h.storeHookDelivery(payload, harness, time.Now())
		w.WriteHeader(http.StatusNoContent)
	}
}

// decodeHookDelivery reads the size-capped body and returns the payload, or the
// refusal condition. Both identifiers are REQUIRED: a delivery missing either is
// unusable (an entry with no key cannot be found; an entry with no session id
// resolves to nothing) and is refused rather than stored as a husk.
func decodeHookDelivery(w http.ResponseWriter, r *http.Request) (hookPayload, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, hookBodyMaxBytes))
	if err != nil {
		return hookPayload{}, err
	}
	var probe json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return hookPayload{}, err
	}
	if len(probe) == 0 || probe[0] != '{' {
		return hookPayload{}, errHookNotObject
	}
	var payload hookPayload
	if err := json.Unmarshal(probe, &payload); err != nil {
		return hookPayload{}, err
	}
	if payload.SessionID == "" {
		return hookPayload{}, errors.New("session_id is empty or absent")
	}
	if payload.ToolUseID == "" {
		return hookPayload{}, errors.New("tool_use_id is empty or absent")
	}
	return payload, nil
}

// storeHookDelivery files a delivery under its tool-call id. A repeat id
// replaces the entry in place (latest wins) and keeps its position in the
// eviction order, so re-delivery cannot be used to keep a stale entry alive
// ahead of newer ones. At hookCorrelationMax the oldest entry is evicted first.
func (h *HTTPServer) storeHookDelivery(p hookPayload, harness string, now time.Time) {
	key := hookKey{harness: harness, id: p.ToolUseID}
	h.hookMu.Lock()
	defer h.hookMu.Unlock()
	if h.hooks == nil {
		h.hooks = map[hookKey]*hookDelivery{}
	}
	if _, exists := h.hooks[key]; !exists {
		for len(h.hookOrder) >= hookCorrelationMax {
			oldest := h.hookOrder[0]
			h.hookOrder = h.hookOrder[1:]
			delete(h.hooks, oldest)
		}
		h.hookOrder = append(h.hookOrder, key)
	}
	h.hooks[key] = &hookDelivery{
		sessionID:      p.SessionID,
		cwd:            p.Cwd,
		transcriptPath: p.TranscriptPath,
		receivedAt:     now,
	}
}

// lookupHookDelivery returns the delivery filed under (harness, id) if one is present AND
// still within the TTL relative to now. An expired entry is reported as a MISS
// even when the sweep has not reached it yet, so the TTL is a property of the
// lookup rather than of the sweep's timing.
func (h *HTTPServer) lookupHookDelivery(harness, id string, now time.Time) (*hookDelivery, bool) {
	if id == "" {
		return nil, false
	}
	h.hookMu.Lock()
	defer h.hookMu.Unlock()
	d, ok := h.hooks[hookKey{harness: harness, id: id}]
	if !ok || now.Sub(d.receivedAt) > hookCorrelationTTL {
		return nil, false
	}
	return d, true
}

// reapHookDeliveries drops every delivery older than the TTL relative to now and
// returns how many went. It is driven from the SAME runReaper goroutine that
// sweeps idle sessions — a second goroutine would be a second lifetime to prove
// — and is called directly by tests with an explicit now, the way reapIdle is.
func (h *HTTPServer) reapHookDeliveries(now time.Time) int {
	h.hookMu.Lock()
	defer h.hookMu.Unlock()
	kept := h.hookOrder[:0]
	evicted := 0
	for _, key := range h.hookOrder {
		d, ok := h.hooks[key]
		if !ok {
			continue
		}
		if now.Sub(d.receivedAt) > hookCorrelationTTL {
			delete(h.hooks, key)
			evicted++
			continue
		}
		kept = append(kept, key)
	}
	h.hookOrder = kept
	return evicted
}

// browserOriginated reports whether the request carries a tell that a browser
// sent it. Fetch and XHR attach Origin on every cross-origin request and
// Sec-Fetch-Site on every request in a modern browser; a PreToolUse hook (an
// http POST from the Claude Code process, or curl from the Codex command hook)
// attaches neither. Either one present is a refusal — this is an allowlist of
// shapes we expect, not a blocklist of attacks we thought of.
func browserOriginated(r *http.Request) bool {
	return r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != ""
}

// requestIsLoopback reports whether the request's peer address is a loopback
// address. RemoteAddr is "host:port", never a bare IP, so it is SPLIT before
// parsing — a string-prefix match against it is a bug in its own right. Any
// address that does not split, does not parse, or is not loopback is a refusal:
// a peer the daemon cannot identify is never admitted.
func requestIsLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
