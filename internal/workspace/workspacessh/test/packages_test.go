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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
	"unstable.build/rune/internal/ide/idepkg/pkgrpc"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacessh"
)

type progressRecorder struct {
	mu    sync.Mutex
	steps []int64
}

func (p *progressRecorder) Progress(progress, total int64, _ string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.steps = append(p.steps, progress*100/total)
}

func (p *progressRecorder) recorded() []int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int64(nil), p.steps...)
}

// approvingUI approves every change the host asks about.
type approvingUI struct {
	*idepkgtest.Notifications
	mu      sync.Mutex
	prompts []string
}

func (u *approvingUI) PromptConfig(p idepkg.ConfigPrompt, answer func(bool)) {
	u.mu.Lock()
	u.prompts = append(u.prompts, p.Message)
	u.mu.Unlock()
	answer(true)
}

func (u *approvingUI) asked() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.prompts...)
}

func TestInstallPackageOverSSH(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ws := workspace.NewSchemeWorkspace(uri, scheme, func(fn func()) bool { fn(); return true })
	cc, ok := ws.(workspace.PackageHost).HostConn()
	require.True(t, ok, "an ssh workspace reaches its host's package manager")
	ui := &approvingUI{Notifications: idepkgtest.NewNotifications(t)}
	pm := pkgrpc.NewClient(cc, ui)

	const pkg = "rune-tool"
	_, err = pm.LibDir(ctx, pkg)
	require.ErrorIs(t, err, idepkg.ErrNotInstalled)
	version, err := pm.LatestVersion(ctx, pkg)
	require.NoError(t, err)

	pw := &progressRecorder{}
	require.NoError(t, pm.InstallPackageVersion(ctx, pkg, version, pw))
	assert.Equal(t, []int64{25, 50, 75, 100}, pw.recorded())
	assert.Equal(t, []string{"rune-tool wants to update your configuration."}, ui.asked())
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		var notices []string
		for _, noti := range ui.Active() {
			notices = append(notices, noti.Msg)
		}
		assert.Equal(c, []string{
			"applied rune-tool configuration updates. All changes are in effect now: gui.env.",
		}, notices)
	}, 10*time.Second, 10*time.Millisecond, "the host applies the approved change")

	it, err := pm.LibDir(ctx, pkg)
	require.NoError(t, err)
	paths, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	require.Len(t, paths, 1)
	assert.True(t, strings.HasSuffix(paths[0], "/.rune/bin/"+pkg),
		"the path is on the host: %s", paths[0])

	var stdout bytes.Buffer
	ch := make(chan error, 1)
	_, err = scheme.StartCommand(ctx, workspaceapi.Cmd{
		Path:    pkg,
		Stdout:  &stdout,
		Watcher: workspaceapi.ChanProcessWatcher(ch),
	})
	require.NoError(t, err, "the installed executable runs by bare name")
	select {
	case err := <-ch:
		require.NoError(t, err, "stdout=%q", stdout.String())
	case <-ctx.Done():
		t.Fatalf("timed out running the installed executable; stdout=%q", stdout.String())
	}
	assert.Equal(t, fmt.Sprintf("%s ok %s\n", pkg, version), stdout.String())
}
