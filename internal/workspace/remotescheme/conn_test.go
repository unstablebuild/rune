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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"unstable.build/rune/internal/workspace/schemetest"
)

// fakeConn answers every call with its own name.
type fakeConn struct{ name string }

func (c fakeConn) Invoke(
	_ context.Context, _ string, _, reply any, _ ...grpc.CallOption,
) error {
	*reply.(*string) = c.name
	return nil
}

func (c fakeConn) NewStream(
	context.Context, *grpc.StreamDesc, string, ...grpc.CallOption,
) (grpc.ClientStream, error) {
	return fakeStream{name: c.name}, nil
}

type fakeStream struct {
	grpc.ClientStream
	name string
}

type connScheme struct {
	schemeapi.Scheme
	cc grpc.ClientConnInterface
}

func (s connScheme) Conn() grpc.ClientConnInterface { return s.cc }

func closingScheme(ctrl *gomock.Controller) *schemetest.MockScheme {
	mock := schemetest.NewMockScheme(ctrl)
	mock.EXPECT().Close().Return(nil).AnyTimes()
	return mock
}

// connections serves the schemes sent on next, one per connect
// attempt, and reports each attempt's close hook on hooks.
func connections(
	hooks chan<- func(error), next <-chan schemeapi.Scheme,
) ConnectFn {
	return func(
		ctx context.Context, _ workspaceapi.URI, closeHook func(error),
	) (schemeapi.Scheme, error) {
		hooks <- closeHook
		select {
		case s := <-next:
			return s, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func invoke(ctx context.Context, cc grpc.ClientConnInterface) (string, error) {
	var got string
	err := cc.Invoke(ctx, "/pkg.Service/Method", nil, &got)
	return got, err
}

func TestHostConn(t *testing.T) {
	uri, err := workspaceapi.ParseURI("ssh://unstable.build/home/ernie")
	require.NoError(t, err)

	t.Run("calls follow the current connection", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		hooks := make(chan func(error), 2)
		next := make(chan schemeapi.Scheme)
		s := New(context.Background(), connections(hooks, next), uri, alwaysRetry)
		t.Cleanup(func() { require.NoError(t, s.Close()) })

		cc, ok := s.(*remoteScheme).HostConn()
		require.True(t, ok)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		_, err := invoke(ctx, cc)
		require.ErrorIs(t, err, context.DeadlineExceeded,
			"a call before the first connection honours its context")

		dropFirst := <-hooks
		next <- connScheme{Scheme: closingScheme(ctrl), cc: fakeConn{name: "first"}}
		got, err := invoke(context.Background(), cc)
		require.NoError(t, err)
		require.Equal(t, "first", got)

		dropped := errors.New("connection reset")
		dropFirst(dropped)
		_, err = invoke(context.Background(), cc)
		require.ErrorIs(t, err, dropped)

		<-hooks
		next <- connScheme{Scheme: closingScheme(ctrl), cc: fakeConn{name: "second"}}
		require.Eventually(t, func() bool {
			got, err := invoke(context.Background(), cc)
			return err == nil && got == "second"
		}, 5*time.Second, time.Millisecond)

		stream, err := cc.NewStream(context.Background(), &grpc.StreamDesc{}, "/pkg.Service/Stream")
		require.NoError(t, err)
		require.Equal(t, "second", stream.(fakeStream).name)
	})

	t.Run("a transport without other services is unimplemented", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		hooks := make(chan func(error), 1)
		next := make(chan schemeapi.Scheme, 1)
		next <- closingScheme(ctrl)
		s := New(context.Background(), connections(hooks, next), uri, alwaysRetry)
		t.Cleanup(func() { require.NoError(t, s.Close()) })

		cc, _ := s.(*remoteScheme).HostConn()
		_, err := invoke(context.Background(), cc)
		require.Equal(t, codes.Unimplemented, status.Code(err))
		_, err = cc.NewStream(context.Background(), &grpc.StreamDesc{}, "/pkg.Service/Stream")
		require.Equal(t, codes.Unimplemented, status.Code(err))
	})

	t.Run("a closed scheme reports it", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		hooks := make(chan func(error), 1)
		next := make(chan schemeapi.Scheme, 1)
		next <- connScheme{Scheme: closingScheme(ctrl), cc: fakeConn{name: "only"}}
		s := New(context.Background(), connections(hooks, next), uri, alwaysRetry)
		cc, _ := s.(*remoteScheme).HostConn()
		_, err := invoke(context.Background(), cc)
		require.NoError(t, err)

		require.NoError(t, s.Close())
		_, err = invoke(context.Background(), cc)
		require.ErrorIs(t, err, ErrRemoteClosed)
	})
}
