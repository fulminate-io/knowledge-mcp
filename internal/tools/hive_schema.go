// SPDX-License-Identifier: Apache-2.0

package tools

import "github.com/fulminate-io/knowledge-mcp/internal/kgtools"

// HiveToolDef returns the agent-facing `hive` MCP tool definition — the LLM's
// WHOLE hive surface. It is op-dispatched with EXACTLY the five AGENT ops:
// register / send / claim / ack / fail. The daemon/supervisor ops (renew,
// evict, ack-on-behalf) ride the same RPC + HiveOp enum but are DELIBERATELY
// ABSENT from this schema — they are invoked by the client daemon, never by the
// LLM. The hive work-queue is a hosted feature: a self-hosted (unauthenticated)
// caller's server fails loud on every hive op.
//
// The wire intercept lives in intercept_hive.go (InterceptHive); the server has
// no rendered hive handler. Wired into tools/list via the client's schema
// catalog (catalog.go).
func HiveToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name: "hive",
		Description: "Cloud work-queue for multi-agent coordination. Op-dispatched: " +
			"register (declare self in a hive with roles), send (put a unit of work into the hive), " +
			"claim (atomically lease one eligible item matching your roles or @queue, priority-first), " +
			"ack (mark your claimed work done with a result), fail (voluntarily mark your claimed work " +
			"blocked when you cannot finish). Reads — who's registered, what's done/blocked — go through " +
			"ordinary graph query/search (everything is in the graph). Cloud-only; requires login.",
		InputSchema: kgtools.InputSchema{
			Type:     "object",
			Required: []string{"op"},
			Properties: map[string]kgtools.Property{
				"op": {
					Type: "string",
					Enum: []string{"register", "send", "claim", "ack", "fail"},
					Description: "Operation: register (declare self) | send (enqueue work) | " +
						"claim (lease one eligible item) | ack (complete with result) | fail (block with reason).",
				},
				"hive": {
					Type:        "string",
					Description: "Hive name (register/send/claim). The hive is created implicitly on first register/send.",
				},
				"name": {
					Type:        "string",
					Description: "Your human-friendly member label (register). Your true identity is your session.",
				},
				"roles": {
					Type:        "array",
					Items:       &kgtools.Property{Type: "string"},
					Description: "Roles you hold (register). claim matches work addressed to @queue OR @<one-of-your-roles>.",
				},
				"body": {
					Type:        "string",
					Description: "The work body (send) — the task description / payload for the claiming worker.",
				},
				"to": {
					Type: "string",
					Description: "Work routing (send): '@<role>' (any member holding that role may claim) or " +
						"'@queue' (any worker may claim, role-agnostic). ONLY @<role> or @queue — no '*' broadcast, " +
						"no bare '<name>' direct addressing (those are a deferred messaging layer).",
				},
				"priority": {
					Type:        "string",
					Description: "Work priority (send), an integer string; claim is priority-first (higher first).",
				},
				"reply_to": {
					Type:        "string",
					Description: "Message ID this work answers (send) — the ack of the reply links back to it via responds-to.",
				},
				"msg_id": {
					Type:        "string",
					Description: "The message (work item) ID to ack or fail — the id of the item you claimed.",
				},
				"result": {
					Type:        "string",
					Description: "Completion result (ack) — stored inline on the done message.",
				},
				"reason": {
					Type:        "string",
					Description: "Failure reason (fail) — why you could not finish; reported to the sender and queryable.",
				},
			},
		},
	}
}
