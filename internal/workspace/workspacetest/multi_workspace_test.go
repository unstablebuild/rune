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
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/workspace"
)

func TestMultiWorkspace(t *testing.T) {
	ctx := context.Background()

	tsuite := []struct {
		desc               string
		defURI             workspaceapi.URI
		fileURI            workspaceapi.URI
		recover            bool
		wantError          bool
		wantOtherWorkspace bool
		expectAddWorkspace workspaceapi.URI
	}{
		{"should load files in the default workspace",
			parseURI(t, "memory:///"), parseURI(t, "memory:///file.txt"), false, false, false, workspaceapi.URI{}},
		{"should recover files in the default workspace",
			parseURI(t, "memory:///"), parseURI(t, "memory:///file.txt"), true, false, false, workspaceapi.URI{}},
		{"should load files in a registered non-default workspace and should call AddWorkspace with dir URI",
			parseURI(t, "memory:///"), parseURI(t, "test:///file.txt"), false, false, false, parseURI(t, "test:///")},
		{"should recover files in a registered non-default workspace and should call AddWorkspace with dir URI",
			parseURI(t, "memory:///"), parseURI(t, "test:///file.txt"), true, false, false, parseURI(t, "test:///")},
		{"should return other workspace error when file belongs to already open workspace",
			parseURI(t, "memory:///"), parseURI(t, "test:///file.txt"), false, true, true, workspaceapi.URI{}},
		{"should not load files in a non-registered non-default workspace",
			parseURI(t, "memory:///"), parseURI(t, "nagging:///file.txt"), false, true, false, workspaceapi.URI{}},
		{"should not recover files in a non-registered non-default workspace",
			parseURI(t, "memory:///"), parseURI(t, "nagging:///file.txt"), true, true, false, workspaceapi.URI{}},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			memScheme, err := workspace.NewMemoryScheme(ctx, config.NopConfig(), tcase.defURI)
			require.NoError(t, err)
			cwd := workspace.NewSchemeWorkspace(tcase.defURI, memScheme, inlineSchedule)
			mockManager := &mockManager{}
			if tcase.wantOtherWorkspace {
				uri := parseURI(t, "test:///")
				scheme, err := NewNopScheme(uri.Scheme())(ctx, config.NopConfig(), uri)
				require.NoError(t, err)
				mockManager.workspace = workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule)
			}
			cwd = workspace.Multi(ctx, mockManager, cwd, tcase.defURI)

			if tcase.recover {
				swapFile := fmt.Sprintf("%s.rswp", tcase.fileURI.Path())
				_, werr := memScheme.OpenFile(swapFile, os.O_CREATE, 0)
				require.Nil(t, werr)
				swapFileURI := parseURI(t, fmt.Sprintf("%s.rswp", tcase.fileURI.String()))
				_, err = cwd.Recover(tcase.fileURI, swapFileURI, cell.NewBuffer(), false)
			} else {
				_, err = cwd.Load(tcase.fileURI, cell.NewBuffer(), workspaceapi.Dir(tcase.fileURI), false)
			}
			if tcase.wantOtherWorkspace {
				assert.ErrorIs(t, err, workspace.ErrOpenInOtherWorkspace)
			} else if tcase.wantError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tcase.expectAddWorkspace != (workspaceapi.URI{}) {
				require.Len(t, mockManager.addWorkspace, 1)
				assert.Equal(t, mockManager.addWorkspace[0], tcase.expectAddWorkspace)
			}
		})
	}
}

type mockManager struct {
	addWorkspace []workspaceapi.URI
	workspace    workspace.Workspace
}

func (m *mockManager) RegisterScheme(string, schemeapi.SchemeFunc) error {
	panic("should not be called")
}
func (m *mockManager) UnregisterScheme(string) error {
	panic("should not be called")
}
func (m *mockManager) AddWorkspace(ctx context.Context, uri workspaceapi.URI) (
	workspace.Workspace, error,
) {
	if uri.Scheme() != "test" {
		return nil, errors.New("not registered")
	}
	m.addWorkspace = append(m.addWorkspace, uri)
	scheme, err := NewNopScheme(uri.Scheme())(ctx, config.NopConfig(), uri)
	if err != nil {
		return nil, err
	}
	return workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule), nil
}

