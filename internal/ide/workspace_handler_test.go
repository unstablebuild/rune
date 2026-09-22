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
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	_ "net/http/pprof"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/document"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release/docrelease"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/docmarshal/docbson"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/component/notifications"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension"
	"unstable.build/rune/internal/handler/handlertest"
	handlermarkdown "unstable.build/rune/internal/handler/markdown"
	"unstable.build/rune/internal/ide/ideauthorizer"
	"unstable.build/rune/internal/ide/idehistory"
	"unstable.build/rune/internal/ide/idetask"
	"unstable.build/rune/internal/ide/pkgtrust"
	"unstable.build/rune/internal/ide/vctrl/testgit"
	"unstable.build/rune/internal/localstorage"
	"unstable.build/rune/internal/term/vte/vtereservoir"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/textrpc"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacetest"
)

func TestFileCommandRegistryIntegration(t *testing.T) {
	dir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(dir)
	})

	m := newTestWorkspaceManagerHandlerWithDir(t, defaultConfigWithWrap(false), dir,
		nopShutdownShaderConfig())

	h := newSafeHandler(m)
	// jumptolocation is registered on a per-file basis, so the following tests
	// file-level subscriptions across a file's lifecycle.
	cases := []handlertest.SequenceTestCase{
		{":edit dakar.md>igentleman>driver>gentleman<:write>/gentleman>:jumptolocation next search>",
			`┌━━━━━━━━━━──────────────────┐
│o dakar.md                  │
├────────────────────────────┤
│gentleman                   │
│driver                      │
│▐entleman                   │
│       searching 'gentleman'│
│                      NORMAL│
└────────────────────────────┘`},
		{":jumptolocation next search>",
			`┌━━━━━━━━━━──────────────────┐
│o dakar.md                  │
├────────────────────────────┤
│▐entleman                   │
│driver                      │
│gentleman                   │
│       searching 'gentleman'│
│                      NORMAL│
└────────────────────────────┘`},
		{":tabclose>:edit dakar.md>/gentleman>",
			`┌━━━━━━━━━━──────────────────┐
│o dakar.md                  │
├────────────────────────────┤
│gentleman                   │
│driver                      │
│▐entleman                   │
│       searching 'gentleman'│
│                      NORMAL│
└────────────────────────────┘`},
		{":jumptolocation next search>",
			`┌━━━━━━━━━━──────────────────┐
│o dakar.md                  │
├────────────────────────────┤
│▐entleman                   │
│driver                      │
│gentleman                   │
│       searching 'gentleman'│
│                      NORMAL│
└────────────────────────────┘`},
		{":foldexpandall>", // this fails if not installed correctly
			`┌━━━━━━━━━━──────────────────┐
│o dakar.md                  │
├────────────────────────────┤
│▐entleman                   │
│driver                      │
│gentleman                   │
│       searching 'gentleman'│
│                      NORMAL│
└────────────────────────────┘`},
	}
	handlertest.TestHandlerSequence(t, h, 30, 9, cases)

	require.NoError(t, m.Close())
}

// TestFileExplorerEnterDelegatesIntegration exercises the real
// :fexplorer integration path with both real text.Editor
// implementations (vi-style modal and modeless). Regression
// coverage for RUNE-138: when the file explorer's inner editor
// reports IsSearchMode() == true, pressing <Enter> must delegate
// to the inner editor (committing the search) rather than be
// captured by the outer fileExplorerHandler as expand-or-open.
//
// The IsSearchMode() signal must propagate from the leaf editor
// handler (vi.Vi or modeless.editorHandler) up through every
// wrapper that text.Editor.Edit installs — text.Publisher's
// cursorPublisher, fold/location/indent/comment/git command
// wrappers, status/aux/icons bars — and reach
// fileExplorerHandler.ed.IsSearchMode(). Because text.Handler
// declares IsSearchMode(), this propagation happens through
// interface embedding on each wrapper.
//
// vi has a key-driven inline search ('/'), so for modal we drive
// the bug repro end-to-end: '/findme<Enter>' must finish the
// search and not toggle the tree. modeless does not have a
// key-driven inline search (text search in modeless is surfaced
// through a separate fuzzy_search extension command), so we
// instead assert the non-search-mode behavior is preserved end-
// to-end: <Enter> still toggles the tree exactly as before.
func TestFileExplorerEnterDelegatesIntegration(t *testing.T) {
	cases := []struct {
		name string
		mode string
	}{
		{name: "modal", mode: editorModeModal},
		{name: "standard", mode: editorModeStandard},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(
				filepath.Join(dir, "findme.txt"), []byte("hi"), 0o644))
			require.NoError(t, os.MkdirAll(
				filepath.Join(dir, "subdir"), 0o755))
			require.NoError(t, os.WriteFile(
				filepath.Join(dir, "subdir", "child.txt"),
				[]byte("hi"), 0o644))

			cfg := defaultConfigWithWrap(false)
			editorCfg := cfg.cfg["editor"].(map[string]any)
			editorCfg["mode"] = tc.mode
			cfg.cfg["editor"] = editorCfg
			require.Equal(t, tc.mode, cfg.editorMode())

			uri, err := workspaceapi.ParseURI("file://" + dir)
			require.NoError(t, err)
			m := newTestWorkspaceManagerHandlerWithDir(t, cfg, dir,
				nopShutdownShaderConfig())
			require.NoError(t, m.addOrCreateWorkspace(uri))
			m.quiesce()
			t.Cleanup(func() { _ = m.Close() })

			h := newSafeHandler(m)
			h.Resize(40, 12)

			ex := m.focusEx()
			require.NotNil(t, ex)
			// ex.fexplorer constructs newFileExplorerHandler, which the
			// async FS-watcher goroutine spawned by Phase C reads from
			// under m.mu. Take m.mu while building the explorer so its
			// internal state is published before the watcher dispatches
			// a Create event into eventApplies.
			m.mu.Lock()
			err = ex.fexplorer(context.Background())
			m.mu.Unlock()
			require.NoError(t, err)
			require.NotNil(t, ex.fileExplorerWin,
				"file explorer must be open")
			require.NotNil(t, ex.fileExplorerHandler)

			explorer := ex.fileExplorerHandler
			require.False(t, explorer.ed.IsSearchMode(),
				"IsSearchMode must propagate through the full "+
					"wrapper chain and report false initially")

			beforeRows := explorer.ed.CellView().Rows()
			require.Greater(t, beforeRows, 0)

			switch tc.mode {
			case editorModeModal:
				// Bug repro: enter search mode and type a query.
				_, handled := h.Handle(term.Event{
					Type: term.EventKey, Ch: '/',
				})
				require.True(t, handled,
					"'/' must enter vi search mode")
				require.True(t, explorer.ed.IsSearchMode(),
					"vi must be in search mode after '/'")

				for _, r := range "findme" {
					_, handled = h.Handle(term.Event{
						Type: term.EventKey, Ch: r,
					})
					require.True(t, handled,
						"typed char %q must be handled", r)
				}
				require.True(t, explorer.ed.IsSearchMode(),
					"vi must still be in search mode while "+
						"typing the query")

				_, handled = h.Handle(term.Event{
					Type: term.EventKey, Key: term.KeyEnter,
				})
				require.True(t, handled,
					"<Enter> must be handled")
				// Bug regression: <Enter> in search mode must
				// commit the search and exit search mode rather
				// than be captured by the outer file explorer.
				require.False(t, explorer.ed.IsSearchMode(),
					"<Enter> in search mode must commit the "+
						"inner search and exit search mode")
				require.Equal(t, beforeRows,
					explorer.ed.CellView().Rows(),
					"<Enter> in search mode must not expand "+
						"or collapse a tree node")
				for _, tab := range ex.comp.Browser().Tabs() {
					require.NotEqual(t,
						"file://"+filepath.Join(dir, "findme.txt"),
						tab.URI().String(),
						"<Enter> in search mode must not open "+
							"a file from the explorer")
				}

			case editorModeStandard:
				// standard has no key-driven inline search in the
				// explorer; verify the non-search-mode behavior
				// still holds end-to-end through the full
				// wrapper chain. With the cursor on the first
				// (directory) row, <Enter> must expand the tree
				// — i.e. row count increases.
				_, handled := h.Handle(term.Event{
					Type: term.EventKey, Key: term.KeyEnter,
				})
				require.True(t, handled,
					"<Enter> must be handled")
				require.False(t, explorer.ed.IsSearchMode(),
					"standard must not be in search mode after "+
						"<Enter> on a non-search context")
				require.NotEqual(t, beforeRows,
					explorer.ed.CellView().Rows(),
					"<Enter> outside search mode must still "+
						"toggle the tree (regression guard)")
			}
		})
	}
}

// TestFileExplorerReactsToFilesystemChangesIntegration verifies that reopening
// the cached explorer refreshes its tree from disk after a file is created while
// the explorer is closed.
func TestFileExplorerReactsToFilesystemChangesIntegration(t *testing.T) {
	// Resolve symlinks so the workspace URI matches the canonical
	// path emitted by the FS watcher. On macOS t.TempDir() returns
	// /var/folders/... but the kqueue watcher reports
	// /private/var/folders/...; without canonicalisation the
	// explorer's prefix check would reject every event.
	rawDir := t.TempDir()
	dir, err := filepath.EvalSymlinks(rawDir)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "alpha.go"), []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "beta.go"), []byte("b"), 0o644))

	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)
	// Use a separate dataDir so the pkgmanager's .db/ scratch
	// directory doesn't appear inside the workspace and pollute
	// the rendered tree.
	m := newTestWorkspaceManagerHandlerWithDirs(t,
		defaultConfigWithWrap(false), dir, t.TempDir(),
		nopShutdownShaderConfig())
	err = m.addOrCreateWorkspace(uri)
	require.NoError(t, err)
	m.quiesce()
	t.Cleanup(func() { _ = m.Close() })

	h := newSafeHandler(m)
	const width, height = 30, 9

	openedFrame := strings.Join([]string{
		"┌────────────────────────────┐",
		"│                            │",
		"┌────────────┐┌──────────────┤",
		"│ ▐ alpha.go ││              │",
		"│ o beta.go  ││workspaceWallp│",
		"│            ││              │",
		"├────────────┘└──────────────┤",
		"│1 1  2 2                    │",
		"└─────━━━────────────────────┘",
	}, "\n")
	closedFrame := strings.Join([]string{
		"┌────────────────────────────┐",
		"│                            │",
		"├────────────────────────────┤",
		"│                            │",
		"│     workspaceWallpaper     │",
		"│                            │",
		"├────────────────────────────┤",
		"│1 1  2 2                    │",
		"└─────━━━────────────────────┘",
	}, "\n")
	editorFrame := strings.Join([]string{
		"┌━━━━━━━━━━──────────────────┐",
		"│o gamma.go                  │",
		"├────────────────────────────┤",
		"│hell▐                       │",
		"│                            │",
		"│                      NORMAL│",
		"├────────────────────────────┤",
		"│1 1  2 2                    │",
		"└─────━━━────────────────────┘",
	}, "\n")
	reopenedFrame := strings.Join([]string{
		"┌────────────────────────────┐",
		"│o gamma.go                  │",
		"┌────────────┐┌──────────────┤",
		"│ ▐ alpha.go ││hello         │",
		"│ o beta.go  ││              │",
		"│ o gamma.go ││        NORMAL│",
		"├────────────┘└──────────────┤",
		"│1 1  2 2                    │",
		"└─────━━━────────────────────┘",
	}, "\n")

	handlertest.RunHandlerSequence(t, h, width, height,
		[]handlertest.SequenceTestCase{
			{
				InputSequence: "<c-\\\\>fexplorer<enter>",
				Expected:      openedFrame,
			},
			{
				InputSequence: "<c-\\\\>fexplorer<enter>",
				Expected:      closedFrame,
			},
			{
				InputSequence: "<c-\\\\>edit<space>gamma.go<enter>" +
					"ihello<esc><c-\\\\>write<enter>",
				Expected: editorFrame,
			},
		})

	handlertest.RunHandlerSequence(t, h, width, height,
		[]handlertest.SequenceTestCase{{
			InputSequence: "<c-\\\\>fexplorer<enter>",
			Expected:      reopenedFrame,
		}})
}

// TestGitlinkIntegration exercises the :gitlink command end-to-end
// against a real on-disk git repository, using the same editor wiring
// production uses (newBuiltinModal/ModelessEditor →
// vctrlcmd.SubscribeGitCommands). It is a regression guard for a nil
// pointer dereference at vctrlcmd/remote_web_link.go:195: vi.Editor
// did not seed its viConfig with defaults, so the notifications
// interface forwarded into copyRemoteURL was nil and Notify panicked
// the moment :gitlink completed successfully.
//
// The test asserts that:
//   - :gitlink does not panic;
//   - the resulting URL is copied to the clipboard;
//   - a success notification is rendered on screen with the URL of
//     the file currently open at the cursor position.
func TestGitlinkIntegration(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}

	for _, mode := range []string{editorModeModal, editorModeStandard} {
		t.Run(mode, func(t *testing.T) {
			dir, commit, relFile := setupGitlinkRepo(t)

			cfg := defaultConfigWithWrap(false)
			editorCfg := cfg.cfg["editor"].(map[string]any)
			editorCfg["mode"] = mode
			cfg.cfg["editor"] = editorCfg
			require.Equal(t, mode, cfg.editorMode())

			uri, err := workspaceapi.ParseURI("file://" + dir)
			require.NoError(t, err)
			m := newTestWorkspaceManagerHandlerWithDir(t, cfg, dir,
				nopShutdownShaderConfig())
			require.NoError(t, m.addOrCreateWorkspace(uri))
			m.quiesce()
			t.Cleanup(func() { _ = m.Close() })

			h := newSafeHandler(m)

			expectedURL := fmt.Sprintf(
				"https://github.com/unstablebuild/gitproj/blob/%s/%s#L1",
				commit, relFile)

			// Open the file via :edit so the full vi/modeless
			// editor wrapper chain (SubscribeGitCommands included)
			// is installed for the file's text.Handler — exactly
			// the path that panicked in production. Then run
			// :gitlink. The notification box wraps "copied <url>"
			// at 11 columns (15 cols wide minus borders/padding)
			// and spans the full editor height, occluding the
			// status row. Asserting the rendered framebuffer
			// guarantees the notification reached Draw, not just
			// Notify.
			cases := []handlertest.SequenceTestCase{{
				InputSequence: `<c-\\>edit<space>` + relFile +
					`<enter><c-\\>gitlink<enter>`,
				Expected: strings.Join([]string{
					"┌━━━━━━━━━━━━━━──────────────────────────────────────────────────┌─────────────┐",
					"│o guasacaca.md                                                  │ copied      │",
					"├────────────────────────────────────────────────────────────────│ https://git │",
					"│▐i                                                              │ hub.com/uns │",
					"│                                                                │ tablebuild/ │",
					"│                                                                │ gitproj/blo │",
					"│                                                                │ b/d8b96a860 │",
					"│                                                                │ 305b0c87524 │",
					"│                                                                │ da7eaae159f │",
					"├────────────────────────────────────────────────────────────────│ 25dd233db/r ┤",
					"│1 1  2 2                                                                      │",
					"└─────━━━──────────────────────────────────────────────────────────────────────┘",
				}, "\n"),
			}}
			require.NotPanics(t, func() {
				handlertest.RunHandlerSequence(t, h, 80, 12, cases)
			})

			paste, err := m.clip.Paste(clipboard.DefaultRegisterID)
			require.NoError(t, err)
			assert.Equal(t, expectedURL, paste.Text,
				":gitlink must copy the web URL of the file "+
					"under the cursor to the clipboard")
		})
	}
}

// setupGitlinkRepo creates a git repo in a fresh temp dir with a
// single committed file under recipes/ and a github origin remote.
// Author/committer dates are pinned so the resulting commit hash is
// deterministic across runs and across machines.
func setupGitlinkRepo(t *testing.T) (dir, commit, relFile string) {
	t.Helper()
	dir = t.TempDir()
	// EvalSymlinks because git returns canonical paths on macOS
	// (/private/var/folders/...) and the workspace path must match
	// for vctrl.RelPath to succeed.
	canonical, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	dir = canonical

	relFile = filepath.Join("recipes", "guasacaca.md")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "recipes"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, relFile), []byte("hi\n"), 0o644))

	dateEnv := []string{
		"GIT_AUTHOR_DATE=2020-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE=2020-01-01T00:00:00Z",
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"remote", "add", "origin",
			"https://github.com/unstablebuild/gitproj.git"},
		{"add", "."},
		{"commit", "-q", "-m", "initial"},
	} {
		cmd := testgit.Command(t, dir, args...)
		cmd.Env = append(cmd.Env, dateEnv...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}

	out := testgit.Run(t, dir, "rev-parse", "HEAD")
	commit = strings.TrimSpace(string(out))
	return
}

// TestCommandPromptEditModeWrappedCursor exercises the integration
// between the modal command prompt's responsive (wrapping) renderer
// and its embedded vi editor (which has wrap=false). When the user
// fills the prompt past one visual row, opens edit mode via
// <shift-esc>, and navigates with hjkl, the cursor must follow the
// VISUAL wrapped position — not stay glued to row 0 of the editor's
// flat buffer view.
//
// At width=60 the prompt clamps to its 50-wide minWidth dimension,
// minus the 2-cell frame, minus animationWidth=3 = 45 cells of
// visible input per visual row. 134 ones therefore render as three
// visual rows of 45/45/44 cells.
func TestCommandPromptEditModeWrappedCursor(t *testing.T) {
	dir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	uri1, err := workspaceapi.ParseURI("memory://" + dir)
	require.NoError(t, err)

	m := newTestWorkspaceManagerHandlerWithDir(t, defaultConfigWithWrap(false), dir,
		nopShutdownShaderConfig())
	require.NoError(t, m.addOrCreateWorkspace(uri1))

	h := newSafeHandler(m)
	const width, height = 60, 15
	const fill = 134 // 45 + 45 + 44 = 3 wrapped rows

	cases := []handlertest.SequenceTestCase{
		// 1. Open prompt and overflow it to three wrapped rows.
		// In command mode the cursor sits one past the last typed
		// character, i.e. visual (24, 2).
		{InputSequence: `<c-\\>` + strings.Repeat("1", fill), Expected: `
┌──────────────────────────────────────────────────────────┐
│                                                          │
├──────────────────────────────────────────────────────────┤
│                                                          │
│                                                          │
│                                                          │
│                                                          │
┌──────────────────────────────────────────────────────────┐
│ 11111111111111111111111111111111111111111111111111111    │
│ 11111111111111111111111111111111111111111111111111111    │
│ 1111111111111111111111111111▐                            │
└──────────────────────────────────────────────────────────┘
│                                                          │
│                                                          │
└──────────────────────────────────────────────────────────┘`[1:]},
		// 2. <s-esc> enters modal edit mode (vi normal mode); the
		// editor is seeded at the same buffer position as the
		// command-mode cursor (one past the last typed char) and
		// the prompt MUST translate that position through the same
		// wrap geometry. Without that translation the cursor
		// disappears (vi reports a window cursor that the prompt
		// blindly forwards into its own coordinate space).
		{InputSequence: "<s-esc>", Expected: `
┌──────────────────────────────────────────────────────────┐
│                                                          │
├──────────────────────────────────────────────────────────┤
│                                                          │
│                                                          │
│                                                          │
│                                                          │
┌──────────────────────────────────────────────────────────┐
│ 11111111111111111111111111111111111111111111111111111    │
│ 11111111111111111111111111111111111111111111111111111    │
│ 1111111111111111111111111111▐                            │
└──────────────────────────────────────────────────────────┘
│                                                          │
│                                                          │
└──────────────────────────────────────────────────────────┘`[1:]},
		// 3. Replace the first cell of visual row 0 with 'X' via
		// `0rX`. After `0` the cursor is at scroll col 0; `rX`
		// replaces it with X and keeps the cursor on the new char.
		{InputSequence: "0rX", Expected: `
┌──────────────────────────────────────────────────────────┐
│                                                          │
├──────────────────────────────────────────────────────────┤
│                                                          │
│                                                          │
│                                                          │
│                                                          │
┌──────────────────────────────────────────────────────────┐
│ ▐1111111111111111111111111111111111111111111111111111    │
│ 11111111111111111111111111111111111111111111111111111    │
│ 1111111111111111111111111111                             │
└──────────────────────────────────────────────────────────┘
│                                                          │
│                                                          │
└──────────────────────────────────────────────────────────┘`[1:]},
		// 4. Move to the last column of visual row 0 (scroll col 54)
		// and replace with X. The cursor sits ON the replaced char,
		// so it overlays the new X at visual (54, 0).
		{InputSequence: strings.Repeat("l", 54) + "rX", Expected: `
┌──────────────────────────────────────────────────────────┐
│                                                          │
├──────────────────────────────────────────────────────────┤
│                                                          │
│                                                          │
│                                                          │
│                                                          │
┌──────────────────────────────────────────────────────────┐
│ X1111111111111111111111111111111111111111111111111111    │
│ 1▐111111111111111111111111111111111111111111111111111    │
│ 1111111111111111111111111111                             │
└──────────────────────────────────────────────────────────┘
│                                                          │
│                                                          │
└──────────────────────────────────────────────────────────┘`[1:]},
		// 5. Step right one cell — first column of visual row 1.
		// User-requested: "move to the start of the second line".
		// Replace with X. Visual (0, 1).
		{InputSequence: "lrX", Expected: `
┌──────────────────────────────────────────────────────────┐
│                                                          │
├──────────────────────────────────────────────────────────┤
│                                                          │
│                                                          │
│                                                          │
│                                                          │
┌──────────────────────────────────────────────────────────┐
│ X1111111111111111111111111111111111111111111111111111    │
│ 1X▐11111111111111111111111111111111111111111111111111    │
│ 1111111111111111111111111111                             │
└──────────────────────────────────────────────────────────┘
│                                                          │
│                                                          │
└──────────────────────────────────────────────────────────┘`[1:]},
		// 6. End of visual row 1 (scroll col 109). Cursor overlays
		// the new X at visual (54, 1).
		{InputSequence: strings.Repeat("l", 54) + "rX", Expected: `
┌──────────────────────────────────────────────────────────┐
│                                                          │
├──────────────────────────────────────────────────────────┤
│                                                          │
│                                                          │
│                                                          │
│                                                          │
┌──────────────────────────────────────────────────────────┐
│ X1111111111111111111111111111111111111111111111111111    │
│ 1XX11111111111111111111111111111111111111111111111111    │
│ 111▐111111111111111111111111                             │
└──────────────────────────────────────────────────────────┘
│                                                          │
│                                                          │
└──────────────────────────────────────────────────────────┘`[1:]},
		// 7. Start of visual row 2 (scroll col 110).
		{InputSequence: "lrX", Expected: `
┌──────────────────────────────────────────────────────────┐
│                                                          │
├──────────────────────────────────────────────────────────┤
│                                                          │
│                                                          │
│                                                          │
│                                                          │
┌──────────────────────────────────────────────────────────┐
│ X1111111111111111111111111111111111111111111111111111    │
│ 1XX11111111111111111111111111111111111111111111111111    │
│ 111X▐11111111111111111111111                             │
└──────────────────────────────────────────────────────────┘
│                                                          │
│                                                          │
└──────────────────────────────────────────────────────────┘`[1:]},
		// 8. From start of row 2 (col 110, which the prior step
		// replaced with X) navigate to scroll col 132, replace with
		// X. Visual (22, 2). 134 cells fill row 2 partially:
		// positions 110..133, i.e. 24 cells. Col 0 of row 2 still
		// shows the X added in case 7. Col 23 still shows the
		// original 1 (134 cells, last index 133, visual col 23).
		{InputSequence: strings.Repeat("l", 22) + "rX", Expected: `
┌──────────────────────────────────────────────────────────┐
│                                                          │
├──────────────────────────────────────────────────────────┤
│                                                          │
│                                                          │
│                                                          │
│                                                          │
┌──────────────────────────────────────────────────────────┐
│ X1111111111111111111111111111111111111111111111111111    │
│ 1XX11111111111111111111111111111111111111111111111111    │
│ 111XX111111111111111111111▐1                             │
└──────────────────────────────────────────────────────────┘
│                                                          │
│                                                          │
└──────────────────────────────────────────────────────────┘`[1:]},
	}
	handlertest.RunHandlerSequence(t, h, width, height, cases)

	require.NoError(t, m.Close())
}

func TestSetTabNameWithAttrIntegration(t *testing.T) {
	dir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})

	cfg := defaultConfigWithWrap(false)
	bellRung := make(chan struct{}, 8)
	cfg.ringBell = func() {
		select {
		case bellRung <- struct{}{}:
		default:
		}
	}
	mu, sched, drain := buildTestSchedulerForCfg(t, &cfg)
	manager := workspace.NewManager(config.NopConfig(), sched)
	require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme,
		workspace.NewMemoryScheme))
	// A fake pty scheme keeps the terminal hermetic: the bell byte is
	// injected straight into the pty output stream instead of typing
	// into a host shell, whose startup output (prompts, the macOS bash
	// deprecation banner) would otherwise race the rendered frames.
	ptys := new(bellPtyState)
	const bellScheme = "bellpty"
	require.NoError(t, manager.RegisterScheme(bellScheme,
		func(ctx context.Context, cfg config.Config, uri workspaceapi.URI) (
			schemeapi.Scheme, error,
		) {
			base, err := newTerminalSessionTestScheme(ctx, cfg, uri)
			if err != nil {
				return nil, err
			}
			return &bellPtyScheme{Scheme: base, state: ptys}, nil
		}))

	bootURI, err := workspaceapi.ParseURI("memory://" + dir)
	require.NoError(t, err)
	runner := FuncExtensionsRunner(testRunnerFn)
	m := newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
		&bootURI, cfg, runner, nil, dir, nil, nopShutdownShaderConfig())

	uri1, err := workspaceapi.ParseURI(bellScheme + ":///workspace")
	require.NoError(t, err)
	require.NoError(t, m.addOrCreateWorkspace(uri1))
	m.quiesce()
	m.tabAttentionNameSuffix = "*"

	h := newSafeHandler(m)
	cases := []handlertest.SequenceTestCase{
		{InputSequence: "<c-\\\\>terminalnewtab<enter>" +
			"<c-\\\\>tabrename<space>terminal<enter>", // avoid dynamic tty name
			Expected: `┌━━━━━━━━━━──────────────────┐
│$ terminal                  │
├────────────────────────────┤
│▐                           │
│                            │
│                            │
├────────────────────────────┤
│1 1  2 2                    │
└─────━━━────────────────────┘`},
	}
	// safeHandler internally serializes Resize/Draw/Handle on
	// m.mu, so no manual lock is required here. The default
	// scheduleNextTick stub installed by
	// newTestWorkspaceManagerHandlerWithManagerAndExtensions
	// dispatches scheduled callbacks under m.mu on a fresh
	// goroutine, so any host-scheduled work is already
	// serialized with handler input through the same lock.
	handlertest.RunHandlerSequence(t, h, 30, 9, cases)
	keys, err := term.ParseKeys("<c-\\\\>workspacefocus<space>1<enter>")
	require.NoError(t, err)
	for _, key := range keys {
		ev := term.Event{
			Type: term.EventKey,
			Ch:   key.Ch,
			Mod:  key.Mod,
			Key:  key.Key,
		}
		_, handled := h.Handle(ev)
		require.True(t, handled, "%s", ev.KeyComb().String())
	}
	// Ring the bell in the now-unfocused terminal workspace by
	// injecting BEL into the fake pty output stream.
	ptys.ring(t)
	select {
	case <-bellRung:
	case <-time.After(10 * time.Second):
		t.Fatal("bell never rang after injecting BEL into the pty")
	}
	// SetTabName routes the attention attribute through
	// f.parent.scheduleNextTick, which under the default test stub
	// dispatches on a fresh goroutine. The bell fires from the vte
	// parser path before that scheduled tick runs, so bellRung above
	// only proves the bell was rung — it does not guarantee the
	// attention attr propagated to workspaces[1] yet. Block here
	// until the bar tab actually reflects the attention attr so the
	// final render is deterministic.
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		if len(m.workspaces) < 2 || m.workspaces[1] == nil {
			return false
		}
		return m.workspaces[1].attentionAttr != (term.Attributes{})
	}, 5*time.Second, 5*time.Millisecond,
		"workspace attention attr never propagated after bell")
	cases = []handlertest.SequenceTestCase{
		{InputSequence: "",
			Expected: `┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│     workspaceWallpaper     │
│                            │
├────────────────────────────┤
│1 1  2 2*                   │
└━━━─────────────────────────┘`},
	}
	handlertest.RunHandlerSequence(t, h, 30, 9, cases)
	require.NoError(t, m.Close())
}

func TestCrossWorkspaceNotifications(t *testing.T) {
	dir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(dir)
	})

	uri1, err := workspaceapi.ParseURI("memory://" + dir)
	require.NoError(t, err)
	uri2, err := workspaceapi.ParseURI("memory:///b")
	require.NoError(t, err)

	m := newTestWorkspaceManagerHandlerWithDir(t, defaultConfigWithWrap(false), dir,
		nopShutdownShaderConfig())
	require.NoError(t, m.addOrCreateWorkspace(uri1))
	m.quiesce()
	require.NoError(t, m.addOrCreateWorkspace(uri2))
	m.quiesce()

	m.workspaces[0].notifications.Notify(browserapi.LevelError, "sh")

	h := newSafeHandler(m)
	cases := []handlertest.SequenceTestCase{
		{"",
			`┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│     workspaceWallpaper     │
│                            │
├────────────────────────────┤
│1 #  2 #                    │
└─────###────────────────────┘`},
	}
	handlertest.RunHandlerSequenceWriter(t, newWriterForAttrTesting(30, 9),
		h, 30, 9, cases)

	require.NoError(t, m.Close())
}

func TestCustomLocations(t *testing.T) {
	dir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(dir)
	})

	uri1, err := workspaceapi.ParseURI("memory://" + dir)
	require.NoError(t, err)

	m := newTestWorkspaceManagerHandlerWithDir(t, defaultConfigWithWrap(false), dir,
		nopShutdownShaderConfig())
	require.NoError(t, m.addOrCreateWorkspace(uri1))
	m.quiesce()

	h := newSafeHandler(m)
	cases := []handlertest.SequenceTestCase{
		{":edit dakar.md>igentleman<:locationcreate mylist>a>driver<:locationcreate mylist>a>gentleman<:write>:locationcreate mylist>",
			`┌━━━━━━━━━━──────────────────┐
│o dakar.md                  │
├────────────────────────────┤
│gentleman                   │
│driver                      │
│gentlema▐                   │
│                            │
│                      NORMAL│
└────────────────────────────┘`},
		{":jumptolocation previous mylist>",
			`┌━━━━━━━━━━──────────────────┐
│o dakar.md                  │
├────────────────────────────┤
│gentleman                   │
│drive▐                      │
│gentleman                   │
│                            │
│                      NORMAL│
└────────────────────────────┘`},
		{":jumptolocation previous mylist>",
			`┌━━━━━━━━━━──────────────────┐
│o dakar.md                  │
├────────────────────────────┤
│gentlema▐                   │
│driver                      │
│gentleman                   │
│                            │
│                      NORMAL│
└────────────────────────────┘`},
		{":locationdelete mork>",
			`┌━━━━━━━━━━────┌─────────────┐
│o dakar.md    │ there's no  │
├──────────────│ location    │
│gentlema▐     │ at the      │
│driver        │ given       │
│gentleman     │ cursor      │
│              │ position    │
│              │ for given   │
└──────────────│ location    │`},
		{":noticloseall>:locationdelete mylist>",
			`┌━━━━━━━━━━──────────────────┐
│o dakar.md                  │
├────────────────────────────┤
│gentlema▐                   │
│driver                      │
│gentleman                   │
│                            │
│                      NORMAL│
└────────────────────────────┘`},
		{":jumptolocation next mylist>",
			`┌━━━━━━━━━━──────────────────┐
│o dakar.md                  │
├────────────────────────────┤
│gentleman                   │
│drive▐                      │
│gentleman                   │
│                            │
│                      NORMAL│
└────────────────────────────┘`},
		{":jumptolocation next mylist>",
			`┌━━━━━━━━━━──────────────────┐
│o dakar.md                  │
├────────────────────────────┤
│gentleman                   │
│driver                      │
│gentlema▐                   │
│                            │
│                      NORMAL│
└────────────────────────────┘`},
		{":jumptolocation next mylist>",
			`┌━━━━━━━━━━──────────────────┐
│o dakar.md                  │
├────────────────────────────┤
│gentleman                   │
│drive▐                      │
│gentleman                   │
│                            │
│                      NORMAL│
└────────────────────────────┘`},
		{":locationdeleteall mylist>",
			`┌━━━━━━━━━━──────────────────┐
│o dakar.md                  │
├────────────────────────────┤
│gentleman                   │
│drive▐                      │
│gentleman                   │
│                            │
│                      NORMAL│
└────────────────────────────┘`},
		{":jumptolocation next mylist>",
			`┌━━━━━━━━━━──────────────────┐
│o dakar.md                  │
├────────────────────────────┤
│gentleman                   │
│drive▐                      │
│gentleman                   │
│                            │
│                      NORMAL│
└────────────────────────────┘`},
		{"k:locationtoggle mylist>j:locationtoggle mylist>:jumptolocation next mylist>",
			`┌━━━━━━━━━━──────────────────┐
│o dakar.md                  │
├────────────────────────────┤
│gentl▐man                   │
│driver                      │
│gentleman                   │
│                            │
│                      NORMAL│
└────────────────────────────┘`},
		{":jumptolocation next mylist>",
			`┌━━━━━━━━━━──────────────────┐
│o dakar.md                  │
├────────────────────────────┤
│gentleman                   │
│drive▐                      │
│gentleman                   │
│                            │
│                      NORMAL│
└────────────────────────────┘`},
		{":locationtoggle mylist>:jumptolocation next mylist>",
			`┌━━━━━━━━━━──────────────────┐
│o dakar.md                  │
├────────────────────────────┤
│gentl▐man                   │
│driver                      │
│gentleman                   │
│                            │
│                      NORMAL│
└────────────────────────────┘`},
		{":jumptolocation next mylist>",
			`┌━━━━━━━━━━──────────────────┐
│o dakar.md                  │
├────────────────────────────┤
│gentl▐man                   │
│driver                      │
│gentleman                   │
│                            │
│                      NORMAL│
└────────────────────────────┘`},
	}
	handlertest.TestHandlerSequence(t, h, 30, 9, cases)

	require.NoError(t, m.Close())
}

