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
	"slices"
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
	blueauth "github.com/unstablebuild/blue/auth"
	"github.com/unstablebuild/blue/document"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release/docrelease"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/docmarshal/docbson"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	sdkiterator "github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/cell"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/component/asciiart"
	"unstable.build/rune/internal/component/imageuri/imageuritest"
	"unstable.build/rune/internal/component/notifications"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension"
	"unstable.build/rune/internal/extension/extensionv2"
	thandler "unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/handler/handlertest"
	handlermarkdown "unstable.build/rune/internal/handler/markdown"
	"unstable.build/rune/internal/ide/ideauthorizer"
	"unstable.build/rune/internal/ide/idehistory"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
	"unstable.build/rune/internal/ide/idestatus"
	"unstable.build/rune/internal/ide/idetask"
	"unstable.build/rune/internal/ide/plugin"
	"unstable.build/rune/internal/ide/vctrl/testgit"
	"unstable.build/rune/internal/localstorage"
	"unstable.build/rune/internal/term/programstatus"
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

func TestFileExplorerEnterDelegatesIntegration(t *testing.T) {
	cases := []struct {
		name     string
		mode     string
		readOnly bool
	}{
		{name: "vim", mode: editorModeVim},
		{name: "standard", mode: editorModeStandard},
		{name: "standard read-only", mode: editorModeStandard, readOnly: true},
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
			editorCfg["file_explorer"] = map[string]any{
				"read_only": tc.readOnly,
			}
			cfg.cfg["editor"] = editorCfg
			require.Equal(t, tc.mode, cfg.editorMode())
			require.Equal(t, tc.readOnly, cfg.fileExplorerReadOnly())

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
			case editorModeVim:
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
				// The cursor sits on the first row, a directory
				// (they sort first). A modeless editor is always
				// inserting, so while the explorer is editable
				// <Enter> must stay a newline; only the lock turns
				// it into expand-or-open. An expanded directory is
				// the only thing that puts an indent guide in the
				// buffer.
				_, handled := h.Handle(term.Event{
					Type: term.EventKey, Key: term.KeyEnter,
				})
				require.True(t, handled,
					"<Enter> must be handled")
				require.False(t, explorer.ed.IsSearchMode(),
					"standard must not be in search mode after "+
						"<Enter> on a non-search context")
				buf := term.CellsToString(explorer.ed.CellView().RawCells())
				if tc.readOnly {
					require.Contains(t, buf, "│",
						"<Enter> on a locked explorer must expand "+
							"the directory under the cursor")
					return
				}
				require.NotContains(t, buf, "│",
					"<Enter> on an editable modeless explorer must "+
						"not expand the tree")
				require.Equal(t, beforeRows+1,
					explorer.ed.CellView().Rows(),
					"<Enter> on an editable modeless explorer must "+
						"insert a newline")
			}
		})
	}
}

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

func TestGitlinkIntegration(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}

	for _, mode := range []string{editorModeVim, editorModeStandard} {
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
	// SetTabName routes the bell attribute through
	// f.parent.scheduleNextTick, which under the default test stub
	// dispatches on a fresh goroutine. The bell fires from the vte
	// parser path before that scheduled tick runs, so bellRung above
	// only proves the bell was rung — it does not guarantee the
	// bell attr propagated to workspaces[1] yet. Block here until the
	// bar tab actually reflects the bell attr so the final render is
	// deterministic.
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		if len(m.workspaces) < 2 || m.workspaces[1] == nil {
			return false
		}
		return m.workspaces[1].bellAttr != (term.Attributes{})
	}, 5*time.Second, 5*time.Millisecond,
		"workspace bell attr never propagated after bell")
	m.mu.Lock()
	bellAttr := m.workspaces[1].bellAttr
	barIdx := slices.Index(m.barIdxToSlot, 1)
	require.GreaterOrEqual(t, barIdx, 0)
	assert.Equal(t, bellAttr, m.bar.TabAttr(barIdx),
		"the bar tab of the unfocused workspace carries the bell attr")
	m.mu.Unlock()
	cases = []handlertest.SequenceTestCase{
		{InputSequence: "",
			Expected: `┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│     workspaceWallpaper     │
│                            │
├────────────────────────────┤
│1 1  2 2                    │
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
	m.quiesce()

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
│1 ##  2 #                   │
└──────###───────────────────┘`},
	}
	handlertest.RunHandlerSequenceWriter(t, newWriterForAttrTesting(30, 9),
		h, 30, 9, cases)

	require.NoError(t, m.Close())
}

// newActivityTestManager opens two workspaces and returns the manager
// with the slot of the one that is not focused.
func newActivityTestManager(t *testing.T, cfg ideConfig) (*testWorkspaceManagerHandler, int) {
	t.Helper()
	dir := t.TempDir()
	uri1, err := workspaceapi.ParseURI("memory://" + dir)
	require.NoError(t, err)
	uri2, err := workspaceapi.ParseURI("memory:///b")
	require.NoError(t, err)

	m := newTestWorkspaceManagerHandlerWithDir(t, cfg, dir, nopShutdownShaderConfig())
	require.NoError(t, m.addOrCreateWorkspace(uri1))
	m.quiesce()
	require.NoError(t, m.addOrCreateWorkspace(uri2))
	m.quiesce()

	m.mu.Lock()
	defer m.mu.Unlock()
	m.Resize(30, 9)
	for slot, w := range m.workspaces {
		if w != nil && slot != m.focus {
			return m, slot
		}
	}
	t.Fatal("no unfocused workspace")
	return nil, 0
}

func TestWorkspaceManagerHandlerPointerLeaves(t *testing.T) {
	for _, size := range []term.Coordinates{{X: 100, Y: 30}, {X: 2, Y: 2}} {
		m, _ := newActivityTestManager(t, defaultConfigWithWrap(false))
		h := newSafeHandler(m)
		h.mu.Lock()
		focus := m.focus
		h.mu.Unlock()
		handlertest.RunPointerLeaves(t, h, size.X, size.Y)
		h.mu.Lock()
		workspaces := m.workspaceCount
		h.mu.Unlock()
		assert.Equal(t, 2, workspaces, "a leave closes no workspace")
		if size.X == 2 {
			h.mu.Lock()
			assert.Equal(t, focus, m.focus)
			h.mu.Unlock()
		}
		require.NoError(t, m.Close())
	}
}

// pointerProbe records the events its window is sent.
type pointerProbe struct {
	*browsertest.TestHandler
	events        []term.Event
	width, height int
}

func (p *pointerProbe) Resize(width, height int) {
	p.width, p.height = width, height
	p.TestHandler.Resize(width, height)
}

func (p *pointerProbe) Handle(ev term.Event) (bool, bool) {
	p.events = append(p.events, ev)
	return p.TestHandler.Handle(ev)
}

func TestWorkspaceManagerHandlerTellsTheWorkspaceThePointerLeftForTheBar(t *testing.T) {
	m, _ := newActivityTestManager(t, defaultConfigWithWrap(false))
	t.Cleanup(func() { _ = m.Close() })
	probe := &pointerProbe{TestHandler: browsertest.NewTestHandler()}
	h := newSafeHandler(m)
	h.Resize(30, 9)
	h.mu.Lock()
	win, err := m.focusEx().comp.Focus()
	if err == nil {
		err = win.SetContent(probe)
	}
	// The bar's first row is the frame it shares with the workspace.
	barTabsRow := m.height - m.barSize() + 1
	h.mu.Unlock()
	require.NoError(t, err)
	m.quiesce()

	_, _ = h.Handle(term.Event{Type: term.EventMouse, MouseX: 15, MouseY: 4})
	require.NotEmpty(t, probe.events, "sanity: the window is under the pointer")
	probe.events = nil
	_, _ = h.Handle(term.Event{Type: term.EventMouse, MouseX: 15, MouseY: barTabsRow})
	require.Len(t, probe.events, 1, "the window is told the pointer left it")
	leave := probe.events[0]
	assert.Zero(t, leave.Key)
	assert.False(t, leave.MouseX >= 0 && leave.MouseX < probe.width &&
		leave.MouseY >= 0 && leave.MouseY < probe.height,
		"off the window: %+v in %dx%d", leave, probe.width, probe.height)
}

func TestWorkspaceBarShadesActiveWorkspaces(t *testing.T) {
	m, slot := newActivityTestManager(t, defaultConfigWithWrap(false))
	chat, err := workspaceapi.ParseURI("rune-agent://model/rolling-fox")
	require.NoError(t, err)

	m.mu.Lock()
	require.NotNil(t, m.shadedBar)
	assert.False(t, m.shadedBar.Running())

	comp := &m.workspaces[slot].ex.comp
	_, err = comp.Tab(chat, 'x', "rolling-fox", browsertest.NewTestHandler())
	require.NoError(t, err)
	require.NoError(t, comp.SetTabStatus(chat, workingStatus()))
	assert.True(t, m.shadedBar.Running(),
		"an unfocused workspace's activity shows on the bar")
	barIdx := slices.Index(m.barIdxToSlot, slot)
	require.GreaterOrEqual(t, barIdx, 0)
	assert.Equal(t, []int{barIdx}, m.activeWorkspaceBarIndices())

	require.True(t, comp.OnTabExit(chat))
	assert.False(t, comp.HasWorkingTabs())
	assert.False(t, m.shadedBar.Running(),
		"closing the active tab stops the effect")
	assert.Empty(t, m.activeWorkspaceBarIndices())
	m.mu.Unlock()

	require.NoError(t, m.Close())
}

func TestWorkspaceBarNotificationBadge(t *testing.T) {
	m, slot := newActivityTestManager(t, defaultConfigWithWrap(false))
	chat, err := workspaceapi.ParseURI("rune-agent://model/rolling-fox")
	require.NoError(t, err)

	m.mu.Lock()
	comp := &m.workspaces[slot].ex.comp
	_, err = comp.Tab(chat, 'x', "rolling-fox", browsertest.NewTestHandler())
	require.NoError(t, err)
	require.NoError(t, comp.SetTabStatus(chat, workingStatus()))
	require.True(t, m.shadedBar.Running())
	barIdx := slices.Index(m.barIdxToSlot, slot)
	require.GreaterOrEqual(t, barIdx, 0)
	require.Equal(t, []int{barIdx}, m.activeWorkspaceBarIndices())
	uri := m.workspaces[slot].uri
	m.mu.Unlock()

	yellow := term.Attributes{Fg: term.ColorYellow}
	red := term.Attributes{Fg: term.ColorRed}
	m.addWorkspaceNotification(uri, browserapi.LevelWarn, yellow)
	m.quiesce()
	m.addWorkspaceNotification(uri, browserapi.LevelError, red)
	m.quiesce()
	m.addWorkspaceNotification(uri, browserapi.LevelInfo, term.Attributes{Fg: term.ColorBlue})
	m.quiesce()

	m.mu.Lock()
	assert.Equal(t, pendingNotifications{3, browserapi.LevelError, red},
		m.workspaces[slot].notifs)
	badge, badgeAttr := m.bar.TabBadge(barIdx)
	assert.Equal(t, "³", badge)
	assert.Equal(t, term.AttributesUnion(m.barNonFocusAttr, red), badgeAttr,
		"the badge takes the colour of the highest level pending")
	assert.Equal(t, []int{barIdx}, m.activeWorkspaceBarIndices(),
		"notifications no longer stop the working effect")
	assert.True(t, m.shadedBar.Running())
	m.mu.Unlock()

	h := newSafeHandler(m)
	handlertest.RunHandlerSequence(t, h, 30, 9, []handlertest.SequenceTestCase{
		{InputSequence: "", Expected: `┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│     workspaceWallpaper     │
│                            │
├────────────────────────────┤
│1 1³  2 2                   │
└──────━━━───────────────────┘`},
	})

	m.mu.Lock()
	require.True(t, m.switchToWorkspace(slot))
	assert.Equal(t, pendingNotifications{}, m.workspaces[slot].notifs)
	barIdx = slices.Index(m.barIdxToSlot, slot)
	require.GreaterOrEqual(t, barIdx, 0)
	badge, _ = m.bar.TabBadge(barIdx)
	assert.Empty(t, badge, "focusing clears the badge")
	assert.True(t, m.shadedBar.Running())
	assert.Contains(t, m.activeWorkspaceBarIndices(), barIdx)
	m.mu.Unlock()

	require.NoError(t, m.Close())
}

func TestTabStatusNotifications(t *testing.T) {
	cfg := defaultConfigWithWrap(false)
	// idestatus covers the rate limiting.
	cfg.cfg["terminal"] = map[string]any{"status": map[string]any{"notify_interval": "0s"}}
	m, slot := newActivityTestManager(t, cfg)
	chat, err := workspaceapi.ParseURI("rune-agent://model/rolling-fox")
	require.NoError(t, err)
	seen, err := workspaceapi.ParseURI("rune-agent://model/seen")
	require.NoError(t, err)
	notiCfg := notificationsConfig()
	warn := term.Attributes{Fg: notiCfg.ColorWarning.Fg}

	m.mu.Lock()
	w := m.workspaces[slot]
	comp := &w.comp
	notes := &recordingNotifications{inner: w.notifications}
	w.notifications = notes
	for _, tab := range []struct {
		uri  workspaceapi.URI
		name string
	}{{chat, "rolling-fox"}, {seen, "seen"}} {
		_, err = comp.Tab(tab.uri, 'x', tab.name, browsertest.NewTestHandler())
		require.NoError(t, err)
	}

	send := func(tab workspaceapi.URI, reports ...programstatus.Report) {
		t.Helper()
		for _, r := range reports {
			require.NoError(t, comp.SetTabStatus(tab, r))
		}
		m.mu.Unlock()
		m.quiesce()
		m.mu.Lock()
	}
	taken := 0
	newNotes := func() []capturedNote {
		all := notes.snapshot()
		ret := all[taken:]
		taken = len(all)
		return ret
	}
	note := func(level browserapi.NotificationLevel, msg string) []capturedNote {
		return []capturedNote{{level: level, msg: msg}}
	}
	report := func(state programstatus.State, msg string) programstatus.Report {
		return programstatus.Report{State: state, Progress: -1, Msg: msg}
	}
	updated := 0
	newUpdates := func() []capturedUpdate {
		all := notes.updatesSnapshot()
		ret := all[updated:]
		updated = len(all)
		return ret
	}

	send(chat, report(programstatus.Working, ""))
	assert.Empty(t, newNotes(), "working without a message is not notified")

	send(chat, report(programstatus.Working, "Compiling"))
	assert.Equal(t, note(browserapi.LevelInfo, "rolling-fox: Compiling"), newNotes(),
		"a message comes through whatever the state")
	assert.Equal(t, []capturedUpdate{{progress: 0, total: 1, ok: true}}, newUpdates(),
		"a working notification stays")

	send(chat, programstatus.Report{State: programstatus.Working, Progress: 40, Msg: "Linking"})
	assert.Empty(t, newNotes(), "the next working message updates it")
	assert.Contains(t, newUpdates(),
		capturedUpdate{msg: "rolling-fox: Linking", progress: 40, total: 100, ok: true})

	send(chat, programstatus.Report{State: programstatus.Working, Progress: 60, Msg: "Linking"})
	assert.Empty(t, newNotes(), "an unchanged message is not repeated")
	assert.Equal(t, []capturedUpdate{{progress: 60, total: 100, ok: true}}, newUpdates(),
		"the working notification follows the progress")

	send(chat, programstatus.Report{
		ID: "tests", State: programstatus.Working, Progress: -1, App: "go", Msg: "ok ./internal",
	})
	assert.Empty(t, newNotes())
	assert.Contains(t, newUpdates(),
		capturedUpdate{msg: "rolling-fox (go): ok ./internal", progress: 0, total: 1, ok: true},
		"messages of child records come through")

	send(chat, programstatus.Report{
		State: programstatus.Blocked, Kind: programstatus.KindPermission,
		Progress: -1, App: "claude", Msg: "run make test?",
	})
	assert.Equal(t, note(browserapi.LevelWarn, "rolling-fox (claude): run make test?"), newNotes(),
		"the message stands for the transition it comes with")
	assert.Equal(t, []capturedUpdate{{progress: 1, total: 1, ok: true}}, newUpdates(),
		"leaving working closes the working notification")
	assert.Equal(t, pendingNotifications{2, browserapi.LevelWarn, warn}, w.notifs,
		"an unfocused workspace counts every notification")

	send(chat, report(programstatus.Done, "ok"), report(programstatus.Error, "boom"))
	assert.Equal(t, []capturedNote{
		{level: browserapi.LevelSuccess, msg: "rolling-fox: ok"},
		{level: browserapi.LevelError, msg: "rolling-fox: boom"},
	}, newNotes(), "messages are never rate limited")

	send(chat, report(programstatus.Done, ""))
	assert.Equal(t, note(browserapi.LevelSuccess, "rolling-fox: done"), newNotes(),
		"a transition without a message names the state")
	send(chat, programstatus.Report{State: programstatus.Clear})
	assert.Empty(t, newNotes(), "clearing is not notified")

	require.True(t, m.switchToWorkspace(slot))
	win, err := comp.Focus()
	require.NoError(t, err)
	idx := slices.IndexFunc(comp.Tabs(), func(tab *browser.Tab) bool {
		return tab.URI().Equal(seen)
	})
	require.True(t, comp.Browser().SetContentToTab(win, idx))
	focusTab, ok := comp.FocusTab()
	require.True(t, ok)
	require.True(t, focusTab.URI().Equal(seen))

	send(seen, report(programstatus.Blocked, ""))
	assert.Empty(t, newNotes(), "a transition in sight is not notified")

	send(seen, report(programstatus.Blocked, "approve?"))
	assert.Equal(t, note(browserapi.LevelWarn, "seen: approve?"), newNotes(),
		"a message comes through in sight")

	send(chat, report(programstatus.Error, ""))
	assert.Equal(t, note(browserapi.LevelError, "rolling-fox: error"), newNotes(),
		"another tab of the focused workspace is out of sight")
	assert.Equal(t, pendingNotifications{}, w.notifs)
	m.mu.Unlock()

	require.NoError(t, m.Close())
}

