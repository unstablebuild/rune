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

package shop

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"

	"unstable.build/rune/internal/handler/handlertest"
)

const (
	testWidth  = 80
	testHeight = 30
)

// feed dispatches the sequence to the handler one key at a time, the
// same way the rune-go-sdk event loop would. Unlike
// handlertest.RunHandlerSequence this does not assert output equality.
func feed(t *testing.T, h tui.Handler, sequence string) {
	t.Helper()
	keys, err := term.ParseKeys(sequence)
	require.NoError(t, err)
	for _, k := range keys {
		h.Handle(term.Event{Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key})
	}
}

func TestRoot_HomePaletteShowsPages(t *testing.T) {
	r := NewRoot(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() { _ = r.Close() }()

	r.Resize(testWidth, testHeight)
	screen := handlertest.DrawHandler(r, testWidth, testHeight)

	// Home page is visible by default, not the command palette.
	require.Contains(t, screen, "Welcome to sshshop",
		"home page should be visible by default; got:\n"+screen)
	require.Contains(t, screen, "Instructions",
		"home page instructions should be visible by default; got:\n"+screen)
	require.Contains(t, screen, "shop>",
		"default layout should include the shell in the bottom-right tile; got:\n"+screen)
	for _, name := range []string{"home", "about", "product", "pricing", "account", "shell"} {
		require.Containsf(t, screen, name,
			"tab bar should expose %q by default; got:\n%s", name, screen)
	}
	require.NotContains(t, screen, "windowclose",
		"command palette should not be open by default; got:\n"+screen)
}

func TestRoot_SelectAboutShowsPage(t *testing.T) {
	r := NewRoot(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() { _ = r.Close() }()

	r.Resize(testWidth, testHeight)
	// Open the command palette explicitly, then execute `view about`.
	feed(t, r, "<c-p>view<space>about<enter>")

	screen := handlertest.DrawHandler(r, testWidth, testHeight)
	require.Contains(t, screen, "About",
		"after selecting 'about' the About page should render; got:\n"+screen)
	require.Contains(t, screen, "sshshop",
		"about page body should mention sshshop; got:\n"+screen)

	// Palette should be gone.
	require.NotContains(t, collapseWS(screen), "product pricing",
		"palette must be closed when a page is shown; got:\n"+screen)
}

func TestRoot_CtrlPOpensPalette(t *testing.T) {
	r := NewRoot(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() { _ = r.Close() }()
	r.Resize(testWidth, testHeight)

	feed(t, r, "<c-p>")
	screen := handlertest.DrawHandler(r, testWidth, testHeight)
	require.Contains(t, screen, "windowclose",
		"ctrl-p should open the command palette; got:\n"+screen)
	require.Contains(t, screen, "Description",
		"palette should show manual immediately; got:\n"+screen)
}

func TestRoot_CtrlCExits(t *testing.T) {
	r := NewRoot(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() { _ = r.Close() }()
	r.Resize(testWidth, testHeight)

	exit, handled := r.Handle(term.Event{
		Type: term.EventKey,
		Ch:   'c',
		Mod:  term.ModCtrl,
	})

	require.True(t, exit, "Ctrl-C should exit the session")
	require.True(t, handled, "Ctrl-C should be reported as handled")
}

func TestRoot_CtrlCExitsFromPage(t *testing.T) {
	r := NewRoot(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() { _ = r.Close() }()
	r.Resize(testWidth, testHeight)
	feed(t, r, "<c-p>view<space>about<enter>")

	exit, handled := r.Handle(term.Event{
		Type: term.EventKey,
		Ch:   'c',
		Mod:  term.ModCtrl,
	})
	require.True(t, exit, "Ctrl-C should exit the session from a page")
	require.True(t, handled)
}

func TestRoot_WithInterrupterIsPluggedIn(t *testing.T) {
	var calls atomic.Int32
	interrupter := term.FuncInterrupter(func(context.Context) error {
		calls.Add(1)
		return nil
	})

	r := NewRoot(nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithInterrupter(interrupter),
	)
	defer func() { _ = r.Close() }()

	// The palette is initialized eagerly in NewRoot and uses the
	// interrupter during setup, so `calls` is already > 0 by the
	// time NewRoot returns. Invoking it directly below proves the
	// Root kept our interrupter rather than substituting Nop.
	require.NotNil(t, r.interrupter)
	before := calls.Load()
	require.NoError(t, r.interrupter.Interrupt(context.Background()))
	require.Greater(t, calls.Load(), before,
		"Root.interrupter should route to the caller-supplied interrupter")
}

func TestRoot_PageHasNoHashPrefix(t *testing.T) {
	r := NewRoot(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() { _ = r.Close() }()
	r.Resize(testWidth, testHeight)
	feed(t, r, "<c-p>view<space>about<enter>")

	screen := handlertest.DrawHandler(r, testWidth, testHeight)
	require.Contains(t, screen, "About",
		"about page should render its heading; got:\n"+screen)
	require.NotContains(t, screen, "# About",
		"header prefix must be disabled; got:\n"+screen)
}

func TestRoot_WithScheduleNextTickPluggedIn(t *testing.T) {
	var calls atomic.Int32
	schedule := func(fn func()) bool {
		calls.Add(1)
		fn()
		return true
	}

	r := NewRoot(nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithScheduleNextTick(schedule),
	)
	defer func() { _ = r.Close() }()
	r.Resize(testWidth, testHeight)
	feed(t, r, "<c-p>view<space>product<enter>")

	// The product page contains a fenced code block candidate via
	// the pricing table etc. — regardless of which blocks exist, the
	// markdown component only invokes ScheduleNextTick when a Parser
	// is configured AND there is a code block to highlight. We don't
	// configure a Parser, so this test asserts identity-plumbing via
	// the Root field rather than a call-count: WithScheduleNextTick
	// must override the nil default.
	require.NotNil(t, r.scheduleNextTick,
		"WithScheduleNextTick should populate Root.scheduleNextTick")
	r.scheduleNextTick(func() { calls.Add(100) })
	require.EqualValues(t, 101, calls.Load(),
		"Root.scheduleNextTick should route to the caller-supplied function")
}

func TestRoot_PaletteShowsManualImmediately(t *testing.T) {
	r := NewRoot(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() { _ = r.Close() }()
	r.Resize(testWidth, testHeight)
	feed(t, r, "<c-p>")

	// Focus the first entry so its manual has something to display.
	// The palette populates the manual from the focused Manual's
	// Summary / Synopsis / Description — which in our case is the
	// Summary string configured in pages.go.
	screen := handlertest.DrawHandler(r, testWidth, testHeight)
	require.Contains(t, screen, "Close the current active window",
		"palette should show the focused command's manual immediately; got:\n"+screen)
}

func TestRoot_FexplorerShowsPagesList(t *testing.T) {
	r := NewRoot(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() { _ = r.Close() }()
	r.Resize(testWidth, testHeight)
	feed(t, r, "<c-p>fexplorer<enter>")

	screen := handlertest.DrawHandler(r, testWidth, testHeight)
	require.Contains(t, screen, "about.md",
		"fexplorer should show the available pages as fake files; got:\n"+screen)
	require.Contains(t, screen, "product.md",
		"fexplorer should show the available pages as fake files; got:\n"+screen)
	require.NotContains(t, screen, "",
		"fexplorer should use guaranteed single-width icons; got:\n"+screen)
}

func TestRoot_TabTogglesFexplorer(t *testing.T) {
	r := NewRoot(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() { _ = r.Close() }()
	r.Resize(testWidth, testHeight)

	feed(t, r, "<tab>")
	screen := handlertest.DrawHandler(r, testWidth, testHeight)
	require.Contains(t, screen, "about.md",
		"tab should open the page explorer; got:\n"+screen)

	feed(t, r, "<tab>")
	screen = handlertest.DrawHandler(r, testWidth, testHeight)
	require.NotContains(t, screen, "about.md",
		"tab should toggle the explorer back closed; got:\n"+screen)
}

func TestRoot_ShellDoesNotExposeOSCommands(t *testing.T) {
	r := NewRoot(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() { _ = r.Close() }()
	require.NotNil(t, r.shellHelp)

	_, err := r.shellHelp.HandleCommand(context.Background(), repl.Command{Name: "ls"}, repl.NopProgressWriter())
	require.True(t, errors.Is(err, repl.ErrNotFound),
		"shell registry should reject unknown OS commands instead of delegating to mvdan shell")
}

func TestRoot_ViewRecreatesClosedPageTab(t *testing.T) {
	r := NewRoot(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() { _ = r.Close() }()
	r.Resize(testWidth, testHeight)

	// Reproduce the real user flow that used to panic:
	//  1. show a page tab in the current window,
	//  2. close it via the tab/window path,
	//  3. ask the shop to show it again.
	require.NoError(t, r.view("about"))
	require.NoError(t, r.tabclose())

	// This used to panic because Root cached the stale *browser.Tab pointer
	// and Window.SetContent would panic when browser.Component could no longer
	// find that tab in its internal registry.
	require.NoError(t, r.view("about"))

	screen := handlertest.DrawHandler(r, testWidth, testHeight)
	require.Contains(t, screen, "About",
		"view should recreate and show a previously-closed page tab; got:\n"+screen)
	require.NotContains(t, screen, "Welcome to sshshop",
		"recreated page tab should replace the current window content; got:\n"+screen)
}

// collapseWS collapses runs of whitespace into single spaces so tests
// can make structural assertions without caring about padding.
func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
