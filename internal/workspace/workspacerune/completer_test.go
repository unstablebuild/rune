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
	"net"
	"os"
	"path/filepath"
	"testing"

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
		args []string
		want []string
	}{
		{
			name: "offers every peer once the scheme is typed",
			args: []string{"workspaceopen", "rune://"},
			want: []string{"rune://laptop/", "rune://lab-box/", "rune://workstation/"},
		},
		{
			name: "filters by the typed peer prefix",
			args: []string{"workspaceopen", "rune://la"},
			want: []string{"rune://laptop/", "rune://lab-box/"},
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
	}

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			it, newLastArg, err := Completer(mesh).
				Complete(context.Background(), tcase.args)
			require.NoError(t, err)
			assert.Empty(t, newLastArg)
			assert.ElementsMatch(t, tcase.want, drain(t, it))
		})
	}
}

// TestCompleterOffersPeerDirectories covers completion past the
// hostname: only the peer knows its filesystem, so the completions
// come from a round-trip to it.
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
		last string
		want []string
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
	}

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			it, newLastArg, err := Completer(mesh).Complete(
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
