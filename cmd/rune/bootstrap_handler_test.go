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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/term/gui/glassbar"
)

// fakeHandler counts calls to each tui.Handler method so tests can
// assert delegation behavior.
type fakeHandler struct {
	resizeCalls    int
	lastResizeW    int
	lastResizeH    int
	drawCalls      int
	cursorCalls    int
	selectionCalls int
	handleCalls    int
	lastEv         term.Event

	cursorCoord term.Coordinates
	cursorStyle term.CursorStyle
	cursorShow  bool

	selectionText string
	selectionOK   bool

	handleExit    bool
	handleHandled bool
}

func (f *fakeHandler) Resize(w, h int) {
	f.resizeCalls++
	f.lastResizeW = w
	f.lastResizeH = h
}

func (f *fakeHandler) Draw(term.Writer) { f.drawCalls++ }

func (f *fakeHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	f.cursorCalls++
	return f.cursorCoord, f.cursorStyle, f.cursorShow
}

func (f *fakeHandler) Selection() (string, bool) {
	f.selectionCalls++
	return f.selectionText, f.selectionOK
}

func (f *fakeHandler) Handle(ev term.Event) (exit, handled bool) {
	f.handleCalls++
	f.lastEv = ev
	return f.handleExit, f.handleHandled
}

func TestBootstrapHandlerDelegates(t *testing.T) {
	inner := &fakeHandler{
		cursorCoord:   term.Coordinates{X: 3, Y: 4},
		cursorShow:    true,
		selectionText: "selected",
		selectionOK:   true,
		handleHandled: true,
	}
	b := &bootstrapHandler{inner: inner}

	b.Resize(80, 24)
	require.Equal(t, 1, inner.resizeCalls)
	require.Equal(t, 80, inner.lastResizeW)
	require.Equal(t, 24, inner.lastResizeH)

	b.Draw(nil)
	require.Equal(t, 1, inner.drawCalls)

	c, _, show := b.Cursor()
	require.Equal(t, 1, inner.cursorCalls)
	require.Equal(t, term.Coordinates{X: 3, Y: 4}, c)
	require.True(t, show)

	sel, ok := b.Selection()
	require.Equal(t, 1, inner.selectionCalls)
	require.Equal(t, "selected", sel)
	require.True(t, ok)

	ev := term.Event{Type: term.EventKey}
	exit, handled := b.Handle(ev)
	require.Equal(t, 1, inner.handleCalls)
	require.Equal(t, ev, inner.lastEv)
	require.False(t, exit)
	require.True(t, handled)
}

// TestBootstrapHandlerSwapInner verifies that swapping the inner
// handler causes Handle to forward events to the new inner. We cannot
// build a real *ide.IDE in a unit test so we exercise the post-swap
// state directly.
func TestBootstrapHandlerSwapInner(t *testing.T) {
	a := &fakeHandler{}
	b := &fakeHandler{}
	bh := &bootstrapHandler{inner: a, chosenEditor: editorVim}
	bh.inner = b

	ev := term.Event{Type: term.EventKey}
	_, _ = bh.Handle(ev)
	require.Equal(t, 0, a.handleCalls, "old inner should not receive events after swap")
	require.Equal(t, 1, b.handleCalls, "new inner should receive events after swap")

	_, _ = bh.Handle(ev)
	require.Equal(t, 2, b.handleCalls)
}

// TestBootstrapHandlerResizesAfterSwap reproduces a panic where the
// configured IDE's first Draw rendered against a zero-width buffer
// because gui.Update only invokes Resize on layout change, not on every
// tick. The handler must remember the last Resize dimensions and apply
// them to the new inner immediately after the swap.
func TestBootstrapHandlerResizesAfterSwap(t *testing.T) {
	pre := &fakeHandler{}
	post := &fakeHandler{}

	bh := &bootstrapHandler{inner: pre}
	bh.Resize(120, 40)
	require.Equal(t, 1, pre.resizeCalls)
	require.Equal(t, 120, pre.lastResizeW)
	require.Equal(t, 40, pre.lastResizeH)

	// Simulate the post-swap assignment + explicit Resize that
	// performSwap does for the configured IDE.
	bh.inner = post
	if bh.lastResizeW > 0 && bh.lastResizeH > 0 {
		bh.inner.Resize(bh.lastResizeW, bh.lastResizeH)
	}
	require.Equal(t, 1, post.resizeCalls, "new inner must be Resized after swap")
	require.Equal(t, 120, post.lastResizeW)
	require.Equal(t, 40, post.lastResizeH)
}

