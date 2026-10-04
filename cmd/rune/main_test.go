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
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	flag "github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"

	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/workspace/workspacessh"
)

func TestCheckModeArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		files   []string
		wantErr string
	}{
		{name: "gui alone", args: []string{"--gui"}},
		{name: "headless alone", args: []string{"--headless"}},
		{name: "headless with its own flags", args: []string{"--headless", "-c", "/c.yaml", "-d", "/d"}},
		{name: "tui with workspace and files", args: []string{"--tui", "-w", "/src"}, files: []string{"a.go"}},
		{
			name:    "two modes",
			args:    []string{"--tui", "-G"},
			wantErr: "only one of --gui, --tui or --headless can be passed at once, got --gui --tui",
		},
		{
			name:    "headless with editor flags",
			args:    []string{"--headless", "-w", "/src", "-f"},
			wantErr: "--headless does not take --fps, --workspace",
		},
		{
			name:    "headless with files",
			args:    []string{"--headless"},
			files:   []string{"a.go"},
			wantErr: "--headless does not take file arguments",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs, gui, tui, headless := parseModeFlags(t, tt.args)
			err := checkModeArgs(fs, gui, tui, headless, tt.files)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("checkModeArgs() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("checkModeArgs() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestVersionString(t *testing.T) {
	tests := []struct {
		name      string
		tag       string
		commit    string
		buildDate string
		want      string
	}{
		{
			name:      "with build date",
			tag:       "v1.2.3",
			commit:    "abc1234",
			buildDate: "2026-09-11T13:12:48Z",
			want:      "v1.2.3 (HEAD is abc1234, built 2026-09-11T13:12:48Z)",
		},
		{
			name:      "without build date",
			tag:       "v1.2.3",
			commit:    "abc1234",
			buildDate: "",
			want:      "v1.2.3 (HEAD is abc1234)",
		},
		{
			name:      "development defaults",
			tag:       "development",
			commit:    "HEAD",
			buildDate: "",
			want:      "development (HEAD is HEAD)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := versionString(tt.tag, tt.commit, tt.buildDate)
			if got != tt.want {
				t.Errorf("versionString() = %q, want %q", got, tt.want)
			}
		})
	}
}

// parseModeFlags parses args on a fresh flag set so the test does not
// mutate the process-wide one.
func parseModeFlags(
	t *testing.T, args []string,
) (fs *flag.FlagSet, gui, tui, headless bool) {
	t.Helper()
	fs = flag.NewFlagSet("rune", flag.ContinueOnError)
	guiFlag := fs.BoolP("gui", "G", false, "")
	tuiFlag := fs.Bool("tui", false, "")
	headlessFlag := fs.Bool("headless", false, "")
	fs.StringP("config", "c", "", "")
	fs.StringP("datadir", "d", "", "")
	fs.StringP("workspace", "w", "", "")
	fs.BoolP("fps", "f", false, "")
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return fs, *guiFlag, *tuiFlag, *headlessFlag
}

// A misspelt entry in headlessFlags would reject a flag the node needs.
func TestHeadlessFlagsAreRuneFlags(t *testing.T) {
	for name := range headlessFlags {
		if flag.CommandLine.Lookup(name) == nil {
			t.Errorf("headlessFlags names %q, which is not a rune flag", name)
		}
	}
}

// TestVersionStringUsesBuildStampLayout pins the ldflag -> --version
// contract: a stamp in the exact form cmd/buildstamp emits must reach
// the version line verbatim. Without a reader the linker prunes
// debug.BuildDate and the -X in the Makefile becomes a silent no-op.
func TestVersionStringUsesBuildStampLayout(t *testing.T) {
	stamp := time.Date(2026, 9, 11, 13, 12, 48, 0, time.UTC).
		Format(debug.BuildDateLayout)

	got := versionString("v1.2.3", "abc1234", stamp)

	if !strings.Contains(got, stamp) {
		t.Errorf("versionString() = %q, want it to contain the stamp %q", got, stamp)
	}
}

func TestAppLaunchArgs(t *testing.T) {
	tests := []struct {
		name     string
		goos     string
		wantArgs []string
		wantOK   bool
	}{
		{name: "darwin", goos: "darwin", wantArgs: []string{"-G", "-w", ""}, wantOK: true},
		{name: "linux", goos: "linux", wantArgs: []string{"-G", "-w", ""}, wantOK: true},
		{name: "windows", goos: "windows", wantArgs: []string{"-G", "-w", ""}, wantOK: true},
		{name: "freebsd unsupported", goos: "freebsd", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotArgs, gotOK := appLaunchArgs(tt.goos)
			if gotOK != tt.wantOK {
				t.Fatalf("appLaunchArgs() ok = %v, want %v", gotOK, tt.wantOK)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Fatalf("appLaunchArgs() = %#v, want %#v", gotArgs, tt.wantArgs)
			}
		})
	}
}

// A data dir the dotfiles cannot be written to must not pass silently:
// terminal modal mode then breaks in zsh and bash, and the user has to be
// told why.
func TestInstallShellRCUnwritableDataDir(t *testing.T) {
	// A path below a regular file fails even as root.
	dataDir := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(dataDir, nil, 0o644))

	dir, err := installShellRC(dataDir)
	assert.Empty(t, dir, "shells must not be pointed at a missing directory")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminal modal mode")
}