func TestWorkspaceBarStatusProgress(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			cfg := defaultConfigWithWrap(false)
			cfg.cfg["terminal"] = map[string]any{
				"status": map[string]any{
					"content_tab":   map[string]any{"progress": !enabled},
					"workspace_tab": map[string]any{"progress": enabled},
				},
			}
			cfg.cfg["animations"] = map[string]any{
				"active_workspace_tab": map[string]any{"shader": "shine"},
			}
			m, slot := newActivityTestManager(t, cfg)
			t.Cleanup(func() { require.NoError(t, m.Close()) })
			m.mu.Lock()
			defer m.mu.Unlock()
			const width = 120
			m.workspaces[slot].tabname = "tinygrad"
			m.Resize(width, 9)
			chat, err := workspaceapi.ParseURI("rune-agent://model/rolling-fox")
			require.NoError(t, err)
			comp := &m.workspaces[slot].ex.comp
			_, err = comp.Tab(chat, 'x', "rolling-fox", browsertest.NewTestHandler())
			require.NoError(t, err)
			barIdx := slices.Index(m.barIdxToSlot, slot)
			require.GreaterOrEqual(t, barIdx, 0)
			m.bar.SetTabProgress(barIdx, 25, term.Attributes{Fg: term.ColorGreen})
			for _, progress := range []int{0, 50, 100} {
				require.NoError(t, comp.SetTabStatus(chat, programstatus.Report{
					State: programstatus.Working, Progress: progress,
				}))
				m.bar.Draw(term.NewStringWriter(width, m.barSize()))
				pct, _ := m.bar.TabProgress(barIdx)
				offset, fullWidth, ok := m.bar.TabRect(barIdx)
				require.True(t, ok)
				shadeOffset, shadeWidth, shadeOK := m.bar.TabShadeRect(barIdx)
				if enabled {
					assert.Equal(t, progress, pct)
					if progress == 100 {
						assert.False(t, shadeOK)
					} else {
						require.True(t, shadeOK)
						if progress > 0 {
							assert.Less(t, shadeWidth, fullWidth)
						}
					}
				} else {
					assert.Equal(t, -1, pct)
					require.True(t, shadeOK)
					assert.Equal(t, offset, shadeOffset)
					assert.Equal(t, fullWidth, shadeWidth)
				}
				assert.True(t, m.shadedBar.Running())
				summary, _ := m.workspaceStatus(slot)
				assert.Equal(t, progress, summary.Progress)
			}
		})
	}
}

func TestWorkspaceBarStatusRollup(t *testing.T) {
	cfg := defaultConfigWithWrap(false)
	cfg.cfg["terminal"] = map[string]any{
		"status": map[string]any{
			"workspace_tab": map[string]any{"progress": true},
		},
	}
	m, slot := newActivityTestManager(t, cfg)
	chat, err := workspaceapi.ParseURI("rune-agent://model/rolling-fox")
	require.NoError(t, err)
	other, err := workspaceapi.ParseURI("rune-agent://model/other")
	require.NoError(t, err)
	status := browser.DefaultTabStatusConfig().WorkspaceTab

	m.mu.Lock()
	defer m.mu.Unlock()
	// A name long enough to split, on a bar wide enough to show it.
	const width = 120
	m.workspaces[slot].tabname = "tinygrad"
	m.Resize(width, 9)
	comp := &m.workspaces[slot].ex.comp
	_, err = comp.Tab(chat, 'x', "rolling-fox", browsertest.NewTestHandler())
	require.NoError(t, err)
	_, err = comp.Tab(other, 'x', "other", browsertest.NewTestHandler())
	require.NoError(t, err)
	barIdx := slices.Index(m.barIdxToSlot, slot)
	require.GreaterOrEqual(t, barIdx, 0)

	assert.Equal(t, term.Attributes{}, m.bar.TabAttr(barIdx))

	require.NoError(t, comp.SetTabStatus(chat, workingStatus()))
	assert.Equal(t, term.Attributes{}, m.bar.TabAttr(barIdx),
		"working keeps the name attribute")
	assert.Equal(t, m.barNonFocusIconAttr, m.bar.TabIconAttr(barIdx),
		"the icon keeps the bar's own attributes")
	assert.True(t, m.shadedBar.Running())
	pct, _ := m.bar.TabProgress(barIdx)
	assert.Equal(t, -1, pct, "indeterminate progress paints nothing")
	// TabShadeRect needs the layout of a draw.
	m.bar.Draw(term.NewStringWriter(width, m.barSize()))
	_, fullWidth, ok := m.bar.TabShadeRect(barIdx)
	require.True(t, ok)

	halfway := programstatus.Report{State: programstatus.Working, Progress: 50}
	require.NoError(t, comp.SetTabStatus(chat, halfway))
	pct, attr := m.bar.TabProgress(barIdx)
	assert.Equal(t, 50, pct, "the completed part is painted on the bar")
	assert.Equal(t, status.WorkingDoneAttr, attr)
	m.bar.Draw(term.NewStringWriter(width, m.barSize()))
	_, shadedWidth, ok := m.bar.TabShadeRect(barIdx)
	require.True(t, ok)
	assert.Less(t, shadedWidth, fullWidth, "the effect runs over the remaining part only")
	assert.True(t, m.shadedBar.Running())

	done := programstatus.Report{State: programstatus.Done, Progress: -1}
	require.NoError(t, comp.SetTabStatus(other, done))
	assert.Equal(t, status.DoneAttr, m.bar.TabAttr(barIdx),
		"done outranks working")
	assert.Equal(t, m.barNonFocusIconAttr, m.bar.TabIconAttr(barIdx))
	assert.False(t, m.shadedBar.Running(), "the effect runs only while working")
	pct, _ = m.bar.TabProgress(barIdx)
	assert.Equal(t, -1, pct, "progress goes with the working roll-up")

	blocked := programstatus.Report{State: programstatus.Blocked, Progress: -1}
	require.NoError(t, comp.SetTabStatus(chat, blocked))
	assert.Equal(t, status.BlockedAttr, m.bar.TabAttr(barIdx))
	assert.Equal(t, m.barNonFocusIconAttr, m.bar.TabIconAttr(barIdx))

	failed := programstatus.Report{State: programstatus.Error, Progress: -1}
	require.NoError(t, comp.SetTabStatus(other, failed))
	assert.Equal(t, status.BlockedAttr, m.bar.TabAttr(barIdx), "blocked outranks error")
	require.NoError(t, comp.SetTabStatus(chat, done))
	assert.Equal(t, status.ErrorAttr, m.bar.TabAttr(barIdx), "error outranks done")
	assert.Equal(t, m.barNonFocusIconAttr, m.bar.TabIconAttr(barIdx))
	require.NoError(t, comp.SetTabStatus(chat, blocked))

	// The bell ranks below the status.
	m.workspaces[slot].bellAttr = term.Attributes{Attrs: term.AttrBlink}
	m.decorateWorkspaceBar()
	assert.Equal(t, status.BlockedAttr, m.bar.TabAttr(barIdx))

	idle := programstatus.Report{State: programstatus.Clear}
	require.NoError(t, comp.SetTabStatus(chat, idle))
	require.NoError(t, comp.SetTabStatus(other, idle))
	assert.Equal(t, term.Attributes{Attrs: term.AttrBlink}, m.bar.TabAttr(barIdx),
		"the bell shows once the status is idle")
	assert.Equal(t, m.barNonFocusIconAttr, m.bar.TabIconAttr(barIdx))

	reportedIdle := programstatus.Report{State: programstatus.Idle, Progress: -1}
	require.NoError(t, comp.SetTabStatus(chat, reportedIdle))
	assert.Equal(t, m.barNonFocusIconAttr, m.bar.TabIconAttr(barIdx))
	assert.Equal(t, term.Attributes{Attrs: term.AttrBlink}, m.bar.TabAttr(barIdx))
	assert.False(t, m.shadedBar.Running())
	require.NoError(t, comp.SetTabStatus(chat, idle))
	assert.Equal(t, m.barNonFocusIconAttr, m.bar.TabIconAttr(barIdx))

	require.True(t, m.switchToWorkspace(slot))
	barIdx = slices.Index(m.barIdxToSlot, slot)
	assert.Equal(t, term.Attributes{}, m.bar.TabAttr(barIdx), "focusing clears the bell")
}

func TestWorkspaceTabInSightOnlyWhileFocused(t *testing.T) {
	m, slot := newActivityTestManager(t, defaultConfigWithWrap(false))
	chat, err := workspaceapi.ParseURI("rune-agent://model/rolling-fox")
	require.NoError(t, err)

	m.mu.Lock()
	defer m.mu.Unlock()
	comp := &m.workspaces[slot].ex.comp
	tab, err := comp.Tab(chat, 'x', "rolling-fox", browsertest.NewTestHandler())
	require.NoError(t, err)
	require.NoError(t, comp.Browser().Focus().SetContent(tab))
	state := func() programstatus.State {
		t.Helper()
		status, ok := comp.Browser().TabStatus(chat)
		require.True(t, ok)
		return status.State
	}
	done := programstatus.Report{State: programstatus.Done, Progress: -1}

	require.NoError(t, comp.SetTabStatus(chat, done))
	assert.Equal(t, programstatus.Done, state(), "the tab of a workspace out of focus is not in sight")

	focused := m.focus
	require.True(t, m.switchToWorkspace(slot))
	assert.Equal(t, programstatus.Idle, state(), "switching to its workspace sees it")

	require.NoError(t, comp.SetTabStatus(chat, done))
	assert.Equal(t, programstatus.Idle, state(), "reported in sight")

	require.True(t, m.switchToWorkspace(focused))
	require.NoError(t, comp.SetTabStatus(chat, done))
	assert.Equal(t, programstatus.Done, state(), "switching away takes it out of sight")
}

func TestWorkspaceBarStatusFromTerminalWithoutTab(t *testing.T) {
	m, slot := newActivityTestManager(t, defaultConfigWithWrap(false))
	pty, err := workspaceapi.ParseURI("file:///dev/ttys042")
	require.NoError(t, err)
	status := browser.DefaultTabStatusConfig().WorkspaceTab

	m.mu.Lock()
	w := m.workspaces[slot]
	notes := &recordingNotifications{inner: w.notifications}
	w.notifications = notes
	barIdx := slices.Index(m.barIdxToSlot, slot)
	require.GreaterOrEqual(t, barIdx, 0)
	publish := func(reports ...programstatus.Report) {
		t.Helper()
		// As a vte in a floating window or a task does: its pty is
		// no tab of the workspace.
		for _, r := range reports {
			require.NoError(t, w.ex.tm.SetTabStatus(pty, r))
		}
		m.mu.Unlock()
		m.quiesce()
		m.mu.Lock()
	}

	publish(programstatus.Report{State: programstatus.Working, Progress: -1})
	sum, from := m.workspaceStatus(slot)
	assert.Equal(t, programstatus.Working, sum.State)
	assert.Equal(t, pty.String(), from.String())
	assert.Equal(t, term.AttributesUnion(m.barNonFocusIconAttr, status.WorkingIconAttr),
		m.bar.TabIconAttr(barIdx))
	assert.True(t, m.shadedBar.Running())

	publish(programstatus.Report{State: programstatus.Working, Progress: -1, Msg: "Compiling"})
	publish(programstatus.Report{State: programstatus.Error, Progress: -1, App: "make", Msg: "boom"})
	assert.Equal(t, status.ErrorAttr, m.bar.TabAttr(barIdx))
	assert.Equal(t, term.AttributesUnion(m.barNonFocusIconAttr, status.ErrorIconAttr),
		m.bar.TabIconAttr(barIdx))
	assert.False(t, m.shadedBar.Running())
	assert.Equal(t, []capturedNote{
		{level: browserapi.LevelInfo, msg: "terminal: Compiling"},
		{level: browserapi.LevelError, msg: "make: boom"},
	}, notes.snapshot())

	// The program ended, then the terminal closed, withdrawing what it
	// reported.
	require.NoError(t, w.ex.tm.EndTabStatus(pty))
	m.mu.Unlock()
	m.quiesce()
	m.mu.Lock()
	sum, _ = m.workspaceStatus(slot)
	assert.Equal(t, programstatus.Error, sum.State, "ending keeps the error")
	require.NoError(t, w.ex.tm.SetTabStatus(pty, programstatus.Report{State: programstatus.Clear}))
	m.mu.Unlock()
	m.quiesce()
	m.mu.Lock()
	sum, _ = m.workspaceStatus(slot)
	assert.Equal(t, programstatus.Idle, sum.State)
	assert.Equal(t, term.Attributes{}, m.bar.TabAttr(barIdx))
	assert.Equal(t, m.barNonFocusIconAttr, m.bar.TabIconAttr(barIdx))
	assert.Empty(t, m.status.Items())
	m.mu.Unlock()

	require.NoError(t, m.Close())
}

func TestStatusSources(t *testing.T) {
	m, slot := newActivityTestManager(t, defaultConfigWithWrap(false))
	t.Cleanup(func() { _ = m.Close() })
	m.mu.Lock()
	defer m.mu.Unlock()

	assert.Empty(t, m.status.Items())

	other := m.workspaces[slot].ex
	focused := m.focusEx()
	open := func(e *ex, name string) workspaceapi.URI {
		uri, err := workspaceapi.ParseURI("rune-agent://model/" + name)
		require.NoError(t, err)
		_, err = e.comp.Tab(uri, 'x', name, browsertest.NewTestHandler())
		require.NoError(t, err)
		return uri
	}
	summaryOf := func(r programstatus.Report) programstatus.Summary {
		s := programstatus.New()
		s.Apply(r)
		return s.Summary()
	}
	assertSource := func(want, got idestatus.Item) {
		t.Helper()
		assert.Truef(t, want.Status.Equal(got.Status), "want %+v, got %+v", want.Status, got.Status)
		want.Status, got.Status = programstatus.Summary{}, programstatus.Summary{}
		assert.Equal(t, want, got)
	}
	workingReport := programstatus.Report{State: programstatus.Working, Progress: 40, Title: "cargo build"}
	failedReport := programstatus.Report{State: programstatus.Error, Progress: -1, Msg: "exit status 2"}
	doneReport := programstatus.Report{State: programstatus.Done, Progress: -1, Msg: "ok"}
	working := open(focused, "worker")
	require.NoError(t, focused.comp.SetTabStatus(working, workingReport))
	failed := open(other, "build")
	require.NoError(t, other.comp.SetTabStatus(failed, failedReport))
	silent := open(other, "silent")
	pty, err := workspaceapi.ParseURI("file:///dev/ttys042")
	require.NoError(t, err)
	m.status.SetTabStatus(m.workspaces[slot].uri, &other.comp, pty, doneReport)

	otherName := m.makeWorkspaceTabName(slot, m.workspaces[slot])
	focusedName := m.makeWorkspaceTabName(m.focus, m.workspaces[m.focus])
	sources := m.status.Items()
	byTab := map[string]idestatus.Item{}
	for _, s := range sources {
		byTab[s.URI.String()] = s
	}
	require.Len(t, sources, 3, "every reporting source is listed: %+v", sources)
	assertSource(idestatus.Item{
		Slot: m.focus, WS: m.workspaces[m.focus].uri, WorkspaceName: focusedName, URI: working, Name: "worker", Icon: 'x', Status: summaryOf(workingReport),
	}, byTab[working.String()])
	assertSource(idestatus.Item{
		Slot: slot, WS: m.workspaces[slot].uri, WorkspaceName: otherName, URI: failed, Name: "build", Icon: 'x', Status: summaryOf(failedReport),
	}, byTab[failed.String()])
	assert.NotContains(t, byTab, silent.String(), "a tab that reported nothing is not tracked")
	assertSource(idestatus.Item{
		Slot: slot, WS: m.workspaces[slot].uri, WorkspaceName: otherName, URI: pty, Name: "terminal", Status: summaryOf(doneReport),
	}, byTab[pty.String()])

	require.NoError(t, m.focusStatusEntry(m.workspaces[slot].uri, pty))
	assert.Equal(t, slot, m.focus, "a terminal that is no tab focuses its workspace")
	silentTab, ok := other.comp.Browser().Tab(silent)
	require.True(t, ok)
	require.True(t, other.comp.Browser().RemoveTab(silentTab))
	assert.Error(t, m.focusStatusEntry(m.workspaces[slot].uri, silent))
}

func TestPermissionPromptStatus(t *testing.T) {
	m, slot := newActivityTestManager(t, defaultConfigWithWrap(false))
	t.Cleanup(func() { _ = m.Close() })
	status := browser.DefaultTabStatusConfig().WorkspaceTab
	prompt, err := workspaceapi.ParseURI("ideauthorizer:///prompt/1")
	require.NoError(t, err)

	m.mu.Lock()
	w := m.workspaces[slot]
	notes := &recordingNotifications{inner: w.notifications}
	w.notifications = notes
	barIdx := slices.Index(m.barIdxToSlot, slot)
	require.GreaterOrEqual(t, barIdx, 0)
	reporter := w.ex.tm

	// As ideauthorizer announces a pending prompt.
	require.NoError(t, reporter.SetTabStatus(prompt, programstatus.Report{
		State: programstatus.Blocked, Kind: programstatus.KindPermission, Progress: -1,
		App: "rune", Title: "Authorization", Msg: "ext wants to run make",
	}))
	m.mu.Unlock()
	m.quiesce()
	m.mu.Lock()
	sum, from := m.workspaceStatus(slot)
	assert.Equal(t, programstatus.Blocked, sum.State)
	assert.Equal(t, prompt.String(), from.String())
	assert.Equal(t, status.BlockedAttr, m.bar.TabAttr(barIdx))
	assert.Equal(t, []capturedNote{{level: browserapi.LevelWarn, msg: "rune: ext wants to run make"}},
		notes.snapshot())
	items := m.status.Items()
	require.Len(t, items, 1)
	assert.Equal(t, prompt.String(), items[0].URI.String(), "the prompt is listed in the inbox")

	// And withdraws it once answered.
	require.NoError(t, reporter.SetTabStatus(prompt, programstatus.Report{State: programstatus.Clear}))
	m.mu.Unlock()
	m.quiesce()
	m.mu.Lock()
	sum, _ = m.workspaceStatus(slot)
	assert.Equal(t, programstatus.Idle, sum.State)
	assert.Equal(t, term.Attributes{}, m.bar.TabAttr(barIdx))
	assert.Empty(t, m.status.Items())
	m.mu.Unlock()
}

func TestWorkspaceTabManagerDropsClosedWorkspaceStatus(t *testing.T) {
	for _, end := range []bool{false, true} {
		t.Run(fmt.Sprintf("end=%t", end), func(t *testing.T) {
			m, slot := newActivityTestManager(t, defaultConfigWithWrap(false))
			t.Cleanup(func() { _ = m.Close() })
			m.mu.Lock()
			defer m.mu.Unlock()
			w := m.workspaces[slot]
			uri, err := workspaceapi.ParseURI("file:///dev/closed-status")
			require.NoError(t, err)
			var queued func()
			tm := &workspaceTabManager{workspace: w, parent: m.workspaceManagerHandler, tm: &w.comp}
			schedule := m.scheduleNextTick
			m.scheduleNextTick = func(fn func()) bool { queued = fn; return true }
			if end {
				require.NoError(t, tm.EndTabStatus(uri))
			} else {
				require.NoError(t, tm.SetTabStatus(uri, workingStatus()))
			}
			m.scheduleNextTick = schedule
			require.NotNil(t, queued)
			m.workspaces[slot] = &workspaceHandler{uri: w.uri}
			queued()
			m.workspaces[slot] = w
			_, ok := m.status.Status(w.uri, uri)
			assert.False(t, ok)
		})
	}
}

