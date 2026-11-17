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

package vteclient

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vtebench"
	"unstable.build/rune/internal/term/vte/vteregistry"
)

// socketHost reaches a registry served on a unix socket in this
// process, as a mesh peer's would be over its attachment port.
type socketHost struct {
	r    *vteregistry.Registry
	addr string
}

func (h *socketHost) Root(context.Context) (string, error) { return "", nil }

func (h *socketHost) ListVTEs(ctx context.Context) ([]vte.Info, error) {
	return h.r.ListVTEs(ctx)
}

func (h *socketHost) VTELiveness(ctx context.Context, id vte.ID) (vte.Liveness, error) {
	return h.r.VTELiveness(ctx, id)
}

func (h *socketHost) DeleteVTE(ctx context.Context, id vte.ID) error {
	return h.r.DeleteVTE(ctx, id)
}

func (h *socketHost) WatchVTEs(context.Context) (iterator.Iterator[vte.Change], error) {
	return iterator.Empty[vte.Change](), nil
}

func (h *socketHost) DialVTE(ctx context.Context) (io.ReadWriteCloser, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", h.addr)
}

// TestVTEBench measures a replica of a terminal owned by a Registry in
// this process, fed over a unix socket: what showing a peer's terminal
// costs over the loopback, server included.
func TestVTEBench(t *testing.T) {
	loop := vtebench.NewLoop(t)
	scheme := vtebench.LocalScheme(t)

	serverHost := vtebench.NewHost()
	r := vteregistry.New(serverHost, serverHost, scheme, scheme, serverHost, vteregistry.Config{
		VTE: vtebench.Config(loop, nil),
	})
	t.Cleanup(func() { _ = r.Close() })
	dir, err := os.MkdirTemp("", "vtebench")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	lis, err := net.Listen("unix", filepath.Join(dir, "vte.sock"))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = lis.Close()
	})
	go debug.CapturePanicReport(func() { _ = r.Serve(ctx, lis) })

	host := vtebench.NewHost()
	o := newOwner(&socketHost{r: r, addr: lis.Addr().String()}, host, host, host, Config{
		VTE: vtebench.Config(loop, nil),
	})
	t.Cleanup(func() { _ = o.Close() })
	replica := vtebench.Path{
		Name: "replica",
		Open: func(t *testing.T, cmd []string) *vtebench.VTE {
			v, err := o.CreateVTE(context.Background(), vte.CreateOptions{
				CommandAndArgs: cmd, Width: vtebench.Width, Height: vtebench.Height,
			})
			require.NoError(t, err)
			rep := v.(*replica)
			loop.Schedule(func() { rep.Resize(vtebench.Width, vtebench.Height) })
			return &vtebench.VTE{
				Comp: rep.Handler.Component(), Host: host,
				Close: func() {
					id := rep.ID()
					_ = rep.Close()
					_ = r.DeleteVTE(context.Background(), id)
				},
			}
		},
	}

	vtebench.Run(t, []vtebench.Path{replica})
}
