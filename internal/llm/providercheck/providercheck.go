// SPDX-License-Identifier: Apache-2.0

// Package providercheck answers one question about a credential the user
// has just typed: does THIS key work at THIS provider, and which models
// does the provider list for it?
//
// It sits beside llm/precheck rather than inside it because the two answer
// different questions at different prices. precheck's ping issues a BILLED
// Generate against the configured model using the PROCESS-GLOBAL key
// (precheck.go:121-128), which is right for a startup probe of a
// configuration already on disk and wrong for a first-run guide: the key
// is not written yet, so a process-global read cannot see it, and a user
// pasting a key three times would pay for three completions. Every API
// check here is instead the provider's own list-models metadata read,
// which is what makes one call both the key check and the model list.
//
// # What it never does
//
// It writes no configuration, resolves no credential from the environment
// or from a file, and returns no secret. A provider's own error text is
// carried back as the outcome reason, bounded in length and with the
// caller's key removed if the provider echoed it — see SanitizeReason.
//
// # Cost
//
// One HTTPS GET per API check, bounded at APITimeout. One subprocess per
// CLI check, bounded at CLITimeout because a cold `claude` invocation
// loads Node and the user's credentials before it answers (the bound
// llm/precheck adopts for the same reason, precheck.go:44-52).
package providercheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
	"github.com/fulminate-io/knowledge-mcp/internal/llm/anthropic"
	"github.com/fulminate-io/knowledge-mcp/internal/llm/gemini"
	"github.com/fulminate-io/knowledge-mcp/internal/llm/openai"
)

// Outcome is the four-word vocabulary every check answers in. The fourth
// word is not decoration: a provider that is rate-limiting has accepted
// the key, and reporting that as Rejected tells the user their key is bad
// when it is not. Only Accepted may complete a guide step or precede a
// write; the other three all fail closed.
type Outcome string

// The Outcome values. They are the wire vocabulary the Desktop's response
// validator allowlists, so a new one is a coordinated change on both sides.
const (
	Accepted    Outcome = "accepted"
	Rejected    Outcome = "rejected"
	Unreachable Outcome = "unreachable"
	RateLimited Outcome = "rate_limited"
)

// APITimeout caps one list-models round trip. A provider answers a
// metadata read in 200-500ms; 10s absorbs a slow network without making
// a user watch a guide step stall. It is deliberately NOT
// llm.DefaultHTTPTimeout, whose 120s is sized for a long completion
// (llm/http_client.go:8-17).
const APITimeout = 10 * time.Second

// CLITimeout caps one CLI auth-status subprocess. 60s covers a cold first
// invocation, which loads the CLI's runtime and credential store before it
// prints anything.
const CLITimeout = 60 * time.Second

// reasonMaxBytes bounds the provider text carried back to the user. A
// provider error body is attacker-adjacent input of unbounded size: it
// crosses a process boundary, an IPC boundary and then lands in rendered
// page text a failing test screenshots.
const reasonMaxBytes = 512

// anthropicVersion is the API version header api.anthropic.com requires on
// every request, including the model list.
const anthropicVersion = "2023-06-01"

// Request names one provider and the credentials to try against it. Key is
// the value the user just typed; nothing here reads a stored one.
type Request struct {
	Provider config.Provider
	Key      string
	BaseURL  string
	CLIBin   string
}

// Result is one check's answer. Models is populated only by an API check
// that both succeeded and returned a readable list; a CLI provider lists
// no models, and an empty list is a truthful answer rather than a failure.
type Result struct {
	Outcome Outcome
	Reason  string
	Models  []string
}

// Check verifies req's credential against req's provider and, for an API
// provider, returns the models that provider lists for it.
//
// The error result is for a caller that asked something unanswerable — an
// empty or unrecognized provider — never for a provider that said no. A
// provider saying no is a Result whose Outcome is not Accepted, because
// "your key was rejected" is this function's answer and not its failure.
func Check(ctx context.Context, req Request) (Result, error) {
	switch {
	case req.Provider == "":
		return Result{}, errors.New("providercheck: no provider named; the caller must resolve the section's provider before checking it")
	case req.Provider.IsCLI():
		return checkCLI(ctx, req), nil
	case req.Provider.IsAPI():
		return checkAPI(ctx, req), nil
	default:
		return Result{}, fmt.Errorf("providercheck: unrecognized provider %q", req.Provider)
	}
}