func TestCommandProgramStatusFocusesEntry(t *testing.T) {
	m, slot := newActivityTestManager(t, defaultConfigWithWrap(false))
	t.Cleanup(func() { _ = m.Close() })
	blocked, err := workspaceapi.ParseURI("rune-agent://model/agent")
	require.NoError(t, err)

	var other *ex
	func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		other = m.workspaces[slot].ex
		_, err = other.comp.Tab(blocked, 'x', "agent", browsertest.NewTestHandler())
		require.NoError(t, err)
		require.NoError(t, other.comp.SetTabStatus(blocked, programstatus.Report{
			State: programstatus.Blocked, Progress: -1, Msg: "needs you",
		}))
		focused := m.focusEx()
		require.NoError(t, m.commandProgramStatus())
		require.Equal(t, 1, focused.comp.Browser().FloatingWindows())
		win, err := focused.comp.Focus()
		require.NoError(t, err)
		content, err := win.Content()
		require.NoError(t, err)
		picker, ok := content.(*idestatus.View)
		require.True(t, ok)
		require.Len(t, m.status.Items(), 1)
		require.True(t, picker.Select(m.workspaces[slot].uri, blocked))

		oldFocus := m.focus
		require.NoError(t, m.moveWorkspace(strconv.Itoa(slot+1)))
		slot = oldFocus
		exit, handled := picker.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
		assert.True(t, exit)
		assert.True(t, handled)
	}()
	m.quiesce()

	m.mu.Lock()
	defer m.mu.Unlock()
	assert.Equal(t, slot, m.focus, "the entry's workspace is focused")
	tab, ok := other.comp.FocusTab()
	require.True(t, ok)
	assert.True(t, tab.URI().Equal(blocked), "the entry's tab is shown")
	assert.NotEqual(t, -1, slices.IndexFunc(other.comp.Browser().Tabs(),
		func(t *browser.Tab) bool { return t.URI().Equal(blocked) }))

	assert.Error(t, m.commandProgramStatus("extra"))
}

func TestCommandProgramStatusDrawsEveryProgramOverItsWorkspace(t *testing.T) {
	m, slot := newActivityTestManager(t, defaultConfigWithWrap(false))
	t.Cleanup(func() { _ = m.Close() })
	m.mu.Lock()
	defer m.mu.Unlock()
	focused := m.focusEx()
	here := m.workspaces[m.focus]
	require.Same(t, focused, here.ex, "the inbox floats over a workspace that reports too")
	there := m.workspaces[slot]
	focused.Resize(120, 40)
	there.ex.Resize(120, 40)
	report := func(w *workspaceHandler, uri workspaceapi.URI, app string) {
		m.status.SetTabStatus(w.uri, &w.ex.comp, uri, programstatus.Report{
			State: programstatus.Working, Progress: -1, App: app,
		})
		_, ok := m.status.Status(w.uri, uri)
		require.True(t, ok, app)
	}
	tab := func(w *workspaceHandler, raw, app string) {
		uri := mustURI(t, raw)
		_, err := w.ex.comp.Tab(uri, 'x', app, browsertest.NewTestHandler())
		require.NoError(t, err)
		require.NoError(t, w.ex.comp.SetTabStatus(uri, programstatus.Report{
			State: programstatus.Working, Progress: -1, App: app,
		}))
	}
	tab(here, "rune-agent://model/here", "tab-here")
	tab(there, "rune-agent://model/there", "tab-there")
	b := focused.comp.Browser()
	for i, w := range []*workspaceHandler{here, there} {
		pty := mustURI(t, "file:///dev/ttys07"+strconv.Itoa(i))
		tv := newTestVteWithConfig([]string{"sh"})
		tv.uri = pty
		win, ok := w.ex.comp.Browser().Split(browserapi.OrientationDefault,
			w.ex.comp.Browser().Focus(), tv)
		require.True(t, ok)
		w.ex.comp.Browser().SetFocus(win)
		report(w, pty, "terminal-"+strconv.Itoa(i))
	}
	cmd := mustURI(t, "file:///dev/ttys080")
	ph := &pluginAdapter{pluginHandler: &statusTestPluginHandler{testVte: newTestVteWithConfig([]string{"make"}), uri: cmd}}
	ph.win = b.Floating(ph, browserapi.FloatingConfig{Alignment: component.AlignmentCentered})
	report(here, cmd, "command")
	prompt := mustURI(t, "ideauthorizer:///prompt/1")
	focused.setStatusWindow(prompt, focused.comp.Prompt("Allow make?", []string{"Yes", "No"}, nil,
		handler.FuncPromptHandler(func(int, string) {}, func() error { return nil })))
	report(here, prompt, "prompt")
	shell := mustURI(t, "terminal://loading/9100")
	companion := newTestVteWithConfig(nil)
	companion.uri = shell
	focused.companionTerminal = companion
	report(here, shell, "companion")

	require.NoError(t, m.commandProgramStatus())
	win, err := focused.comp.Focus()
	require.NoError(t, err)
	content, err := win.Content()
	require.NoError(t, err)
	inbox, ok := content.(*idestatus.View)
	require.True(t, ok)
	items := m.status.Items()
	require.Len(t, items, 7)
	for _, it := range items {
		require.True(t, inbox.Select(it.WS, it.URI))
		name, ok := inbox.Selection()
		require.True(t, ok)
		require.Equal(t, it.App, name, "every program is listed by its app")
		w := term.NewStringWriter(120, 40)
		require.NotPanics(t, func() { focused.Draw(w) }, name)
		require.NoError(t, w.Flush())
		assert.Contains(t, w.String(), name, "the inbox is drawn over its workspace")
		if name == "command" || name == "prompt" || name == "companion" {
			// Shown in the very workspace the inbox floats over.
			_, ok := statusWorkspaces{m.workspaceManagerHandler}.Preview(it.WS, it.URI)
			assert.True(t, ok, "%s is previewed", name)
		}
	}
}

func TestCommandProgramStatusPreviewsTab(t *testing.T) {
	m, slot := newActivityTestManager(t, defaultConfigWithWrap(false))
	t.Cleanup(func() { _ = m.Close() })
	uri, err := workspaceapi.ParseURI("rune-agent://model/agent")
	require.NoError(t, err)
	m.mu.Lock()
	defer m.mu.Unlock()

	other := m.workspaces[slot].ex
	agent := browsertest.NewTestHandler()
	_, err = other.comp.Tab(uri, 'x', "agent", agent)
	require.NoError(t, err)
	b := other.comp.Browser()
	tab, ok := b.Tab(uri)
	require.True(t, ok)
	require.True(t, b.SetContentToTab(b.Focus(), slices.Index(b.Tabs(), tab)))
	// As if the workspace had been in focus once.
	other.Resize(60, 30)
	tabW, tabH := tab.ContentSize()
	require.NotZero(t, tabW)
	require.NotZero(t, tabH)
	require.NoError(t, other.comp.SetTabStatus(uri, programstatus.Report{
		State: programstatus.Working, Progress: -1,
	}))
	focused := m.focusEx()
	focused.comp.Resize(120, 60)
	require.NoError(t, m.commandProgramStatus())
	win, err := focused.comp.Focus()
	require.NoError(t, err)
	content, err := win.Content()
	require.NoError(t, err)
	picker, ok := content.(*idestatus.View)
	require.True(t, ok)
	ws := m.workspaces[slot].uri
	size := func() (int, int) { return focused.comp.Browser().ContentSize(win) }

	// With room for it, the window is sized to show the tab whole beside
	// the sidebar.
	focused.comp.Resize(120, 60)
	for _, ev := range []term.Event{
		{Type: term.EventKey, Ch: 'j'},
		{Type: term.EventKey, Ch: 'k'},
	} {
		_, handled := picker.Handle(ev)
		assert.True(t, handled)
	}
	width, height := size()
	drawn := handlertest.DrawHandler(picker, width, height)
	sideW := statusInboxSidebarWidth(t, drawn)
	_, wantH := picker.Dimensions()
	assert.Equal(t, []int{sideW + tabW, wantH}, []int{width, height})
	assert.GreaterOrEqual(t, height, tabH)
	lines := strings.Split(drawn, "\n")
	for y := range tabH {
		assert.Equal(t, strings.Repeat("A", tabW), string([]rune(lines[y])[sideW:]),
			"the tab of another workspace is previewed")
	}
	assert.Equal(t, 'A', agent.Ch, "the previewed tab receives no input")

	// The window keeps its top as what it previews changes size.
	top := win.Position().Y
	require.Zero(t, top, "the window opens at the top")
	gone, err := workspaceapi.ParseURI("file:///dev/ttys043")
	require.NoError(t, err)
	require.NoError(t, m.status.SetTabStatus(ws, &other.comp, gone, programstatus.Report{
		State: programstatus.Done, Progress: -1,
	}))
	require.True(t, picker.Select(ws, gone))
	focused.comp.Draw(term.NewStringWriter(120, 60))
	width, _ = size()
	assert.NotEqual(t, sideW+tabW, width, "the window follows the preview")
	wmW, _ := focused.comp.Browser().WindowManagerSize()
	assert.GreaterOrEqual(t, width, wmW*statusMinWidthPercent/100,
		"the window takes at least its share of the width")
	assert.Equal(t, top, win.Position().Y, "the window keeps its top")
	require.True(t, picker.Select(ws, uri))
	focused.comp.Draw(term.NewStringWriter(120, 60))
	width, _ = size()
	assert.Equal(t, sideW+tabW, width)
	assert.Equal(t, top, win.Position().Y, "the window keeps its top")

	// Without, the window takes what there is and the preview less
	// than the tab.
	focused.comp.Resize(40, 20)
	width, height = size()
	assert.Less(t, width, sideW+tabW)
	assert.Less(t, height, tabH)
	w, h := tab.ContentSize()
	assert.Equal(t, []int{tabW, tabH}, []int{w, h}, "the previewed tab keeps its size")
}

// statusInboxSidebarWidth returns the columns the sidebar of the
// status inbox drawn takes, its divider included.
func statusInboxSidebarWidth(t *testing.T, drawn string) int {
	t.Helper()
	first, _, _ := strings.Cut(drawn, "\n")
	i := slices.Index([]rune(first), idestatus.DefaultInboxStyle().Frame.VerticalLeft)
	require.GreaterOrEqual(t, i, 0, "the sidebar ends with a divider")
	return i + 1
}

func TestCommandProgramStatusPreviewsWindowedTerminal(t *testing.T) {
	m, slot := newActivityTestManager(t, defaultConfigWithWrap(false))
	t.Cleanup(func() { _ = m.Close() })
	pty, err := workspaceapi.ParseURI("file:///dev/ttys042")
	require.NoError(t, err)
	m.mu.Lock()
	defer m.mu.Unlock()

	// As :terminal opens it: the content of a window, in no tab.
	w := m.workspaces[slot]
	b := w.ex.comp.Browser()
	tv := newTestVteWithConfig([]string{"brew"})
	tv.uri = pty
	termWin := b.Focus()
	require.NoError(t, termWin.SetContent(tv))
	w.ex.Resize(60, 30)
	m.status.SetTabStatus(w.uri, &w.ex.comp, pty, programstatus.Report{
		State: programstatus.Done, Progress: -1, App: "brew",
	})
	_, ok := m.status.Status(w.uri, pty)
	require.True(t, ok)

	preview, ok := statusWorkspaces{m.workspaceManagerHandler}.Preview(w.uri, pty)
	require.True(t, ok, "a terminal outside of a tab is previewed in its window")
	assert.Same(t, tv, preview.Content)
	assert.Nil(t, preview.Fit, "a terminal shown in a window is never resized")
	tvW, tvH := b.ContentSize(termWin)
	require.NotZero(t, tvW)
	assert.Equal(t, []int{tvW, tvH}, []int{preview.Width, preview.Height})

	focused := m.focusEx()
	require.NoError(t, m.commandProgramStatus())
	inbox, err := focused.comp.Focus()
	require.NoError(t, err)
	content, err := inbox.Content()
	require.NoError(t, err)
	picker := content.(*idestatus.View)
	focused.comp.Resize(120, 60)
	width, height := focused.comp.Browser().ContentSize(inbox)
	drawn := handlertest.DrawHandler(picker, width, height)
	lines := strings.Split(drawn, "\n")
	assert.Equal(t, "brew", strings.TrimSpace(string([]rune(lines[0])[statusInboxSidebarWidth(t, drawn):])))

	// Choosing it focuses its window.
	split, ok := b.Split(browserapi.OrientationDefault, termWin, browsertest.NewTestHandler())
	require.True(t, ok)
	b.SetFocus(split)
	require.NoError(t, m.focusStatusEntry(w.uri, pty))
	assert.Equal(t, termWin.WindowID(), b.Focus().WindowID())
}

func TestCommandProgramStatusPreviewsPermissionPrompt(t *testing.T) {
	m, slot := newActivityTestManager(t, defaultConfigWithWrap(false))
	t.Cleanup(func() { _ = m.Close() })
	uri, err := workspaceapi.ParseURI("ideauthorizer:///prompt/1")
	require.NoError(t, err)
	m.mu.Lock()
	defer m.mu.Unlock()

	w := m.workspaces[slot]
	b := w.ex.comp.Browser()
	tiled := b.Focus()
	prompt := w.ex.comp.Prompt("Allow make?", []string{"Yes", "No"}, nil,
		handler.FuncPromptHandler(func(int, string) {}, func() error { return nil }))
	w.ex.Resize(60, 30)
	m.status.SetTabStatus(w.uri, &w.ex.comp, uri, programstatus.Report{
		State: programstatus.Blocked, Kind: programstatus.KindPermission, Progress: -1,
		App: programstatus.HostApp,
	})
	_, ok := statusWorkspaces{m.workspaceManagerHandler}.Preview(w.uri, uri)
	require.False(t, ok, "nothing tells which window shows the status yet")

	w.ex.setStatusWindow(uri, prompt)
	preview, ok := statusWorkspaces{m.workspaceManagerHandler}.Preview(w.uri, uri)
	require.True(t, ok, "the permission prompt is previewed")
	content, err := prompt.Content()
	require.NoError(t, err)
	assert.Same(t, content, preview.Content)
	assert.Positive(t, preview.Width)
	assert.Positive(t, preview.Height)

	b.SetFocus(tiled)
	require.NoError(t, m.focusStatusEntry(w.uri, uri))
	assert.Equal(t, prompt.WindowID(), b.Focus().WindowID(), "choosing it focuses the prompt")

	require.NoError(t, prompt.Close())
	_, ok = statusWorkspaces{m.workspaceManagerHandler}.Preview(w.uri, uri)
	assert.False(t, ok, "a closed prompt has no preview")
}

func TestCommandProgramStatusPreviewsDetachedPrograms(t *testing.T) {
	m, slot := newActivityTestManager(t, defaultConfigWithWrap(false))
	t.Cleanup(func() { _ = m.Close() })
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.workspaces[slot]
	e := w.ex
	b := e.comp.Browser()
	w.ex.Resize(60, 30)
	tiled := b.Focus()
	report := func(uri workspaceapi.URI) {
		m.status.SetTabStatus(w.uri, &e.comp, uri, programstatus.Report{
			State: programstatus.Working, Progress: -1,
		})
		_, ok := m.status.Status(w.uri, uri)
		require.True(t, ok)
	}

	// A floating "!" command, whose window shows the program behind
	// layers that report under the uri of its terminal.
	cmd := mustURI(t, "file:///dev/ttys050")
	program := &statusTestPluginHandler{testVte: newTestVteWithConfig([]string{"make"}), uri: cmd}
	assert.Equal(t, cmd, (&asyncPlugin{real: program}).StatusURI())
	ph := &pluginAdapter{pluginHandler: program}
	win, err := e.comp.Floating(ph, browserapi.FloatingConfig{Alignment: component.AlignmentCentered})
	require.NoError(t, err)
	ph.win = win
	report(cmd)
	preview, ok := statusWorkspaces{m.workspaceManagerHandler}.Preview(w.uri, cmd)
	require.True(t, ok, "a floating command is previewed in its window")
	assert.Same(t, ph, preview.Content)
	assert.False(t, preview.Hidden)
	b.SetFocus(tiled)
	require.NoError(t, m.focusStatusEntry(w.uri, cmd))
	assert.Equal(t, win.WindowID(), b.Focus().WindowID(), "choosing it focuses its window")
	require.NoError(t, win.Close())

	// The companion terminal, hidden while its shell runs on.
	shell := mustURI(t, "terminal://loading/9000")
	tv := newTestVteWithConfig(nil)
	tv.uri = shell
	e.companionTerminal = tv
	report(shell)
	preview, ok = statusWorkspaces{m.workspaceManagerHandler}.Preview(w.uri, shell)
	require.True(t, ok, "a hidden companion terminal is previewed")
	assert.Same(t, tv, preview.Content)
	assert.True(t, preview.Hidden, "the inbox sizes what is shown nowhere")
	assert.NotNil(t, preview.Fit)
	b.SetFocus(tiled)
	require.NoError(t, m.focusStatusEntry(w.uri, shell))
	require.NotNil(t, e.companionTerminalWin, "choosing it shows it")
	assert.Equal(t, e.companionTerminalWin.WindowID(), b.Focus().WindowID())
	preview, ok = statusWorkspaces{m.workspaceManagerHandler}.Preview(w.uri, shell)
	require.True(t, ok)
	assert.False(t, preview.Hidden, "shown, it is previewed in its window")
}

type statusTestPluginHandler struct {
	*testVte
	uri workspaceapi.URI
}

func (h *statusTestPluginHandler) StatusURI() workspaceapi.URI { return h.uri }

