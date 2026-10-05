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

package remotescheme

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Conner is implemented by a scheme a [ConnectFn] returns when the
// connection it runs on can carry other gRPC services to the host.
type Conner interface {
	Conn() grpc.ClientConnInterface
}

var _ grpc.ClientConnInterface = (*remoteScheme)(nil)

// HostConn returns a connection to the host that follows reconnects:
// every call goes over whichever connection is current. It reports
// ok for every remote scheme; calls fail with codes.Unimplemented when
// the transport carries no other services.
func (s *remoteScheme) HostConn() (grpc.ClientConnInterface, bool) {
	return s, true
}

// Invoke implements grpc.ClientConnInterface. While disconnected it
// fails with the error that dropped the connection.
func (s *remoteScheme) Invoke(
	ctx context.Context, method string, args, reply any,
	opts ...grpc.CallOption,
) error {
	cc, err := s.conn(ctx)
	if err != nil {
		return err
	}
	return cc.Invoke(ctx, method, args, reply, opts...)
}

// NewStream implements grpc.ClientConnInterface. While disconnected
// it fails with the error that dropped the connection.
func (s *remoteScheme) NewStream(
	ctx context.Context, desc *grpc.StreamDesc, method string,
	opts ...grpc.CallOption,
) (grpc.ClientStream, error) {
	cc, err := s.conn(ctx)
	if err != nil {
		return nil, err
	}
	return cc.NewStream(ctx, desc, method, opts...)
}

func (s *remoteScheme) conn(ctx context.Context) (grpc.ClientConnInterface, error) {
	if err := s.WaitConnected(ctx); err != nil {
		return nil, err
	}
	scheme, _, err := s.stateWithGeneration()
	if err != nil {
		return nil, err
	}
	c, ok := scheme.(Conner)
	if !ok {
		return nil, status.Error(codes.Unimplemented,
			"the connection to the host carries no other services")
	}
	return c.Conn(), nil
}