// TestAttachGUIInstallsQuickMenu reproduces a bug where the native quick
// menu only appeared after the first window resize: installing replays
// the last frame, which stayed zero because SetFrame was only ever
// reached from Resize. Attaching the GUI must publish the install, and
// the install must reposition the bar itself.
func TestAttachGUIInstallsQuickMenu(t *testing.T) {
	var published []term.Event
	bh := &bootstrapHandler{
		inner: &fakeHandler{},
		publishEvent: func(ev term.Event) bool {
			published = append(published, ev)
			return true
		},
		quickMenu: []ide.QuickMenuButton{
			{Symbol: "plus", Title: "Open Project", Command: []string{"workspaceopen"}},
		},
	}

	bh.attachGUI(nil, false)
	require.Len(t, published, 1)
	require.Equal(t, term.EventInterrupt, published[0].Type)
	require.NotNil(t, published[0].UserFunc,
		"attachGUI must publish a quick menu install without waiting for a Resize")
}

func TestQuickMenuCellsWithoutButtons(t *testing.T) {
	b := &bootstrapHandler{}
	require.Zero(t, b.quickMenuCells(),
		"an empty quick menu must not reserve a grid column")
}

// TestQuickMenuToggleCollapsesReservedColumn pins that hiding the bar
// gives its reserved column back and clears the native buttons, and
// that showing it restores both.
func TestQuickMenuToggleCollapsesReservedColumn(t *testing.T) {
	if !glassbar.Supported() {
		t.Skip("no native quick menu on this platform")
	}
	b := &bootstrapHandler{
		publishEvent: func(term.Event) bool { return true },
		quickMenu: []ide.QuickMenuButton{
			{Symbol: "plus", Title: "Open Project", Command: []string{"workspaceopen"}},
		},
	}

	require.True(t, b.quickMenuVisible())
	require.Equal(t, quickMenuColumnCells, b.quickMenuCells())
	require.Len(t, b.quickMenuButtons(), 1)

	b.setQuickMenuVisible(false)
	require.False(t, b.quickMenuVisible())
	require.Zero(t, b.quickMenuCells(), "hiding must give the column back")
	require.Empty(t, b.quickMenuButtons(), "hiding must clear the native buttons")

	b.setQuickMenuVisible(true)
	require.True(t, b.quickMenuVisible())
	require.Equal(t, quickMenuColumnCells, b.quickMenuCells())
}

// TestQuickMenuUnavailableWithoutButtons keeps the toggle inert when
// the user configured no buttons, so it can never reserve a column for
// a bar that has nothing to show.
func TestQuickMenuUnavailableWithoutButtons(t *testing.T) {
	b := &bootstrapHandler{publishEvent: func(term.Event) bool { return true }}
	require.False(t, b.quickMenuAvailable())

	b.setQuickMenuVisible(true)
	require.False(t, b.quickMenuVisible())
	require.Zero(t, b.quickMenuCells())
}

// TestLoadQuickMenuKeepsValidButtons pins that one malformed entry does
// not cost the user the whole bar: config validation neutralises the bad
// entry and still hands back a usable tree, so the valid buttons load.
func TestLoadQuickMenuKeepsValidButtons(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.star")
	require.NoError(t, os.WriteFile(path, []byte(
		`config["gui"]["quick_menu"] = [`+
			`{"symbol": "terminal", "title": "New Terminal", "command": "terminalnew"},`+
			`{"symbol": "not a symbol", "command": "help"},`+
			`]`+"\n"), 0o644))

	b := &bootstrapHandler{configPath: path}
	b.loadQuickMenu()

	require.Equal(t, []ide.QuickMenuButton{{Symbol: "terminal",
		Title: "New Terminal", Command: []string{"terminalnew"}}}, b.quickMenu)
}

// TestApplyInitialThemeAttrHoldsEventLoopLock pins that the theme seed
// runs as an event-loop iteration. ide.New starts the cwd workspace
// build on a background goroutine that reads the same shader-runner
// state SetDefaultAttributes writes (via abortPendingBuild ->
// stopLoading), so seeding the attributes off the loop lock is a data
// race.
func TestApplyInitialThemeAttrHoldsEventLoopLock(t *testing.T) {
	b := newConfiguredBootstrapForEnvTest(t, configFilename,
		"editor:\n  mode: modal\n", t.TempDir())

	done := make(chan struct{})
	b.mu.Lock()
	go func() {
		defer close(done)
		b.applyInitialThemeAttr(b.realIDE)
	}()

	select {
	case <-done:
		b.mu.Unlock()
		t.Fatal("applyInitialThemeAttr must run under the event loop lock")
	case <-time.After(100 * time.Millisecond):
	}

	b.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("applyInitialThemeAttr did not complete after the lock was released")
	}
}
