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

package runenet

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vtewire"
	tworkspacerpc "unstable.build/rune/internal/workspace/workspacerpc"
	"unstable.build/rune/internal/workspace/workspacetest"
)

func peerCtx(addr string) context.Context {
	return peer.NewContext(context.Background(),
		&peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(addr), Port: 4242}})
}

func TestPeerAuthorizer(t *testing.T) {
	self := func(context.Context) (string, error) { return "ernie@example.com", nil }

	t.Run("admits a machine owned by the same account", func(t *testing.T) {
		a := NewPeerAuthorizer(func(context.Context, string) (Caller, error) {
			return Caller{Login: "ernie@example.com"}, nil
		}, self)
		require.NoError(t, a.Authorize(peerCtx("100.64.0.2")))
	})

	t.Run("rejects a machine owned by another account", func(t *testing.T) {
		a := NewPeerAuthorizer(func(context.Context, string) (Caller, error) {
			return Caller{Login: "mallory@example.com"}, nil
		}, self)
		err := a.Authorize(peerCtx("100.64.0.3"))
		require.Error(t, err)
		assert.Equal(t, codes.PermissionDenied, status.Code(err))
	})

	// Tagged machines never open connections under the mesh policy;
	// if one gets a packet through anyway, the claimed account is not
	// enough to let it in.
	t.Run("rejects a tagged machine claiming the same account", func(t *testing.T) {
		a := NewPeerAuthorizer(func(context.Context, string) (Caller, error) {
			return Caller{
				Login: "ernie@example.com",
				Tags:  []string{"tag:serve-ernie@example.com"},
			}, nil
		}, self)
		err := a.Authorize(peerCtx("100.64.0.6"))
		require.Error(t, err)
		assert.Equal(t, codes.PermissionDenied, status.Code(err))
	})

	t.Run("rejects an unidentifiable caller", func(t *testing.T) {
		a := NewPeerAuthorizer(func(context.Context, string) (Caller, error) {
			return Caller{}, errors.New("peer not found")
		}, self)
		err := a.Authorize(peerCtx("100.64.0.4"))
		require.Error(t, err)
		assert.Equal(t, codes.Unauthenticated, status.Code(err))
	})

	t.Run("rejects a request with no peer address", func(t *testing.T) {
		a := NewPeerAuthorizer(func(context.Context, string) (Caller, error) {
			return Caller{Login: "ernie@example.com"}, nil
		}, self)
		err := a.Authorize(context.Background())
		require.Error(t, err)
		assert.Equal(t, codes.Unauthenticated, status.Code(err))
	})

	t.Run("rejects while the local identity is unknown", func(t *testing.T) {
		a := NewPeerAuthorizer(func(context.Context, string) (Caller, error) {
			return Caller{Login: "ernie@example.com"}, nil
		}, func(context.Context) (string, error) {
			return "", errors.New("not logged in yet")
		})
		err := a.Authorize(peerCtx("100.64.0.5"))
		require.Error(t, err)
		assert.Equal(t, codes.Unavailable, status.Code(err))
	})
}

// gateAuthorizer admits every peer while open.
type gateAuthorizer struct{ open atomic.Bool }

func (g *gateAuthorizer) Authorize(ctx context.Context) error {
	if p, ok := peer.FromContext(ctx); !ok || p.Addr == nil {
		return status.Error(codes.Unauthenticated, "no peer address")
	}
	if !g.open.Load() {
		return status.Error(codes.PermissionDenied, "closed")
	}
	return nil
}

func TestServeTerminals(t *testing.T) {
	t.Parallel()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	attachLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	scheme := workspacetest.NewPtyScheme("/")
	auth := &gateAuthorizer{}
	auth.open.Store(true)
	server := Serve(lis, attachLis, auth, scheme, "/data")
	t.Cleanup(func() { _ = server.Close() })

	conn, err := grpc.NewClient("passthrough:///"+lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", addr)
		}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	ctx := context.Background()
	lifecycle := tworkspacerpc.NewVTEClient(conn, "/home/a")

	infos, err := lifecycle.ListVTEs(ctx)
	require.NoError(t, err)
	assert.Empty(t, infos)

	// An attachment creates a terminal in the workspace its HELLO
	// names, which the lifecycle service lists under that root.
	rw, err := net.Dial("tcp", attachLis.Addr().String())
	require.NoError(t, err)
	wire := vtewire.NewConn(rw)
	t.Cleanup(func() { _ = wire.Close() })
	ok, err := vtewire.Dial(wire, &vtewire.Hello{Root: "/home/a", Width: 20, Height: 4})
	require.NoError(t, err)
	select {
	case <-scheme.Chrooted("/home/a").Started():
	case <-time.After(5 * time.Second):
		t.Fatal("no process started in the workspace")
	}
	infos, err = lifecycle.ListVTEs(ctx)
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, vte.ID(ok.VTEID), infos[0].ID)
	assert.Equal(t, 1, infos[0].Attachments)
	other := tworkspacerpc.NewVTEClient(conn, "/home/b")
	infos, err = other.ListVTEs(ctx)
	require.NoError(t, err)
	assert.Empty(t, infos, "another workspace's terminals are its own")

	// A peer the authorizer turns away gets no attachment: its
	// connection is closed before any HELLO is answered.
	auth.open.Store(false)
	rw, err = net.Dial("tcp", attachLis.Addr().String())
	require.NoError(t, err)
	refused := vtewire.NewConn(rw)
	t.Cleanup(func() { _ = refused.Close() })
	_, err = vtewire.Dial(refused, &vtewire.Hello{Root: "/home/a"})
	require.Error(t, err)
	_, err = lifecycle.ListVTEs(ctx)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	auth.open.Store(true)

	// The terminals end with the server, which its members are told.
	require.NoError(t, server.Close())
	frame, err := readUntilEnd(wire)
	if err == nil {
		dropped, isDropped := frame.(*vtewire.Dropped)
		require.True(t, isDropped, "expected DROPPED, got %T", frame)
		assert.Equal(t, vtewire.CodeNotFound, dropped.Code)
	}
}

// readUntilEnd reads frames until the attachment ends: an EXIT or
// DROPPED frame, or the connection closing.
func readUntilEnd(conn *vtewire.Conn) (vtewire.Frame, error) {
	for {
		frame, err := conn.ReadFrame()
		if err != nil {
			return nil, err
		}
		switch frame.(type) {
		case *vtewire.Exit, *vtewire.Dropped:
			return frame, nil
		}
	}
}