func TestApostropheMarkJump(t *testing.T) {
	dir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(dir)
	})

	uri1, err := workspaceapi.ParseURI("memory://" + dir)
	require.NoError(t, err)

	m := newTestWorkspaceManagerHandlerWithDir(t, defaultConfigWithWrap(false), dir,
		nopShutdownShaderConfig())
	require.NoError(t, m.addOrCreateWorkspace(uri1))
	m.quiesce()

	h := newSafeHandler(m)
	cases := []handlertest.SequenceTestCase{
		// Create file with leading spaces on a line, set mark at column 6
		{InputSequence: "<c-\\\\>edit<space>test.md<enter>i<space><space><space>hello<enter>world<esc><c-\\\\>write<enter>k$ma",
			Expected: `┌━━━━━━━━━───────────────────┐
│o test.md                   │
├────────────────────────────┤
│   hell▐                    │
│world                       │
│                            │
│                            │
│                      NORMAL│
└────────────────────────────┘`},
		// Move to next line
		{InputSequence: "j",
			Expected: `┌━━━━━━━━━───────────────────┐
│o test.md                   │
├────────────────────────────┤
│   hello                    │
│worl▐                       │
│                            │
│                            │
│                      NORMAL│
└────────────────────────────┘`},
		// 'a jumps to first non-blank character on marked line
		{InputSequence: "'a",
			Expected: `┌━━━━━━━━━───────────────────┐
│o test.md                   │
├────────────────────────────┤
│   ▐ello                    │
│world                       │
│                            │
│                            │
│                      NORMAL│
└────────────────────────────┘`},
		// Move away again
		{InputSequence: "j",
			Expected: `┌━━━━━━━━━───────────────────┐
│o test.md                   │
├────────────────────────────┤
│   hello                    │
│wor▐d                       │
│                            │
│                            │
│                      NORMAL│
└────────────────────────────┘`},
		// `a jumps to exact mark position (column 6)
		{InputSequence: "`a",
			Expected: `┌━━━━━━━━━───────────────────┐
│o test.md                   │
├────────────────────────────┤
│   hell▐                    │
│world                       │
│                            │
│                            │
│                      NORMAL│
└────────────────────────────┘`},
	}
	handlertest.RunHandlerSequence(t, h, 30, 9, cases)

	require.NoError(t, m.Close())
}

func TestOpenFilesinEmptyWorkspace(t *testing.T) {
	m := newTestWorkspaceManagerHandlerWithDir(t, defaultConfigWithWrap(false), "",
		nopShutdownShaderConfig())

	h := newSafeHandler(m)
	cases := []handlertest.SequenceTestCase{
		{"<c-\\\\>edit<space>dakar.md<enter>",
			`┌━━━━━━━━━━──────────────────┐
│o dakar.md                  │
├────────────────────────────┤
│▐                           │
│                            │
│                      NORMAL│
├────────────────────────────┤
│1                           │
└━───────────────────────────┘`},
		{"<c-\\\\>tabclose<enter>",
			`┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│     workspaceWallpaper     │
│                            │
├────────────────────────────┤
│1                           │
└━───────────────────────────┘`},
	}
	handlertest.RunHandlerSequence(t, h, 30, 9, cases)

	require.NoError(t, m.Close())
}

func TestEditFileURIRedirectsToWorkspaceWithOpenFile(t *testing.T) {
	tmp1 := t.TempDir()
	tmp2 := t.TempDir()
	sharedDir := t.TempDir()
	sharedPath := filepath.Join(sharedDir, "shared.txt")
	require.NoError(t, os.WriteFile(sharedPath, []byte("shared"), 0o666))

	cfg := defaultConfigWithWrap(false)
	m := newTestWorkspaceManagerHandlerWithDir(t, cfg, tmp1, nopShutdownShaderConfig())
	defer func() {
		require.NoError(t, m.Close())
	}()

	uri2, err := workspaceapi.ParseURI("file://" + tmp2)
	require.NoError(t, err)
	require.NoError(t, m.addOrCreateWorkspace(uri2))
	m.quiesce()

	sharedURI, err := workspaceapi.ParseURI("file://" + sharedPath)
	require.NoError(t, err)

	openTab, err := m.workspaces[1].ex.editFileURI(
		sharedURI, m.workspaces[1].ex.invokeWindow(), false)
	require.NoError(t, err)
	m.workspaces[1].ex.Wait()

	require.True(t, m.switchToWorkspace(0))
	redirectedTab, err := m.workspaces[0].ex.editFileURI(
		sharedURI, m.workspaces[0].ex.invokeWindow(), false)
	require.NoError(t, err)
	m.workspaces[0].ex.Wait()
	m.workspaces[1].ex.Wait()

	assert.Equal(t, 1, m.focus)
	assert.Same(t, openTab, redirectedTab)
	assert.Empty(t, m.workspaces[0].ex.comp.Tabs())

	focusTab, ok := m.workspaces[1].ex.comp.FocusTab()
	require.True(t, ok)
	assert.Same(t, openTab, focusTab)
	assert.True(t, focusTab.URI().Equal(sharedURI))
	assert.Len(t, m.workspaces[1].ex.comp.Tabs(), 1)
	_, ok = redirectedTab.Window()
	assert.True(t, ok)
}

// TestOpenPrevSessionFilesSkipsNonTextHandler is a regression test for
// the crash where restoring a previous session containing a markdown
// tab (whose handler is *handlermarkdown.Handler, not a text.Handler)
// panicked in openPrevSessionFiles via an unchecked type assertion.
func TestOpenPrevSessionFilesSkipsNonTextHandler(t *testing.T) {
	tmp := t.TempDir()
	mdPath := filepath.Join(tmp, "README.md")
	require.NoError(t, os.WriteFile(mdPath, []byte("# hi\n"), 0o666))

	cfg := defaultConfigWithWrap(false)
	m := newTestWorkspaceManagerHandlerWithDir(t, cfg, "", nopShutdownShaderConfig())
	defer func() {
		require.NoError(t, m.Close())
	}()

	uri, err := workspaceapi.ParseURI("file://" + tmp)
	require.NoError(t, err)
	require.NoError(t, m.addOrCreateWorkspace(uri))
	m.quiesce()

	ex := m.workspaces[0].ex
	mdURI, err := workspaceapi.ParseURI("file://" + mdPath)
	require.NoError(t, err)

	// Open the markdown file read-only to install a tab whose handler
	// is *handlermarkdown.Handler (this is what :view README.md does).
	mdTab, err := ex.editFileURI(mdURI, ex.invokeWindow(), true)
	require.NoError(t, err)
	ex.Wait()
	_, ok := mdTab.Handler().(*handlermarkdown.Handler)
	require.True(t, ok, "markdown view tab must use *handlermarkdown.Handler")

	state := idehistory.State{
		Files: []idehistory.File{{
			URI:    mdURI,
			Cursor: term.Coordinates{X: 1, Y: 2},
		}},
	}

	// On main this panics:
	//   interface conversion: *markdown.Handler is not text.Handler:
	//       missing method CellEditor
	require.NotPanics(t, func() {
		err = m.openPrevSessionFiles(ex, state.Files, nil)
	})
	require.NoError(t, err)

	tabs := ex.comp.Tabs()
	var foundMD bool
	for _, tab := range tabs {
		if !tab.URI().Equal(mdURI) {
			continue
		}
		_, ok := tab.Handler().(*handlermarkdown.Handler)
		assert.True(t, ok, "markdown tab handler should remain *handlermarkdown.Handler")
		foundMD = true
	}
	assert.True(t, foundMD, "expected to find markdown tab after session restore")
}

func TestOpenExoEditDelegatesMarkdown(t *testing.T) {
	assertExoDelegatesMarkdown(t, "README.md", false)
}

func TestOpenExoViewDelegatesMarkdown(t *testing.T) {
	assertExoDelegatesMarkdown(t, "NOTES.md", true)
}

func assertExoDelegatesMarkdown(t *testing.T, fileName string, readOnly bool) {
	if _, err := exec.LookPath("vim"); err != nil {
		t.Skip("vim binary not available")
	}

	dir := t.TempDir()
	canonical, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	dir = canonical

	filePath := filepath.Join(dir, fileName)
	require.NoError(t, os.WriteFile(filePath, []byte("# hi\n"), 0o644))

	cfg := defaultConfigWithWrap(false)
	editorCfg := cfg.cfg["editor"].(map[string]any)
	editorCfg["mode"] = "exo"
	editorCfg["exo"] = map[string]any{
		"command": `vim -Nu NONE -n {file}`,
		"goto":    "<esc>:{line}<enter>{col}|",
	}
	cfg.ringBell = func() {}
	require.Equal(t, "exo", cfg.editorMode())

	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	m := newTestWorkspaceManagerHandlerWithDir(t, cfg, dir,
		nopShutdownShaderConfig())
	t.Cleanup(func() { _ = m.Close() })

	require.NoError(t, m.addOrCreateWorkspace(uri))
	m.quiesce()

	h := newSafeHandler(m)
	h.Resize(80, 24)

	dispatch := func(evs ...term.Event) {
		for _, ev := range evs {
			_, _ = h.Handle(ev)
		}
	}
	cmd := "edit"
	if readOnly {
		cmd = "view"
	}
	openSeq, err := term.ParseKeys(
		`<c-\\>` + cmd + `<space>` + fileName + `<enter>`)
	require.NoError(t, err)
	for _, k := range openSeq {
		dispatch(keyEvent(k))
	}

	ex := m.workspaces[m.focus].ex
	mdURI, err := workspaceapi.ParseURI("file://" + filePath)
	require.NoError(t, err)

	res, ok := ex.comp.Resource(mdURI)
	require.True(t, ok, "tab for %q must exist after :%s", fileName, cmd)
	tab, ok := res.(*browser.Tab)
	require.True(t, ok, "tab must be *browser.Tab")
	_, isMarkdown := tab.Handler().(*handlermarkdown.Handler)
	assert.False(t, isMarkdown,
		"under editor.mode=exo, .md must delegate to the exo "+
			"handler, not the built-in markdown viewer "+
			"(readOnly=%v)", readOnly)
}

func TestReadfileCrossWorkspaceIntegration(t *testing.T) {
	tmp1 := t.TempDir()
	tmp2 := t.TempDir()

	currentPath := filepath.Join(tmp1, "current.txt")
	require.NoError(t, os.WriteFile(currentPath, []byte("current\n"), 0o666))
	foreignPath := filepath.Join(tmp2, "foreign.txt")
	require.NoError(t, os.WriteFile(foreignPath, []byte("foreign contents"), 0o666))

	cfg := defaultConfigWithWrap(false)
	m := newTestWorkspaceManagerHandlerWithDir(t, cfg, "", nopShutdownShaderConfig())
	defer func() {
		require.NoError(t, m.Close())
	}()

	uri1, err := workspaceapi.ParseURI("file://" + tmp1)
	require.NoError(t, err)
	require.NoError(t, m.addOrCreateWorkspace(uri1))
	m.quiesce()

	uri2, err := workspaceapi.ParseURI("file://" + tmp2)
	require.NoError(t, err)
	require.NoError(t, m.addOrCreateWorkspace(uri2))
	m.quiesce()

	currentURI, err := workspaceapi.ParseURI("file://" + currentPath)
	require.NoError(t, err)
	m.locked(func() {
		require.True(t, m.switchToWorkspace(0))
		_, err = m.workspaces[0].ex.editFileURI(
			currentURI, m.workspaces[0].ex.invokeWindow(), false)
	})
	require.NoError(t, err)
	m.workspaces[0].ex.Wait()

	foreignURI, err := workspaceapi.ParseURI("file://" + foreignPath)
	require.NoError(t, err)
	m.locked(func() {
		err = m.workspaces[0].ex.readfile(context.Background(), foreignURI.String())
	})
	require.NoError(t, err)
	m.workspaces[0].ex.Wait()

	m.locked(func() {
		assert.Equal(t, 0, m.focus)
		_, h, ok := m.workspaces[0].ex.handlerInFocus()
		require.True(t, ok)
		assert.Equal(t, "current\nforeign contents",
			term.CellsToString(h.CellView().RawCells()))
		assert.Empty(t, m.workspaces[1].ex.comp.Tabs())
		_, ok = m.workspaces[1].ex.comp.FocusTab()
		assert.False(t, ok)
	})
}

func TestCrossWorkspaceOpenRoutingIntegration(t *testing.T) {
	type integrationCase struct {
		name           string
		startWorkspace int
		// seed writes fixture files before the workspaces exist.
		// Creating them afterwards would land inside a live
		// fs-watcher and race the frame assertions below with a
		// "changed on disk" reload notification.
		seed      func(t *testing.T, tmp1, tmp2 string)
		setup     func(t *testing.T, m *testWorkspaceManagerHandler, tmp1, tmp2 string)
		sequences []handlertest.SequenceTestCase
	}

	newManager := func(t *testing.T, tmp1, tmp2 string) *testWorkspaceManagerHandler {
		cfg := defaultConfigWithWrap(false)
		cfg.cfg["browser"] = map[string]any{
			"workspace_bar":      "number",
			"focus_tab_attr":     map[string]any{"bg": "blue"},
			"non_focus_tab_attr": map[string]any{"bg": "default"},
			"window_manager": map[string]any{
				"no_max_size": false,
			},
		}
		m := newTestWorkspaceManagerHandlerWithDir(t, cfg, "", nopShutdownShaderConfig())

		uri1, err := workspaceapi.ParseURI("file://" + tmp1)
		require.NoError(t, err)
		require.NoError(t, m.addOrCreateWorkspace(uri1))
		m.quiesce()

		uri2, err := workspaceapi.ParseURI("file://" + tmp2)
		require.NoError(t, err)
		require.NoError(t, m.addOrCreateWorkspace(uri2))
		m.quiesce()

		return m
	}

	setAliases := func(m *testWorkspaceManagerHandler, aliases map[string]text.CommandAlias) {
		m.locked(func() {
			for _, wh := range m.workspaces {
				if wh == nil || wh.ex == nil {
					continue
				}
				if wh.ex.config.CommandAliases == nil {
					wh.ex.config.CommandAliases = make(map[string]text.CommandAlias)
				}
				maps.Copy(wh.ex.config.CommandAliases, aliases)
			}
		})
	}

	tests := []integrationCase{
		{
			name:           "local file stays in current workspace",
			startWorkspace: 0,
			seed: func(t *testing.T, tmp1, tmp2 string) {
				require.NoError(t, os.WriteFile(filepath.Join(tmp1, "local.go"), []byte("package main\n"), 0o666))
			},
			setup: func(t *testing.T, m *testWorkspaceManagerHandler, tmp1, tmp2 string) {
				setAliases(m, map[string]text.CommandAlias{
					"openCase": {Commands: []string{"edit local.go"}},
					"to1":      {Commands: []string{"workspacefocus 1"}},
					"to2":      {Commands: []string{"workspacefocus 2"}},
				})
			},
			sequences: []handlertest.SequenceTestCase{
				{
					InputSequence: "<c-\\\\>openCase<enter>",
					Expected: `┌━━━━━━━━━━──────────────────┐
│o ········                  │
├────────────────────────────┤
│▐ackage main                │
│                            │
│                      NORMAL│
├────────────────────────────┤
│1 ·  2 2                    │
└━━━─────────────────────────┘`,
				},
				{
					InputSequence: "<c-\\\\>to2<enter>",
					Expected: `┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│     workspaceWallpaper     │
│                            │
├────────────────────────────┤
│1 1  2 ·                    │
└─────━━━────────────────────┘`,
				},
				{
					InputSequence: "<c-\\\\>to1<enter>",
					Expected: `┌━━━━━━━━━━──────────────────┐
│o ········                  │
├────────────────────────────┤
│▐ackage main                │
│                            │
│                      NORMAL│
├────────────────────────────┤
│1 ·  2 2                    │
└━━━─────────────────────────┘`,
				},
			},
		},
		{
			name:           "unopened foreign file switches to owning workspace",
			startWorkspace: 0,
			seed: func(t *testing.T, tmp1, tmp2 string) {
				ownedPath := filepath.Join(tmp2, "owned.go")
				require.NoError(t, os.WriteFile(ownedPath, []byte("package main\n"), 0o666))
			},
			setup: func(t *testing.T, m *testWorkspaceManagerHandler, tmp1, tmp2 string) {
				ownedPath := filepath.Join(tmp2, "owned.go")
				setAliases(m, map[string]text.CommandAlias{
					"openCase": {Commands: []string{fmt.Sprintf("edit file://%s", ownedPath)}},
					"to1":      {Commands: []string{"workspacefocus 1"}},
					"to2":      {Commands: []string{"workspacefocus 2"}},
				})
			},
			sequences: []handlertest.SequenceTestCase{
				{
					InputSequence: "<c-\\\\>openCase<enter>",
					Expected: `┌━━━━━━━━━━──────────────────┐
│o ········                  │
├────────────────────────────┤
│▐ackage main                │
│                            │
│                      NORMAL│
├────────────────────────────┤
│1 1  2 ·                    │
└─────━━━────────────────────┘`,
				},
				{
					InputSequence: "<c-\\\\>to1<enter>",
					Expected: `┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│     workspaceWallpaper     │
│                            │
├────────────────────────────┤
│1 ·  2 2                    │
└━━━─────────────────────────┘`,
				},
				{
					InputSequence: "<c-\\\\>to2<enter>",
					Expected: `┌━━━━━━━━━━──────────────────┐
│o ········                  │
├────────────────────────────┤
│▐ackage main                │
│                            │
│                      NORMAL│
├────────────────────────────┤
│1 1  2 ·                    │
└─────━━━────────────────────┘`,
				},
			},
		},
		{
			name:           "existing foreign tab is focused in owning workspace",
			startWorkspace: 0,
			setup: func(t *testing.T, m *testWorkspaceManagerHandler, tmp1, tmp2 string) {
				sharedPath := filepath.Join(t.TempDir(), "shared.go")
				require.NoError(t, os.WriteFile(sharedPath, []byte("package main\n"), 0o666))
				sharedURI, err := workspaceapi.ParseURI("file://" + sharedPath)
				require.NoError(t, err)
				m.locked(func() {
					_, err = m.workspaces[1].ex.editFileURI(
						sharedURI, m.workspaces[1].ex.invokeWindow(), false)
				})
				require.NoError(t, err)
				m.workspaces[1].ex.Wait()
				setAliases(m, map[string]text.CommandAlias{
					"openCase": {Commands: []string{fmt.Sprintf("edit file://%s", sharedPath)}},
					"to1":      {Commands: []string{"workspacefocus 1"}},
					"to2":      {Commands: []string{"workspacefocus 2"}},
				})
			},
			sequences: []handlertest.SequenceTestCase{
				{
					InputSequence: "<c-\\\\>openCase<enter>",
					Expected: `┌━━━━━━━━━━━─────────────────┐
│o ·········                 │
├────────────────────────────┤
│▐ackage main                │
│                            │
│                      NORMAL│
├────────────────────────────┤
│1 1  2 ·                    │
└─────━━━────────────────────┘`,
				},
				{
					InputSequence: "<c-\\\\>to1<enter>",
					Expected: `┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│     workspaceWallpaper     │
│                            │
├────────────────────────────┤
│1 ·  2 2                    │
└━━━─────────────────────────┘`,
				},
				{
					InputSequence: "<c-\\\\>to2<enter>",
					Expected: `┌━━━━━━━━━━━─────────────────┐
│o ·········                 │
├────────────────────────────┤
│▐ackage main                │
│                            │
│                      NORMAL│
├────────────────────────────┤
│1 1  2 ·                    │
└─────━━━────────────────────┘`,
				},
			},
		},
		{
			name:           "focused owner opens locally without reroute",
			startWorkspace: 1,
			seed: func(t *testing.T, tmp1, tmp2 string) {
				ownedPath := filepath.Join(tmp2, "self.go")
				require.NoError(t, os.WriteFile(ownedPath, []byte("package main\n"), 0o666))
			},
			setup: func(t *testing.T, m *testWorkspaceManagerHandler, tmp1, tmp2 string) {
				ownedPath := filepath.Join(tmp2, "self.go")
				setAliases(m, map[string]text.CommandAlias{
					"openCase": {Commands: []string{fmt.Sprintf("edit file://%s", ownedPath)}},
					"to1":      {Commands: []string{"workspacefocus 1"}},
					"to2":      {Commands: []string{"workspacefocus 2"}},
				})
			},
			sequences: []handlertest.SequenceTestCase{
				{
					InputSequence: "<c-\\\\>openCase<enter>",
					Expected: `┌━━━━━━━━━───────────────────┐
│o ·······                   │
├────────────────────────────┤
│▐ackage main                │
│                            │
│                      NORMAL│
├────────────────────────────┤
│1 1  2 ·                    │
└─────━━━────────────────────┘`,
				},
				{
					InputSequence: "<c-\\\\>to1<enter>",
					Expected: `┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│     workspaceWallpaper     │
│                            │
├────────────────────────────┤
│1 ·  2 2                    │
└━━━─────────────────────────┘`,
				},
				{
					InputSequence: "<c-\\\\>to2<enter>",
					Expected: `┌━━━━━━━━━───────────────────┐
│o ·······                   │
├────────────────────────────┤
│▐ackage main                │
│                            │
│                      NORMAL│
├────────────────────────────┤
│1 1  2 ·                    │
└─────━━━────────────────────┘`,
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmp1 := t.TempDir()
			tmp2 := t.TempDir()
			if tc.seed != nil {
				tc.seed(t, tmp1, tmp2)
			}
			m := newManager(t, tmp1, tmp2)
			t.Cleanup(func() {
				require.NoError(t, m.Close())
			})
			tc.setup(t, m, tmp1, tmp2)
			require.True(t, m.switchToWorkspace(tc.startWorkspace))

			h := newSafeHandler(m)
			writer := term.NewStringWriter(30, 9)
			writer.BackgroundCh = '·'
			handlertest.RunHandlerSequenceWriter(t, writer, h, 30, 9, tc.sequences)
		})
	}
}

func TestWorkspaceConfig(t *testing.T) {
	mockConfig := map[string]any{
		"1": "2",
		"2": map[string]any{
			"dos": "2",
			"two": "2",
		},
	}
	uri, err := workspaceapi.ParseURI("memory:///tmp")
	require.NoError(t, err)

	t.Run("passes default scheme config to SchemeFunc", func(t *testing.T) {
		cfg := defaultCfg()
		mu, sched, drain := buildTestSchedulerForCfg(t, &cfg)
		manager := workspace.NewManager(cfg.workspace(), sched)
		workspaceConfig := cfg.cfg["workspace"].(map[string]any)
		workspaceConfig[workspace.MemoryScheme] = mockConfig

		passed := make(map[string]any)
		manager.RegisterScheme(workspace.MemoryScheme,
			func(ctx context.Context, cfg config.Config, uri workspaceapi.URI) (
				schemeapi.Scheme, error,
			) {
				cfg.Iterate(func(k string, v any) {
					passed[k] = v
				})
				return workspace.NewMemoryScheme(ctx, cfg, uri)
			})

		m := newTestWorkspaceManagerHandlerWithManager(t, manager, mu, drain, uri, cfg)
		defer m.Close()

		assert.EqualValues(t, mockConfig, passed)
	})

	t.Run("notifies user if config decode fails but does not hard error", func(t *testing.T) {
		cfg := defaultCfg()
		mu, sched, drain := buildTestSchedulerForCfg(t, &cfg)
		manager := workspace.NewManager(cfg.workspace(), sched)
		workspaceConfig := cfg.cfg["workspace"].(map[string]any)
		workspaceConfig[workspace.MemoryScheme] = mockConfig

		passed := make(map[string]any)
		manager.RegisterScheme(workspace.MemoryScheme,
			func(ctx context.Context, cfg config.Config, uri workspaceapi.URI) (
				schemeapi.Scheme, error,
			) {
				cfg.Iterate(func(k string, v any) {
					passed[k] = v
				})
				return workspace.NewMemoryScheme(ctx, cfg, uri)
			})

		homeURI, err := workspaceapi.ParseURI("memory:///home")
		require.NoError(t, err)

		dir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = os.RemoveAll(dir)
		})

		runner := FuncExtensionsRunner(testRunnerFn)

		m := new(testWorkspaceManagerHandler)
		m.workspaceManagerHandler = new(workspaceManagerHandler)

		releaseManager := docrelease.NewManager(document.NewInMemoryService())
		shRunner := new(shaderRunner)
		shRunner.init(
			handler.Nop(), term.NopInterrupter(), term.Attributes{},
			nopShutdownShaderConfig(), loadingShaderConfig{}, openShaderConfig{},
			component.FrameCharSetDefault())
		storage := localstorage.New(context.Background(), dir, docbson.Marshaler())
		m.tutorialsInstalled = func([]string) (bool, error) { return false, nil }
		err = m.workspaceManagerHandler.init(&uri, homeURI, manager,
			notificationsConfig(), cfg, storage, dir,
			func(term.Event) bool {
				return true
			}, runner, pkgtrust.NewStore(dir, nil), mu, nil,
			func() (ideConfig, error) { return cfg, errors.New("boom") },
			".sixrc", 0, 0, 0, '1', 0, 0, true, nil, releaseManager, shRunner, 0, nil, false,
			false, newCommandObserverRegistry())
		require.NoError(t, err)
		defer m.Close()
		m.schedDrain = drain
		m.quiesce()

		assert.EqualValues(t, mockConfig, passed)

		cases := []handlertest.SequenceTestCase{
			{"",
				`┌────┌─────────────┐
│    │ Config      │
├────│ decode      │
│    │ error: boom │
│    └─────────────┘
│workspaceWallpaper│
│                  │
│                  │
│                  │
└──────────────────┘`},
		}
		h := &safeHandler{Component: m, Handler: m, mu: m.mu,
			quiesce: m.quiesce}
		handlertest.TestHandlerSequence(t, h, 20, 10, cases)
	})

	t.Run("does not reload workspace config", func(t *testing.T) {
		cfg := defaultCfg()
		mu, sched, drain := buildTestSchedulerForCfg(t, &cfg)
		manager := workspace.NewManager(cfg.workspace(), sched)
		workspaceConfig := cfg.cfg["workspace"].(map[string]any)
		workspaceConfig[workspace.MemoryScheme] = mockConfig

		passed := make(map[string]any)
		manager.RegisterScheme(workspace.MemoryScheme,
			func(ctx context.Context, cfg config.Config, uri workspaceapi.URI) (
				schemeapi.Scheme, error,
			) {
				cfg.Iterate(func(k string, v any) {
					passed[k] = v
				})
				return workspace.NewMemoryScheme(ctx, cfg, uri)
			})

		m := newTestWorkspaceManagerHandlerWithManager(t, manager, mu, drain, uri, cfg)
		assert.EqualValues(t, mockConfig, passed)

		m.reloadConfig = func() (ideConfig, error) {
			cfg := defaultCfg()
			cfg.cfg["workspace"].(map[string]any)["1"] = "!!!!"
			return cfg, nil
		}

		require.Equal(t, 0, m.height)
		require.Equal(t, 0, m.width)
		m.mu.Lock()
		require.NoError(t, m.commandReloadWorkspace())
		m.mu.Unlock()
		m.quiesce()
		assert.EqualValues(t, mockConfig, passed)

		require.NoError(t, m.Close())
	})
}

func TestWorkspaceExtensions(t *testing.T) {
	t.Run("calls extension runner with user extensions", func(t *testing.T) {
		cfg := defaultCfg()
		cfg.cfg = map[string]any{
			"command":            map[string]any{},
			"show_manual":        false,
			"show_progress_hint": false,
			"extensions": map[string]any{
				"git": map[string]any{
					"path": "myPath",
					"config": map[string]any{
						"a": "b",
					},
				},
			},
		}
		mu, sched, drain := buildTestSchedulerForCfg(t, &cfg)
		manager := workspace.NewManager(cfg.workspace(), sched)

		manager.RegisterScheme(workspace.MemoryScheme, workspace.NewMemoryScheme)

		uri, err := workspaceapi.ParseURI("memory:///tmp")
		require.NoError(t, err)

		// extension.Runner.Run is called asynchronously
		dir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = os.RemoveAll(dir)
		})
		var uris [2]string
		var i atomic.Int32
		var wg sync.WaitGroup
		runner := FuncExtensionsRunner(
			func(_uri workspaceapi.URI,
				res map[extensionapi.Permission]extension.ResourceRegistrar,
				s, _ string, noti browser.Notifications,
				exec, extExec schemeapi.Executor,
				grantor extension.Grantor,
				editor text.Editor,
				promptOpener ideauthorizer.PromptOpener, storage storageapi.Service,
				scheduleNextTick func(func()) bool,
			) (extension.Runner, error) {
				defer wg.Done()
				assert.NotNil(t, res)
				assert.Equal(t, dir, s)
				assert.NotNil(t, noti)
				assert.NotNil(t, exec)
				assert.NotNil(t, grantor)
				assert.NotNil(t, promptOpener)
				assert.NotNil(t, storage)
				assert.NotNil(t, scheduleNextTick)
				i := i.Add(1)
				uris[i-1] = _uri.String()
				return fnRunner{fn: func(extensionID, path string, cfg config.Config) error {
					assert.Equal(t, "myPath", path)
					assert.Equal(t, "git", extensionID)
					assert.Equal(t, config.MapConfig(map[string]any{"a": "b"}), cfg)
					return nil
				},
				}, nil
			})
		wg.Add(2)
		m := newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
			&uri, cfg, runner, nil, dir, nil, nopShutdownShaderConfig())
		defer m.Close()

		wg.Wait()
		assert.ElementsMatch(t, []string{"memory:///home", "memory:///tmp"}, uris)
	})

	t.Run("calls extension runner with built-in extensions", func(t *testing.T) {
		dir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = os.RemoveAll(dir)
		})
		cfg := defaultCfg()
		cfg.cfg = map[string]any{}
		mu, sched, drain := buildTestSchedulerForCfg(t, &cfg)
		manager := workspace.NewManager(cfg.workspace(), sched)

		manager.RegisterScheme(workspace.MemoryScheme, workspace.NewMemoryScheme)

		uri, err := workspaceapi.ParseURI("memory:///tmp")
		require.NoError(t, err)

		extensions := map[string]Extension{
			"myID": {
				ID:         "myID",
				CmdAndArgs: "myPath2",
				Config:     config.MapConfig(map[string]any{"a": "b"}),
			},
		}

		// extension.Runner.Run is called asynchronously
		var uris [2]string
		var wg sync.WaitGroup
		var i atomic.Int32
		runner := FuncExtensionsRunner(
			func(_uri workspaceapi.URI,
				res map[extensionapi.Permission]extension.ResourceRegistrar,
				s, _ string, noti browser.Notifications,
				executor, extExec schemeapi.Executor,
				grantor extension.Grantor,
				editor text.Editor,
				promptOpener ideauthorizer.PromptOpener, storage storageapi.Service,
				scheduleNextTick func(func()) bool) (extension.Runner, error) {
				defer wg.Done()
				assert.NotNil(t, res)
				assert.Equal(t, dir, s)
				assert.NotNil(t, noti)
				assert.NotNil(t, executor)
				assert.NotNil(t, grantor)
				assert.NotNil(t, promptOpener)
				assert.NotNil(t, storage)
				assert.NotNil(t, scheduleNextTick)
				i := i.Add(1)
				uris[i-1] = _uri.String()
				return fnRunner{fn: func(extensionID, path string, cfg config.Config) error {
					assert.Equal(t, "myID", extensionID)
					assert.Equal(t, "myPath2", path)
					assert.Equal(t, config.MapConfig(map[string]any{"a": "b"}), cfg)
					return nil
				},
				}, nil
			})
		wg.Add(2)
		m := newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
			&uri, cfg, runner, extensions, dir, nil, nopShutdownShaderConfig())
		defer m.Close()

		wg.Wait()
		assert.ElementsMatch(t, []string{"memory:///home", "memory:///tmp"}, uris)
	})
}

