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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/workspace"
)

func parseURI(t *testing.T, uriStr string) workspaceapi.URI {
	u, err := workspaceapi.ParseURI(uriStr)
	require.NoError(t, err)
	return u
}

func TestManager(t *testing.T) {
	ctx := context.Background()

	t.Run("returns ErrSchemeAlreadyRegistered when same scheme is registered twice", func(t *testing.T) {
		m := workspace.NewManager(config.NopConfig(), inlineSchedule)
		err := m.RegisterScheme("test", NewNopScheme("test"))
		require.NoError(t, err)

		require.Equal(t, schemeapi.ErrSchemeAlreadyRegistered,
			m.RegisterScheme("test", NewNopScheme("test")))
	})

	t.Run("registers scheme to be used by AcquireWorkspace", func(*testing.T) {
		m := workspace.NewManager(config.NopConfig(), inlineSchedule)
		err := m.RegisterScheme("test", NewNopScheme("test"))
		require.NoError(t, err)

		w, ok, err := m.Workspace(parseURI(t, "test:///tmp/file.txt"))
		require.NoError(t, err)
		assert.Nil(t, w)
		require.False(t, ok)

		w, err = m.AcquireWorkspace(ctx, parseURI(t, "test:///tmp/"))
		assert.NotNil(t, w)
		require.NoError(t, err)

		w1, ok, err := m.Workspace(parseURI(t, "test:///tmp/file.txt"))
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, w, w1)

		require.NoError(t, m.Close())
	})

	t.Run("removes Workspace upon call to workspace.Close", func(*testing.T) {
		m := workspace.NewManager(config.NopConfig(), inlineSchedule)
		err := m.RegisterScheme("test", NewNopScheme("test"))
		require.NoError(t, err)

		w, err := m.AcquireWorkspace(ctx, parseURI(t, "test:///tmp/"))
		assert.NotNil(t, w)
		require.NoError(t, err)

		w1, ok, err := m.Workspace(parseURI(t, "test:///tmp/file.txt"))
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, w, w1)

		require.NoError(t, w1.Close())

		w1, ok, err = m.Workspace(parseURI(t, "test:///tmp/file.txt"))
		require.NoError(t, err)
		require.False(t, ok)
		assert.Nil(t, w1)

		require.NoError(t, m.Close())
	})

	t.Run("AcquireWorkspace creates a new Workspace if URI is different", func(*testing.T) {
		m := workspace.NewManager(config.NopConfig(), inlineSchedule)
		err := m.RegisterScheme("test", NewNopScheme("test"))
		require.NoError(t, err)

		w0, err := m.AcquireWorkspace(ctx, parseURI(t, "test:///tmp/"))
		assert.NotNil(t, w0)
		require.NoError(t, err)

		w1, err := m.AcquireWorkspace(ctx, parseURI(t, "test:///tmp/blah"))
		assert.NotNil(t, w1)
		require.NoError(t, err)

		assert.NotEqual(t, w0, w1)

		w2, err := m.AcquireWorkspace(ctx, parseURI(t, "test:///tmp/blah/hello"))
		assert.NotNil(t, w2)
		require.NoError(t, err)

		assert.NotEqual(t, w1, w2)

		// same as w2
		w3, err := m.AcquireWorkspace(ctx, parseURI(t, "test:///tmp/blah/hello"))
		assert.NotNil(t, w2)
		require.NoError(t, err)

		assert.Equal(t, w2, w3)

		require.NoError(t, m.Close())
	})

	t.Run("Workspace returns ANY workspace capable "+
		"of handling a uri, as defined by IsWorkspaceURI", func(*testing.T) {
		m := workspace.NewManager(config.NopConfig(), inlineSchedule)
		err := m.RegisterScheme("test", NewNopScheme("test"))
		require.NoError(t, err)

		w0, err := m.AcquireWorkspace(ctx, parseURI(t, "test:///var/"))
		assert.NotNil(t, w0)
		require.NoError(t, err)

		w1, err := m.AcquireWorkspace(ctx, parseURI(t, "test:///tmp/blah"))
		assert.NotNil(t, w1)
		require.NoError(t, err)

		assert.NotEqual(t, w0, w1)

		w2, ok, err := m.Workspace(parseURI(t, "test:///tmp/blah/hello"))
		require.NoError(t, err)
		assert.True(t, ok)
		assert.True(t, w2 == w0 || w2 == w1)

		require.NoError(t, m.Close())
	})

	t.Run("register same scheme twice returns error", func(t *testing.T) {
		m := workspace.NewManager(config.NopConfig(), inlineSchedule)
		err := m.RegisterScheme("test", NewNopScheme("test"))
		require.NoError(t, err)
		err = m.RegisterScheme("test", NewNopScheme("test"))
		require.Error(t, err)
	})

	t.Run("ReleaseWorkspace removes the entry and returns the raw workspace", func(t *testing.T) {
		m := workspace.NewManager(config.NopConfig(), inlineSchedule)
		err := m.RegisterScheme("test", NewNopScheme("test"))
		require.NoError(t, err)

		uri := parseURI(t, "test:///tmp/")
		w, err := m.AcquireWorkspace(ctx, uri)
		require.NoError(t, err)
		require.NotNil(t, w)

		detached, ok := m.ReleaseWorkspace(uri)
		require.True(t, ok)
		require.NotNil(t, detached)
		assert.False(t, m.HasWorkspace(uri),
			"detach must unregister the uri from the manager")

		_, ok = m.ReleaseWorkspace(uri)
		assert.False(t, ok, "second detach must return false")

		// A new workspace registered under the same uri must survive
		// closing the detached one: the detached value must not be
		// wrapped in the manager-removing decorator.
		w2, err := m.AcquireWorkspace(ctx, uri)
		require.NoError(t, err)
		require.NotNil(t, w2)

		require.NoError(t, detached.Close())
		assert.True(t, m.HasWorkspace(uri),
			"closing a detached workspace must not touch the manager maps")

		require.NoError(t, m.Close())
	})

	t.Run("ReleaseWorkspace keeps a shared entry open until the last reference", func(t *testing.T) {
		m := workspace.NewManager(config.NopConfig(), inlineSchedule)
		closes := new(int)
		require.NoError(t, m.RegisterScheme("test", newCloseCountingScheme(closes)))

		uri := parseURI(t, "test:///tmp/")
		_, err := m.AcquireWorkspace(ctx, uri)
		require.NoError(t, err)
		_, err = m.AcquireWorkspace(ctx, uri)
		require.NoError(t, err)

		detached, ok := m.ReleaseWorkspace(uri)
		assert.False(t, ok, "release with another holder must not detach")
		assert.Nil(t, detached)
		assert.True(t, m.HasWorkspace(uri))
		assert.Zero(t, *closes, "release with another holder must not close")

		detached, ok = m.ReleaseWorkspace(uri)
		require.True(t, ok, "the last release must detach")
		require.NotNil(t, detached)
		assert.False(t, m.HasWorkspace(uri))
		assert.Zero(t, *closes, "the caller owns closing the detached workspace")

		require.NoError(t, detached.Close())
		assert.Equal(t, 1, *closes)
	})

	t.Run("stale wrapped Close does not evict a successor instance", func(t *testing.T) {
		m := workspace.NewManager(config.NopConfig(), inlineSchedule)
		err := m.RegisterScheme("test", NewNopScheme("test"))
		require.NoError(t, err)

		uri := parseURI(t, "test:///tmp/")
		stale, err := m.AcquireWorkspace(ctx, uri)
		require.NoError(t, err)

		_, ok := m.ReleaseWorkspace(uri)
		require.True(t, ok)

		successor, err := m.AcquireWorkspace(ctx, uri)
		require.NoError(t, err)

		// The wrapped handle from before the remove is now stale:
		// closing it must not evict the successor entry.
		require.NoError(t, stale.Close())
		assert.True(t, m.HasWorkspace(uri),
			"a stale wrapped handle's Close must not evict the successor")
		w, err := m.AcquireWorkspace(ctx, uri)
		require.NoError(t, err)
		assert.True(t, successor == w,
			"the successor must remain the registered entry")

		require.NoError(t, m.Close())
	})

	t.Run("buubles up scheme constructor errors", func(t *testing.T) {
		m := workspace.NewManager(config.NopConfig(), inlineSchedule)
		err := m.RegisterScheme("test",
			func(ctx context.Context, cfg config.Config, uri workspaceapi.URI) (schemeapi.Scheme, error) {
				return nil, errors.New("boom")
			})
		require.NoError(t, err)

		w, err := m.AcquireWorkspace(ctx, parseURI(t, "test:///tmp/"))
		assert.Nil(t, w)
		require.Error(t, err)
		assert.False(t, m.HasWorkspace(parseURI(t, "test:///tmp/")))

		require.NoError(t, m.Close())
	})

	t.Run("passes scheme config to scheme constructor", func(*testing.T) {
		m := workspace.NewManager(config.MapConfig(map[string]any{
			"test": map[string]any{
				"key": "value",
			},
			"file": map[string]any{
				"kk": "vv",
			},
		}), inlineSchedule)

		var called bool
		err := m.RegisterScheme("test", func(ctx context.Context, cfg config.Config, uri workspaceapi.URI) (schemeapi.Scheme, error) {

			value, err := cfg.GetString("key")
			assert.NoError(t, err)
			assert.Equal(t, "value", value)

			value, err = cfg.GetString("kk")
			assert.Equal(t, config.ErrNotFound, err)
			assert.Zero(t, value)

			called = true
			return &NopScheme{}, nil
		})
		require.NoError(t, err)

		w, err := m.AcquireWorkspace(ctx, parseURI(t, "test:///tmp/"))
		require.NoError(t, err)
		assert.NotNil(t, w)
		assert.True(t, called)

		require.NoError(t, m.Close())
	})

	// Manager wraps every workspace returned by AcquireWorkspace in
	// a managerWorkspace, which embeds workspace.Workspace via
	// an interface field — so OnDisconnect is NOT promoted into
	// the wrapper's method set even when the underlying scheme
	// implements workspace.RemoteScheme. Without an explicit
	// forwarder, vtereservoir.New's terminal.(RemoteScheme)
	// type-assertion would always fail at runtime for SSH
	// workspaces because the Manager's wrapper hides the
	// optional interface.
	t.Run("AcquireWorkspace result forwards RemoteScheme.OnDisconnect", func(t *testing.T) {
		m := workspace.NewManagerWithWorkspaceFunc(config.NopConfig(),
			inlineSchedule,
			func(uri workspaceapi.URI, _ schemeapi.Scheme, _ func(func()) bool) workspace.Workspace {
				return remoteWorkspace{
					Workspace:    workspace.NewSchemeWorkspace(uri, &NopScheme{}, inlineSchedule),
					disconnectCh: make(chan struct{}),
				}
			})
		require.NoError(t, m.RegisterScheme("test", NewNopScheme("test")))

		w, err := m.AcquireWorkspace(ctx, parseURI(t, "test:///tmp/"))
		require.NoError(t, err)

		rs, ok := w.(workspace.RemoteScheme)
		require.True(t, ok,
			"managerWorkspace must expose OnDisconnect when the "+
				"wrapped workspace implements RemoteScheme; otherwise "+
				"vtereservoir cannot detect SSH transport drops and "+
				"dead terminals persist after reconnect")
		require.NotNil(t, rs.OnDisconnect())

		require.NoError(t, m.Close())
	})

	// InstallRoot is part of the Workspace interface, so unlike
	// OnDisconnect it is promoted through Manager's wrapper; this
	// pins that AcquireWorkspace surfaces the wrapped workspace's answer
	// rather than a wrapper default.
	t.Run("AcquireWorkspace result surfaces the workspace InstallRoot", func(t *testing.T) {
		m := workspace.NewManagerWithWorkspaceFunc(config.NopConfig(),
			inlineSchedule,
			func(uri workspaceapi.URI, _ schemeapi.Scheme, _ func(func()) bool) workspace.Workspace {
				return installRootWorkspace{
					Workspace: workspace.NewSchemeWorkspace(uri, &NopScheme{}, inlineSchedule),
					root:      "/home/peer/.rune",
				}
			})
		require.NoError(t, m.RegisterScheme("test", NewNopScheme("test")))

		w, err := m.AcquireWorkspace(ctx, parseURI(t, "test:///tmp/"))
		require.NoError(t, err)

		root, err := w.InstallDataDir(ctx)
		require.NoError(t, err)
		assert.Equal(t, "/home/peer/.rune", root)

		require.NoError(t, m.Close())
	})

	t.Run("InstallRoot reports ErrUnsupported for a non-provider scheme", func(t *testing.T) {
		m := workspace.NewManager(config.NopConfig(), inlineSchedule)
		require.NoError(t, m.RegisterScheme("test", NewNopScheme("test")))

		w, err := m.AcquireWorkspace(ctx, parseURI(t, "test:///tmp/"))
		require.NoError(t, err)

		root, err := w.InstallDataDir(ctx)
		require.ErrorIs(t, err, errors.ErrUnsupported,
			"a host that cannot report its install root must degrade "+
				"to the caller's fallback, not fail the workspace")
		assert.Empty(t, root)

		require.NoError(t, m.Close())
	})
}