func TestCommandProgramStatusClosesPrograms(t *testing.T) {
	type program struct {
		m   *testWorkspaceManagerHandler
		e   *ex
		uri workspaceapi.URI
		p   *statusTestProgram
		win browser.Window
	}
	del := term.Event{Type: term.EventKey, Key: term.KeyDelete}
	backspace := term.Event{Type: term.EventKey, Key: term.KeyBackspace}
	ctx := context.Background()
	terminalTab := func(t *testing.T, pr *program) {
		require.NoError(t, pr.e.terminalnewtab(ctx))
	}
	// Shown in no window, its status is not seen when reported.
	terminalTabOutOfSight := func(t *testing.T, pr *program) {
		terminalTab(t, pr)
		other, err := pr.e.comp.Tab(mustURI(t, "rune-agent://model/other"), 'x', "other",
			browsertest.NewTestHandler())
		require.NoError(t, err)
		require.NoError(t, pr.e.invokeWindow().SetContent(other))
		tab, ok := pr.e.comp.Browser().Tab(pr.uri)
		require.True(t, ok)
		_, shown := tab.Window()
		require.False(t, shown)
	}
	task := func(t *testing.T, pr *program, tab bool) {
		if tab {
			require.NoError(t, pr.e.newTaskTab(ctx, "build", "--", "make"))
		} else {
			require.NoError(t, pr.e.newTask(ctx, "build", "left", "--", "make"))
		}
	}
	taskStopped := func(t *testing.T, pr *program) {
		assert.True(t, pr.p.calledClose, "the program is closed")
		assert.Empty(t, pr.e.tasks.ListTasks(), "the task is stopped")
	}
	tabGone := func(t *testing.T, pr *program) {
		assert.True(t, pr.p.calledClose, "the program is closed")
		_, ok := pr.e.comp.Browser().Tab(pr.uri)
		assert.False(t, ok, "its tab is gone")
	}
	for _, tc := range []struct {
		name string
		// here runs the program in the workspace the inbox floats over.
		here  bool
		state programstatus.State
		key   term.Event
		open  func(t *testing.T, pr *program)
		// answer is the key answering the question asked before closing,
		// if any.
		answer rune
		// stays is set for what is hidden rather than closed: its row
		// stays.
		stays  bool
		closed func(t *testing.T, pr *program)
	}{{
		name: "terminal tab", state: programstatus.Error, key: del,
		open: terminalTab, closed: tabGone,
	}, {
		// The idle report on the tab in sight is seen right away, yet the
		// row stays until the tab is closed.
		name: "terminal tab here", here: true, state: programstatus.Idle, key: backspace,
		open: terminalTab, closed: tabGone,
	}, {
		name: "terminal tab out of sight", state: programstatus.Idle, key: del,
		open: terminalTabOutOfSight, closed: tabGone,
	}, {
		name: "working terminal tab", state: programstatus.Working, key: del,
		open: terminalTab, answer: 'y', closed: tabGone,
	}, {
		name: "working terminal tab kept", state: programstatus.Working, key: del,
		open: terminalTab, answer: 'n', stays: true,
		closed: func(t *testing.T, pr *program) {
			assert.False(t, pr.p.calledClose, "the program runs on")
			_, ok := pr.e.comp.Browser().Tab(pr.uri)
			assert.True(t, ok, "its tab stays")
		},
	}, {
		name: "terminal in a window", state: programstatus.Error, key: backspace,
		open: func(t *testing.T, pr *program) {
			require.NoError(t, pr.e.terminalneworsplit(ctx))
			win, ok := statusWindowOf(pr.e, pr.uri)
			require.True(t, ok)
			pr.win = win
		},
		closed: func(t *testing.T, pr *program) {
			assert.True(t, pr.p.calledClose, "the program is closed")
			_, ok := statusWindowOf(pr.e, pr.uri)
			assert.False(t, ok, "no window shows it")
		},
	}, {
		name: "blocked terminal in a split here", here: true, state: programstatus.Blocked, key: del,
		open: func(t *testing.T, pr *program) {
			other, err := pr.e.comp.Tab(mustURI(t, "rune-agent://model/other"), 'x', "other",
				browsertest.NewTestHandler())
			require.NoError(t, err)
			require.NoError(t, pr.e.invokeWindow().SetContent(other))
			require.NoError(t, pr.e.terminalneworsplit(ctx))
			_, shown := other.(*browser.Tab).Window()
			require.True(t, shown, "the terminal is split off")
		},
		answer: 'y',
		closed: func(t *testing.T, pr *program) {
			assert.True(t, pr.p.calledClose, "the program is closed")
			_, ok := statusWindowOf(pr.e, pr.uri)
			assert.False(t, ok, "no window shows it")
		},
	}, {
		name: "floating command", here: true, state: programstatus.Done, key: del,
		open: func(t *testing.T, pr *program) {
			tiled := pr.e.comp.Browser().Focus()
			require.NoError(t, pr.e.executePlugin(ctx, "make"))
			// Reported on its window in focus, it would be seen at once.
			pr.e.comp.Browser().SetFocus(tiled)
		},
		closed: func(t *testing.T, pr *program) {
			assert.True(t, pr.p.calledClose, "the program is closed")
			_, ok := statusWindowOf(pr.e, pr.uri)
			assert.False(t, ok, "its window is closed")
		},
	}, {
		name: "working floating command", state: programstatus.Working, key: backspace,
		open: func(t *testing.T, pr *program) {
			require.NoError(t, pr.e.executePlugin(ctx, "make"))
		},
		answer: 'y',
		closed: func(t *testing.T, pr *program) {
			assert.True(t, pr.p.calledClose, "the program is closed")
			_, ok := statusWindowOf(pr.e, pr.uri)
			assert.False(t, ok, "its window is closed")
		},
	}, {
		name: "task", state: programstatus.Error, key: del,
		open:   func(t *testing.T, pr *program) { task(t, pr, false) },
		closed: taskStopped,
	}, {
		name: "working task in a tab", state: programstatus.Working, key: del,
		open:   func(t *testing.T, pr *program) { task(t, pr, true) },
		answer: 'y', closed: taskStopped,
	}, {
		name: "permission prompt", state: programstatus.Blocked, key: del,
		open: func(t *testing.T, pr *program) {
			// As ideauthorizer prompts: denied when closed, and its status
			// withdrawn.
			pr.win = pr.e.comp.Prompt("Allow make?", []string{"Yes", "No"}, nil,
				handler.FuncPromptHandler(func(int, string) {}, func() error {
					return pr.e.tm.SetTabStatus(pr.uri, programstatus.Report{State: programstatus.Clear})
				}))
			pr.e.setStatusWindow(pr.uri, pr.win)
		},
		answer: 'y',
		closed: func(t *testing.T, pr *program) {
			assert.True(t, pr.win.Closed(), "the prompt is closed")
		},
	}, {
		name: "shown companion terminal", here: true, state: programstatus.Working, key: del,
		open: func(t *testing.T, pr *program) {
			require.NoError(t, pr.e.toggleCompanionTerminal())
		},
		answer: 'y', stays: true,
		closed: func(t *testing.T, pr *program) {
			assert.False(t, pr.p.calledClose, "its shell runs on")
			assert.Nil(t, pr.e.companionTerminalWin, "it is hidden")
			_, ok := hiddenCompanionTerminal(pr.e, pr.uri)
			assert.True(t, ok)
		},
	}, {
		name: "hidden companion terminal", state: programstatus.Idle, key: backspace,
		open: func(t *testing.T, pr *program) {
			require.NoError(t, pr.e.toggleCompanionTerminal())
			require.NoError(t, pr.e.toggleCompanionTerminal())
		},
		stays: true,
		closed: func(t *testing.T, pr *program) {
			assert.False(t, pr.p.calledClose, "its shell runs on")
			assert.Nil(t, pr.e.companionTerminalWin, "it stays hidden")
		},
	}, {
		name: "file with changes", state: programstatus.Error, key: del,
		open: func(t *testing.T, pr *program) {
			var err error
			pr.uri, err = pr.e.workspace.URI("dirty.go")
			require.NoError(t, err)
			_, err = pr.e.editFileURI(pr.uri, pr.e.invokeWindow(), false)
			require.NoError(t, err)
			editBuffer(t, pr.e, pr.uri, "ABC")
		},
		// Asked whether to discard the changes.
		answer: 'y',
		closed: func(t *testing.T, pr *program) {
			_, ok := pr.e.comp.Browser().Tab(pr.uri)
			assert.False(t, ok, "its tab is gone")
		},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			m, slot := newActivityTestManager(t, defaultConfigWithWrap(false))
			t.Cleanup(func() { _ = m.Close() })
			pr := program{m: m}
			var focused *ex
			ws := m.workspaces[slot].uri
			m.locked(func() {
				focused = m.focusEx()
				pr.e = m.workspaces[slot].ex
				if tc.here {
					pr.e = focused
					ws = m.workspaces[m.focus].uri
				}
				require.NotSame(t, focused, m.workspaces[slot].ex)
				pr.e.Resize(80, 30)
				focused.Resize(120, 40)
				pr.uri = mustURI(t, "file:///dev/ttys060")
				pr.p = newStatusTestProgram(pr.e.tm, pr.uri, "make")
				pr.e.newEmulatorHandler = func([]string) (vtereservoir.VTE, error) { return pr.p, nil }
				pr.e.newPluginHandler = func(int, ...string) (pluginHandler, error) { return pr.p, nil }
				pr.e.tasks.SetProgramBuilder(func(
					browser.EventPublisher, browser.Notifications, schemeapi.Executor,
					schemeapi.Terminal, browser.TabManager, []string, int, ...plugin.Option,
				) (browser.ScrollableFloating, error) {
					return pr.p, nil
				})
				tc.open(t, &pr)
			})
			m.quiesce()
			require.Eventually(t, func() bool {
				ok := true
				m.locked(func() {
					if len(pr.e.tasks.ListTasks()) > 0 {
						_, ok = pr.e.tasks.StatusTask(pr.uri)
					}
				})
				return ok
			}, 5*time.Second, time.Millisecond, "a task runs its program")
			m.locked(func() {
				require.NoError(t, pr.e.tm.SetTabStatus(pr.uri, programstatus.Report{
					State: tc.state, Progress: -1, App: "make", Msg: tc.name,
				}))
			})
			m.quiesce()

			var inbox *idestatus.View
			var inboxWin browser.Window
			m.locked(func() {
				require.NoError(t, m.commandProgramStatus())
				var err error
				inboxWin, err = focused.comp.Focus()
				require.NoError(t, err)
				content, err := inboxWin.Content()
				require.NoError(t, err)
				var ok bool
				inbox, ok = content.(*idestatus.View)
				require.True(t, ok)
				require.True(t, inbox.Select(ws, pr.uri), "the program is listed")
				m.Handle(tc.key)
			})
			m.quiesce()
			if tc.answer != 0 {
				m.locked(func() {
					win, err := focused.comp.Focus()
					require.NoError(t, err)
					require.NotEqual(t, inboxWin.WindowID(), win.WindowID(), "a question is asked")
					assert.False(t, pr.p.calledClose, "nothing is closed before the answer")
					m.Handle(term.Event{Type: term.EventKey, Ch: tc.answer})
				})
				m.quiesce()
			}

			m.locked(func() {
				tc.closed(t, &pr)
				_, listed := m.status.Status(ws, pr.uri)
				listed = listed || inbox.Select(ws, pr.uri)
				assert.Equal(t, tc.stays, listed, "the row goes with the program")
				assert.False(t, inboxWin.Closed(), "the inbox stays open")
				win, err := focused.comp.Focus()
				require.NoError(t, err)
				assert.Equal(t, inboxWin.WindowID(), win.WindowID(), "the inbox keeps the focus")
			})
		})
	}
}

func TestWorkspaceBarStatusSeenFromTerminalWithoutTab(t *testing.T) {
	type program struct {
		e   *ex
		uri workspaceapi.URI
		p   *statusTestProgram
	}
	ctx := context.Background()
	focusWindow := func(t *testing.T, pr *program) {
		win, ok := statusWindowOf(pr.e, pr.uri)
		require.True(t, ok, "a window shows the program")
		pr.e.comp.Browser().SetFocus(win)
	}
	focusTask := func(t *testing.T, pr *program) {
		require.True(t, pr.e.tasks.FocusTask("build"))
	}
	for _, tc := range []struct {
		name  string
		open  func(t *testing.T, pr *program)
		focus func(t *testing.T, pr *program)
	}{{
		name: "floating command",
		open: func(t *testing.T, pr *program) {
			require.NoError(t, pr.e.executePlugin(ctx, "make"))
		},
		focus: focusWindow,
	}, {
		name: "companion terminal",
		open: func(t *testing.T, pr *program) {
			require.NoError(t, pr.e.toggleCompanionTerminal())
		},
		focus: focusWindow,
	}, {
		name: "terminal in a split",
		open: func(t *testing.T, pr *program) {
			require.NoError(t, pr.e.terminalneworsplit(ctx))
		},
		focus: focusWindow,
	}, {
		name: "floating task",
		open: func(t *testing.T, pr *program) {
			require.NoError(t, pr.e.newTask(ctx, "build", "left", "--", "make"))
		},
		focus: focusTask,
	}, {
		name: "task in a tab",
		open: func(t *testing.T, pr *program) {
			require.NoError(t, pr.e.newTaskTab(ctx, "build", "--", "make"))
		},
		focus: focusTask,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			m, otherSlot := newActivityTestManager(t, defaultConfigWithWrap(false))
			t.Cleanup(func() { _ = m.Close() })
			style := browser.DefaultTabStatusConfig().WorkspaceTab
			pr := program{uri: mustURI(t, "file:///dev/ttys061")}
			var (
				slot  int
				ws    workspaceapi.URI
				notes *recordingNotifications
				away  browser.Window
			)
			m.locked(func() {
				slot = m.focus
				w := m.workspaces[slot]
				pr.e, ws = w.ex, w.uri
				notes = &recordingNotifications{inner: w.notifications}
				w.notifications = notes
				pr.e.Resize(80, 30)
				pr.p = newStatusTestProgram(pr.e.tm, pr.uri, "make")
				pr.e.newEmulatorHandler = func([]string) (vtereservoir.VTE, error) { return pr.p, nil }
				pr.e.newPluginHandler = func(int, ...string) (pluginHandler, error) { return pr.p, nil }
				pr.e.tasks.SetProgramBuilder(func(
					browser.EventPublisher, browser.Notifications, schemeapi.Executor,
					schemeapi.Terminal, browser.TabManager, []string, int, ...plugin.Option,
				) (browser.ScrollableFloating, error) {
					return pr.p, nil
				})
				b := pr.e.comp.Browser()
				tiled := b.Focus()
				other, err := pr.e.comp.Tab(mustURI(t, "rune-agent://model/other"), 'x', "other",
					browsertest.NewTestHandler())
				require.NoError(t, err)
				require.NoError(t, tiled.SetContent(other))
				tc.open(t, &pr)
				var ok bool
				away, ok = b.Split(browserapi.OrientationRight, tiled, browsertest.NewTestHandler())
				require.True(t, ok)
				b.SetFocus(away)
			})
			m.quiesce()
			require.Eventually(t, func() bool {
				ok := true
				m.locked(func() {
					if len(pr.e.tasks.ListTasks()) > 0 {
						_, ok = pr.e.tasks.StatusTask(pr.uri)
					}
				})
				return ok
			}, 5*time.Second, time.Millisecond, "a task runs its program")

			report := func(msg string) {
				t.Helper()
				m.locked(func() {
					require.NoError(t, pr.e.tm.SetTabStatus(pr.uri, programstatus.Report{
						State: programstatus.Error, Progress: -1, App: "make", Msg: msg,
					}))
				})
				m.quiesce()
			}
			assertSeen := func(seen bool, msgAndArgs ...any) {
				t.Helper()
				m.locked(func() {
					sum, _ := m.workspaceStatus(slot)
					_, listed := m.status.Status(ws, pr.uri)
					barIdx := slices.Index(m.barIdxToSlot, slot)
					require.GreaterOrEqual(t, barIdx, 0)
					if seen {
						assert.Equal(t, programstatus.Idle, sum.State, msgAndArgs...)
						assert.False(t, listed, msgAndArgs...)
						assert.Equal(t, term.Attributes{}, m.bar.TabAttr(barIdx), msgAndArgs...)
					} else {
						assert.Equal(t, programstatus.Error, sum.State, msgAndArgs...)
						assert.True(t, listed, msgAndArgs...)
						assert.Equal(t, style.ErrorAttr, m.bar.TabAttr(barIdx), msgAndArgs...)
					}
				})
			}

			report("failed out of sight")
			assertSeen(false, "reported while another window has focus")

			m.locked(func() { tc.focus(t, &pr) })
			m.quiesce()
			assertSeen(true, "focusing its window sees it")

			report("failed in sight")
			assertSeen(true, "reported while its window has focus")
			assert.Contains(t, notes.snapshot(),
				capturedNote{level: browserapi.LevelError, msg: "make: failed in sight"},
				"its message is notified all the same")

			m.locked(func() { require.True(t, m.switchToWorkspace(otherSlot)) })
			report("failed in another workspace")
			assertSeen(false, "reported while its workspace is out of focus")
			m.locked(func() { require.True(t, m.switchToWorkspace(slot)) })
			m.quiesce()
			assertSeen(true, "switching to its workspace sees it")

			m.locked(func() { m.Handle(term.Event{Type: term.EventUnfocus}) })
			report("failed out of focus")
			assertSeen(false, "reported while Rune is out of focus")
			m.locked(func() {
				require.True(t, m.switchToWorkspace(otherSlot))
				require.True(t, m.switchToWorkspace(slot))
			})
			m.quiesce()
			report("failed out of focus again")
			assertSeen(false, "switching workspaces leaves Rune out of focus")
			m.locked(func() { m.Handle(term.Event{Type: term.EventFocus}) })
			m.quiesce()
			assertSeen(true, "focus coming back sees it")
		})
	}
}

// statusTestProgram is a program that, as a terminal does, reports its
// statuses through tm under the uri of its own, and withdraws them when
// closed.
type statusTestProgram struct {
	*testVte
	tm browser.TabManager
}

func newStatusTestProgram(tm browser.TabManager, uri workspaceapi.URI, cmd string) *statusTestProgram {
	tv := newTestVteWithConfig([]string{cmd})
	tv.uri = uri
	return &statusTestProgram{testVte: tv, tm: tm}
}

func (p *statusTestProgram) StatusURI() workspaceapi.URI { return p.uri }

func (p *statusTestProgram) Close() error {
	err := p.testVte.Close()
	_ = p.tm.SetTabStatus(p.uri, programstatus.Report{State: programstatus.Clear})
	return err
}

