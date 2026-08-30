// SPDX-License-Identifier: Apache-2.0

package calibration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	gogithub "github.com/google/go-github/v68/github"
	"golang.org/x/oauth2"
)

// envCalibrate gates every live test in this package. It must equal "1".
const envCalibrate = "CODEQL_CALIBRATE"

// liveToken resolves the API token the way the CI/CD collector does: GITHUB_TOKEN
// first, GH_TOKEN as the alternate spelling. Once a live run was explicitly
// requested a missing token is a FAILURE naming both variables, not a skip — the
// operator asked for a live run and a silent degrade would report a clean pass
// for a run that never happened.
func liveToken(t *testing.T) string {
	t.Helper()
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	if token == "" {
		t.Fatalf("%s=1 was requested but neither GITHUB_TOKEN nor GH_TOKEN is set", envCalibrate)
	}
	return token
}

// liveCodeScanning builds an authenticated code-scanning client.
func liveCodeScanning(ctx context.Context, token string) codeScanningAPI {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	return gogithub.NewClient(oauth2.NewClient(ctx, ts)).CodeScanning
}

// TestFreezeAlertCorpusLive fetches the live alert corpus and writes it to
// testdata. It is the GENERATOR for the committed artifact, not a gate:
//
//	CODEQL_CALIBRATE=1 go test ./internal/topology/calibration/ -run '^TestFreezeAlertCorpusLive$' -v -count=1
//
// The generated file MUST be committed. Without it the hermetic guard, the
// path-map alert-path table and the live calibration run are red on every
// machine including CI.
// UNVERIFIED IN CI: that the frozen corpus still matches the live mirror. This
// test performs the only network fetch in the package and runs on no standing
// gate, so drift between testdata and the mirror's real alert set is detected by
// an operator running it deliberately and by nothing else. What IS proven
// unconditionally is that the committed corpus holds its measured floor and both
// rule ids — TestFrozenAlertCorpusMatchesConstant, which reads only testdata.
func TestFreezeAlertCorpusLive(t *testing.T) {
	if os.Getenv(envCalibrate) != "1" {
		t.Skipf("set %s=1 to run the live alert fetch", envCalibrate)
	}
	ctx := context.Background()
	api := liveCodeScanning(ctx, liveToken(t))

	sites, err := FetchAlertSites(ctx, api, MirrorOwner, MirrorRepo)
	if err != nil {
		t.Fatalf("FetchAlertSites: %v", err)
	}

	// Refuse to overwrite a good artifact with a shrunken one. A fetch below
	// the floor is a truncated fetch, and writing it would quietly replace the
	// ground truth with a smaller one that every later score flatters itself
	// against.
	if len(sites) < frozenAlertFloor {
		t.Fatalf("live fetch returned %d sites, below the measured floor of %d — refusing to overwrite the frozen corpus", len(sites), frozenAlertFloor)
	}

	raw, err := json.MarshalIndent(sites, "", "  ")
	if err != nil {
		t.Fatalf("marshal corpus: %v", err)
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(frozenCorpusPath), 0o750); err != nil {
		t.Fatalf("create testdata dir: %v", err)
	}
	if err := os.WriteFile(frozenCorpusPath, raw, 0o600); err != nil {
		t.Fatalf("write frozen corpus: %v", err)
	}
	t.Logf("froze %d alert sites to %s", len(sites), frozenCorpusPath)
}
