// Copyright (C) 2017-2026 The Rune Authors
// SPDX-License-Identifier: GPL-3.0-or-later
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or (at
// your option) any later version.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term/progstatus"
)

func newStatusTTY(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tty")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	return path
}

func readStatusTTY(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func TestStatus(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantTTY string
		wantOut string
		wantErr string
	}{
		{
			name:    "idle",
			args:    []string{"status", "idle"},
			wantTTY: "\x1b]7501;state=idle\x1b\\",
		},
		{
			name:    "working/message",
			args:    []string{"status", "working", "Installing updates"},
			wantTTY: "\x1b]7501;state=working:msg=SW5zdGFsbGluZyB1cGRhdGVz\x1b\\",
		},
		{
			name:    "working/progress",
			args:    []string{"status", "working", "--progress", "40", "--id", "us-east", "--title", "US East", "Pushing image"},
			wantTTY: "\x1b]7501;state=working:id=us-east:progress=40:title=VVMgRWFzdA==:msg=UHVzaGluZyBpbWFnZQ==\x1b\\",
		},
		{
			name:    "working/progress-zero",
			args:    []string{"status", "working", "--progress", "0"},
			wantTTY: "\x1b]7501;state=working:progress=0\x1b\\",
		},
		{
			name:    "blocked/permission",
			args:    []string{"status", "blocked", "--kind", "permission", "--app", "terraform", "Apply 3 to add, 1 to change, 0 to destroy?"},
			wantTTY: "\x1b]7501;state=blocked:kind=permission:app=terraform:msg=QXBwbHkgMyB0byBhZGQsIDEgdG8gY2hhbmdlLCAwIHRvIGRlc3Ryb3k/\x1b\\",
		},
		{
			name:    "blocked/auth-progress",
			args:    []string{"status", "blocked", "--kind", "auth", "--progress", "100"},
			wantTTY: "\x1b]7501;state=blocked:kind=auth:progress=100\x1b\\",
		},
		{
			name:    "done",
			args:    []string{"status", "done", "--app", "brew", "Upgraded 12 packages"},
			wantTTY: "\x1b]7501;state=done:app=brew:msg=VXBncmFkZWQgMTIgcGFja2FnZXM=\x1b\\",
		},
		{
			name:    "error",
			args:    []string{"status", "error", "rsync failed"},
			wantTTY: "\x1b]7501;state=error:msg=cnN5bmMgZmFpbGVk\x1b\\",
		},
		{
			name:    "clear/all",
			args:    []string{"status", "clear"},
			wantTTY: "\x1b]7501;state=clear\x1b\\",
		},
		{
			name:    "clear/id",
			args:    []string{"status", "clear", "--id", "build/test"},
			wantTTY: "\x1b]7501;state=clear:id=build/test\x1b\\",
		},
		{
			name:    "bel",
			args:    []string{"status", "idle", "--bel"},
			wantTTY: "\x1b]7501;state=idle\x07",
		},
		{
			name:    "json",
			args:    []string{"status", "idle", "-F", "json"},
			wantTTY: "\x1b]7501;state=idle\x1b\\",
			wantOut: "{\"success\":true}\n",
		},
		{
			name:    "unicode-message",
			args:    []string{"status", "done", "Déployé ✓"},
			wantTTY: "\x1b]7501;state=done:msg=RMOpcGxvecOpIOKckw==\x1b\\",
		},
		{
			name:    "err/state",
			args:    []string{"status", "busy"},
			wantErr: `invalid state "busy"`,
		},
		{
			name:    "err/kind-value",
			args:    []string{"status", "blocked", "--kind", "waiting"},
			wantErr: `invalid kind "waiting"`,
		},
		{
			name:    "err/kind-state",
			args:    []string{"status", "working", "--kind", "auth"},
			wantErr: "kind only applies to the blocked state",
		},
		{
			name:    "err/progress-range",
			args:    []string{"status", "working", "--progress", "101"},
			wantErr: "invalid progress 101",
		},
		{
			name:    "err/progress-state",
			args:    []string{"status", "done", "--progress", "50"},
			wantErr: "progress only applies to the working and blocked states",
		},
		{
			name:    "err/id-chars",
			args:    []string{"status", "idle", "--id", "a b"},
			wantErr: `invalid id "a b"`,
		},
		{
			name:    "err/id-empty-segment",
			args:    []string{"status", "idle", "--id", "a//b"},
			wantErr: `invalid id "a//b"`,
		},
		{
			name:    "err/id-segment-length",
			args:    []string{"status", "idle", "--id", strings.Repeat("x", 33)},
			wantErr: "invalid id",
		},
		{
			name:    "err/id-depth",
			args:    []string{"status", "idle", "--id", strings.Repeat("a/", 8) + "a"},
			wantErr: "9 levels, over the 8 level limit",
		},
		{
			name:    "err/id-length",
			args:    []string{"status", "idle", "--id", strings.Repeat(strings.Repeat("x", 32)+"/", 3) + strings.Repeat("x", 30)},
			wantErr: "129 bytes, over the 128 byte limit",
		},
		{
			name:    "err/app",
			args:    []string{"status", "idle", "--app", "my app"},
			wantErr: `invalid app "my app"`,
		},
		{
			name:    "err/app-length",
			args:    []string{"status", "idle", "--app", strings.Repeat("a", 33)},
			wantErr: "invalid app",
		},
		{
			name:    "err/message-control",
			args:    []string{"status", "done", "line1\nline2"},
			wantErr: "message contains control character U+000A",
		},
		{
			name:    "err/message-c1",
			args:    []string{"status", "done", "bad\u0085char"},
			wantErr: "message contains control character U+0085",
		},
		{
			name:    "err/message-length",
			args:    []string{"status", "done", strings.Repeat("m", 2049)},
			wantErr: "message is 2049 bytes, over the 2048 byte limit",
		},
		{
			name:    "err/title-length",
			args:    []string{"status", "done", "--title", strings.Repeat("t", 193)},
			wantErr: "title is 193 bytes, over the 192 byte limit",
		},
		{
			name:    "err/title-control",
			args:    []string{"status", "done", "--title", "a\tb"},
			wantErr: "title contains control character U+0009",
		},
		{
			name:    "err/args",
			args:    []string{"status"},
			wantErr: "accepts between 1 and 2 arg(s), received 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t)
			defer env.cleanup()
			tty := newStatusTTY(t)

			out, err := env.run(append(tt.args, "--tty", tty)...)

			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				require.Empty(t, readStatusTTY(t, tty))
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantOut, out)
			require.Equal(t, tt.wantTTY, readStatusTTY(t, tty))
		})
	}
}

