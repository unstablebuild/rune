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

package ide

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

func mustFileURI(t *testing.T, path string) workspaceapi.URI {
	t.Helper()
	u, err := workspaceapi.ParseURI("file://" + path)
	require.NoError(t, err)
	return u
}

func TestWorkspaceBasename(t *testing.T) {
	cases := []struct {
		uri  string
		want string
	}{
		{"file:///Users/alice/projects/blue", "blue"},
		{"file:///Users/alice/work/blue/", "blue"},
		{"file:///tmp/has space/foo bar", "foo_bar"},
		{"file:///tmp/weird:name", "weird_name"},
		// Non-file schemes resolve too: commands dispatched through
		// an alias run via the workspace's executor in its native
		// filesystem, so the URI path is a meaningful identifier.
		{"ssh://user@host/srv/blue", "blue"},
		{"memory:///tmp/playground/blue", "blue"},
	}
	for _, tc := range cases {
		uri, err := workspaceapi.ParseURI(tc.uri)
		require.NoError(t, err, "uri %q", tc.uri)
		got := workspaceBasename(uri)
		assert.Equal(t, tc.want, got, "uri %q", tc.uri)
	}
}

func TestWorkspaceManagerHandlerEnvSource(t *testing.T) {
	t.Run("memory test workspace exposes every workspace var", func(t *testing.T) {
		// The IDE test harness installs workspaces with the
		// memory:// scheme. Every $WORKSPACE_* variable resolves
		// because commands dispatched through these aliases would
		// run inside the workspace's own executor.
		dir := t.TempDir()
		m := newTestWorkspaceManagerHandlerWithDir(t,
			defaultConfigWithWrap(false), dir, nopShutdownShaderConfig())
		t.Cleanup(func() { _ = m.Close() })
		m.quiesce()

		base, ok := m.envSource("WORKSPACE")
		assert.True(t, ok)
		assert.NotEmpty(t, base)
		uriStr, ok := m.envSource("WORKSPACE_URI")
		assert.True(t, ok)
		assert.NotEmpty(t, uriStr)
		assert.Contains(t, uriStr, dir)
		path, ok := m.envSource("WORKSPACE_PATH")
		assert.True(t, ok)
		assert.Equal(t, dir, path)
	})

	t.Run("unknown names return ok=false", func(t *testing.T) {
		dir := t.TempDir()
		m := newTestWorkspaceManagerHandlerWithDir(t,
			defaultConfigWithWrap(false), dir, nopShutdownShaderConfig())
		t.Cleanup(func() { _ = m.Close() })
		_, ok := m.envSource("FILE")
		assert.False(t, ok, "envSource is workspace-scoped only")
		_, ok = m.envSource("")
		assert.False(t, ok)
	})
}
