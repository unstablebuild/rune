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
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"unstable.build/rune/internal/tailnet"
	"unstable.build/rune/internal/tailnet/tailnettest"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacerune"
)

// tailnetClient is a tailnet.Client on m, reaching the tailnet as the
// operating system's network does on a machine running Tailscale.
func tailnetClient(m *tailnettest.Machine) *tailnet.Client {
	return tailnet.New(tailnet.Config{
		LocalClient: m.LocalClient, Listen: m.Listen, Dial: m.Dial,
	})
}

// openTailscaleWorkspace opens tailscale://peer/dir through client and
// waits until it is connected.
func openTailscaleWorkspace(
	t *testing.T, client *tailnet.Client, peer, dir string,
) schemeapi.Scheme {
	t.Helper()
	uri, err := workspaceapi.ParseURI(workspace.TailscaleScheme + "://" + peer + dir)
	require.NoError(t, err)
	scheme, err := workspacerune.New(workspace.TailscaleScheme, client)(
		context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), tailnettest.Timeout)
	defer cancel()
	require.NoError(t, scheme.(workspace.RemoteScheme).WaitConnected(ctx))
	return scheme
}

func TestTailscaleNet(t *testing.T) {
	tn := tailnettest.Start(t)
	tn.Control.AllNodesSameUser = true
	workstation := tn.Join(t, "workstation")
	laptop := tn.Join(t, "laptop")

	dataDir := t.TempDir()
	served := newTailscaleNet(tailnetClient(workstation), dataDir, "", &servedPackages{})
	t.Cleanup(func() { _ = served.Close() })
	require.NoError(t, served.serve())
	require.NoError(t, served.serve(), "serving again is a no-op")
	opener := newTailscaleNet(tailnetClient(laptop), t.TempDir(), "", &servedPackages{})

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi"), 0o600))

	t.Run("completes the machines that serve workspaces", func(t *testing.T) {
		var got []string
		require.Eventually(t, func() bool {
			it, _, err := opener.completer().Complete(context.Background(),
				[]string{"workspaceopen", "tailscale://"})
			if err != nil {
				return false
			}
			got, err = iterator.ToSlice(context.Background(), it)
			return err == nil && len(got) == 1
		}, tailnettest.Timeout, 200*time.Millisecond, "completions: %v", got)
		assert.Equal(t, []string{"tailscale://workstation/"}, got)
	})

	t.Run("opens a workspace", func(t *testing.T) {
		uri, err := workspaceapi.ParseURI("tailscale://workstation" + dir)
		require.NoError(t, err)
		scheme, err := opener.schemeFunc()(context.Background(), config.NopConfig(), uri)
		require.NoError(t, err)
		t.Cleanup(func() { _ = scheme.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), tailnettest.Timeout)
		defer cancel()
		require.NoError(t, scheme.(workspace.RemoteScheme).WaitConnected(ctx))

		assert.Equal(t, "hi", readSchemeFile(t, scheme, "hello.txt"))
		fileURI, err := scheme.URI("hello.txt")
		require.NoError(t, err)
		assert.Equal(t, "tailscale://workstation"+filepath.Join(dir, "hello.txt"),
			fileURI.String())
		installRoot, err := scheme.(workspace.InstallDataDirProvider).
			InstallDataDir(context.Background())
		require.NoError(t, err)
		assert.Equal(t, dataDir, installRoot)
	})

	t.Run("refuses another user's machine", func(t *testing.T) {
		tn.Control.AllNodesSameUser = false
		stranger := tn.Join(t, "stranger")
		uri, err := workspaceapi.ParseURI("tailscale://workstation" + dir)
		require.NoError(t, err)
		scheme, err := workspacerune.New(workspace.TailscaleScheme, tailnetClient(stranger))(
			context.Background(), config.NopConfig(), uri)
		require.NoError(t, err)
		t.Cleanup(func() { _ = scheme.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), tailnettest.Timeout)
		defer cancel()
		require.NoError(t, scheme.(workspace.RemoteScheme).WaitConnected(ctx))
		_, err = scheme.Open("hello.txt")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "belongs to")
		assert.Equal(t, codes.PermissionDenied, status.Code(rootCause(err)), "%v", err)
	})

	t.Run("stops serving once closed", func(t *testing.T) {
		require.NoError(t, served.Close())
		require.NoError(t, served.serve(), "a closed network does not serve again")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := tailnetClient(laptop).Dial(ctx, "workstation")
		assert.Error(t, err)
	})
}

func readSchemeFile(t *testing.T, scheme schemeapi.Scheme, name string) string {
	t.Helper()
	f, err := scheme.Open(name)
	require.NoError(t, err)
	defer f.Close()
	data, err := io.ReadAll(f)
	require.NoError(t, err)
	return string(data)
}

func rootCause(err error) error {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
}
