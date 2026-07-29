// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"reflect"
	"testing"
)

// TestSplitAtDoubleDash asserts the flag-side / remote-command split at the first
// standalone `--`: tokens before it stay on the flag side, tokens after it are the
// verbatim command, a trailing `--` yields an empty (non-nil) command, and no `--`
// leaves everything on the flag side with a nil command.
func TestSplitAtDoubleDash(t *testing.T) {
	cases := []struct {
		name         string
		args         []string
		wantFlagArgs []string
		wantCmd      []string
	}{
		{name: "no double dash", args: []string{"test", "--new"}, wantFlagArgs: []string{"test", "--new"}, wantCmd: nil},
		{name: "command after env", args: []string{"test", "--", "cat", "/root/.knowledge/config"}, wantFlagArgs: []string{"test"}, wantCmd: []string{"cat", "/root/.knowledge/config"}},
		{name: "command carries its own dashes", args: []string{"test", "--", "ls", "-la"}, wantFlagArgs: []string{"test"}, wantCmd: []string{"ls", "-la"}},
		{name: "trailing bare dashdash", args: []string{"test", "--"}, wantFlagArgs: []string{"test"}, wantCmd: []string{}},
		{name: "only the first dashdash splits", args: []string{"test", "--", "echo", "--", "x"}, wantFlagArgs: []string{"test"}, wantCmd: []string{"echo", "--", "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flagArgs, cmd := splitAtDoubleDash(tc.args)
			if !reflect.DeepEqual(flagArgs, tc.wantFlagArgs) {
				t.Errorf("flagArgs = %v, want %v", flagArgs, tc.wantFlagArgs)
			}
			if !reflect.DeepEqual(cmd, tc.wantCmd) {
				t.Errorf("cmd = %v, want %v", cmd, tc.wantCmd)
			}
		})
	}
}

// TestParseTunnelFlags_Interspersed is the regression guard for the bug where a
// flag placed AFTER the env name (`tunnel test --list-sessions`) was never parsed
// by Go's stdlib flag — it stopped at the first non-flag arg, so the trailing
// `--list-sessions` fell through and (before this fix) leaked into the ssh argv,
// which rejected it with "illegal option". parseTunnelFlags permutes around the
// positional so a flag is recognized wherever it sits relative to the env name.
func TestParseTunnelFlags_Interspersed(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantEnv string
		check   func(t *testing.T, tf *tunnelFlags)
		wantErr bool
	}{
		{
			name:    "list-sessions AFTER env (the reported bug)",
			args:    []string{"test", "--list-sessions"},
			wantEnv: "test",
			check:   func(t *testing.T, tf *tunnelFlags) { mustTrue(t, *tf.listSessions, "listSessions") },
		},
		{
			name:    "list-sessions before env",
			args:    []string{"--list-sessions", "test"},
			wantEnv: "test",
			check:   func(t *testing.T, tf *tunnelFlags) { mustTrue(t, *tf.listSessions, "listSessions") },
		},
		{
			name:    "new AFTER env",
			args:    []string{"test", "--new"},
			wantEnv: "test",
			check:   func(t *testing.T, tf *tunnelFlags) { mustTrue(t, *tf.newSession, "newSession") },
		},
		{
			name:    "reuse with value AFTER env",
			args:    []string{"test", "--reuse", "s-abc"},
			wantEnv: "test",
			check:   func(t *testing.T, tf *tunnelFlags) { mustEq(t, *tf.reuse, "s-abc", "reuse") },
		},
		{
			name:    "reuse with value BEFORE env",
			args:    []string{"--reuse", "s-abc", "test"},
			wantEnv: "test",
			check:   func(t *testing.T, tf *tunnelFlags) { mustEq(t, *tf.reuse, "s-abc", "reuse") },
		},
		{
			name:    "env only (interactive)",
			args:    []string{"test"},
			wantEnv: "test",
			check:   func(t *testing.T, tf *tunnelFlags) {},
		},
		{
			name:    "no args at all",
			args:    nil,
			wantEnv: "",
			check:   func(t *testing.T, tf *tunnelFlags) {},
		},
		{
			name:    "proxy flag AFTER env",
			args:    []string{"test", "--proxy"},
			wantEnv: "test",
			check:   func(t *testing.T, tf *tunnelFlags) { mustTrue(t, *tf.proxy, "proxy") },
		},
		{
			name:    "kill with value AFTER env",
			args:    []string{"test", "--kill", "harness"},
			wantEnv: "test",
			check:   func(t *testing.T, tf *tunnelFlags) { mustEq(t, *tf.kill, "harness", "kill") },
		},
		{
			name:    "rename old=new AFTER env",
			args:    []string{"test", "--rename", "cli-host=feature"},
			wantEnv: "test",
			check:   func(t *testing.T, tf *tunnelFlags) { mustEq(t, *tf.rename, "cli-host=feature", "rename") },
		},
		{
			name:    "unknown flag errors clearly (not leaked to ssh)",
			args:    []string{"test", "--bogus"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs, tf := newTunnelFlagSet()
			env, err := parseTunnelFlags(fs, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseTunnelFlags(%v) = %q, want an error (an unrecognized flag must fail, not pass through)", tc.args, env)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTunnelFlags(%v): unexpected error %v", tc.args, err)
			}
			if env != tc.wantEnv {
				t.Errorf("env = %q, want %q", env, tc.wantEnv)
			}
			tc.check(t, tf)
		})
	}
}

func mustTrue(t *testing.T, got bool, name string) {
	t.Helper()
	if !got {
		t.Errorf("%s = false, want true (flag was not parsed)", name)
	}
}

func mustEq(t *testing.T, got, want, name string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}