func TestWorkspaceBarActiveTabAnimationConfig(t *testing.T) {
	for _, tc := range []struct {
		name       string
		animations map[string]any
		running    bool
	}{{
		name: "workspace disabled",
		animations: map[string]any{
			"active_workspace_tab": map[string]any{"enabled": false},
		},
		running: false,
	}, {
		name: "content disabled",
		animations: map[string]any{
			"active_content_tab": false,
		},
		running: true,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultConfigWithWrap(false)
			cfg.cfg["animations"] = tc.animations
			m, slot := newActivityTestManager(t, cfg)
			chat, err := workspaceapi.ParseURI("rune-agent://model/rolling-fox")
			require.NoError(t, err)

			m.mu.Lock()
			comp := &m.workspaces[slot].ex.comp
			_, err = comp.Tab(chat, 'x', "rolling-fox", browsertest.NewTestHandler())
			require.NoError(t, err)
			require.NoError(t, comp.SetTabStatus(chat, workingStatus()))
			assert.True(t, comp.HasWorkingTabs())
			assert.Equal(t, tc.running, m.shadedBar.Running())
			m.mu.Unlock()

			require.NoError(t, m.Close())
		})
	}
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

// caseRuleWorkspace fixes the case rule of a workspace so tests do not
// depend on the host filesystem.
type caseRuleWorkspace struct {
	workspace.Workspace
	caseSensitive bool
}

func (w caseRuleWorkspace) PathCaseSensitive() bool { return w.caseSensitive }

func TestOpenFileTargetMatchesTabUnderOtherCase(t *testing.T) {
	for _, tc := range []struct {
		name          string
		caseSensitive bool
		wantTab       bool
	}{
		{"case-sensitive host", true, false},
		{"case-insensitive host", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp1 := t.TempDir()
			tmp2 := t.TempDir()
			sharedDir := t.TempDir()
			sharedPath := filepath.Join(sharedDir, "Shared.txt")
			require.NoError(t, os.WriteFile(sharedPath, []byte("shared"), 0o666))

			cfg := defaultConfigWithWrap(false)
			mu, sched, drain := buildTestSchedulerForCfg(t, &cfg)
			manager := workspace.NewManagerWithWorkspaceFunc(config.NopConfig(), sched,
				func(
					uri workspaceapi.URI, scheme schemeapi.Scheme, tick func(func()) bool,
				) workspace.Workspace {
					return caseRuleWorkspace{
						Workspace:     workspace.NewSchemeWorkspace(uri, scheme, tick),
						caseSensitive: tc.caseSensitive,
					}
				})
			require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme,
				workspace.NewMemoryScheme))
			require.NoError(t, manager.RegisterScheme(workspace.FileScheme,
				workspace.NewFileScheme))
			homeURI, err := workspaceapi.ParseURI("memory://" + tmp1)
			require.NoError(t, err)
			m := newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
				&homeURI, cfg, FuncExtensionsRunner(testRunnerFn), nil, tmp1, nil,
				nopShutdownShaderConfig())
			defer func() {
				require.NoError(t, m.Close())
			}()

			uri2, err := workspaceapi.ParseURI("file://" + tmp2)
			require.NoError(t, err)
			require.NoError(t, m.addOrCreateWorkspace(uri2))
			m.quiesce()

			sharedURI, err := workspaceapi.ParseURI("file://" + sharedPath)
			require.NoError(t, err)
			otherCaseURI, err := workspaceapi.ParseURI(
				"file://" + filepath.Join(sharedDir, "shared.txt"))
			require.NoError(t, err)

			openTab, err := m.workspaces[1].ex.editFileURI(
				sharedURI, m.workspaces[1].ex.invokeWindow(), false)
			require.NoError(t, err)
			m.workspaces[1].ex.Wait()

			// Opening the other case only reaches openFileTarget when the
			// host filesystem folds case and the swap files collide, so
			// the case rule is exercised here rather than through Open.
			target, ok := m.openFileTarget(otherCaseURI)
			if tc.wantTab {
				require.True(t, ok)
				assert.Equal(t, 1, target.workspaceIdx)
				assert.Same(t, openTab, target.tab)
			} else if ok {
				assert.Nil(t, target.tab)
			}
		})
	}
}

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

// Frames of a 42x22 IDE viewing an image file, named after the file the
// tab shows.
const (
	viewImageLoadingPNG = `┌━━━━━━━━━━━━────────────────────────────┐
│o sample.png                            │
├────────────────────────────────────────┤
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                   ⠃                    │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
├────────────────────────────────────────┤
│1 1  2 2                                │
└─────━━━────────────────────────────────┘`
	viewImageLoadingJPG = `┌━━━━━━━━━━━━────────────────────────────┐
│o sample.jpg                            │
├────────────────────────────────────────┤
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                   ⠃                    │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
├────────────────────────────────────────┤
│1 1  2 2                                │
└─────━━━────────────────────────────────┘`
	viewImageLoadingGIF = `┌━━━━━━━━━━━━────────────────────────────┐
│o sample.gif                            │
├────────────────────────────────────────┤
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                   ⠃                    │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
├────────────────────────────────────────┤
│1 1  2 2                                │
└─────━━━────────────────────────────────┘`
	viewImageLoadingWebP = `┌━━━━━━━━━━━━━───────────────────────────┐
│o sample.webp                           │
├────────────────────────────────────────┤
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                   ⠃                    │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
│                                        │
├────────────────────────────────────────┤
│1 1  2 2                                │
└─────━━━────────────────────────────────┘`
	viewImagePNG = `┌━━━━━━━━━━━━────────────────────────────┐
│o sample.png                            │
├────────────────────────────────────────┤
│@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@ │
│@@@@@@@@@#b#@@@@##b##@@@##bW#@@@@@@@@@@ │
│@@@@@@@bbbbbb#@acccccb@#cccc;c@@@@@@@@@ │
│@@@@@@@#cbbbb##cccccc#@c;;;;;#@@@@@@@@@ │
│@@@@@@@@@@@@@8cc#@@@@W;;#@@@@@@@@@@@@@@ │
│@@@@@@@#;ccccc##;;;;;;#@;;;;;#@@@@@@@@@ │
│@@@@@@@ccccccW@c;;;;;;@$;::::;@@@@@@@@@ │
│@@@@@@@@bc;;W@#;;;;;!@#:::::;@@@@@@@@@@ │
│@@@@@@@@@@#W;;;@@@@W:::@@@@@@@@@@@@@@@@ │
│@@@@@@@3;;;;;a@8:::::;@@:::++1@@@@@@@@@ │
│@@@@@@@4;;;::#@$:::::W@@+++++3@@@@@@@@@ │
│@@@@@@@@@W##@@@@@###@@@@@###@@@@@@@@@@@ │
│@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@ │
│####:##a###9@c#+@+?W#=9#@@2#*####=@##@@ │
│@###:++#:+#9@;#@#+#++3$==9*=:#**=@5@@@@ │
│@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@ │
├────────────────────────────────────────┤
│1 1  2 2                                │
└─────━━━────────────────────────────────┘`
	viewImageJPG = `┌━━━━━━━━━━━━────────────────────────────┐
│o sample.jpg                            │
├────────────────────────────────────────┤
│@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@ │
│@@@@@@@###aW######b#######bW###@@@@@@@@ │
│@@@@@@#cbbbbb##ccccccc##cccc;;#@@@@@@@@ │
│@@@@@@##bbbcb##cccccc##c;;;;;##@@@@@@@@ │
│@@@@@@@@@@@##9ccW####W+:##@#@@@@@@@@@@@ │
│@@@@@@##:bcccc##;c;;;c##:+;;;##@@@@@@@@ │
│@@@@@@#;cccccW#c;;;;;c#W;::::c#@@@@@@@@ │
│@@@@@@##b;;;###;;;;;!@#:::::;##@@@@@@@@ │
│@@@@@@@@@@@$c:;##@#W+::####@@@@@@@@@@@@ │
│@@@@@@#3;;;;;a#$::::::##::++:2#@@@@@@@@ │
│@@@@@@#4;;;::##$:::::$##+++++3#@@@@@@@@ │
│@@@@@@@@@W##@@@@@###@@@@####@@@@@@@@@@@ │
│@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@ │
│#W##:##b###9#;#+#+?##*9#@@1#=####+@#W@@ │
│@###++:#:+#9#:#WW+#+=3$*+$**:#***#5@@@@ │
│@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@ │
├────────────────────────────────────────┤
│1 1  2 2                                │
└─────━━━────────────────────────────────┘`
	viewImageGIF = `┌━━━━━━━━━━━━────────────────────────────┐
│o sample.gif                            │
├────────────────────────────────────────┤
│@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@ │
│@@@@@@@@@#b#@@@@@#b@@@@@@@b#@@@@@@@@@@@ │
│@@@@@@@bbbbbb@@acccccb@@cccccc@@@@@@@@@ │
│@@@@@@@@bbbbb@@cccccc@@cccc;;@@@@@@@@@@ │
│@@@@@@@@@@@@@9cc#@@@@#:c@@@@@@@@@@@@@@@ │
│@@@@@@@@;ccccc@@;ccc;;@@c;;;;@@@@@@@@@@ │
│@@@@@@@ccccccW@c;;;;;;@W;::::;@@@@@@@@@ │
│@@@@@@@@accc#@@;;;;;!@@:::::;@@@@@@@@@@ │
│@@@@@@@@@@@W;;;@@@@W:::@@@@@@@@@@@@@@@@ │
│@@@@@@@4;;;;;a@8::::::@@::::+2@@@@@@@@@ │
│@@@@@@@4;;;::@@$:::::$@@+++++2@@@@@@@@@ │
│@@@@@@@@@W@@@@@@@@@@@@@@@@@@@@@@@@@@@@@ │
│@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@ │
│####:#@a#@#9@;@+@+0#@=9@@@2@*@###=@@#@@ │
│@@@@;+:@:+#8@;@@@+#++2$*=9*=:@**=@4@@@@ │
│@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@ │
├────────────────────────────────────────┤
│1 1  2 2                                │
└─────━━━────────────────────────────────┘`
	viewImageWebP = `┌━━━━━━━━━━━━━───────────────────────────┐
│o sample.webp                           │
├────────────────────────────────────────┤
│@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@ │
│@@@@@@@@@#b#@@@@##b##@@@##bW#@@@@@@@@@@ │
│@@@@@@@bbbbbb#@acccccb@#cccc;c@@@@@@@@@ │
│@@@@@@@#cbbbb##cccccc#@c;;;;;#@@@@@@@@@ │
│@@@@@@@@@@@@@8cc#@@@@W;;#@@@@@@@@@@@@@@ │
│@@@@@@@#;ccccc##;;;;;;#@;;;;;#@@@@@@@@@ │
│@@@@@@@ccccccW@c;;;;;;@$;::::;@@@@@@@@@ │
│@@@@@@@@bc;;W@#;;;;;!@#:::::;@@@@@@@@@@ │
│@@@@@@@@@@#W;;;@@@@W:::@@@@@@@@@@@@@@@@ │
│@@@@@@@3;;;;;a@8:::::;@@:::++1@@@@@@@@@ │
│@@@@@@@4;;;::#@$:::::W@@+++++3@@@@@@@@@ │
│@@@@@@@@@W##@@@@@###@@@@@###@@@@@@@@@@@ │
│@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@ │
│####:##a###9@c#+@+?W#=9#@@2#*####=@##@@ │
│@###:++#:+#9@;#@#+#++3$==9*=:#**=@5@@@@ │
│@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@ │
├────────────────────────────────────────┤
│1 1  2 2                                │
└─────━━━────────────────────────────────┘`
	viewImageProblemPNG = `┌━━━━━━━━━━━━────────────────────────────┐
│o sample.png                            │
├────────────────────────────────────────┤
│                                        │
│                                        │
│              ___                       │
│             /___/\_                    │
│            _\   \/_/\__                │
│          __\       \/_/\               │
│          \   __    __ \ \              │
│         __\  \_\   \_\ \ \   __        │
│        /_/\\   __   __  \ \_/_/\       │
│        \_\/_\__\/\__\/\__\/_\_\/       │
│           \_\/_/\       /_\_\/         │
│              \_\/       \_\/           │
│                                        │
│                                        │
│    Uh, Houston, we've had a problem    │
│                                        │
├────────────────────────────────────────┤
│1 1  2 2                                │
└─────━━━────────────────────────────────┘`
)

func TestViewImageIntegration(t *testing.T) {
	const width, height = 42, 22
	tests := []struct {
		name string
		// cmd opens the file, view when empty.
		cmd  string
		file string
		data []byte
		// images reports whether the terminal draws images, as ASCII art.
		images  bool
		loading string
		loaded  string
	}{
		{
			name: "png", file: "sample.png", data: imageuritest.SamplePNG,
			images: true, loading: viewImageLoadingPNG, loaded: viewImagePNG,
		},
		{
			name: "edit png", cmd: "edit", file: "sample.png",
			data:   imageuritest.SamplePNG,
			images: true, loading: viewImageLoadingPNG, loaded: viewImagePNG,
		},
		{
			name: "edit webp", cmd: "edit", file: "sample.webp",
			data:   imageuritest.SampleWebP,
			images: true, loading: viewImageLoadingWebP, loaded: viewImageWebP,
		},
		{
			name: "jpg", file: "sample.jpg", data: imageuritest.SampleJPEG,
			images: true, loading: viewImageLoadingJPG, loaded: viewImageJPG,
		},
		{
			name: "gif", file: "sample.gif", data: imageuritest.SampleGIF,
			images: true, loading: viewImageLoadingGIF, loaded: viewImageGIF,
		},
		{
			name: "webp", file: "sample.webp", data: imageuritest.SampleWebP,
			images: true, loading: viewImageLoadingWebP, loaded: viewImageWebP,
		},
		{
			name: "not an image", file: "sample.png",
			data:   []byte("<html>not an image</html>"),
			images: true, loading: viewImageLoadingPNG, loaded: viewImageProblemPNG,
		},
		{
			name: "edit not an image", cmd: "edit", file: "sample.png",
			data:   []byte("<html>not an image</html>"),
			images: true, loading: viewImageLoadingPNG, loaded: viewImageProblemPNG,
		},
		{
			name: "terminal without image support", file: "sample.png",
			data:    imageuritest.SamplePNG,
			loading: viewImageLoadingPNG, loaded: viewImageProblemPNG,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			interrupts := make(chan struct{}, 1)
			newTestPublishOverride = func(term.Event) bool {
				select {
				case interrupts <- struct{}{}:
				default:
				}
				return true
			}
			t.Cleanup(func() { newTestPublishOverride = nil })

			dir, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(
				filepath.Join(dir, tt.file), tt.data, 0o644))

			m := newTestWorkspaceManagerHandlerWithDir(t,
				defaultConfigWithWrap(false), dir, nopShutdownShaderConfig())
			t.Cleanup(func() { _ = m.Close() })
			uri, err := workspaceapi.ParseURI("file://" + dir)
			require.NoError(t, err)
			require.NoError(t, m.addOrCreateWorkspace(uri))
			m.quiesce()

			h := newSafeHandler(m)
			var w handlertest.Writer = term.NewStringWriter(width, height)
			if tt.images {
				w = asciiart.NewStringWriter(width, height, asciiart.DefaultConfig())
			}
			cmd := tt.cmd
			if cmd == "" {
				cmd = "view"
			}
			handlertest.RunHandlerSequenceWriter(t, w, h, width, height,
				[]handlertest.SequenceTestCase{{
					InputSequence: `<c-\\>` + cmd + `<space>` + tt.file + `<enter>`,
					Expected:      tt.loading,
				}})
			awaitImageView(t, h, w, interrupts)
			handlertest.RunHandlerSequenceWriter(t, w, h, width, height,
				[]handlertest.SequenceTestCase{{Expected: tt.loaded}})
		})
	}
}

// awaitImageView redraws on every interrupt, as the host event loop
// does, until the image view's loading animation is gone.
func awaitImageView(
	t *testing.T, h tui.Component, w handlertest.Writer, interrupts <-chan struct{},
) {
	frames, _ := component.ProgressAnimationFrames()
	spinner := strings.Join(frames, "")
	timeout := time.After(10 * time.Second)
	for {
		require.NoError(t, w.Clear(term.Attributes{}))
		h.Draw(w)
		require.NoError(t, w.Flush())
		if !strings.ContainsAny(w.String(), spinner) {
			return
		}
		select {
		case <-interrupts:
		case <-timeout:
			t.Fatalf("image view never finished loading:\n%s", w.String())
		}
	}
}