func TestStatusWrapHelperProcess(t *testing.T) {
	if os.Getenv("RUNECTL_STATUS_HELPER") != "1" {
		return
	}
	switch os.Getenv("RUNECTL_STATUS_HELPER_MODE") {
	case "exit3":
		os.Exit(3)
	case "signal":
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		time.Sleep(5 * time.Second)
	}
	os.Exit(0)
}

func TestStatusWrap(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		args     []string
		wantTTY  string
		wantCode int
	}{
		{
			name: "done",
			mode: "exit0",
			wantTTY: "\x1b]7501;state=working:msg=YnVpbGQ=\x1b\\" +
				"\x1b]7501;state=done:msg=YnVpbGQ=\x1b\\",
		},
		{
			name: "done/app-bel",
			mode: "exit0",
			args: []string{"--app", "make", "--bel"},
			wantTTY: "\x1b]7501;state=working:app=make:msg=YnVpbGQ=\x07" +
				"\x1b]7501;state=done:app=make:msg=YnVpbGQ=\x07",
		},
		{
			name: "error",
			mode: "exit3",
			wantTTY: "\x1b]7501;state=working:msg=YnVpbGQ=\x1b\\" +
				"\x1b]7501;state=error:msg=ZXhpdCBzdGF0dXMgMw==\x1b\\",
			wantCode: 3,
		},
		{
			name: "signaled",
			mode: "signal",
			wantTTY: "\x1b]7501;state=working:msg=YnVpbGQ=\x1b\\" +
				"\x1b]7501;state=error:msg=c2lnbmFsOiB0ZXJtaW5hdGVk\x1b\\",
			wantCode: 128 + int(syscall.SIGTERM),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t)
			defer env.cleanup()
			tty := newStatusTTY(t)
			t.Setenv("RUNECTL_STATUS_HELPER", "1")
			t.Setenv("RUNECTL_STATUS_HELPER_MODE", tt.mode)

			args := append([]string{"status", "wrap", "--tty", tty, "--msg", "build"}, tt.args...)
			args = append(args, "--", os.Args[0], "-test.run=TestStatusWrapHelperProcess")
			_, err := env.run(args...)

			if tt.wantCode == 0 {
				require.NoError(t, err)
			} else {
				var exit exitError
				require.ErrorAs(t, err, &exit)
				require.Equal(t, tt.wantCode, exit.code)
			}
			require.Equal(t, tt.wantTTY, readStatusTTY(t, tty))
		})
	}
}

