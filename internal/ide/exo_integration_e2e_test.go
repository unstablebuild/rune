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
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/text"
)

func TestExoVimEndToEnd(t *testing.T) {
	vimBin, err := exec.LookPath("vim")
	if err != nil {
		t.Skip("vim binary not available")
	}
	_ = vimBin

	// EvalSymlinks because t.TempDir() returns /var/... on macOS but
	// the file scheme canonicalises to /private/var/... so URI lookup
	// must match.
	dir := t.TempDir()
	canonical, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	dir = canonical

	const initialContent = "hello\n"
	const insertedContent = "exo"
	relFile := "exo.txt"
	filePath := filepath.Join(dir, relFile)
	require.NoError(t, os.WriteFile(filePath, []byte(initialContent), 0o644))

	cfg := defaultConfigWithWrap(false)
	editorCfg := cfg.cfg["editor"].(map[string]any)
	editorCfg["mode"] = "exo"
	// Turning auto_save on lets the e2e also guard the
	// "auto-save must be skipped under exo" wiring: the
	// autoSaverFactory hook below must never fire under an
	// external editor.
	editorCfg["auto_save"] = true
	editorCfg["exo"] = map[string]any{
		// Quoted argv segments intentionally exercise the parens
		// that regressed in shell.Fields double-tokenisation. The
		// extra flags make startup deterministic across machines:
		//   -Nu NONE     skip user vimrc.
		//   -n           disable swap.
		//   +startinsert! drop straight into insert mode at EOL so
		//                 subsequent keystrokes type literal text.
		"command": `vim -Nu NONE -n "+call cursor({line}, {col})" "+startinsert!" {file}`,
		// editor.exo.goto is required by validateExo. The
		// concrete value is irrelevant for this test (we don't
		// drive SetCursorAtScroll) but it must be non-empty and
		// parse, otherwise the IDE silently falls back to modal
		// mode and the test stops exercising exo at all.
		"goto": "<esc>:{line}<enter>{col}|",
		"quit": "<esc>:qa<enter>",
	}
	// terminalConfig() reads cfg.ringBell; vte panics on a nil bell.
	cfg.ringBell = func() {}
	require.Equal(t, "exo", cfg.editorMode())

	// Hook the autoSaver factory so we can assert exo never wires
	// the saver up. Done before constructing the test handler so
	// the swap is in place when subscribeAllEvents runs.
	prevFactory := autoSaverFactory
	t.Cleanup(func() { autoSaverFactory = prevFactory })
	var autoSaverConstructed atomic.Bool
	autoSaverFactory = func(
		_ autoSaverFlusher, _ browserapi.Notifications,
		_ func(func()) bool, _ time.Duration,
	) *autoSaver {
		autoSaverConstructed.Store(true)
		return nil
	}

	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	m := newTestWorkspaceManagerHandlerWithDir(t, cfg, dir,
		nopShutdownShaderConfig())
	t.Cleanup(func() { _ = m.Close() })

	require.NoError(t, m.addOrCreateWorkspace(uri))
	m.quiesce()

	assert.False(t, autoSaverConstructed.Load(),
		"exo must skip the autoSaver wiring; otherwise external "+
			"saves race the saver and surface ErrStaleData warnings")

	// Resize the handler so the embedded vte has a viewport big
	// enough for vim to render its statusline and command area.
	h := newSafeHandler(m)
	h.Resize(80, 24)

	// Open the file via :edit so the same path that crashed in
	// production is exercised end-to-end. We dispatch events
	// directly rather than using handlertest.RunHandlerSequence
	// because the latter asserts the rendered frame and we only
	// care that the editor opens and writes the file.
	dispatch := func(evs ...term.Event) {
		for _, ev := range evs {
			_, _ = h.Handle(ev)
		}
	}
	openSeq, err := term.ParseKeys(
		`<c-\\>edit<space>` + relFile + `<enter>`)
	require.NoError(t, err)
	for _, k := range openSeq {
		dispatch(keyEvent(k))
	}

	// Give vim a moment to launch and process its `+startinsert!`
	// before we drive keystrokes. There is no portable in-band
	// readiness signal across editors (and -n disables the swap
	// file we could otherwise poll), so we use a generous fixed
	// delay. The test still asserts modification on disk.
	time.Sleep(750 * time.Millisecond)

	// Drive vim: <text><esc>:wq<enter>. We started vim with
	// +startinsert! so we are already in insert mode.
	for _, r := range insertedContent {
		dispatch(keyEvent(term.KeyComb{Ch: r}))
	}
	dispatch(
		keyEvent(term.KeyComb{Key: term.KeyEsc}),
		keyEvent(term.KeyComb{Ch: ':'}),
		keyEvent(term.KeyComb{Ch: 'w'}),
		keyEvent(term.KeyComb{Ch: 'q'}),
		keyEvent(term.KeyComb{Key: term.KeyEnter}),
	)

	// vim should write and exit; the file is then modified on disk.
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return false
		}
		return string(data) != initialContent &&
			strings.Contains(string(data), insertedContent)
	}, 10*time.Second, 100*time.Millisecond,
		"file %q must contain inserted text %q after vim :wq",
		filePath, insertedContent)

	got, err := os.ReadFile(filePath)
	require.NoError(t, err)
	assert.Contains(t, string(got), insertedContent,
		"on-disk file must reflect vim's writes")

	assert.False(t, autoSaverConstructed.Load(),
		"autoSaver must remain unwired for the duration of a "+
			"exo session, even after the external editor saves")
}

