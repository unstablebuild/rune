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
	"os"
	"path/filepath"
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
