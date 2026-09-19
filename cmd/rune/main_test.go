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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	flag "github.com/spf13/pflag"
	"unstable.build/rune/internal/debug"
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
		zdotDir  string
		wantArgs []string
		wantOK   bool
	}{
		{
			name:    "darwin app with zdotdir",
			goos:    "darwin",
			zdotDir: filepath.Join("home", ".rune", "zdot"),
			wantArgs: []string{
				"--rune-zdotdir=" + filepath.Join("home", ".rune", "zdot"),
				"-G", "-w", "",
			},
			wantOK: true,
		},
		{
			name:    "darwin without zdotdir",
			goos:    "darwin",
			zdotDir: "",
			wantArgs: []string{
				"-G", "-w", "",
			},
			wantOK: true,
		},
		{
			name:    "linux app with zdotdir",
			goos:    "linux",
			zdotDir: filepath.Join("home", ".rune", "zdot"),
			wantArgs: []string{
				"--rune-zdotdir=" + filepath.Join("home", ".rune", "zdot"),
				"-G", "-w", "",
			},
			wantOK: true,
		},
		{
			name:    "linux without zdotdir",
			goos:    "linux",
			zdotDir: "",
			wantArgs: []string{
				"-G", "-w", "",
			},
			wantOK: true,
		},
		{
			name:    "windows without zdotdir",
			goos:    "windows",
			zdotDir: "",
			wantArgs: []string{
				"-G", "-w", "",
			},
			wantOK: true,
		},
		{
			name:    "freebsd unsupported",
			goos:    "freebsd",
			zdotDir: "",
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotArgs, gotOK := appLaunchArgs(tt.goos, tt.zdotDir)
			if gotOK != tt.wantOK {
				t.Fatalf("appLaunchArgs() ok = %v, want %v", gotOK, tt.wantOK)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Fatalf("appLaunchArgs() = %#v, want %#v", gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestBundleZdotDir(t *testing.T) {
	tests := []struct {
		name     string
		goos     string
		execPath string
		wantDir  string
		wantOK   bool
	}{
		{
			name:     "darwin app",
			goos:     "darwin",
			execPath: filepath.Join("Applications", "Rune.app", "Contents", "MacOS", "rune"),
			wantDir:  filepath.Join("Applications", "Rune.app", "Contents", "Resources", "zdot"),
			wantOK:   true,
		},
		{
			name:     "darwin non-app still resolves to relative resources",
			goos:     "darwin",
			execPath: filepath.Join("usr", "local", "bin", "rune"),
			wantDir:  filepath.Join("usr", "local", "Resources", "zdot"),
			wantOK:   true,
		},
		{
			name:     "linux freedesktop app",
			goos:     "linux",
			execPath: filepath.Join("opt", "Rune", "rune.app", "bin", "rune"),
			wantDir:  filepath.Join("opt", "Rune", "rune.app", "share", "zdot"),
			wantOK:   true,
		},
		{
			name:     "linux non app",
			goos:     "linux",
			execPath: filepath.Join("usr", "local", "bin", "rune"),
			wantOK:   false,
		},
		{
			name:     "windows unsupported",
			goos:     "windows",
			execPath: filepath.Join("C:", "Program Files", "Rune", "rune.exe"),
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotDir, gotOK := bundleZdotDir(tt.goos, tt.execPath)
			if gotOK != tt.wantOK {
				t.Fatalf("bundleZdotDir() ok = %v, want %v", gotOK, tt.wantOK)
			}
			if gotDir != tt.wantDir {
				t.Fatalf("bundleZdotDir() dir = %q, want %q", gotDir, tt.wantDir)
			}
		})
	}
}

// TestInstallZdotDirDoesNotWriteToSource reproduces the
// "Rune is damaged" Gatekeeper bug. Pointing ZDOTDIR inside the signed
// bundle let zsh write .zcompdump (and similar) into a read-only sealed
// directory, invalidating the code signature. installZdotDir mirrors the
// dotfiles into a writable location so the bundle stays untouched.
func TestInstallZdotDirDoesNotWriteToSource(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := filepath.Join(t.TempDir(), "zdot")

	for _, name := range zdotFiles {
		if err := os.WriteFile(filepath.Join(srcDir, name),
			[]byte("# "+name), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	srcBefore, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatalf("read src: %v", err)
	}

	if err := installZdotDir(srcDir, dstDir); err != nil {
		t.Fatalf("installZdotDir: %v", err)
	}

	srcAfter, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatalf("read src after: %v", err)
	}
	if len(srcAfter) != len(srcBefore) {
		t.Fatalf("source directory mutated: before=%d after=%d",
			len(srcBefore), len(srcAfter))
	}

	for _, name := range zdotFiles {
		dst := filepath.Join(dstDir, name)
		got, err := os.ReadFile(dst)
		if err != nil {
			t.Fatalf("read %s: %v", dst, err)
		}
		if want := "# " + name; string(got) != want {
			t.Fatalf("dst %s = %q, want %q", name, got, want)
		}
	}
}

func TestInstallZdotDirOverwrites(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := filepath.Join(t.TempDir(), "zdot")

	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatalf("mkdir dst: %v", err)
	}
	stale := filepath.Join(dstDir, ".zshrc")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	for _, name := range zdotFiles {
		if err := os.WriteFile(filepath.Join(srcDir, name),
			[]byte("fresh "+name), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	if err := installZdotDir(srcDir, dstDir); err != nil {
		t.Fatalf("installZdotDir: %v", err)
	}

	got, err := os.ReadFile(stale)
	if err != nil {
		t.Fatalf("read .zshrc: %v", err)
	}
	if want := "fresh .zshrc"; string(got) != want {
		t.Fatalf(".zshrc = %q, want %q (not overwritten)", got, want)
	}
}

func TestInstallZdotDirCreatesMissingDest(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := filepath.Join(t.TempDir(), "nested", "zdot")
	if err := os.WriteFile(filepath.Join(srcDir, ".zshenv"),
		[]byte("# zshenv"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := installZdotDir(srcDir, dstDir); err != nil {
		t.Fatalf("installZdotDir: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dstDir, ".zshenv"))
	if err != nil {
		t.Fatalf("read .zshenv: %v", err)
	}
	if want := "# zshenv"; string(got) != want {
		t.Fatalf(".zshenv = %q, want %q", got, want)
	}
}

func TestInstallZdotDirSkipsMissingSourceFiles(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := filepath.Join(t.TempDir(), "zdot")
	if err := os.WriteFile(filepath.Join(srcDir, ".zshrc"),
		[]byte("# zshrc only"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := installZdotDir(srcDir, dstDir); err != nil {
		t.Fatalf("installZdotDir: %v", err)
	}

	entries, err := os.ReadDir(dstDir)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != ".zshrc" {
		t.Fatalf("dst entries = %v, want only .zshrc", entries)
	}
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
