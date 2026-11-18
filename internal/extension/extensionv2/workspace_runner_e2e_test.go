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

package extensionv2

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/auth"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/extension"
	"unstable.build/rune/internal/workspace"
)

func TestWorkspaceRunnerExtensionProcessE2E(t *testing.T) {
	meta, err := json.Marshal(extensionapi.Metadata{
		DeveloperID: "dev-id", DeveloperEmail: "dev@example.com", DeveloperKey: "dev-key",
		ExtensionID: "ext", ExtensionName: "Ext",
		Permissions: extensionapi.NewPermissions(extensionapi.PermissionStorage),
	})
	require.NoError(t, err)

	for _, tt := range []struct {
		name string
		// script runs in a directory holding meta.json, the extension's
		// metadata.
		script  string
		wantErr string
		// wantLog are lines of the extension's log, in order.
		wantLog []string
	}{
		{
			// As `go run` does when the extension does not build.
			name:    "exit before handshake is seen",
			script:  "echo 'no Go files' >&2\nexit 1\n",
			wantErr: "exit status 1",
			wantLog: []string{
				`level=info msg="starting `,
				"no Go files",
				`level=error msg="exited: exit status 1" source=rune`,
			},
		},
		{
			name: "handshake delivers the config and ends stdin",
			script: "cat meta.json\ncat > config.json\n" +
				"echo 'config read' >&2\nexec sleep 60\n",
			wantLog: []string{
				`level=info msg="starting `,
				`level=info msg="handshake complete: config sent" source=rune`,
				"config read",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			keys, err := auth.GenerateKeys()
			require.NoError(t, err)
			dir := t.TempDir()
			uri, err := workspaceapi.ParseURI("file://" + dir)
			require.NoError(t, err)
			execScheme, err := workspace.NewFileScheme(ctx, config.MapConfig(map[string]any{}), uri)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, execScheme.Close()) })
			socket := filepath.Join(dir, "ext.sock")
			runner := newWorkspaceRunner(execScheme, execScheme, extension.GrantAll(),
				nopTrustVerifier{}, uri, socket, dir, dir, []byte("cert"), keys)
			t.Cleanup(func() { require.NoError(t, runner.Close()) })

			require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), meta, 0o644))
			ext := filepath.Join(dir, "ext")
			require.NoError(t, os.WriteFile(ext, []byte("#!/bin/sh\n"+tt.script), 0o755))

			waitCtx, waitCancel := context.WithTimeout(ctx, 10*time.Second)
			defer waitCancel()
			err = runner.startExtension(waitCtx, "ext", ext, config.NopConfig())
			require.NotErrorIs(t, err, context.DeadlineExceeded)

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				state, ok := runner.extension("ext")
				require.True(t, ok)
				assert.False(t, state.Running)
				assert.Zero(t, state.Pid)
				assert.ErrorContains(t, state.LastErr, tt.wantErr)
				requireExtensionLog(t, runner, tt.wantLog)
				return
			}
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				return strings.Contains(readExtensionLog(t, runner), "config read")
			}, 10*time.Second, 10*time.Millisecond, "the extension must read its config to EOF")
			requireExtensionLog(t, runner, tt.wantLog)
			data, err := os.ReadFile(filepath.Join(dir, "config.json"))
			require.NoError(t, err)
			var cfg extensionapi.Config
			require.NoError(t, json.Unmarshal(data, &cfg))
			assert.Equal(t, socket, cfg.Socket)
			state, ok := runner.extension("ext")
			require.True(t, ok)
			assert.True(t, state.Running)
		})
	}
}

// goSourceExtension is a Go extension that completes the handshake, then
// reports the reload signal when it gets it.
const goSourceExtension = `package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	meta, err := os.ReadFile("meta.json")
	if err != nil {
		panic(err)
	}
	if _, err := os.Stdout.Write(meta); err != nil {
		panic(err)
	}
	if _, err := io.ReadAll(os.Stdin); err != nil {
		panic(err)
	}
	fmt.Fprintf(os.Stderr, "config read pid=%d\n", os.Getpid())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR2)
	<-ch
	fmt.Fprintln(os.Stderr, "reload signal")
	time.Sleep(time.Minute)
}
`