func (m *mockManager) Workspace(file workspaceapi.URI) (workspace.Workspace, bool, error) {
	if m.workspace == nil {
		return nil, false, nil
	}
	is, err := workspace.CanWorkspaceURI(m.workspace, file)
	if err != nil {
		return nil, false, err
	}
	if !is {
		return nil, false, nil
	}
	return m.workspace, true, nil
}

func (m *mockManager) IncrementReference(workspaceapi.URI) {}

func (m *mockManager) DecrementReference(workspaceapi.URI) error { return nil }

func (m *mockManager) RemoveWorkspace(workspaceapi.URI) (workspace.Workspace, bool) {
	panic("should not be called")
}

func TestMultiForwardsRemoteScheme(t *testing.T) {
	ctx := context.Background()
	uri := parseURI(t, "memory:///")

	memScheme, err := workspace.NewMemoryScheme(ctx, config.NopConfig(), uri)
	require.NoError(t, err)
	disconnectCh := make(chan struct{})
	cwd := remoteWorkspace{
		Workspace:    workspace.NewSchemeWorkspace(uri, memScheme, inlineSchedule),
		disconnectCh: disconnectCh,
	}
	m := workspace.Multi(ctx, &mockManager{}, cwd, uri)

	rs, ok := m.(workspace.RemoteScheme)
	require.True(t, ok,
		"workspace.Multi must expose OnDisconnect when the "+
			"underlying workspace implements RemoteScheme; "+
			"otherwise vtereservoir cannot detect SSH transport "+
			"drops and dead terminals persist after reconnect")
	require.Equal(t, (<-chan struct{})(disconnectCh), rs.OnDisconnect(),
		"Multi.OnDisconnect must return the underlying "+
			"workspace's disconnect channel")
}

func TestMultiExtraneousReleasesWorkspaceOnFileClose(t *testing.T) {
	ctx := context.Background()

	defURI := parseURI(t, "memory:///")
	extraneousDir := parseURI(t, "test:///tmp/")
	extraneousFile := parseURI(t, "test:///tmp/file.txt")

	t.Run("single file release", func(t *testing.T) {
		m := newRefcountManager(t)
		def := defaultWorkspaceFor(t, ctx, m, defURI)
		multi := workspace.Multi(ctx, hideExtraneous(m), def, defURI)

		require.False(t, m.HasWorkspace(extraneousDir))

		fc, err := multi.Load(extraneousFile, cell.NewBuffer(), extraneousDir, false)
		require.NoError(t, err)
		require.True(t, m.HasWorkspace(extraneousDir),
			"loadExtraneous must install the ad-hoc workspace")

		require.NoError(t, fc.Close())
		assert.False(t, m.HasWorkspace(extraneousDir),
			"closing the per-file FlusherCloser must release the "+
				"ad-hoc workspace; otherwise long sessions leak "+
				"one workspace per out-of-workspace open")
	})

	t.Run("refcount keeps workspace alive until last file closes", func(t *testing.T) {
		m := newRefcountManager(t)
		def := defaultWorkspaceFor(t, ctx, m, defURI)
		multi := workspace.Multi(ctx, hideExtraneous(m), def, defURI)

		file1 := parseURI(t, "test:///tmp/a.txt")
		file2 := parseURI(t, "test:///tmp/b.txt")

		fc1, err := multi.Load(file1, cell.NewBuffer(), extraneousDir, false)
		require.NoError(t, err)
		fc2, err := multi.Load(file2, cell.NewBuffer(), extraneousDir, false)
		require.NoError(t, err)

		require.True(t, m.HasWorkspace(extraneousDir))

		require.NoError(t, fc1.Close())
		assert.True(t, m.HasWorkspace(extraneousDir),
			"workspace must survive close of first file while a "+
				"second file in the same dir is still open")

		require.NoError(t, fc2.Close())
		assert.False(t, m.HasWorkspace(extraneousDir),
			"workspace must be released after the last file closes")
	})

	t.Run("double close does not under-release", func(t *testing.T) {
		m := newRefcountManager(t)
		def := defaultWorkspaceFor(t, ctx, m, defURI)
		multi := workspace.Multi(ctx, hideExtraneous(m), def, defURI)

		file1 := parseURI(t, "test:///tmp/a.txt")
		file2 := parseURI(t, "test:///tmp/b.txt")

		fc1, err := multi.Load(file1, cell.NewBuffer(), extraneousDir, false)
		require.NoError(t, err)
		holdFC, err := multi.Load(file2, cell.NewBuffer(), extraneousDir, false)
		require.NoError(t, err)

		require.NoError(t, fc1.Close())
		_ = fc1.Close()
		assert.True(t, m.HasWorkspace(extraneousDir),
			"double Close on a single per-file FlusherCloser must "+
				"only decrement the refcount once; the second "+
				"open in the same dir must still keep the "+
				"workspace alive")

		require.NoError(t, holdFC.Close())
		assert.False(t, m.HasWorkspace(extraneousDir))

		fc2, err := multi.Load(file2, cell.NewBuffer(), extraneousDir, false)
		require.NoError(t, err)
		require.True(t, m.HasWorkspace(extraneousDir),
			"a fresh extraneous open after a previous release "+
				"must re-create the workspace")
		require.NoError(t, fc2.Close())
		assert.False(t, m.HasWorkspace(extraneousDir))
	})

	t.Run("default workspace is never released by refcount", func(t *testing.T) {
		m := newRefcountManager(t)
		def := defaultWorkspaceFor(t, ctx, m, defURI)
		multi := workspace.Multi(ctx, hideExtraneous(m), def, defURI)

		require.True(t, m.HasWorkspace(defURI))

		fc, err := multi.Load(extraneousFile, cell.NewBuffer(), extraneousDir, false)
		require.NoError(t, err)
		require.NoError(t, fc.Close())

		assert.True(t, m.HasWorkspace(defURI),
			"the IDE-owned default workspace must not be subject "+
				"to the refcount; only workspaces explicitly "+
				"incremented should be released on decrement")
	})
}