// OutcomeForError classifies a failed provider call that already carries an
// *llm.LLMError, which is the shape every API arm in this tree returns.
// The mapping is the one llm/precheck/voyage.go:123-133 established for the
// startup axis checks, lifted here so the guide's embed check and this
// package's own HTTP arm cannot drift apart.
//
// An error with no *llm.LLMError in its chain is Unreachable: a transport
// failure, a cancelled context or a client that could not be built all mean
// the provider never got to answer.
func OutcomeForError(err error) (Outcome, string) {
	if err == nil {
		return Accepted, ""
	}
	if llmErr, ok := errors.AsType[*llm.LLMError](err); ok {
		switch llmErr.Reason {
		case "http_401", "http_403":
			return Rejected, "the provider rejected the key: it may be invalid, revoked, or out of credits"
		case "http_429":
			return RateLimited, "the provider is rate-limiting this key; retry shortly or check your usage"
		}
		return Unreachable, "the provider returned " + llmErr.Reason
	}
	return Unreachable, "the provider could not be reached: " + err.Error()
}

// SanitizeReason makes one line of provider text safe to carry back to the
// renderer: it removes key from the text and bounds the result.
//
// The removal is not hypothetical hygiene. The reason reaches the guide as
// rendered TEXT beside the field, which document.body.innerText carries and
// a failing Electron driver screenshots and uploads as CI evidence — unlike
// the key's own input, which is type=password and therefore in neither. A
// provider that echoes the offending credential in its error body would
// publish it there. The owner's standing rule is that an assertion about a
// credential reports a non-reversible property, never the value; this is
// the same rule applied to the text a user is shown.
func SanitizeReason(text, key string) string {
	out := strings.TrimSpace(text)
	if key != "" {
		out = strings.ReplaceAll(out, key, "[redacted]")
	}
	if len(out) > reasonMaxBytes {
		out = strings.TrimSpace(out[:reasonMaxBytes]) + "…"
	}
	return out
}

// modelListEndpoint returns the list-models URL and the auth headers for
// one API provider, honoring an explicit base URL.
//
// The base URLs come from the provider packages themselves rather than
// being restated here, so a compatible-endpoint user and a default user
// reach the same host this client would use for a completion.
func modelListEndpoint(req Request) (string, http.Header, error) {
	base := strings.TrimSuffix(req.BaseURL, "/")
	header := http.Header{}
	switch req.Provider {
	case config.ProviderAnthropic:
		if base == "" {
			base = anthropic.DefaultBaseURL
		}
		header.Set("X-Api-Key", req.Key)
		header.Set("Anthropic-Version", anthropicVersion)
		return base + "/v1/models", header, nil
	case config.ProviderOpenAI:
		if base == "" {
			base = openai.DefaultBaseURL
		}
		if req.Key != "" {
			header.Set("Authorization", "Bearer "+req.Key)
		}
		return base + "/v1/models", header, nil
	case config.ProviderGemini:
		if base == "" {
			base = gemini.DefaultBaseURL
		}
		// Gemini authenticates the metadata read with a query parameter,
		// so the key is URL-escaped rather than placed in a header.
		return base + "/v1beta/models?key=" + url.QueryEscape(req.Key), header, nil
	default:
		return "", nil, fmt.Errorf("providercheck: provider %q has no model-list endpoint", req.Provider)
	}
}

// checkAPI issues one list-models read and classifies the answer.
func checkAPI(ctx context.Context, req Request) Result {
	// FAIL CLOSED, before any call. An API provider with neither a pasted
	// key nor an explicit endpoint has nothing to check, and reporting
	// that as accepted would complete a guide step and write a
	// configuration the provider never agreed to. A keyless base_url is
	// the one legitimate keyless shape — the validator admits it
	// (config/validator.go, ValidateEmbedCredential's key-OR-base_url
	// rule) — because a local compatible server needs no credential.
	if req.Key == "" && req.BaseURL == "" {
		return Result{Outcome: Rejected, Reason: "no API key was provided for " + req.Provider.String() + "; paste the provider's API key, or set a custom endpoint that needs none"}
	}
	endpoint, header, err := modelListEndpoint(req)
	if err != nil {
		return Result{Outcome: Unreachable, Reason: SanitizeReason(err.Error(), req.Key)}
	}
	callCtx, cancel := context.WithTimeout(ctx, APITimeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Result{Outcome: Unreachable, Reason: SanitizeReason("the model-list request could not be built: "+err.Error(), req.Key)}
	}
	httpReq.Header = header
	client := &http.Client{Timeout: APITimeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return Result{Outcome: Unreachable, Reason: SanitizeReason("the provider could not be reached: "+err.Error(), req.Key)}
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return Result{Outcome: Rejected, Reason: SanitizeReason(rejectionReason(resp.StatusCode, body), req.Key)}
	case resp.StatusCode == http.StatusTooManyRequests:
		return Result{Outcome: RateLimited, Reason: "the provider is rate-limiting this key; retry shortly or check your usage"}
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return Result{Outcome: Unreachable, Reason: SanitizeReason(fmt.Sprintf("the provider returned HTTP %d for its model list: %s", resp.StatusCode, string(body)), req.Key)}
	case readErr != nil:
		return Result{Outcome: Accepted, Reason: "the provider accepted the key; its model list could not be read, so enter the model identifier yourself"}
	}
	models, err := parseModels(req.Provider, body)
	if err != nil {
		// The key WORKED — the provider answered 2xx to an authenticated
		// read. Only the list is unusable, so the outcome stays Accepted
		// with an empty list and the reason says what the user must do.
		return Result{Outcome: Accepted, Reason: "the provider accepted the key; its model list could not be read, so enter the model identifier yourself"}
	}
	return Result{Outcome: Accepted, Models: models}
}