func TestViewImageUnderCommandPromptShader(t *testing.T) {
	const width, height = 42, 22
	interrupts := make(chan struct{}, 1)
	newTestPublishOverride = func(term.Event) bool {
		select {
		case interrupts <- struct{}{}:
		default:
		}
		return true
	}
	t.Cleanup(func() { newTestPublishOverride = nil })

	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "sample.png"), imageuritest.SamplePNG, 0o644))

	m := newTestWorkspaceManagerHandlerWithDir(t,
		defaultConfigWithWrap(false), dir, nopShutdownShaderConfig())
	t.Cleanup(func() { _ = m.Close() })
	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)
	require.NoError(t, m.addOrCreateWorkspace(uri))
	m.quiesce()

	h := newSafeHandler(m)
	w := asciiart.NewStringWriter(width, height, asciiart.DefaultConfig())
	handlertest.RunHandlerSequenceWriter(t, w, h, width, height,
		[]handlertest.SequenceTestCase{{
			InputSequence: `<c-\\>view<space>sample.png<enter>`,
			Expected:      viewImageLoadingPNG,
		}})
	awaitImageView(t, h, w, interrupts)

	wh, ok := m.focusHandler().(*workspaceHandler)
	require.True(t, ok)
	wh.ex.commandPromptCfg.shader.enabled = true
	handlertest.RunHandlerSequenceWriter(t, w, h, width, height,
		[]handlertest.SequenceTestCase{{
			InputSequence: `<c-\\>`,
			Expected: `┌━━━━━━━━━━━━────────────────────────────┐
│o sample.png                            │
├────────────────────────────────────────┤
│@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@ │
│@@@@@@@@@#b#@@@@##b##@@@##bW#@@@@@@@@@@ │
│@@@@@@@bbbbbb#@acccccb@#cccc;c@@@@@@@@@ │
│@@@@@@@#cbbbb##cccccc#@c;;;;;#@@@@@@@@@ │
│@@@@@@@@@@@@@8cc#@@@@W;;#@@@@@@@@@@@@@@ │
┌────────────────────────────────────────┐
│ ▐                                      │
│ !                                      │
│ !!                                     │
│ addBlaBla                              │
│ cheatsheet                             │
│ clipboardcopy                          │
│ clipboardpaste                         │
│ console                                │
│ cursorhistory                          │
│ debugger                               │
├────────────────────────────────────────┤
│1 1  2 2                                │
└─────━━━────────────────────────────────┘`,
		}})
	require.NotNil(t, wh.ex.promptShader, "the prompt must draw through its shader")
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
		m.tutorialsInstalled = func([]string) ([]string, error) { return nil, nil }
		err = m.workspaceManagerHandler.init(&uri, homeURI, manager,
			notificationsConfig(), cfg, storage, dir,
			func(term.Event) bool {
				return true
			}, runner, pkgtrust.NewStore(), mu, nil,
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
│hello             │
│▐ello             │
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
│       Yes        │
│       No         │
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
│       Yes        │
│       No         │
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
│       Yes        │
│       No         │
└──────────────────┘`},
			{":quit>",
				`┌──────────────────┐
│                  │
├●█████████████████┤
│                  │
│  Are you sure    │
│  you want to     │
│  exit?           │
│       Yes        │
│       No         │
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
│       Yes        │
│       No         │
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
│       Yes        │
│       No         │
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
│       Yes        │
│       No         │
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
│       Yes        │
│       No         │
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
│       Yes        │
│       No         │
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
│       Yes        │
│       No         │
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

	t.Run("tabs opened by workspaceready before the restore stay focusable", func(t *testing.T) {
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
			&uri, cfg, runner, nil, t.TempDir(), nil, nopShutdownShaderConfig())
		t.Cleanup(func() { _ = m.Close() })
		m.mu.Lock()
		ex1 := m.exHandler(m.focusHandler())
		require.NoError(t, ex1.windownew(context.Background(), "right"))
		require.NoError(t, ex1.terminalnewtab(context.Background(), "previous run"))
		require.NoError(t, m.commandCloseWorkspace())

		// Reopening runs the queued commands and then restores the
		// stored session over the layout they built.
		require.NoError(t, m.addWorkspace(uri, true, false, -1))
		for _, cmd := range [][]string{
			{"windowcloseall"},
			{"windownew", "left"},
			{"terminalnewtab", "this run"},
		} {
			require.NoError(t, m.commandWorkspaceReady(cmd...))
		}
		m.mu.Unlock()
		m.waitForWorkspace(t, uri)

		m.mu.Lock()
		defer m.mu.Unlock()
		m.Resize(80, 24)
		ex2 := m.exHandler(m.focusHandler())
		tabs := ex2.comp.Tabs()
		require.Len(t, tabs, 2)
		for _, tab := range tabs {
			win, bound := tab.Window()
			if !bound {
				continue
			}
			require.False(t, win.Closed(),
				"tab %s must not stay bound to a window the restore discarded",
				tab.URI())
			// What clicking the tab in the tab bar does.
			_, err := ex2.comp.SetFocus(win)
			require.NoError(t, err)
			require.NotPanics(t, func() { _, _ = ex2.comp.Focus() })
		}
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

func (s *terminalSessionTestScheme) SetPtySize(workspaceapi.Pty, workspaceapi.PtySize) error {
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
	updates  []capturedUpdate
}

type capturedNote struct {
	level browserapi.NotificationLevel
	msg   string
}

// capturedUpdate is an UpdateNotificationProgress call and whether it
// found the notification.
type capturedUpdate struct {
	msg             string
	progress, total int64
	ok              bool
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
	err := r.inner.UpdateNotificationProgress(id, message, progress, total)
	r.mu.Lock()
	r.updates = append(r.updates, capturedUpdate{msg: message, progress: progress, total: total, ok: err == nil})
	r.mu.Unlock()
	return err
}

func (r *recordingNotifications) updatesSnapshot() []capturedUpdate {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.updates)
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
	m.Handle(term.Event{Type: term.EventMouse, Key: term.MouseRelease})

	// the bottom row is the frame of the only window, not a tab
	m.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseY: 7})
	assert.Equal(t, 1, called)

	m.mu.Unlock()

	require.NoError(t, m.Close())
}

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
	return newTestWorkspaceManagerHandlerWithHome(t, homeURI, manager, mu,
		drainSched, uri, cfg, runner, extensions, dir, onTabsClick,
		shutdownShaderCfg, prepare...)
}

// newTestWorkspaceManagerHandlerWithHome is like
// newTestWorkspaceManagerHandlerWithManagerMu but registers homeURI as
// the home workspace instead of memory:///home.
func newTestWorkspaceManagerHandlerWithHome(
	t *testing.T, homeURI workspaceapi.URI, manager *workspace.Manager,
	mu *sync.Mutex, drainSched func() uint64,
	uri *workspaceapi.URI, cfg ideConfig, runner ExtensionsRunner,
	extensions map[string]Extension, dir string,
	onTabsClick func(int) bool,
	shutdownShaderCfg shutdownShaderConfig,
	prepare ...func(*workspaceManagerHandler),
) *testWorkspaceManagerHandler {
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
	m.tutorialsInstalled = func([]string) ([]string, error) { return nil, nil }
	for _, fn := range prepare {
		fn(m.workspaceManagerHandler)
	}
	err := m.workspaceManagerHandler.init(uri, homeURI, manager,
		notiConfig, cfg, storage, dir, publish, runner, pkgtrust.NewStore(), mu, extensions,
		func() (ideConfig, error) {
			// Like a real reload, every workspace build records its
			// soft errors into its own map.
			ret := cfg
			ret.errors = map[string]error{}
			return ret, nil
		},
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
			"wallpaper_ascii": "workspaceWallpaper",
			"auto_restore":    false,
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
		editorSectionVim: map[string]any{
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

// newHomeSharingTestHandler builds a handler whose home workspace is a
// real directory on disk, so files and workspaces under it resolve to
// the same manager entry as the home workspace.
func newHomeSharingTestHandler(
	t *testing.T, home string, uri workspaceapi.URI,
) (*testWorkspaceManagerHandler, *workspace.Manager, workspaceapi.URI) {
	cfg := defaultCfg()
	mu, sched, drain := buildTestSchedulerForCfg(t, &cfg)
	manager := workspace.NewManager(config.NopConfig(), sched)
	require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme,
		workspace.NewMemoryScheme))
	require.NoError(t, manager.RegisterScheme(workspace.FileScheme,
		workspace.NewFileScheme))

	homeURI, err := workspaceapi.ParseURI("file://" + home)
	require.NoError(t, err)

	runner := FuncExtensionsRunner(testRunnerFn)
	m := newTestWorkspaceManagerHandlerWithHome(t, homeURI, manager, mu,
		drain, &uri, cfg, runner, nil, t.TempDir(), nil,
		nopShutdownShaderConfig())
	t.Cleanup(func() { _ = m.Close() })
	return m, manager, homeURI
}

// requireHomeSchemeOpen fails when the home workspace's scheme has been
// closed. Starting a missing executable is rejected by exec without
// spawning anything, but only after the scheme checks its own ctx.
func requireHomeSchemeOpen(
	t *testing.T, m *testWorkspaceManagerHandler, manager *workspace.Manager,
	homeURI workspaceapi.URI, home string,
) {
	t.Helper()
	require.True(t, manager.HasWorkspace(homeURI),
		"home workspace must stay registered with the manager")
	_, err := m.homeWorkspace.StartCommand(context.Background(),
		workspaceapi.Cmd{Path: filepath.Join(home, "does-not-exist")})
	require.Error(t, err)
	require.NotErrorIs(t, err, context.Canceled,
		"home workspace scheme was closed: every home terminal now fails")
}

func TestHomeWorkspaceSurvivesClosingHomeFileOpenedFromWorkspace(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, "proj")
	require.NoError(t, os.Mkdir(proj, 0o755))
	file := filepath.Join(home, "script.md")
	require.NoError(t, os.WriteFile(file, []byte("# script\n"), 0o644))

	uri, err := workspaceapi.ParseURI("file://" + proj)
	require.NoError(t, err)
	m, manager, homeURI := newHomeSharingTestHandler(t, home, uri)

	fileURI, err := workspaceapi.ParseURI("file://" + file)
	require.NoError(t, err)

	m.mu.Lock()
	ex := m.workspaces[m.focus].ex
	tab, err := ex.editFileURI(fileURI, ex.invokeWindow(), false)
	if err == nil {
		tabWindow, ok := tab.Window()
		require.True(t, ok)
		_, err = ex.comp.SetFocus(tabWindow)
	}
	if err == nil {
		err = ex.dispatchCommand("tabclose")
	}
	m.mu.Unlock()
	require.NoError(t, err)
	m.quiesce()

	requireHomeSchemeOpen(t, m, manager, homeURI, home)
}

func TestClosingHomeDirWorkspaceKeepsHomeWorkspaceAlive(t *testing.T) {
	home := t.TempDir()
	uri, err := workspaceapi.ParseURI("file://" + home)
	require.NoError(t, err)
	m, manager, homeURI := newHomeSharingTestHandler(t, home, uri)

	m.mu.Lock()
	err = m.commandCloseWorkspace()
	m.mu.Unlock()
	require.NoError(t, err)
	m.quiesce()
	require.Equal(t, 0, m.workspaceCount)

	requireHomeSchemeOpen(t, m, manager, homeURI, home)
}

func TestStreamingOpenOfAnExtraneousFileUsesTheManagerOnTheEventLoop(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, "proj")
	require.NoError(t, os.Mkdir(proj, 0o755))
	projURI, err := workspaceapi.ParseURI("file://" + proj)
	require.NoError(t, err)
	m, manager, _ := newHomeSharingTestHandler(t, home, projURI)

	streaming := filepath.Join(home, "streaming")
	require.NoError(t, os.Mkdir(streaming, 0o755))
	streamingURI, err := workspaceapi.ParseURI("file://" + streaming)
	require.NoError(t, err)
	m.mu.Lock()
	m.streamingOpen = true
	require.NoError(t, m.addWorkspace(streamingURI, false, false, -1))
	m.mu.Unlock()
	m.waitForWorkspace(t, streamingURI)

	other := t.TempDir()
	otherURI, err := workspaceapi.ParseURI("file://" + other)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(other, "f.txt"), []byte("x\n"), 0o644))
	fileURI, err := workspaceapi.ParseURI("file://" + filepath.Join(other, "f.txt"))
	require.NoError(t, err)

	m.mu.Lock()
	ex := m.workspaces[m.focus].ex
	require.True(t, ex.workspaceURI.Equal(streamingURI))
	_, err = ex.editFileURI(fileURI, ex.invokeWindow(), false)
	require.NoError(t, err)
	// Any event-loop use of the manager while the file loads.
	_ = manager.HasWorkspace(otherURI)
	m.mu.Unlock()
	ex.comp.WaitStreamingLoads()
	m.quiesce()

	m.mu.Lock()
	defer m.mu.Unlock()
	require.True(t, manager.HasWorkspace(otherURI),
		"the open file must hold the workspace of its directory")
}

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

func TestWorkspaceConfigRequiresTrust(t *testing.T) {
	const wsConfig = "authorizer:\n  auto_authorize_commands: true\neditor:\n  tabspaces: 2\n"
	tests := []struct {
		name        string
		answer      int
		promptErr   error
		wantApplied bool
	}{
		{name: "ignore", answer: workspaceTrustIgnore},
		{name: "dismiss", answer: -1, promptErr: context.Canceled},
		{name: "trust once", answer: workspaceTrustOnce, wantApplied: true},
		{name: "trust", answer: workspaceTrustPersist, wantApplied: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestWorkspaceManagerHandlerWithDir(t, defaultCfg(), "", nopShutdownShaderConfig())
			t.Cleanup(func() { _ = m.Close() })
			m.workspaceConfigFilename = ".rune/config.yaml"
			var prompts []string
			m.workspaceTrust.prompt = func(_ context.Context, message string, _ []string) (int, error) {
				prompts = append(prompts, message)
				return tt.answer, tt.promptErr
			}
			uri, err := workspaceapi.ParseURI("memory://" + t.TempDir())
			require.NoError(t, err)

			for build := range 2 {
				cfg := buildWorkspaceWithConfig(t, m, uri, wsConfig)
				assert.Equal(t, tt.wantApplied, cfg.authorizerAutoAuthorizeCommands(), "build %d", build)
				assert.Equal(t, tt.wantApplied, cfg.editorTabspaces() == 2, "build %d", build)
			}
			require.Len(t, prompts, 1, "one answer holds for the session")
			assert.Contains(t, prompts[0], wsConfig)
		})
	}

	t.Run("the user's own config is not offered for trust", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".rune"), 0o755))
		configPath := filepath.Join(dir, ".rune", "config.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte(wsConfig), 0o644))
		m := newTestWorkspaceManagerHandlerWithDir(t, defaultCfg(), "", nopShutdownShaderConfig())
		t.Cleanup(func() { _ = m.Close() })
		m.workspaceConfigFilename = ".rune/config.yaml"
		m.configPath = configPath
		m.workspaceTrust.prompt = func(context.Context, string, []string) (int, error) {
			t.Error("the user's own config must not be offered for trust")
			return workspaceTrustIgnore, nil
		}
		uri, err := workspaceapi.CurrentUserHostURI(dir)
		require.NoError(t, err)

		cfg := buildWorkspaceWithConfig(t, m, uri, "")
		assert.True(t, cfg.authorizerAutoAuthorizeCommands())
		assert.Equal(t, 2, cfg.editorTabspaces())
	})

	t.Run("the authorizer command revokes trust", func(t *testing.T) {
		m := newTestWorkspaceManagerHandlerWithDir(t, defaultCfg(), "", nopShutdownShaderConfig())
		t.Cleanup(func() { _ = m.Close() })
		m.workspaceConfigFilename = ".rune/config.yaml"
		answers := []int{workspaceTrustPersist, workspaceTrustIgnore}
		m.workspaceTrust.prompt = func(context.Context, string, []string) (int, error) {
			require.NotEmpty(t, answers, "unexpected workspace trust prompt")
			answer := answers[0]
			answers = answers[1:]
			return answer, nil
		}
		uri, err := workspaceapi.ParseURI("memory:///repo")
		require.NoError(t, err)
		require.True(t, buildWorkspaceWithConfig(t, m, uri, wsConfig).authorizerAutoAuthorizeCommands())

		m.mu.Lock()
		authorizer, ok := m.focusEx().comp.REPLCommand("authorizer")
		m.mu.Unlock()
		require.True(t, ok)
		run := func(args ...string) string {
			t.Helper()
			it, err := authorizer.HandleCommand(context.Background(),
				repl.Command{Name: "authorizer", Args: args}, repl.NopProgressWriter())
			require.NoError(t, err)
			return renderResponsives(t, it)
		}
		decision := fmt.Sprintf("persisted trust %s sha256:%s",
			uri.String(), workspaceConfigFingerprint([]byte(wsConfig))[:8])
		assert.Contains(t, run("list"), decision)

		assert.Contains(t, run(append([]string{"revoke"}, strings.Fields(decision)...)...), "Revoked")
		assert.Contains(t, run("list"), "No workspace config decisions.")
		assert.False(t, buildWorkspaceWithConfig(t, m, uri, wsConfig).authorizerAutoAuthorizeCommands(),
			"a revoked workspace asks again")
		assert.Empty(t, answers)
	})
}

// buildWorkspaceWithConfig builds uri as a workspace open runs it, with
// contents as its workspace config unless contents is empty, and returns
// the config the workspace was built with.
func buildWorkspaceWithConfig(
	t *testing.T, m *testWorkspaceManagerHandler, uri workspaceapi.URI, contents string,
) ideConfig {
	t.Helper()
	m.mu.Lock()
	cwd, err := m.createWorkspaceScheme(uri)
	m.mu.Unlock()
	require.NoError(t, err)
	if contents != "" {
		require.NoError(t, cwd.MkdirAll(cwd.Join(cwd.Root(), ".rune"), 0o755))
		f, err := cwd.OpenFile(cwd.Join(cwd.Root(), m.workspaceConfigFilename),
			os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		require.NoError(t, err)
		_, err = f.Write([]byte(contents))
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}
	built, err := m.buildWorkspaceAsync(context.Background(), uri, cwd,
		&pendingWorkspace{uri: uri, cancelCtx: func() {}})
	require.NoError(t, err)
	m.discardBuiltWorkspace(built)
	m.mu.Lock()
	detached, released := m.workspace.ReleaseWorkspace(uri)
	m.mu.Unlock()
	if released {
		require.NoError(t, detached.Close())
	}
	return built.cfg
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

// authorizerCapturingRunner is an ExtensionsRunner that keeps the
// authorizer each workspace's extensions are served with.
type authorizerCapturingRunner struct {
	mu          sync.Mutex
	authorizers map[string]extensionv2.ServerAuthorizer
}

func (r *authorizerCapturingRunner) WorkspaceExtensionsRunner(
	uri workspaceapi.URI, _ map[extensionapi.Permission]extension.ResourceRegistrar,
	authorizer extensionv2.ServerAuthorizer, _ extensionv2.TrustVerifier,
	_, _ string, _ browser.Notifications, _, _ schemeapi.Executor,
	_ extension.Grantor, _ text.Editor, _ ideauthorizer.PromptOpener,
	_ storageapi.Service, _ func(func()) bool,
) (extension.Runner, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.authorizers[uri.String()] = authorizer
	return fnRunner{fn: func(string, string, config.Config) error { return nil }}, nil
}

func (r *authorizerCapturingRunner) authorizer(uri workspaceapi.URI) extensionv2.ServerAuthorizer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.authorizers[uri.String()]
}

func TestBuildExtensionsCommandAuthorizerBindsAlwaysToTheFileItRuns(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.Mkdir(bin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "tool"), []byte("#!/bin/sh\n"), 0o755))
	t.Setenv("PATH", bin+string(filepath.ListSeparator)+os.Getenv("PATH"))

	cfg := defaultConfigWithWrap(false)
	mu, sched, drain := buildTestSchedulerForCfg(t, &cfg)
	manager := workspace.NewManager(config.NopConfig(), sched)
	require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme,
		workspace.NewMemoryScheme))
	require.NoError(t, manager.RegisterScheme(workspace.FileScheme,
		workspace.NewFileScheme))
	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)
	runner := &authorizerCapturingRunner{authorizers: map[string]extensionv2.ServerAuthorizer{}}
	m := newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
		&uri, cfg, runner, nil, t.TempDir(), nil, nopShutdownShaderConfig())
	t.Cleanup(func() { _ = m.Close() })
	m.quiesce()

	authorizer, ok := runner.authorizer(uri).(*ideauthorizer.Authorizer)
	require.True(t, ok, "the workspace's extensions are served with the command authorizer")
	ctx := blueauth.ContextWithClaims(context.Background(),
		blueauth.UserClaims[ideauthorizer.Extension]{Extra: ideauthorizer.Extension{
			Metadata: extensionapi.Metadata{
				DeveloperID: "dev-id", ExtensionID: "test-extension",
				ExtensionName: "Test Extension",
				Permissions:   extensionapi.NewPermissions(extensionapi.PermissionExecute),
			},
		}})
	authorize := func(path string, args ...string) <-chan error {
		done := make(chan error, 1)
		go debug.CapturePanicReport(func() {
			done <- authorizer.AuthorizeCommand(ctx, workspaceapi.Cmd{
				Path: path, Args: args, Dir: dir,
			})
		})
		return done
	}
	const width, height = 120, 40
	h := newSafeHandler(m)
	h.Resize(width, height)
	screen := func() string { return handlertest.DrawHandler(h, width, height) }
	waitFor := func(text string) {
		t.Helper()
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			m.quiesce()
			assert.Contains(c, screen(), text)
		}, 5*time.Second, 10*time.Millisecond)
	}
	await := func(done <-chan error) error {
		t.Helper()
		var err error
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			m.quiesce()
			select {
			case err = <-done:
			default:
				assert.Fail(c, "authorization is pending")
			}
		}, 5*time.Second, 10*time.Millisecond)
		return err
	}

	done := authorize("tool", "sub", "arg")
	waitFor("decides for each of these programs")
	h.Handle(term.Event{Type: term.EventKey, Ch: 'A'})
	require.NoError(t, await(done))

	require.NoError(t, await(authorize("tool", "other")),
		"Always covers the program with any arguments")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tool"), []byte("#!/bin/sh\n"), 0o755))
	done = authorize("./tool", "sub", "arg")
	waitFor("./tool")
	h.Handle(term.Event{Type: term.EventKey, Ch: 'N'})
	require.ErrorIs(t, await(done), blueauth.ErrForbidden,
		"another file with the same name is another program")
}

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

