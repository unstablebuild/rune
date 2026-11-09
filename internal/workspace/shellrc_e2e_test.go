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

//go:build e2e

package workspace

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/ide/hostenv"
)

// shellRCCase describes how a shell's own startup files undo Rune's
// environment and how to print what the shell ends up with.
type shellRCCase struct {
	profile string // relative to $HOME
	reset   string
	print   string
}

var shellRCCases = map[string]shellRCCase{
	"bash": {
		profile: ".bash_profile",
		reset:   "PATH=/usr/bin:/bin; export PATH; export RUNE_E2E_VAR=from-profile\n",
		print:   "echo \"RUNE_PATH=$PATH\"; echo \"RUNE_VAR=$RUNE_E2E_VAR\"; exit\n",
	},
	"zsh": {
		profile: ".zprofile",
		reset:   "PATH=/usr/bin:/bin; export PATH; export RUNE_E2E_VAR=from-profile\n",
		print:   "echo \"RUNE_PATH=$PATH\"; echo \"RUNE_VAR=$RUNE_E2E_VAR\"; exit\n",
	},
	"fish": {
		profile: ".config/fish/config.fish",
		reset:   "set -gx PATH /usr/bin /bin; set -gx RUNE_E2E_VAR from-profile\n",
		print:   "echo RUNE_PATH=(string join : $PATH); echo RUNE_VAR=$RUNE_E2E_VAR; exit\n",
	},
}

var (
	shellRCPath = regexp.MustCompile(`RUNE_PATH=(/[^\r\n]*)`)
	shellRCVar  = regexp.MustCompile(`RUNE_VAR=(from-[a-z]+)`)
)

func TestTerminalShellReappliesRuneEnvironment(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			shellPath, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("%s is not installed", shell)
			}
			for _, tc := range []struct {
				name      string
				fragments bool
			}{
				{"Rune's environment wins over the startup files", true},
				// Proves the startup files really undo the environment
				// the shell inherits, so the case above tests something.
				{"without the fragments the startup files win", false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					path, value := runTerminalShell(t, shellPath, shellRCCases[shell], tc.fragments)
					dataDir := os.Getenv(hostenv.DataDirVar)
					runeEntries := filepath.Join(dataDir, "bin") + ":" +
						filepath.Join(dataDir, "tools", "bin") + ":"
					if tc.fragments {
						assert.True(t, strings.HasPrefix(path, runeEntries),
							"PATH %q must start with %q", path, runeEntries)
						assert.Contains(t, path, ":/usr/bin:/bin",
							"the startup files' PATH must be kept")
						assert.Equal(t, "from-rune", value)
						return
					}
					assert.Equal(t, "/usr/bin:/bin", path)
					assert.Equal(t, "from-profile", value)
				})
			}
		})
	}
}

// runTerminalShell starts shell the way a Rune terminal does, on a pty
// with an empty Cmd.Path, in a $HOME whose startup files reset PATH and a
// gui.env variable, and returns the PATH and variable the shell ends up
// with.
func runTerminalShell(
	t *testing.T, shellPath string, tc shellRCCase, fragments bool,
) (path, value string) {
	t.Helper()
	home := t.TempDir()
	profile := filepath.Join(home, tc.profile)
	require.NoError(t, os.MkdirAll(filepath.Dir(profile), 0o755))
	require.NoError(t, os.WriteFile(profile, []byte(tc.reset), 0o644))
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("SHELL", shellPath)
	t.Setenv("ZDOTDIR", "")
	t.Setenv("PATH", os.Getenv("PATH"))
	t.Setenv("RUNE_E2E_VAR", "")
	t.Setenv(hostenv.DataDirVar, "")

	dataDir := t.TempDir()
	shellRCDir, err := InstallShellRC(dataDir)
	require.NoError(t, err)
	hostShellRCDir := shellRCDir
	if !fragments {
		hostShellRCDir = ""
	}
	require.NoError(t, hostenv.New(dataDir, hostShellRCDir).Apply(config.MapConfig(map[string]any{
		"RUNE_E2E_VAR": "from-rune",
		"PATH":         "$RUNE_DATADIR/tools/bin:$PATH",
	})))

	uri, err := workspaceapi.ParseURI("file://" + t.TempDir())
	require.NoError(t, err)
	scheme, err := NewFileSchemeFunc(dataDir, shellRCDir)(
		context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pty, err := scheme.NewPty(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pty.Master.Close() })

	var mu sync.Mutex
	var out bytes.Buffer
	read := make(chan struct{})
	go func() {
		defer close(read)
		buf := make([]byte, 4096)
		for {
			n, err := pty.Master.Read(buf)
			mu.Lock()
			out.Write(buf[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()

	exited := make(chan error, 1)
	_, err = scheme.StartCommand(ctx, workspaceapi.Cmd{
		SysProcAttr: &syscall.SysProcAttr{Setsid: true, Setctty: true},
		Stdin:       pty.Slave,
		Stdout:      pty.Slave,
		Stderr:      pty.Slave,
		Watcher:     workspaceapi.ChanProcessWatcher(exited),
	})
	require.NoError(t, err)
	_, err = pty.Master.Write([]byte(tc.print))
	require.NoError(t, err)

	select {
	case <-exited:
	case <-ctx.Done():
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("shell did not exit; output: %q", out.String())
	}
	_ = pty.Slave.Close()
	select {
	case <-read:
	case <-time.After(5 * time.Second):
	}

	mu.Lock()
	defer mu.Unlock()
	pathMatch := shellRCPath.FindStringSubmatch(out.String())
	valueMatch := shellRCVar.FindStringSubmatch(out.String())
	require.NotNil(t, pathMatch, "no PATH in output %q", out.String())
	require.NotNil(t, valueMatch, "no variable in output %q", out.String())
	t.Setenv(hostenv.DataDirVar, dataDir)
	return pathMatch[1], valueMatch[1]
}
