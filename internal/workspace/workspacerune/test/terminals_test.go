//go:build e2e

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
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/runenet"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vtebench"
	"unstable.build/rune/internal/term/vte/vteclient"
	"unstable.build/rune/internal/workspace"
	tworkspacerpc "unstable.build/rune/internal/workspace/workspacerpc"
	"unstable.build/rune/internal/workspace/workspacerune"
)

// breakableDialer dials through a mesh node and remembers the
// connections, so a test can drop them all as a network loss does.
type breakableDialer struct {
	node *runenet.Node

	mu    sync.Mutex
	conns []net.Conn
}

func (d *breakableDialer) Dial(ctx context.Context, peer string) (net.Conn, error) {
	return d.keep(d.node.Dial(ctx, peer))
}

func (d *breakableDialer) DialAttachment(ctx context.Context, peer string) (net.Conn, error) {
	return d.keep(d.node.DialAttachment(ctx, peer))
}

func (d *breakableDialer) keep(conn net.Conn, err error) (net.Conn, error) {
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.conns = append(d.conns, conn)
	d.mu.Unlock()
	return conn, nil
}

func (d *breakableDialer) breakAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.conns {
		_ = c.Close()
	}
	d.conns = nil
}

// tick prints a tick every 100ms, for a minute.
var tick = []string{"sh", "-c",
	"'i=0; while [ $i -lt 600 ]; do i=$((i+1)); echo tick $i; sleep 0.1; done'"}

var tickRE = regexp.MustCompile(`tick (\d+)`)

// lastTick is the highest tick number on screen, 0 when none.
func lastTick(screen string) int {
	last := 0
	for _, m := range tickRE.FindAllStringSubmatch(screen, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n > last {
			last = n
		}
	}
	return last
}