func TestExoVimSwapfileGracefulClose(t *testing.T) {
	if _, err := exec.LookPath("vim"); err != nil {
		t.Skip("vim binary not available")
	}
	// Isolate HOME so vim writes its .viminfo (and the spinner of
	// .viminf[a-z].tmp files it uses when the primary is locked)
	// under a fresh dir. A real $HOME can carry leftover viminf*.tmp
	// files from prior crashes, causing vim to bail with E929 before
	// it has a chance to react to our quit sequence — and the .swp
	// file would then linger past the Close.
	t.Setenv("HOME", t.TempDir())

	// t.TempDir() returns /var/... on macOS but the file scheme
	// canonicalises to /private/var/... so URI lookup must match.
	dir := t.TempDir()
	canonical, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	dir = canonical

	relFile := "exo.txt"
	filePath := filepath.Join(dir, relFile)
	require.NoError(t, os.WriteFile(filePath, []byte("hello\n"), 0o644))

	cfg := defaultConfigWithWrap(false)
	editorCfg := cfg.cfg["editor"].(map[string]any)
	editorCfg["mode"] = "exo"
	editorCfg["exo"] = map[string]any{
		// No -n so vim creates a swap file while running.
		"command": `vim -Nu NONE {file}`,
		"goto":    "<esc>:{line}<enter>{col}|",
		"quit":    "<esc>:qa!<enter>",
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
	openSeq, err := term.ParseKeys(
		`<c-\\>edit<space>` + relFile + `<enter>`)
	require.NoError(t, err)
	for _, k := range openSeq {
		dispatch(keyEvent(k))
	}

	time.Sleep(750 * time.Millisecond)

	// Confirm vim actually created a swap so the post-close
	// assertion below is not vacuously true.
	swapFile := filepath.Join(dir, "."+relFile+".swp")
	require.Eventually(t, func() bool {
		_, err := os.Stat(swapFile)
		return err == nil
	}, 5*time.Second, 100*time.Millisecond,
		"vim must have created a swap file at %q while running",
		swapFile)

	fileURI, err := m.workspaces[m.focus].ex.workspace.URI(relFile)
	require.NoError(t, err)
	eh, err := m.workspaces[m.focus].ex.comp.Editor(fileURI)
	require.NoError(t, err)

	require.NoError(t, eh.Close())

	require.Eventually(t, func() bool {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return false
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".swp") {
				return false
			}
		}
		return true
	}, 10*time.Second, 100*time.Millisecond,
		"editor.exo.quit must let vim clean up its swap file "+
			"before the PTY is torn down")
}

