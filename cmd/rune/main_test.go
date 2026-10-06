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

func TestHeadlessFlagsAreRuneFlags(t *testing.T) {
	for name := range headlessFlags {
		if flag.CommandLine.Lookup(name) == nil {
			t.Errorf("headlessFlags names %q, which is not a rune flag", name)
		}
	}
}

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

	// rune -x has no UI of its own: the local side shows its warnings.
	t.Run("warns the local side of rune -x", func(t *testing.T) {
		var stderr bytes.Buffer
		reportRemoteShellRCErr(&stderr, err)
		msg, ok := workspacessh.ParseWarningLine(
			bytes.TrimRight(stderr.Bytes(), "\n"))
		require.True(t, ok, "not a warning line: %q", stderr.String())
		assert.Contains(t, msg, "disk full")
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

func TestConfigPathForDirPrefersYAMLThenStar(t *testing.T) {
	dataDir := t.TempDir()
	yamlPath := filepath.Join(dataDir, "config.yaml")
	starPath := filepath.Join(dataDir, "config.star")

	got := configPathForDir(dataDir)
	if got != yamlPath {
		t.Fatalf("configPathForDir() = %q, want %q when neither exists",
			got, yamlPath)
	}

	if err := os.WriteFile(starPath, []byte("config = {}\n"), 0o644); err != nil {
		t.Fatalf("write star: %v", err)
	}
	got = configPathForDir(dataDir)
	if got != starPath {
		t.Fatalf("configPathForDir() = %q, want %q when only .star exists",
			got, starPath)
	}

	if err := os.WriteFile(yamlPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	got = configPathForDir(dataDir)
	if got != yamlPath {
		t.Fatalf("configPathForDir() = %q, want %q when both exist",
			got, yamlPath)
	}
}

func TestConfigPathForDirUsesDatadirDefaultLocation(t *testing.T) {
	dataDir := filepath.Join("home", ".rune")
	got := configPathForDir(dataDir)
	want := filepath.Join(dataDir, "config.yaml")
	if got != want {
		t.Fatalf("configPathForDir() = %q, want %q", got, want)
	}
}

func TestResolveDefaultDataPath(t *testing.T) {
	xdgData := filepath.Join(t.TempDir(), "xdgdata")
	tests := []struct {
		name        string
		legacyDir   bool
		xdgDataHome string
		want        func(home string) string
	}{
		{
			name:        "legacy ~/.rune wins over XDG",
			legacyDir:   true,
			xdgDataHome: xdgData,
			want:        func(home string) string { return filepath.Join(home, ".rune") },
		},
		{
			name:        "XDG_DATA_HOME used without a legacy dir",
			xdgDataHome: xdgData,
			want:        func(string) string { return filepath.Join(xdgData, "rune") },
		},
		{
			name: "default XDG data home without a legacy dir",
			want: func(home string) string {
				return filepath.Join(home, ".local", "share", "rune")
			},
		},
		{
			name:        "relative XDG_DATA_HOME is ignored",
			xdgDataHome: "relative/xdg",
			want: func(home string) string {
				return filepath.Join(home, ".local", "share", "rune")
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("XDG_DATA_HOME", tc.xdgDataHome)
			if tc.legacyDir {
				require.NoError(t,
					os.MkdirAll(filepath.Join(home, ".rune"), 0o755))
			}
			require.Equal(t, tc.want(home), resolveDefaultDataPath(home))
		})
	}
}

func TestResolveConfigPath(t *testing.T) {
	xdgConfig := filepath.Join(t.TempDir(), "xdgcfg")
	tests := []struct {
		name          string
		configFlag    string
		configChanged bool
		dataChanged   bool
		setup         func(t *testing.T, home, dataDir string)
		xdgConfigHome string
		want          func(home, dataDir string) string
	}{
		{
			name:          "-c wins over every other source",
			configFlag:    filepath.Join("somewhere", "mine.yaml"),
			configChanged: true,
			setup: func(t *testing.T, home, dataDir string) {
				require.NoError(t, os.MkdirAll(filepath.Join(home, ".rune"), 0o755))
				require.NoError(t, os.WriteFile(
					filepath.Join(home, ".rune", "config.yaml"), []byte("{}\n"), 0o644))
			},
			want: func(_, _ string) string {
				return filepath.Join("somewhere", "mine.yaml")
			},
		},
		{
			name:        "explicit -d pins the config beside the data dir",
			dataChanged: true,
			want: func(_, dataDir string) string {
				return filepath.Join(dataDir, "config.yaml")
			},
		},
		{
			name:        "explicit -d prefers an existing config.star",
			dataChanged: true,
			setup: func(t *testing.T, _, dataDir string) {
				require.NoError(t, os.WriteFile(
					filepath.Join(dataDir, "config.star"), []byte("config = {}\n"), 0o644))
			},
			want: func(_, dataDir string) string {
				return filepath.Join(dataDir, "config.star")
			},
		},
		{
			name: "legacy ~/.rune/config.yaml wins over XDG without -d",
			setup: func(t *testing.T, home, _ string) {
				require.NoError(t, os.MkdirAll(filepath.Join(home, ".rune"), 0o755))
				require.NoError(t, os.WriteFile(
					filepath.Join(home, ".rune", "config.yaml"), []byte("{}\n"), 0o644))
			},
			xdgConfigHome: xdgConfig,
			want: func(home, _ string) string {
				return filepath.Join(home, ".rune", "config.yaml")
			},
		},
		{
			name: "legacy ~/.rune/config.star is found without -d",
			setup: func(t *testing.T, home, _ string) {
				require.NoError(t, os.MkdirAll(filepath.Join(home, ".rune"), 0o755))
				require.NoError(t, os.WriteFile(
					filepath.Join(home, ".rune", "config.star"), []byte("config = {}\n"), 0o644))
			},
			want: func(home, _ string) string {
				return filepath.Join(home, ".rune", "config.star")
			},
		},
		{
			name: "legacy dir without a config falls to the XDG path",
			setup: func(t *testing.T, home, _ string) {
				require.NoError(t, os.MkdirAll(filepath.Join(home, ".rune"), 0o755))
			},
			xdgConfigHome: xdgConfig,
			want: func(string, string) string {
				return filepath.Join(xdgConfig, "rune", "config.yaml")
			},
		},
		{
			name:          "XDG_CONFIG_HOME used without a legacy config",
			xdgConfigHome: xdgConfig,
			want: func(string, string) string {
				return filepath.Join(xdgConfig, "rune", "config.yaml")
			},
		},
		{
			name:          "XDG config.star preferred over a missing yaml",
			xdgConfigHome: xdgConfig,
			setup: func(t *testing.T, _, _ string) {
				require.NoError(t, os.MkdirAll(
					filepath.Join(xdgConfig, "rune"), 0o755))
				require.NoError(t, os.WriteFile(
					filepath.Join(xdgConfig, "rune", "config.star"),
					[]byte("config = {}\n"), 0o644))
			},
			want: func(string, string) string {
				return filepath.Join(xdgConfig, "rune", "config.star")
			},
		},
		{
			name: "default config home without a legacy config or XDG",
			want: func(home, _ string) string {
				return filepath.Join(home, ".config", "rune", "config.yaml")
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			dataDir := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", tc.xdgConfigHome)
			if tc.setup != nil {
				tc.setup(t, home, dataDir)
			}
			got := resolveConfigPath(
				tc.configFlag, tc.configChanged, dataDir, tc.dataChanged, home)
			require.Equal(t, tc.want(home, dataDir), got)
		})
	}
}

func TestDataPathDefaultsUseOSPaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	// A legacy install keeps ~/.rune; a fresh one lands under XDG data home.
	dataDir := filepath.Join(home, ".local", "share", "rune")
	if _, err := os.Stat(filepath.Join(home, ".rune")); err == nil {
		dataDir = filepath.Join(home, ".rune")
	}
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{"datadir", defaultDataPath, dataDir},
		{"workspace server log", flag.Lookup("workspace-server-log").DefValue,
			filepath.Join(dataDir, "server.log")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("default = %q, want %q", tc.got, tc.want)
			}
		})
	}
}
