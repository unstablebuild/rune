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

package workspacerune

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/runenet"
)

// stubMesh answers peer listings from a fixed set and dials every peer
// to the same address, which the tests point at a peer workspace
// server serving a temporary directory.
type stubMesh struct {
	peers []runenet.Peer
	addr  string
}

func (m stubMesh) Peers(context.Context) ([]runenet.Peer, error) {
	return m.peers, nil
}

func (m stubMesh) Dial(ctx context.Context, _ string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", m.addr)
}

// countingMesh is a stubMesh that counts its connections and can break
// them.
type countingMesh struct {
	stubMesh
	dials atomic.Int32
	// refuse is how many of the next connections are refused.
	refuse atomic.Int32

	mu    sync.Mutex
	conns []net.Conn
}

func (m *countingMesh) Dial(ctx context.Context, peer string) (net.Conn, error) {
	m.dials.Add(1)
	if m.refuse.Add(-1) >= 0 {
		return nil, errors.New("connection refused")
	}
	conn, err := m.stubMesh.Dial(ctx, peer)
	if err == nil {
		m.mu.Lock()
		m.conns = append(m.conns, conn)
		m.mu.Unlock()
	}
	return conn, err
}

// breakAll drops every connection the way a machine that went to
// sleep and came back does.
func (m *countingMesh) breakAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.conns {
		_ = c.Close()
	}
	m.conns = nil
}

func drain(t *testing.T, it iterator.Iterator[string]) []string {
	t.Helper()
	var ret []string
	for {
		v, ok := it.Next(context.Background())
		if !ok {
			break
		}
		ret = append(ret, v)
	}
	require.NoError(t, it.Err())
	return ret
}

func TestCompleterOffersPeers(t *testing.T) {
	mesh := stubMesh{peers: []runenet.Peer{
		{Hostname: "laptop", Online: true},
		{Hostname: "lab-box", Online: false},
		{Hostname: "workstation", Online: true},
	}}

	tsuite := []struct {
		name string
		// scheme is Scheme unless set.
		scheme string
		args   []string
		want   []string
	}{
		{
			name: "offers connected peers once the scheme is typed",
			args: []string{"workspaceopen", "rune://"},
			want: []string{"rune://laptop/", "rune://workstation/"},
		},
		{
			name: "filters by the typed peer prefix",
			args: []string{"workspaceopen", "rune://la"},
			want: []string{"rune://laptop/"},
		},
		{
			name: "stays out of plain path completion",
			args: []string{"workspaceopen", "/home/ernie"},
			want: nil,
		},
		{
			name: "no argument yet",
			args: []string{"workspaceopen"},
			want: nil,
		},
		{
			name:   "on its own scheme",
			scheme: "tailscale",
			args:   []string{"workspaceopen", "tailscale://w"},
			want:   []string{"tailscale://workstation/"},
		},
		{
			name:   "stays out of other schemes",
			scheme: "tailscale",
			args:   []string{"workspaceopen", "rune://"},
			want:   nil,
		},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			scheme := tcase.scheme
			if scheme == "" {
				scheme = Scheme
			}
			it, newLastArg, err := Completer(scheme, mesh).
				Complete(context.Background(), tcase.args)
			require.NoError(t, err)
			assert.Empty(t, newLastArg)
			assert.ElementsMatch(t, tcase.want, drain(t, it))
		})
	}
}

func TestCompleterOffersPeerDirectories(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"code", "code/rune", "config", ".cache"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0o755))
	}
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "notes.txt"), []byte("hi"), 0o600))

	mesh := stubMesh{
		peers: []runenet.Peer{{Hostname: "laptop", Online: true}},
		addr:  servePeerWorkspace(t, "/"),
	}

	tsuite := []struct {
		name string
		// scheme is Scheme unless set.
		scheme string
		last   string
		want   []string
	}{
		{
			name: "lists the directories of the typed directory",
			last: "rune://laptop" + root + "/",
			want: []string{
				"rune://laptop" + root + "/code/",
				"rune://laptop" + root + "/config/",
			},
		},
		{
			name: "filters by the typed prefix",
			last: "rune://laptop" + root + "/co",
			want: []string{
				"rune://laptop" + root + "/code/",
				"rune://laptop" + root + "/config/",
			},
		},
		{
			name: "descends into the picked directory",
			last: "rune://laptop" + root + "/code/",
			want: []string{"rune://laptop" + root + "/code/rune/"},
		},
		{
			name: "offers hidden directories once a dot is typed",
			last: "rune://laptop" + root + "/.",
			want: []string{"rune://laptop" + root + "/.cache/"},
		},
		{
			name:   "on its own scheme",
			scheme: "tailscale",
			last:   "tailscale://laptop" + root + "/code/",
			want:   []string{"tailscale://laptop" + root + "/code/rune/"},
		},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			scheme := tcase.scheme
			if scheme == "" {
				scheme = Scheme
			}
			it, newLastArg, err := Completer(scheme, mesh).Complete(
				context.Background(), []string{"workspaceopen", tcase.last})
			require.NoError(t, err)
			assert.Empty(t, newLastArg)
			got := drain(t, it)
			assert.ElementsMatch(t, tcase.want, got)
			for _, candidate := range got {
				assert.True(t, command.IsPartialCandidate(candidate),
					"peer directory candidate %q must be partial", candidate)
			}
		})
	}
}

func TestCompleterReusesItsConnection(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "code", "rune"), 0o755))
	mesh := &countingMesh{stubMesh: stubMesh{
		peers: []runenet.Peer{{Hostname: "laptop", Online: true}},
		addr:  servePeerWorkspace(t, "/"),
	}}
	conns := newPeerConns(mesh, time.Hour)
	c := completer(Scheme, mesh, conns)
	complete := func(last string) []string {
		t.Helper()
		it, _, err := c.Complete(context.Background(), []string{"workspaceopen", last})
		require.NoError(t, err)
		return drain(t, it)
	}

	for _, typed := range []string{"/", "/c", "/code/", "/code/r"} {
		require.NotEmpty(t, complete("rune://laptop"+root+typed), typed)
	}
	assert.Equal(t, int32(1), mesh.dials.Load(), "keystrokes share one connection")

	t.Run("a connection that broke while idle is replaced", func(t *testing.T) {
		// The machine was away when the connection tried to come back,
		// which leaves it waiting to try again.
		mesh.breakAll()
		mesh.refuse.Store(1)
		assert.Equal(t, []string{"rune://laptop" + root + "/code/rune/"},
			complete("rune://laptop"+root+"/code/"))
	})

	t.Run("an idle connection is closed", func(t *testing.T) {
		conns.idle = 10 * time.Millisecond
		complete("rune://laptop" + root + "/")
		dials := mesh.dials.Load()
		require.Eventually(t, func() bool {
			conns.mu.Lock()
			defer conns.mu.Unlock()
			return len(conns.conns) == 0
		}, 5*time.Second, 10*time.Millisecond)
		complete("rune://laptop" + root + "/")
		assert.Equal(t, dials+1, mesh.dials.Load())
	})
}