// rejectionReason composes the user-facing sentence for a 401 or 403,
// carrying the provider's own body so the user sees why rather than a
// generic denial.
func rejectionReason(status int, body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return fmt.Sprintf("the provider rejected the key (HTTP %d)", status)
	}
	return fmt.Sprintf("the provider rejected the key (HTTP %d): %s", status, text)
}

// parseModels reads one provider's model list off its list-models body.
// Anthropic and OpenAI (and every OpenAI-compatible host) return
// {"data":[{"id":...}]}; Gemini returns {"models":[{"name":"models/..."}]}.
func parseModels(provider config.Provider, body []byte) ([]string, error) {
	var envelope struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("providercheck: decode %s model list: %w", provider, err)
	}
	var out []string
	for _, entry := range envelope.Data {
		if entry.ID != "" {
			out = append(out, entry.ID)
		}
	}
	for _, entry := range envelope.Models {
		// Gemini names a model "models/gemini-2.5-flash"; the configuration
		// field takes the bare identifier.
		if name := strings.TrimPrefix(entry.Name, "models/"); name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

// cliStatusArgs is the sign-in probe each CLI provider answers. Both print
// their state and carry the answer in the exit code.
var cliStatusArgs = map[config.Provider][]string{
	config.ProviderClaudeCLI: {"auth", "status"},
	config.ProviderCodexCLI:  {"login", "status"},
}

// cliStrippedKeys names the API-key variable each CLI provider must NOT
// see. A CLI provider exists to use the user's own subscription, and a
// stray key in the environment would let the CLI report itself signed in
// on API-key billing instead — the incident llm/claudecli/subprocess.go
// records at its ChildEnv call.
var cliStrippedKeys = map[config.Provider][]string{
	config.ProviderClaudeCLI: {"ANTHROPIC_API_KEY"},
	config.ProviderCodexCLI:  {"OPENAI_API_KEY"},
}

// checkCLI runs one CLI provider's sign-in probe.
func checkCLI(ctx context.Context, req Request) Result {
	args, ok := cliStatusArgs[req.Provider]
	if !ok {
		return Result{Outcome: Unreachable, Reason: "no sign-in probe is known for " + req.Provider.String()}
	}
	// FAIL CLOSED: the CLI arm's credential is the binary's own login
	// state, and with no path there is nothing to ask. config.Validate
	// refuses a CLI provider with an unset cli_bin for the same reason
	// (validateCLIBin), and it does not fall back to PATH.
	if req.CLIBin == "" {
		return Result{Outcome: Rejected, Reason: "no CLI executable path was set for " + req.Provider.String() + "; enter the path to the signed-in CLI under Connection options"}
	}
	callCtx, cancel := context.WithTimeout(ctx, CLITimeout)
	defer cancel()
	cmd := exec.CommandContext(callCtx, req.CLIBin, args...)
	cmd.Env = llm.ChildEnv(cliStrippedKeys[req.Provider])
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	output := strings.TrimSpace(stdout.String() + "\n" + stderr.String())
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return Result{Outcome: Accepted}
	case callCtx.Err() != nil:
		return Result{Outcome: Unreachable, Reason: "the CLI did not answer within " + CLITimeout.String()}
	case errors.As(err, &exitErr):
		// A non-zero exit is the CLI's way of saying "not signed in",
		// which is a rejected credential rather than an unreachable one.
		return Result{Outcome: Rejected, Reason: SanitizeReason("the CLI reports it is not signed in: "+output, req.Key)}
	default:
		return Result{Outcome: Unreachable, Reason: SanitizeReason("the CLI executable could not be run: "+err.Error(), req.Key)}
	}
}