// recordingOpener is a resource opener that records every call. Each call
// returns what open, the extension's side, returns when set; otherwise it
// returns h, or fails with err when h is nil. It completes with complete,
// or with nothing when complete is nil.
type recordingOpener struct {
	mu       sync.Mutex
	opened   []string
	h        browserapi.Handler
	err      error
	open     func(ctx context.Context, uri workspaceapi.URI) (browserapi.Handler, error)
	complete func(ctx context.Context, prefix string) (sdkiterator.Iterator[string], error)
}

func (r *recordingOpener) OpenResource(
	ctx context.Context, uri workspaceapi.URI,
) (browserapi.Handler, error) {
	r.mu.Lock()
	r.opened = append(r.opened, uri.String())
	r.mu.Unlock()
	if r.open != nil {
		return r.open(ctx, uri)
	}
	if r.h == nil {
		return nil, r.err
	}
	return r.h, nil
}

func (r *recordingOpener) CompleteResource(
	ctx context.Context, prefix string,
) (sdkiterator.Iterator[string], error) {
	if r.complete != nil {
		return r.complete(ctx, prefix)
	}
	return sdkiterator.Empty[string](), nil
}

// register makes r the opener of the fake scheme on the focused workspace
// of m, as the extension would over its editor RPC.
func (r *recordingOpener) register(t *testing.T, m *testWorkspaceManagerHandler) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	require.NoError(t, m.exHandler(m.focusHandler()).editorObserver.
		RegisterResourceOpener("fake", r))
}

func (r *recordingOpener) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.opened)
}

func TestRestoreExtensionTabs(t *testing.T) {
	const tabURI = "fake://host/chat"
	uri, err := workspaceapi.ParseURI(tabURI)
	require.NoError(t, err)

	// setup leaves the focused workspace split in two, with the fake tab
	// shown in the right-hand window. Only a tab whose scheme has an
	// opener is saved, so withOpener decides whether there is anything to
	// restore. The reloaded workspace starts without the opener, as a
	// workspace does until its extensions register theirs.
	setup := func(
		t *testing.T, withOpener bool,
	) (*testWorkspaceManagerHandler, workspaceapi.URI) {
		runner := newWaitReadyRunner()
		close(runner.ready)
		m := newTestWorkspaceManagerHandlerWithRunner(t,
			defaultConfigWithWrap(false), t.TempDir(), runner)
		t.Cleanup(func() { _ = m.Close() })
		m.quiesce()
		if withOpener {
			new(recordingOpener).register(t, m)
		}

		m.mu.Lock()
		defer m.mu.Unlock()
		m.Resize(80, 24)
		ex := m.exHandler(m.focusHandler())
		tab, err := ex.comp.Tab(uri, 'x', "chat", browsertest.NewTestHandler())
		require.NoError(t, err)
		_, err = ex.comp.Split(browserapi.OrientationRight, ex.invokeWindow(), tab)
		require.NoError(t, err)
		return m, m.workspaces[m.focus].uri
	}

	reload := func(t *testing.T, m *testWorkspaceManagerHandler) {
		t.Helper()
		m.mu.Lock()
		require.NoError(t, m.commandReloadWorkspace())
		m.mu.Unlock()
		m.quiesce()
	}

	// restoredTabWindow is the window that took the place of the one
	// that showed the tab before the reload.
	restoredTabWindow := func(
		t *testing.T, m *testWorkspaceManagerHandler,
	) browser.Window {
		t.Helper()
		m.mu.Lock()
		defer m.mu.Unlock()
		b := m.exHandler(m.focusHandler()).comp.Browser()
		layout := b.TileLayout()
		require.Len(t, layout.Children, 2, "the split must be restored")
		win, ok := b.Window(layout.Children[1].WindowID)
		require.True(t, ok)
		return win
	}

	// tabOf returns the tab of the fake URI on the focused workspace.
	tabOf := func(t *testing.T, m *testWorkspaceManagerHandler) (*browser.Tab, bool) {
		t.Helper()
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.exHandler(m.focusHandler()).comp.Browser().Tab(uri)
	}

	// showsHandler reports whether the tab of the fake URI has h as its
	// content.
	showsHandler := func(m *testWorkspaceManagerHandler, h browserapi.Handler) bool {
		m.quiesce()
		m.mu.Lock()
		defer m.mu.Unlock()
		tab, ok := m.exHandler(m.focusHandler()).comp.Browser().Tab(uri)
		return ok && tab.Handler() == h
	}

	// placeholderText renders the placeholder of the fake URI.
	placeholderText := func(t *testing.T, m *testWorkspaceManagerHandler) string {
		t.Helper()
		tab, ok := tabOf(t, m)
		require.True(t, ok)
		m.mu.Lock()
		defer m.mu.Unlock()
		w := cell.NewBufferWriter(context.Background(), 60, 6)
		tab.Resize(60, 6)
		tab.Draw(w)
		var b strings.Builder
		for _, row := range w.RawCells() {
			for _, c := range row {
				if c.Ch != 0 {
					b.WriteRune(c.Ch)
				}
			}
			b.WriteRune('\n')
		}
		return b.String()
	}

	opened := func(m *testWorkspaceManagerHandler, r *recordingOpener) func() bool {
		return func() bool {
			m.quiesce()
			return len(r.snapshot()) > 0
		}
	}

	recordNotifications := func(m *testWorkspaceManagerHandler) *recordingNotifications {
		m.mu.Lock()
		defer m.mu.Unlock()
		ex := m.workspaces[m.focus]
		notes := &recordingNotifications{inner: ex.notifications}
		ex.notifications = notes
		ex.pendingTabs.notifications = notes
		return notes
	}

	notified := func(notes *recordingNotifications, want string) bool {
		for _, n := range notes.snapshot() {
			if n.level == browserapi.LevelError && strings.Contains(n.msg, want) {
				return true
			}
		}
		return false
	}

	t.Run("reload shows a placeholder in the restored window", func(t *testing.T) {
		m, _ := setup(t, true)
		reload(t, m)
		want := restoredTabWindow(t, m)

		tab, ok := tabOf(t, m)
		require.True(t, ok, "the tab must be restored before the extension registers")
		m.mu.Lock()
		ex := m.exHandler(m.focusHandler())
		assert.True(t, ex.comp.PendingTabs().Contains(uri))
		bound, ok := tab.Window()
		require.True(t, ok)
		assert.Equal(t, want.WindowID(), bound.WindowID())
		_, icon, _ := ex.comp.Browser().TabIcon(uri)
		_, name, _ := ex.comp.Browser().TabName(uri)
		m.mu.Unlock()
		assert.Equal(t, 'x', icon, "the placeholder must keep the tab's icon")
		assert.Equal(t, "chat", name, "the placeholder must keep the tab's name")
		text := placeholderText(t, m)
		assert.Contains(t, text, "Waiting for the owning extension to open")
		assert.Contains(t, text, tabURI)
	})

	t.Run("the opened content takes the placeholder's place once the opener registers", func(t *testing.T) {
		m, _ := setup(t, true)
		reload(t, m)
		want := restoredTabWindow(t, m)

		h := browsertest.NewTestHandler()
		rec := recordingOpener{h: h}
		rec.register(t, m)

		require.Eventually(t, opened(m, &rec), 2*time.Second, 10*time.Millisecond,
			"the extension must be asked to reopen its tab")
		require.Eventually(t, func() bool { return showsHandler(m, h) },
			2*time.Second, 10*time.Millisecond)
		require.Never(t, func() bool {
			m.quiesce()
			return len(rec.snapshot()) > 1
		}, 100*time.Millisecond, 10*time.Millisecond,
			"the tab must be reopened once")
		assert.Equal(t, []string{tabURI}, rec.snapshot())
		tab, ok := tabOf(t, m)
		require.True(t, ok)
		m.mu.Lock()
		defer m.mu.Unlock()
		bound, ok := tab.Window()
		require.True(t, ok)
		assert.Equal(t, want.WindowID(), bound.WindowID())
		assert.False(t, m.exHandler(m.focusHandler()).comp.PendingTabs().Contains(uri))
		_, icon, _ := m.exHandler(m.focusHandler()).comp.Browser().TabIcon(uri)
		_, name, _ := m.exHandler(m.focusHandler()).comp.Browser().TabName(uri)
		assert.Equal(t, 'x', icon, "the tab keeps the icon it was saved with")
		assert.Equal(t, "chat", name, "the tab keeps the name it was saved with")
	})

	t.Run("the placeholder follows the user to the tab bar", func(t *testing.T) {
		m, _ := setup(t, true)
		reload(t, m)
		closed := restoredTabWindow(t, m)
		m.mu.Lock()
		require.NoError(t, closed.Close())
		m.mu.Unlock()

		h := browsertest.NewTestHandler()
		rec := recordingOpener{h: h}
		rec.register(t, m)

		require.Eventually(t, opened(m, &rec), 2*time.Second, 10*time.Millisecond,
			"the extension must be asked to reopen its tab")
		require.Eventually(t, func() bool { return showsHandler(m, h) },
			2*time.Second, 10*time.Millisecond)
		tab, _ := tabOf(t, m)
		m.mu.Lock()
		_, bound := tab.Window()
		m.mu.Unlock()
		assert.False(t, bound, "the tab must stay where the user left it")
	})

	t.Run("a placeholder the user closed is not reopened", func(t *testing.T) {
		m, _ := setup(t, true)
		reload(t, m)
		tab, ok := tabOf(t, m)
		require.True(t, ok)
		m.mu.Lock()
		require.True(t, m.exHandler(m.focusHandler()).comp.Browser().RemoveTab(tab))
		m.mu.Unlock()

		var rec recordingOpener
		rec.register(t, m)

		require.Never(t, opened(m, &rec), 200*time.Millisecond, 20*time.Millisecond)
	})

	t.Run("content opened for a placeholder the user closed is closed", func(t *testing.T) {
		m, _ := setup(t, true)
		reload(t, m)
		h := browsertest.NewTestHandler()
		closed := make(chan struct{})
		h.CloseCallback = func() error { close(closed); return nil }
		release := make(chan struct{})
		rec := recordingOpener{open: func(context.Context, workspaceapi.URI) (browserapi.Handler, error) {
			<-release
			return h, nil
		}}
		rec.register(t, m)
		require.Eventually(t, opened(m, &rec), 2*time.Second, 10*time.Millisecond)

		tab, ok := tabOf(t, m)
		require.True(t, ok)
		m.mu.Lock()
		require.True(t, m.exHandler(m.focusHandler()).comp.Browser().RemoveTab(tab))
		m.mu.Unlock()
		close(release)

		require.Eventually(t, func() bool {
			m.quiesce()
			select {
			case <-closed:
				return true
			default:
				return false
			}
		}, 2*time.Second, 10*time.Millisecond,
			"content nobody shows must be closed")
		_, ok = tabOf(t, m)
		assert.False(t, ok, "the closed tab must not come back")
	})

	t.Run("a registration during an open does not ask again", func(t *testing.T) {
		m, _ := setup(t, true)
		reload(t, m)
		h := browsertest.NewTestHandler()
		release := make(chan struct{})
		rec := recordingOpener{open: func(context.Context, workspaceapi.URI) (browserapi.Handler, error) {
			<-release
			return h, nil
		}}
		rec.register(t, m)
		require.Eventually(t, opened(m, &rec), 2*time.Second, 10*time.Millisecond)

		// the extension restarts and registers again
		m.mu.Lock()
		require.NoError(t, m.exHandler(m.focusHandler()).editorObserver.
			UnregisterResourceOpener("fake"))
		m.mu.Unlock()
		rec.register(t, m)
		m.quiesce()
		close(release)

		require.Eventually(t, func() bool { return showsHandler(m, h) },
			2*time.Second, 10*time.Millisecond)
		require.Never(t, func() bool {
			m.quiesce()
			return len(rec.snapshot()) > 1
		}, 100*time.Millisecond, 10*time.Millisecond)
	})

	t.Run("an opener registered before the restore is asked right away", func(t *testing.T) {
		m, _ := setup(t, false)
		other, err := workspaceapi.ParseURI("fake://host/other")
		require.NoError(t, err)
		h := browsertest.NewTestHandler()
		rec := recordingOpener{h: h}
		rec.register(t, m)

		m.mu.Lock()
		ex := m.exHandler(m.focusHandler())
		win := ex.invokeWindow()
		err = m.restoreExtensionTabs(ex, []idehistory.ExtensionTab{{
			URI: other, Icon: 'x', Name: "other", WindowID: win.WindowID(), Focus: true,
		}}, map[uint64]browser.Window{win.WindowID(): win})
		m.mu.Unlock()
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			m.quiesce()
			m.mu.Lock()
			defer m.mu.Unlock()
			tab, ok := ex.comp.Browser().Tab(other)
			return ok && tab.Handler() == h
		}, 2*time.Second, 10*time.Millisecond)
		assert.Equal(t, []string{other.String()}, rec.snapshot())
	})

	t.Run("a placeholder is saved again while it waits", func(t *testing.T) {
		m, _ := setup(t, true)
		reload(t, m)
		reload(t, m)
		restoredTabWindow(t, m)

		_, ok := tabOf(t, m)
		require.True(t, ok, "a placeholder must survive a reload before its opener registers")
		m.mu.Lock()
		defer m.mu.Unlock()
		assert.True(t, m.exHandler(m.focusHandler()).comp.PendingTabs().Contains(uri))
	})

	t.Run("all open tabs survive reload", func(t *testing.T) {
		for _, tt := range []struct {
			name    string
			visible int
		}{
			{name: "one visible", visible: 1},
			{name: "all inactive"},
			{name: "two visible in splits", visible: 2},
		} {
			t.Run(tt.name, func(t *testing.T) {
				m, _ := setup(t, true)
				uris := []workspaceapi.URI{uri}
				for _, raw := range []string{"fake://host/second", "fake://host/third"} {
					u, err := workspaceapi.ParseURI(raw)
					require.NoError(t, err)
					uris = append(uris, u)
				}
				names := []string{"chat", "second", "third"}
				icons := []rune{'x', 'y', 'z'}
				wantURIs := []string{tabURI, uris[1].String(), uris[2].String()}

				func() {
					m.mu.Lock()
					defer m.mu.Unlock()
					ex := m.exHandler(m.focusHandler())
					b := ex.comp.Browser()
					layout := b.TileLayout()
					right, ok := b.Window(layout.Children[1].WindowID)
					require.True(t, ok)
					for i := 1; i < len(uris); i++ {
						tab, err := ex.comp.Tab(uris[i], icons[i], names[i], browsertest.NewTestHandler())
						require.NoError(t, err)
						require.NoError(t, right.SetContent(tab))
					}
					switch tt.visible {
					case 0:
						require.NoError(t, right.SetContent(browsertest.NewTestHandler()))
					case 2:
						left, ok := b.Window(layout.Children[0].WindowID)
						require.True(t, ok)
						tab, ok := b.Tab(uri)
						require.True(t, ok)
						require.NoError(t, left.SetContent(tab))
					}
					b.SetFocus(right)
				}()

				assertTabs := func(pending bool) {
					t.Helper()
					m.mu.Lock()
					defer m.mu.Unlock()
					ex := m.exHandler(m.focusHandler())
					b := ex.comp.Browser()
					var gotURIs []string
					for _, tab := range ex.comp.Tabs() {
						if tab.URI().Scheme() == "fake" {
							gotURIs = append(gotURIs, tab.URI().String())
						}
					}
					require.Equal(t, wantURIs, gotURIs, "all tabs must retain their order")
					for i, u := range uris {
						tab, ok := b.Tab(u)
						require.True(t, ok)
						assert.Equal(t, pending, ex.comp.PendingTabs().Contains(u))
						_, icon, _ := b.TabIcon(u)
						_, name, _ := b.TabName(u)
						assert.Equal(t, icons[i], icon)
						assert.Equal(t, names[i], name)
						win, bound := tab.Window()
						side := -1
						if i == 2 && tt.visible > 0 {
							side = 1
						} else if i == 0 && tt.visible == 2 {
							side = 0
						}
						if side < 0 {
							assert.False(t, bound, "inactive tab %s must remain in the tab bar", u)
							continue
						}
						require.True(t, bound)
						layout := b.TileLayout()
						require.Len(t, layout.Children, 2)
						assert.Equal(t, layout.Children[side].WindowID, win.WindowID())
						focus, _ := win.Focus()
						assert.Equal(t, side == 1, focus)
					}
				}

				reload(t, m)
				assertTabs(true)
				reload(t, m)
				assertTabs(true)

				handlers := make(map[string]browserapi.Handler)
				for _, u := range uris {
					handlers[u.String()] = browsertest.NewTestHandler()
				}
				rec := recordingOpener{open: func(_ context.Context, u workspaceapi.URI) (browserapi.Handler, error) {
					return handlers[u.String()], nil
				}}
				rec.register(t, m)
				require.Eventually(t, func() bool {
					m.quiesce()
					m.mu.Lock()
					defer m.mu.Unlock()
					b := m.exHandler(m.focusHandler()).comp.Browser()
					for _, u := range uris {
						tab, ok := b.Tab(u)
						if !ok || tab.Handler() != handlers[u.String()] {
							return false
						}
					}
					return true
				}, 2*time.Second, 10*time.Millisecond)
				assert.ElementsMatch(t, wantURIs, rec.snapshot())
				assertTabs(false)
				reload(t, m)
				assertTabs(true)
			})
		}
	})

	t.Run("the programs listing a status are listed again if their tab is back", func(t *testing.T) {
		m, ws := setup(t, true)
		// No opener serves its scheme, so its tab is not saved.
		gone := mustURI(t, "nowhere://host/gone")
		m.locked(func() {
			ex := m.exHandler(m.focusHandler())
			_, err := ex.comp.Tab(gone, 'g', "gone", browsertest.NewTestHandler())
			require.NoError(t, err)
			for _, u := range []workspaceapi.URI{uri, gone} {
				require.NoError(t, ex.comp.SetTabStatus(u, programstatus.Report{
					State: programstatus.Done, Progress: -1, App: "agent",
				}))
			}
			b := ex.comp.Browser()
			chat, ok := b.Tab(uri)
			require.True(t, ok)
			chat.AckStatus()
			require.Len(t, m.status.Items(), 2, "seen, a tab is still listed")
		})

		reload(t, m)
		m.locked(func() {
			items := m.status.Items()
			require.Len(t, items, 1, "only the program whose tab came back is listed")
			assert.Equal(t, ws, items[0].WS)
			assert.Equal(t, uri, items[0].URI)
			assert.Equal(t, "chat", items[0].Name)
			assert.Equal(t, "agent", items[0].App)
			assert.Empty(t, items[0].Status.Records, "it reported nothing since")
		})

		reload(t, m)
		m.locked(func() {
			require.Len(t, m.status.Items(), 1, "it is saved again with the workspace")
			b := m.exHandler(m.focusHandler()).comp.Browser()
			chat, ok := b.Tab(uri)
			require.True(t, ok)
			b.RemoveTab(chat)
			assert.Empty(t, m.status.Items(), "closed, its tab takes it away")
		})
		reload(t, m)
		m.locked(func() {
			assert.Empty(t, m.status.Items(), "a closed tab does not come back")
		})
	})

	// reopen closes the workspace in focus and opens it again as reload
	// does, with edit, if any, applied to the state it saved, as a later
	// run may find it, and restores the session only if restore. It
	// returns the state the workspace opens with.
	reopen := func(
		t *testing.T, m *testWorkspaceManagerHandler, restore bool, edit func(*idehistory.State),
	) idehistory.State {
		t.Helper()
		m.mu.Lock()
		slot := m.focus
		ws, _, err := m.closeWorkspace()
		require.NoError(t, err)
		state, err := m.state.LoadWorkspaceState(context.Background(), ws)
		require.NoError(t, err)
		if edit != nil {
			edit(&state)
		}
		require.NoError(t, m.state.StoreWorkspaceState(context.Background(), ws, state))
		require.NoError(t, m.addWorkspace(ws, restore, false, slot))
		m.mu.Unlock()
		m.quiesce()
		return state
	}
	// reporting sets up a workspace whose fake tab is saved and reported
	// done as the app agent.
	reporting := func(t *testing.T) (*testWorkspaceManagerHandler, workspaceapi.URI) {
		m, ws := setup(t, true)
		m.locked(func() {
			require.NoError(t, m.exHandler(m.focusHandler()).comp.SetTabStatus(uri, programstatus.Report{
				State: programstatus.Done, Progress: -1, App: "agent",
			}))
		})
		m.quiesce()
		return m, ws
	}
	type listed struct{ ws, uri, app string }
	listing := func(m *testWorkspaceManagerHandler) []listed {
		var ret []listed
		m.locked(func() {
			for _, it := range m.status.Items() {
				ret = append(ret, listed{it.WS.String(), it.URI.String(), it.App})
			}
		})
		return ret
	}

	t.Run("the programs of a saved state", func(t *testing.T) {
		tests := []struct {
			name    string
			restore bool
			edit    func(*idehistory.State)
			wantTab bool
			want    []string
		}{
			{
				name: "a program saved with its tab is listed with it", restore: true,
				wantTab: true, want: []string{"agent"},
			},
			{
				name: "a program whose tab was not saved is not listed", restore: true,
				edit: func(s *idehistory.State) { s.Extensions = nil },
			},
			{
				name: "a tab saved without its program comes back unlisted", restore: true,
				edit:    func(s *idehistory.State) { s.Programs = nil },
				wantTab: true,
			},
			{
				name: "a program saved where no tab was is not listed", restore: true,
				edit: func(s *idehistory.State) {
					s.Programs = append(s.Programs, idehistory.Program{
						URI: mustURI(t, "fake://host/nowhere"), App: "agent",
					})
				},
				wantTab: true, want: []string{"agent"},
			},
			{
				name: "a program saved without a uri is not listed", restore: true,
				edit: func(s *idehistory.State) {
					s.Programs = append([]idehistory.Program{{App: "agent"}}, s.Programs...)
				},
				wantTab: true, want: []string{"agent"},
			},
			{
				name: "a program saved twice is listed once, with the app saved first", restore: true,
				edit: func(s *idehistory.State) {
					s.Programs = []idehistory.Program{{URI: uri, App: "first"}, {URI: uri, App: "second"}}
				},
				wantTab: true, want: []string{"first"},
			},
			{
				name: "a program saved without an app is listed without one", restore: true,
				edit:    func(s *idehistory.State) { s.Programs = []idehistory.Program{{URI: uri}} },
				wantTab: true, want: []string{""},
			},
			{
				name: "a session that is not restored lists no program",
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				m, ws := reporting(t)
				reopen(t, m, tt.restore, tt.edit)

				_, ok := tabOf(t, m)
				assert.Equal(t, tt.wantTab, ok, "whether the tab is back")
				var want []listed
				for _, app := range tt.want {
					want = append(want, listed{ws.String(), uri.String(), app})
				}
				assert.Equal(t, want, listing(m))

				reload(t, m)
				assert.Equal(t, want, listing(m), "what is listed is saved again, and only that")
			})
		}
	})

	t.Run("the programs restored from the prompt", func(t *testing.T) {
		tests := []struct {
			name   string
			idx    int
			option string
			want   []string
		}{
			{name: "answered yes", idx: 0, option: yesOpt, want: []string{"agent"}},
			{name: "answered no", idx: 1, option: noOpt},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				m, ws := reporting(t)
				state := reopen(t, m, false, nil)
				require.Empty(t, listing(m))
				m.locked(func() {
					newOpenRestorePromptHandler(m.workspaceManagerHandler, m.exHandler(m.focusHandler()),
						ws, state, yesOpt, noOpt).OnSelect(tt.idx, tt.option)
				})
				m.quiesce()
				var want []listed
				for _, app := range tt.want {
					want = append(want, listed{ws.String(), uri.String(), app})
				}
				assert.Equal(t, want, listing(m))

				reload(t, m)
				assert.Equal(t, want, listing(m), "what is listed is saved again, and only that")
			})
		}
	})

	t.Run("a restored program stays listed once its extension reopens its tab", func(t *testing.T) {
		m, ws := reporting(t)
		reload(t, m)
		require.Equal(t, []listed{{ws.String(), uri.String(), "agent"}}, listing(m),
			"the placeholder of the tab is listed")

		h := browsertest.NewTestHandler()
		rec := recordingOpener{h: h}
		rec.register(t, m)
		require.Eventually(t, func() bool { return showsHandler(m, h) },
			2*time.Second, 10*time.Millisecond)
		assert.Equal(t, []listed{{ws.String(), uri.String(), "agent"}}, listing(m))

		m.locked(func() {
			require.NoError(t, m.exHandler(m.focusHandler()).comp.SetTabStatus(uri, programstatus.Report{
				State: programstatus.Working, Progress: -1, App: "fresh",
			}))
		})
		m.quiesce()
		assert.Equal(t, []listed{{ws.String(), uri.String(), "fresh"}}, listing(m),
			"it lists what its program reports next")
	})

	// A second reload can land while the first one's placeholder is still
	// being opened. The reloaded workspace saves the placeholder as the
	// tab, and the open in flight belongs to the workspace that is gone: it
	// must neither reach the new placeholder nor leak what it opened.
	t.Run("a reload during an open", func(t *testing.T) {
		tests := []struct {
			name string
			// honorCancel makes the first opener give up when the reload
			// cancels it, instead of returning its content late.
			honorCancel bool
		}{
			{name: "cancels the open of the closed workspace", honorCancel: true},
			{name: "closes the content the closed workspace's open returns late"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				m, _ := setup(t, true)
				reload(t, m)
				restoredTabWindow(t, m)
				m.mu.Lock()
				closedEx := m.exHandler(m.focusHandler())
				m.mu.Unlock()

				late := browsertest.NewTestHandler()
				lateClosed := make(chan struct{})
				late.CloseCallback = func() error { close(lateClosed); return nil }
				release := make(chan struct{})
				firstOpen := make(chan error, 1)
				paced := recordingOpener{open: func(
					ctx context.Context, _ workspaceapi.URI,
				) (browserapi.Handler, error) {
					if tt.honorCancel {
						select {
						case <-release:
						case <-ctx.Done():
							firstOpen <- ctx.Err()
							return nil, ctx.Err()
						}
					} else {
						<-release
					}
					firstOpen <- nil
					return late, nil
				}}
				paced.register(t, m)
				require.Eventually(t, opened(m, &paced), 2*time.Second, 10*time.Millisecond,
					"the first reload must ask for the tab")

				reload(t, m)
				second := restoredTabWindow(t, m)
				notes := recordNotifications(m)

				tab, ok := tabOf(t, m)
				require.True(t, ok, "the placeholder must be saved as the tab it stands for")
				m.mu.Lock()
				ex := m.exHandler(m.focusHandler())
				require.NotSame(t, closedEx, ex, "the reload must rebuild the workspace")
				assert.True(t, ex.comp.PendingTabs().Contains(uri))
				bound, ok := tab.Window()
				require.True(t, ok)
				assert.Equal(t, second.WindowID(), bound.WindowID())
				_, icon, _ := ex.comp.Browser().TabIcon(uri)
				_, name, _ := ex.comp.Browser().TabName(uri)
				m.mu.Unlock()
				assert.Equal(t, 'x', icon, "the placeholder must keep the tab's icon")
				assert.Equal(t, "chat", name, "the placeholder must keep the tab's name")

				if tt.honorCancel {
					select {
					case err := <-firstOpen:
						require.ErrorIs(t, err, context.Canceled,
							"closing the workspace must cancel its open")
					case <-time.After(2 * time.Second):
						t.Fatal("the open of the closed workspace was not cancelled")
					}
				} else {
					close(release)
					require.NoError(t, <-firstOpen)
					select {
					case <-lateClosed:
					case <-time.After(2 * time.Second):
						t.Fatal("content nobody can show must be closed")
					}
				}
				assert.Equal(t, []string{tabURI}, paced.snapshot(),
					"the reloaded workspace must not ask the opener of the closed one")
				assert.True(t, func() bool {
					m.quiesce()
					m.mu.Lock()
					defer m.mu.Unlock()
					return ex.comp.PendingTabs().Contains(uri)
				}(), "the placeholder must keep waiting for the restarted extension")

				// the extension restarts and registers again
				h := browsertest.NewTestHandler()
				rec := recordingOpener{h: h}
				rec.register(t, m)
				require.Eventually(t, func() bool { return showsHandler(m, h) },
					2*time.Second, 10*time.Millisecond,
					"the restarted extension's content must take the placeholder's place")
				m.mu.Lock()
				bound, ok = tab.Window()
				m.mu.Unlock()
				require.True(t, ok)
				assert.Equal(t, second.WindowID(), bound.WindowID())
				assert.Empty(t, notes.snapshot(), "nothing failed on the reloaded workspace")
			})
		}
	})

	t.Run("reopening without restore does not reopen the tab", func(t *testing.T) {
		m, uri := setup(t, true)

		m.mu.Lock()
		require.NoError(t, m.commandCloseWorkspace())
		require.NoError(t, m.addWorkspace(uri, false, false, -1))
		m.mu.Unlock()
		m.waitForWorkspace(t, uri)

		var rec recordingOpener
		rec.register(t, m)

		require.Never(t, opened(m, &rec), 200*time.Millisecond, 20*time.Millisecond,
			"a workspace opened without restore must not reopen extension tabs")
		_, ok := tabOf(t, m)
		assert.False(t, ok)
	})

	t.Run("a tab no opener can reopen is not saved", func(t *testing.T) {
		m, _ := setup(t, false)
		reload(t, m)

		var rec recordingOpener
		rec.register(t, m)

		require.Never(t, opened(m, &rec), 200*time.Millisecond, 20*time.Millisecond,
			"a tab whose scheme had no opener when it was saved must not be reopened")
	})

	t.Run("a failure is shown on the placeholder and notified", func(t *testing.T) {
		m, _ := setup(t, true)
		reload(t, m)
		notes := recordNotifications(m)
		(&recordingOpener{err: errors.New("boom")}).register(t, m)

		require.Eventually(t, func() bool {
			m.quiesce()
			return notified(notes, "open "+tabURI+": boom")
		}, 2*time.Second, 10*time.Millisecond)
		assert.Contains(t, placeholderText(t, m), "boom")
		m.mu.Lock()
		defer m.mu.Unlock()
		assert.True(t, m.exHandler(m.focusHandler()).comp.PendingTabs().Contains(uri),
			"the placeholder must keep waiting for a later attempt")
	})

	t.Run("an opener that returns no content is reported", func(t *testing.T) {
		m, _ := setup(t, true)
		reload(t, m)
		notes := recordNotifications(m)
		new(recordingOpener).register(t, m)

		require.Eventually(t, func() bool {
			m.quiesce()
			return notified(notes, "open "+tabURI+": the extension returned no content")
		}, 2*time.Second, 10*time.Millisecond)
	})

	t.Run("an opener that does not answer in time is reported", func(t *testing.T) {
		m, _ := setup(t, true)
		reload(t, m)
		notes := recordNotifications(m)
		m.mu.Lock()
		m.exHandler(m.focusHandler()).pendingTabs.timeout = 50 * time.Millisecond
		m.mu.Unlock()
		(&recordingOpener{open: func(ctx context.Context, _ workspaceapi.URI) (browserapi.Handler, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}}).register(t, m)

		require.Eventually(t, func() bool {
			m.quiesce()
			return notified(notes,
				"open "+tabURI+": the extension did not open it within 50ms")
		}, 2*time.Second, 10*time.Millisecond)
	})
}

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
	require.NoError(t, loadConfig(&cfg, f.Name(), nil,
		DefaultConfig{src: "config = {}"},
		term.RingBell, term.ScheduleNextTick))
	return cfg
}

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

