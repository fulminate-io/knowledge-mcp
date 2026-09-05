// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// TestAutoUpdateGate_ResolvesAcrossConfigFlagAndEnv drives the disable
// predicate across every source and combination.
//
// The ENABLED row is the discriminating control: without it a predicate
// hard-wired to "disabled" would satisfy every other row in this table.
func TestAutoUpdateGate_ResolvesAcrossConfigFlagAndEnv(t *testing.T) {
	cases := []struct {
		name       string
		configAuto *bool // nil = the auto_update key is absent
		flag       bool  // --no-auto-update
		wantEnable bool
	}{
		{name: "config key absent and no flag — the default is ON", configAuto: nil, flag: false, wantEnable: true},
		{name: "config auto_update=false disables", configAuto: new(false), flag: false, wantEnable: false},
		{name: "flag disables with the config key absent", configAuto: nil, flag: true, wantEnable: false},
		{name: "flag disables even when config auto_update=true — a disable from EITHER source wins", configAuto: new(true), flag: true, wantEnable: false},
		{name: "config auto_update=true with no flag stays ON", configAuto: new(true), flag: false, wantEnable: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(config.SetForTest(&config.Config{AutoUpdate: tc.configAuto}))
			got := autoUpdateEnabled(Config{NoAutoUpdate: tc.flag})
			if got != tc.wantEnable {
				t.Errorf("autoUpdateEnabled = %v, want %v", got, tc.wantEnable)
			}
		})
	}

	t.Run("KNOWLEDGE_NO_AUTO_UPDATE set true-ish disables via the flag default", func(t *testing.T) {
		t.Setenv(envNoAutoUpdate, "1")
		t.Cleanup(config.SetForTest(&config.Config{}))
		v, err := noAutoUpdateFromEnv()
		if err != nil {
			t.Fatalf("noAutoUpdateFromEnv: %v", err)
		}
		if !v {
			t.Fatalf("%s=1 must read as a disable", envNoAutoUpdate)
		}
		if autoUpdateEnabled(Config{NoAutoUpdate: v}) {
			t.Errorf("the environment disable did not reach the predicate")
		}
	})

	t.Run("an unset environment variable is an absence, not a disable", func(t *testing.T) {
		// KNOWN-POSITIVE for the environment arm specifically: without it, a
		// reader that treated every LookUp result as true would still satisfy
		// the disable row above.
		t.Setenv(envNoAutoUpdate, "")
		v, err := noAutoUpdateFromEnv()
		if err != nil {
			t.Fatalf("noAutoUpdateFromEnv: %v", err)
		}
		if v {
			t.Errorf("an empty %s must read as absent, not as a disable", envNoAutoUpdate)
		}
	})

	t.Run("a non-boolean environment value is a startup error naming the value and the vocabulary", func(t *testing.T) {
		t.Setenv(envNoAutoUpdate, "yes-please")
		err := checkAutoUpdateEnv()
		if err == nil {
			t.Fatalf("a non-boolean %s must be a startup error, never a silent default", envNoAutoUpdate)
		}
		for _, want := range []string{"yes-please", "true", "false"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the error must name the offending value and the accepted vocabulary; %q omits %q", err.Error(), want)
			}
		}
		// And it must actually reach a parse entry point rather than only
		// existing as a helper.
		if _, perr := ParseFlags(nil); perr == nil {
			t.Errorf("ParseFlags must surface the malformed environment value")
		}
	})

	t.Run("an unloaded config reads as absent, which is enabled", func(t *testing.T) {
		t.Cleanup(config.SetForTest(nil))
		if !autoUpdateEnabled(Config{}) {
			t.Errorf("a degraded boot that could not read the config file must not silently disable updates")
		}
	})
}
