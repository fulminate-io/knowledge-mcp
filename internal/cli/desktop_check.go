// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/backends/linear"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/llm/precheck"
	"github.com/fulminate-io/knowledge-mcp/internal/llm/providercheck"
)

// The axis words desktop-check accepts. Each names one credential the
// first-run guide configures, and each is validated by exact membership so
// an unrecognized word is refused rather than defaulted to a check the
// caller did not ask for.
const (
	checkAxisSummarizer = "summarizer"
	checkAxisEmbedder   = "embedder"
	checkAxisTracker    = "tracker"
)

var desktopCheckAxes = []string{checkAxisSummarizer, checkAxisEmbedder, checkAxisTracker}

// desktopCheckRequest is the stdin body. The key travels here and nowhere
// else: never in argv, never in the environment (desktop_settings.go:36-37
// states the same rule for the settings command, and the Desktop's own
// child environment carries no provider variable at all).
type desktopCheckRequest struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
	Model    string `json:"model"`
	BaseURL  string `json:"base_url"`
	CLIBin   string `json:"cli_bin"`
}

// desktopCheckResult is the single JSON object written to stdout. Outcome
// is one of providercheck's four words; Reason is the provider's own
// sentence, sanitized; Models is populated only by a summarizer-axis check
// whose provider returned a readable list.
type desktopCheckResult struct {
	Outcome string   `json:"outcome"`
	Reason  string   `json:"reason,omitempty"`
	Models  []string `json:"models,omitempty"`
}

// DesktopCheckCmd verifies ONE axis's credential by making the call that
// axis's provider answers, and writes one JSON object to stdout.
//
// IT NEVER WRITES THE CONFIGURATION. That is the whole point of it being a
// separate command from desktop-settings save: the first-run guide checks
// the key the user pasted, and saves only once the provider has accepted
// it, so a rejected or unreachable key is never persisted. Structurally,
// config.WriteFileAtomic is reached from the save path alone
// (desktop_settings.go:117-122) and nothing here calls it.
//
// The configuration file is READ, and only for one purpose: a step revisited
// with a key already stored and nothing re-pasted must still be checkable,
// so an empty request key falls back to the stored one. A missing file is
// the ordinary first-run case and resolves to no stored key.
func DesktopCheckCmd(args []string) error {
	return runDesktopCheckCmd(context.Background(), args, os.Stdin, os.Stdout)
}

// runDesktopCheckCmd is DesktopCheckCmd with its process streams as
// parameters, so the tests drive the whole command — argv, stdin body,
// stdout object — in-process against a stub provider.
//
// THE AXIS IS VALIDATED BEFORE STDIN IS READ. That ordering is the reason
// this refusal is here rather than only in runDesktopCheck's dispatch: the
// body arrives on a pipe, so a command that dispatched first would block
// waiting for a body it is going to refuse anyway.
func runDesktopCheckCmd(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	if len(args) < 1 || !slices.Contains(desktopCheckAxes, args[0]) {
		return fmt.Errorf("unsupported check axis; want one of %v", desktopCheckAxes)
	}
	flags := flag.NewFlagSet("desktop-check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config-file", "", "Knowledge configuration path")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || !filepath.IsAbs(*path) {
		return errors.New("invalid check arguments")
	}
	request, err := readDesktopCheckRequest(input)
	if err != nil {
		return err
	}
	credentials, err := desktopCheckCredentials(*path)
	if err != nil {
		return err
	}
	result, err := runDesktopCheck(ctx, args[0], request, credentials)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}

