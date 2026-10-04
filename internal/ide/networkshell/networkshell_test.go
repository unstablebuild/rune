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

package networkshell

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/runenet"
)

type fakeNetwork struct {
	status runenet.Status
	peers  []runenet.Peer
	up     int
	upErr  error
	down   int

	machines    []Machine
	machinesErr error
	removed     []string
	removeErr   error

	machineNames    []string
	machineNamesErr error
}

func (f *fakeNetwork) Status(context.Context) (runenet.Status, error) {
	return f.status, nil
}

func (f *fakeNetwork) Peers(context.Context) ([]runenet.Peer, error) {
	return f.peers, nil
}

func (f *fakeNetwork) Up(context.Context) error   { f.up++; return f.upErr }
func (f *fakeNetwork) Down(context.Context) error { f.down++; return nil }

func (f *fakeNetwork) Machines(context.Context) ([]Machine, error) {
	return f.machines, f.machinesErr
}

func (f *fakeNetwork) MachineNames(context.Context) ([]string, error) {
	return f.machineNames, f.machineNamesErr
}

func (f *fakeNetwork) Remove(_ context.Context, hostname string) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removed = append(f.removed, hostname)
	return nil
}

type nopProgressWriter struct{}

func (nopProgressWriter) Progress(int64, int64, string) {}

func handle(
	t *testing.T, h *Handler, args ...string,
) iterator.Iterator[component.Responsive] {
	t.Helper()
	it, err := h.HandleCommand(context.Background(),
		repl.Command{Name: CommandName, Args: args}, nopProgressWriter{})
	require.NoError(t, err)
	return it
}

