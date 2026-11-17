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

package workspacerpc

import (
	"context"
	"errors"
	"io"

	"github.com/unstablebuild/blue/iterator"
	grpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/workspace/workspacerpc/vterpc"
)

// VTEClient reaches the lifecycle of the terminals a workspace's host
// owns: which exist, what they run, terminating one, and what changes.
//
// Attaching to a terminal is not here. An attachment is a raw byte
// stream between a client's emulator and a pty, so it has a transport
// of its own (see vtewire), apart from the connection this client
// uses.
type VTEClient struct {
	c    vterpc.VTEClient
	root string
}

// NewVTEClient returns a client of the terminals of the workspace
// rooted at root on the host conn reaches. An empty root is the host's
// own.
func NewVTEClient(conn grpc.ClientConnInterface, root string) *VTEClient {
	return &VTEClient{c: vterpc.NewVTEClient(conn), root: root}
}

// ListVTEs is [vte.Host.ListVTEs] on the host.
func (c *VTEClient) ListVTEs(ctx context.Context) ([]vte.Info, error) {
	resp, err := c.c.List(ctx, &vterpc.ListRequest{Root: c.root})
	if err != nil {
		return nil, vteError(err)
	}
	ret := make([]vte.Info, 0, len(resp.GetVtes()))
	for _, info := range resp.GetVtes() {
		ret = append(ret, info.ToModel())
	}
	return ret, nil
}

// VTELiveness is [vte.Host.VTELiveness] on the host.
func (c *VTEClient) VTELiveness(
	ctx context.Context, id vte.ID,
) (vte.Liveness, error) {
	resp, err := c.c.Liveness(ctx, &vterpc.LivenessRequest{Id: string(id), Root: c.root})
	if err != nil {
		return vte.LivenessUnknown, vteError(err)
	}
	return resp.GetLiveness().ToModel(), nil
}

// DeleteVTE is [vte.Host.DeleteVTE] on the host.
func (c *VTEClient) DeleteVTE(ctx context.Context, id vte.ID) error {
	_, err := c.c.Delete(ctx, &vterpc.DeleteRequest{Id: string(id), Root: c.root})
	return vteError(err)
}

// WatchVTEs streams what [vte.Host.Watch] tells on the host. The
// iterator ends when the stream does; Close ends it.
func (c *VTEClient) WatchVTEs(
	ctx context.Context,
) (iterator.Iterator[vte.Change], error) {
	ctx, cancel := context.WithCancel(ctx)
	stream, err := c.c.Watch(ctx, &vterpc.WatchRequest{Root: c.root})
	if err != nil {
		cancel()
		return nil, vteError(err)
	}
	return iterator.FromFunc(func(context.Context) (vte.Change, bool, error) {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) || status.Code(err) == codes.Canceled {
			return vte.Change{}, false, nil
		}
		if err != nil {
			return vte.Change{}, false, vteError(err)
		}
		return msg.ToModel(), true, nil
	}, func() error {
		cancel()
		return nil
	}), nil
}

// vteError restores the sentinel the status code stands for: NotFound
// keeps a confirmed missing terminal distinguishable from a transport
// failure.
func vteError(err error) error {
	if err == nil {
		return nil
	}
	if status.Code(err) == codes.NotFound {
		return vte.ErrNotFound
	}
	return err
}