func TestWorkspaceRunnerGoSourceExtensionE2E(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no reload signal")
	}
	goTool, err := exec.LookPath("go")
	require.NoError(t, err, "the go toolchain must be on PATH")
	meta, err := json.Marshal(extensionapi.Metadata{
		DeveloperID: "dev-id", DeveloperEmail: "dev@example.com", DeveloperKey: "dev-key",
		ExtensionID: "ext", ExtensionName: "Ext",
		Permissions: extensionapi.NewPermissions(extensionapi.PermissionStorage),
	})
	require.NoError(t, err)

	newRunner := func(t *testing.T, mainGo string) (*workspaceRunner, string) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		keys, err := auth.GenerateKeys()
		require.NoError(t, err)
		dir := t.TempDir()
		uri, err := workspaceapi.ParseURI("file://" + dir)
		require.NoError(t, err)
		execScheme, err := workspace.NewFileScheme(ctx, config.MapConfig(map[string]any{}), uri)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, execScheme.Close()) })
		dataDir := filepath.Join(dir, "data")
		require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "bin"), 0o755))
		require.NoError(t, os.Symlink(goTool, filepath.Join(dataDir, "bin", "go")))
		runner := newWorkspaceRunner(execScheme, execScheme, extension.GrantAll(),
			nopTrustVerifier{}, uri, filepath.Join(dir, "ext.sock"), dataDir, dataDir,
			[]byte("cert"), keys)
		t.Cleanup(func() { require.NoError(t, runner.Close()) })
		require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), meta, 0o644))
		pkgDir := filepath.Join(dir, "ext")
		require.NoError(t, os.MkdirAll(pkgDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "go.mod"),
			[]byte("module ext\n\ngo 1.21\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "main.go"), []byte(mainGo), 0o644))
		return runner, pkgDir
	}
	// builtBinary is the binary the log says the extension was built into.
	builtBinary := func(t *testing.T, runner *workspaceRunner) string {
		t.Helper()
		for _, line := range strings.Split(readExtensionLog(t, runner), "\n") {
			if _, after, ok := strings.Cut(line, " build -o "); ok {
				return strings.TrimSuffix(strings.TrimSuffix(after, `" source=rune`), " .")
			}
		}
		t.Fatal("the log does not say what the extension was built into")
		return ""
	}

	t.Run("the signal reaches the extension", func(t *testing.T) {
		runner, pkgDir := newRunner(t, goSourceExtension)
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer waitCancel()
		require.NoError(t, runner.startExtension(waitCtx, "ext", pkgDir, config.NopConfig()))

		var pid int
		require.Eventually(t, func() bool {
			_, err := fmt.Sscanf(lineWith(readExtensionLog(t, runner), "config read pid="),
				"config read pid=%d", &pid)
			return err == nil
		}, 10*time.Second, 10*time.Millisecond, "the extension must read its config")
		state, ok := runner.extension("ext")
		require.True(t, ok)
		assert.True(t, state.Running)
		assert.Equal(t, workspaceapi.Pid(pid), state.Pid, "the pid recorded is the extension's own")
		bin := builtBinary(t, runner)
		assert.FileExists(t, bin)

		assert.Equal(t, 1, runner.SignalReload())
		require.Eventually(t, func() bool {
			return strings.Contains(readExtensionLog(t, runner), "reload signal")
		}, 10*time.Second, 10*time.Millisecond, "the extension must get the reload signal")

		require.NoError(t, runner.stopExtensionByID("ext"))
		require.Eventually(t, func() bool {
			_, err := os.Stat(filepath.Dir(bin))
			return os.IsNotExist(err)
		}, 10*time.Second, 10*time.Millisecond, "the binary is removed when the run ends")
	})

	t.Run("a restart builds the changed source", func(t *testing.T) {
		runner, pkgDir := newRunner(t, goSourceExtension)
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer waitCancel()
		require.NoError(t, runner.startExtension(waitCtx, "ext", pkgDir, config.NopConfig()))
		first := builtBinary(t, runner)

		changed := strings.Replace(goSourceExtension, "config read pid=", "config read again pid=", 1)
		require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "main.go"), []byte(changed), 0o644))
		require.NoError(t, runner.restartExtension(waitCtx, "ext"))
		require.Eventually(t, func() bool {
			return strings.Contains(readExtensionLog(t, runner), "config read again pid=")
		}, 10*time.Second, 10*time.Millisecond, "the restarted extension runs the new source")
		assert.NotEqual(t, first, builtBinary(t, runner), "the restart builds into a new file")
		assert.NoFileExists(t, first, "the previous run's binary is removed")
	})

	t.Run("a build failure ends the run before the handshake", func(t *testing.T) {
		runner, pkgDir := newRunner(t, "package main\n\nfunc main() { undefined() }\n")
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer waitCancel()
		err := runner.startExtension(waitCtx, "ext", pkgDir, config.NopConfig())
		require.ErrorContains(t, err, "build: exit status 1")
		state, ok := runner.extension("ext")
		require.True(t, ok)
		assert.False(t, state.Running)
		assert.Zero(t, state.Pid)
		logs := readExtensionLog(t, runner)
		assert.Contains(t, logs, "undefined: undefined", "the compiler's output is logged")
		assert.Contains(t, logs, `level=error msg="exited: build: exit status 1" source=rune`)
		assert.NotContains(t, logs, "handshake complete")
	})
}

// lineWith returns the first line of logs holding substr, "" for none.
func lineWith(logs, substr string) string {
	for _, line := range strings.Split(logs, "\n") {
		if i := strings.Index(line, substr); i >= 0 {
			return line[i:]
		}
	}
	return ""
}