func TestStatusWrapStartFailure(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()
	tty := newStatusTTY(t)

	_, err := env.run("status", "wrap", "--tty", tty, "--", filepath.Join(t.TempDir(), "missing"))

	require.Error(t, err)
	var exit exitError
	require.False(t, errors.As(err, &exit))
	got := readStatusTTY(t, tty)
	require.True(t, strings.HasPrefix(got, "\x1b]7501;state=working:msg="), got)
	require.Contains(t, got, "\x1b]7501;state=error:msg=")
}

func TestStatusWrapDefaultMessage(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()
	tty := newStatusTTY(t)
	t.Setenv("RUNECTL_STATUS_HELPER", "1")
	t.Setenv("RUNECTL_STATUS_HELPER_MODE", "exit0")

	_, err := env.run("status", "wrap", "--tty", tty, "--", os.Args[0], "-test.run=TestStatusWrapHelperProcess")

	require.NoError(t, err)
	wantMsg := os.Args[0] + " -test.run=TestStatusWrapHelperProcess"
	working := progstatus.Report{State: progstatus.Working, Msg: wantMsg}
	done := progstatus.Report{State: progstatus.Done, Msg: wantMsg}
	require.Equal(t, working.Encode(false)+done.Encode(false), readStatusTTY(t, tty))
}

func TestStatusDemo(t *testing.T) {
	// ramp is a working report played with its progress going from 0 to
	// 100% in 5% steps.
	ramp := func(r progstatus.Report) []progstatus.Report {
		var out []progstatus.Report
		for p := 0; p <= 100; p += 5 {
			r.Progress = progstatus.Percent(p)
			out = append(out, r)
		}
		return out
	}
	concat := func(parts ...[]progstatus.Report) []progstatus.Report {
		var out []progstatus.Report
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	one := func(r progstatus.Report) []progstatus.Report { return []progstatus.Report{r} }

	tests := []struct {
		name     string
		scenario string
		want     []progstatus.Report
	}{
		{
			name:     "brew",
			scenario: "brew",
			want: concat(
				one(progstatus.Report{State: progstatus.Working, App: "brew", Msg: "Installing updates"}),
				one(progstatus.Report{State: progstatus.Blocked, Kind: progstatus.Auth, App: "brew", Msg: "Password required to install updates"}),
				ramp(progstatus.Report{State: progstatus.Working, App: "brew", Msg: "Installing updates"}),
				one(progstatus.Report{State: progstatus.Done, App: "brew", Msg: "Upgraded 12 packages"}),
			),
		},
		{
			name:     "deploy",
			scenario: "deploy",
			want: concat(
				one(progstatus.Report{State: progstatus.Working, App: "deploy", Msg: "Deploying v2.4.1"}),
				ramp(progstatus.Report{State: progstatus.Working, ID: "us-east", Title: "US East", Msg: "Pushing image"}),
				one(progstatus.Report{State: progstatus.Blocked, Kind: progstatus.Permission, ID: "eu-west", Title: "EU West", Msg: "Approve deploy to eu-west (production)?"}),
				one(progstatus.Report{State: progstatus.Done, ID: "us-east", Title: "US East", Msg: "Healthy"}),
				one(progstatus.Report{State: progstatus.Clear, ID: "us-east"}),
				one(progstatus.Report{State: progstatus.Clear, ID: "eu-west"}),
				one(progstatus.Report{State: progstatus.Done, App: "deploy", Msg: "Deployed to 3 regions"}),
			),
		},
		{
			name:     "rsync",
			scenario: "rsync",
			want: concat(
				ramp(progstatus.Report{State: progstatus.Working, Msg: "Syncing photos"}),
				one(progstatus.Report{State: progstatus.Done, Msg: "Photos synced"}),
			),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t)
			defer env.cleanup()
			tty := newStatusTTY(t)

			out, err := env.run("status", "demo", "--scenario", tt.scenario,
				"--speed", "0", "--progress-duration", "0", "--tty", tty)

			require.NoError(t, err)
			var wantOut, wantTTY strings.Builder
			for _, r := range tt.want {
				wantOut.WriteString(r.String())
				wantOut.WriteByte('\n')
				wantTTY.WriteString(r.Encode(false))
			}
			require.Equal(t, wantOut.String(), out)
			require.Equal(t, wantTTY.String(), readStatusTTY(t, tty))
		})
	}
}

