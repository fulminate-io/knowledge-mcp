// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// Supervisor is the Tier-2 hive judge: given a formatted worker transcript it
// returns a Verdict (state + confidence + reason + lifted result). It is invoked
// only on Tier-1 monitor ambiguity, so one call per escalation — not a hot loop.
//
// Exported as an interface so the hivemonitor escalation handler depends on the
// seam (and test harnesses can supply a fake) without importing the concrete
// impl.
type Supervisor interface {
	// Judge classifies the worker's recent transcript. A parse/format failure
	// returns a non-nil error; the caller treats any error as conservative
	// (resume-renew only, no Hive op).
	Judge(ctx context.Context, transcript string) (Verdict, error)
}

// Verdict is the supervisor's structured judgment of a worker.
//
//   - State is one of working|done|stuck|off-rails (the verdict-to-action matrix
//     in the hivemonitor handler keys on this plus Confidence).
//   - Confidence is the model's 0..1 self-assessed certainty; the handler only
//     acts terminally (ack / evict) at high confidence.
//   - Reason is the human-readable DNF / completion rationale, threaded into the
//     evict reason on the stuck/off-rails path.
//   - Result is the lifted completion text used as the ack-on-behalf result on
//     the done path.
type Verdict struct {
	State      string  `json:"state"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
	Result     string  `json:"result"`
}

// llmSupervisor is the substrate-routed supervisor adapter over a single
// llm.Client, mirroring llmSummarizer: it owns only the judge-specific
// JSON-schema prompt shape; all wire concerns live in domains/llm.
type llmSupervisor struct {
	client llm.Client
	model  llm.Model
}

// Compile-time interface satisfaction.
var _ Supervisor = (*llmSupervisor)(nil)

// NewLLMSupervisor constructs a substrate-routed Supervisor over client+model.
// Production callers build this via BuildHiveSupervisor; the constructor stays
// exported so test harnesses can inject their own llm.Client (typically
// llm.NewFakeClient) without reaching into unexported types.
func NewLLMSupervisor(client llm.Client, model llm.Model) Supervisor {
	return &llmSupervisor{client: client, model: model}
}

// BuildHiveSupervisor constructs the client-side hive supervisor from
// config.Active(). Returns (nil, nil) when config is unloaded — the caller
// treats this as "supervision disabled" and the escalation path degrades to the
// conservative log-and-resume fallback.
//
// Mirrors BuildSummarizer: resolve the per-consumer section, build an
// llm.Client, wrap it. The only deltas are the consumer constant
// (ConsumerHiveSupervisor) and the return type.
func BuildHiveSupervisor(ctx context.Context) (Supervisor, error) {
	if !config.Loaded() {
		slog.Warn("llmproviders: config not loaded; hive supervision disabled")
		return nil, nil
	}
	sec, err := config.Active().Resolve(config.ConsumerHiveSupervisor)
	if err != nil {
		return nil, fmt.Errorf("resolve supervisor config: %w", err)
	}
	model := llm.Model(sec.Model)
	client, err := llm.NewClient(ctx, &llm.Config{
		Provider: sec.Provider,
		Model:    model,
		APIKey:   config.APIKeyForProvider(sec.Provider),
		BaseURL:  sec.BaseURL,
		CLIBin:   sec.CLIBin,
	})
	if err != nil {
		return nil, fmt.Errorf("build supervisor client: %w", err)
	}
	slog.Info("llmproviders: hive supervisor ready", "provider", sec.Provider, "model", sec.Model)
	return NewLLMSupervisor(client, model), nil
}

// supervisorSystemPrompt instructs the model to judge a worker transcript and
// return the strict verdict shape. The schema (below) is the hard contract; this
// prompt sets the semantics of each state and confidence.
const supervisorSystemPrompt = "You are a supervisor judging whether an autonomous coding agent is making " +
	"progress from its recent transcript. Classify its current state as exactly one of: " +
	"\"working\" (actively progressing), \"done\" (the task is genuinely complete), " +
	"\"stuck\" (looping, blocked, or making no progress), or \"off-rails\" (doing the wrong thing " +
	"or violating its task). Provide a confidence in [0,1] for your classification. " +
	"In \"reason\" give a one-sentence rationale. In \"result\", ONLY when state is \"done\", put a " +
	"concise summary of what the worker accomplished (the completion result); otherwise leave it empty. " +
	"Be conservative: prefer \"working\" and low confidence when the transcript is ambiguous."

// verdictSchema is the json_schema response format constraining the model to the
// Verdict shape. additionalProperties:false is REQUIRED on every object — the
// OpenAI Responses API (which the codex-cli provider's --output-schema flag
// drives) rejects strict schemas that omit it; other providers accept the
// tightening as a no-op.
const verdictSchema = `{"type":"object","properties":{` +
	`"state":{"type":"string","enum":["working","done","stuck","off-rails"]},` +
	`"confidence":{"type":"number","minimum":0,"maximum":1},` +
	`"reason":{"type":"string"},` +
	`"result":{"type":"string"}` +
	`},"required":["state","confidence","reason","result"],"additionalProperties":false}`

// Judge sends the formatted transcript to the configured llm.Client under the
// verdict json_schema and decodes the response into a Verdict.
//
// Mirrors SummarizeBatch's Generate+json_schema call shape. A Generate error or
// a JSON parse failure is returned as an error; the hivemonitor handler treats
// any error as conservative (resume-renew only).
func (s *llmSupervisor) Judge(ctx context.Context, transcript string) (Verdict, error) {
	if strings.TrimSpace(transcript) == "" {
		return Verdict{}, fmt.Errorf("supervisor: empty transcript")
	}

	resp, err := s.client.Generate(ctx, []*schema.Message{
		{Role: schema.User, Content: transcript},
	},
		llm.WithModel(s.model),
		llm.WithSystemPrompt(supervisorSystemPrompt),
		llm.WithMaxTokens(1024),
		llm.WithResponseFormat(&llm.ResponseFormat{
			Type:   "json_schema",
			Schema: json.RawMessage(verdictSchema),
		}),
	)
	if err != nil {
		return Verdict{}, fmt.Errorf("supervisor generate: %w", err)
	}

	var v Verdict
	if err := json.Unmarshal([]byte(resp.Content), &v); err != nil {
		return Verdict{}, fmt.Errorf("parse verdict JSON: %w (content: %s)", err, resp.Content)
	}
	if strings.TrimSpace(v.State) == "" {
		return Verdict{}, fmt.Errorf("supervisor: verdict missing state (content: %s)", resp.Content)
	}
	return v, nil
}