func TestWorkspaceManagerHandlerDraw(t *testing.T) {
	fn := func(t *testing.T) tui.Handler {
		m := newTestWorkspaceManagerHandler(t, defaultCfg(), nil, nopShutdownShaderConfig())
		t.Cleanup(func() { m.Close() })
		h := newSafeHandler(m)
		return h
	}

	cases := []handlertest.SequenceTestCase{
		{"",
			`┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│                  │
│workspaceWallpaper│
│                  │
│                  │
│                  │
└──────────────────┘`},
		{":edit",
			`┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│                  │
│workspaceWallpaper│
┌──────────────────┐
│ edit▐            │
│                  │
└──────────────────┘`},
		{":edit /tmp/12345aZZ>ihello<yyp",
			`┌━━━━━━━━━━━───────┐
│o 12345aZZ*       │
├──────────────────┤
│hell▐             │
│hello             │
│                  │
│                  │
│                  │
│            NORMAL│
└──────────────────┘`},
		{":edit /tmp/12345aZZ>ihello<yyp:workspacerelo>", // un-saved
			`┌━━━━━━━━━━────────┐
│o 12345aZZ        │
├──────────────────┤
│▐                 │
│                  │
│                  │
│                  │
│                  │
│            NORMAL│
└──────────────────┘`},
		// RUNE-180: reload is now a true close+open, so a memory://
		// scheme's buffer disappears with the scheme. The tab is
		// restored from session state but the buffer is empty.
		{":edit /tmp/12345aZZ>ihello<yyp:w>:workspacerelo>", // saved (mem://)
			`┌━━━━━━━━━━────────┐
│o 12345aZZ        │
├──────────────────┤
│▐                 │
│                  │
│                  │
│                  │
│                  │
│            NORMAL│
└──────────────────┘`},
		{":edit memory\\:///12345aZZ>ihello<yyp:w>:workspacerelo>", // full uri
			`┌━━━━━━━━━━────────┐
│o 12345aZZ        │
├──────────────────┤
│▐                 │
│                  │
│                  │
│                  │
│                  │
│            NORMAL│
└──────────────────┘`},
		{":woc>:wope memory\\:///tmp2>:edit 12345aZZ>:w>:woc>:wope  memory\\:///tmp2>:noticloseall>", // prompt
			`┌──────────────────┐
│                  │
├●█████████████████┤
│                  │
│  Do you want to  │
│  restore the     │
│  previous        │
│                  │
│  Yes        No   │
└──────────────────┘`},
		// prompt resets cache (use file scheme to avoid needing
		// to use ':' to indicate memory scheme)
		{":woc>:wope memory\\:///tmp2>:edit 12345aZZ>:w>:woc>:wope  memory\\:///tmp2>y:noticloseall>",
			`┌━━━━━━━━━━────────┐
│o 12345aZZ        │
├──────────────────┤
│▐                 │
│                  │
│                  │
│                  │
│                  │
│            NORMAL│
└──────────────────┘`},
		{":woc>:wope memory\\:///tmp2>edit 12345aZZ>:w>:woc>:wope  memory\\:///tmp2>n:woc>:wope  memory\\:///tmp2>:noticloseall>", // prompt no: resets cache
			`┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│                  │
│workspaceWallpaper│
│                  │
│                  │
│                  │
└──────────────────┘`},
		{":wof 3>",
			`┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│workspaceWallpaper│
│                  │
│                  │
├──────────────────┤
│1 1  3            │
└─────━────────────┘`},
		{":woc>",
			`┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│workspaceWallpaper│
│                  │
│                  │
├──────────────────┤
│1                 │
└━─────────────────┘`},
		{":q!>",
			`┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│                  │
│workspaceWallpaper│
│                  │
│                  │
│                  │
└──────────────────┘`},
		{":woc>:woc>",
			`┌────┌─────────────┐
│    │ workspace   │
├────│ tab is      │
│    │ empty       │
│work└─────────────┘
│                  │
│                  │
├──────────────────┤
│1                 │
└━─────────────────┘`},
		{":wofo 100>",
			`┌────┌─────────────┐
│    │ invalid     │
├────│ workspace:  │
│    │ there's     │
│    │ only 9      │
│work│ workspaces  │
│    └─────────────┘
│                  │
│                  │
└──────────────────┘`},
		{":addBlaBla>", // workspaceopen should work on a workspace, use next avail
			`┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│workspaceWallpaper│
│                  │
│                  │
├──────────────────┤
│1 1  2 2          │
└─────━━━──────────┘`},
		{"123456789",
			`┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│workspaceWallpaper│
│                  │
│                  │
├──────────────────┤
│1 1  9            │
└─────━────────────┘`},
		{"2:wope>", // errors when no workspace path argument is given
			`┌────┌─────────────┐
│    │ expected    │
├────│ at least    │
│    │ one         │
│work│ argument    │
│    │ with the    │
│    │ workspace   │
├────│ path        ┤
│1 1  2            │
└─────━────────────┘`},
		{"2:wofo>",
			`┌────┌─────────────┐
│    │ invalid     │
├────│ arguments.  │
│    │ Expecting   │
│work│ 1 argument  │
│    │ with        │
│    │ workspace   │
├────│ number      ┤
│1 1  2            │
└─────━────────────┘`},
		{":wofo 3>:addBlaBla>",
			`┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│workspaceWallpaper│
│                  │
│                  │
├──────────────────┤
│1 1  3 3          │
└─────━━━──────────┘`},
		{":wofo 4>:workspaceopen memory\\:///>", // can give path as arg to workspaceopen
			`┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│workspaceWallpaper│
│                  │
│                  │
├──────────────────┤
│1 1  4 4          │
└─────━━━──────────┘`},
		{":wofo 4>:workspaceopen memory\\:///tmp2>:edit memory\\:///tmp2/12>:workspacerelo>", // reloads non-primary workspace
			`┌━━━━──────────────┐
│o 12              │
├──────────────────┤
│▐                 │
│                  │
│                  │
│            NORMAL│
├──────────────────┤
│1 1  4 4          │
└─────━━━──────────┘`},
		{":workspacerename bla>:wofo 4>",
			`┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│workspaceWallpaper│
│                  │
│                  │
├──────────────────┤
│1 bla  4          │
└───────━──────────┘`},
	}
	handlertest.TestHandlerIsolated(t, fn, 20, 10, cases)
}

func TestWorkspaceManagerClosePromptIntegration(t *testing.T) {
	t.Run("prompts on quit if files are clean, user continues", func(t *testing.T) {
		m := newTestWorkspaceManagerHandler(t, defaultCfg(), []string{}, nopShutdownShaderConfig())

		cases := []handlertest.SequenceTestCase{
			{":quit>",
				`┌──────────────────┐
│                  │
├●█████████████████┤
│                  │
│  Are you sure    │
│  you want to     │
│  exit?           │
│                  │
│  Yes        No   │
└──────────────────┘`},
		}
		h := newSafeHandler(m)
		handlertest.TestHandlerSequence(t, h, 20, 10, cases)

		m.mu.Lock()
		exit, handled := m.Handle(term.Event{Type: term.EventKey, Ch: 'y'})
		assert.True(t, exit)
		assert.True(t, handled)
		m.mu.Unlock()

		require.NoError(t, m.Close())
	})

	t.Run("single prompt when running quite more than once", func(t *testing.T) {
		m := newTestWorkspaceManagerHandler(t, defaultCfg(), []string{}, nopShutdownShaderConfig())

		cases := []handlertest.SequenceTestCase{
			{":quit>",
				`┌──────────────────┐
│                  │
├●█████████████████┤
│                  │
│  Are you sure    │
│  you want to     │
│  exit?           │
│                  │
│  Yes        No   │
└──────────────────┘`},
			{":quit>",
				`┌──────────────────┐
│                  │
├●█████████████████┤
│                  │
│  Are you sure    │
│  you want to     │
│  exit?           │
│                  │
│  Yes        No   │
└──────────────────┘`},
		}
		h := newSafeHandler(m)
		handlertest.TestHandlerSequence(t, h, 20, 10, cases)
		exit, _ := h.Handle(term.Event{Type: term.EventKey, Ch: '3'})
		assert.True(t, exit)
		require.NoError(t, m.Close())
	})

	t.Run("open prompt, close it, open again", func(t *testing.T) {
		m := newTestWorkspaceManagerHandler(t, defaultCfg(), []string{}, nopShutdownShaderConfig())

		cases := []handlertest.SequenceTestCase{
			{":quit>",
				`┌──────────────────┐
│                  │
├●█████████████████┤
│                  │
│  Are you sure    │
│  you want to     │
│  exit?           │
│                  │
│  Yes        No   │
└──────────────────┘`},
			{"n",
				`┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│                  │
│workspaceWallpaper│
│                  │
│                  │
│                  │
└──────────────────┘`},
			{":quit>",
				`┌──────────────────┐
│                  │
├●█████████████████┤
│                  │
│  Are you sure    │
│  you want to     │
│  exit?           │
│                  │
│  Yes        No   │
└──────────────────┘`},
		}
		h := newSafeHandler(m)
		handlertest.TestHandlerSequence(t, h, 20, 10, cases)
		require.NoError(t, m.Close())
	})

	t.Run("prompts on quit if files are dirty, user continues", func(t *testing.T) {
		dir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = os.RemoveAll(dir)
		})
		m := newTestWorkspaceManagerHandlerWithDir(t, defaultCfg(), dir, nopShutdownShaderConfig())
		require.NoError(t, m.openFile("1234", true))
		require.NoError(t, m.openFile("4567", false))

		cases := []handlertest.SequenceTestCase{
			{"ihola <",
				`┌━━━━━━━───────────┐
│o 1234*  o 4567   │
├──────────────────┤
│hola▐             │
│                  │
│                  │
│                  │
│                  │
│            NORMAL│
└──────────────────┘`},
			{":quit>",
				`┌──────────────────┐
│o 1234*  o 4567   │
├●█████████████████┤
│                  │
│  There are open  │
│  files with      │
│  changes         │
│                  │
│  Yes        No   │
└──────────────────┘`},
		}
		h := newSafeHandler(m)
		handlertest.TestHandlerSequence(t, h, 20, 10, cases)

		m.mu.Lock()
		exit, handled := m.Handle(term.Event{Type: term.EventKey, Ch: 'y'})
		assert.True(t, exit)
		assert.True(t, handled)
		m.mu.Unlock()

		require.NoError(t, m.Close())
	})

	t.Run("prompts on quit if files are dirty, user backs down", func(t *testing.T) {
		dir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = os.RemoveAll(dir)
		})
		m := newTestWorkspaceManagerHandlerWithDir(t, defaultCfg(), dir,
			nopShutdownShaderConfig())
		require.NoError(t, m.openFile("1234", true))
		require.NoError(t, m.openFile("4567", false))

		cases := []handlertest.SequenceTestCase{
			{"ihola <",
				`┌━━━━━━━───────────┐
│o 1234*  o 4567   │
├──────────────────┤
│hola▐             │
│                  │
│                  │
│                  │
│                  │
│            NORMAL│
└──────────────────┘`},
			{":quit>",
				`┌──────────────────┐
│o 1234*  o 4567   │
├●█████████████████┤
│                  │
│  There are open  │
│  files with      │
│  changes         │
│                  │
│  Yes        No   │
└──────────────────┘`},
		}
		h := newSafeHandler(m)
		handlertest.TestHandlerSequence(t, h, 20, 10, cases)

		m.mu.Lock()

		exit, handled := m.Handle(term.Event{Type: term.EventKey, Ch: 'n'})
		assert.False(t, exit)
		assert.True(t, handled)

		exit, handled = m.Handle(term.Event{Type: term.EventNone})
		assert.False(t, exit)
		assert.True(t, handled)

		m.mu.Unlock()

		require.NoError(t, m.Close())
	})

	for _, cmd := range []string{"forcequit!", "writeforcequit!"} {
		t.Run(fmt.Sprintf("does not prompt on %s", cmd), func(t *testing.T) {
			dir, err := os.MkdirTemp("", "")
			require.NoError(t, err)
			t.Cleanup(func() {
				_ = os.RemoveAll(dir)
			})
			m := newTestWorkspaceManagerHandlerWithDir(t, defaultCfg(), dir,
				nopShutdownShaderConfig())
			require.NoError(t, m.openFile("1234", true))
			require.NoError(t, m.openFile("4567", false))

			cases := []handlertest.SequenceTestCase{
				{"ihola <",
					`┌━━━━━━━───────────┐
│o 1234*  o 4567   │
├──────────────────┤
│hola▐             │
│                  │
│                  │
│                  │
│                  │
│            NORMAL│
└──────────────────┘`},
			}

			h := newSafeHandler(m)
			handlertest.TestHandlerSequence(t, h, 20, 10, cases)

			m.mu.Lock()
			exit, handled := m.Handle(term.Event{
				Ch:  testCommandKey.Ch,
				Mod: testCommandKey.Mod,
				Key: testCommandKey.Key, Type: term.EventKey,
			})
			assert.False(t, exit)
			assert.True(t, handled)

			for i, ch := range cmd {
				exit, handled := m.Handle(term.Event{Type: term.EventKey, Ch: ch})
				assert.False(t, exit)
				assert.True(t, handled, i)
			}

			// sut
			exit, handled = m.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
			assert.True(t, exit)
			assert.True(t, handled)

			m.mu.Unlock()

			require.NoError(t, m.Close())
		})
	}

	t.Run(
		"exit shader runs on exit and dirty exit prompt and closes when rejecting or dismissing",
		func(t *testing.T) {
			var mockShutdownShader mockShader
			mockShutdownShaderFn := func(term.Attributes) shader.Shader {
				return &mockShutdownShader
			}
			shutdownShaderCfg := shutdownShaderConfig{
				shader:   mockShutdownShaderFn,
				fps:      30,
				duration: 1 * time.Second,
			}

			m := newTestWorkspaceManagerHandler(t, defaultCfg(), []string{},
				shutdownShaderCfg)

			// m.shaderRunner.shader.Draw will use the shutdown shader only if the quit
			// dialog has been opened, so here mockShader won't Shade.
			m.shaderRunner.shader.Draw(&term.NoopWriter{})
			assert.False(t, mockShutdownShader.called)

			cases := []handlertest.SequenceTestCase{
				{":quit>",
					`┌──────────────────┐
│                  │
├●█████████████████┤
│                  │
│  Are you sure    │
│  you want to     │
│  exit?           │
│                  │
│  Yes        No   │
└──────────────────┘`},
			}

			// Runs the shader when invoking the prompt.
			h := newSafeHandler(m)
			handlertest.TestHandlerSequence(t, h, 20, 10, cases)

			// m.shaderRunner.shader.Draw will use the shutdown shader only if the quit
			// dialog has been opened, so here mockShader won't Shade.
			m.shaderRunner.shader.Draw(&term.NoopWriter{})
			assert.True(t, mockShutdownShader.called)

			// Reset variables
			mockShutdownShader.called = false

			m.mu.Lock()

			// Cancel shader when answering "No" to exit prompt.
			exit, handled := m.Handle(term.Event{Type: term.EventKey, Ch: 'n'})
			assert.False(t, exit)
			assert.True(t, handled)
			assert.False(t, mockShutdownShader.called)

			m.mu.Unlock()

			require.NoError(t, m.Close())
		})
}

func TestWorkspaceManagerHandlerDrawWithInitialFiles(t *testing.T) {
	// re-use storage
	dir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})

	for _, wrap := range []bool{false, true} {

		t.Run(fmt.Sprintf("wrap=%v", wrap), func(t *testing.T) {

			t.Run("initial files via openFile", func(t *testing.T) {
				m := newTestWorkspaceManagerHandlerWithDir(t, defaultConfigWithWrap(wrap),
					dir, nopShutdownShaderConfig())
				require.NoError(t, m.openFile("1234", true))
				require.NoError(t, m.openFile("4567", false))

				// the wrap=false run persists session state into the
				// shared dir, so the wrap=true run opens an unanswered
				// restore prompt that floats behind the editor: its
				// window bar shows in the frame row.
				sep := "├──────────────────┤"
				if wrap {
					sep = "├●█████████████████┤"
				}
				cases := []handlertest.SequenceTestCase{
					{"",
						`┌━━━━━━────────────┐
│o 1234  o 4567    │
` + sep + `
│▐                 │
│                  │
│                  │
│                  │
│                  │
│            NORMAL│
└──────────────────┘`},
				}
				h := newSafeHandler(m)
				handlertest.TestHandlerSequence(t, h, 20, 10, cases)

				require.NoError(t, m.Close())
			})

			t.Run("initial files from restore previous session prompt", func(t *testing.T) {
				m := newTestWorkspaceManagerHandlerWithDir(t, defaultConfigWithWrap(wrap),
					dir, nopShutdownShaderConfig())

				cases := []handlertest.SequenceTestCase{
					{"",
						`┌──────────────────┐
│                  │
├●█████████████████┤
│                  │
│  Do you want to  │
│  restore the     │
│  previous        │
│                  │
│  Yes        No   │
└──────────────────┘`},
					{"y",
						`┌────────━━━━━━────┐
│o 1234  o 4567    │
├──────────────────┤
│▐                 │
│                  │
│                  │
│                  │
│                  │
│            NORMAL│
└──────────────────┘`},
				}
				h := newSafeHandler(m)
				handlertest.TestHandlerSequence(t, h, 20, 10, cases)
				require.NoError(t, m.Close())
			})

			t.Run("initial files from auto restore", func(t *testing.T) {
				cfg := defaultConfigWithWrap(wrap)
				cfg.cfg["workspace"].(map[string]any)["auto_restore"] = true
				m := newTestWorkspaceManagerHandlerWithDir(t, cfg, dir, nopShutdownShaderConfig())

				cases := []handlertest.SequenceTestCase{
					{"",
						`┌────────━━━━━━────┐
│o 1234  o 4567    │
├──────────────────┤
│▐                 │
│                  │
│                  │
│                  │
│                  │
│            NORMAL│
└──────────────────┘`},
				}
				h := newSafeHandler(m)
				handlertest.TestHandlerSequence(t, h, 20, 10, cases)
				require.NoError(t, m.Close())
			})

			// do not re-use config across runners,
			// as they're loaded async and causes a data race
			newCfg := func() ideConfig {
				cfg := defaultConfigWithWrap(wrap)
				cfg.cfg["workspace"].(map[string]any)["auto_restore"] = true
				return cfg
			}

			t.Run("position is restored on close and open again", func(t *testing.T) {
				dir, err := os.MkdirTemp("", "")
				require.NoError(t, err)
				t.Cleanup(func() {
					_ = os.RemoveAll(dir)
				})
				// m1/m2/m3 simulate app restarts against the same
				// manager, so they must share one event loop analog:
				// one mutex + one scheduler wired into every cfg.
				mu := new(sync.Mutex)
				sched, drain := newTestScheduler(t, mu)
				manager := workspace.NewManager(config.NopConfig(), sched)
				require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme,
					workspace.NewMemoryScheme))
				uri, err := workspaceapi.ParseURI(fmt.Sprintf("memory:///%s", dir))
				require.NoError(t, err)
				runner := FuncExtensionsRunner(testRunnerFn)

				cfg1 := newCfg()
				cfg1.scheduleNextTick = sched
				m1 := newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
					&uri, cfg1, runner, nil, dir, nil, nopShutdownShaderConfig())

				cases := []handlertest.SequenceTestCase{
					{":edit 1234>ih3ll0\nw1rld <:write>:edit 4567>ihello\nworld <:write>:notificationcloseall>",
						`┌────────━━━━━━────┐
│o 1234  o 4567    │
├──────────────────┤
│hello             │
│world▐            │
│                  │
│                  │
│                  │
│            NORMAL│
└──────────────────┘`},
				}
				h := newSafeHandler(m1)
				handlertest.TestHandlerSequence(t, h, 20, 10, cases)
				require.NoError(t, m1.Close())

				cfg2 := newCfg()
				cfg2.scheduleNextTick = sched
				m2 := newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
					&uri, cfg2, runner, nil, dir, nil, nopShutdownShaderConfig())

				cases = []handlertest.SequenceTestCase{
					{"",
						`┌────────━━━━━━────┐
│o 1234  o 4567    │
├──────────────────┤
│hello             │
│world▐            │
│                  │
│                  │
│                  │
│            NORMAL│
└──────────────────┘`},
					{"i\na\nb\nc\nd\ne\nf<:write>",
						`┌────────━━━━━━────┐
│o 1234  o 4567    │
├──────────────────┤
│b                 │
│c                 │
│d                 │
│e                 │
│▐                 │
│            NORMAL│
└──────────────────┘`},
				}
				h2 := newSafeHandler(m2)
				handlertest.TestHandlerSequence(t, h2, 20, 10, cases)
				require.NoError(t, m2.Close())

				cfg3 := newCfg()
				cfg3.scheduleNextTick = sched
				m3 := newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
					&uri, cfg3, runner, nil, dir, nil, nopShutdownShaderConfig())

				cases = []handlertest.SequenceTestCase{
					{"",
						`┌────────━━━━━━────┐
│o 1234  o 4567    │
├──────────────────┤
│d                 │
│e                 │
│▐                 │
│                  │
│                  │
│            NORMAL│
└──────────────────┘`},
				}
				h3 := newSafeHandler(m3)
				handlertest.TestHandlerSequence(t, h3, 20, 10, cases)
				require.NoError(t, m3.Close())
			})

			t.Run("position is restored on workspacereload", func(t *testing.T) {
				// Use a file:// workspace so :write persists to disk
				// across the reload. RUNE-180 made reload close+open
				// the scheme, so a mem:// buffer would disappear with
				// the scheme even after :write.
				dir := t.TempDir()
				// The manager shares the event-loop scheduler: reload
				// mutates the buffer from its worker goroutine through
				// ScheduleNextTick, and an inline scheduler would run
				// that mutation off the lock, concurrently with Draw.
				cfg := defaultConfigWithWrap(wrap)
				mu, sched, drain := buildTestSchedulerForCfg(t, &cfg)
				manager := workspace.NewManager(config.NopConfig(), sched)
				require.NoError(t, manager.RegisterScheme(
					workspace.MemoryScheme, workspace.NewMemoryScheme))
				require.NoError(t, manager.RegisterScheme(
					workspace.FileScheme, workspace.NewFileScheme))
				uri, err := workspaceapi.ParseURI("file://" + dir)
				require.NoError(t, err)
				runner := FuncExtensionsRunner(testRunnerFn)
				m := newTestWorkspaceManagerHandlerWithManagerMu(
					t, manager, mu, drain, &uri, cfg,
					runner, nil, dir, nil, nopShutdownShaderConfig())

				cases := []handlertest.SequenceTestCase{
					{":edit A>ih3ll0\nw1rld <:write>:edit B>ihello\nworld <:write>:notificationcloseall>",
						`┌─────━━━──────────┐
│o A  o B          │
├──────────────────┤
│hello             │
│world▐            │
│                  │
│                  │
│                  │
│            NORMAL│
└──────────────────┘`},
					{":workspacerelo>",
						`┌─────━━━──────────┐
│o A  o B          │
├──────────────────┤
│hello             │
│world▐            │
│                  │
│                  │
│                  │
│            NORMAL│
└──────────────────┘`},
					{"i\na\nb\nc\nd\ne\nf<:write>",
						`┌─────━━━──────────┐
│o A  o B          │
├──────────────────┤
│b                 │
│c                 │
│d                 │
│e                 │
│▐                 │
│            NORMAL│
└──────────────────┘`},
					{":workspacerelo>",
						`┌─────━━━──────────┐
│o A  o B          │
├──────────────────┤
│d                 │
│e                 │
│▐                 │
│                  │
│                  │
│            NORMAL│
└──────────────────┘`},
				}
				h := newSafeHandler(m)
				handlertest.TestHandlerSequence(t, h, 20, 10, cases)
				require.NoError(t, m.Close())
			})
		})
	}
}

func TestWorkspaceManagerRestoresOpenTerminalSessions(t *testing.T) {
	t.Run("workspace close and reopen restores terminal tabs and windows", func(t *testing.T) {
		dir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = os.RemoveAll(dir)
		})
		cfg := defaultConfigWithWrap(false)
		cfg.cfg["workspace"] = map[string]any{"auto_restore": true}
		cfg.ringBell = func() {}
		mu, sched, drain := buildTestSchedulerForCfg(t, &cfg)
		manager := workspace.NewManager(config.NopConfig(), sched)
		require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme,
			workspace.NewMemoryScheme))
		const terminalWorkspaceScheme = "terminaltest"
		require.NoError(t, manager.RegisterScheme(terminalWorkspaceScheme,
			func(ctx context.Context, cfg config.Config, uri workspaceapi.URI) (schemeapi.Scheme, error) {
				return newTerminalSessionTestScheme(ctx, cfg, uri)
			}))
		uri, err := workspaceapi.ParseURI(terminalWorkspaceScheme + ":///workspace")
		require.NoError(t, err)
		runner := FuncExtensionsRunner(testRunnerFn)

		m := newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
			&uri, cfg, runner, nil, dir, nil, nopShutdownShaderConfig())
		m.mu.Lock()
		ex1 := m.exHandler(m.focusHandler())
		fileURI, err := ex1.workspace.URI("restored.txt")
		require.NoError(t, err)
		fileTab, err := ex1.editFileURI(fileURI, ex1.invokeWindow(), false)
		require.NoError(t, err)
		_, ok := fileTab.Handler().(text.Handler)
		require.True(t, ok)
		require.NoError(t, ex1.newTask(context.Background(), "persisted-task", "right", "--", "echo", "ok"))
		require.NoError(t, ex1.windownew(context.Background(), "right"))
		require.NoError(t, ex1.terminalnewtab(context.Background(), "tab terminal"))
		require.NoError(t, ex1.windownew(context.Background(), "down"))
		require.NoError(t, ex1.terminalnew(context.Background(), "window terminal"))
		require.Len(t, ex1.comp.Tabs(), 2)
		require.NoError(t, m.commandCloseWorkspace())
		require.Equal(t, 0, m.workspaceCount)

		require.NoError(t, m.addWorkspace(uri, true, false, -1))
		m.mu.Unlock()
		m.waitForWorkspace(t, uri)
		m.mu.Lock()
		m.Resize(80, 24)
		ex2 := m.exHandler(m.focusHandler())
		tabs := ex2.comp.Tabs()
		require.Len(t, tabs, 2)
		var terminalTabs int
		for _, tab := range tabs {
			if _, ok := tab.Handler().(vtereservoir.VTE); ok {
				terminalTabs++
			}
		}
		require.Equal(t, 1, terminalTabs)

		var terminalWindows, tabTerminalWindows, ephemeralTerminalWindows, fileWindows int
		var taskWindows, minimizedTaskWindows int
		ex2.comp.Browser().IterateWindows(func(win browser.Window) {
			content, err := win.Content()
			require.NoError(t, err)
			switch content := content.(type) {
			case vtereservoir.VTE:
				ephemeralTerminalWindows++
				terminalWindows++
			case *browser.Tab:
				if _, ok := content.Handler().(vtereservoir.VTE); ok {
					tabTerminalWindows++
					terminalWindows++
				} else if _, ok := content.Handler().(text.Handler); ok {
					fileWindows++
				}
			case *idetask.Task:
				taskWindows++
				_, minimized := win.IsMinimized()
				if minimized {
					minimizedTaskWindows++
				}
			}
		})
		require.Equal(t, 1, tabTerminalWindows)
		require.Equal(t, 1, ephemeralTerminalWindows)
		require.Equal(t, 2, terminalWindows)
		require.Equal(t, 1, fileWindows)
		require.Equal(t, 1, taskWindows)
		require.Equal(t, 1, minimizedTaskWindows)
		require.Equal(t, tcomponent.SplitOrientationVertical,
			ex2.comp.Browser().TileLayout().Split)
		require.Len(t, ex2.comp.Browser().TileLayout().Children, 2)
		require.Equal(t, tcomponent.SplitOrientationHorizontal,
			ex2.comp.Browser().TileLayout().Children[1].Split)
		require.Len(t, ex2.comp.Browser().TileLayout().Children[1].Children, 2)

		m.mu.Unlock()
		require.NoError(t, m.Close())
	})

	t.Run("workspace reload preserves deep layout with minimized task", func(t *testing.T) {
		dir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = os.RemoveAll(dir)
		})
		cfg := defaultConfigWithWrap(false)
		cfg.cfg["workspace"] = map[string]any{"auto_restore": true}
		cfg.ringBell = func() {}
		mu, sched, drain := buildTestSchedulerForCfg(t, &cfg)
		manager := workspace.NewManager(config.NopConfig(), sched)
		require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme,
			workspace.NewMemoryScheme))
		const terminalWorkspaceScheme = "terminaltest"
		require.NoError(t, manager.RegisterScheme(terminalWorkspaceScheme,
			func(ctx context.Context, cfg config.Config, uri workspaceapi.URI) (schemeapi.Scheme, error) {
				return newTerminalSessionTestScheme(ctx, cfg, uri)
			}))
		uri, err := workspaceapi.ParseURI(terminalWorkspaceScheme + ":///workspace")
		require.NoError(t, err)
		runner := FuncExtensionsRunner(testRunnerFn)

		m := newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
			&uri, cfg, runner, nil, dir, nil, nopShutdownShaderConfig())

		wantLayoutBeforeReload := `┌──────────────━━━━━━━━━━━━────────────────────────────────────────────────────┐
│o nested.txt  o middle.txt                                                    │
├┌────────────────────────┐┌────────────────────────┐┌─────────────────────────┤
││                        ││▐                       ││                         │
││                        ││                        ││                         │
││                        ││                        ││                         │
││                        ││                        ││                         │
││                        ││                        ││                         │
││                        ││                        ││                         │
││                        ││                        ││                         │
││                        ││                        ││                         │
││                        ││                        ││                         │
││                        ││                        │└─────────────────────────┘
││                        ││                        │┌───────────┐┌────────────┐
││                        ││                        ││           ││            │
││                        ││                        ││           ││            │
││                        ││                        ││           ││            │
││                        ││                        ││           ││            │
││                        ││                        ││           ││            │
││                        ││                        ││           ││            │
││                        ││                        ││           ││            │
││                        ││                        ││           ││            │
││                        ││                  NORMAL││           ││      NORMAL│
└└────────────────────────┘└────────────────────────┘└───────────┘└────────────┘`
		// After workspacereload, focus is no longer persisted via the
		// layout: WindowManager.RestoreTileLayout picks the first leaf
		// it finds in the new tree as the active focus. Here that is
		// the empty top-left tile (which has no editor and therefore
		// no cursor), so the visible cursor that was present in
		// wantLayoutBeforeReload disappears from the rendered frame.
		wantLayoutAfterReload := `┌──────────────────────────────────────────────────────────────────────────────┐
│o nested.txt  o middle.txt                                                    │
├┌────────────────────────┐┌────────────────────────┐┌─────────────────────────┤
││                        ││                        ││                         │
││                        ││                        ││                         │
││                        ││                        ││                         │
││                        ││                        ││                         │
││                        ││                        ││                         │
││                        ││                        ││                         │
││                        ││                        ││                         │
││                        ││                        ││                         │
││                        ││                        ││                         │
││                        ││                        │└─────────────────────────┘
││                        ││                        │┌───────────┐┌────────────┐
││                        ││                        ││           ││            │
││                        ││                        ││           ││            │
││                        ││                        ││           ││            │
││                        ││                        ││           ││            │
││                        ││                        ││           ││            │
││                        ││                        ││           ││            │
││                        ││                        ││           ││            │
││                        ││                        ││           ││            │
││                        ││                  NORMAL││           ││      NORMAL│
└└────────────────────────┘└────────────────────────┘└───────────┘└────────────┘`
		wantTaskFocus := `┌──────────────────────────────────────────────────────────────────────────────┐
│o nested.txt  o middle.txt                                                    │
├────────────────────────┐┌─────────────────────────┐┌─────────────────────────┤
│                        ││                         ││                         │
█●██████████████████████████████                    ││                         │
│ ▀        sleep 1000        0s│                    ││                         │
│                              │                    ││                         │
│                              │                    ││                         │
│                              │                    ││                         │
│                              │                    ││                         │
│                              │                    ││                         │
│                              │                    ││                         │
│                              │                    │└─────────────────────────┘
│                              │                    │┌───────────┐┌────────────┐
│                              │                    ││           ││            │
│                              │                    ││           ││            │
│                              │                    ││           ││            │
│                              │                    ││           ││            │
│                              │                    ││           ││            │
│                              │                    ││           ││            │
└──────────────────────────────┘                    ││           ││            │
│                        ││                         ││           ││            │
│                        ││                   NORMAL││           ││      NORMAL│
└────────────────────────┘└─────────────────────────┘└───────────┘└────────────┘`
		cases := []handlertest.SequenceTestCase{
			{
				InputSequence: "<c-\\\\>tasknew<space>sleeper<space>left<space>--<space>sleep<space>1000<enter>" +
					"<c-\\\\>windownew<space>right<enter>" +
					"<c-\\\\>windownew<space>right<enter>" +
					"<c-\\\\>windownew<space>down<enter>" +
					"<c-\\\\>windownew<space>right<enter>" +
					"<c-\\\\>edit<space>nested.txt<enter>" +
					"<c-\\\\>windowfocus<space>left<enter>" +
					"<c-\\\\>windowfocus<space>left<enter>" +
					"<c-\\\\>edit<space>middle.txt<enter>",
				Expected: wantLayoutBeforeReload,
			},
			{
				InputSequence: "<c-\\\\>workspacereload<enter>",
				Expected:      wantLayoutAfterReload,
			},
			{
				InputSequence: "<c-\\\\>taskfocus<space>sleeper<enter>",
				Expected:      wantTaskFocus,
			},
		}

		h := newSafeHandler(m)
		handlertest.RunHandlerSequence(t, h, 80, 24, cases)

		require.NoError(t, m.Close())
	})

	t.Run("workspace reload skips empty floating windows", func(t *testing.T) {
		dir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = os.RemoveAll(dir)
		})

		cfg := defaultConfigWithWrap(false)
		cfg.cfg["workspace"] = map[string]any{"auto_restore": true}
		cfg.ringBell = func() {}
		m := newTestWorkspaceManagerHandlerWithDir(t, cfg, dir, nopShutdownShaderConfig())

		ex1 := m.exHandler(m.focusHandler())
		fileURI, err := ex1.workspace.URI("restored-with-floating.txt")
		require.NoError(t, err)
		_, err = ex1.editFileURI(fileURI, ex1.invokeWindow(), false)
		require.NoError(t, err)
		_, err = ex1.comp.Floating(
			browser.NopFloatingHandler(handler.StaticFloating(handler.Nop(), 10, 4)),
			browserapi.FloatingConfig{Alignment: component.AlignmentCentered},
		)
		require.NoError(t, err)
		require.Equal(t, 1, ex1.comp.Browser().FloatingWindows())

		m.mu.Lock()
		err = m.commandReloadWorkspace()
		m.mu.Unlock()
		require.NoError(t, err)
		m.quiesce()
		m.Resize(80, 24)
		ex2 := m.exHandler(m.focusHandler())
		require.Equal(t, 0, ex2.comp.Browser().FloatingWindows())
		tabs := ex2.comp.Tabs()
		require.Len(t, tabs, 1)
		require.Equal(t, "restored-with-floating.txt", tabs[0].URI().Name())

		require.NoError(t, m.Close())
	})

	t.Run("manager close and reopen restores terminal tab", func(t *testing.T) {
		dir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = os.RemoveAll(dir)
		})
		mu := new(sync.Mutex)
		sched, drain := newTestScheduler(t, mu)
		manager := workspace.NewManager(config.NopConfig(), sched)
		require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme,
			workspace.NewMemoryScheme))
		const terminalWorkspaceScheme = "terminaltest"
		require.NoError(t, manager.RegisterScheme(terminalWorkspaceScheme,
			func(ctx context.Context, cfg config.Config, uri workspaceapi.URI) (schemeapi.Scheme, error) {
				return newTerminalSessionTestScheme(ctx, cfg, uri)
			}))
		uri, err := workspaceapi.ParseURI(terminalWorkspaceScheme + ":///workspace")
		require.NoError(t, err)
		runner := FuncExtensionsRunner(testRunnerFn)
		newCfg := func() ideConfig {
			cfg := defaultConfigWithWrap(false)
			cfg.cfg["workspace"] = map[string]any{"auto_restore": true}
			cfg.ringBell = func() {}
			cfg.scheduleNextTick = sched
			return cfg
		}

		m1 := newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
			&uri, newCfg(), runner, nil, dir, nil, nopShutdownShaderConfig())
		m1.mu.Lock()
		ex1 := m1.exHandler(m1.focusHandler())
		require.NoError(t, ex1.terminalnewtab(context.Background()))
		require.Len(t, ex1.comp.Tabs(), 1)
		m1.mu.Unlock()
		require.NoError(t, m1.Close())

		m2 := newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
			&uri, newCfg(), runner, nil, dir, nil, nopShutdownShaderConfig())
		defer m2.Close()
		m2.mu.Lock()
		defer m2.mu.Unlock()
		ex2 := m2.exHandler(m2.focusHandler())
		tabs := ex2.comp.Tabs()
		require.Len(t, tabs, 1)
		_, ok := tabs[0].Handler().(vtereservoir.VTE)
		require.True(t, ok)
	})
}

type terminalSessionTestScheme struct {
	schemeapi.Scheme
	uri workspaceapi.URI
}

