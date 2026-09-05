// SPDX-License-Identifier: Apache-2.0

// update_gate.go — the single home for the question "may this daemon update
// itself?". Three surfaces can answer it and a disable from ANY of them wins.

package bootstrap

import (
	"fmt"
	"os"
	"strconv"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// envNoAutoUpdate is the environment surface of the disable switch.
const envNoAutoUpdate = "KNOWLEDGE_NO_AUTO_UPDATE"

// noAutoUpdateFromEnv reads the environment surface.
//
// An UNSET or empty variable is not a disable — it is an absence, and the
// default is enabled. A non-empty value that does not parse as a boolean is an
// ERROR naming the value and the accepted vocabulary, never a silent default:
// this repo does not coerce bad input, and a typo'd disable that silently left
// updates ON is precisely the surprise the switch exists to prevent.
func noAutoUpdateFromEnv() (bool, error) {
	raw, ok := os.LookupEnv(envNoAutoUpdate)
	if !ok || raw == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s=%q is not a boolean; accepted values are 1, t, T, true, TRUE, True, 0, f, F, false, FALSE, False", envNoAutoUpdate, raw)
	}
	return v, nil
}

// checkAutoUpdateEnv surfaces a malformed environment value as a startup error.
//
// It exists as its own call because registerConfigFlags has no way to return an
// error — it registers flags into a FlagSet — so it takes the parsed value and
// leaves the diagnosis to this function. That is not a swallow: EVERY parse
// entry point that registers the flag also calls this, so a malformed value
// cannot reach a running daemon.
func checkAutoUpdateEnv() error {
	_, err := noAutoUpdateFromEnv()
	return err
}

// autoUpdateEnabled answers whether the background update loop may run.
//
// TWO SURFACES, and a disable from EITHER wins:
//   - the bootstrap Config's NoAutoUpdate, fed by --no-auto-update and by the
//     environment variable that flag defaults from;
//   - the loaded ~/.knowledge/config's auto_update key, where nil (absent)
//     means enabled and only an explicit false disables.
//
// The direction is deliberate and it is the safe one: a user who has said stop
// on any surface is obeyed, and the two can never combine into an unexpected
// enable. It also means the config file — the ONLY surface a launchd- or
// systemd-managed daemon's user can reach, because such a daemon is launched
// with a fixed argv — can always turn the loop off.
//
// It reads the config singleton rather than taking it as a parameter because
// loadBootConfig has already installed it by the time any caller here runs; an
// unloaded config (a test, or a degraded boot that could not read the file)
// reads as absent, which is enabled.
func autoUpdateEnabled(f Config) bool {
	if f.NoAutoUpdate {
		return false
	}
	if !config.Loaded() {
		return true
	}
	if v := config.Active().AutoUpdate; v != nil && !*v {
		return false
	}
	return true
}
