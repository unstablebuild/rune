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

// Package runetest contains the end-to-end harness for the rune://
// workspace scheme. The scheme only exists to connect two Rune
// instances over their embedded mesh, so its tests need a real
// coordination server and a real second instance; this package
// provides both, against a dockerized Headscale (the coordination
// server Rune ships with) or against an in-process control plane for
// runs without docker.
package runetest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"tailscale.com/tstest/integration/testcontrol"
	"unstable.build/rune/internal/runenet"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacerune"
)

// ControlPlane is a coordination server two Rune instances can meet on.
type ControlPlane struct {
	// URL is the coordination server the instances register with.
	URL string
	// AuthKey pre-authorizes a node so no interactive login is needed.
	// Empty for the in-process control plane, which needs none.
	AuthKey string

	// headscale is the docker container id of a Headscale control
	// plane; testControl is the in-process one. Exactly one is set.
	headscale   string
	testControl *testcontrol.Server
}

// StartNode joins a mesh node to control under hostname, registering it
// with authKey, and waits for it to come online.
func StartNode(
	t *testing.T, control ControlPlane, hostname, authKey string,
) *runenet.Node {
	t.Helper()
	return startNodeIn(t, control, hostname, authKey, t.TempDir())
}

// startNodeIn is [StartNode] with the node's data directory chosen by
// the caller, so a node can start from an identity already on disk.
func startNodeIn(
	t *testing.T, control ControlPlane, hostname, authKey, dir string,
) *runenet.Node {
	t.Helper()

	node := runenet.New(runenet.Config{
		Hostname:   hostname,
		ControlURL: control.URL,
		AuthKey:    authKey,
		Port:       runenet.DefaultPort,
		Dir:        dir,
	})
	t.Cleanup(func() { _ = node.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), nodeJoinTimeout)
	defer cancel()
	if err := node.Up(ctx); err != nil {
		st, _ := node.Status(context.Background())
		t.Fatalf("node %q did not join the network: %v "+
			"(state=%q auth_url=%q last_error=%q)",
			hostname, err, st.State, st.AuthURL, st.LastError)
	}
	return node
}

// WaitPeer blocks until node sees peer in its netmap. The coordination
// server pushes peers asynchronously, so a test that dials immediately
// after joining would race the first netmap delivery.
func WaitPeer(t *testing.T, node *runenet.Node, peer string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), nodeJoinTimeout)
	defer cancel()

	for {
		peers, err := node.Peers(ctx)
		if err == nil {
			for _, p := range peers {
				if p.Hostname == peer {
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("peer %q never appeared on the network", peer)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// nodeJoinTimeout bounds joining the mesh and the first netmap
// delivery. Generous because it covers key generation, control
// registration and the DERP handshake on a loaded machine.
const nodeJoinTimeout = 90 * time.Second

// SkipIfRace skips the scheme conformance suites when the race
// detector is on. Each suite drives ~130 cases through a userspace
// network stack, and instrumenting gVisor's packet path costs minutes
// per suite for no signal about the code under test — the scheme, not
// the network stack. The cheaper end-to-end tests in this package still
// run under -race.
//
// Run them with:
//
//	go test -count=1 ./internal/workspace/workspacerune/test/
func SkipIfRace(t *testing.T) {
	t.Helper()
	if raceEnabled {
		t.Skip("scheme conformance suites are prohibitively slow " +
			"under -race; run without it")
	}
}

// ServeWorkspaces exposes node's filesystem to its peers exactly as a
// running Rune instance does: rooted at "/", so a rune:// URI can name
// any path the user could open locally. It returns the data directory
// the instance advertises to peers as its install root. opts add the
// services a running instance serves next to the workspace.
func ServeWorkspaces(
	t *testing.T, node *runenet.Node, opts ...runenet.ServeOption,
) string {
	t.Helper()

	uri, err := workspaceapi.CurrentUserHostURI("/")
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)

	dataDir := t.TempDir()
	server, err := runenet.ServeWorkspace(node, scheme, dataDir, opts...)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = server.Close()
		_ = scheme.Close()
	})
	return dataDir
}

// OpenWorkspace opens path on peer through the rune:// scheme and
// blocks until the connection has settled, so a caller's first call
// measures the operation rather than the dial.
func OpenWorkspace(
	t *testing.T, client *runenet.Node, peer, path string,
) schemeapi.Scheme {
	t.Helper()

	uri, err := workspaceapi.ParseURI("rune://" + peer + path)
	require.NoError(t, err)

	scheme, err := workspacerune.New(client)(
		context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), nodeJoinTimeout)
	defer cancel()
	require.NoError(t, scheme.(workspace.RemoteScheme).WaitConnected(ctx))
	return scheme
}
