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

package vteclient

import (
	"context"
	"io"

	"github.com/unstablebuild/blue/iterator"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacerpc"
)

// host is what an Owner needs of the machine that owns the terminals.
type host interface {
	// Root is the workspace's root as the host names it, which scopes
	// its terminals. It fails until the host resolved it.
	Root(context.Context) (string, error)
	ListVTEs(context.Context) ([]vte.Info, error)
	VTELiveness(context.Context, vte.ID) (vte.Liveness, error)
	DeleteVTE(context.Context, vte.ID) error
	// WatchVTEs streams what [vte.Host.Watch] tells on the host, until
	// ctx ends or the connection drops.
	WatchVTEs(context.Context) (iterator.Iterator[vte.Change], error)
	// DialVTE opens the byte stream of one attachment to a terminal of
	// the host, over which the attachment's handshake follows.
	DialVTE(context.Context) (io.ReadWriteCloser, error)
}

// peerHost reaches the terminals of a peer's workspace: their lifecycle
// over the workspace connection, and each attachment over a stream of
// its own.
type peerHost struct{ peer workspace.Peer }

func (h peerHost) Root(ctx context.Context) (string, error) {
	return h.peer.Root(ctx)
}

func (h peerHost) lifecycle(ctx context.Context) (*workspacerpc.VTEClient, error) {
	root, err := h.peer.Root(ctx)
	if err != nil {
		return nil, err
	}
	return workspacerpc.NewVTEClient(h.peer.Conn(), root), nil
}

func (h peerHost) ListVTEs(ctx context.Context) ([]vte.Info, error) {
	c, err := h.lifecycle(ctx)
	if err != nil {
		return nil, err
	}
	return c.ListVTEs(ctx)
}

func (h peerHost) VTELiveness(ctx context.Context, id vte.ID) (vte.Liveness, error) {
	c, err := h.lifecycle(ctx)
	if err != nil {
		return vte.LivenessUnknown, err
	}
	return c.VTELiveness(ctx, id)
}

func (h peerHost) DeleteVTE(ctx context.Context, id vte.ID) error {
	c, err := h.lifecycle(ctx)
	if err != nil {
		return err
	}
	return c.DeleteVTE(ctx, id)
}

func (h peerHost) WatchVTEs(ctx context.Context) (iterator.Iterator[vte.Change], error) {
	c, err := h.lifecycle(ctx)
	if err != nil {
		return nil, err
	}
	return c.WatchVTEs(ctx)
}

func (h peerHost) DialVTE(ctx context.Context) (io.ReadWriteCloser, error) {
	return h.peer.DialAttachment(ctx)
}