func TestHandleCommandRouting(t *testing.T) {
	net := &fakeNetwork{
		status: runenet.Status{
			Hostname:  "laptop",
			State:     "Running",
			LoginName: "ernie@example.com",
			Addrs:     []netip.Addr{netip.MustParseAddr("100.64.0.1")},
		},
		peers: []runenet.Peer{{Hostname: "workstation", OS: "linux", Online: true}},
	}
	h := New(Config{Network: net})

	t.Run("status", func(t *testing.T) {
		require.NotNil(t, handle(t, h, "status"))
	})

	t.Run("peers", func(t *testing.T) {
		require.NotNil(t, handle(t, h, "peers"))
	})

	t.Run("up joins the network", func(t *testing.T) {
		require.NotNil(t, handle(t, h, "up"))
		assert.Equal(t, 1, net.up)
	})

	t.Run("up surfaces the join failure", func(t *testing.T) {
		failing := &fakeNetwork{upErr: errors.New("join network: bad auth key")}
		_, err := New(Config{Network: failing}).HandleCommand(
			context.Background(),
			repl.Command{Name: CommandName, Args: []string{"up"}},
			nopProgressWriter{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bad auth key")
	})

	t.Run("down leaves the network", func(t *testing.T) {
		require.NotNil(t, handle(t, h, "down"))
		assert.Equal(t, 1, net.down)
	})

	t.Run("machines lists the account's machines", func(t *testing.T) {
		require.NotNil(t, handle(t, h, "machines"))
	})

	t.Run("remove unregisters the named machine", func(t *testing.T) {
		require.NotNil(t, handle(t, h, "remove", "workstation"))
		assert.Equal(t, []string{"workstation"}, net.removed)
	})

	t.Run("remove needs exactly one machine", func(t *testing.T) {
		_, err := h.HandleCommand(context.Background(),
			repl.Command{Name: CommandName, Args: []string{"remove"}},
			nopProgressWriter{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "usage")
	})

	t.Run("remove surfaces the failure", func(t *testing.T) {
		failing := &fakeNetwork{removeErr: errors.New("no machine named \"gone\"")}
		_, err := New(Config{Network: failing}).HandleCommand(
			context.Background(),
			repl.Command{Name: CommandName, Args: []string{"remove", "gone"}},
			nopProgressWriter{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "gone")
	})

	t.Run("no argument prints usage", func(t *testing.T) {
		require.NotNil(t, handle(t, h))
	})

	t.Run("unknown subcommand is an error", func(t *testing.T) {
		_, err := h.HandleCommand(context.Background(),
			repl.Command{Name: CommandName, Args: []string{"sideways"}},
			nopProgressWriter{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown command")
	})
}

func TestStatusMarkdown(t *testing.T) {
	t.Run("joined", func(t *testing.T) {
		got := statusMarkdown(runenet.Status{
			Hostname:  "laptop",
			State:     "Running",
			LoginName: "ernie@example.com",
			Addrs:     []netip.Addr{netip.MustParseAddr("100.64.0.1")},
		})
		assert.Contains(t, got, "`laptop`")
		assert.Contains(t, got, "Running")
		assert.Contains(t, got, "`100.64.0.1`")
		assert.NotContains(t, got, "**error**")
	})

	t.Run("failed to join", func(t *testing.T) {
		got := statusMarkdown(runenet.Status{
			Hostname:  "laptop",
			State:     "NotRunning",
			LastError: "join network: control server unreachable",
		})
		assert.Contains(t, got, "NotRunning")
		assert.Contains(t, got,
			"- **error**: join network: control server unreachable")
	})

	t.Run("pending sign-in", func(t *testing.T) {
		got := statusMarkdown(runenet.Status{
			Hostname: "laptop",
			State:    "NeedsLogin",
			AuthURL:  "https://control.example.com/a/1",
		})
		assert.Contains(t, got, "https://control.example.com/a/1")
	})
}

// The list is what a user reads before deciding which machine to
// remove, so it has to say which one they are on and which ones are
// still reachable.
func TestMachinesMarkdown(t *testing.T) {
	lastSeen := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	got := machinesMarkdown([]Machine{
		{Hostname: "laptop", Online: true, LastSeen: lastSeen},
		{Hostname: "desktop"},
		{Hostname: "builder", Online: true, LastSeen: lastSeen, ServeOnly: true},
	}, "laptop")

	assert.Contains(t, got, "| laptop (this machine) | online |")
	assert.Contains(t, got, "| desktop | offline | never |")
	assert.Contains(t, got, "| builder (serve-only) | online |")
	assert.Contains(t, got, lastSeen.Local().Format("2006-01-02 15:04"))
	assert.Contains(t, got, "network remove <machine>")
}

func TestComplete(t *testing.T) {
	tsuite := []struct {
		name string
		net  fakeNetwork
		args []string
		want []string
	}{
		{
			name: "every subcommand",
			args: nil,
			want: []string{
				"status", "peers", "machines", "remove", "up", "down",
			},
		},
		{
			name: "filtered by prefix",
			args: []string{"p"},
			want: []string{"peers"},
		},
		{
			name: "subcommands take no arguments",
			args: []string{"peers", ""},
			want: nil,
		},
		{
			// `remove` names a machine, and the account's own list is
			// the only place those names can come from.
			name: "remove offers the account's machines",
			net:  fakeNetwork{machineNames: []string{"laptop", "desktop"}},
			args: []string{"remove", ""},
			want: []string{"laptop", "desktop"},
		},
		{
			name: "machines filtered by prefix",
			net:  fakeNetwork{machineNames: []string{"laptop", "desktop", "lab"}},
			args: []string{"remove", "la"},
			want: []string{"laptop", "lab"},
		},
		{
			// A completion runs on a keystroke, so an unreachable
			// account server offers nothing rather than failing.
			name: "unreachable account offers nothing",
			net: fakeNetwork{
				machineNamesErr: errors.New("account server unreachable"),
			},
			args: []string{"remove", ""},
			want: nil,
		},
		{
			name: "remove takes only one machine",
			net:  fakeNetwork{machineNames: []string{"laptop"}},
			args: []string{"remove", "laptop", ""},
			want: nil,
		},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			net := tcase.net
			h := New(Config{Network: &net})
			it, err := h.Complete(context.Background(), CommandName, tcase.args)
			require.NoError(t, err)
			var got []string
			for {
				v, ok := it.Next(context.Background())
				if !ok {
					break
				}
				got = append(got, v)
			}
			assert.ElementsMatch(t, tcase.want, got)
		})
	}
}