func newTerminalSessionTestScheme(
	ctx context.Context,
	cfg config.Config,
	uri workspaceapi.URI,
) (schemeapi.Scheme, error) {
	memURI, err := workspaceapi.ParseURI("memory://" + uri.Path())
	if err != nil {
		return nil, err
	}
	base, err := workspace.NewMemoryScheme(ctx, cfg, memURI)
	if err != nil {
		return nil, err
	}
	return &terminalSessionTestScheme{Scheme: base, uri: uri}, nil
}

func (s *terminalSessionTestScheme) URI(path string) (workspaceapi.URI, error) {
	return s.Scheme.URI(path)
}

func (s *terminalSessionTestScheme) Root() string {
	return s.Scheme.Root()
}

func (s *terminalSessionTestScheme) Chroot(path string) (schemeapi.Scheme, error) {
	base, err := s.Scheme.Chroot(path)
	if err != nil {
		return nil, err
	}
	uri, err := s.URI(path)
	if err != nil {
		return nil, err
	}
	return &terminalSessionTestScheme{Scheme: base, uri: uri}, nil
}

func (s *terminalSessionTestScheme) NewPty(context.Context) (workspaceapi.Pty, error) {
	return workspaceapi.Pty{
		Master: workspacetest.NewFile(),
		Slave:  workspacetest.NewFile(),
	}, nil
}

func (s *terminalSessionTestScheme) SetPtySize(workspaceapi.Pty, int, int) error {
	return nil
}

func (s *terminalSessionTestScheme) StartCommand(context.Context, workspaceapi.Cmd) (workspaceapi.Pid, error) {
	return 0, nil
}

func (s *terminalSessionTestScheme) Signal(workspaceapi.Pid, syscall.Signal) error {
	return nil
}

// bellPtyState records every pty master pipe created by a
// bellPtyScheme so a test can inject bytes into the terminal's
// output stream.
type bellPtyState struct {
	mu      sync.Mutex
	writers []*io.PipeWriter
}

// ring writes BEL into every pty created so far.
func (s *bellPtyState) ring(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	writers := append([]*io.PipeWriter(nil), s.writers...)
	s.mu.Unlock()
	require.NotEmpty(t, writers, "no pty was created")
	for _, w := range writers {
		_, err := w.Write([]byte{'\a'})
		require.NoError(t, err)
	}
}

// bellPtyScheme overrides NewPty to hand the emulator a pipe-backed
// master whose write end stays with the test, so terminal output can
// be injected without a host process.
type bellPtyScheme struct {
	schemeapi.Scheme
	state *bellPtyState
}

func (s *bellPtyScheme) Chroot(path string) (schemeapi.Scheme, error) {
	base, err := s.Scheme.Chroot(path)
	if err != nil {
		return nil, err
	}
	return &bellPtyScheme{Scheme: base, state: s.state}, nil
}

func (s *bellPtyScheme) NewPty(context.Context) (workspaceapi.Pty, error) {
	r, w := io.Pipe()
	s.state.mu.Lock()
	s.state.writers = append(s.state.writers, w)
	s.state.mu.Unlock()
	return workspaceapi.Pty{
		Master: bellPtyMaster{File: workspacetest.NewFile(), r: r},
		Slave:  workspacetest.NewFile(),
	}, nil
}

// bellPtyMaster reads terminal output from the test-fed pipe and
// swallows emulator writes (keystrokes, probe responses).
type bellPtyMaster struct {
	workspaceapi.File
	r *io.PipeReader
}

func (f bellPtyMaster) Read(b []byte) (int, error)  { return f.r.Read(b) }
func (f bellPtyMaster) Write(b []byte) (int, error) { return len(b), nil }
func (f bellPtyMaster) Close() error                { return f.r.Close() }

func TestInitializeNoCwd(t *testing.T) {
	m := newTestWorkspaceManagerHandlerWithDir(t,
		defaultConfigWithWrap(false), "", nopShutdownShaderConfig())

	cases := []handlertest.SequenceTestCase{
		{"",
			`┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│workspaceWallpaper│
│                  │
│                  │
├──────────────────┤
│1                 │
└━─────────────────┘`},
	}
	h := newSafeHandler(m)
	handlertest.TestHandlerSequence(t, h, 20, 10, cases)

	require.NoError(t, m.Close())
}

func TestInitializeNotifications(t *testing.T) {
	m := newTestWorkspaceManagerHandlerWithDir(t,
		defaultConfigWithWrap(false), "", nopShutdownShaderConfig())

	cases := []handlertest.SequenceTestCase{
		{"",
			`┌────┌─────────────┐
│    │ 6:14am      │
├────└─────────────┘
│                  │
│workspaceWallpaper│
│                  │
│                  │
├──────────────────┤
│1                 │
└━─────────────────┘`},
	}
	h := newSafeHandler(m)
	m.notifications.current().NotifyOnce(browserapi.LevelWarn, "6:14am")
	handlertest.TestHandlerSequence(t, h, 20, 10, cases)

	require.NoError(t, m.Close())
}

// recordingNotifications wraps a browserapi.Notifications and captures every
// (level, formatted-msg) pair seen. Used by the auto-save integration test
// to assert which notifications the autoSaver surfaces, without depending
// on UI rendering.
type recordingNotifications struct {
	mu       sync.Mutex
	inner    browserapi.Notifications
	captured []capturedNote
}

type capturedNote struct {
	level browserapi.NotificationLevel
	msg   string
}

// feedAutoSaveSequence feeds the legacy handlertest sequence syntax used by
// other integration tests in this file: ':' opens the modal prompt, '>' is
// Enter, '<' is Esc, and bare runes are typed verbatim. It bypasses the
// rendered-output assertion of TestHandlerSequence which is irrelevant
// here.
func feedAutoSaveSequence(t *testing.T, h tui.Handler, seq string) {
	t.Helper()
	for _, r := range seq {
		switch r {
		case ':':
			h.Handle(term.Event{Mod: term.ModCtrl, Ch: '\\', Type: term.EventKey})
		case ' ':
			h.Handle(term.Event{Key: term.KeySpace, Type: term.EventKey})
		case '>':
			h.Handle(term.Event{Key: term.KeyEnter, Type: term.EventKey})
		case '<':
			h.Handle(term.Event{Key: term.KeyEsc, Type: term.EventKey})
		default:
			h.Handle(term.Event{Ch: r, Type: term.EventKey})
		}
	}
}

func (r *recordingNotifications) Notify(level browserapi.NotificationLevel,
	msg string, args ...any) (string, error) {
	r.mu.Lock()
	r.captured = append(r.captured,
		capturedNote{level: level, msg: fmt.Sprintf(msg, args...)})
	r.mu.Unlock()
	return r.inner.Notify(level, msg, args...)
}

func (r *recordingNotifications) NotifyOnce(level browserapi.NotificationLevel,
	msg string, args ...any) (string, error) {
	r.mu.Lock()
	r.captured = append(r.captured,
		capturedNote{level: level, msg: fmt.Sprintf(msg, args...)})
	r.mu.Unlock()
	return r.inner.NotifyOnce(level, msg, args...)
}

func (r *recordingNotifications) UpdateNotificationProgress(
	id, message string, progress, total int64,
) error {
	return r.inner.UpdateNotificationProgress(id, message, progress, total)
}

func (r *recordingNotifications) snapshot() []capturedNote {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]capturedNote, len(r.captured))
	copy(out, r.captured)
	return out
}

// integrationFlusher is a stub autoSaverFlusher used by
// TestAutoSaveIntegration. It records flush attempts and returns the
// error configured by the test, isolating the integration to the
// wiring (config → subscription → debounce → flush → notification)
// without exercising real disk I/O, which would race with the
// per-workspace filesystem-event dispatcher under -race.
type integrationFlusher struct {
	mu     sync.Mutex
	called bool
	err    error
}

func (f *integrationFlusher) Resource(uri workspaceapi.URI) (browserapi.Handler, bool) {
	return integrationHandler{uri: uri}, true
}

func (f *integrationFlusher) FlushTab(
	_ context.Context, _ browserapi.Handler,
) (<-chan error, error) {
	f.mu.Lock()
	f.called = true
	err := f.err
	f.mu.Unlock()
	if err != nil {
		// pre-flight error (e.g. ErrInvalidSave)
		return nil, err
	}
	ch := make(chan error, 1)
	ch <- nil
	close(ch)
	return ch, nil
}

func (f *integrationFlusher) flushed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.called
}

type integrationHandler struct{ uri workspaceapi.URI }

func (integrationHandler) Resize(_, _ int)                          {}
func (integrationHandler) Draw(_ term.Writer)                       {}
func (integrationHandler) Handle(_ term.Event) (exit, handled bool) { return false, false }
func (integrationHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, 0, false
}
func (integrationHandler) Selection() (string, bool) { return "", false }
func (integrationHandler) Close() error              { return nil }

func TestAutoSaveIntegration(t *testing.T) {
	// Shorten the auto-save delay so tests don't have to wait the
	// production 2s. The factory hook lets us wrap the workspace's
	// notifications channel with a recorder.
	prevDelay := defaultAutoSaveDelay
	defaultAutoSaveDelay = 10 * time.Millisecond
	t.Cleanup(func() { defaultAutoSaveDelay = prevDelay })

	cases := []struct {
		name       string
		flushErr   error
		wantNotify bool
		wantNotMsg string
	}{
		{
			name:       "writable file is flushed after idle delay",
			flushErr:   nil,
			wantNotify: false,
		},
		{
			name:       "read-only file surfaces a warning notification",
			flushErr:   workspaceapi.ErrFileIsNotWritable,
			wantNotify: true,
			wantNotMsg: "auto-save skipped",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			filename := "doc.txt"

			cfg := defaultConfigWithWrap(false)
			editorCfg := cfg.cfg["editor"].(map[string]any)
			editorCfg["auto_save"] = true
			cfg.cfg["editor"] = editorCfg

			// Replace the autoSaver's component with a stub flusher
			// that records flush attempts and returns the test's
			// configured error. This isolates the integration to
			// the wiring (config → subscription → debounced timer
			// → flush call → notification) without exercising real
			// disk I/O, which would race with the per-workspace
			// filesystem-event dispatcher under -race.
			stub := &integrationFlusher{
				err: tc.flushErr,
			}
			var rec *recordingNotifications
			prevFactory := autoSaverFactory
			// The autoSaver assumes all map mutations happen on the
			// editor's event-loop goroutine. In production this is
			// guaranteed because scheduleNextTick re-posts the flush
			// callback as a term.EventInterrupt, which the event loop
			// serializes with edit handling. defaultCfg's
			// scheduleNextTick runs callbacks inline, so we replace
			// it with a queue and drain inside the polling loop below
			// so flushURI always runs on the test goroutine.
			schedQ := newQueueSched()
			autoSaverFactory = func(_ autoSaverFlusher,
				notif browserapi.Notifications,
				_ func(func()) bool, _ time.Duration,
			) *autoSaver {
				rec = &recordingNotifications{inner: notif}
				return newAutoSaver(stub, rec, schedQ.sched,
					defaultAutoSaveDelay)
			}
			t.Cleanup(func() { autoSaverFactory = prevFactory })

			uri, err := workspaceapi.ParseURI("memory://" + dir)
			require.NoError(t, err)
			m := newTestWorkspaceManagerHandlerWithDir(t, cfg, dir,
				nopShutdownShaderConfig())
			require.NoError(t, m.addOrCreateWorkspace(uri))
			m.quiesce()
			t.Cleanup(func() { _ = m.Close() })

			require.NotNil(t, rec, "autoSaver was not constructed; "+
				"check editor.auto_save config wiring")

			h := newSafeHandler(m)
			h.Resize(30, 9)
			feedAutoSaveSequence(t, h, ":edit "+filename+">iHello<")

			require.Eventually(t, func() bool {
				schedQ.drainAll()
				if !stub.flushed() {
					return false
				}
				if tc.wantNotify {
					for _, n := range rec.snapshot() {
						if strings.Contains(n.msg, tc.wantNotMsg) {
							return true
						}
					}
					return false
				}
				return true
			}, 2*time.Second, 5*time.Millisecond)

			assert.True(t, stub.flushed(),
				"autoSaver did not invoke flush after edit")
			if tc.wantNotify {
				var found bool
				for _, n := range rec.snapshot() {
					if strings.Contains(n.msg, tc.wantNotMsg) {
						assert.Equal(t, browserapi.LevelWarn, n.level)
						found = true
						break
					}
				}
				assert.True(t, found,
					"expected notification containing %q, got %v",
					tc.wantNotMsg, rec.snapshot())
			} else {
				for _, n := range rec.snapshot() {
					assert.NotContains(t, n.msg, "auto-save",
						"unexpected auto-save notification: %v", n)
				}
			}
		})
	}

	t.Run("fexplorer edits do not arm autoSaver", func(t *testing.T) {
		dir := t.TempDir()

		cfg := defaultConfigWithWrap(false)
		editorCfg := cfg.cfg["editor"].(map[string]any)
		editorCfg["auto_save"] = true
		cfg.cfg["editor"] = editorCfg

		stub := &integrationFlusher{}
		var rec *recordingNotifications
		var saver *autoSaver
		prevFactory := autoSaverFactory
		schedQ := newQueueSched()
		autoSaverFactory = func(_ autoSaverFlusher,
			notif browserapi.Notifications,
			_ func(func()) bool, _ time.Duration,
		) *autoSaver {
			rec = &recordingNotifications{inner: notif}
			saver = newAutoSaver(stub, rec, schedQ.sched,
				defaultAutoSaveDelay)
			return saver
		}
		t.Cleanup(func() { autoSaverFactory = prevFactory })

		uri, err := workspaceapi.ParseURI("memory://" + dir)
		require.NoError(t, err)
		m := newTestWorkspaceManagerHandlerWithDir(t, cfg, dir,
			nopShutdownShaderConfig())
		require.NoError(t, m.addOrCreateWorkspace(uri))
		m.quiesce()
		t.Cleanup(func() { _ = m.Close() })

		require.NotNil(t, rec, "autoSaver was not constructed; "+
			"check editor.auto_save config wiring")

		// Simulate an editor Edit event on the fexplorer pseudo-URI
		// straight through the wired autoSaver. We bypass the TUI
		// here because reproducing a real fexplorer buffer edit via
		// terminal input is brittle (the explorer intercepts most
		// keys for navigation); what we care about is that the
		// production wiring routes fexplorer Edit events without
		// arming a debounce timer.
		fexURI, err := workspaceapi.ParseURI(fileExplorerURI)
		require.NoError(t, err)
		saver.Handle(context.Background(), textapi.Event{
			Type: textapi.EventTypeEdit,
			URI:  fexURI,
		})

		time.Sleep(3 * defaultAutoSaveDelay)
		schedQ.drainAll()

		assert.Empty(t, saver.timers,
			"autoSaver must not arm a debounce timer for fexplorer")
		assert.False(t, stub.flushed(),
			"autoSaver must not flush the fexplorer pseudo-buffer")
		for _, n := range rec.snapshot() {
			assert.NotContains(t, n.msg, "auto-save",
				"unexpected auto-save notification: %v", n)
		}
	})
}

func TestNoBar(t *testing.T) {
	cfg := defaultConfigWithWrap(false)
	cfg.cfg["browser"] = map[string]any{"workspace_bar": false}
	m := newTestWorkspaceManagerHandlerWithDir(t,
		cfg, "", nopShutdownShaderConfig())

	cases := []handlertest.SequenceTestCase{
		{"",
			`┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│                  │
│workspaceWallpaper│
│                  │
│                  │
│                  │
└──────────────────┘`},
		{":workspacefocus 2>",
			`┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│                  │
│workspaceWallpaper│
│                  │
│                  │
│                  │
└──────────────────┘`},
	}
	h := newSafeHandler(m)
	handlertest.TestHandlerSequence(t, h, 20, 10, cases)

	require.NoError(t, m.Close())
}

func TestSwitchToWorkspaceComplete(t *testing.T) {
	m := newTestWorkspaceManagerHandlerWithDirs(t, defaultConfigWithWrap(false),
		"/tmp", "", nopShutdownShaderConfig())

	cases := []handlertest.SequenceTestCase{
		{":wofo ",
			`┌──────────────────────────────────────┐
│                                      │
├──────────────────────────────────────┤
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
┌──────────────────────────────────────┐
│ workspacefocus ▐1|2|3|4|5|6|7|8|9    │
│ 1 memory:///tmp                      │
│ 2                                    │
│ 3                                    │
│ 4                                    │
│ 5                                    │
│ 6                                    │
│ 7                                    │
│ 8                                    │
│ 9                                    │
└──────────────────────────────────────┘`},
	}
	h := newSafeHandler(m)
	handlertest.TestHandlerSequence(t, h, 40, 20, cases)

	require.NoError(t, m.Close())
}

func TestMoveWorkspace(t *testing.T) {
	m := newTestWorkspaceManagerHandlerWithDirs(t, defaultConfigWithWrap(false),
		"/tmp", "", nopShutdownShaderConfig())

	cases := []handlertest.SequenceTestCase{
		{":womo ",
			`┌──────────────────────────────────────┐
│                                      │
├──────────────────────────────────────┤
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
┌──────────────────────────────────────┐
│ workspacemove ▐right|left|1|2|3|4    │
│ 1                                    │
│ 2                                    │
│ 3                                    │
│ 4                                    │
│ 5                                    │
│ 6                                    │
│ 7                                    │
│ 8                                    │
│ 9                                    │
└──────────────────────────────────────┘`},
	}
	h := newSafeHandler(m)
	handlertest.TestHandlerSequence(t, h, 40, 20, cases)

	cases = []handlertest.SequenceTestCase{
		{"right>",
			`┌──────────────────────────────────────┐
│                                      │
├──────────────────────────────────────┤
│                                      │
│          workspaceWallpaper          │
│                                      │
└──────────────────────────────────────┘`},
		{":wope memory\\:///tmp2>",
			`┌──────────────────────────────────────┐
│                                      │
├──────────────────────────────────────┤
│          workspaceWallpaper          │
├──────────────────────────────────────┤
│2 2  3 3                              │
└─────━━━──────────────────────────────┘`},
		{":womo 1>",
			`┌──────────────────────────────────────┐
│                                      │
├──────────────────────────────────────┤
│          workspaceWallpaper          │
├──────────────────────────────────────┤
│1 1  2 2                              │
└━━━───────────────────────────────────┘`},
		{":wofo 1>:womo left>",
			`┌────────────────────────┌─────────────┐
│                        │ workspace   │
├────────────────────────│ is already  │
│          workspaceWallp│ at the      │
├────────────────────────│ first slot  ┤
│1 1  2 2                              │
└━━━───────────────────────────────────┘`},
		{":noticloseall>:womo 9>:womo right>",
			`┌────────────────────────┌─────────────┐
│                        │ workspace   │
├────────────────────────│ is already  │
│          workspaceWallp│ at the      │
├────────────────────────│ last slot   ┤
│2 2  9 9                              │
└─────━━━──────────────────────────────┘`},
	}
	handlertest.TestHandlerSequence(t, h, 40, 7, cases)

	require.NoError(t, m.Close())
}

func TestExternalCommands(t *testing.T) {
	// FIXME: flaky under CI; skipped until stabilized.
	if ci := os.Getenv("CI"); ci == "true" {
		t.SkipNow()
	}

	t.Run("happy path", func(t *testing.T) {
		dir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		dir2, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() {
			os.RemoveAll(dir)
			os.RemoveAll(dir2)
		})

		m := newTestWorkspaceManagerHandlerWithDir(t, defaultConfigWithWrap(false), dir,
			nopShutdownShaderConfig())
		err = m.subscribeCommand(textapi.CommandManual{Name: "ramon"},
			text.FuncCommandHandler(func(context.Context, textapi.Command) error {
				return nil
			}, func(ctx context.Context, cmd textapi.Command) (
				iterator.Iterator[string], string, error,
			) {
				return iterator.FromSlice([]string{"wasup", "wasep"}), "", nil
			}))
		require.NoError(t, err)

		cases := []handlertest.SequenceTestCase{
			// '_' simulates sleeps; we can't and shouldn't
			// enable sync command prompt from here
			{":ramo w__", // existing workspace
				`┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│                            │
│                            │
│                            │
┌────────────────────────────┐
│ ramon w▐                   │
│ wasep                      │
│ wasup                      │
└────────────────────────────┘
│                            │
│                            │
└────────────────────────────┘`},
			{fmt.Sprintf(":workspaceopen %s>:ramo w__", escapeInputPath(dir2)), // new workspace
				`┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│                            │
│                            │
│                            │
┌────────────────────────────┐
│ ramon w▐                   │
│ wasep                      │
│ wasup                      │
└────────────────────────────┘
│                            │
│                            │
└────────────────────────────┘`},
			{":wofo 8>:ramo w__", // empty workspace
				`┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│                            │
│                            │
│                            │
┌────────────────────────────┐
│ ramon w▐                   │
│ wasep                      │
│ wasup                      │
└────────────────────────────┘
│                            │
│                            │
└────────────────────────────┘`},
		}
		h := newSafeHandler(m)
		handlertest.TestHandlerSequence(t, h, 30, 15, cases)

		require.NoError(t, m.Close())
	})

	t.Run("is goroutine safe", func(t *testing.T) {
		dir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		dir2, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() {
			os.RemoveAll(dir)
			os.RemoveAll(dir2)
		})

		m := newTestWorkspaceManagerHandlerWithDir(t, defaultConfigWithWrap(false), dir,
			nopShutdownShaderConfig())

		const n = 50
		var wg sync.WaitGroup
		var errs [n]error

		wg.Add(n)
		for i := range n {
			go func(i int) {
				defer wg.Done()
				errs[i] = m.subscribeCommand(textapi.CommandManual{Name: "cmd" + strconv.Itoa(i)},
					text.FuncCommandHandler(func(context.Context, textapi.Command) error {
						return nil
					}, func(ctx context.Context, cmd textapi.Command) (
						iterator.Iterator[string], string, error,
					) {
						return iterator.FromSlice([]string{strconv.Itoa(i), strconv.Itoa(i + 1000)}), "", nil
					}))
			}(i)
		}
		wg.Wait()
		for _, err := range errs {
			require.NoError(t, err)
		}

		cases := []handlertest.SequenceTestCase{
			{":c0 1", // existing workspace
				`┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│                            │
│                            │
│                            │
┌────────────────────────────┐
│ cmd0 1▐                    │
│ 1000                       │
│                            │
└────────────────────────────┘
│                            │
│                            │
└────────────────────────────┘`},
		}
		h := newSafeHandler(m)
		handlertest.TestHandlerSequence(t, h, 30, 15, cases)

		require.NoError(t, m.Close())
	})
}

// escapeInputPath escapes a filesystem path for interpolation into a
// legacy TestHandlerSequence input. The legacy syntax gives bare
// runes special meaning ('_' sleeps, ':' opens the command prompt,
// '>' is enter, ...) and types a '\\'-prefixed rune literally.
// macOS per-user temp roots (/var/folders/xx/<random>) can draw an
// '_' in their random segment, which would otherwise be swallowed
// as a sleep token and corrupt the typed path.
func escapeInputPath(p string) string {
	var b strings.Builder
	for _, r := range p {
		switch r {
		case ':', '`', '_', ' ', '^', '#', '$', '>', '<', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func TestExternalEvents(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		dir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		// The '_' in the pattern pins the legacy input-DSL escape
		// below: macOS per-user temp roots can draw an '_' in their
		// random segment, and an unescaped one is interpreted as a
		// sleep token instead of a typed rune.
		dir2, err := os.MkdirTemp("", "x_")
		require.NoError(t, err)
		t.Cleanup(func() {
			os.RemoveAll(dir)
			os.RemoveAll(dir2)
		})

		evsk := []textapi.EventType{
			textapi.EventTypeOpen,
			textapi.EventTypeFlush,
			textapi.EventTypeEdit,
			textapi.EventTypeClose,
		}
		var open, flush, edit, close atomic.Int64
		m := newTestWorkspaceManagerHandlerWithDir(t, defaultConfigWithWrap(false), dir,
			nopShutdownShaderConfig())
		sub := text.FuncEventHandler(func(_ context.Context, ev textapi.Event) bool {
			switch ev.Type {
			case textapi.EventTypeOpen:
				open.Add(1)
			case textapi.EventTypeFlush:
				flush.Add(1)
			case textapi.EventTypeEdit:
				edit.Add(1)
			case textapi.EventTypeClose:
				close.Add(1)
			}
			return false
		})
		m.mu.Lock()
		err = m.SubscribeEvents(evsk, sub)
		m.mu.Unlock()
		require.NoError(t, err)

		cases := []handlertest.SequenceTestCase{
			{":edit a>", // existing workspace
				`┌━━━─────────────────────────┐
│o a                         │
├────────────────────────────┤
│▐                           │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                      NORMAL│
└────────────────────────────┘`},
			{"iabc<:write>", // edit + flush (async, drained by testEx.Handle wrapper)
				`┌━━━─────────────────────────┐
│o a                         │
├────────────────────────────┤
│ab▐                         │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                      NORMAL│
└────────────────────────────┘`},
			{":tabclose>", // close
				`┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│                            │
│                            │
│                            │
│                            │
│     workspaceWallpaper     │
│                            │
│                            │
│                            │
│                            │
│                            │
└────────────────────────────┘`},
			{fmt.Sprintf(":workspaceopen file\\://%s>:edit b>", escapeInputPath(dir2)), // new workspace
				`┌━━━─────────────────────────┐
│o b                         │
├────────────────────────────┤
│▐                           │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                      NORMAL│
├────────────────────────────┤
│1 1  2 2                    │
└─────━━━────────────────────┘`},
			{"iabc<:write>", // edit + flush
				`┌━━━─────────────────────────┐
│o b                         │
├────────────────────────────┤
│ab▐                         │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                      NORMAL│
├────────────────────────────┤
│1 1  2 2                    │
└─────━━━────────────────────┘`},
			{":tabclose>", // close
				`┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│                            │
│                            │
│                            │
│     workspaceWallpaper     │
│                            │
│                            │
│                            │
│                            │
├────────────────────────────┤
│1 1  2 2                    │
└─────━━━────────────────────┘`},
			{":wofo 8>:edit c>", // empty workspace
				`┌━━━─────────────────────────┐
│o c                         │
├────────────────────────────┤
│▐                           │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                      NORMAL│
├────────────────────────────┤
│1 1  2 2  8                 │
└──────────━─────────────────┘`},
			{"iabc<:write>", // edit + flush
				`┌━━━─────────────────────────┐
│o c                         │
├────────────────────────────┤
│ab▐                         │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                      NORMAL│
├────────────────────────────┤
│1 1  2 2  8                 │
└──────────━─────────────────┘`},
			{":tabclose>", // close
				`┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│                            │
│                            │
│                            │
│     workspaceWallpaper     │
│                            │
│                            │
│                            │
│                            │
├────────────────────────────┤
│1 1  2 2  8                 │
└──────────━─────────────────┘`},
		}
		h := newSafeHandler(m)
		handlertest.TestHandlerSequence(t, h, 30, 15, cases)

		assert.Equal(t, 3, int(open.Load()))
		assert.Equal(t, 3, int(flush.Load()))
		assert.Equal(t, 9, int(edit.Load()))
		assert.Equal(t, 3, int(close.Load()))

		m.mu.Lock()
		ok, err := m.UnsubscribeEvents(sub)
		m.mu.Unlock()
		require.NoError(t, err)
		require.True(t, ok)

		cases = []handlertest.SequenceTestCase{
			{":wofo 2>:woc>:wofo 1>",
				`┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│                            │
│                            │
│                            │
│                            │
│     workspaceWallpaper     │
│                            │
│                            │
│                            │
│                            │
│                            │
└────────────────────────────┘`},
			{":edit a>",
				`┌━━━─────────────────────────┐
│o a                         │
├────────────────────────────┤
│▐bc                         │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                      NORMAL│
└────────────────────────────┘`},
			{"iabc<:write>",
				`┌━━━─────────────────────────┐
│o a                         │
├────────────────────────────┤
│ab▐abc                      │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                      NORMAL│
└────────────────────────────┘`},
			{":tabclose>",
				`┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│                            │
│                            │
│                            │
│                            │
│     workspaceWallpaper     │
│                            │
│                            │
│                            │
│                            │
│                            │
└────────────────────────────┘`},
			{fmt.Sprintf(":workspaceopen file\\://%s>:edit b>", escapeInputPath(dir2)), // new workspace
				`┌━━━─────────────────────────┐
│o b                         │
├────────────────────────────┤
│▐bc                         │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                      NORMAL│
├────────────────────────────┤
│1 1  2 2                    │
└─────━━━────────────────────┘`},
			{"iabc<:write>",
				`┌━━━─────────────────────────┐
│o b                         │
├────────────────────────────┤
│ab▐abc                      │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                      NORMAL│
├────────────────────────────┤
│1 1  2 2                    │
└─────━━━────────────────────┘`},
			{":tabclose>",
				`┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│                            │
│                            │
│                            │
│     workspaceWallpaper     │
│                            │
│                            │
│                            │
│                            │
├────────────────────────────┤
│1 1  2 2                    │
└─────━━━────────────────────┘`},
			{":wofo 8>:edit c>",
				`┌━━━─────────────────────────┐
│o c                         │
├────────────────────────────┤
│▐bc                         │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                      NORMAL│
├────────────────────────────┤
│1 1  2 2  8                 │
└──────────━─────────────────┘`},
			{"iabc<:write>",
				`┌━━━─────────────────────────┐
│o c                         │
├────────────────────────────┤
│ab▐abc                      │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                      NORMAL│
├────────────────────────────┤
│1 1  2 2  8                 │
└──────────━─────────────────┘`},
			{":tabclose>",
				`┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│                            │
│                            │
│                            │
│     workspaceWallpaper     │
│                            │
│                            │
│                            │
│                            │
├────────────────────────────┤
│1 1  2 2  8                 │
└──────────━─────────────────┘`},
		}

		handlertest.TestHandlerSequence(t, h, 30, 15, cases)
		assert.Equal(t, 3, int(open.Load()))
		assert.Equal(t, 3, int(flush.Load()))
		assert.Equal(t, 9, int(edit.Load()))
		assert.Equal(t, 3, int(close.Load()))

		require.NoError(t, m.Close())
	})
}

func TestWorkspaceCommands(t *testing.T) {
	dir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	dir2, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	dir3, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(dir)
		os.RemoveAll(dir2)
		os.RemoveAll(dir3)
	})

	uri1, err := workspaceapi.ParseURI("memory://" + dir)
	require.NoError(t, err)
	uri2, err := workspaceapi.ParseURI("file://" + dir2)
	require.NoError(t, err)
	uri3, err := workspaceapi.ParseURI("file://" + dir3)
	require.NoError(t, err)

	abcCmd := textapi.CommandManual{Name: "tttt"}
	xyzCmd := textapi.CommandManual{Name: "xyz"}

	var abc, xyz atomic.Int64
	m := newTestWorkspaceManagerHandlerWithDir(t, defaultConfigWithWrap(false), dir,
		nopShutdownShaderConfig())
	sub := text.FuncCommandHandler(func(_ context.Context, cmd textapi.Command) error {
		switch cmd.Name {
		case "tttt":
			abc.Add(1)
		case "xyz":
			xyz.Add(1)
		default:
			return errors.New("not cool, man")
		}
		return nil
	}, func(ctx context.Context, cmd textapi.Command) (
		iterator.Iterator[string], string, error,
	) {
		return iterator.Empty[string](), "", nil
	})

	m.mu.Lock()
	err = m.SubscribeCommandForWorkspace(uri1, xyzCmd, sub)
	require.NoError(t, err)
	err = m.SubscribeCommandForWorkspace(uri1, abcCmd, sub)
	require.NoError(t, err)

	require.NoError(t, m.workspaceManagerHandler.addOrCreateWorkspace(uri2))
	require.NoError(t, m.workspaceManagerHandler.addOrCreateWorkspace(uri3))
	// Release mu so the install goroutines (Phase B) can lock it
	// when they reach the rollback path / WaitGroup, then drain
	// pending workspaces, then re-acquire mu.
	m.mu.Unlock()
	m.quiesce()
	m.mu.Lock()

	err = m.SubscribeCommandForWorkspace(uri2, xyzCmd, sub)
	require.NoError(t, err)
	err = m.SubscribeCommandForWorkspace(uri2, abcCmd, sub)
	require.NoError(t, err)
	m.mu.Unlock()

	cases := []handlertest.SequenceTestCase{
		{":workspacefocus 1>:xyz>:tttt>",
			`┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│                            │
│                            │
│                            │
│     workspaceWallpaper     │
│                            │
│                            │
│                            │
│                            │
├────────────────────────────┤
│1 1  2 2  3 3               │
└━━━─────────────────────────┘`},
		{":workspacefocus 2>:tttt>:xyz>",
			`┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│                            │
│                            │
│                            │
│     workspaceWallpaper     │
│                            │
│                            │
│                            │
│                            │
├────────────────────────────┤
│1 1  2 2  3 3               │
└─────━━━────────────────────┘`},
		{":workspacefocus 3>:tttt>:xyz>",
			`┌──────────────┌─────────────┐
│              │ unknown     │
├──────────────│ command or  │
│              │ command     │
│              │ alias "xyz" │
│              └─────────────┘
│              ┌─────────────┐
│     workspace│ unknown     │
│              │ command or  │
│              │ command     │
│              │ alias       │
│              │ "tttt"      │
├──────────────└─────────────┤
│1 1  2 2  3 3               │
└──────────━━━───────────────┘`},
	}
	h := newSafeHandler(m)
	handlertest.TestHandlerSequence(t, h, 30, 15, cases)

	assert.Equal(t, 2, int(xyz.Load()))
	assert.Equal(t, 2, int(abc.Load()))

	err = m.UnsubscribeCommandForWorkspace(uri1, "tttt")
	require.NoError(t, err)

	err = m.UnsubscribeCommandForWorkspace(uri2, "xyz")
	require.NoError(t, err)

	cases = []handlertest.SequenceTestCase{
		{":noticloseall>:workspacefocus 1>:xyz>:tttt>",
			`┌──────────────┌─────────────┐
│              │ unknown     │
├──────────────│ command or  │
│              │ command     │
│              │ alias       │
│              │ "tttt"      │
│              └─────────────┘
│     workspaceWallpaper     │
│                            │
│                            │
│                            │
│                            │
├────────────────────────────┤
│1 1  2 2  3 3               │
└━━━─────────────────────────┘`},
		{":noticloseall>:workspacefocus 2>:tttt>:xyz>",
			`┌──────────────┌─────────────┐
│              │ unknown     │
├──────────────│ command or  │
│              │ command     │
│              │ alias "xyz" │
│              └─────────────┘
│                            │
│     workspaceWallpaper     │
│                            │
│                            │
│                            │
│                            │
├────────────────────────────┤
│1 1  2 2  3 3               │
└─────━━━────────────────────┘`},
		{":noticloseall>:workspacefocus 3>:tttt>:xyz>",
			`┌──────────────┌─────────────┐
│              │ unknown     │
├──────────────│ command or  │
│              │ command     │
│              │ alias "xyz" │
│              └─────────────┘
│              ┌─────────────┐
│     workspace│ unknown     │
│              │ command or  │
│              │ command     │
│              │ alias       │
│              │ "tttt"      │
├──────────────└─────────────┤
│1 1  2 2  3 3               │
└──────────━━━───────────────┘`},
	}

	handlertest.TestHandlerSequence(t, h, 30, 15, cases)
	assert.Equal(t, 3, int(xyz.Load()))
	assert.Equal(t, 3, int(abc.Load()))

	err = m.UnsubscribeCommandForWorkspace(uri2, "tttt")
	require.NoError(t, err)

	err = m.UnsubscribeCommandForWorkspace(uri1, "xyz")
	require.NoError(t, err)

	cases = []handlertest.SequenceTestCase{
		{":noticloseall>:workspacefocus 1>:xyz>:tttt>",
			`┌──────────────┌─────────────┐
│              │ unknown     │
├──────────────│ command or  │
│              │ command     │
│              │ alias       │
│              │ "tttt"      │
│              └─────────────┘
│     workspace┌─────────────┐
│              │ unknown     │
│              │ command or  │
│              │ command     │
│              │ alias "xyz" │
├──────────────└─────────────┤
│1 1  2 2  3 3               │
└━━━─────────────────────────┘`},
		{":noticloseall>:workspacefocus 2>:tttt>:xyz>",
			`┌──────────────┌─────────────┐
│              │ unknown     │
├──────────────│ command or  │
│              │ command     │
│              │ alias "xyz" │
│              └─────────────┘
│              ┌─────────────┐
│     workspace│ unknown     │
│              │ command or  │
│              │ command     │
│              │ alias       │
│              │ "tttt"      │
├──────────────└─────────────┤
│1 1  2 2  3 3               │
└─────━━━────────────────────┘`},
		{":noticloseall>:workspacefocus 3>:tttt>:xyz>",
			`┌──────────────┌─────────────┐
│              │ unknown     │
├──────────────│ command or  │
│              │ command     │
│              │ alias "xyz" │
│              └─────────────┘
│              ┌─────────────┐
│     workspace│ unknown     │
│              │ command or  │
│              │ command     │
│              │ alias       │
│              │ "tttt"      │
├──────────────└─────────────┤
│1 1  2 2  3 3               │
└──────────━━━───────────────┘`},
	}

	handlertest.TestHandlerSequence(t, h, 30, 15, cases)
	assert.Equal(t, 3, int(xyz.Load()))
	assert.Equal(t, 3, int(abc.Load()))

	require.NoError(t, m.Close())
}