// readDesktopCheckRequest decodes and bounds the stdin body. The limits
// mirror the settings command's: 64KB of request, 16KB of any one value,
// and no unknown fields, so a body the Desktop did not compose is refused
// rather than partly honored.
func readDesktopCheckRequest(input io.Reader) (desktopCheckRequest, error) {
	raw, err := io.ReadAll(io.LimitReader(input, 65537))
	if err != nil || len(raw) > 65536 {
		return desktopCheckRequest{}, errors.New("invalid check request")
	}
	var request desktopCheckRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&request) != nil || dec.Decode(new(any)) != io.EOF {
		return desktopCheckRequest{}, errors.New("invalid check request")
	}
	for _, value := range []string{request.Provider, request.Key, request.Model, request.BaseURL, request.CLIBin} {
		if len(value) > 16384 {
			return desktopCheckRequest{}, errors.New("invalid check request")
		}
	}
	return request, nil
}

// desktopCheckCredentials reads the stored credentials table from path, so
// a re-visited step with a saved key and nothing re-pasted can still be
// checked. A missing file is first run and yields none; an unreadable or
// malformed one is reported rather than treated as empty, because silently
// reading "no stored key" out of a broken file would make the check report
// a rejected credential for a key that is actually there.
func desktopCheckCredentials(path string) (*config.Credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errors.New("the configuration could not be read")
	}
	parsed, err := config.Parse(data)
	if err != nil {
		return nil, errors.New("the configuration could not be parsed")
	}
	return parsed.Credentials, nil
}

// runDesktopCheck dispatches one axis. Split from DesktopCheckCmd so the
// tests drive it in-process with a scratch configuration and a stub
// provider, the way the other desktop- actions are tested.
func runDesktopCheck(ctx context.Context, axis string, request desktopCheckRequest, credentials *config.Credentials) (desktopCheckResult, error) {
	switch axis {
	case checkAxisSummarizer:
		return checkSummarizerAxis(ctx, request, credentials)
	case checkAxisEmbedder:
		return checkEmbedderAxis(ctx, request, credentials)
	case checkAxisTracker:
		return checkTrackerAxis(ctx, request, credentials)
	default:
		return desktopCheckResult{}, fmt.Errorf("unsupported check axis %q; want one of %v", axis, desktopCheckAxes)
	}
}

// checkSummarizerAxis checks an LLM provider and returns the models it
// lists. This is the one axis with a live model list: the provider's
// list-models read is both the key check and the list, so the guide never
// pays for a completion to find out whether a key works.
func checkSummarizerAxis(ctx context.Context, request desktopCheckRequest, credentials *config.Credentials) (desktopCheckResult, error) {
	// The provider is NOT re-validated here. providercheck.Check refuses an
	// empty or unrecognized provider itself and names it, and a second
	// identical refusal on this side would be a guard no test could
	// observe: removing it would change nothing a caller can see.
	provider := config.Provider(request.Provider)
	key := request.Key
	if key == "" {
		key = credentials.APIKeyFor(provider)
	}
	result, err := providercheck.Check(ctx, providercheck.Request{
		Provider: provider,
		Key:      key,
		BaseURL:  request.BaseURL,
		CLIBin:   request.CLIBin,
	})
	if err != nil {
		return desktopCheckResult{}, err
	}
	return desktopCheckResult{Outcome: string(result.Outcome), Reason: result.Reason, Models: result.Models}, nil
}