func TestStatusDemoProgressDuration(t *testing.T) {
	def, err := time.ParseDuration(newStatusDemoCmd().Flags().Lookup("progress-duration").DefValue)
	require.NoError(t, err)
	require.GreaterOrEqual(t, def, 5*time.Second)
	require.LessOrEqual(t, def, 10*time.Second)

	env := newTestEnv(t)
	defer env.cleanup()
	start := time.Now()
	_, err = env.run("status", "demo", "--scenario", "rsync",
		"--speed", "0", "--progress-duration", "200ms", "--tty", newStatusTTY(t))
	require.NoError(t, err)
	require.GreaterOrEqual(t, time.Since(start), 200*time.Millisecond,
		"the ramp spreads over the progress duration")
}

func TestStatusDemoMatchesSpecExamples(t *testing.T) {
	want := map[string][]string{
		"brew": {
			"\x1b]7501;state=working:app=brew:msg=SW5zdGFsbGluZyB1cGRhdGVz\x1b\\",
			"\x1b]7501;state=blocked:kind=auth:app=brew:msg=UGFzc3dvcmQgcmVxdWlyZWQgdG8gaW5zdGFsbCB1cGRhdGVz\x1b\\",
			"\x1b]7501;state=working:app=brew:msg=SW5zdGFsbGluZyB1cGRhdGVz\x1b\\",
			"\x1b]7501;state=done:app=brew:msg=VXBncmFkZWQgMTIgcGFja2FnZXM=\x1b\\",
		},
		"deploy": {
			"\x1b]7501;state=working:app=deploy:msg=RGVwbG95aW5nIHYyLjQuMQ==\x1b\\",
			"\x1b]7501;state=working:id=us-east:progress=40:title=VVMgRWFzdA==:msg=UHVzaGluZyBpbWFnZQ==\x1b\\",
			"\x1b]7501;state=blocked:id=eu-west:kind=permission:title=RVUgV2VzdA==:msg=QXBwcm92ZSBkZXBsb3kgdG8gZXUtd2VzdCAocHJvZHVjdGlvbik/\x1b\\",
			"\x1b]7501;state=done:id=us-east:title=VVMgRWFzdA==:msg=SGVhbHRoeQ==\x1b\\",
			"\x1b]7501;state=clear:id=us-east\x1b\\",
			"\x1b]7501;state=clear:id=eu-west\x1b\\",
			"\x1b]7501;state=done:app=deploy:msg=RGVwbG95ZWQgdG8gMyByZWdpb25z\x1b\\",
		},
	}
	for scenario, seqs := range want {
		steps := statusDemoScenarios[scenario]
		require.Len(t, steps, len(seqs), scenario)
		for i, r := range steps {
			require.Equal(t, seqs[i], r.Encode(false), "%s step %d", scenario, i)
		}
	}
}

func TestStatusDemoInvalidScenario(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	_, err := env.run("status", "demo", "--scenario", "nope", "--tty", newStatusTTY(t))

	require.Error(t, err)
	require.Contains(t, err.Error(), `invalid scenario "nope"`)
}

func TestStatusQueryFile(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	_, err := env.run("status", "query", "--tty", newStatusTTY(t), "--timeout", "50ms")

	require.ErrorIs(t, err, errStatusUnsupported)
}