func TestComponentOnTabsClickIntegration(t *testing.T) {
	// setup
	dir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(dir)
	})
	cfg := defaultCfg()
	mu, sched, drain := buildTestSchedulerForCfg(t, &cfg)
	manager := workspace.NewManager(config.NopConfig(), sched)
	require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme,
		workspace.NewMemoryScheme))

	var uri *workspaceapi.URI
	if dir != "" {
		var err error
		uri = new(workspaceapi.URI)
		*uri, err = workspaceapi.ParseURI(fmt.Sprintf("memory://%s", dir))
		require.NoError(t, err)
	}
	runner := FuncExtensionsRunner(testRunnerFn)
	var called int
	m := newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
		uri, cfg, runner, nil, dir,
		func(i int) bool {
			called++
			return true
		}, nopShutdownShaderConfig())
	m.Resize(20, 8)

	// sut
	require.Equal(t, 0, called)

	m.mu.Lock()

	_, handled := m.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft})
	assert.True(t, handled)
	assert.Equal(t, 1, called)

	// the bottom row is a window resize grip: handled, but no tab click
	_, handled = m.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseY: 7})
	assert.True(t, handled)
	assert.Equal(t, 1, called)

	m.mu.Unlock()

	require.NoError(t, m.Close())
}

// TestWorkspaceBarTabClickIntegration is a black-box regression suite
// for the bottom workspace bar's mouse routing. Each case builds a
// (possibly sparse) workspace layout via the manager's internal
// helpers, dispatches a single term.EventMouse/MouseLeft event at
// chosen bar coordinates, and asserts the resulting screen with
// handlertest.RunHandlerSequence. Verification is purely from the
// rendered Draw output, never via private fields.
//
// The bar config sets focus_tab_attr to {bg: blue} so the focused tab's
// name cells render as the writer's BackgroundCh ('·'). That makes
// which slot gained focus directly observable in the expected string.
//
// Bar layout in numbers mode renders one bar tab per visible workspace
// as "<icon> <name>" with a two-space separator. With single-digit
// names each tab spans 5 columns (icon + space + name + separator).
func TestWorkspaceBarTabClickIntegration(t *testing.T) {
	const (
		width  = 30
		height = 9
		// barY is the on-screen Y of the workspace bar's tab row. The
		// bar sits one row above the bottom border.
		barY = height - 2
	)

	// screen builds the expected Draw output. The bar string encodes
	// the focused slot via '·' on the focused name (see writer setup).
	// hlStart/hlLen describe the focus-frame highlight rendered on the
	// bottom border row over the focused tab's cell columns.
	screen := func(bar string, hlStart, hlLen int) string {
		// Build the bottom border: '└' + 28 box chars + '┘'.
		bottom := []rune("└────────────────────────────┘")
		for i := 0; i < hlLen; i++ {
			bottom[1+hlStart+i] = '━'
		}
		return `┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│     workspaceWallpaper     │
│                            │
├────────────────────────────┤
` + bar + `
` + string(bottom)
	}

	type clickCase struct {
		name string
		// filled[i] indicates whether slot i should be populated.
		filled []bool
		// focusSlot is the slot to focus before the click.
		focusSlot int
		// mouseX is the X column of the click on the bar row.
		mouseX int
		// expected is the rendered screen after the click.
		expected string
	}

	cases := []clickCase{
		// Dense layout (3 filled): "1 1  2 2  3 3". Each filled tab
		// is 5 cells wide (icon + space + name + 2-space sep), so
		// tab 0 spans X∈[0,5], tab 1 spans X∈[6,10], tab 2 spans
		// X∈[11,15].
		{
			name:      "dense_click_first_tab_focuses_slot_1",
			filled:    []bool{true, true, true},
			focusSlot: 2, mouseX: 1,
			expected: screen("│1 ·  2 2  3 3               │", 0, 3),
		},
		{
			name:      "dense_click_middle_tab_focuses_slot_2",
			filled:    []bool{true, true, true},
			focusSlot: 0, mouseX: 7,
			expected: screen("│1 1  2 ·  3 3               │", 5, 3),
		},
		{
			name:      "dense_click_last_tab_focuses_slot_3",
			filled:    []bool{true, true, true},
			focusSlot: 0, mouseX: 12,
			expected: screen("│1 1  2 2  3 ·               │", 10, 3),
		},
		{
			name:      "dense_click_far_past_last_tab_keeps_focus",
			filled:    []bool{true, true, true},
			focusSlot: 0, mouseX: width - 2,
			expected: screen("│1 ·  2 2  3 3               │", 0, 3),
		},

		// Single empty middle slot — the original RUNE-126 bug. Bar
		// renders "1 1  3 3"; widths 5/5 → tab 0 X∈[0,5], tab 1
		// X∈[6,10]. Clicking inside tab 1 must focus slot 3, not 2.
		{
			name:      "middle_gap_click_first_visible_tab_focuses_slot_1",
			filled:    []bool{true, false, true},
			focusSlot: 2, mouseX: 1,
			expected: screen("│1 ·  3 3                    │", 0, 3),
		},
		{
			name:      "middle_gap_click_second_visible_tab_focuses_slot_3",
			filled:    []bool{true, false, true},
			focusSlot: 0, mouseX: 7,
			expected: screen("│1 1  3 ·                    │", 5, 3),
		},
		{
			name:      "middle_gap_click_past_last_visible_tab_keeps_focus",
			filled:    []bool{true, false, true},
			focusSlot: 0, mouseX: 15,
			expected: screen("│1 ·  3 3                    │", 0, 3),
		},

		// Two consecutive empty middle slots: bar renders "1 1  4 4".
		{
			name:      "double_gap_click_second_visible_tab_focuses_slot_4",
			filled:    []bool{true, false, false, true},
			focusSlot: 0, mouseX: 7,
			expected: screen("│1 1  4 ·                    │", 5, 3),
		},

		// Focused empty middle slot: bar renders "1 1  2    3 3".
		// The focused-but-empty entry has no icon, so its width is
		// only 3 cells (sep + name) → widths 5/3/5 → tab spans
		// X∈[0,5], X∈[6,8], X∈[9,13].
		// Clicking the left or right filled tab moves focus off the
		// empty slot, whiy slot, which then disappears from the bar — the bar
		// collapses back to the dense two-tab layout.
		{
			name:      "focused_empty_middle_click_left_filled_focuses_slot_1",
			filled:    []bool{true, false, true},
			focusSlot: 1, mouseX: 1,
			expected: screen("│1 ·  3 3                    │", 0, 3),
		},
		{
			name:      "focused_empty_middle_click_right_filled_focuses_slot_3",
			filled:    []bool{true, false, true},
			focusSlot: 1, mouseX: 10,
			expected: screen("│1 1  3 ·                    │", 5, 3),
		},

		// Focused empty first slot: bar renders "1  2 2  3 3" with
		// widths 3/5/5 → tab 0 X∈[0,3], tab 1 X∈[4,8], tab 2 X∈[9,13].
		// Clicking tab 1 or tab 2 unfocuses slot 0, so its empty
		// entry vanishes and the bar collapses left.
		{
			name:      "focused_empty_first_click_second_visible_focuses_slot_2",
			filled:    []bool{false, true, true},
			focusSlot: 0, mouseX: 5,
			expected: screen("│2 ·  3 3                    │", 0, 3),
		},
		{
			name:      "focused_empty_first_click_third_visible_focuses_slot_3",
			filled:    []bool{false, true, true},
			focusSlot: 0, mouseX: 10,
			expected: screen("│2 2  3 ·                    │", 5, 3),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultConfigWithWrap(false)
			// Configure attrs so the focused tab's name renders as
			// '·' (the writer's BackgroundCh) and the unfocused
			// tabs render as their literal name characters.
			cfg.cfg["browser"] = map[string]any{
				"workspace_bar":      "number",
				"focus_tab_attr":     map[string]any{"bg": "blue"},
				"non_focus_tab_attr": map[string]any{"bg": "default"},
			}
			m := newTestWorkspaceManagerHandlerWithDir(t, cfg, "",
				nopShutdownShaderConfig())
			t.Cleanup(func() { require.NoError(t, m.Close()) })

			// addWorkspace is async; the default scheduleNextTick
			// stub serialises Phase C under m.mu on a fresh
			// goroutine. Drive each install fully (Phase B+C)
			// before invoking the next internal helper so the
			// subsequent switch/close calls observe a consistent
			// h.workspaces snapshot.
			for range tc.filled {
				uri, err := workspaceapi.ParseURI("file://" + t.TempDir())
				require.NoError(t, err)
				require.NoError(t, m.addOrCreateWorkspace(uri))
				m.quiesce()
			}
			m.mu.Lock()
			for i, f := range tc.filled {
				if f {
					continue
				}
				require.True(t, m.switchToWorkspace(i))
				_, _, err := m.closeWorkspace()
				require.NoError(t, err)
			}
			require.True(t, m.switchToWorkspace(tc.focusSlot))
			m.mu.Unlock()

			// Black-box verification: route the mouse click through
			// the full handler chain and assert the rendered screen
			// via RunHandlerSequence with no key input.
			h := newSafeHandler(m)
			writer := term.NewStringWriter(width, height)
			writer.BackgroundCh = '·'
			h.Resize(width, height)
			h.Handle(term.Event{
				Type: term.EventMouse, Key: term.MouseLeft,
				MouseX: tc.mouseX, MouseY: barY,
			})
			handlertest.RunHandlerSequenceWriter(t, writer, h,
				width, height, []handlertest.SequenceTestCase{
					{InputSequence: "", Expected: tc.expected},
				})
		})
	}
}

func TestWorkspaceManagerCreateWorkspace(t *testing.T) {
	m := newTestWorkspaceManagerHandler(t, defaultCfg(), nil, nopShutdownShaderConfig())
	t.Cleanup(func() { m.Close() })

	// create new temp dir, with consistent name, so test below works
	const (
		tempDir  = "/tmp/TestWorkspaceManagerCreateWorkspace"
		tempDir2 = "/tmp/TestWorkspaceManagerCreateWorkspace2"
	)
	for _, tempDir := range []string{tempDir, tempDir2} {
		err := os.MkdirAll(tempDir, 0600)
		require.NoError(t, err)
		err = os.RemoveAll(tempDir)
		require.NoError(t, err)
		t.Cleanup(func() {
			os.RemoveAll(tempDir)
		})
	}

	cases := []handlertest.SequenceTestCase{
		{fmt.Sprintf(":workspaceopen file\\://%s>", tempDir),
			`┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│                            │
█●████████████████████████████
│                            │
│  workspace with URI        │
│  file:///tmp/TestWorkspac  │
│  eManagerCreateWorkspace   │
│  does not exist. Do you    │
│  want to create it?        │
│                            │
│                            │
│      Yes          No       │
└────────────────────────────┘
│                            │
│                            │
│                            │
└────────────────────────────┘`},
		{"y>",
			`┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│     workspaceWallpaper     │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
├────────────────────────────┤
│1 1  2 2                    │
└─────━━━────────────────────┘`},
		{fmt.Sprintf(":workspaceopen file\\://%s>", tempDir2),
			`┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
█●████████████████████████████
│                            │
│  workspace with URI        │
│  file:///tmp/TestWorkspac  │
│  eManagerCreateWorkspace2  │
│  does not exist. Do you    │
│  want to create it?        │
│                            │
│                            │
│      Yes          No       │
└────────────────────────────┘
│                            │
│                            │
├────────────────────────────┤
│1 1  2 2                    │
└─────━━━────────────────────┘`},
		{"y>",
			`┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│     workspaceWallpaper     │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
├────────────────────────────┤
│1 1  2 2  3 3               │
└──────────━━━───────────────┘`},
	}

	h := newSafeHandler(m)
	handlertest.TestHandlerSequence(t, h, 30, 20, cases)

	// test that they indeed exist
	for _, tempDir := range []string{tempDir, tempDir2} {
		fs, err := os.Stat(tempDir)
		require.NoError(t, err)
		require.True(t, fs.IsDir())
	}
}

// TestWorkspaceManagerCreateWorkspaceQuotedPath guards the fix for RUNE-120:
// the modal command prompt must respect bash-style quoting/escaping so that
// directory paths containing spaces survive `:` dispatch unchanged. Each case
// drives a separate fresh manager so the variants can be asserted independently.
func TestWorkspaceManagerCreateWorkspaceQuotedPath(t *testing.T) {
	parent := t.TempDir()
	// The escape variant exercises raw backslash space escapes which only
	// guard whitespace; shell metacharacters such as parens still need to be
	// quoted. Keep the directory name space-only so the test focuses on the
	// space-escaping behaviour without dragging in unrelated metacharacter
	// quoting concerns.
	dirEscape := filepath.Join(parent, "Unstable Build escape")
	dirSingle := filepath.Join(parent, "Unstable Build (single)")
	dirDouble := filepath.Join(parent, "Unstable Build (double)")
	for _, d := range []string{dirEscape, dirSingle, dirDouble} {
		require.NoError(t, os.MkdirAll(d, 0700))
	}

	cases := []struct {
		name string
		// raw is the buffer the prompt should receive after the
		// leading "workspaceopen ". feedLiteral emits each rune as a
		// literal key event; spaces are sent as KeySpace events to
		// match the runtime keypress path.
		raw  string
		path string
	}{
		{
			name: "backslash-escaped spaces",
			raw:  strings.ReplaceAll(dirEscape, " ", `\ `),
			path: dirEscape,
		},
		{
			name: "single-quoted path",
			raw:  fmt.Sprintf("'%s'", dirSingle),
			path: dirSingle,
		},
		{
			name: "double-quoted path",
			raw:  fmt.Sprintf(`"%s"`, dirDouble),
			path: dirDouble,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestWorkspaceManagerHandler(t, defaultCfg(), nil,
				nopShutdownShaderConfig())
			t.Cleanup(func() { m.Close() })
			m.forceSyncCommandPrompt = true
			m.Resize(30, 20)

			h := newSafeHandler(m)
			// open the modal command prompt (Ctrl+\\, see defaultCfg).
			h.Handle(term.Event{Type: term.EventKey,
				Mod: term.ModCtrl, Ch: '\\'})
			feedLiteral(t, h, "workspaceopen ")
			feedLiteral(t, h, tc.raw)
			h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})

			require.Equal(t, 2, m.workspaceCount,
				"expected the new workspace to be registered alongside the default one")

			uri, err := m.homeWorkspace.URI(tc.path)
			require.NoError(t, err)
			var found bool
			for _, w := range m.workspaces {
				if w == nil {
					continue
				}
				if w.uri == uri {
					found = true
					break
				}
			}
			require.True(t, found,
				"expected to find workspace registered at %s", uri)
		})
	}
}

// feedLiteral writes each rune in s to h as a regular key event. Space is
// translated to KeySpace to match the runtime keypress path.
func feedLiteral(t *testing.T, h tui.Handler, s string) {
	t.Helper()
	for _, r := range s {
		switch r {
		case ' ':
			h.Handle(term.Event{Type: term.EventKey, Key: term.KeySpace})
		default:
			h.Handle(term.Event{Type: term.EventKey, Ch: r})
		}
	}
}

func newTestWorkspaceManagerHandlerWithManager(
	t *testing.T, manager *workspace.Manager,
	mu *sync.Mutex, drain func() uint64,
	uri workspaceapi.URI, cfg ideConfig,
) *testWorkspaceManagerHandler {
	dir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	return newTestWorkspaceManagerHandlerWithManagerMu(t, manager,
		mu, drain, &uri, cfg, FuncExtensionsRunner(testRunnerFn), nil, dir, nil,
		nopShutdownShaderConfig())
}

// newTestWorkspaceManagerHandlerWithManagerMu is like
// newTestWorkspaceManagerHandler for callers that built the manager
// themselves: they must give the manager the same test scheduler
// installed on cfg.scheduleNextTick and thread the shared event-loop
// mutex and drain function in (usually via buildTestSchedulerForCfg),
// so manager-scheduled work runs under the same lock as handler
// work. Pass nil mu/drain only when the manager schedules nothing;
// the helper then builds a scheduler bound to its own mu.
func newTestWorkspaceManagerHandlerWithManagerMu(
	t *testing.T, manager *workspace.Manager,
	mu *sync.Mutex, drainSched func() uint64,
	uri *workspaceapi.URI, cfg ideConfig, runner ExtensionsRunner,
	extensions map[string]Extension, dir string,
	onTabsClick func(int) bool,
	shutdownShaderCfg shutdownShaderConfig,
	prepare ...func(*workspaceManagerHandler),
) *testWorkspaceManagerHandler {
	homeURI, err := workspaceapi.ParseURI("memory:///home")
	require.NoError(t, err)

	m := new(testWorkspaceManagerHandler)
	m.workspaceManagerHandler = new(workspaceManagerHandler)
	// ensure that command manual is never shown while preserving any
	// command customizations the caller seeded (e.g. key bindings)
	if cmd, ok := cfg.cfg["command"].(map[string]any); ok {
		cmd["show_manual"] = false
		cmd["show_progress_hint"] = false
	} else {
		cfg.cfg["command"] = defaultCfg().cfg["command"]
	}

	shRunner := new(shaderRunner)
	shRunner.init(handler.Nop(), term.NopInterrupter(), term.Attributes{},
		shutdownShaderCfg, loadingShaderConfig{}, openShaderConfig{},
		component.FrameCharSetDefault())

	if mu == nil {
		mu = new(sync.Mutex)
	}
	// If cfg.scheduleNextTick was not pre-installed by the caller,
	// install the default tracked test scheduler bound to mu so it
	// mirrors the event-loop dispatch. The manager scheduler must
	// also point at this scheduler (callers that build the manager
	// outside this helper should use newTestScheduler).
	if cfg.scheduleNextTick == nil {
		cfg.scheduleNextTick, drainSched = newTestScheduler(t, mu)
	}

	notiConfig := notificationsConfig()
	// Frames only render notifications as styled cells, so a CI-only
	// failure banner (e.g. "Failed to load workspace ...") is
	// undiagnosable from a frame diff alone. Record every posted
	// notification in the test log; t.Log output is only printed for
	// failing tests. The observer can fire from background goroutines
	// that outlive the test (e.g. a slow external-editor teardown), and
	// t.Logf after test completion panics the whole binary, so gate it
	// behind a flag flipped during cleanup.
	var obsMu sync.Mutex
	obsDone := false
	t.Cleanup(func() {
		obsMu.Lock()
		obsDone = true
		obsMu.Unlock()
	})
	notiConfig.Observer = func(level notifications.Level, msg string) {
		obsMu.Lock()
		defer obsMu.Unlock()
		if obsDone {
			return
		}
		t.Logf("notification posted (level %d): %s", level, msg)
	}
	releaseManager := docrelease.NewManager(document.NewInMemoryService())
	var storage storageapi.Service = localstorage.New(
		context.Background(), dir, docbson.Marshaler())
	// init treats the storage as borrowed and never closes it, so
	// without this the helper leaks a firstmover gRPC server per test.
	t.Cleanup(func() { _ = storage.Close() })
	if newTestStorageWrap != nil {
		storage = newTestStorageWrap(storage)
	}
	publish := newTestPublishOverride
	if publish == nil {
		publish = func(term.Event) bool { return true }
	}
	m.tutorialsInstalled = func([]string) (bool, error) { return false, nil }
	for _, fn := range prepare {
		fn(m.workspaceManagerHandler)
	}
	err = m.workspaceManagerHandler.init(uri, homeURI, manager,
		notiConfig, cfg, storage, dir, publish, runner, pkgtrust.NewStore(dir, nil), mu, extensions,
		func() (ideConfig, error) { return cfg, nil },
		".sixrc", 0, 0, 0, '1', 0, 0, true, onTabsClick, releaseManager, shRunner, 0, nil, false,
		false, newCommandObserverRegistry())

	require.NoError(t, err)
	m.schedDrain = drainSched
	if uri != nil {
		// addWorkspace is async: init kicks off Phase B in a
		// goroutine and Phase C lands the install via
		// scheduleNextTick. Tests built on top of this helper expect
		// the boot workspace to be installed by the time they start
		// interacting with the handler, so block here until that has
		// happened (or fail with a clear message).
		m.waitForWorkspace(t, *uri)
	}
	return m
}

// newTestScheduler returns a scheduleNextTick stub bound to mu that
// mirrors the host event loop's UserFunc dispatch (gui.Update at
// term/gui/gui.go:248): fn runs on a fresh goroutine while holding
// mu.
//
// The returned drain function waits until the queue is empty and no
// callback is running, then reports the total number of callbacks
// executed so far. quiesceHandler compares that count across passes
// to detect whether a drain did any work.
//
// Dispatch stays parked until the first drain call. The host loop
// only pumps UserFunc events once it is running, so callbacks
// scheduled while the IDE or handler is still being constructed must
// queue rather than run alongside the constructor — running them
// early races the constructor's unsynchronized wiring. Every harness
// quiesces (and therefore drains) right after construction, which is
// the moment the "loop" starts.
func newTestScheduler(t *testing.T, mu sync.Locker) (
	sched func(func()) bool, drain func() uint64,
) {
	// A single consumer goroutine drains a FIFO queue, running each
	// callback under mu in enqueue order. This mirrors the host event
	// loop's UserFunc serialization (callbacks scheduled with
	// ScheduleNextTick run later, one at a time, in order) so tests
	// observe the same ordering production relies on. Enqueuing never
	// touches mu, so a caller holding mu (event-loop handlers) does not
	// deadlock against the consumer.
	var schedMu sync.Mutex
	schedCond := sync.NewCond(&schedMu)
	var queue []func()
	var executed uint64
	running := false
	stopped := false
	started := false
	go debug.CapturePanicReport(func() {
		for {
			schedMu.Lock()
			for (len(queue) == 0 || !started) && !stopped {
				schedCond.Wait()
			}
			if stopped {
				schedMu.Unlock()
				return
			}
			fn := queue[0]
			queue = queue[1:]
			running = true
			schedMu.Unlock()

			mu.Lock()
			fn()
			mu.Unlock()

			schedMu.Lock()
			executed++
			running = false
			schedCond.Broadcast()
			schedMu.Unlock()
		}
	})
	// Registered before any caller cleanup so it runs last (LIFO):
	// teardown that still schedules callbacks keeps a live consumer.
	t.Cleanup(func() {
		schedMu.Lock()
		stopped = true
		schedCond.Broadcast()
		schedMu.Unlock()
	})
	sched = func(fn func()) bool {
		schedMu.Lock()
		queue = append(queue, fn)
		schedCond.Broadcast()
		schedMu.Unlock()
		return true
	}
	// drain blocks until the queue is empty and no callback is running,
	// including callbacks enqueued by previously-running callbacks.
	drain = func() uint64 {
		schedMu.Lock()
		defer schedMu.Unlock()
		if !started {
			started = true
			schedCond.Broadcast()
		}
		for len(queue) > 0 || running {
			schedCond.Wait()
		}
		return executed
	}
	return sched, drain
}

// buildTestSchedulerForCfg builds a fresh mu + tracked test
// scheduler and installs the scheduler on cc.scheduleNextTick if it
// is not already set. The returned mu and drain func are intended to
// be threaded through newTestWorkspaceManagerHandlerWithManagerMu so
// the workspace.Manager, the IDE event-loop locker and the cfg
// scheduler all share the same goroutine/lock pair.
func buildTestSchedulerForCfg(t *testing.T, cc *ideConfig) (
	*sync.Mutex, func(func()) bool, func() uint64,
) {
	mu := new(sync.Mutex)
	sched, drain := newTestScheduler(t, mu)
	if cc.scheduleNextTick == nil {
		cc.scheduleNextTick = sched
	}
	return mu, sched, drain
}

// waitForWorkspace blocks until uri has been installed into
// h.workspaces. It is the test counterpart to the async addWorkspace
// contract: production code returns to the event loop immediately
// while Phase B runs, so tests that immediately read
// m.focusHandler() / m.workspaces must wait first.
func (m *testWorkspaceManagerHandler) waitForWorkspace(
	t *testing.T, uri workspaceapi.URI,
) {
	t.Helper()
	m.quiesce()
	m.mu.Lock()
	_, installed := m.findInstalledSlot(uri)
	m.mu.Unlock()
	if !installed {
		t.Fatalf("waitForWorkspace: %s not installed after quiesce",
			uri.String())
	}
}

func newTestWorkspaceManagerHandlerWithDir(
	t *testing.T, cc ideConfig, dir string,
	shutdownShaderCfg shutdownShaderConfig,
) *testWorkspaceManagerHandler {
	mu, sched, drain := buildTestSchedulerForCfg(t, &cc)
	manager := workspace.NewManager(config.NopConfig(), sched)
	require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme,
		workspace.NewMemoryScheme))
	require.NoError(t, manager.RegisterScheme(workspace.FileScheme,
		workspace.NewFileScheme))

	var uri *workspaceapi.URI
	if dir != "" {
		var err error
		uri = new(workspaceapi.URI)
		*uri, err = workspaceapi.ParseURI(fmt.Sprintf("memory://%s", dir))
		require.NoError(t, err)
	}
	dataDir := dir
	if dataDir == "" {
		dir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() {
			os.RemoveAll(dir)
		})
		dataDir = dir
	}
	runner := FuncExtensionsRunner(testRunnerFn)
	return newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
		uri, cc, runner, nil, dataDir, nil, shutdownShaderCfg)
}

// newTestWorkspaceManagerHandlerWithDirs is like newTestWorkspaceManagerHandlerWithDir
// but allows the workspace dir and the pkgmanager dataDir to be specified
// independently. This is useful for tests that need the workspace URI to be a
// stable path (e.g. "/tmp") but must not share the system temp dir as the
// pkgmanager data dir, to avoid interfering with other tests and stale
// package-manager state.
func newTestWorkspaceManagerHandlerWithDirs(
	t *testing.T, cc ideConfig, dir, dataDir string,
	shutdownShaderCfg shutdownShaderConfig,
) *testWorkspaceManagerHandler {
	mu, sched, drain := buildTestSchedulerForCfg(t, &cc)
	manager := workspace.NewManager(config.NopConfig(), sched)
	require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme,
		workspace.NewMemoryScheme))
	require.NoError(t, manager.RegisterScheme(workspace.FileScheme,
		workspace.NewFileScheme))

	var uri *workspaceapi.URI
	if dir != "" {
		var err error
		uri = new(workspaceapi.URI)
		*uri, err = workspaceapi.ParseURI(fmt.Sprintf("memory://%s", dir))
		require.NoError(t, err)
	}
	if dataDir == "" {
		d, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(d) })
		dataDir = d
	}
	runner := FuncExtensionsRunner(testRunnerFn)
	return newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
		uri, cc, runner, nil, dataDir, nil, shutdownShaderCfg)
}

func newTestWorkspaceManagerHandler(
	t *testing.T, cc ideConfig, filenames []string,
	shutdownShaderCfg shutdownShaderConfig,
) *testWorkspaceManagerHandler {
	dir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(dir)
	})
	return newTestWorkspaceManagerHandlerWithDir(t, cc, dir, shutdownShaderCfg)
}

// deterministic usage of search list
type testWorkspaceManagerHandler struct {
	*workspaceManagerHandler
	forceSyncCommandPrompt bool
	// schedDrain waits until the test scheduler queue is empty and
	// reports how many callbacks have run in total; quiesce uses it
	// to detect when the system has stopped generating work. Nil for
	// tests that wire their own scheduler.
	schedDrain func() uint64
}

func (t *testWorkspaceManagerHandler) enableSyncCommandPrompt() {
	if t == nil || t.workspaceManagerHandler == nil {
		return
	}
	if t.empty != nil {
		t.empty.syncCommandPrompt = true
	}
	for _, w := range t.workspaces {
		if w != nil && w.ex != nil {
			w.ex.syncCommandPrompt = true
		}
	}
	if h := t.focusHandler(); h != nil {
		if ex, ok := h.(*ex); ok {
			ex.syncCommandPrompt = true
		}
		if wh, ok := h.(*workspaceHandler); ok && wh.ex != nil {
			wh.ex.syncCommandPrompt = true
		}
	}
}

// mimic ide.IDE
func (t *testWorkspaceManagerHandler) Close() error {
	t.workspaceManagerHandler.mu.Lock()
	defer t.workspaceManagerHandler.mu.Unlock()

	return t.workspaceManagerHandler.Close()
}

func (t *testWorkspaceManagerHandler) Handle(ev term.Event) (bool, bool) {
	if t.forceSyncCommandPrompt {
		t.enableSyncCommandPrompt()
	}
	quit, handle := t.workspaceManagerHandler.Handle(ev)
	if t.forceSyncCommandPrompt {
		t.enableSyncCommandPrompt()
	}
	handler := t.workspaceManagerHandler.focusHandler()
	ex, ok := handler.(*ex)
	if !ok {
		ex = handler.(*workspaceHandler).ex
	}
	ex.Wait()
	return quit, handle
}

// quiesce blocks until all async work started by previously handled
// events has finished: background workspace teardowns, pending
// workspace builds/installs, in-flight saves and reloads, and any
// callbacks they scheduled. The caller must NOT hold h.mu.
func (t *testWorkspaceManagerHandler) quiesce() {
	quiesceHandler(t.workspaceManagerHandler, t.schedDrain)
}

// quiesceHandler drains every async queue the handler feeds. The
// queues feed each other (a scheduled callback can open a workspace,
// a finished build schedules its install, a completed save schedules
// its completion callback), so a single pass over them is not enough:
// it loops until a full pass executes no scheduled callbacks, using
// the drain generation count as the progress signal.
func quiesceHandler(h *workspaceManagerHandler, drain func() uint64) {
	if drain == nil {
		drain = func() uint64 { return 0 }
	}
	gen := drain()
	for {
		h.waitClosing()
		h.pendingWG.Wait()
		h.waitInflight()
		next := drain()
		if next == gen {
			return
		}
		gen = next
	}
}

// locked runs fn under the event-loop lock. Tests that poke handler
// or ex state directly need it: every installed workspace has an
// fs-watcher goroutine dispatching events under the same lock.
func (t *testWorkspaceManagerHandler) locked(fn func()) {
	t.workspaceManagerHandler.mu.Lock()
	defer t.workspaceManagerHandler.mu.Unlock()
	fn()
}

// addOrCreateWorkspace shadows the embedded handler method to take
// the event-loop lock first. Production only invokes it from the
// event loop with mu held, and the test scheduler runs Phase C
// callbacks under the same mu on its own goroutine, so calling the
// embedded method bare from the test goroutine races on the
// handler's pending/closing maps. Tests that already hold mu for a
// wider critical section must call
// t.workspaceManagerHandler.addOrCreateWorkspace directly.
func (t *testWorkspaceManagerHandler) addOrCreateWorkspace(
	uri workspaceapi.URI,
) error {
	t.workspaceManagerHandler.mu.Lock()
	defer t.workspaceManagerHandler.mu.Unlock()
	return t.workspaceManagerHandler.addOrCreateWorkspace(uri)
}

func defaultCfg() ideConfig {
	return ideConfig{cfg: map[string]any{
		"clipboard": "memory",
		// keep the package install prompt deterministic in tests;
		// auto_install bypasses it entirely.
		"updates": map[string]any{"auto_install": false},
		"command": map[string]any{
			"show_manual":        false,
			"show_progress_hint": false,
			"key":                "<c-\\\\>", // see handlertest.TestHandlerIsolated
			"key_bindings": map[string]any{
				"1": "workspacefocus 1",
				"2": "workspacefocus 2",
				"3": "workspacefocus 3",
				"4": "workspacefocus 4",
				"5": "workspacefocus 5",
				"6": "workspacefocus 6",
				"7": "workspacefocus 7",
				"8": "workspacefocus 8",
				"9": "workspacefocus 9",
				"0": "workspacefocus 10",
			},
			"aliases": map[string]any{
				"addBlaBla": "workspaceopen memory:///blabla",
				"w":         "write!",
			},
		},
		"workspace": map[string]any{
			"wallpaper":    "workspaceWallpaper",
			"auto_restore": false,
		},
		"browser": map[string]any{
			"workspace_bar": "number",
			"window_manager": map[string]any{
				"no_max_size": false,
			},
		},
		"notifications": map[string]any{
			"progress_bar": false,
		},
	},
		configPath: "not-empty",
	}
}

