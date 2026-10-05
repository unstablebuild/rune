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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
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
