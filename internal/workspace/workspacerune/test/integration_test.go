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

package runetest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacetest"
)

func TestIntegrationScheme(t *testing.T) {
	SkipIfRace(t)
	SkipIfNoDocker(t)

	control := StartHeadscale(t)
	peer := StartInstance(t, control, "peer", control.AuthKey)

	client := StartNode(t, control, "client", control.AuthKey)
	WaitPeer(t, client, "peer")

	// The serving instance advertises its own data directory as the
	// install root; the client must see it verbatim over the mesh.
	t.Run("install root", func(t *testing.T) {
		scheme := OpenWorkspace(t, client, "peer", t.TempDir())
		defer scheme.Close()
		root, err := scheme.(workspace.InstallDataDirProvider).
			InstallDataDir(t.Context())
		require.NoError(t, err)
		assert.Equal(t, peer.DataDir, root)
	})

	newScheme := func(t *testing.T) schemeapi.Scheme {
		return OpenWorkspace(t, client, "peer", t.TempDir())
	}
	workspacetest.TestWorkspaceSchemeFiles(t, newScheme)
	workspacetest.TestWorkspaceSchemeExecutor(t, newScheme)
}