func defaultConfigWithWrap(wrap bool) ideConfig {
	ret := defaultCfg()
	ret.cfg["editor"] = map[string]any{
		"modal": map[string]any{
			"wrap": wrap,
		},
	}
	return ret
}

type fnRunner struct {
	fn func(extensionID, path string, config config.Config) error
}

func (f fnRunner) Run(extensionID, path string, config config.Config) error {
	return f.fn(extensionID, path, config)
}

func (f fnRunner) Close() error {
	return nil
}

func (f fnRunner) WaitReady(ctx context.Context, id string) error {
	return nil
}

func newSafeHandler(m *testWorkspaceManagerHandler) *safeHandler {
	// Force the command Prompt into sync mode so completion runs
	// inline on the test goroutine. The async path spawns a raw
	// goroutine that iterates the prompt's history slice without
	// holding the harness lock; in production the single-threaded
	// event loop serialises everything so this is safe, but tests
	// drive Handle from the test goroutine while the prompt is still
	// iterating, which races with the next History.Add. See the data
	// race fixed for TestWorkspaceManagerHandlerDraw.
	m.forceSyncCommandPrompt = true
	return &safeHandler{
		Component: m,
		Handler:   m, mu: m.mu,
		quiesce: m.quiesce,
	}
}

func newWriterForAttrTesting(width, height int) *term.StringWriter {
	writer := term.NewStringWriter(width, height)
	writer.ForegroundCh = '#'
	return writer
}

// TestCloseWorkspaceRemovesClosedWorkspaceFromManager guards against a
// regression where closing a workspace via the IDE left the workspace
// rooted in workspace.Manager. Each closed workspace would keep its scheme
// (and the file/watcher state owned by the scheme) alive forever, which
// caused steady memory growth on workspace open/close cycles.
func TestCloseWorkspaceRemovesClosedWorkspaceFromManager(t *testing.T) {
	dir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	cfg := defaultCfg()
	mu, sched, drain := buildTestSchedulerForCfg(t, &cfg)
	manager := workspace.NewManager(config.NopConfig(), sched)
	require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme,
		workspace.NewMemoryScheme))

	uri, err := workspaceapi.ParseURI(fmt.Sprintf("memory:///%s", dir))
	require.NoError(t, err)

	runner := FuncExtensionsRunner(testRunnerFn)
	m := newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
		&uri, cfg, runner, nil, dir, nil, nopShutdownShaderConfig())
	t.Cleanup(func() { _ = m.Close() })

	// Sanity: workspace was added to the Manager.
	require.True(t, manager.HasWorkspace(uri),
		"workspace should be registered with the Manager after init")

	m.mu.Lock()
	err = m.commandCloseWorkspace()
	m.mu.Unlock()
	require.NoError(t, err)
	m.quiesce()
	require.Equal(t, 0, m.workspaceCount)

	require.False(t, manager.HasWorkspace(uri),
		"closing a workspace must remove it from workspace.Manager so its scheme can be GC'd")
}

// TestCloseWorkspaceClosesScheme guards the RUNE-180 invariant that
// :workspaceclose closes the underlying scheme, killing subprocesses
// whose lifetime is bound to the scheme ctx (via bluectx.First in
// fileScheme.StartCommand).
func TestCloseWorkspaceClosesScheme(t *testing.T) {
	dir := t.TempDir()

	cfg := defaultCfg()
	mu, sched, drain := buildTestSchedulerForCfg(t, &cfg)
	manager := workspace.NewManager(config.NopConfig(), sched)
	require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme,
		workspace.NewMemoryScheme))
	require.NoError(t, manager.RegisterScheme(workspace.FileScheme,
		workspace.NewFileScheme))

	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	runner := FuncExtensionsRunner(testRunnerFn)
	m := newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
		&uri, cfg, runner, nil, dir, nil, nopShutdownShaderConfig())
	t.Cleanup(func() { _ = m.Close() })

	require.True(t, manager.HasWorkspace(uri))

	cwd := m.workspaces[m.focus].cwd
	require.NotNil(t, cwd, "workspace handler must have cwd installed")
	ch := make(chan error, 1)
	_, err = cwd.StartCommand(context.Background(), workspaceapi.Cmd{
		Path:    "/bin/sh",
		Args:    []string{"-c", "sleep 30"},
		Watcher: workspaceapi.ChanProcessWatcher(ch),
	})
	require.NoError(t, err)

	m.mu.Lock()
	err = m.commandCloseWorkspace()
	m.mu.Unlock()
	require.NoError(t, err)
	require.Equal(t, 0, m.workspaceCount)
	require.False(t, manager.HasWorkspace(uri),
		"closing a workspace must remove it from workspace.Manager")

	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("subprocess survived :workspaceclose: the scheme " +
			"ctx was not canceled, so spawned commands leak")
	}
}

// TestReloadWorkspaceClosesAndReopensScheme guards the RUNE-180
// invariant that :workspacereload is a real close+open: the old scheme
// (and any subprocesses it owns) is torn down, and a fresh scheme is
// installed under the same URI. The pre-fix path kept the scheme alive
// across reload, leaking LSP/DAP children.
func TestReloadWorkspaceClosesAndReopensScheme(t *testing.T) {
	dir := t.TempDir()

	cfg := defaultCfg()
	mu, sched, drain := buildTestSchedulerForCfg(t, &cfg)
	manager := workspace.NewManager(config.NopConfig(), sched)
	require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme,
		workspace.NewMemoryScheme))
	require.NoError(t, manager.RegisterScheme(workspace.FileScheme,
		workspace.NewFileScheme))

	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	runner := FuncExtensionsRunner(testRunnerFn)
	m := newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
		&uri, cfg, runner, nil, dir, nil, nopShutdownShaderConfig())
	t.Cleanup(func() { _ = m.Close() })

	origCwd := m.workspaces[m.focus].cwd
	require.NotNil(t, origCwd)

	ch := make(chan error, 1)
	_, err = origCwd.StartCommand(context.Background(), workspaceapi.Cmd{
		Path:    "/bin/sh",
		Args:    []string{"-c", "sleep 30"},
		Watcher: workspaceapi.ChanProcessWatcher(ch),
	})
	require.NoError(t, err)

	m.mu.Lock()
	err = m.commandReloadWorkspace()
	m.mu.Unlock()
	require.NoError(t, err)
	m.quiesce()

	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("subprocess survived :workspacereload: the old " +
			"scheme was reused instead of closed+reopened, so LSP/DAP " +
			"children spawned by it leak across reload")
	}

	require.True(t, manager.HasWorkspace(uri),
		":workspacereload must re-register the workspace")
	newCwd := m.workspaces[m.focus].cwd
	require.NotNil(t, newCwd)
	// Compare interface values via %p: require.NotSame rejects
	// interface arguments, and we need concrete pointer identity to
	// distinguish scheme reuse from a fresh install.
	require.NotEqual(t,
		fmt.Sprintf("%p", origCwd), fmt.Sprintf("%p", newCwd),
		":workspacereload must install a fresh workspace.Workspace, "+
			"not reuse the cached one")

	ch2 := make(chan error, 1)
	_, err = newCwd.StartCommand(context.Background(), workspaceapi.Cmd{
		Path:    "/bin/sh",
		Args:    []string{"-c", "true"},
		Watcher: workspaceapi.ChanProcessWatcher(ch2),
	})
	require.NoError(t, err,
		"new scheme must be usable after :workspacereload")
	select {
	case <-ch2:
	case <-time.After(5 * time.Second):
		t.Fatal("new scheme did not run a trivial command after reload")
	}
}

// blockingCloser is an io.Closer whose Close blocks until release is
// closed. Tests inject it as a workspaceHandler.symbolDBCloser to hold
// the background workspace teardown open at a controlled point.
type blockingCloser struct {
	entered   chan struct{}
	release   chan struct{}
	completed atomic.Bool
	once      sync.Once
}

func newBlockingCloser() *blockingCloser {
	return &blockingCloser{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (b *blockingCloser) Close() error {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	b.completed.Store(true)
	return nil
}

// TestCloseWorkspaceReturnsBeforeTeardown guards the async-close
// contract: commandCloseWorkspace must return on the event loop (slot
// cleared, count decremented, manager detached) while the expensive
// teardown is still running in a background goroutine. It also hammers
// the detached workspace with scheme calls that overlap the teardown
// (including scheme.Close) so the race detector can catch
// close-vs-in-flight-call hazards.
func TestCloseWorkspaceReturnsBeforeTeardown(t *testing.T) {
	dir := t.TempDir()

	cfg := defaultCfg()
	mu, sched, drain := buildTestSchedulerForCfg(t, &cfg)
	manager := workspace.NewManager(config.NopConfig(), sched)
	require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme,
		workspace.NewMemoryScheme))

	uri, err := workspaceapi.ParseURI(fmt.Sprintf("memory://%s", dir))
	require.NoError(t, err)

	runner := FuncExtensionsRunner(testRunnerFn)
	m := newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
		&uri, cfg, runner, nil, dir, nil, nopShutdownShaderConfig())
	t.Cleanup(func() { _ = m.Close() })

	blocker := newBlockingCloser()
	m.mu.Lock()
	slot := m.focus
	hm := m.workspaces[slot]
	hm.symbolDBCloser = blocker
	err = m.commandCloseWorkspace()
	slotCleared := m.workspaces[slot] == nil
	count := m.workspaceCount
	// hm.cwd now holds the detached raw workspace. The background
	// goroutine only touches it after blocker.release, so this read
	// happens-before the teardown's writes via the release channel.
	cwd := hm.cwd
	m.mu.Unlock()
	require.NoError(t, err)

	// All of this must hold while the teardown is still blocked.
	require.True(t, slotCleared,
		"closeWorkspace must clear the slot on the event loop")
	require.Equal(t, 0, count)
	require.False(t, manager.HasWorkspace(uri),
		"closeWorkspace must detach the workspace from the manager "+
			"synchronously")
	select {
	case <-blocker.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("background teardown never reached the symbolDB closer")
	}
	require.False(t, blocker.completed.Load())

	// Hammer the detached workspace with scheme calls while the
	// teardown is parked, then keep hammering across the release so
	// the calls overlap the LSP/DAP/ctx/scheme close sequence. Errors
	// are expected once the scheme closes; -race and orderly loop
	// completion are the assertions.
	hammerStarted := make(chan struct{})
	hammerStop := make(chan struct{})
	hammerDone := make(chan struct{})
	var hammerFinished atomic.Bool
	go debug.CapturePanicReport(func() {
		defer close(hammerDone)
		first := true
		for {
			select {
			case <-hammerStop:
				hammerFinished.Store(true)
				return
			default:
			}
			if f, err := cwd.Create("hammer.txt"); err == nil {
				_, _ = f.Write([]byte("x"))
				_ = f.Close()
			}
			_, _ = cwd.Stat("hammer.txt")
			if f, err := cwd.Open("hammer.txt"); err == nil {
				_ = f.Close()
			}
			if first {
				first = false
				close(hammerStarted)
			}
		}
	})
	select {
	case <-hammerStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("scheme-call hammer never completed an iteration")
	}

	close(blocker.release)
	m.workspaceManagerHandler.waitClosing()
	require.True(t, blocker.completed.Load(),
		"background teardown must complete after the closer unblocks")

	close(hammerStop)
	select {
	case <-hammerDone:
	case <-time.After(10 * time.Second):
		t.Fatal("scheme-call hammer wedged against the closed scheme")
	}
	require.True(t, hammerFinished.Load(),
		"hammer must exit its loop normally; an early exit means a "+
			"scheme call panicked during teardown")
}

// TestReloadWaitsForCloseBeforeReopen guards the RUNE-180 real
// close+open invariant under the async close: :workspacereload must
// reserve the pending slot immediately, but only create the fresh
// scheme once the previous instance has fully torn down.
func TestReloadWaitsForCloseBeforeReopen(t *testing.T) {
	dir := t.TempDir()

	cfg := defaultCfg()
	mu, sched, drain := buildTestSchedulerForCfg(t, &cfg)
	manager := workspace.NewManager(config.NopConfig(), sched)
	require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme,
		workspace.NewMemoryScheme))

	uri, err := workspaceapi.ParseURI(fmt.Sprintf("memory://%s", dir))
	require.NoError(t, err)

	runner := FuncExtensionsRunner(testRunnerFn)
	m := newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
		&uri, cfg, runner, nil, dir, nil, nopShutdownShaderConfig())
	t.Cleanup(func() { _ = m.Close() })

	blocker := newBlockingCloser()
	m.mu.Lock()
	slot := m.focus
	origCwd := m.workspaces[slot].cwd
	m.workspaces[slot].symbolDBCloser = blocker
	err = m.commandReloadWorkspace()
	pending := m.isPending(uri)
	m.mu.Unlock()
	require.NoError(t, err)

	require.True(t, pending,
		"reload must reserve the pending slot immediately")

	// The old instance is still blocked in teardown: the fresh scheme
	// must not exist yet. Reads are taken under m.mu because the gated
	// waiter creates the scheme through the scheduler (which holds mu).
	m.mu.Lock()
	reopened := manager.HasWorkspace(uri)
	m.mu.Unlock()
	require.False(t, reopened,
		"reload must not recreate the scheme while the previous "+
			"instance is still closing")

	close(blocker.release)
	m.quiesce()
	m.waitForWorkspace(t, uri)

	m.mu.Lock()
	reopened = manager.HasWorkspace(uri)
	newCwd := m.workspaces[m.focus].cwd
	m.mu.Unlock()
	require.True(t, reopened,
		":workspacereload must re-register the workspace after the "+
			"old instance closed")
	require.NotEqual(t,
		fmt.Sprintf("%p", origCwd), fmt.Sprintf("%p", newCwd),
		"reload must install a fresh workspace.Workspace")
}

// TestOpenSameURIWhileClosingIsDeferred asserts that opening a URI
// whose previous instance is still tearing down defers the build until
// the close finishes, installs exactly one instance, and dedupes
// concurrent opens through the pending reservation.
func TestOpenSameURIWhileClosingIsDeferred(t *testing.T) {
	dir := t.TempDir()
	m := newTestWorkspaceManagerHandlerWithDir(t,
		defaultConfigWithWrap(false), dir, nopShutdownShaderConfig())
	t.Cleanup(func() { _ = m.Close() })
	m.quiesce()

	uri, err := workspaceapi.ParseURI(fmt.Sprintf("memory://%s", dir))
	require.NoError(t, err)

	blocker := newBlockingCloser()
	m.mu.Lock()
	m.workspaces[m.focus].symbolDBCloser = blocker
	err = m.commandCloseWorkspace()
	require.NoError(t, err)

	require.NoError(t, m.addWorkspace(uri, false, false, -1))
	require.True(t, m.isPending(uri),
		"open-while-closing must reserve a pending slot immediately")

	// A duplicate open during the window must be deduped by the
	// pending reservation, not queue a second build.
	require.NoError(t, m.addWorkspace(uri, false, false, -1))
	pendingCount := len(m.pending)
	m.mu.Unlock()
	require.Equal(t, 1, pendingCount,
		"duplicate open while closing must not reserve a second slot")

	close(blocker.release)
	m.quiesce()
	m.waitForWorkspace(t, uri)

	m.mu.Lock()
	installed := 0
	for _, w := range m.workspaces {
		if w != nil && w.uri.Equal(uri) {
			installed++
		}
	}
	count := m.workspaceCount
	m.mu.Unlock()
	require.Equal(t, 1, installed,
		"exactly one instance must be installed after the drain")
	require.Equal(t, 1, count)
}

// TestManagerCloseWaitsForBackgroundCloses asserts that shutting the
// handler down while a background workspace teardown is in flight
// blocks until that teardown completes, so shared resources are not
// freed under it.
func TestManagerCloseWaitsForBackgroundCloses(t *testing.T) {
	dir := t.TempDir()
	m := newTestWorkspaceManagerHandlerWithDir(t,
		defaultConfigWithWrap(false), dir, nopShutdownShaderConfig())
	m.quiesce()

	blocker := newBlockingCloser()
	m.mu.Lock()
	m.workspaces[m.focus].symbolDBCloser = blocker
	err := m.commandCloseWorkspace()
	m.mu.Unlock()
	require.NoError(t, err)

	closed := make(chan struct{})
	go debug.CapturePanicReport(func() {
		_ = m.Close()
		close(closed)
	})

	select {
	case <-closed:
		t.Fatal("Close returned while a background workspace teardown " +
			"was still blocked")
	case <-time.After(100 * time.Millisecond):
	}

	close(blocker.release)
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return after the background teardown " +
			"completed")
	}
	require.True(t, blocker.completed.Load())
}

// TestCloseGatedReloadCancelsQueuedReopen asserts that closing the
// pending (gated) workspace of an in-flight reload makes the gated
// waiter clean up its reservation without installing anything.
func TestCloseGatedReloadCancelsQueuedReopen(t *testing.T) {
	dir := t.TempDir()
	m := newTestWorkspaceManagerHandlerWithDir(t,
		defaultConfigWithWrap(false), dir, nopShutdownShaderConfig())
	t.Cleanup(func() { _ = m.Close() })
	m.quiesce()

	uri, err := workspaceapi.ParseURI(fmt.Sprintf("memory://%s", dir))
	require.NoError(t, err)

	blocker := newBlockingCloser()
	m.mu.Lock()
	m.workspaces[m.focus].symbolDBCloser = blocker
	err = m.commandReloadWorkspace()
	require.NoError(t, err)
	pending, ok := m.pending[uri.String()]
	require.True(t, ok, "reload must have reserved a gated pending slot")

	// Close the reloading (pending) workspace while the gate is held:
	// the pending-cancel branch of closeWorkspace must fire.
	m.focus = pending.slot
	_, _, err = m.closeWorkspace()
	require.NoError(t, err)
	require.False(t, m.isPending(uri),
		"closing a gated pending workspace must drop the reservation")
	m.mu.Unlock()

	close(blocker.release)
	m.quiesce()

	m.mu.Lock()
	_, installed := m.findInstalledSlot(uri)
	m.mu.Unlock()
	require.False(t, installed,
		"a canceled gated reload must not install a workspace")
}

// TestGatedReopenAbortsWhenSchedulerRejects covers the shutdown race
// where the host loop stops accepting ticks while a gated reopen is
// queued behind an in-flight close: the waiter must abort its pending
// reservation (releasing pendingWG) instead of leaking it, or Close's
// pendingWG.Wait would deadlock.
func TestGatedReopenAbortsWhenSchedulerRejects(t *testing.T) {
	dir := t.TempDir()
	m := newTestWorkspaceManagerHandlerWithDir(t,
		defaultConfigWithWrap(false), dir, nopShutdownShaderConfig())
	t.Cleanup(func() { _ = m.Close() })
	m.quiesce()

	uri, err := workspaceapi.ParseURI(fmt.Sprintf("memory://%s", dir))
	require.NoError(t, err)

	var reject atomic.Bool
	blocker := newBlockingCloser()
	m.mu.Lock()
	// Swapped before the gated waiter spawns so the field write
	// happens-before the goroutine's read.
	orig := m.scheduleNextTick
	m.scheduleNextTick = func(fn func()) bool {
		if reject.Load() {
			return false
		}
		return orig(fn)
	}
	m.workspaces[m.focus].symbolDBCloser = blocker
	err = m.commandCloseWorkspace()
	require.NoError(t, err)
	require.NoError(t, m.addWorkspace(uri, false, false, -1))
	require.True(t, m.isPending(uri),
		"open-while-closing must reserve a pending slot immediately")
	m.mu.Unlock()

	// The loop stops accepting ticks before the close finishes, so
	// the waiter wakes into the rejected-schedule path.
	reject.Store(true)
	close(blocker.release)
	m.quiesce()

	m.mu.Lock()
	pendingAfter := m.isPending(uri)
	_, installed := m.findInstalledSlot(uri)
	m.mu.Unlock()
	require.False(t, pendingAfter,
		"rejected gated reopen must drop its pending reservation")
	require.False(t, installed,
		"rejected gated reopen must not install a workspace")
}

type pendingTeardownWorkspace struct {
	workspace.Workspace
	closeCalls   atomic.Int32
	closeStarted chan struct{}
	closeDone    chan struct{}
	closeRelease <-chan struct{}
	startOnce    sync.Once
	doneOnce     sync.Once
}

func (w *pendingTeardownWorkspace) Close() error {
	w.closeCalls.Add(1)
	w.startOnce.Do(func() { close(w.closeStarted) })
	if w.closeRelease != nil {
		<-w.closeRelease
	}
	err := w.Workspace.Close()
	w.doneOnce.Do(func() { close(w.closeDone) })
	return err
}

type pendingTeardownTracker struct {
	target       workspaceapi.URI
	firstRelease <-chan struct{}
	mu           sync.Mutex
	workspaces   []*pendingTeardownWorkspace
}

func (t *pendingTeardownTracker) wrap(
	uri workspaceapi.URI, scheme schemeapi.Scheme, scheduleNextTick func(func()) bool,
) workspace.Workspace {
	base := workspace.NewSchemeWorkspace(uri, scheme, scheduleNextTick)
	if !uri.Equal(t.target) {
		return base
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	w := &pendingTeardownWorkspace{
		Workspace:    base,
		closeStarted: make(chan struct{}),
		closeDone:    make(chan struct{}),
	}
	if len(t.workspaces) == 0 {
		w.closeRelease = t.firstRelease
	}
	t.workspaces = append(t.workspaces, w)
	return w
}

func (t *pendingTeardownTracker) snapshot() []*pendingTeardownWorkspace {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]*pendingTeardownWorkspace(nil), t.workspaces...)
}

func newPendingTeardownTestHandler(
	t *testing.T, firstRelease <-chan struct{},
) (*testWorkspaceManagerHandler, *workspace.Manager, workspaceapi.URI, *pendingTeardownTracker) {
	t.Helper()
	cfg := defaultCfg()
	mu, scheduleNextTick, drain := buildTestSchedulerForCfg(t, &cfg)
	uri, err := workspaceapi.ParseURI(fmt.Sprintf("memory://%s", t.TempDir()))
	require.NoError(t, err)
	tracker := &pendingTeardownTracker{target: uri, firstRelease: firstRelease}
	manager := workspace.NewManagerWithWorkspaceFunc(
		config.NopConfig(), scheduleNextTick, tracker.wrap,
	)
	require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme, workspace.NewMemoryScheme))
	m := newTestWorkspaceManagerHandlerWithManagerMu(
		t, manager, mu, drain, nil, cfg, FuncExtensionsRunner(testRunnerFn), nil,
		t.TempDir(), nil, nopShutdownShaderConfig(),
	)
	t.Cleanup(func() { _ = m.Close() })
	return m, manager, uri, tracker
}

func TestPendingWorkspaceBuildFailureEvictsAndReopensFresh(t *testing.T) {
	releaseClose := make(chan struct{})
	var releaseCloseOnce sync.Once
	release := func() { releaseCloseOnce.Do(func() { close(releaseClose) }) }
	m, manager, uri, tracker := newPendingTeardownTestHandler(t, releaseClose)
	t.Cleanup(release)

	m.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	pending, err := m.reservePendingSlot(uri, -1, cancel)
	require.NoError(t, err)
	cwd, err := m.createWorkspaceScheme(uri)
	require.NoError(t, err)
	pending.cwd = cwd
	m.shaderRunner.startLoading()
	m.installPendingWorkspace(
		pending, uri, ctx, cancel, cwd, nil, errors.New("build failed"), false, false,
	)
	m.mu.Unlock()

	first := tracker.snapshot()[0]
	select {
	case <-first.closeStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("failed pending workspace was not closed")
	}
	require.False(t, manager.HasWorkspace(uri))
	require.Equal(t, int32(1), first.closeCalls.Load())

	m.mu.Lock()
	require.NoError(t, m.addWorkspace(uri, false, false, -1))
	m.mu.Unlock()
	require.Len(t, tracker.snapshot(), 1,
		"same-URI reopen must wait until failed workspace teardown completes")

	release()
	m.quiesce()
	m.waitForWorkspace(t, uri)
	require.Len(t, tracker.snapshot(), 2)
	require.True(t, manager.HasWorkspace(uri))
	require.Equal(t, int32(1), first.closeCalls.Load())

	m.mu.Lock()
	m.beginPendingWorkspaceTeardown(pending)
	m.mu.Unlock()
	m.finishPendingWorkspaceTeardown(pending)
	m.waitClosing()
	require.True(t, manager.HasWorkspace(uri),
		"late teardown from the failed pending build must not remove its successor")
	require.Equal(t, int32(1), first.closeCalls.Load())

	stale := &pendingWorkspace{uri: uri, cwd: cwd}
	m.mu.Lock()
	m.beginPendingWorkspaceTeardown(stale)
	m.mu.Unlock()
	m.finishPendingWorkspaceTeardown(stale)
	require.True(t, manager.HasWorkspace(uri),
		"teardown without exact pending identity must not detach the successor")
	require.Equal(t, int32(1), first.closeCalls.Load())
}

type closeUnblocksOpenScheme struct {
	schemeapi.Scheme
	openStarted chan struct{}
	closed      chan struct{}
	openOnce    sync.Once
	closeOnce   sync.Once
}

func (s *closeUnblocksOpenScheme) OpenFile(
	path string, flag int, perm os.FileMode,
) (workspaceapi.File, error) {
	if path != ".sixrc" {
		return s.Scheme.OpenFile(path, flag, perm)
	}
	s.openOnce.Do(func() { close(s.openStarted) })
	<-s.closed
	return nil, errors.New("scheme closed")
}

func (s *closeUnblocksOpenScheme) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return s.Scheme.Close()
}

func TestPendingWorkspaceCancellationClosesSchemeToAbortBuild(t *testing.T) {
	cfg := defaultCfg()
	mu, scheduleNextTick, drain := buildTestSchedulerForCfg(t, &cfg)
	manager := workspace.NewManager(config.NopConfig(), scheduleNextTick)
	require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme,
		workspace.NewMemoryScheme))

	const schemeName = "closeabort"
	var blocked *closeUnblocksOpenScheme
	require.NoError(t, manager.RegisterScheme(schemeName,
		func(ctx context.Context, cfg config.Config, uri workspaceapi.URI) (
			schemeapi.Scheme, error,
		) {
			inner, err := workspace.NewInMemorySchemeFunc(schemeName)(ctx, cfg, uri)
			if err != nil {
				return nil, err
			}
			blocked = &closeUnblocksOpenScheme{
				Scheme:      inner,
				openStarted: make(chan struct{}),
				closed:      make(chan struct{}),
			}
			return blocked, nil
		}))

	m := newTestWorkspaceManagerHandlerWithManagerMu(
		t, manager, mu, drain, nil, cfg, FuncExtensionsRunner(testRunnerFn), nil,
		t.TempDir(), nil, nopShutdownShaderConfig(),
	)
	t.Cleanup(func() { _ = m.Close() })
	uri, err := workspaceapi.ParseURI(schemeName + ":///workspace")
	require.NoError(t, err)

	m.mu.Lock()
	require.NoError(t, m.addWorkspace(uri, false, false, -1))
	pending := m.pending[uri.String()]
	m.mu.Unlock()
	require.NotNil(t, pending)

	select {
	case <-blocked.openStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("workspace build did not reach the blocking scheme call")
	}

	m.mu.Lock()
	m.focus = pending.slot
	_, _, err = m.closeWorkspace()
	m.mu.Unlock()
	require.NoError(t, err)

	drained := make(chan struct{})
	go debug.CapturePanicReport(func() {
		m.pendingWG.Wait()
		close(drained)
	})
	select {
	case <-drained:
	case <-time.After(time.Second):
		_ = blocked.Close()
		select {
		case <-drained:
		case <-time.After(10 * time.Second):
			t.Fatal("pending build remained blocked after forced scheme close")
		}
		t.Fatal("canceling a pending build did not close the scheme to abort its workspace call")
	}

	m.waitClosing()
	require.False(t, manager.HasWorkspace(uri))
}

func TestPendingWorkspaceCancellationKeepsReopenGatedUntilBuildReleases(t *testing.T) {
	releaseClose := make(chan struct{})
	var releaseCloseOnce sync.Once
	release := func() { releaseCloseOnce.Do(func() { close(releaseClose) }) }
	m, manager, uri, tracker := newPendingTeardownTestHandler(t, releaseClose)
	t.Cleanup(release)
	releaseBuild := make(chan struct{})

	m.mu.Lock()
	_, cancel := context.WithCancel(context.Background())
	pending, err := m.reservePendingSlot(uri, -1, cancel)
	require.NoError(t, err)
	cwd, err := m.createWorkspaceScheme(uri)
	require.NoError(t, err)
	pending.cwd = cwd
	m.shaderRunner.startLoading()
	m.pendingWG.Add(1)
	go debug.CapturePanicReport(func() {
		<-releaseBuild
		m.abortPendingBuild(pending, nil, cancel)
	})
	m.focus = pending.slot
	_, _, err = m.closeWorkspace()
	require.NoError(t, err)
	require.NoError(t, m.addWorkspace(uri, false, false, -1))
	m.mu.Unlock()

	first := tracker.snapshot()[0]
	require.False(t, manager.HasWorkspace(uri))
	select {
	case <-first.closeStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("pending cancellation did not start the workspace close")
	}
	require.Len(t, tracker.snapshot(), 1)

	release()
	select {
	case <-first.closeDone:
	case <-time.After(10 * time.Second):
		t.Fatal("pending workspace close did not complete")
	}
	require.Len(t, tracker.snapshot(), 1,
		"queued reopen must remain gated until Phase B releases ownership")
	close(releaseBuild)
	m.quiesce()
	m.waitForWorkspace(t, uri)
	require.Len(t, tracker.snapshot(), 2)
	require.Equal(t, int32(1), first.closeCalls.Load())
}

func TestPendingWorkspaceScheduleRejectTearsDown(t *testing.T) {
	m, manager, uri, tracker := newPendingTeardownTestHandler(t, nil)

	m.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	pending, err := m.reservePendingSlot(uri, -1, cancel)
	require.NoError(t, err)
	cwd, err := m.createWorkspaceScheme(uri)
	require.NoError(t, err)
	pending.cwd = cwd
	m.shaderRunner.startLoading()
	m.pendingWG.Add(1)
	originalSchedule := m.scheduleNextTick
	m.scheduleNextTick = func(func()) bool { return false }
	m.launchBuild(pending, uri, cwd, ctx, cancel, false, false)
	m.mu.Unlock()

	m.pendingWG.Wait()
	m.waitClosing()
	m.mu.Lock()
	m.scheduleNextTick = originalSchedule
	_, stillPending := m.pending[uri.String()]
	m.mu.Unlock()

	require.False(t, stillPending)
	require.False(t, manager.HasWorkspace(uri))
	created := tracker.snapshot()
	require.Len(t, created, 1)
	require.Equal(t, int32(1), created[0].closeCalls.Load())
}

// blockingPtyScheme wraps a scheme so NewPty wedges until the scheme
// is closed, ignoring the spawn ctx. This models the live deadlock
// where a VTE warm-up goroutine is stuck in an unbounded remote RPC
// (remoteFile.Close on a stalled SSH transport) that only the scheme
// teardown can abort.
type blockingPtyScheme struct {
	schemeapi.Scheme
	entered   chan struct{}
	release   chan struct{}
	enterOnce sync.Once
	closeOnce sync.Once
}

func newBlockingPtyScheme(inner schemeapi.Scheme) *blockingPtyScheme {
	return &blockingPtyScheme{
		Scheme:  inner,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *blockingPtyScheme) NewPty(context.Context) (workspaceapi.Pty, error) {
	s.enterOnce.Do(func() { close(s.entered) })
	<-s.release
	return workspaceapi.Pty{}, errors.New("scheme closed")
}

func (s *blockingPtyScheme) Close() error {
	s.closeOnce.Do(func() { close(s.release) })
	return s.Scheme.Close()
}

// TestReloadWithWedgedVTEWarmupDoesNotDeadlock reproduces the live
// :workspacereload deadlock: a VTE warm-up goroutine wedged in a
// scheme call that only the scheme teardown can abort, while
// closeWorkspace's on-loop ex.Close fenced on WaitForPendingInit —
// before the teardown that would abort the call. The event loop froze
// forever.
//
// Teardown never waits for warm-ups before closing the scheme: the
// close is what aborts the wedged call, and the background teardown
// drains the goroutine only after that. The test asserts the reload
// returns promptly and that the teardown un-wedges the warm-up on
// its own — no external release.
func TestReloadWithWedgedVTEWarmupDoesNotDeadlock(t *testing.T) {
	dir := t.TempDir()
	m := newTestWorkspaceManagerHandlerWithDir(t,
		defaultConfigWithWrap(false), dir, nopShutdownShaderConfig())
	t.Cleanup(func() { _ = m.Close() })
	m.quiesce()

	var schemesMu sync.Mutex
	var schemes []*blockingPtyScheme
	// Runs before the handler-close cleanup above (LIFO): un-wedge
	// every scheme so no wedged warm-up goroutine outlives the test
	// if an assertion fails before the teardown closes the scheme.
	t.Cleanup(func() {
		schemesMu.Lock()
		defer schemesMu.Unlock()
		for _, s := range schemes {
			s.closeOnce.Do(func() { close(s.release) })
		}
	})
	m.mu.Lock()
	err := m.workspace.RegisterScheme("blockpty",
		func(ctx context.Context, cfg config.Config, uri workspaceapi.URI) (
			schemeapi.Scheme, error,
		) {
			memURI, err := workspaceapi.ParseURI("memory://" + uri.Path())
			if err != nil {
				return nil, err
			}
			inner, err := workspace.NewMemoryScheme(ctx, cfg, memURI)
			if err != nil {
				return nil, err
			}
			s := newBlockingPtyScheme(inner)
			schemesMu.Lock()
			schemes = append(schemes, s)
			schemesMu.Unlock()
			return s, nil
		})
	// Warm one VTE per workspace so a wedged warm-up is in flight.
	m.initialVTECapacity = 1
	m.mu.Unlock()
	require.NoError(t, err)

	uri, err := workspaceapi.ParseURI("blockpty://" + t.TempDir())
	require.NoError(t, err)
	m.mu.Lock()
	err = m.addWorkspace(uri, false, false, -1)
	m.mu.Unlock()
	require.NoError(t, err)
	m.waitForWorkspace(t, uri)

	schemesMu.Lock()
	require.Len(t, schemes, 1)
	first := schemes[0]
	schemesMu.Unlock()
	select {
	case <-first.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("VTE warm-up never reached the wedged NewPty")
	}

	m.mu.Lock()
	slot, ok := m.findInstalledSlot(uri)
	require.True(t, ok)
	m.switchToWorkspace(slot)
	m.mu.Unlock()

	reloadDone := make(chan error, 1)
	go debug.CapturePanicReport(func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		reloadDone <- m.commandReloadWorkspace()
	})

	select {
	case err := <-reloadDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		// Un-wedge manually so cleanup can proceed, then fail.
		t.Error(":workspacereload blocked the event loop " +
			"behind a wedged VTE warm-up")
		first.closeOnce.Do(func() { close(first.release) })
		<-reloadDone
		return
	}

	// The background teardown must abort the wedged warm-up on its
	// own: closing the scheme is the un-wedge, and nothing waits for
	// the warm-up before that close.
	select {
	case <-first.release:
	case <-time.After(10 * time.Second):
		t.Fatal("reload teardown never closed the old scheme")
	}
	m.quiesce()
	m.waitForWorkspace(t, uri)
	m.mu.Lock()
	_, reopened := m.findInstalledSlot(uri)
	m.mu.Unlock()
	require.True(t, reopened, "reload must reopen the workspace")
}

