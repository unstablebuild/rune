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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
	"unstable.build/rune/internal/ide/idepkg/pkgrpc"
	"unstable.build/rune/internal/runenet"
	"unstable.build/rune/internal/workspace"
)

// peerPackages stands in for a peer's package manager. It implements
// only what these tests call; any other method panics on the nil
// embedded interface.
type peerPackages struct {
	idepkg.PackageManager
	mu        sync.Mutex
	installed map[string]release.Version
}

func newPeerPackages() *peerPackages {
	return &peerPackages{installed: map[string]release.Version{}}
}

func (p *peerPackages) LibDir(
	_ context.Context, pkgID string,
) (iterator.Iterator[string], error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	version, ok := p.installed[pkgID]
	if !ok {
		return nil, idepkg.ErrNotInstalled
	}
	return iterator.FromSlice([]string{
		"/home/peer/.rune/pkg/" + pkgID + "/" + string(version) + "/bin/" + pkgID,
	}), nil
}

func (p *peerPackages) InstallPackageVersion(
	_ context.Context, pkgID string, version release.Version, _ repl.ProgressWriter,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.installed[pkgID] = version
	return nil
}

func (p *peerPackages) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.installed)
}

func servePackages(pm idepkg.PackageManager) runenet.ServeOption {
	return runenet.WithServices(func(r grpc.ServiceRegistrar) {
		pkgrpc.NewServer(pm).Register(r)
	})
}

// clientUI is the UI of the user installing on the peer. These peers'
// packages change no config, so nothing is ever asked.
type clientUI struct {
	*idepkgtest.Notifications
}

func (clientUI) PromptConfig(idepkg.ConfigPrompt, func(bool)) {}

func hostPackages(t *testing.T, scheme schemeapi.Scheme) idepkg.PackageManager {
	t.Helper()
	cc, ok := scheme.(workspace.PackageHost).HostConn()
	require.True(t, ok)
	return pkgrpc.NewClient(cc, clientUI{idepkgtest.NewNotifications(t)})
}

func TestInstallsOnPeer(t *testing.T) {
	control := StartTestControl(t, true /* sameUser */)
	peer := StartNode(t, control, "peer", "")
	client := StartNode(t, control, "client", "")
	ServeWorkspaces(t, peer, servePackages(newPeerPackages()))
	WaitPeer(t, client, "peer")

	scheme := OpenWorkspace(t, client, "peer", t.TempDir())
	defer scheme.Close()
	pm := hostPackages(t, scheme)
	ctx := t.Context()

	_, err := pm.LibDir(ctx, "go")
	require.ErrorIs(t, err, idepkg.ErrNotInstalled)
	require.NoError(t, pm.InstallPackageVersion(ctx, "go", "1.24.0", nil))
	it, err := pm.LibDir(ctx, "go")
	require.NoError(t, err)
	paths, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.Equal(t, []string{"/home/peer/.rune/pkg/go/1.24.0/bin/go"}, paths)
}

func TestPeerWithoutPackageManagement(t *testing.T) {
	control := StartTestControl(t, true /* sameUser */)
	peer := StartNode(t, control, "peer", "")
	client := StartNode(t, control, "client", "")
	ServeWorkspaces(t, peer)
	WaitPeer(t, client, "peer")

	scheme := OpenWorkspace(t, client, "peer", t.TempDir())
	defer scheme.Close()
	_, err := hostPackages(t, scheme).LibDir(t.Context(), "go")
	require.ErrorIs(t, err, pkgrpc.ErrUnsupported)
}

func TestRejectsPackagesFromAnotherAccount(t *testing.T) {
	control := StartTestControl(t, false /* sameUser */)
	peer := StartNode(t, control, "peer", "")
	client := StartNode(t, control, "client", "")
	host := newPeerPackages()
	ServeWorkspaces(t, peer, servePackages(host))
	WaitPeer(t, client, "peer")

	scheme := OpenWorkspace(t, client, "peer", t.TempDir())
	defer scheme.Close()
	err := hostPackages(t, scheme).InstallPackageVersion(t.Context(), "go", "1.24.0", nil)
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(rootErr(err)),
		"a peer owned by another account must not install packages: %v", err)
	assert.Zero(t, host.count())
}
