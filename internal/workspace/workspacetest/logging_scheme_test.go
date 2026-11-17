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

package workspacetest

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"google.golang.org/grpc"
	"unstable.build/rune/internal/workspace"
)

func TestLoggingScheme(t *testing.T) {
	TestWorkspaceSchemeFiles(t, func(t *testing.T) schemeapi.Scheme {
		uri, err := workspaceapi.ParseURI("memory:///")
		require.NoError(t, err)
		log := workspace.LoggingScheme("memory", workspace.NewMemoryScheme)
		scheme, err := log(context.Background(), config.NopConfig(), uri)
		require.NoError(t, err)
		return scheme
	})
}

func TestLoggingSchemeKeepsHostCapabilities(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	disconnect := make(chan struct{})
	for _, tc := range []struct {
		name  string
		inner func(schemeapi.Scheme) schemeapi.Scheme
		check func(*testing.T, workspace.Workspace)
	}{{
		name: "a capable scheme keeps its capabilities",
		inner: func(s schemeapi.Scheme) schemeapi.Scheme {
			return &capableScheme{Scheme: s, disconnect: disconnect}
		},
		check: func(t *testing.T, w workspace.Workspace) {
			ctx := context.Background()
			pgrp, err := w.ForegroundProcessGroup(workspaceapi.Pty{})
			require.NoError(t, err)
			require.Equal(t, 42, pgrp)
			rs, ok := w.(workspace.RemoteScheme)
			require.True(t, ok)
			require.Equal(t, (<-chan struct{})(disconnect), rs.OnDisconnect())
			require.ErrorIs(t, rs.WaitConnected(ctx), errCapableConnect)
			dir, err := w.InstallDataDir(ctx)
			require.NoError(t, err)
			require.Equal(t, "/capable", dir)
			cc, ok := w.(workspace.PackageHost).HostConn()
			require.True(t, ok)
			require.Same(t, capableHostConn, cc)
			peer, ok := w.Peer()
			require.True(t, ok)
			require.Same(t, capablePeer, peer)
		},
	}, {
		name:  "a plain scheme still lacks them",
		inner: func(s schemeapi.Scheme) schemeapi.Scheme { return s },
		check: func(t *testing.T, w workspace.Workspace) {
			ctx := context.Background()
			_, err := w.ForegroundProcessGroup(workspaceapi.Pty{})
			require.ErrorIs(t, err, errors.ErrUnsupported)
			rs, ok := w.(workspace.RemoteScheme)
			require.True(t, ok)
			require.Nil(t, rs.OnDisconnect())
			require.NoError(t, rs.WaitConnected(ctx))
			_, err = w.InstallDataDir(ctx)
			require.ErrorIs(t, err, errors.ErrUnsupported)
			_, ok = w.(workspace.PackageHost).HostConn()
			require.False(t, ok)
			_, ok = w.Peer()
			require.False(t, ok)
		},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			traced := workspace.LoggingScheme("memory", func(
				ctx context.Context, cfg config.Config, uri workspaceapi.URI,
			) (schemeapi.Scheme, error) {
				s, err := workspace.NewMemoryScheme(ctx, cfg, uri)
				if err != nil {
					return nil, err
				}
				return tc.inner(s), nil
			})
			scheme, err := traced(context.Background(), config.NopConfig(), uri)
			require.NoError(t, err)
			t.Cleanup(func() { _ = scheme.Close() })
			tc.check(t, workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule))
		})
	}
}

var (
	errCapableConnect = errors.New("connected by the wrapped scheme")
	capableHostConn   = new(capableConn)
	capablePeer       = new(capableHost)
)

// capableConn is the connection to the host of a capableScheme.
type capableConn struct{ grpc.ClientConnInterface }

// capableHost is the Rune serving a capableScheme.
type capableHost struct{ workspace.Peer }

// capableScheme has every optional host capability, each answering in
// a way only it could.
type capableScheme struct {
	schemeapi.Scheme
	disconnect chan struct{}
}

func (s *capableScheme) OnDisconnect() <-chan struct{} { return s.disconnect }

func (*capableScheme) WaitConnected(context.Context) error { return errCapableConnect }

func (*capableScheme) InstallDataDir(context.Context) (string, error) {
	return "/capable", nil
}

func (*capableScheme) HostConn() (grpc.ClientConnInterface, bool) {
	return capableHostConn, true
}

func (*capableScheme) Peer() (workspace.Peer, bool) {
	return capablePeer, true
}

func (*capableScheme) ForegroundProcessGroup(workspaceapi.Pty) (int, error) {
	return 42, nil
}