// brokenClosePtyScheme models a transport whose Close fails to abort
// an in-flight NewPty: the warm-up goroutine stays wedged across the
// entire teardown and only the test's cleanup releases it.
type brokenClosePtyScheme struct {
	*blockingPtyScheme
}

func (s *brokenClosePtyScheme) Close() error {
	return s.Scheme.Close()
}

// TestBackgroundCloseCompletesWithUnabortableWarmup asserts that the
// background workspace teardown never waits on VTE warm-up
// goroutines: they are self-disposing (Facility.initCap closes any
// late VTE against a closed pool), so joining them only converts a
// self-limiting goroutine into a permanent hang of the closing gate
// and closeWG.Wait when a broken transport's scheme close cannot
// abort the wedged call.
func TestBackgroundCloseCompletesWithUnabortableWarmup(t *testing.T) {
	dir := t.TempDir()
	m := newTestWorkspaceManagerHandlerWithDir(t,
		defaultConfigWithWrap(false), dir, nopShutdownShaderConfig())
	t.Cleanup(func() { _ = m.Close() })
	m.quiesce()

	var schemesMu sync.Mutex
	var schemes []*brokenClosePtyScheme
	// Runs before the handler-close cleanup above (LIFO): release
	// the wedged warm-up goroutines at test end.
	t.Cleanup(func() {
		schemesMu.Lock()
		defer schemesMu.Unlock()
		for _, s := range schemes {
			s.closeOnce.Do(func() { close(s.release) })
		}
	})
	m.mu.Lock()
	err := m.workspace.RegisterScheme("brokenpty",
		func(ctx context.Context, cfg config.Config, uri workspaceapi.URI) (
			schemeapi.Scheme, error,
		) {
			memURI, err := workspaceapi.ParseURI("memory://" + uri.Path())
			if err != nil {
				return nil, err
			}
			inner, err := workspace.NewMemoryScheme(ctx, cfg, memURI)
			if err != nil {
				return nil, err
			}
			s := &brokenClosePtyScheme{newBlockingPtyScheme(inner)}
			schemesMu.Lock()
			schemes = append(schemes, s)
			schemesMu.Unlock()
			return s, nil
		})
	m.initialVTECapacity = 1
	m.mu.Unlock()
	require.NoError(t, err)

	uri, err := workspaceapi.ParseURI("brokenpty://" + t.TempDir())
	require.NoError(t, err)
	m.mu.Lock()
	err = m.addWorkspace(uri, false, false, -1)
	m.mu.Unlock()
	require.NoError(t, err)
	m.waitForWorkspace(t, uri)

	schemesMu.Lock()
	require.Len(t, schemes, 1)
	first := schemes[0]
	schemesMu.Unlock()
	select {
	case <-first.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("VTE warm-up never reached the wedged NewPty")
	}

	m.mu.Lock()
	slot, ok := m.findInstalledSlot(uri)
	require.True(t, ok)
	m.switchToWorkspace(slot)
	err = m.commandCloseWorkspace()
	m.mu.Unlock()
	require.NoError(t, err)

	drained := make(chan struct{})
	go debug.CapturePanicReport(func() {
		m.quiesce()
		close(drained)
	})
	select {
	case <-drained:
	case <-time.After(10 * time.Second):
		t.Fatal("background close never completed: teardown waited " +
			"on a warm-up goroutine the scheme close could not abort")
	}
}

// newTestStorageWrap lets a test install a wrapper around the
// storageapi.Service that newTestWorkspaceManagerHandlerWithManagerMu
// constructs internally. Tests must reset it to nil in t.Cleanup so
// other tests fall back to an unwrapped storage.
var newTestStorageWrap func(storageapi.Service) storageapi.Service

// partitionTrackingService counts opens and closes of partitions whose
// (root-level) name matches a target string. Used by the RUNE-189
// regression test to assert that the per-workspace
// `extension-permissions` Partition created inside buildExtensions is
// closed when the workspace closes.
type partitionTrackingService struct {
	storageapi.Service
	target string
	opens  atomic.Int32
	closes atomic.Int32
}

func (s *partitionTrackingService) Partition(name string) (storageapi.Service, error) {
	p, err := s.Service.Partition(name)
	if err != nil {
		return nil, err
	}
	if name != s.target {
		return p, nil
	}
	s.opens.Add(1)
	return &trackedPartition{Service: p, parent: s}, nil
}

type trackedPartition struct {
	storageapi.Service
	parent *partitionTrackingService
	once   sync.Once
}

func (p *trackedPartition) Close() error {
	// firstmover's underlying delayedLoadingService.Close is not
	// idempotent; double-counting Close would mask a real leak so
	// guard with sync.Once.
	p.once.Do(func() { p.parent.closes.Add(1) })
	return p.Service.Close()
}

// TestCloseWorkspaceClosesExtensionPermissionsPartition guards the
// RUNE-189 invariant that every storageapi.Service Partition opened
// while a workspace is installing (e.g. the "extension-permissions"
// partition allocated inside buildExtensions) is closed when the
// workspace is closed. Each unclosed Partition on a firstmover-backed
// storage leaks a follower goroutine, a leadOrFollow goroutine, a
// monitorLeader goroutine, and a gRPC client subscription.
func TestCloseWorkspaceClosesExtensionPermissionsPartition(t *testing.T) {
	dir := t.TempDir()

	var tracker *partitionTrackingService
	newTestStorageWrap = func(s storageapi.Service) storageapi.Service {
		tracker = &partitionTrackingService{
			Service: s, target: "extension-permissions",
		}
		return tracker
	}
	t.Cleanup(func() { newTestStorageWrap = nil })

	cfg := defaultCfg()
	mu, sched, drain := buildTestSchedulerForCfg(t, &cfg)
	manager := workspace.NewManager(config.NopConfig(), sched)
	require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme,
		workspace.NewMemoryScheme))
	require.NoError(t, manager.RegisterScheme(workspace.FileScheme,
		workspace.NewFileScheme))

	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	runner := FuncExtensionsRunner(testRunnerFn)
	m := newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
		&uri, cfg, runner, nil, dir, nil, nopShutdownShaderConfig())
	t.Cleanup(func() { _ = m.Close() })

	require.NotNil(t, tracker, "newTestStorageWrap must have been invoked")
	require.GreaterOrEqual(t, tracker.opens.Load(), int32(1),
		"buildExtensions must open at least one Partition during install")

	openedDuringInstall := tracker.opens.Load()
	closedDuringInstall := tracker.closes.Load()

	m.mu.Lock()
	err = m.commandCloseWorkspace()
	m.mu.Unlock()
	require.NoError(t, err)
	// Teardown now runs in a background goroutine; drain before
	// counting closes.
	m.quiesce()

	openedDuringClose := tracker.opens.Load() - openedDuringInstall
	closedDuringClose := tracker.closes.Load() - closedDuringInstall

	// The home install path always opens one "extension-permissions"
	// partition that stays alive until the manager (not the
	// workspace) closes. Anything beyond that one belongs to
	// per-workspace installs and MUST be closed when the workspace
	// closes. netLeak counts unclosed per-workspace partitions:
	// homeAccountedFor = 1; openedClose may include re-installs.
	netLeak := openedDuringInstall - 1 - closedDuringClose - openedDuringClose
	require.LessOrEqual(t, netLeak, int32(0),
		"closing a workspace must close every Partition opened during "+
			"its install (opened=%d closed=%d net=%d): "+
			"unclosed Partitions leak a firstmover follower goroutine "+
			"plus a gRPC client per workspace cycle (RUNE-189)",
		openedDuringInstall, closedDuringClose, netLeak)
}

func TestBuildExtensionsFailureClosesOwnedResources(t *testing.T) {
	dir := t.TempDir()
	var tracker *partitionTrackingService
	newTestStorageWrap = func(s storageapi.Service) storageapi.Service {
		tracker = &partitionTrackingService{
			Service: s, target: "extension-permissions",
		}
		return tracker
	}
	t.Cleanup(func() { newTestStorageWrap = nil })

	cfg := defaultCfg()
	mu, sched, drain := buildTestSchedulerForCfg(t, &cfg)
	manager := workspace.NewManager(config.NopConfig(), sched)
	require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme,
		workspace.NewMemoryScheme))
	require.NoError(t, manager.RegisterScheme(workspace.FileScheme,
		workspace.NewFileScheme))

	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)
	wantErr := errors.New("extension runner failed")
	runner := FuncExtensionsRunner(func(
		workspaceapi.URI,
		map[extensionapi.Permission]extension.ResourceRegistrar,
		string, string, browser.Notifications,
		schemeapi.Executor, schemeapi.Executor, extension.Grantor,
		text.Editor, ideauthorizer.PromptOpener, storageapi.Service,
		func(func()) bool,
	) (extension.Runner, error) {
		return nil, wantErr
	})

	m := newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
		&uri, cfg, runner, nil, dir, nil, nopShutdownShaderConfig())
	t.Cleanup(func() { _ = m.Close() })

	require.NotNil(t, tracker)
	require.Positive(t, tracker.opens.Load())
	require.Equal(t, tracker.opens.Load(), tracker.closes.Load(),
		"failed builds must close every resource allocated before the error")
}

// TestWorkspaceReadyCommand exercises the `workspaceready` event-loop
// primitive that defers a command until the most-recently-issued
// pending workspace finishes installing. This is what makes the
// `worktreenew` alias's `workspacerename $1` step run on the new
// workspace rather than on the previously focused one.
func TestWorkspaceReadyCommand(t *testing.T) {
	t.Run("dispatches inline when nothing is pending", func(t *testing.T) {
		dir := t.TempDir()
		m := newTestWorkspaceManagerHandlerWithDir(t,
			defaultConfigWithWrap(false), dir, nopShutdownShaderConfig())
		t.Cleanup(func() { _ = m.Close() })
		m.quiesce()

		m.mu.Lock()
		defer m.mu.Unlock()
		require.Nil(t, m.lastReservedPending)
		require.NoError(t,
			m.commandWorkspaceReady(cmdRenameWorkspace, "inline"))
		require.Equal(t, "inline", m.workspaces[m.focus].tabname,
			"workspaceready with no pending must dispatch inline "+
				"against the focused workspace")
	})

	t.Run("waits for the follow-up command to be registered", func(t *testing.T) {
		runner := newWaitReadyRunner()
		close(runner.ready)
		dir := t.TempDir()
		m := newTestWorkspaceManagerHandlerWithRunner(t,
			defaultConfigWithWrap(false), dir, runner)
		t.Cleanup(func() { _ = m.Close() })
		m.quiesce()

		const extCmd = "extcmd"
		var dispatched atomic.Bool

		m.mu.Lock()
		require.NoError(t,
			m.commandExtensionReady("ext-id", extCmd))
		m.mu.Unlock()

		// The extension is ready, but extcmd has not been registered
		// yet: the follow-up command must not dispatch.
		require.Never(t, func() bool {
			m.quiesce()
			return dispatched.Load()
		}, 150*time.Millisecond, 15*time.Millisecond,
			"extensionready must wait for the follow-up command to be "+
				"registered before dispatching it")

		require.NoError(t, m.subscribeCommand(
			textapi.CommandManual{Name: extCmd},
			text.FuncCommandHandler(
				func(context.Context, textapi.Command) error {
					dispatched.Store(true)
					return nil
				}, nil)))

		require.Eventually(t, func() bool {
			m.quiesce()
			return dispatched.Load()
		}, 2*time.Second, 10*time.Millisecond,
			"extensionready must dispatch the follow-up command once it "+
				"has been registered")
	})
	t.Run("defers until pending workspace is installed", func(t *testing.T) {
		dir1 := t.TempDir()
		m := newTestWorkspaceManagerHandlerWithDir(t,
			defaultConfigWithWrap(false), dir1, nopShutdownShaderConfig())
		t.Cleanup(func() { _ = m.Close() })
		m.quiesce()
		firstSlot := m.focus

		dir2 := t.TempDir()
		uri2, err := workspaceapi.ParseURI("memory://" + dir2)
		require.NoError(t, err)

		m.mu.Lock()
		require.NoError(t, m.workspaceManagerHandler.addOrCreateWorkspace(uri2))
		require.NotNil(t, m.lastReservedPending,
			"addWorkspace must reserve a pending entry "+
				"and record it as the most recent")
		require.Equal(t, uri2.String(),
			m.lastReservedPending.uri.String())
		require.NoError(t,
			m.commandWorkspaceReady(cmdRenameWorkspace, "deferred"))
		// The previously focused workspace must NOT be renamed
		// while the new build is still pending — that is the
		// whole point of `workspaceready`.
		require.Equal(t, "", m.workspaces[firstSlot].tabname)
		m.mu.Unlock()

		m.quiesce()

		m.mu.Lock()
		defer m.mu.Unlock()
		newSlot, ok := m.findInstalledSlot(uri2)
		require.True(t, ok, "new workspace must be installed")
		require.NotNil(t, m.workspaces[newSlot])
		require.Equal(t, "deferred", m.workspaces[newSlot].tabname,
			"queued workspacerename must apply to the freshly "+
				"installed workspace")
		require.Equal(t, "", m.workspaces[firstSlot].tabname,
			"the previously focused workspace must remain "+
				"unrenamed")
		require.Nil(t, m.lastReservedPending,
			"installPendingWorkspace must clear lastReservedPending")
	})

	t.Run("queue is dropped when pending is canceled", func(t *testing.T) {
		dir1 := t.TempDir()
		m := newTestWorkspaceManagerHandlerWithDir(t,
			defaultConfigWithWrap(false), dir1, nopShutdownShaderConfig())
		t.Cleanup(func() { _ = m.Close() })
		m.quiesce()
		firstSlot := m.focus

		// Reserve a pending workspace manually so we can cancel it
		// before Phase C runs without racing with the goroutine.
		dir2 := t.TempDir()
		uri2, err := workspaceapi.ParseURI("memory://" + dir2)
		require.NoError(t, err)

		m.mu.Lock()
		_, cancel := context.WithCancel(context.Background())
		pending, err := m.reservePendingSlot(uri2, -1, cancel)
		require.NoError(t, err)
		require.Same(t, pending, m.lastReservedPending)

		require.NoError(t,
			m.commandWorkspaceReady(cmdRenameWorkspace, "dropped"))
		require.Len(t, pending.onReady, 1)

		// Focus the pending slot so closeWorkspace cancels the
		// pending entry (its branch keys off pendingForFocus).
		m.focus = pending.slot
		_, _, err = m.closeWorkspace()
		require.NoError(t, err)
		require.Nil(t, m.lastReservedPending,
			"canceling the pending entry must clear "+
				"lastReservedPending")

		// Switch focus back and ensure the queued command never ran.
		m.focus = firstSlot
		require.Equal(t, "", m.workspaces[firstSlot].tabname)
		m.mu.Unlock()
	})

	t.Run("attaches to most recently reserved pending", func(t *testing.T) {
		dir1 := t.TempDir()
		m := newTestWorkspaceManagerHandlerWithDir(t,
			defaultConfigWithWrap(false), dir1, nopShutdownShaderConfig())
		t.Cleanup(func() { _ = m.Close() })
		m.quiesce()

		// Reserve two pending workspaces back-to-back; the second
		// must be selected by workspaceready.
		dirA := t.TempDir()
		uriA, err := workspaceapi.ParseURI("memory://" + dirA)
		require.NoError(t, err)
		dirB := t.TempDir()
		uriB, err := workspaceapi.ParseURI("memory://" + dirB)
		require.NoError(t, err)

		m.mu.Lock()
		_, cancelA := context.WithCancel(context.Background())
		pendingA, err := m.reservePendingSlot(uriA, -1, cancelA)
		require.NoError(t, err)
		_, cancelB := context.WithCancel(context.Background())
		pendingB, err := m.reservePendingSlot(uriB, -1, cancelB)
		require.NoError(t, err)
		require.Same(t, pendingB, m.lastReservedPending)

		require.NoError(t,
			m.commandWorkspaceReady(cmdRenameWorkspace, "onB"))
		require.Empty(t, pendingA.onReady,
			"the older pending workspace must not receive the queued "+
				"command")
		require.Len(t, pendingB.onReady, 1)

		// Tear down both pending entries so Close doesn't leak
		// reservations or hang waiting for Phase B goroutines that
		// were never launched.
		pendingA.canceled.Store(true)
		pendingA.cancelCtx()
		delete(m.pending, uriA.String())
		pendingB.canceled.Store(true)
		pendingB.cancelCtx()
		delete(m.pending, uriB.String())
		m.lastReservedPending = nil
		m.mu.Unlock()
	})
}

// waitReadyRunner is a fake extension.Runner that resolves WaitReady
// when its ready channel is closed, or returns waitErr.
type waitReadyRunner struct {
	ready   chan struct{}
	waitErr error
}

func newWaitReadyRunner() *waitReadyRunner {
	return &waitReadyRunner{ready: make(chan struct{})}
}

func (r *waitReadyRunner) Run(extensionID, path string, config config.Config) error {
	return nil
}

func (r *waitReadyRunner) Close() error { return nil }

func (r *waitReadyRunner) WaitReady(ctx context.Context, id string) error {
	if r.waitErr != nil {
		return r.waitErr
	}
	select {
	case <-r.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// perIDReadyRunner is a fake extension.Runner that gates WaitReady on a
// per-id channel so different extension ids can be released
// independently. It lets tests prove that a never-ready extension only
// blocks its own follow-up commands.
type perIDReadyRunner struct {
	mu    sync.Mutex
	ready map[string]chan struct{}
}

func newPerIDReadyRunner(ids ...string) *perIDReadyRunner {
	r := &perIDReadyRunner{ready: make(map[string]chan struct{}, len(ids))}
	for _, id := range ids {
		r.ready[id] = make(chan struct{})
	}
	return r
}

func (r *perIDReadyRunner) chanFor(id string) chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch, ok := r.ready[id]
	if !ok {
		ch = make(chan struct{})
		r.ready[id] = ch
	}
	return ch
}

func (r *perIDReadyRunner) release(id string) { close(r.chanFor(id)) }

func (r *perIDReadyRunner) Run(extensionID, path string, config config.Config) error {
	return nil
}

func (r *perIDReadyRunner) Close() error { return nil }

func (r *perIDReadyRunner) WaitReady(ctx context.Context, id string) error {
	ch := r.chanFor(id)
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// waitableCommandHandler models an out-of-process extension command: its
// HandleCommand claims the caller's textrpc.Waiter, returns immediately
// (fire-and-forget), and reports completion on the waiter channel only
// after release is closed. This lets tests prove a follow-up command does
// not start until the previous one has finished across the RPC boundary.
type waitableCommandHandler struct {
	name    string
	release chan struct{}
	mu      *sync.Mutex
	order   *[]string
}

func (h *waitableCommandHandler) HandleCommand(
	ctx context.Context, _ textapi.Command,
) error {
	w, ok := textrpc.WaiterFromContext(ctx)
	if !ok {
		return nil
	}
	w.Claimed = true
	h.mu.Lock()
	*h.order = append(*h.order, h.name+":start")
	h.mu.Unlock()
	go debug.CapturePanicReport(func() {
		select {
		case <-h.release:
		case <-ctx.Done():
			w.Ch <- ctx.Err()
			return
		}
		h.mu.Lock()
		*h.order = append(*h.order, h.name+":done")
		h.mu.Unlock()
		w.Ch <- nil
	})
	return nil
}

func (h *waitableCommandHandler) Complete(
	context.Context, textapi.Command,
) (iterator.Iterator[string], string, error) {
	return nil, "", nil
}

// silentExtHandler models an extension that claims the waiter but never
// reports completion, as happens when the extension drops its reply while
// the stream tears down. The follow-up worker must fall back to its wait
// timeout instead of blocking forever.
type silentExtHandler struct{}

func (silentExtHandler) HandleCommand(ctx context.Context, _ textapi.Command) error {
	if w, ok := textrpc.WaiterFromContext(ctx); ok {
		w.Claimed = true
	}
	return nil
}

func (silentExtHandler) Complete(
	context.Context, textapi.Command,
) (iterator.Iterator[string], string, error) {
	return nil, "", nil
}

// newTestWorkspaceManagerHandlerWithRunner builds a handler whose
// workspaces install runner for their Extensions. Using a single
// shared runner keeps the concrete type stored in the per-workspace
// atomic.Value consistent.
func newTestWorkspaceManagerHandlerWithRunner(
	t *testing.T, cc ideConfig, dir string, runner extension.Runner,
) *testWorkspaceManagerHandler {
	mu, sched, drain := buildTestSchedulerForCfg(t, &cc)
	manager := workspace.NewManager(config.NopConfig(), sched)
	require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme,
		workspace.NewMemoryScheme))
	require.NoError(t, manager.RegisterScheme(workspace.FileScheme,
		workspace.NewFileScheme))

	uri := new(workspaceapi.URI)
	var err error
	*uri, err = workspaceapi.ParseURI("memory://" + dir)
	require.NoError(t, err)

	runnerFn := func(
		_ workspaceapi.URI,
		_ map[extensionapi.Permission]extension.ResourceRegistrar,
		_, _ string, _ browser.Notifications,
		_, _ schemeapi.Executor, _ extension.Grantor, _ text.Editor,
		_ ideauthorizer.PromptOpener, _ storageapi.Service,
		_ func(func()) bool) (extension.Runner, error) {
		return runner, nil
	}
	return newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
		uri, cc, FuncExtensionsRunner(runnerFn), nil, dir, nil,
		nopShutdownShaderConfig())
}

func TestExtensionReadyCommand(t *testing.T) {
	t.Run("dispatches inline once the extension is ready", func(t *testing.T) {
		runner := newWaitReadyRunner()
		dir := t.TempDir()
		m := newTestWorkspaceManagerHandlerWithRunner(t,
			defaultConfigWithWrap(false), dir, runner)
		t.Cleanup(func() { _ = m.Close() })
		m.quiesce()

		m.mu.Lock()
		require.Nil(t, m.lastReservedPending)
		require.NoError(t,
			m.commandExtensionReady("ext-id", cmdRenameWorkspace, "inline"))
		// The follow-up command must NOT run before the extension is
		// ready.
		require.Equal(t, "", m.workspaces[m.focus].tabname)
		focus := m.focus
		m.mu.Unlock()

		close(runner.ready)

		require.Eventually(t, func() bool {
			m.quiesce()
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.workspaces[focus].tabname == "inline"
		}, time.Second, 10*time.Millisecond,
			"extensionready must dispatch the follow-up command once "+
				"the extension is ready")
	})

	t.Run("waits for the follow-up command to be registered", func(t *testing.T) {
		runner := newWaitReadyRunner()
		close(runner.ready)
		dir := t.TempDir()
		m := newTestWorkspaceManagerHandlerWithRunner(t,
			defaultConfigWithWrap(false), dir, runner)
		t.Cleanup(func() { _ = m.Close() })
		m.quiesce()

		const extCmd = "extcmd"
		var dispatched atomic.Bool

		m.mu.Lock()
		require.NoError(t, m.commandExtensionReady("ext-id", extCmd))
		m.mu.Unlock()

		// The extension is ready, but extcmd has not been registered
		// yet: the follow-up command must not dispatch.
		require.Never(t, func() bool {
			m.quiesce()
			return dispatched.Load()
		}, 150*time.Millisecond, 15*time.Millisecond,
			"extensionready must wait for the follow-up command to be "+
				"registered before dispatching it")

		require.NoError(t, m.subscribeCommand(
			textapi.CommandManual{Name: extCmd},
			text.FuncCommandHandler(
				func(context.Context, textapi.Command) error {
					dispatched.Store(true)
					return nil
				}, nil)))

		require.Eventually(t, func() bool {
			m.quiesce()
			return dispatched.Load()
		}, 2*time.Second, 10*time.Millisecond,
			"extensionready must dispatch the follow-up command once it "+
				"has been registered")
	})

	t.Run("defers until pending workspace is installed", func(t *testing.T) {
		dir1 := t.TempDir()
		m := newTestWorkspaceManagerHandlerWithDir(t,
			defaultConfigWithWrap(false), dir1, nopShutdownShaderConfig())
		t.Cleanup(func() { _ = m.Close() })
		m.quiesce()
		firstSlot := m.focus

		dir2 := t.TempDir()
		uri2, err := workspaceapi.ParseURI("memory://" + dir2)
		require.NoError(t, err)

		m.mu.Lock()
		require.NoError(t, m.workspaceManagerHandler.addOrCreateWorkspace(uri2))
		require.NotNil(t, m.lastReservedPending)
		pending := m.lastReservedPending
		require.NoError(t,
			m.commandExtensionReady("ext-id", cmdRenameWorkspace, "deferred"))
		require.Len(t, pending.onReady, 1)
		require.Equal(t,
			[]string{cmdExtensionReady, "ext-id", cmdRenameWorkspace, "deferred"},
			pending.onReady[0],
			"the queued command must preserve the extensionready envelope")
		require.Equal(t, "", m.workspaces[firstSlot].tabname)
		m.mu.Unlock()

		m.quiesce()

		// The drained extensionready spawns a goroutine that waits on
		// the freshly installed workspace's runner (the testRunner,
		// whose WaitReady returns immediately) and then dispatches the
		// rename via scheduleNextTick.
		require.Eventually(t, func() bool {
			m.quiesce()
			m.mu.Lock()
			defer m.mu.Unlock()
			newSlot, ok := m.findInstalledSlot(uri2)
			if !ok || m.workspaces[newSlot] == nil {
				return false
			}
			return m.workspaces[newSlot].tabname == "deferred"
		}, time.Second, 10*time.Millisecond,
			"queued extensionready must rename the freshly installed "+
				"workspace once its extension is ready")

		m.mu.Lock()
		defer m.mu.Unlock()
		require.Equal(t, "", m.workspaces[firstSlot].tabname,
			"the previously focused workspace must remain unrenamed")
	})

	t.Run("surfaces a notification and dispatches nothing on error", func(t *testing.T) {
		runner := &waitReadyRunner{ready: make(chan struct{}),
			waitErr: errors.New("ext exited")}
		dir := t.TempDir()
		m := newTestWorkspaceManagerHandlerWithRunner(t,
			defaultConfigWithWrap(false), dir, runner)
		t.Cleanup(func() { _ = m.Close() })
		m.quiesce()

		m.mu.Lock()
		require.NoError(t,
			m.commandExtensionReady("ext-id", cmdRenameWorkspace, "never"))
		focus := m.focus
		m.mu.Unlock()

		// Give the goroutine and scheduled tick time to run, then
		// assert the follow-up command never dispatched.
		require.Never(t, func() bool {
			m.quiesce()
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.workspaces[focus].tabname == "never"
		}, 200*time.Millisecond, 20*time.Millisecond,
			"a WaitReady error must not dispatch the follow-up command")
	})

	t.Run("surfaces a notification when the extension never becomes ready", func(t *testing.T) {
		// The extension id is never released, so WaitReady blocks until
		// the readiness timeout fires.
		runner := newPerIDReadyRunner("ext-id")
		dir := t.TempDir()
		m := newTestWorkspaceManagerHandlerWithRunner(t,
			defaultConfigWithWrap(false), dir, runner)
		t.Cleanup(func() { _ = m.Close() })
		m.quiesce()

		m.mu.Lock()
		m.extReadyWait = 50 * time.Millisecond
		focus := m.focus
		notes := &recordingNotifications{inner: m.workspaces[focus].notifications}
		m.workspaces[focus].notifications = notes
		require.NoError(t,
			m.commandExtensionReady("ext-id", cmdRenameWorkspace, "never"))
		m.mu.Unlock()

		require.Eventually(t, func() bool {
			m.quiesce()
			for _, n := range notes.snapshot() {
				if n.level == browserapi.LevelError &&
					strings.Contains(n.msg, "not ready within") {
					return true
				}
			}
			return false
		}, 2*time.Second, 10*time.Millisecond,
			"a never-ready extension must surface a readiness-timeout "+
				"error notification")

		m.mu.Lock()
		defer m.mu.Unlock()
		require.Equal(t, "", m.workspaces[focus].tabname,
			"the follow-up command must not dispatch when the extension "+
				"never becomes ready")
	})

	t.Run("surfaces a notification when the follow-up command never registers", func(t *testing.T) {
		// The extension becomes ready immediately, but the follow-up
		// command is never registered, so waitCommandRegistered times
		// out.
		runner := newWaitReadyRunner()
		close(runner.ready)
		dir := t.TempDir()
		m := newTestWorkspaceManagerHandlerWithRunner(t,
			defaultConfigWithWrap(false), dir, runner)
		t.Cleanup(func() { _ = m.Close() })
		m.quiesce()

		m.mu.Lock()
		m.extCommandWait = 50 * time.Millisecond
		focus := m.focus
		notes := &recordingNotifications{inner: m.workspaces[focus].notifications}
		m.workspaces[focus].notifications = notes
		require.NoError(t, m.commandExtensionReady("ext-id", "neverregistered"))
		m.mu.Unlock()

		require.Eventually(t, func() bool {
			m.quiesce()
			for _, n := range notes.snapshot() {
				if n.level == browserapi.LevelError &&
					strings.Contains(n.msg, "was not registered within") {
					return true
				}
			}
			return false
		}, 2*time.Second, 10*time.Millisecond,
			"a follow-up command that never registers must surface a "+
				"registration-timeout error notification")
	})

	t.Run("queue is dropped when pending is canceled", func(t *testing.T) {
		dir1 := t.TempDir()
		m := newTestWorkspaceManagerHandlerWithDir(t,
			defaultConfigWithWrap(false), dir1, nopShutdownShaderConfig())
		t.Cleanup(func() { _ = m.Close() })
		m.quiesce()
		firstSlot := m.focus

		dir2 := t.TempDir()
		uri2, err := workspaceapi.ParseURI("memory://" + dir2)
		require.NoError(t, err)

		m.mu.Lock()
		_, cancel := context.WithCancel(context.Background())
		pending, err := m.reservePendingSlot(uri2, -1, cancel)
		require.NoError(t, err)
		require.Same(t, pending, m.lastReservedPending)

		require.NoError(t,
			m.commandExtensionReady("ext-id", cmdRenameWorkspace, "dropped"))
		require.Len(t, pending.onReady, 1)

		m.focus = pending.slot
		_, _, err = m.closeWorkspace()
		require.NoError(t, err)
		require.Nil(t, m.lastReservedPending)

		m.focus = firstSlot
		require.Equal(t, "", m.workspaces[firstSlot].tabname)
		m.mu.Unlock()
	})

	t.Run("dispatches follow-up commands in submission order (same id)", func(t *testing.T) {
		runner := newPerIDReadyRunner("ext-A")
		dir := t.TempDir()
		m := newTestWorkspaceManagerHandlerWithRunner(t,
			defaultConfigWithWrap(false), dir, runner)
		t.Cleanup(func() { _ = m.Close() })
		m.quiesce()

		var mu sync.Mutex
		var order []string
		record := func(name string) text.CommandHandler {
			return text.FuncCommandHandler(
				func(context.Context, textapi.Command) error {
					mu.Lock()
					order = append(order, name)
					mu.Unlock()
					return nil
				}, nil)
		}
		const cmdA1, cmdA2 = "ext-a-cmd-1", "ext-a-cmd-2"
		require.NoError(t, m.subscribeCommand(
			textapi.CommandManual{Name: cmdA1}, record(cmdA1)))
		require.NoError(t, m.subscribeCommand(
			textapi.CommandManual{Name: cmdA2}, record(cmdA2)))

		m.mu.Lock()
		require.NoError(t, m.commandExtensionReady("ext-A", cmdA1))
		require.NoError(t, m.commandExtensionReady("ext-A", cmdA2))
		m.mu.Unlock()

		runner.release("ext-A")

		require.Eventually(t, func() bool {
			m.quiesce()
			mu.Lock()
			defer mu.Unlock()
			return len(order) == 2
		}, 2*time.Second, 10*time.Millisecond,
			"both follow-up commands must dispatch once the extension is ready")

		mu.Lock()
		defer mu.Unlock()
		require.Equal(t, []string{cmdA1, cmdA2}, order,
			"follow-up commands must dispatch in submission order even when "+
				"their waiters unblock out of order")
	})

	t.Run("independent extensions do not block each other (per-id workers)", func(t *testing.T) {
		runner := newPerIDReadyRunner("ext-slow", "ext-fast")
		dir := t.TempDir()
		m := newTestWorkspaceManagerHandlerWithRunner(t,
			defaultConfigWithWrap(false), dir, runner)
		t.Cleanup(func() { _ = m.Close() })
		m.quiesce()

		var mu sync.Mutex
		var order []string
		record := func(name string) text.CommandHandler {
			return text.FuncCommandHandler(
				func(context.Context, textapi.Command) error {
					mu.Lock()
					order = append(order, name)
					mu.Unlock()
					return nil
				}, nil)
		}
		const cmdSlow, cmdFast = "ext-slow-cmd", "ext-fast-cmd"
		require.NoError(t, m.subscribeCommand(
			textapi.CommandManual{Name: cmdSlow}, record(cmdSlow)))
		require.NoError(t, m.subscribeCommand(
			textapi.CommandManual{Name: cmdFast}, record(cmdFast)))

		m.mu.Lock()
		require.NoError(t, m.commandExtensionReady("ext-slow", cmdSlow))
		require.NoError(t, m.commandExtensionReady("ext-fast", cmdFast))
		m.mu.Unlock()

		// Release only the fast extension; ext-slow stays unready.
		runner.release("ext-fast")

		require.Eventually(t, func() bool {
			m.quiesce()
			mu.Lock()
			defer mu.Unlock()
			return len(order) == 1 && order[0] == cmdFast
		}, 2*time.Second, 10*time.Millisecond,
			"a follow-up command for a ready extension must dispatch while a "+
				"different, never-ready extension is still blocked")

		// Release the slow extension so its worker terminates cleanly.
		runner.release("ext-slow")
		require.Eventually(t, func() bool {
			m.quiesce()
			mu.Lock()
			defer mu.Unlock()
			return len(order) == 2
		}, 2*time.Second, 10*time.Millisecond,
			"the slow extension's follow-up must dispatch once it becomes ready")
	})

	t.Run("rejects when queue is full", func(t *testing.T) {
		runner := newPerIDReadyRunner("ext-full")
		dir := t.TempDir()
		m := newTestWorkspaceManagerHandlerWithRunner(t,
			defaultConfigWithWrap(false), dir, runner)
		t.Cleanup(func() { _ = m.Close() })
		m.quiesce()

		// Withhold readiness so the worker stays parked before it
		// drains the queue. Fill the buffer to capacity, then assert
		// the next submission is rejected.
		m.mu.Lock()
		for i := 0; i < extReadyQueueLimit; i++ {
			require.NoError(t,
				m.commandExtensionReady("ext-full", "ext-full-cmd"))
		}
		err := m.commandExtensionReady("ext-full", "ext-full-cmd")
		m.mu.Unlock()
		require.Error(t, err)
		require.Contains(t, err.Error(), "too many extensionready commands queued")

		// Release so the worker drains and exits for clean teardown.
		runner.release("ext-full")
		m.quiesce()
	})

	t.Run("close stops dispatching queued commands", func(t *testing.T) {
		runner := newPerIDReadyRunner("ext-close")
		dir := t.TempDir()
		m := newTestWorkspaceManagerHandlerWithRunner(t,
			defaultConfigWithWrap(false), dir, runner)
		t.Cleanup(func() { _ = m.Close() })
		m.quiesce()

		var mu sync.Mutex
		var order []string
		record := func(name string) text.CommandHandler {
			return text.FuncCommandHandler(
				func(context.Context, textapi.Command) error {
					mu.Lock()
					order = append(order, name)
					mu.Unlock()
					return nil
				}, nil)
		}
		// cmd1 is registered so it dispatches; cmd2 is intentionally
		// never registered so the worker parks in waitCommandRegistered
		// holding cmd2 in flight when Close cancels it.
		const cmd1, cmd2 = "ext-close-cmd-1", "ext-close-cmd-2"
		require.NoError(t, m.subscribeCommand(
			textapi.CommandManual{Name: cmd1}, record(cmd1)))

		m.mu.Lock()
		require.NoError(t, m.commandExtensionReady("ext-close", cmd1))
		require.NoError(t, m.commandExtensionReady("ext-close", cmd2))
		m.mu.Unlock()

		runner.release("ext-close")

		// Wait until cmd1 has dispatched: the worker has now consumed
		// cmd2 from the buffer and is parked waiting for it to register.
		require.Eventually(t, func() bool {
			m.quiesce()
			mu.Lock()
			defer mu.Unlock()
			return len(order) == 1
		}, 2*time.Second, 10*time.Millisecond,
			"the registered follow-up command must dispatch")

		require.NoError(t, m.Close())

		// Registering cmd2 now must not dispatch it: Close cancelled the
		// worker, so the in-flight command is dropped rather than run.
		require.NoError(t, m.subscribeCommand(
			textapi.CommandManual{Name: cmd2}, record(cmd2)))
		require.Never(t, func() bool {
			m.quiesce()
			mu.Lock()
			defer mu.Unlock()
			return len(order) != 1
		}, 200*time.Millisecond, 20*time.Millisecond,
			"Close must stop the worker from dispatching queued commands")
	})

	t.Run("waits for an extension command to finish before the next", func(t *testing.T) {
		runner := newWaitReadyRunner()
		close(runner.ready)
		dir := t.TempDir()
		m := newTestWorkspaceManagerHandlerWithRunner(t,
			defaultConfigWithWrap(false), dir, runner)
		t.Cleanup(func() { _ = m.Close() })
		m.quiesce()

		var mu sync.Mutex
		var order []string
		const cmdFirst, cmdSecond = "ext-wait-first", "ext-wait-second"
		releaseFirst := make(chan struct{})
		releaseSecond := make(chan struct{})
		close(releaseSecond)
		require.NoError(t, m.subscribeCommand(
			textapi.CommandManual{Name: cmdFirst},
			&waitableCommandHandler{
				name: cmdFirst, release: releaseFirst, mu: &mu, order: &order}))
		require.NoError(t, m.subscribeCommand(
			textapi.CommandManual{Name: cmdSecond},
			&waitableCommandHandler{
				name: cmdSecond, release: releaseSecond, mu: &mu, order: &order}))

		m.mu.Lock()
		require.NoError(t, m.commandExtensionReady("ext-id", cmdFirst))
		require.NoError(t, m.commandExtensionReady("ext-id", cmdSecond))
		m.mu.Unlock()

		// cmdFirst is in flight (its waiter has not reported completion);
		// cmdSecond must not start until cmdFirst finishes, even though
		// both were submitted back to back.
		require.Eventually(t, func() bool {
			m.quiesce()
			mu.Lock()
			defer mu.Unlock()
			return len(order) >= 1 && order[0] == cmdFirst+":start"
		}, 2*time.Second, 10*time.Millisecond,
			"the first extension command must start handling")
		require.Never(t, func() bool {
			m.quiesce()
			mu.Lock()
			defer mu.Unlock()
			for _, e := range order {
				if e == cmdSecond+":start" {
					return true
				}
			}
			return false
		}, 200*time.Millisecond, 20*time.Millisecond,
			"a follow-up extension command must not start before the "+
				"previous one finishes")

		close(releaseFirst)

		require.Eventually(t, func() bool {
			m.quiesce()
			mu.Lock()
			defer mu.Unlock()
			return len(order) == 4
		}, 2*time.Second, 10*time.Millisecond,
			"both extension commands must finish handling")
		mu.Lock()
		defer mu.Unlock()
		require.Equal(t, []string{
			cmdFirst + ":start", cmdFirst + ":done",
			cmdSecond + ":start", cmdSecond + ":done",
		}, order, "extension commands must be handled in submission order")
	})

	t.Run("in-process follow-ups dispatch in submission order", func(t *testing.T) {
		runner := newWaitReadyRunner()
		close(runner.ready)
		dir := t.TempDir()
		m := newTestWorkspaceManagerHandlerWithRunner(t,
			defaultConfigWithWrap(false), dir, runner)
		t.Cleanup(func() { _ = m.Close() })
		m.quiesce()

		var mu sync.Mutex
		var order []string
		record := func(name string) text.CommandHandler {
			return text.FuncCommandHandler(
				func(context.Context, textapi.Command) error {
					mu.Lock()
					order = append(order, name)
					mu.Unlock()
					return nil
				}, nil)
		}
		const cmd1, cmd2 = "ext-inproc-1", "ext-inproc-2"
		require.NoError(t, m.subscribeCommand(
			textapi.CommandManual{Name: cmd1}, record(cmd1)))
		require.NoError(t, m.subscribeCommand(
			textapi.CommandManual{Name: cmd2}, record(cmd2)))

		m.mu.Lock()
		require.NoError(t, m.commandExtensionReady("ext-id", cmd1))
		require.NoError(t, m.commandExtensionReady("ext-id", cmd2))
		m.mu.Unlock()

		require.Eventually(t, func() bool {
			m.quiesce()
			mu.Lock()
			defer mu.Unlock()
			return len(order) == 2
		}, 2*time.Second, 10*time.Millisecond,
			"both in-process follow-up commands must dispatch")
		mu.Lock()
		defer mu.Unlock()
		require.Equal(t, []string{cmd1, cmd2}, order,
			"in-process follow-ups must dispatch in submission order")
	})

	t.Run("a dropped extension reply times out and keeps draining", func(t *testing.T) {
		runner := newWaitReadyRunner()
		close(runner.ready)
		dir := t.TempDir()
		m := newTestWorkspaceManagerHandlerWithRunner(t,
			defaultConfigWithWrap(false), dir, runner)
		t.Cleanup(func() { _ = m.Close() })
		m.quiesce()

		var mu sync.Mutex
		var dispatched bool
		const cmdSilent, cmdNext = "ext-drop-silent", "ext-drop-next"
		require.NoError(t, m.subscribeCommand(
			textapi.CommandManual{Name: cmdSilent}, silentExtHandler{}))
		require.NoError(t, m.subscribeCommand(
			textapi.CommandManual{Name: cmdNext},
			text.FuncCommandHandler(
				func(context.Context, textapi.Command) error {
					mu.Lock()
					dispatched = true
					mu.Unlock()
					return nil
				}, nil)))

		m.mu.Lock()
		m.extHandleWait = 50 * time.Millisecond
		focus := m.focus
		notes := &recordingNotifications{inner: m.workspaces[focus].notifications}
		m.workspaces[focus].notifications = notes
		require.NoError(t, m.commandExtensionReady("ext-id", cmdSilent))
		require.NoError(t, m.commandExtensionReady("ext-id", cmdNext))
		m.mu.Unlock()

		// cmdSilent claims the waiter but never reports completion, so the
		// worker must surface its wait timeout instead of blocking forever.
		require.Eventually(t, func() bool {
			m.quiesce()
			for _, n := range notes.snapshot() {
				if n.level == browserapi.LevelError &&
					strings.Contains(n.msg, "extensionready ext-id") &&
					strings.Contains(n.msg, context.DeadlineExceeded.Error()) {
					return true
				}
			}
			return false
		}, 2*time.Second, 10*time.Millisecond,
			"a dropped extension reply must surface a wait-timeout error")

		// The queue must keep draining: the next follow-up still dispatches.
		require.Eventually(t, func() bool {
			m.quiesce()
			mu.Lock()
			defer mu.Unlock()
			return dispatched
		}, 2*time.Second, 10*time.Millisecond,
			"the next follow-up must dispatch after a dropped reply times out")
	})
}

