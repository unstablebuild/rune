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

package runetest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"unstable.build/rune/auth"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacerune"
	"unstable.build/rune/internal/workspace/workspacetest"
)

func TestMeshRoundTrip(t *testing.T) {
	control := StartTestControl(t, true /* sameUser */)
	peer := StartNode(t, control, "peer", "")
	client := StartNode(t, control, "client", "")
	peerDataDir := ServeWorkspaces(t, peer)
	WaitPeer(t, client, "peer")

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "hello.txt"), []byte("from the peer\n"), 0o600))

	scheme := OpenWorkspace(t, client, "peer", dir)
	defer scheme.Close()

	assert.Equal(t, dir, scheme.Root())

	// The peer advertises its own data directory as the install root,
	// so a client with a different --datadir provisions and resolves
	// toolchains where the peer actually keeps them.
	root, err := scheme.(workspace.InstallDataDirProvider).
		InstallDataDir(context.Background())
	require.NoError(t, err)
	assert.Equal(t, peerDataDir, root)

	entries, err := scheme.ReadDir(".")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "hello.txt", entries[0].Name())

	f, err := scheme.Open("hello.txt")
	require.NoError(t, err)
	defer f.Close()
	buf := make([]byte, 64)
	n, err := f.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "from the peer\n", string(buf[:n]))
}

func TestWorkspaceScheme(t *testing.T) {
	SkipIfRace(t)

	control := StartTestControl(t, true /* sameUser */)
	peer := StartNode(t, control, "peer", "")
	client := StartNode(t, control, "client", "")
	ServeWorkspaces(t, peer)
	WaitPeer(t, client, "peer")

	newScheme := func(t *testing.T) schemeapi.Scheme {
		return OpenWorkspace(t, client, "peer", t.TempDir())
	}
	workspacetest.TestWorkspaceSchemeFiles(t, newScheme)
	workspacetest.TestWorkspaceSchemeExecutor(t, newScheme)
}

func TestRejectsPeerOwnedByAnotherAccount(t *testing.T) {
	control := StartTestControl(t, false /* sameUser */)
	peer := StartNode(t, control, "peer", "")
	client := StartNode(t, control, "client", "")
	ServeWorkspaces(t, peer)
	WaitPeer(t, client, "peer")

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "secret.txt"), []byte("private\n"), 0o600))

	uri, err := workspaceapi.ParseURI("rune://peer" + dir)
	require.NoError(t, err)
	scheme, err := workspacerune.New(client)(
		context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	defer scheme.Close()

	ctx, cancel := context.WithTimeout(context.Background(), nodeJoinTimeout)
	defer cancel()
	require.NoError(t, scheme.(workspace.RemoteScheme).WaitConnected(ctx))

	_, err = scheme.Open("secret.txt")
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(rootErr(err)),
		"a peer owned by another account must not read files: %v", err)
}

func TestRejectsTaggedPeer(t *testing.T) {
	control := StartTestControl(t, true /* sameUser */)
	peer := StartNode(t, control, "peer", "")
	client := StartNode(t, control, "client", "")
	ServeWorkspaces(t, peer)
	WaitPeer(t, client, "peer")

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "secret.txt"), []byte("private\n"), 0o600))
	scheme := OpenWorkspace(t, client, "peer", dir)
	defer scheme.Close()
	_, err := scheme.ReadDir(".")
	require.NoError(t, err, "an untagged machine of the account is admitted")

	control.SetNodeTags(t, client, peer, auth.ServeTagPrefix+"someone")

	_, err = scheme.ReadDir(".")
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(rootErr(err)),
		"a tagged machine must not read files: %v", err)
}

// rootErr unwraps to the innermost error so a gRPC status wrapped by
// the scheme's own context is still inspectable.
func rootErr(err error) error {
	for {
		unwrapped := errors.Unwrap(err)
		if unwrapped == nil {
			return err
		}
		err = unwrapped
	}
}
