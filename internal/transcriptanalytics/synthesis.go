// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// Recommendation is one ranked, actionable agent-flow optimization the synthesis
// stage derived from the deterministic detector report.
type Recommendation struct {
	Title     string `json:"title"`
	Category  string `json:"category"`
	Impact    string `json:"impact"` // high | medium | low
	Rationale string `json:"rationale"`
}

// SynthesisResult is the ranked recommendation set. It is EMPTY (not an error) when no
// LLM is configured — the analyzer still returns its deterministic detector output.
type SynthesisResult struct {
	Recommendations []Recommendation `json:"recommendations"`
}

// Synthesizer turns a deterministic DetectorReport into ranked recommendations via the
// daemon's own configured (BYOK) LLM. It is the ONLY inference surface in the analyzer;
// the detectors themselves are pure SQL.
type Synthesizer struct {
	client llm.Client
	model  llm.Model
}

// NewSynthesizer builds a Synthesizer from the active config, mirroring the build-
// from-active-config pattern the hive supervisor uses. It resolves the supervisor
// consumer section — a strong one-shot structured reasoner; an absent [supervisor]
// section inherits fully from [default], so the analyzer uses the user's default model
// out of the box. Returns (nil, nil) when config is unloaded, so the caller degrades to
// detector-only output rather than failing.
func NewSynthesizer(ctx context.Context) (*Synthesizer, error) {
	if !config.Loaded() {
		return nil, nil
	}
	sec, err := config.Active().Resolve(config.ConsumerHiveSupervisor)
	if err != nil {
		return nil, fmt.Errorf("transcriptanalytics: resolve synthesizer config: %w", err)
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
		return nil, fmt.Errorf("transcriptanalytics: build synthesizer client: %w", err)
	}
	return &Synthesizer{client: client, model: model}, nil
}

// synthesisSystemPrompt frames the model as an agent-flow optimization advisor and
// pins the output to ranked, actionable recommendations grounded in the detector data.
const synthesisSystemPrompt = "You are an agent-flow optimization advisor. You are given a JSON report of " +
	"deterministic metrics computed over a developer's own AI coding-assistant transcripts: " +
	"redundantly-rerun commands, per-tool latency and total wall-time, per-session and " +
	"per-subagent token spend, prompt-cache reuse, subagent wall-time, agent-chain " +
	"over-orchestration, and waste (API errors, interrupts, and max-token truncations). " +
	"Produce a SHORT list of concrete, ranked recommendations to make this developer's agent " +
	"usage faster, cheaper, and less wasteful. Rank the highest-impact items first. Each " +
	"recommendation must cite the specific metric that motivates it in its rationale. Set " +
	"\"impact\" to exactly one of \"high\", \"medium\", or \"low\". Do not invent metrics not " +
	"present in the report; if the report is sparse, return fewer recommendations."

// synthesisSchema is the json_schema response format. additionalProperties:false is set
// on every object — required by strict structured-output providers.
const synthesisSchema = `{"type":"object","properties":{` +
	`"recommendations":{"type":"array","items":{"type":"object","properties":{` +
	`"title":{"type":"string"},` +
	`"category":{"type":"string"},` +
	`"impact":{"type":"string","enum":["high","medium","low"]},` +
	`"rationale":{"type":"string"}` +
	`},"required":["title","category","impact","rationale"],"additionalProperties":false}}` +
	`},"required":["recommendations"],"additionalProperties":false}`

// Synthesize feeds the detector report to the configured LLM under the recommendation
// json_schema and returns the ranked recommendations. A nil Synthesizer or nil client
// (unconfigured LLM) returns an empty, NON-error result — the detector-only degrade —
// so the analyzer always produces deterministic output regardless of LLM availability.
func (s *Synthesizer) Synthesize(ctx context.Context, report *DetectorReport) (SynthesisResult, error) {
	if s == nil || s.client == nil {
		return SynthesisResult{}, nil
	}
	if report == nil {
		return SynthesisResult{}, nil
	}

	reportJSON, err := json.Marshal(report)
	if err != nil {
		return SynthesisResult{}, fmt.Errorf("transcriptanalytics: marshal detector report: %w", err)
	}

	resp, err := s.client.Generate(ctx, []*schema.Message{
		{Role: schema.User, Content: "Detector report:\n" + string(reportJSON)},
	},
		llm.WithModel(s.model),
		llm.WithSystemPrompt(synthesisSystemPrompt),
		llm.WithMaxTokens(2048),
		llm.WithResponseFormat(&llm.ResponseFormat{
			Type:   "json_schema",
			Schema: json.RawMessage(synthesisSchema),
		}),
	)
	if err != nil {
		return SynthesisResult{}, fmt.Errorf("transcriptanalytics: synthesize generate: %w", err)
	}

	var out SynthesisResult
	if err := json.Unmarshal([]byte(resp.Content), &out); err != nil {
		return SynthesisResult{}, fmt.Errorf("transcriptanalytics: parse synthesis JSON: %w (content: %s)", err, resp.Content)
	}
	return out, nil
}

// Recommend runs the deterministic detectors and, when a BYOK LLM is configured,
// synthesizes ranked recommendations from the report. The detector report is ALWAYS
// returned (it is the analyzer's deterministic core); a missing or failing LLM degrades
// synthesis to empty recommendations rather than sinking the whole call. Only a
// detector (SQL/engine) failure returns a non-nil error.
func (s *Service) Recommend(ctx context.Context) (*DetectorReport, SynthesisResult, error) {
	report, err := s.RunDetectors(ctx)
	if err != nil {
		return nil, SynthesisResult{}, err
	}
	synth, err := NewSynthesizer(ctx)
	if err != nil {
		slog.Warn("transcriptanalytics: synthesizer unavailable; returning detector-only output", "err", err)
		return report, SynthesisResult{}, nil
	}
	result, serr := synth.Synthesize(ctx, report)
	if serr != nil {
		slog.Warn("transcriptanalytics: synthesis failed; returning detector-only output", "err", serr)
		return report, SynthesisResult{}, nil
	}
	return report, result, nil
}
