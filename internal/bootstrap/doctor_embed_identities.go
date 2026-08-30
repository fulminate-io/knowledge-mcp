// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// doctor_embed_identities.go is where the live-identity cross-check reaches an
// operator.
//
// WHY THE DOCTOR AND NOT config.Load. The check needs the graphs, and
// config.Load has none — it parses a file. The doctor is the surface that
// already has both a config path and a graph client, already reports a config
// error at exit 1, and is what an operator runs when search results look wrong.
// Putting the check at load time would either couple config parsing to a
// running server or make every parse of a config file a network call.
//
// IT IS AN ERROR, NOT A WARNING, and that is the whole point of the check: a
// recorded identity with no constructible embedder means the semantic arm
// cannot run for those graphs, and the alternative to saying so is a search
// that quietly returns BM25 results while reporting success.

// checkEmbedIdentities cross-checks the config against the embed identities the
// local graphs actually carry, and NAMES the graphs that depend on anything it
// cannot construct.
func checkEmbedIdentities(gc *graphclient.GraphClient, configFile string, healthy bool) checkResult {
	if !healthy {
		// NOT A FAILURE: with no server there are no graphs to read, so the check
		// has learned nothing rather than found nothing wrong. Reporting ok here
		// would be the silent pass this whole check exists to prevent.
		return identityResult(statusInfo, "server not running — graph embed identities unknown",
			"run `knowledge start`, then `knowledge doctor` again")
	}
	cfg, err := loadDoctorConfig(configFile)
	if err != nil {
		return identityResult(statusErr, "cannot read config: "+err.Error(), "")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	live, err := tools.RecordedGraphIdentities(ctx, gc.Execute)
	if err != nil {
		return identityResult(statusErr, "cannot enumerate graph embed identities: "+err.Error(), "")
	}
	if res, failed := assembleIdentityCheck(cfg, live); failed {
		return res
	}
	// THE CONFIG CHECK IS NECESSARY BUT NOT SUFFICIENT, and the gap is not
	// hypothetical: it answers "does this config offer a credential and an
	// endpoint for that identity", which is the question about the CONFIG. An arm
	// can still refuse the identity for a reason only the arm knows — a
	// representation it cannot produce, a model it does not serve. Actually
	// building each distinct identity's embedder is the ground truth, it makes no
	// network call, and without it this check can report every identity
	// constructible while a search then fails to embed.
	if detail := buildFailures(ctx, live); detail != "" {
		return identityResult(statusErr,
			"a recorded embed identity cannot be built by its provider arm", detail)
	}
	return assembleOKResult(live)
}

// buildFailures attempts to construct an embedder for every DISTINCT recorded
// identity and returns a joined description of the failures, or "" when all
// build. Grouped by identity for the same reason the config check is: one arm
// failure is one problem however many graphs share the identity.
func buildFailures(ctx context.Context, live []config.LiveGraphIdentity) string {
	seen := map[config.RecordedIdentity][]string{}
	for _, g := range live {
		if g.Identity.Provider == "" {
			continue
		}
		seen[g.Identity] = append(seen[g.Identity], g.String())
	}
	var lines []string
	for id, graphs := range seen {
		sort.Strings(graphs)
		_, err := llmproviders.BuildEmbedderForIdentity(ctx, &knowledgev1.EmbedIdentity{
			Provider:  string(id.Provider),
			Model:     id.Model,
			Dimension: int32(id.Dimension), //nolint:gosec // a width from the accepted set, max 2048
			Dtype:     id.Dtype,
		}, embed.InputRoleQuery)
		if err != nil {
			lines = append(lines, fmt.Sprintf("%s: %v", strings.Join(graphs, ", "), err))
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// assembleIdentityCheck is the CONFIG half, split out so every branch is
// testable without a live server — the same shape assembleStaleness uses. The
// bool reports whether the result is terminal (a failure the caller must return
// rather than continue past).
func assembleIdentityCheck(cfg *config.Config, live []config.LiveGraphIdentity) (checkResult, bool) {
	if err := cfg.ValidateAgainstLiveIdentities(live); err != nil {
		// The count is of IDENTITIES, not graphs: the failures are grouped by
		// identity because the repair is per identity, and one restored profile
		// can fix several graphs at once. The detail below names every graph.
		return identityResult(statusErr,
			fmt.Sprintf("%d recorded embed identity/identities cannot be constructed from this config",
				countErrLines(err)),
			err.Error()), true
	}
	return checkResult{}, false
}

// assembleOKResult renders the passing outcome, distinguishing "nothing has
// embedded yet" from "everything that embedded is constructible" — the two are
// different facts and reporting the first as the second would be the silent pass
// this check exists to prevent.
func assembleOKResult(live []config.LiveGraphIdentity) checkResult {
	recorded := 0
	for _, g := range live {
		if g.Identity.Provider != "" {
			recorded++
		}
	}
	if recorded == 0 {
		return identityResult(statusInfo, "no graph has recorded an embed identity yet", "")
	}
	return identityResult(statusOK,
		fmt.Sprintf("every recorded embed identity is constructible (%d of %d graph(s) embedded)",
			recorded, len(live)), "")
}

// countErrLines reports how many joined errors an aggregate carries, so the
// one-liner can say how many identities failed rather than only that one did.
func countErrLines(err error) int {
	if joined, ok := err.(interface{ Unwrap() []error }); ok { //nolint:errorlint // the join carrier itself, not a wrapped cause
		return len(joined.Unwrap())
	}
	return 1
}

func identityResult(status checkStatus, msg, detail string) checkResult {
	return checkResult{name: "embed-identities", status: status, msg: msg, detail: detail}
}

// doctorConfigPath resolves the config file the doctor judges: the --config-file
// flag when given, else the default under the home directory. An unresolvable
// home yields the empty string, which config.Load then reports on.
//
// SHARED BY BOTH CONFIG-READING CHECKS rather than duplicated, so checkConfig
// and checkEmbedIdentities can never disagree about WHICH config they judged.
func doctorConfigPath(configFile string) string {
	if configFile != "" {
		return configFile
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".knowledge", "config")
}

// loadDoctorConfig resolves the path and parses it.
func loadDoctorConfig(configFile string) (*config.Config, error) {
	path := doctorConfigPath(configFile)
	if path == "" {
		return nil, fmt.Errorf("cannot resolve home dir")
	}
	return config.Load(path)
}
