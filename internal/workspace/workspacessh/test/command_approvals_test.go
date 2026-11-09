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

package workspacetest

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	blueauth "github.com/unstablebuild/blue/auth"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/ide/ideauthorizer"
	"unstable.build/rune/internal/ide/ideauthorizer/ideauthorizertest"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
	"unstable.build/rune/internal/ide/idepkg/pkgrpc"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacessh"
)

// runOnHost runs cmd on the workspace host and returns its stdout.
func runOnHost(ctx context.Context, t *testing.T, exec schemeapi.Executor, cmd workspaceapi.Cmd) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Watcher = workspaceapi.ChanProcessWatcher(done)
	_, err := exec.StartCommand(ctx, cmd)
	require.NoError(t, err)
	select {
	case err := <-done:
		require.NoError(t, err, "stdout=%q stderr=%q", stdout.String(), stderr.String())
	case <-ctx.Done():
		t.Fatalf("timed out running %s", cmd.Path)
	}
	return stdout.String()
}

func TestCommandApprovalDoesNotCoverWhatTheHostPATHRunsLater(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)

	c := StartContainer(t, SSHDScenario{
		PublicKeyFile:     "/id_ed25519.pub",
		InstallRuneBinary: true,
	})
	uri, err := workspaceapi.ParseURI(fmt.Sprintf("ssh://test@%s/tmp", c.HostPort))
	require.NoError(t, err)
	cfg := config.MapConfig(map[string]any{
		"private_keys": []any{PrivateKeyPath(t, "id_ed25519")},
		"timeout":      "20s",
		"insecure":     true,
	})
	scheme, err := workspacessh.New(errorUI{})(context.Background(), cfg, uri)
	require.NoError(t, err)
	defer scheme.Close()
	ws := workspace.NewSchemeWorkspace(uri, scheme, func(fn func()) bool { fn(); return true })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	home := runOnHost(ctx, t, ws, workspaceapi.Cmd{
		Path: "sh", Args: []string{"-c", `printf %s "$HOME"`},
	})
	oldDir, newDir := home+"/old", home+"/new"
	runOnHost(ctx, t, ws, workspaceapi.Cmd{Path: "sh", Args: []string{"-c", fmt.Sprintf(
		`mkdir -p %[1]q %[2]q && `+
			`printf '#!/bin/sh\necho old\n' > %[1]q/tool && chmod 755 %[1]q/tool && `+
			`printf '#!/bin/sh\necho new\n' > %[2]q/tool && chmod 755 %[2]q/tool`,
		oldDir, newDir)}})

	// A package that changes gui.env changes the PATH of the running
	// host, which re-applies it once the user approves the change.
	cc, ok := ws.(workspace.PackageHost).HostConn()
	require.True(t, ok)
	ui := &approvingUI{Notifications: idepkgtest.NewNotifications(t)}
	pm := pkgrpc.NewClient(cc, ui)
	setGUIEnv := func(pkg, env string) {
		t.Helper()
		f, err := ws.OpenFile(home+"/.rune/gui_env", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		require.NoError(t, err)
		_, err = f.Write([]byte(env + "\n"))
		require.NoError(t, err)
		require.NoError(t, f.Close())
		require.NoError(t, pm.InstallPackageVersion(ctx, pkg, "1.0.0", nil))
		applied := fmt.Sprintf("applied %s configuration updates. "+
			"All changes are in effect now: gui.env.", pkg)
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			var notices []string
			for _, noti := range ui.Active() {
				notices = append(notices, noti.Msg)
			}
			assert.True(c, slices.Contains(notices, applied), "notices: %q", notices)
		}, 10*time.Second, 10*time.Millisecond, "the host applies gui.env")
	}
	setGUIEnv("old-tool", "PATH="+oldDir+":$PATH")

	prompter := ideauthorizertest.NewPrompter(ideauthorizer.PromptOptionYesAlways)
	authorizer := ideauthorizertest.NewAuthorizer(t, ws, uri, prompter)
	extCtx := ideauthorizertest.ExtensionContext(ctx)
	tool := workspaceapi.Cmd{Path: "tool"}
	require.NoError(t, authorizer.AuthorizeCommand(extCtx, tool))
	require.NoError(t, authorizer.AuthorizeCommand(extCtx, tool))
	require.Len(t, prompter.Prompts(), 1)
	assert.Contains(t, prompter.Prompts()[0], "Runs `"+oldDir+"/tool`.")
	assert.Equal(t, "old\n", runOnHost(ctx, t, ws, tool))

	setGUIEnv("new-tool", "PATH="+newDir+":"+oldDir+":$PATH")
	require.Equal(t, "new\n", runOnHost(ctx, t, ws, tool),
		"the host now runs another tool")
	prompter.Answer(ideauthorizer.PromptOptionNo)
	require.ErrorIs(t, authorizer.AuthorizeCommand(extCtx, tool), blueauth.ErrForbidden,
		"approving %s/tool does not approve %s/tool", oldDir, newDir)
	require.Len(t, prompter.Prompts(), 2)
	assert.Contains(t, prompter.Prompts()[1], "Runs `"+newDir+"/tool`.")
}