// inlineSchedule is a synchronous workspace.ScheduleNextTick stub
// that runs fn on the calling goroutine. It is only safe for
// single-goroutine harnesses (the ex-level testEx tests, where the
// test goroutine is the event loop and dispatch is re-entrant; see
// installDefaultTestScheduler). Manager-backed handler tests must
// wire the manager to the same tracked scheduler installed on
// cfg.scheduleNextTick (buildTestSchedulerForCfg) instead, so reload
// buffer mutations never run on a worker goroutine off the IDE lock.
func inlineSchedule(fn func()) bool {
	fn()
	return true
}

// TestWorkspaceNewResolvesRelativeAgainstHome guards that `workspaceopen`
// resolves a bare relative path against the home workspace root rather
// than the process working directory. On macOS the latter is the app
// bundle (e.g. /Applications/Rune.app), so a relative path such as
// "src/blue" must not be anchored there.
func TestWorkspaceNewResolvesRelativeAgainstHome(t *testing.T) {
	dir := t.TempDir()
	m := newTestWorkspaceManagerHandlerWithDir(t,
		defaultConfigWithWrap(false), dir, nopShutdownShaderConfig())
	t.Cleanup(func() { _ = m.Close() })
	m.quiesce()

	want, err := m.homeWorkspace.URI("src/blue")
	require.NoError(t, err)

	m.mu.Lock()
	require.NoError(t, m.commandAddWorkspace("src/blue"))
	require.NotNil(t, m.lastReservedPending,
		"workspaceopen must reserve a pending entry for the resolved URI")
	got := m.lastReservedPending.uri.String()
	pending := m.lastReservedPending
	m.mu.Unlock()

	require.Equal(t, want.String(), got,
		"relative path must resolve against the home workspace root, "+
			"not the process working directory")

	m.mu.Lock()
	pending.canceled.Store(true)
	pending.cancelCtx()
	delete(m.pending, got)
	m.lastReservedPending = nil
	m.mu.Unlock()

	m.quiesce()
}

func TestRegisterREPLCommand(t *testing.T) {
	if ci := os.Getenv("CI"); ci == "true" {
		t.SkipNow()
	}

	dir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })

	m := newTestWorkspaceManagerHandlerWithDir(t, defaultConfigWithWrap(false), dir,
		nopShutdownShaderConfig())
	t.Cleanup(func() { _ = m.Close() })

	require.NoError(t, m.registerREPLCommand(
		textapi.CommandManual{Name: "loginz"}, &stubREPLHandler{}))

	var names []string
	for _, c := range m.empty.comp.REPLCommands() {
		names = append(names, c.Name)
	}
	assert.Contains(t, names, "loginz")

	err = m.registerREPLCommand(textapi.CommandManual{Name: "loginz"}, &stubREPLHandler{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")
}

// uriWorkspace stubs workspace.Workspace to control URI resolution and
// the host-reported install root; a nil install stands in for a host
// that cannot say (ErrUnsupported), as schemeWorkspace does for
// schemes without the capability.
type uriWorkspace struct {
	workspace.Workspace
	fn      func(path string) (workspaceapi.URI, error)
	install func(ctx context.Context) (string, error)
}

func (w uriWorkspace) URI(path string) (workspaceapi.URI, error) {
	return w.fn(path)
}

func (w uriWorkspace) InstallDataDir(ctx context.Context) (string, error) {
	if w.install == nil {
		return "", errors.ErrUnsupported
	}
	return w.install(ctx)
}

func TestInstallRoot(t *testing.T) {
	mustURI := func(t *testing.T, s string) workspaceapi.URI {
		u, err := workspaceapi.ParseURI(s)
		require.NoError(t, err)
		return u
	}
	tests := []struct {
		name         string
		localDataDir string
		uri          string
		fn           func(path string) (workspaceapi.URI, error)
		install      func(ctx context.Context) (string, error)
		want         string
	}{
		{
			// Local workspaces provision into the IDE's own datadir; the
			// remote-home expansion must not run.
			name:         "file workspace uses the local data dir verbatim",
			localDataDir: "/Users/x/.runedev",
			uri:          "file:///Users/x/src/proj",
			fn: func(string) (workspaceapi.URI, error) {
				t.Fatal("URI must not be called for a file workspace")
				return workspaceapi.URI{}, nil
			},
			want: "/Users/x/.runedev",
		},
		{
			name:         "file workspace never consults the provider",
			localDataDir: "/Users/x/.runedev",
			uri:          "file:///Users/x/src/proj",
			fn: func(string) (workspaceapi.URI, error) {
				t.Fatal("URI must not be called for a file workspace")
				return workspaceapi.URI{}, nil
			},
			install: func(context.Context) (string, error) {
				t.Fatal("InstallRoot must not be called for a file workspace")
				return "", nil
			},
			want: "/Users/x/.runedev",
		},
		{
			name:         "remote workspace resolves ~/<basename> on the remote host",
			localDataDir: "/Users/x/.rune",
			uri:          "ssh://host/home/remote/src/proj",
			fn: func(path string) (workspaceapi.URI, error) {
				require.Equal(t, "~/.rune", path)
				return workspaceapi.ParseURI("ssh://host/home/remote/.rune")
			},
			want: "/home/remote/.rune",
		},
		{
			// A client run with --datadir ~/.runedev forces the remote to
			// provision under ~/.runedev (via WithRemoteDataDir), so the
			// install root must mirror the local basename on the remote host.
			name:         "remote install root mirrors the local basename",
			localDataDir: "/Users/x/.runedev",
			uri:          "ssh://host/home/remote/src/proj",
			fn: func(path string) (workspaceapi.URI, error) {
				require.Equal(t, "~/.runedev", path,
					"remote install root must mirror the local basename")
				return workspaceapi.ParseURI("ssh://host/home/remote/.runedev")
			},
			want: "/home/remote/.runedev",
		},
		{
			name:         "expansion error falls back to local data dir",
			localDataDir: "/Users/x/.rune",
			uri:          "ssh://host/home/remote/src/proj",
			fn: func(path string) (workspaceapi.URI, error) {
				return workspaceapi.URI{}, errors.New("boom")
			},
			want: "/Users/x/.rune",
		},
		{
			// A rune:// peer is a long-running Rune with its own
			// datadir; when the peer advertises it, the guess derived
			// from the client's datadir must not run at all.
			name:         "peer-advertised install root wins over the basename guess",
			localDataDir: "/Users/x/.runedev",
			uri:          "rune://peer/home/remote/src/proj",
			fn: func(string) (workspaceapi.URI, error) {
				t.Fatal("URI must not be called when the peer advertises its install root")
				return workspaceapi.URI{}, nil
			},
			install: func(context.Context) (string, error) {
				return "/home/remote/.rune", nil
			},
			want: "/home/remote/.rune",
		},
		{
			name:         "provider error falls back to the basename guess",
			localDataDir: "/Users/x/.runedev",
			uri:          "rune://peer/home/remote/src/proj",
			fn: func(path string) (workspaceapi.URI, error) {
				require.Equal(t, "~/.runedev", path)
				return workspaceapi.ParseURI("rune://peer/home/remote/.runedev")
			},
			install: func(context.Context) (string, error) {
				return "", errors.New("peer info from peer: boom")
			},
			want: "/home/remote/.runedev",
		},
		{
			// An older peer without the PeerInfo service degrades
			// quietly to today's guess.
			name:         "provider ErrUnsupported falls back to the basename guess",
			localDataDir: "/Users/x/.runedev",
			uri:          "rune://peer/home/remote/src/proj",
			fn: func(path string) (workspaceapi.URI, error) {
				require.Equal(t, "~/.runedev", path)
				return workspaceapi.ParseURI("rune://peer/home/remote/.runedev")
			},
			install: func(context.Context) (string, error) {
				return "", fmt.Errorf("peer does not advertise: %w",
					errors.ErrUnsupported)
			},
			want: "/home/remote/.runedev",
		},
		{
			name:         "provider empty root falls back to the basename guess",
			localDataDir: "/Users/x/.runedev",
			uri:          "rune://peer/home/remote/src/proj",
			fn: func(path string) (workspaceapi.URI, error) {
				require.Equal(t, "~/.runedev", path)
				return workspaceapi.ParseURI("rune://peer/home/remote/.runedev")
			},
			install: func(context.Context) (string, error) {
				return "", nil
			},
			want: "/home/remote/.runedev",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := uriWorkspace{fn: tt.fn, install: tt.install}
			assert.Equal(t, tt.want,
				installDataDir(ws, mustURI(t, tt.uri), tt.localDataDir))
		})
	}
}

func swapDirIDEConfig(t *testing.T, enabled bool) ideConfig {
	f, err := os.CreateTemp("", "")
	require.NoError(t, err)
	defer os.Remove(f.Name())

	_, err = fmt.Fprintf(f, "\neditor:\n  swap_dir: %t\n", enabled)
	require.NoError(t, err)

	var cfg ideConfig
	require.NoError(t, loadConfig(&cfg, f.Name(), browser.NopWallpaper(),
		DefaultConfig{src: "config = {}"},
		term.RingBell, term.ScheduleNextTick, ""))
	return cfg
}

// TestSwapDirectory pins the swap directory to the host that owns the
// file: an editor rooted in a remote workspace still opens local
// files, and the remote data directory it resolved does not exist on
// the IDE host. Writing there failed outright under /home on macOS,
// where autofs rejects the mkdir with ENOTSUP.
func TestSwapDirectory(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		file string
		want string
	}{
		{
			name: "a local workspace resolves the local data dir",
			uri:  "file:///Users/x/src/proj",
			file: "file:///Users/x/src/proj/main.go",
			want: "/Users/x/.rune/swap",
		},
		{
			name: "a remote workspace resolves the host's data dir",
			uri:  "rune://peer/home/remote/src/proj",
			file: "rune://peer/home/remote/src/proj/main.go",
			want: "/home/remote/.rune/swap",
		},
		{
			name: "a local file opened from a remote workspace stays local",
			uri:  "rune://peer/home/remote/src/proj",
			file: "file:///Users/x/src/proj/main.go",
			want: "/Users/x/.rune/swap",
		},
		{
			// No data directory was ever resolved for a third host, so
			// the sibling layout is the only one that can work there.
			name: "a file on a third host falls back to the sibling layout",
			uri:  "rune://peer/home/remote/src/proj",
			file: "ssh://other/srv/main.go",
			want: "",
		},
		{
			name: "another user on the workspace's host has another home",
			uri:  "ssh://x@host/home/x/src/proj",
			file: "ssh://y@host/home/y/src/proj/main.go",
			want: "",
		},
	}

	cfg := swapDirIDEConfig(t, true)
	h := &workspaceManagerHandler{sixDir: "/Users/x/.rune"}
	ws := uriWorkspace{install: func(context.Context) (string, error) {
		return "/home/remote/.rune", nil
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uri, err := workspaceapi.ParseURI(tt.uri)
			require.NoError(t, err)
			file, err := workspaceapi.ParseURI(tt.file)
			require.NoError(t, err)

			assert.Equal(t, tt.want, h.swapDirectory(cfg, ws, uri)(file))
		})
	}

	t.Run("editor.swap_dir false installs no resolver", func(t *testing.T) {
		uri, err := workspaceapi.ParseURI("file:///Users/x/src/proj")
		require.NoError(t, err)
		assert.Nil(t, h.swapDirectory(swapDirIDEConfig(t, false), ws, uri))
	})
}

// TestWorkspaceRootURI pins the fix for the remote gopls failure
// "root uri is not contained in the workspace root": the manager root must
// be resolved through the workspace host (expanding a literal ~) so it
// matches the RootURI language extensions derive from fs.URI(".").
func TestWorkspaceRootURI(t *testing.T) {
	mustURI := func(t *testing.T, s string) workspaceapi.URI {
		u, err := workspaceapi.ParseURI(s)
		require.NoError(t, err)
		return u
	}

	t.Run("expands ~ to the host home path", func(t *testing.T) {
		raw := mustURI(t, "ssh://10.0.0.6/~/src/rune")
		ws := uriWorkspace{fn: func(path string) (workspaceapi.URI, error) {
			require.Equal(t, ".", path)
			return workspaceapi.ParseURI("ssh://10.0.0.6/home/ernest/src/rune")
		}}
		got, err := workspaceRootURI(ws, raw)
		require.NoError(t, err)
		assert.Equal(t, "/home/ernest/src/rune", got.Path())
	})

	t.Run("returns an error when the host cannot resolve the root", func(t *testing.T) {
		raw := mustURI(t, "ssh://10.0.0.6/~/src/rune")
		ws := uriWorkspace{fn: func(string) (workspaceapi.URI, error) {
			return workspaceapi.URI{}, errors.New("boom")
		}}
		_, err := workspaceRootURI(ws, raw)
		require.Error(t, err,
			"an unresolved root is fatal; the build must not proceed with a bogus root")
	})

	t.Run("panics when the workspace dependency is nil", func(t *testing.T) {
		raw := mustURI(t, "ssh://10.0.0.6/~/src/rune")
		assert.Panics(t, func() { _, _ = workspaceRootURI(nil, raw) },
			"a nil workspace is a wiring bug and must fail loudly")
	})
}

// newHandlerWithDataDir builds a handler whose IDE storage lives in
// dataDir, so a test can seed that storage beforehand or hand the same
// dataDir to a later handler.
func newHandlerWithDataDir(
	t *testing.T, dataDir string, uri *workspaceapi.URI,
	prepare ...func(*workspaceManagerHandler),
) *testWorkspaceManagerHandler {
	t.Helper()
	cc := defaultConfigWithWrap(false)
	cc.ringBell = func() {}
	mu, sched, drain := buildTestSchedulerForCfg(t, &cc)
	manager := workspace.NewManager(config.NopConfig(), sched)
	require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme,
		workspace.NewMemoryScheme))
	return newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
		uri, cc, FuncExtensionsRunner(testRunnerFn), nil, dataDir, nil,
		nopShutdownShaderConfig(), prepare...)
}

// seedLastSession writes the last-session document a previous run would
// have left behind in dataDir.
func seedLastSession(
	t *testing.T, dataDir string, workspaces ...idehistory.SessionWorkspace,
) {
	t.Helper()
	storage := localstorage.New(
		context.Background(), dataDir, docbson.Marshaler())
	defer func() { require.NoError(t, storage.Close()) }()
	store := idehistory.New(storageapi.WithPartition(storage, "ide"))
	require.NoError(t, store.StoreLastSession(context.Background(),
		idehistory.Session{Workspaces: workspaces}))
}

func loadLastSession(
	t *testing.T, m *testWorkspaceManagerHandler,
) []idehistory.SessionWorkspace {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	session, err := m.state.LoadLastSession(context.Background())
	require.NoError(t, err)
	return session.Workspaces
}

func floatingWindows(m *testWorkspaceManagerHandler) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	m.focusBrowser().IterateWindows(func(w browser.Window) {
		if w.IsFloating() {
			n++
		}
	})
	return n
}

// TestWorkspaceManagerPersistsLastSession pins that the persisted
// last-session document tracks every mutation of the installed set, so
// a crash at any point still leaves an accurate snapshot behind.
func TestWorkspaceManagerPersistsLastSession(t *testing.T) {
	dataDir := t.TempDir()
	uriA := mustURI(t, "memory:///session/a")
	uriB := mustURI(t, "memory:///session/b")

	m := newHandlerWithDataDir(t, dataDir, &uriA)
	m.mu.Lock()
	m.Resize(80, 24)
	m.mu.Unlock()

	assert.Equal(t, []idehistory.SessionWorkspace{{URI: uriA, Slot: 0}},
		loadLastSession(t, m), "the startup workspace must be recorded")

	m.mu.Lock()
	require.NoError(t, m.addWorkspace(uriB, false, false, -1))
	m.mu.Unlock()
	m.waitForWorkspace(t, uriB)
	assert.Equal(t, []idehistory.SessionWorkspace{
		{URI: uriA, Slot: 0}, {URI: uriB, Slot: 1},
	}, loadLastSession(t, m), "an added workspace must be recorded")

	m.mu.Lock()
	require.NoError(t, m.moveWorkspace("left"))
	m.mu.Unlock()
	assert.Equal(t, []idehistory.SessionWorkspace{
		{URI: uriB, Slot: 0}, {URI: uriA, Slot: 1},
	}, loadLastSession(t, m), "a moved workspace must be recorded in its new slot")

	m.mu.Lock()
	_, _, err := m.closeWorkspace()
	require.NoError(t, err)
	m.mu.Unlock()
	m.quiesce()
	assert.Equal(t, []idehistory.SessionWorkspace{{URI: uriA, Slot: 1}},
		loadLastSession(t, m), "a closed workspace must be dropped")

	require.NoError(t, m.Close())
}

// TestWorkspaceManagerReopensLastSession covers the startup offer to
// reopen the workspaces the previous session left open.
func TestWorkspaceManagerReopensLastSession(t *testing.T) {
	uriA := mustURI(t, "memory:///reopen/a")
	uriB := mustURI(t, "memory:///reopen/b")

	t.Run("the launched workspace is deduped from the offer", func(t *testing.T) {
		m := newHandlerWithDataDir(t, t.TempDir(), &uriA)

		m.mu.Lock()
		m.lastSession = idehistory.Session{Workspaces: []idehistory.SessionWorkspace{
			{URI: uriA, Slot: 0}, {URI: uriB, Slot: 2},
		}}
		targets := m.lastSessionReopenTargets()
		m.mu.Unlock()
		assert.Equal(t,
			[]idehistory.SessionWorkspace{{URI: uriB, Slot: 2}}, targets)

		require.NoError(t, m.Close())
	})

	t.Run("prompt declined leaves only the launched workspace", func(t *testing.T) {
		dataDir := t.TempDir()
		seedLastSession(t, dataDir,
			idehistory.SessionWorkspace{URI: uriA, Slot: 0},
			idehistory.SessionWorkspace{URI: uriB, Slot: 1})

		m := newHandlerWithDataDir(t, dataDir, &uriA)
		m.quiesce()

		m.mu.Lock()
		_, installed := m.findInstalledSlot(uriB)
		targets := []idehistory.SessionWorkspace{{URI: uriB, Slot: 1}}
		m.mu.Unlock()
		assert.False(t, installed,
			"the reopen must wait for consent")
		assert.Equal(t, 1, floatingWindows(m),
			"a reopen prompt must be offered")

		m.mu.Lock()
		(&reopenSessionPromptHandler{
			wm: m.workspaceManagerHandler, targets: targets,
		}).OnSelect(1, noOpt)
		m.mu.Unlock()

		_, installed = m.findInstalledSlot(uriB)
		assert.False(t, installed)
		assert.Equal(t, []idehistory.SessionWorkspace{{URI: uriA, Slot: 0}},
			loadLastSession(t, m),
			"declining must overwrite the snapshot so it is not offered again")

		require.NoError(t, m.Close())
	})

	t.Run("prompt accepted reopens the workspaces", func(t *testing.T) {
		dataDir := t.TempDir()
		seedLastSession(t, dataDir,
			idehistory.SessionWorkspace{URI: uriA, Slot: 0},
			idehistory.SessionWorkspace{URI: uriB, Slot: 1})

		m := newHandlerWithDataDir(t, dataDir, &uriA)
		m.quiesce()

		m.mu.Lock()
		(&reopenSessionPromptHandler{
			wm:      m.workspaceManagerHandler,
			targets: []idehistory.SessionWorkspace{{URI: uriB, Slot: 2}},
		}).OnSelect(0, yesOpt)
		m.mu.Unlock()
		m.waitForWorkspace(t, uriB)

		m.mu.Lock()
		slotB, okB := m.findInstalledSlot(uriB)
		m.mu.Unlock()
		require.True(t, okB)
		assert.Equal(t, 2, slotB,
			"a reopened workspace must land in its recorded slot")

		require.NoError(t, m.Close())
	})

	t.Run("no previous session makes no offer", func(t *testing.T) {
		dataDir := t.TempDir()
		m := newHandlerWithDataDir(t, dataDir, &uriA)
		m.quiesce()

		assert.Equal(t, 0, floatingWindows(m))
		m.mu.Lock()
		count := m.workspaceCount
		m.mu.Unlock()
		assert.Equal(t, 1, count)

		require.NoError(t, m.Close())
	})

	t.Run("a secondary window makes no offer", func(t *testing.T) {
		dataDir := t.TempDir()
		seedLastSession(t, dataDir,
			idehistory.SessionWorkspace{URI: uriA, Slot: 0},
			idehistory.SessionWorkspace{URI: uriB, Slot: 1})

		m := newHandlerWithDataDir(t, dataDir, &uriA,
			func(h *workspaceManagerHandler) { h.sessionReopenDisabled = true })
		m.quiesce()

		assert.Equal(t, 0, floatingWindows(m),
			"a window spawned from a running instance must not reopen its session")
		m.mu.Lock()
		_, installed := m.findInstalledSlot(uriB)
		m.mu.Unlock()
		assert.False(t, installed)

		require.NoError(t, m.Close())
	})

	t.Run("the offer lists the workspaces", func(t *testing.T) {
		m := newHandlerWithDataDir(t, t.TempDir(), &uriA)

		m.mu.Lock()
		m.userHome = "/Users/someone"
		msg := m.reopenSessionPromptMessage([]idehistory.SessionWorkspace{
			{URI: mustURI(t, "file:///Users/someone/src/rune"), Slot: 0},
			{URI: uriB, Slot: 1},
		})
		m.mu.Unlock()
		assert.Equal(t,
			"Do you want to **open** the following workspaces "+
				"from your last session?\n"+
				"\n- ~/src/rune"+
				"\n- "+uriB.String(), msg)

		require.NoError(t, m.Close())
	})
}

// TestWorkspaceManagerRestoresWorkspaceName pins that a name set with
// workspacerename is persisted as soon as it is set and comes back when
// the workspace is opened again.
func TestWorkspaceManagerRestoresWorkspaceName(t *testing.T) {
	dataDir := t.TempDir()
	uri := mustURI(t, "memory:///named/workspace")
	m := newHandlerWithDataDir(t, dataDir, &uri)

	m.mu.Lock()
	m.Resize(80, 24)
	require.NoError(t, m.commandRenameWorkspace("api"))
	require.Equal(t, "api", m.workspaces[m.focus].tabname)
	state, err := m.state.LoadWorkspaceState(context.Background(), uri)
	require.NoError(t, err)
	m.mu.Unlock()
	assert.Equal(t, "api", state.Name,
		"a rename must be durable without waiting for the workspace to close")

	m.mu.Lock()
	require.NoError(t, m.commandCloseWorkspace())
	require.Equal(t, 0, m.workspaceCount)
	require.NoError(t, m.addWorkspace(uri, true, false, -1))
	m.mu.Unlock()
	m.waitForWorkspace(t, uri)

	m.mu.Lock()
	name := m.workspaces[m.focus].tabname
	barName := m.makeWorkspaceTabName(m.focus, m.workspaces[m.focus])
	m.mu.Unlock()
	assert.Equal(t, "api", name)
	assert.Equal(t, "api", barName)

	require.NoError(t, m.Close())
}

// keyEvent renders a KeyComb into a term.Event with the Raw bytes
// vte expects. Unlike handlertest.RunHandlerSequence, vte's input
// path falls back to ev.Raw for non-special keys (e.g. plain ASCII
// runes), so Ch alone is insufficient — the pty would receive an
// empty write and the embedded editor would not see the keystroke.
func keyEvent(k term.KeyComb) term.Event {
	ev := term.Event{
		Type: term.EventKey, Mod: k.Mod, Key: k.Key, Ch: k.Ch,
	}
	switch {
	case k.Key == term.KeyEsc:
		ev.Raw = []byte{0x1b}
	case k.Key == term.KeyEnter:
		ev.Raw = []byte{0x0d}
	case k.Key == term.KeySpace:
		ev.Raw = []byte{' '}
	case k.Key == term.KeyTab:
		ev.Raw = []byte{0x09}
	case k.Key == term.KeyBackspace:
		ev.Raw = []byte{0x7f}
	case k.Mod == term.ModCtrl && k.Ch >= 'a' && k.Ch <= 'z':
		ev.Raw = []byte{byte(k.Ch - 'a' + 1)}
	case k.Mod == term.ModCtrl && k.Ch == '\\':
		ev.Raw = []byte{0x1c}
	case k.Ch != 0:
		ev.Raw = []byte(string(k.Ch))
	}
	return ev
}