// remoteWorkspace is a workspace.Workspace that also satisfies
// workspace.RemoteScheme by surfacing a caller-controlled
// disconnect channel. Used by TestMultiForwardsRemoteScheme.
type remoteWorkspace struct {
	workspace.Workspace
	disconnectCh chan struct{}
}

func (r remoteWorkspace) OnDisconnect() <-chan struct{} {
	return r.disconnectCh
}

func (r remoteWorkspace) WaitConnected(context.Context) error {
	return nil
}

func newRefcountManager(t *testing.T) *workspace.Manager {
	t.Helper()
	m := workspace.NewManager(config.NopConfig(), inlineSchedule)
	require.NoError(t, m.RegisterScheme("memory", workspace.NewMemoryScheme))
	require.NoError(t, m.RegisterScheme("test", NewNopScheme("test")))
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func defaultWorkspaceFor(
	t *testing.T, ctx context.Context,
	m *workspace.Manager, uri workspaceapi.URI,
) workspace.Workspace {
	t.Helper()
	w, err := m.AddWorkspace(ctx, uri)
	require.NoError(t, err)
	return w
}

// hideExtraneous wraps the real Manager but always reports "no
// workspace yet" from Workspace(file). This mirrors the production
// visibleWorkspaceManager (ide/workspace_handler.go), which only
// surfaces workspaces that own a visible tab slot; workspaces
// created on demand never appear there, so multi.loadExtraneous
// can find the existing manager entry via AddWorkspace without
// bailing out with ErrOpenInOtherWorkspace.
func hideExtraneous(m *workspace.Manager) workspace.WorkspaceManager {
	return hidingManager{m: m}
}

type hidingManager struct {
	m *workspace.Manager
}

func (h hidingManager) RegisterScheme(s string, fn schemeapi.SchemeFunc) error {
	return h.m.RegisterScheme(s, fn)
}

func (h hidingManager) UnregisterScheme(s string) error {
	return h.m.UnregisterScheme(s)
}

func (h hidingManager) AddWorkspace(ctx context.Context, uri workspaceapi.URI) (
	workspace.Workspace, error,
) {
	return h.m.AddWorkspace(ctx, uri)
}

func (h hidingManager) Workspace(workspaceapi.URI) (workspace.Workspace, bool, error) {
	return nil, false, nil
}

func (h hidingManager) IncrementReference(uri workspaceapi.URI) {
	h.m.IncrementReference(uri)
}

func (h hidingManager) DecrementReference(uri workspaceapi.URI) error {
	return h.m.DecrementReference(uri)
}

func (h hidingManager) RemoveWorkspace(uri workspaceapi.URI) (workspace.Workspace, bool) {
	return h.m.RemoveWorkspace(uri)
}
