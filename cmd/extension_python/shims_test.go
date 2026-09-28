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

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"unstable.build/rune/cmd/extension_python/pyshim"
	"unstable.build/rune/internal/extension/langext"
)

// initRootHarness runs initializeProjectRoot against an empty workspace
// dir on the real filesystem with a scripted executor, returning the
// executor for call assertions.
func initRootHarness(t *testing.T, dataDir string, cfg config.Config) *fakeExecutor {
	t.Helper()
	work := t.TempDir()
	fs := realFS{root: work}
	ex := newFakeExecutor()
	ex.respond("uv python find", scriptedCmd{})
	ex.respond("uv python install --default", scriptedCmd{})
	ex.respond("uvx --from debugpy==1.8.17 python -c ", scriptedCmd{})
	root := langext.Root{Dir: work, URI: "file://" + work}
	setting := newEnvSetting(storagestub.NewInMemoryService())
	require.NoError(t, setting.set(context.Background(), root, true))
	err := initializeProjectRoot(context.Background(), fs, ex,
		newFakeNotifications(), &captureLSP{},
		fakeInstaller{fs: fs, root: dataDir}, cfg, dataDir, setting, nil, root)
	require.NoError(t, err)
	return ex
}

func TestInitializeProjectRootInstallsShims(t *testing.T) {
	dataDir := t.TempDir()
	initRootHarness(t, dataDir, config.NopConfig())
	assert.FileExists(t, filepath.Join(pyshim.Dir(dataDir), "python"))
	assert.FileExists(t, filepath.Join(pyshim.Dir(dataDir), "python3"))
}

func TestInitializeProjectRootPrewarmsDebugpy(t *testing.T) {
	cfg := config.JSONFromMap(map[string]any{"debugpy": "1.8.17"})
	ex := initRootHarness(t, t.TempDir(), cfg)
	assert.Eventually(t, func() bool {
		for _, call := range ex.callsSnapshot() {
			if strings.HasPrefix(call, "uvx --from debugpy==1.8.17") {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "background uvx prewarm must run")
}

func TestInitializeProjectRootSkipsPrewarmWithoutPin(t *testing.T) {
	ex := initRootHarness(t, t.TempDir(), config.NopConfig())
	time.Sleep(50 * time.Millisecond)
	for _, call := range ex.callsSnapshot() {
		assert.NotContains(t, call, "uvx", "no prewarm must run without a debugpy pin")
	}
}

func TestDebugpyPin(t *testing.T) {
	t.Run("nil config returns empty", func(t *testing.T) {
		assert.Equal(t, "", debugpyPin(nil, newFakeNotifications()))
	})

	t.Run("absent key returns empty", func(t *testing.T) {
		notify := newFakeNotifications()
		assert.Equal(t, "", debugpyPin(config.NopConfig(), notify))
		assert.Empty(t, notify.notifs)
	})

	t.Run("pin returned", func(t *testing.T) {
		cfg := config.JSONFromMap(map[string]any{"debugpy": "1.8.17"})
		assert.Equal(t, "1.8.17", debugpyPin(cfg, newFakeNotifications()))
	})

	t.Run("non-string warns and returns empty", func(t *testing.T) {
		cfg := config.JSONFromMap(map[string]any{"debugpy": 1.8})
		notify := newFakeNotifications()
		assert.Equal(t, "", debugpyPin(cfg, notify))
		assert.NotEmpty(t, notify.notifs)
	})
}
