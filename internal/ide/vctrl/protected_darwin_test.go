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

//go:build darwin

package vctrl

import (
	"context"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/workspace"
)

func TestLoadGitignoreExcludesProtectedHomeDirs(t *testing.T) {
	usr, err := user.Current()
	require.NoError(t, err)
	if usr.HomeDir == "" {
		t.Skip("no home directory for current user")
	}

	uri, err := workspaceapi.ParseURI("file://" + usr.HomeDir)
	require.NoError(t, err)
	cwd, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)

	matcher, err := LoadGitignore(cwd)
	require.NoError(t, err)

	libraryURI, err := cwd.URI("Library")
	require.NoError(t, err)
	assert.True(t, matcher.Match(libraryURI, true),
		"~/Library must be excluded by default")

	for _, dir := range []string{"Documents", "Desktop", "Downloads"} {
		uri, err := cwd.URI(dir)
		require.NoError(t, err)
		assert.Falsef(t, matcher.Match(uri, true),
			"~/%s must remain visible", dir)
	}

	subURI, err := cwd.URI(filepath.Join("Library", "Containers", "com.example.app"))
	require.NoError(t, err)
	assert.True(t, matcher.Match(subURI, true),
		"protected dir descendants must be excluded by default")

	okURI, err := cwd.URI("Projects")
	require.NoError(t, err)
	assert.False(t, matcher.Match(okURI, true),
		"non-protected home dirs must remain visible")
}