// screenOf renders v's active screen.
func screenOf(t *testing.T, v vte.View) string {
	t.Helper()
	snap, err := v.Snapshot()
	require.NoError(t, err)
	var b strings.Builder
	for _, row := range snap.ActiveCells() {
		for _, c := range row {
			if c.Ch == 0 {
				b.WriteByte(' ')
				continue
			}
			b.WriteRune(c.Ch)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// typer is the replica's way to the host's pty.
type typer interface {
	Component() *vte.Component
}

// plainShellEnv makes a peer, which runs the test's login shell with its
// dotfiles, run /bin/sh with an empty home instead: the developer's
// prompt may wait on something and hang the shell scenario, and /bin/sh
// is the same shell everywhere, with job control, as a terminal's
// liveness needs.
func plainShellEnv(t *testing.T) []string {
	t.Helper()
	return []string{
		"SHELL=/bin/sh", "HOME=" + t.TempDir(), "ENV=",
		"BASH_SILENCE_DEPRECATION_WARNING=1",
	}
}

// plainShell applies [plainShellEnv] to this process, for a peer in it.
// Not for a test that drives docker: its CLI finds its contexts under
// the home.
func plainShell(t *testing.T) {
	t.Helper()
	for _, kv := range plainShellEnv(t) {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
}

// meshTerminals is a client's hold on the terminals of the workspace
// dir on peer: the owner a tab is opened from, and what the peer tells
// of them.
type meshTerminals struct {
	ctx    context.Context
	dialer *breakableDialer
	host   *tworkspacerpc.VTEClient
	owner  vte.Host

	mu      sync.Mutex
	changes []vte.Change
}

// openMeshTerminals opens the workspace dir on peer from client, as an
// editor with a rune:// workspace does, and holds its terminals.
func openMeshTerminals(t *testing.T, client *runenet.Node, peer, dir string) *meshTerminals {
	t.Helper()
	m := &meshTerminals{dialer: &breakableDialer{node: client}}
	uri, err := workspaceapi.ParseURI("rune://" + peer + dir)
	require.NoError(t, err)
	scheme, err := workspacerune.New(workspacerune.Scheme, m.dialer)(
		context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), nodeJoinTimeout)
	t.Cleanup(cancel)
	m.ctx = ctx
	require.NoError(t, scheme.(workspace.RemoteScheme).WaitConnected(ctx))

	p, ok := scheme.(workspace.PeerScheme).Peer()
	require.True(t, ok, "a peer serves the workspace")
	root, err := p.Root(ctx)
	require.NoError(t, err)
	m.host = tworkspacerpc.NewVTEClient(p.Conn(), root)
	loop := vtebench.NewLoop(t)
	shown := vtebench.NewHost()
	m.owner = vteclient.New(p, shown, shown, shown, vteclient.Config{
		VTE: vtebench.Config(loop, nil), Machine: peer,
	})
	t.Cleanup(func() { _ = m.owner.Close() })
	m.owner.Watch(ctx, func(change vte.Change) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.changes = append(m.changes, change)
	})
	return m
}

// told reports whether a change of id satisfying want was told.
func (m *meshTerminals) told(id vte.ID, want func(vte.Change) bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.changes {
		if c.ID == id && want(c) {
			return true
		}
	}
	return false
}

// listed is what the peer lists of id, false when nothing.
func (m *meshTerminals) listed(id vte.ID) (vte.Info, bool) {
	infos, err := m.host.ListVTEs(m.ctx)
	if err != nil {
		return vte.Info{}, false
	}
	for _, info := range infos {
		if info.ID == id {
			return info, true
		}
	}
	return vte.Info{}, false
}

func TestMeshTerminals(t *testing.T) {
	plainShell(t)
	control := StartTestControl(t, true /* sameUser */)
	peer := StartNode(t, control, "peer", "")
	client := StartNode(t, control, "client", "")
	ServeWorkspaces(t, peer)
	WaitPeer(t, client, "peer")
	testMeshTerminals(t, openMeshTerminals(t, client, "peer", t.TempDir()))
}

// TestInstanceTerminals runs the peer as its own process, as a
// `rune --headless` or a Rune serving its machine is to an editor, and
// stops it: its terminals end with it, and their tabs are told.
func TestInstanceTerminals(t *testing.T) {
	SkipIfNoDocker(t)
	control := StartHeadscale(t)
	inst := StartInstance(t, control, "peer", control.AuthKey,
		WithInstanceEnv(plainShellEnv(t)...))
	client := StartNode(t, control, "client", control.AuthKey)
	WaitPeer(t, client, "peer")
	m := openMeshTerminals(t, client, "peer", t.TempDir())
	testMeshTerminals(t, m)

	t.Run("the peer stopping ends its terminals", func(t *testing.T) {
		v, err := m.owner.CreateVTE(m.ctx, vte.CreateOptions{
			CommandAndArgs: tick, Width: 60, Height: 12,
		})
		require.NoError(t, err)
		require.Eventually(t, func() bool { return lastTick(screenOf(t, v)) >= 3 },
			30*time.Second, 50*time.Millisecond)
		inst.Stop(t)
		require.Eventually(t, v.IsComplete, 30*time.Second, 50*time.Millisecond,
			"the tab is told the terminal ended, not left to wait for the peer")
		require.Eventually(t, func() bool {
			return m.told(v.ID(), func(c vte.Change) bool { return c.Deleted })
		}, 30*time.Second, 50*time.Millisecond)
	})
}

// testMeshTerminals proves the terminals of a rune:// workspace are the
// peer's: they survive the connection and the tabs that show them, are
// reported while detached, and end only when told to.
func testMeshTerminals(t *testing.T, m *meshTerminals) {
	ctx, owner, host, dialer, told, listed :=
		m.ctx, m.owner, m.host, m.dialer, m.told, m.listed

	t.Run("survives the connection", func(t *testing.T) {
		v, err := owner.CreateVTE(ctx, vte.CreateOptions{
			CommandAndArgs: tick, Width: 60, Height: 12,
		})
		require.NoError(t, err)
		id := v.ID()
		require.Eventually(t, func() bool { return lastTick(screenOf(t, v)) >= 3 },
			30*time.Second, 50*time.Millisecond, "the program's output reaches the replica")
		info, ok := listed(id)
		require.True(t, ok)
		assert.Equal(t, 1, info.Attachments)

		// The link drops: the program keeps running on the peer, and the
		// replica catches up with what it printed meanwhile.
		before := lastTick(screenOf(t, v))
		dialer.breakAll()
		require.Eventually(t, func() bool { return lastTick(screenOf(t, v)) > before+5 },
			30*time.Second, 50*time.Millisecond, "the replica resyncs after the link dropped")
		assert.False(t, v.IsComplete())
		require.Eventually(t, func() bool {
			info, ok := listed(id)
			return ok && info.Attachments == 1
		}, 30*time.Second, 50*time.Millisecond, "the replica is attached again, over a new link")

		// Closing the tab leaves the program running, detached, which the
		// peer reports; another tab attaches to it by its id.
		require.NoError(t, v.Close())
		require.Eventually(t, func() bool {
			return told(id, func(c vte.Change) bool {
				return c.Attachments == 0 && c.Liveness == vte.LivenessRunning
			})
		}, 30*time.Second, 50*time.Millisecond, "the detached program is reported running")
		again, err := owner.AttachVTE(ctx, id)
		require.NoError(t, err)
		last := lastTick(screenOf(t, again))
		require.Eventually(t, func() bool { return lastTick(screenOf(t, again)) > last },
			30*time.Second, 50*time.Millisecond, "the terminal is live in its new tab")
		require.Eventually(t, func() bool {
			return told(id, func(c vte.Change) bool { return c.Attachments == 1 })
		}, 30*time.Second, 50*time.Millisecond)

		// Ending the terminal ends the program on the peer.
		require.NoError(t, owner.DeleteVTE(ctx, id))
		require.Eventually(t, again.IsComplete, 30*time.Second, 50*time.Millisecond)
		_, ok = listed(id)
		assert.False(t, ok, "the peer no longer lists it")
		require.Eventually(t, func() bool {
			return told(id, func(c vte.Change) bool { return c.Deleted })
		}, 30*time.Second, 50*time.Millisecond)
	})

	t.Run("a detached shell reports its job done", func(t *testing.T) {
		v, err := owner.CreateVTE(ctx, vte.CreateOptions{Width: 60, Height: 12})
		require.NoError(t, err)
		id := v.ID()
		require.NoError(t, v.(typer).Component().WriteToPty([]byte("sleep 5; echo slept\r")))
		// A shell detached before it took the command up looks idle,
		// and is left alone, as it would be in a tab.
		require.Eventually(t, func() bool {
			l, err := host.VTELiveness(ctx, id)
			return err == nil && l == vte.LivenessRunning
		}, 30*time.Second, 50*time.Millisecond, "the shell runs the job: %q", screenOf(t, v))
		require.NoError(t, v.Close())
		require.Eventually(t, func() bool {
			return told(id, func(c vte.Change) bool {
				return c.Attachments == 0 && c.Liveness == vte.LivenessRunning
			})
		}, 30*time.Second, 50*time.Millisecond, "the job is reported running")
		require.Eventually(t, func() bool {
			return told(id, func(c vte.Change) bool {
				return c.Liveness == vte.LivenessIdle
			})
		}, 30*time.Second, 50*time.Millisecond, "then done, once the shell is back at its prompt")
		again, err := owner.AttachVTE(ctx, id)
		require.NoError(t, err)
		assert.Contains(t, screenOf(t, again), "slept", "the output waited in the terminal")
		require.NoError(t, owner.DeleteVTE(ctx, id))
	})
}
