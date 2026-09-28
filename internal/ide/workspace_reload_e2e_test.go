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

//go:build e2e

package ide

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/ide/pkgtrust"
	"unstable.build/rune/internal/term/vte/vtereservoir"
)

// TestE2EWorkspaceReloadRestoresLayoutAndTerminalOutput drives the
// full real IDE — real config, real file scheme, real vte handler —
// through the same flow that surfaced the original
// :workspacereload DeadlineExceeded bug, and asserts that the
// post-reload IDE preserves both the workspace layout and the
// captured terminal output.
//
// Flow:
//  1. cwd workspace points to a temp dir on disk; auto_restore is on
//     so the reload skips the restore-prompt.
//  2. Open a test file (left window).
//  3. windownew right creates a fresh empty window on the right.
//  4. terminalnew opens a real vte.Handler in that right window
//     running a script that prints `abc` and stays alive, since a
//     terminal whose process exits is closed.
//  5. After the terminal output settles, the test snapshots the
//     layout topology and the textual content of the terminal cells.
//  6. :workspacereload is dispatched the same way a user would
//     dispatch it — through the command prompt.
//  7. After the workspace re-installs, the layout must be the same
//     vertical split, the right window must still hold a vte
//     handler, and its snapshot must still contain "abc".
func TestE2EWorkspaceReloadRestoresLayoutAndTerminalOutput(t *testing.T) {
	dir := t.TempDir()
	dataDir := t.TempDir()

	// The command key is Ctrl+backslash. Tests pick that combination
	// instead of `:` because, after opening a real vte terminal,
	// keyboard focus lands on the vte and a plain `:` would be eaten
	// by the shell — `<c-\\>` reliably opens the command prompt no
	// matter which handler is in focus.
	//
	// auto_restore: true prevents the post-reload "Do you want to
	// restore the previous session?" prompt from interposing itself
	// between the reload and the test assertions.
	configPath := filepath.Join(dataDir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
editor:
  mode: modal
command:
  key: "<c-\\\\>"
workspace:
  auto_restore: true
`), 0o666))

	testFile := filepath.Join(dir, "hello.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("hello world\n"), 0o644))
	script := filepath.Join(dir, "abc.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho abc\nexec cat\n"), 0o755))

	mu := new(sync.Mutex)
	scheduleNextTick, drainSchedule := newTestScheduler(t, mu)

	i, err := New(dir, configPath, dataDir, pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir),
		WithLocker(mu),
		WithScheduleNextTick(scheduleNextTick),
		WithPublishEvent(func(term.Event) bool { return true }),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = i.Close() })

	root := i.Ready()
	mu.Lock()
	root.Resize(80, 24)
	mu.Unlock()
	drainSchedule()
	i.WaitWorkspaces()

	sendKeys := func(t *testing.T, seq string) {
		t.Helper()
		keys, err := term.ParseKeys(seq)
		require.NoError(t, err)
		for _, k := range keys {
			mu.Lock()
			root.Handle(term.Event{
				Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key,
			})
			mu.Unlock()
			i.WaitInflight()
		}
	}

	// 1) Open the file (left window).
	// 2) Open an empty window on the right.
	// 3) Open a terminal in that right window printing `abc`.
	sendKeys(t,
		"<c-\\\\>edit<space>"+testFile+"<enter>"+
			"<c-\\\\>windownew<space>right<enter>"+
			"<c-\\\\>terminalnew<space>"+script+"<enter>",
	)

	// Find the terminal we just created and wait for "abc" to land
	// in its active cells.
	type windowSnapshot struct {
		windowID  uint64
		cellsText string
	}
	snapshotTerminalWindow := func() (windowSnapshot, bool) {
		mu.Lock()
		defer mu.Unlock()
		ex := i.workspaceHandler.focusEx()
		var found windowSnapshot
		var ok bool
		ex.comp.Browser().IterateWindows(func(win browser.Window) {
			if ok {
				return
			}
			content, cerr := win.Content()
			if cerr != nil {
				return
			}
			vte, isVTE := content.(vtereservoir.VTE)
			if !isVTE {
				return
			}
			snap, sErr := vte.Snapshot()
			if sErr != nil {
				return
			}
			found = windowSnapshot{
				windowID:  win.WindowID(),
				cellsText: term.CellsToString(snap.ActiveCells()),
			}
			ok = true
		})
		return found, ok
	}
	var before windowSnapshot
	require.Eventually(t, func() bool {
		snap, ok := snapshotTerminalWindow()
		if !ok {
			return false
		}
		if !strings.Contains(snap.cellsText, "abc") {
			return false
		}
		before = snap
		return true
	}, 10*time.Second, 50*time.Millisecond,
		"terminal did not produce `abc` before workspacereload")

	// Capture the layout structure before reload — must remain
	// identical after reload.
	mu.Lock()
	layoutBefore := i.workspaceHandler.focusEx().comp.Browser().TileLayout()
	mu.Unlock()
	require.Equal(t, tcomponent.SplitOrientationVertical, layoutBefore.Split,
		"pre-reload layout must be a single vertical split "+
			"(left file / right terminal)")
	require.Len(t, layoutBefore.Children, 2,
		"pre-reload layout must have exactly two leaves")
	rightLeafBefore := layoutBefore.Children[1]
	require.Equal(t, before.windowID, rightLeafBefore.WindowID,
		"the terminal must live in the right leaf of the pre-reload layout")

	// 4) Drive :workspacereload through the command prompt — the
	// same path a real user takes.
	sendKeys(t, "<c-\\\\>workspacereload<enter>")

	// reload tears down and re-adds the workspace asynchronously
	// through addWorkspace.
	i.WaitWorkspaces()
	i.WaitInflight()

	require.Eventually(t, func() bool {
		mu.Lock()
		ex := i.workspaceHandler.focusEx()
		layout := ex.comp.Browser().TileLayout()
		mu.Unlock()
		return len(layout.Children) == 2
	}, 30*time.Second, 50*time.Millisecond,
		"workspace reload did not restore the split layout")

	// Layout must be preserved: still one vertical split with two
	// leaves.
	mu.Lock()
	layoutAfter := i.workspaceHandler.focusEx().comp.Browser().TileLayout()
	mu.Unlock()
	require.Equal(t, tcomponent.SplitOrientationVertical, layoutAfter.Split,
		"post-reload layout must remain a vertical split")
	require.Len(t, layoutAfter.Children, 2,
		"post-reload layout must still have two leaves")

	// The right window's terminal must be re-created with the same
	// captured output. RestoreTileLayout allocates fresh window IDs,
	// so we do not require the leaves' WindowIDs to match pre-reload
	// — only that a vte still lives in the workspace and that its
	// snapshot contains "abc".
	var after windowSnapshot
	require.Eventually(t, func() bool {
		snap, ok := snapshotTerminalWindow()
		if !ok {
			return false
		}
		if !strings.Contains(snap.cellsText, "abc") {
			return false
		}
		after = snap
		return true
	}, 10*time.Second, 50*time.Millisecond,
		"post-reload terminal must still contain `abc`")

	// The restored terminal must live in the right leaf of the
	// post-reload layout.
	rightLeafAfter := layoutAfter.Children[1]
	require.NotZero(t, rightLeafAfter.WindowID,
		"post-reload right leaf must reference a concrete window")
	require.Equal(t, rightLeafAfter.WindowID, after.windowID,
		"the restored terminal must live in the right leaf of the post-reload layout")
}