// checkEmbedderAxis checks an embedding provider with one live embed,
// through the same exported check the server runs at startup.
//
// NO MODEL LIST. Voyage documents no list-models endpoint, so a live list
// is not available for the recommended provider, and a list for some
// providers and not others would be a worse control than none. The step
// keeps each provider's own default model and any stored value instead.
func checkEmbedderAxis(ctx context.Context, request desktopCheckRequest, credentials *config.Credentials) (desktopCheckResult, error) {
	provider := config.EmbedProvider(request.Provider)
	if !provider.IsValid() {
		return desktopCheckResult{}, fmt.Errorf("unsupported embedding provider %q", request.Provider)
	}
	key := request.Key
	if key == "" {
		key = credentials.EmbedAPIKeyFor(provider)
	}
	if provider == config.EmbedProviderFake {
		// The deterministic fake authenticates nothing and calls nothing.
		// Saying so is truthful; reporting "accepted" with no explanation
		// would imply a provider agreed to something.
		return desktopCheckResult{Outcome: string(providercheck.Accepted), Reason: "the deterministic fake embedder makes no provider call, so there is nothing to check"}, nil
	}
	// FAIL CLOSED, before the call. precheck.CheckEmbedProvider returns nil
	// for an axis with neither a credential nor an endpoint — that is its
	// documented BM25-only opt-out (voyage.go:61-66), which is the right
	// answer for a server deciding whether to embed and the wrong one
	// here: it would report an unconfigured axis as an accepted key and
	// complete the step.
	if key == "" && request.BaseURL == "" {
		return desktopCheckResult{Outcome: string(providercheck.Rejected), Reason: "no API key was provided for " + provider.String() + "; paste the provider's API key, or set a custom endpoint that needs none"}, nil
	}
	// Dimension and Dtype are the package defaults rather than the step's
	// pending values, deliberately: they decide the SHAPE of the vector a
	// provider returns, not whether it accepts the credential, so a probe
	// at the default width answers the question this command is asked and
	// keeps the request body to the fields the check actually reads.
	err := precheck.CheckEmbedProvider(ctx, config.EmbedSection{
		Provider:  provider,
		Model:     request.Model,
		BaseURL:   request.BaseURL,
		Key:       key,
		Dimension: config.AcceptedEmbedDimension,
		Dtype:     config.AcceptedEmbedDtype,
	})
	outcome, reason := providercheck.OutcomeForError(err)
	return desktopCheckResult{Outcome: string(outcome), Reason: providercheck.SanitizeReason(reason, key)}, nil
}

// checkTrackerAxis checks a Linear personal API key with one viewer query.
//
// BaseURL overrides the endpoint. Its production use is a proxy; its test
// use is a loopback stub, which is what lets this arm be exercised without
// calling Linear.
func checkTrackerAxis(ctx context.Context, request desktopCheckRequest, credentials *config.Credentials) (desktopCheckResult, error) {
	if request.Provider != "" && request.Provider != "linear" {
		return desktopCheckResult{}, fmt.Errorf("unsupported tracker provider %q", request.Provider)
	}
	key := request.Key
	if key == "" && credentials != nil {
		key = credentials.LinearAPIKey
	}
	if key == "" {
		return desktopCheckResult{Outcome: string(providercheck.Rejected), Reason: "no Linear API key was provided; project tracking is optional, so skip this step to leave it unset"}, nil
	}
	endpoint := request.BaseURL
	if endpoint == "" {
		endpoint = linear.DefaultEndpoint
	}
	client := &linear.Client{APIKey: key, Endpoint: endpoint, HTTP: &http.Client{Timeout: providercheck.APITimeout}}
	err := client.CheckViewer(ctx)
	outcome, reason := trackerOutcome(err)
	return desktopCheckResult{Outcome: string(outcome), Reason: providercheck.SanitizeReason(reason, key)}, nil
}

// trackerOutcome maps the Linear transport's own classified error onto the
// four-word vocabulary. The transport already distinguishes the classes
// that matter (client.go:96-128), so this reads its Reason rather than
// re-deriving anything from an HTTP response.
func trackerOutcome(err error) (providercheck.Outcome, string) {
	if err == nil {
		return providercheck.Accepted, ""
	}
	if backendErr, ok := errors.AsType[*backends.Error](err); ok {
		switch backendErr.Reason {
		case backends.ReasonAuth:
			return providercheck.Rejected, "Linear rejected the key: it may be invalid or revoked"
		case backends.ReasonRateLimited:
			return providercheck.RateLimited, "Linear is rate-limiting this key; retry shortly"
		}
		return providercheck.Unreachable, "Linear could not be reached: " + backendErr.Error()
	}
	return providercheck.Unreachable, "Linear could not be reached: " + err.Error()
}