func TestExoVimSetCursorAtScroll(t *testing.T) {
	if _, err := exec.LookPath("vim"); err != nil {
		t.Skip("vim binary not available")
	}

	dir := t.TempDir()
	canonical, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	dir = canonical

	// Fixed-width lines so column math is unambiguous. Lines are
	// AAAA, BBBB, CCCC, DDDD, EEEE with a trailing newline.
	initial := "AAAA\nBBBB\nCCCC\nDDDD\nEEEE\n"
	relFile := "cursor.txt"
	filePath := filepath.Join(dir, relFile)
	require.NoError(t, os.WriteFile(filePath, []byte(initial), 0o644))

	cfg := defaultConfigWithWrap(false)
	editorCfg := cfg.cfg["editor"].(map[string]any)
	editorCfg["mode"] = "exo"
	editorCfg["exo"] = map[string]any{
		// Deterministic vim: no rc, no swap. Crucially we do NOT
		// pass +startinsert! so vim lands in normal mode and the
		// goto sequence below ('<esc>:{line}<enter>{col}|') can
		// position the cursor before we switch to insert.
		"command": `vim -Nu NONE -n {file}`,
		// Default goto: ESC, :<line><enter>, then <col>| in normal
		// mode. `|` is vim's go-to-column motion (1-based).
		"goto": "<esc>:{line}<enter>{col}|",
		"quit": "<esc>:qa<enter>",
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
	openSeq, err := term.ParseKeys(
		`<c-\\>edit<space>` + relFile + `<enter>`)
	require.NoError(t, err)
	for _, k := range openSeq {
		dispatch(keyEvent(k))
	}

	// Let vim start before we interact with the exo handler.
	time.Sleep(750 * time.Millisecond)

	// Reach into the workspace to grab the actual text.Handler for
	// the file. SetCursorAtScroll is part of text.Handler, not the
	// outer browser.Tab, so we go through ex.comp.Editor.
	fileURI, err := m.workspaces[m.focus].ex.workspace.URI(relFile)
	require.NoError(t, err)
	eh, err := m.workspaces[m.focus].ex.comp.Editor(fileURI)
	require.NoError(t, err)

	// Position cursor on line 3 ("CCCC"), column 2: i.e. between
	// the first and second 'C'. The goto template renders
	// <esc>:3<enter>2|. Without the Raw-bytes fix in
	// exoeditor.editorHandler.SetCursorAtScroll, those keys never reach
	// the pty and vim stays at (1,1).
	require.True(t, eh.SetCursorAtScroll(term.Coordinates{Y: 2, X: 1}),
		"SetCursorAtScroll must report success when a goto "+
			"template is configured")

	// Tiny pause so vim consumes the goto sequence before we type.
	time.Sleep(150 * time.Millisecond)

	// Insert a marker at the cursor position then write.
	const marker = "XX"
	dispatch(keyEvent(term.KeyComb{Ch: 'i'}))
	for _, r := range marker {
		dispatch(keyEvent(term.KeyComb{Ch: r}))
	}
	dispatch(
		keyEvent(term.KeyComb{Key: term.KeyEsc}),
		keyEvent(term.KeyComb{Ch: ':'}),
		keyEvent(term.KeyComb{Ch: 'w'}),
		keyEvent(term.KeyComb{Ch: 'q'}),
		keyEvent(term.KeyComb{Key: term.KeyEnter}),
	)

	// Expected on-disk content after the edit: line 3 becomes
	// "CXXCCC" because `i` in vim inserts BEFORE the cursor and the
	// cursor was on the second character of "CCCC".
	expected := "AAAA\nBBBB\nCXXCCC\nDDDD\nEEEE\n"

	require.Eventually(t, func() bool {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return false
		}
		return string(data) == expected
	}, 10*time.Second, 100*time.Millisecond,
		"file %q must equal expected content after vim :wq:\n"+
			"want:\n%q\n",
		filePath, expected)

	got, err := os.ReadFile(filePath)
	require.NoError(t, err)
	assert.Equal(t, expected, string(got),
		"SetCursorAtScroll must place the marker at the exact "+
			"position requested; mismatch means the goto "+
			"sequence did not reach vim and the cursor stayed "+
			"elsewhere")
}

