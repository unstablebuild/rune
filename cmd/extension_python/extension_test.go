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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// bringUp is one run of the extension against a scripted executor, so
// the environment decision is exercised without running real uv.
type bringUp struct {
	exec   *fakeExecutor
	lsp    *captureLSP
	notify *fakeNotifications
}

// newProjectDir creates a workspace root that detects as kindProject.
func newProjectDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "pyproject.toml"), []byte("[project]\n"), 0o644))
	return dir
}

func runBringUp(
	t *testing.T, dir string, storage storageapi.Service, wm *fakeWindowManager,
) bringUp {
	t.Helper()
	dataDir := t.TempDir()
	seedManagedFallback(t, dataDir)

	fs := realFS{root: dir}
	ex := newFakeExecutor()
	ex.respond("uv python find", scriptedCmd{})
	ex.respond("uv sync", scriptedCmd{})
	b := bringUp{exec: ex, lsp: &captureLSP{}, notify: newFakeNotifications()}

	var windows browserapi.WindowManager
	if wm != nil {
		windows = wm
	}
	ext := &pyExtension{}
	require.NoError(t, ext.extendWorkspaceWith(context.Background(),
		fs, ex, b.notify, b.lsp, &fakeEditor{},
		fakeInstaller{fs: fs, root: dataDir},
		config.NopConfig(), dataDir, storage, windows,
		func(textapi.CommandManual, textapi.REPLHandler) error { return nil },
	))
	return b
}

// waitForInit waits for the language server bring-up, which runs on a
// background goroutine whenever the root has no stored answer.
func (b bringUp) waitForInit(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, count := b.lsp.captured()
		return count == 1
	}, 5*time.Second, 5*time.Millisecond, "language server must initialize")
}

var (
	enterEvents = []term.Event{{Type: term.EventKey, Key: term.KeyEnter}}
	optOutEvent = []term.Event{
		{Type: term.EventKey, Key: term.KeyArrowRight},
		{Type: term.EventKey, Key: term.KeyEnter},
	}
	dismissEvent = []term.Event{{Type: term.EventKey, Key: term.KeyEsc}}
)

func TestExtendWorkspaceEnvPolicy(t *testing.T) {
	ctx := context.Background()

	t.Run("disabled root runs no uv and still initializes the server", func(t *testing.T) {
		dir := newProjectDir(t)
		storage := storagestub.NewInMemoryService()
		require.NoError(t, newEnvSetting(storage).set(ctx, dirRoot(dir), false))
		wm := &fakeWindowManager{events: enterEvents}

		b := runBringUp(t, dir, storage, wm)
		b.waitForInit(t)
		assert.Empty(t, b.exec.callsSnapshot())
		assert.Zero(t, wm.callCount(), "a stored answer must not prompt")
	})

	t.Run("enabled root brings the environment up", func(t *testing.T) {
		dir := newProjectDir(t)
		storage := storagestub.NewInMemoryService()
		require.NoError(t, newEnvSetting(storage).set(ctx, dirRoot(dir), true))

		b := runBringUp(t, dir, storage, nil)
		b.waitForInit(t)
		assert.Equal(t, []string{"uv python find", "uv sync"}, b.exec.callsSnapshot())
	})

	t.Run("unknown root prompts once and persists the answer", func(t *testing.T) {
		dir := newProjectDir(t)
		storage := storagestub.NewInMemoryService()
		wm := &fakeWindowManager{events: enterEvents}

		b := runBringUp(t, dir, storage, wm)
		b.waitForInit(t)
		assert.Equal(t, 1, wm.callCount())
		assert.Equal(t, []string{"uv python find", "uv sync"}, b.exec.callsSnapshot())

		managed, known, err := newEnvSetting(storage).get(ctx, dirRoot(dir))
		require.NoError(t, err)
		assert.True(t, known)
		assert.True(t, managed)
	})

	t.Run("opting out persists and skips every uv call", func(t *testing.T) {
		dir := newProjectDir(t)
		storage := storagestub.NewInMemoryService()
		wm := &fakeWindowManager{events: optOutEvent}

		b := runBringUp(t, dir, storage, wm)
		b.waitForInit(t)
		assert.Empty(t, b.exec.callsSnapshot())

		managed, known, err := newEnvSetting(storage).get(ctx, dirRoot(dir))
		require.NoError(t, err)
		assert.True(t, known)
		assert.False(t, managed)
	})

	t.Run("dismissing persists nothing", func(t *testing.T) {
		dir := newProjectDir(t)
		storage := storagestub.NewInMemoryService()
		wm := &fakeWindowManager{events: dismissEvent}

		b := runBringUp(t, dir, storage, wm)
		b.waitForInit(t)
		assert.Empty(t, b.exec.callsSnapshot())

		_, known, err := newEnvSetting(storage).get(ctx, dirRoot(dir))
		require.NoError(t, err)
		assert.False(t, known)
	})

	t.Run("a second bring-up of the same root does not prompt", func(t *testing.T) {
		dir := newProjectDir(t)
		storage := storagestub.NewInMemoryService()
		wm := &fakeWindowManager{events: enterEvents}

		runBringUp(t, dir, storage, wm).waitForInit(t)
		require.Equal(t, 1, wm.callCount())

		runBringUp(t, dir, storage, wm).waitForInit(t)
		assert.Equal(t, 1, wm.callCount())
	})

	// A loose-scripts root has nothing to sync, so it is left alone
	// entirely: no prompt, no stored policy, no nag.
	t.Run("loose scripts root is never auto-managed", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, "main.py"), []byte("pass\n"), 0o644))
		storage := storagestub.NewInMemoryService()
		wm := &fakeWindowManager{events: enterEvents}

		b := runBringUp(t, dir, storage, wm)
		b.waitForInit(t)
		assert.Zero(t, wm.callCount())
		assert.Empty(t, b.exec.callsSnapshot())
		for _, msg := range b.notify.notifs {
			assert.NotContains(t, msg, "not managing the Python environment")
		}

		_, known, err := newEnvSetting(storage).get(ctx, dirRoot(dir))
		require.NoError(t, err)
		assert.False(t, known, "no policy document may be written")
	})
}