func TestTextOptsInstallSyntaxTrees(t *testing.T) {
	var cfg ideConfig
	initDefaultConfig(&cfg, nil, term.RingBell, term.ScheduleNextTick, "")
	cfg.storage = storagestub.NewInMemoryService()
	uri, err := workspaceapi.ParseURI("file:///Users/x/src/proj")
	require.NoError(t, err)

	textCfg := text.DefaultConfig()
	for _, opt := range (&workspaceManagerHandler{}).textOpts(cfg, nil, uri, nil) {
		opt(&textCfg)
	}
	assert.NotNil(t, textCfg.SyntaxTree,
		"without a syntax tree hook, file tabs lose highlights, folds and indents")
}

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

func TestCommandBindingOptsEditorKeyBindings(t *testing.T) {
	seq := func(keys string) thandler.Sequence {
		s, err := thandler.ParseSequence(keys)
		require.NoError(t, err)
		return s
	}
	for _, tc := range []struct {
		mode   string
		want   map[string][][]string
		absent []string
	}{
		{
			mode: editorModeHelix,
			want: map[string][][]string{
				"gd": {{"lsp", "definition"}},
				"]d": {{"jumptolocation", "next", "lsp-diagnostics"}},
			},
			absent: []string{"ma"},
		},
		{
			mode: editorModeVim,
			want: map[string][][]string{
				"ma": {{text.CommandDeleteAllLocations, "a"}, {text.CommandCreateLocation, "a"}},
				"gd": {{"configdefinition"}},
			},
			absent: []string{"]d"},
		},
		{
			mode:   editorModeStandard,
			want:   map[string][][]string{"gd": {{"configdefinition"}}},
			absent: []string{"ma"},
		},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			cfg := ideConfig{
				cfg: map[string]any{
					"editor": map[string]any{"mode": tc.mode},
					"command": map[string]any{
						"key_bindings": map[string]any{"gd": "configdefinition"},
					},
				},
				errors: map[string]error{},
			}
			textCfg := text.DefaultConfig()
			for _, o := range commandBindingOpts(cfg) {
				o(&textCfg)
			}
			for keys, cmds := range tc.want {
				assert.Equalf(t, cmds, textCfg.CommandSequenceBindings[seq(keys)], "binding for %s", keys)
			}
			for _, keys := range tc.absent {
				assert.NotContainsf(t, textCfg.CommandSequenceBindings, seq(keys), "binding for %s", keys)
			}
		})
	}
}

func TestWorkspaceParserHighlightsWithConfiguredHighlights(t *testing.T) {
	rawDir := t.TempDir()
	dir, err := filepath.EvalSymlinks(rawDir)
	require.NoError(t, err)

	dataDir := t.TempDir()
	stageTreeSitterGo(t, dataDir)
	configPath := filepath.Join(dataDir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
clipboard: memory
editor:
  highlights:
    keyword:
      fg: lime
`), 0o666))

	mu := new(sync.Mutex)
	scheduleNextTick, drainSchedule := newTestScheduler(t, mu)
	i, err := New(dir, configPath, dataDir, pkgtrust.NewStore(), newTestStorage(t, dataDir),
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

	mu.Lock()
	wh := i.workspaceHandler.workspaces[i.workspaceHandler.focus]
	mu.Unlock()
	require.NotNil(t, wh)

	uri, err := workspaceapi.ParseURI("file://" + filepath.Join(dir, "main.go"))
	require.NoError(t, err)
	// The workspace parser is the one extensions highlight with.
	it, err := wh.parser.Highlight(uri, "package main")
	require.NoError(t, err)
	locations, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	require.NotEmpty(t, locations)
	assert.Equal(t, term.Coordinates{}, locations[0].From)
	assert.Equal(t, term.Attributes{Fg: term.ColorLime}, locations[0].Attr)
}

func workingStatus() programstatus.Report {
	return programstatus.Report{State: programstatus.Working, Progress: -1}
}