func TestReportShellRCErr(t *testing.T) {
	err := errors.New("terminal modal mode may not work: disk full")

	t.Run("notifies the local user", func(t *testing.T) {
		n := &recordingNotifications{}
		reportShellRCErr(n, err)
		require.Len(t, n.notes, 1)
		assert.Equal(t, browserapi.LevelWarn, n.notes[0].level)
		assert.Contains(t, n.notes[0].msg, "disk full")
	})

	// rune -x has no UI of its own: the local side turns its provisioning
	// stream into notifications.
	t.Run("streams a failure to the local side of rune -x", func(t *testing.T) {
		var stderr bytes.Buffer
		reportRemoteShellRCErr(&stderr, err)
		p, ok := workspacessh.ParseProvisionProgressLine(
			bytes.TrimRight(stderr.Bytes(), "\n"))
		require.True(t, ok, "not a provisioning line: %q", stderr.String())
		assert.Equal(t, workspacessh.NotificationWarning, p.Level())
		assert.Contains(t, p.Message(), "disk full")
	})
}

type recordedNote struct {
	level browserapi.NotificationLevel
	msg   string
}

type recordingNotifications struct {
	browserapi.Notifications
	notes []recordedNote
}

func (r *recordingNotifications) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	r.notes = append(r.notes, recordedNote{level, fmt.Sprintf(msg, args...)})
	return "", nil
}

func TestResolveDefaultConfigPathPrefersYAMLThenStar(t *testing.T) {
	dataDir := t.TempDir()
	yamlPath := filepath.Join(dataDir, "config.yaml")
	starPath := filepath.Join(dataDir, "config.star")

	got := resolveDefaultConfigPath(dataDir)
	if got != yamlPath {
		t.Fatalf("resolveDefaultConfigPath() = %q, want %q when neither exists",
			got, yamlPath)
	}

	if err := os.WriteFile(starPath, []byte("config = {}\n"), 0o644); err != nil {
		t.Fatalf("write star: %v", err)
	}
	got = resolveDefaultConfigPath(dataDir)
	if got != starPath {
		t.Fatalf("resolveDefaultConfigPath() = %q, want %q when only .star exists",
			got, starPath)
	}

	if err := os.WriteFile(yamlPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	got = resolveDefaultConfigPath(dataDir)
	if got != yamlPath {
		t.Fatalf("resolveDefaultConfigPath() = %q, want %q when both exist",
			got, yamlPath)
	}
}

func TestResolveDefaultConfigPathUsesDatadirDefaultLocation(t *testing.T) {
	dataDir := filepath.Join("home", ".rune")
	got := resolveDefaultConfigPath(dataDir)
	want := filepath.Join(dataDir, "config.yaml")
	if got != want {
		t.Fatalf("resolveDefaultConfigPath() = %q, want %q", got, want)
	}
}

func TestDataPathDefaultsUseOSPaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{"datadir", defaultDataPath, filepath.Join(home, ".rune")},
		{"workspace server log", flag.Lookup("workspace-server-log").DefValue,
			filepath.Join(home, ".rune", "server.log")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("default = %q, want %q", tc.got, tc.want)
			}
		})
	}
}
