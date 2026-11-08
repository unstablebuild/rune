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
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	flag "github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/ide/hostenv"
	"unstable.build/rune/internal/ide/idehistory"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
	"unstable.build/rune/internal/term/gui"
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
		{name: "headless on a tailnet", args: []string{"--headless", "--control-url", "https://hs.example.com"}},
		{
			name:    "control url without headless",
			args:    []string{"--gui", "--control-url", "https://hs.example.com"},
			wantErr: "--control-url needs --headless",
		},
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
	fs.String("control-url", "", "")
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

type recordingSink struct {
	events []term.Event
}

func (s *recordingSink) PublishEvent(ev term.Event) bool {
	s.events = append(s.events, ev)
	return true
}

func TestGUIPublisherHoldsScheduledFunctionsUntilAttach(t *testing.T) {
	var p guiPublisher
	var ran []string
	schedule := func(name string) term.Event {
		return term.Event{Type: term.EventInterrupt, UserFunc: func() { ran = append(ran, name) }}
	}

	assert.True(t, p.publish(schedule("before")),
		"a function scheduled before the GUI exists must run once it does")
	assert.False(t, p.publish(term.Event{Type: term.EventInterrupt}),
		"the GUI's first frame supersedes a redraw asked for before it exists")

	sink := new(recordingSink)
	p.attach(sink)
	assert.True(t, p.publish(schedule("after")))

	require.Len(t, sink.events, 2)
	for _, ev := range sink.events {
		ev.UserFunc()
	}
	assert.Equal(t, []string{"before", "after"}, ran,
		"functions scheduled before attach run ahead of those scheduled after")
}

func TestRunGUIOffersToReopenTheLastSessionWithoutAnIntro(t *testing.T) {
	wallpaper := filepath.Join(t.TempDir(), "wallpaper.png")
	f, err := os.Create(wallpaper)
	require.NoError(t, err)
	require.NoError(t, png.Encode(f, image.NewNRGBA(image.Rect(0, 0, 4, 4))))
	require.NoError(t, f.Close())

	for _, tc := range []struct {
		name   string
		config string
	}{
		{name: "wallpaper image", config: "workspace:\n  wallpaper_image: " + wallpaper + "\n"},
		{name: "wallpaper ascii", config: "workspace:\n  wallpaper_ascii: art\n"},
		{name: "intro disabled", config: "animations:\n  intro: false\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			configPath := filepath.Join(dataDir, configFilename)
			require.NoError(t, os.WriteFile(configPath, []byte("editor:\n  mode: modal\n"+
				"upgrade:\n  auto_check_enabled: false\n"+tc.config), 0o644))
			previous, err := workspaceapi.CurrentUserHostURI(t.TempDir())
			require.NoError(t, err)
			storage := newRuneStorage(dataDir)
			require.NoError(t, idehistory.New(storageapi.WithPartition(storage, "ide")).
				StoreLastSession(context.Background(), idehistory.Session{
					Workspaces: []idehistory.SessionWorkspace{{URI: previous}},
				}))
			require.NoError(t, storage.Close())

			srv := httptest.NewServer(http.NotFoundHandler())
			t.Cleanup(srv.Close)
			t.Cleanup(overrideBootstrapFlags(t, bootstrapFlagOverrides{
				httpAddress:    srv.URL,
				grpcAddress:    "127.0.0.1:1",
				dataPath:       dataDir,
				configPath:     configPath,
				websiteAddress: "https://rune.test",
			}))
			rootCfg, err := ide.Config(configPath, runeDefaultConfig())
			require.NoError(t, err)

			mu := new(sync.Mutex)
			var publisher guiPublisher
			soundOutput := gui.NewSounds()
			soundOutput.SetMuted(true)
			root, err := newBootstrapHandler(
				dataDir, configPath, "" /* workspace */, "", /* shellRCDir */
				hostenv.New(dataDir, ""), nil /* filenames */, nil, /* launchCmd */
				ide.FuncExtensionsRunner(testE2EExtensionsRunner),
				mu, publisher.publish, nil, /* cellPixelSize */
				func(*url.URL) error { return nil }, clipboard.NewInMemory(), soundOutput,
				dataDir, rootCfg, pkgtrust.NewStore(),
			)
			require.NoError(t, err)
			t.Cleanup(func() { _ = root.Close() })
			require.NotNil(t, root.realIDE)

			g, err := gui.New(root, gui.WithLocker(mu))
			require.NoError(t, err)
			t.Cleanup(func() { _ = g.Close() })
			publisher.attach(g)
			root.attachGUI(g, false)
			require.NoError(t, g.Update())

			mu.Lock()
			defer mu.Unlock()
			const width, height = 160, 50
			root.Resize(width, height)
			w := term.NewStringWriter(width, height)
			root.Draw(w)
			require.NoError(t, w.Flush())
			assert.True(t, strings.Contains(w.String(),
				"Do you want to open the following workspaces"),
				"the reopen offer must show on the first frame")
		})
	}
}
