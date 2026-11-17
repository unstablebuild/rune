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
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/workspace/workspacerpc/vterpc"
)

// VTEHosts resolves the [vte.Host] of the terminals of the workspace
// rooted at root on this machine. An empty root is the machine's own.
// It is how one server scopes the terminals of every workspace it
// serves.
type VTEHosts func(ctx context.Context, root string) (vte.Host, error)

// VTEServer serves the lifecycle of the terminals this host owns over
// the VTE service.
//
// Attachments are deliberately absent: they are a raw byte stream
// between a client's emulator and a pty, served on a listener of their
// own (see vteregistry.Serve and vtewire).
type VTEServer struct {
	vterpc.UnimplementedVTEServer

	hosts VTEHosts
}

var _ vterpc.VTEServer = (*VTEServer)(nil)

// NewVTEServer returns a server of the terminals hosts resolves.
func NewVTEServer(hosts VTEHosts) *VTEServer {
	if hosts == nil {
		panic("workspacerpc: VTEHosts is required")
	}
	return &VTEServer{hosts: hosts}
}

// List satisfies vterpc.VTEServer.
func (s *VTEServer) List(
	ctx context.Context, req *vterpc.ListRequest,
) (*vterpc.ListResponse, error) {
	host, err := s.hosts(ctx, req.GetRoot())
	if err != nil {
		return nil, vteStatus(err)
	}
	infos, err := host.ListVTEs(ctx)
	if err != nil {
		return nil, vteStatus(err)
	}
	resp := &vterpc.ListResponse{Vtes: make([]*vterpc.Info, 0, len(infos))}
	for _, info := range infos {
		msg := new(vterpc.Info)
		msg.FromModel(info)
		resp.Vtes = append(resp.Vtes, msg)
	}
	return resp, nil
}

// Liveness satisfies vterpc.VTEServer.
func (s *VTEServer) Liveness(
	ctx context.Context, req *vterpc.LivenessRequest,
) (*vterpc.LivenessResponse, error) {
	host, err := s.hosts(ctx, req.GetRoot())
	if err != nil {
		return nil, vteStatus(err)
	}
	l, err := host.VTELiveness(ctx, vte.ID(req.GetId()))
	if err != nil {
		return nil, vteStatus(err)
	}
	return &vterpc.LivenessResponse{Liveness: vterpc.LivenessFromModel(l)}, nil
}

// Delete satisfies vterpc.VTEServer.
func (s *VTEServer) Delete(
	ctx context.Context, req *vterpc.DeleteRequest,
) (*vterpc.DeleteResponse, error) {
	host, err := s.hosts(ctx, req.GetRoot())
	if err != nil {
		return nil, vteStatus(err)
	}
	if err := host.DeleteVTE(ctx, vte.ID(req.GetId())); err != nil {
		return nil, vteStatus(err)
	}
	return new(vterpc.DeleteResponse), nil
}

// Watch satisfies vterpc.VTEServer. Changes are sent in the order the
// host tells them; a client that stops reading is dropped by the
// transport's own flow control, not by this server.
func (s *VTEServer) Watch(
	req *vterpc.WatchRequest, stream vterpc.VTE_WatchServer,
) error {
	ctx := stream.Context()
	host, err := s.hosts(ctx, req.GetRoot())
	if err != nil {
		return vteStatus(err)
	}
	// The host tells changes on its own goroutines and must not be
	// held up by the stream, so they are queued here and sent from
	// this goroutine. A queue this deep only fills if the client stops
	// reading, at which point the stream ends and the client catches
	// up from a listing.
	changes := make(chan vte.Change, 1024)
	overflow := make(chan struct{})
	var once sync.Once
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	host.Watch(wctx, func(change vte.Change) {
		select {
		case changes <- change:
		default:
			once.Do(func() { close(overflow) })
		}
	})
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-overflow:
			return status.Error(codes.ResourceExhausted, "terminal changes not read in time")
		case change := <-changes:
			msg := new(vterpc.Change)
			msg.FromModel(change)
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
	}
}

// vteStatus preserves the distinction clients branch on: only a
// confirmed-absent terminal may be replaced, so ErrVTENotFound must
// survive the transport as a typed code.
func vteStatus(err error) error {
	if errors.Is(err, vte.ErrNotFound) {
		return status.Error(codes.NotFound, err.Error())
	}
	return err
}