// installRootWorkspace is a workspace.Workspace answering InstallRoot
// with a fixed root.
type installRootWorkspace struct {
	workspace.Workspace
	root string
}

func (w installRootWorkspace) InstallDataDir(context.Context) (string, error) {
	return w.root, nil
}

// newCloseCountingScheme returns nop schemes that count every Close
// into closes.
func newCloseCountingScheme(closes *int) schemeapi.SchemeFunc {
	return func(ctx context.Context, cfg config.Config, uri workspaceapi.URI) (
		schemeapi.Scheme, error,
	) {
		s, err := NewNopScheme("test")(ctx, cfg, uri)
		if err != nil {
			return nil, err
		}
		return closeCountingScheme{Scheme: s, closes: closes}, nil
	}
}

type closeCountingScheme struct {
	schemeapi.Scheme
	closes *int
}

func (s closeCountingScheme) Close() error {
	*s.closes++
	return s.Scheme.Close()
}

func TestIntegrationManagerWithWorkspaceLoad(t *testing.T) {
	finnWorkspaceURI, err := workspaceapi.ParseURI("finn:///tmp/hello")
	require.NoError(t, err)

	jakeFileURI, err := workspaceapi.ParseURI("jake:///tmp/hello")
	require.NoError(t, err)

	jakeSwapDirURI, err := workspaceapi.ParseURI("jake:///tmp/hello/.hallo.txt.rswp")
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("default workspace is NOT able to load files from other schemes", func(t *testing.T) {
		manager := workspace.NewManager(config.NopConfig(), inlineSchedule)
		require.NoError(t, manager.RegisterScheme("finn", NewNopScheme("finn")))
		require.NoError(t, manager.RegisterScheme("jake", NewNopScheme("jake")))

		finnWorkspace, err := manager.AcquireWorkspace(ctx, finnWorkspaceURI)
		require.NoError(t, err)

		_, err = finnWorkspace.Load(jakeFileURI, cell.NewBuffer(), jakeSwapDirURI, false)
		require.Error(t, err)

		_, err = finnWorkspace.Recover(jakeFileURI, jakeSwapDirURI, cell.NewBuffer(), false)
		require.Error(t, err)
	})

	t.Run("default workspace wrapped with multi is able to load files from other schemes", func(t *testing.T) {
		manager := workspace.NewManager(config.NopConfig(), inlineSchedule)
		require.NoError(t, manager.RegisterScheme("finn", workspace.LoggingScheme("finn", NewNopScheme("finn"))))
		require.NoError(t, manager.RegisterScheme("jake", workspace.LoggingScheme("jake", NewNopScheme("jake"))))

		finnWorkspace, err := manager.AcquireWorkspace(ctx, finnWorkspaceURI)
		require.NoError(t, err)

		finnWorkspace = workspace.Multi(ctx, manager, finnWorkspace, finnWorkspaceURI)

		ret, err := finnWorkspace.Load(jakeFileURI, cell.NewBuffer(), jakeSwapDirURI, false)
		require.NoError(t, err)
		require.NoError(t, ret.Close())

		ret, err = finnWorkspace.Recover(jakeFileURI, jakeSwapDirURI, cell.NewBuffer(), false)
		require.NoError(t, err)
		require.NoError(t, ret.Close())
	})
}