// compile-time guard: text.Handler must expose SetCursorAtScroll.
var _ interface {
	SetCursorAtScroll(term.Coordinates) bool
} = (text.Handler)(nil)

func TestFileExplorerExoEnter(t *testing.T) {
	if _, err := exec.LookPath("vim"); err != nil {
		t.Skip("vim binary not available")
	}

	// EvalSymlinks: macOS t.TempDir() returns /var/... but the FS
	// scheme canonicalises to /private/var/... so URI lookup must
	// match.
	rawDir := t.TempDir()
	dir, err := filepath.EvalSymlinks(rawDir)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(
		filepath.Join(dir, "subdir"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "subdir", "child.txt"),
		[]byte("hi"), 0o644))

	cfg := defaultConfigWithWrap(false)
	editorCfg := cfg.cfg["editor"].(map[string]any)
	editorCfg["mode"] = "exo"
	editorCfg["exo"] = map[string]any{
		"command": `vim -Nu NONE -n "+call cursor({line}, {col})" {file}`,
		"goto":    "<esc>:{line}<enter>{col}|",
		"quit":    "<esc>:qa<enter>",
	}
	cfg.ringBell = func() {}
	require.Equal(t, "exo", cfg.editorMode())
	// The exofallback default is standard; verify it propagated so
	// downstream behaviour (Enter toggles, no vi search) matches.
	require.Equal(t, "standard", cfg.exoFallback())

	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)
	m := newTestWorkspaceManagerHandlerWithDir(t, cfg, dir,
		nopShutdownShaderConfig())
	t.Cleanup(func() { _ = m.Close() })

	require.NoError(t, m.addOrCreateWorkspace(uri))
	m.quiesce()

	h := newSafeHandler(m)
	h.Resize(40, 12)

	ex := m.focusEx()
	require.NotNil(t, ex)
	assert.True(t, ex.ed.IsExternal(),
		"exo workspace must remain externally managed even when "+
			"some URIs route to the fallback")

	m.mu.Lock()
	err = ex.fexplorer(context.Background())
	m.mu.Unlock()
	require.NoError(t, err, "fexplorer must open under exo via the "+
		"exofallback router for memory:///fexplorer")
	require.NotNil(t, ex.fileExplorerWin,
		"file explorer window must be present")
	require.NotNil(t, ex.fileExplorerHandler,
		"file explorer handler must be cached")

	explorer := ex.fileExplorerHandler
	// The workspace fs watcher refreshes the tree from its own
	// goroutine under m.mu, so every read of explorer state has to
	// take the same lock.
	explorerRows := func() int {
		m.mu.Lock()
		defer m.mu.Unlock()
		return explorer.ed.CellView().Rows()
	}
	searchMode := func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return explorer.ed.IsSearchMode()
	}
	beforeRows := explorerRows()
	require.Greater(t, beforeRows, 0,
		"explorer tree must render at least one row")

	// With cursor on the first (directory) row, <Enter> must expand
	// the tree. The modeless fallback delivers <Enter> to the inner
	// editor handler, which the file explorer interprets as
	// expand-or-open since it is not in search mode.
	_, handled := h.Handle(term.Event{
		Type: term.EventKey, Key: term.KeyEnter,
	})
	require.True(t, handled, "<Enter> must be handled")
	require.False(t, searchMode(),
		"modeless fallback must not enter search mode on <Enter>")
	assert.NotEqual(t, beforeRows, explorerRows(),
		"<Enter> on a directory row must toggle the tree")
}
