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

// Package shop holds the storefront tui.Handler served over SSH.
//
// Each SSH session constructs a fresh Root via NewRoot. The root owns a
// browser.Component, a command palette floating on top of the browser,
// and N markdown pages. When a page is selected the palette closes and
// the only visible window shows that page. When the user dismisses the
// page (q/Esc) the palette reopens and the browser falls back to the
// wallpaper.
package shop

import (
	"fmt"
	"log/slog"

	"github.com/gliderlabs/ssh"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	rhandler "github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"

	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/ide/console/ideconsole"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/standard"
)

// Root is the storefront's top-level tui.Handler. One per SSH session.
type Root struct {
	log     *slog.Logger
	storage storageapi.Service

	b       *browser.Component
	cmd     *command.Prompt
	cmdWin  browser.Window
	cmdPrev browser.Window
	width   int
	height  int

	pages []page

	pageTabs  map[string]*browser.Tab
	pageWin   browser.Window
	shellTab  *browser.Tab
	shell     tui.Handler
	shellHelp *ideconsole.CommandRegistry

	fileExplorerWin browser.Window
	fileExplorer    browserapi.Handler

	// interrupter is passed to child components that expect a
	// term.Interrupter (notably command.Prompt for its async completion
	// animation). When nil, term.NopInterrupter is used.
	interrupter term.Interrupter

	// scheduleNextTick, if non-nil, is forwarded to every markdown
	// page's Config so async highlighting and other markdown-internal
	// work post their completion callbacks to the session's event
	// loop (see runsession.go for the concrete wiring). When nil the
	// markdown component falls back to its default synchronous
	// scheduler, which is fine for tests but unsuitable at runtime
	// because it blocks construction on I/O.
	scheduleNextTick func(func()) bool
}

// Compile-time interface assertion.
var _ tui.Handler = (*Root)(nil)

// Option configures a Root at construction time.
type Option func(*Root)

// WithInterrupter installs a term.Interrupter that child components
// (the command palette in particular) use to request redraws from
// background goroutines. In RunScreen mode the caller is responsible
// for building an interrupter backed by their per-session Screen; the
// process-wide term.PublishEvent / term.ScheduleNextTick helpers do
// NOT target the session and would otherwise be silently dropped.
func WithInterrupter(in term.Interrupter) Option {
	return func(r *Root) { r.interrupter = in }
}

// WithScheduleNextTick installs a next-tick scheduler that child
// components (the markdown pages in particular) use to defer
// async work onto the event loop. In RunScreen mode the caller
// should back this with a function that publishes an interrupt on
// the per-session screen so the scheduled callback executes and a
// redraw follows.
func WithScheduleNextTick(fn func(func()) bool) Option {
	return func(r *Root) { r.scheduleNextTick = fn }
}

// NewRoot constructs a storefront handler for the given session. The
// session is currently unused beyond logging the remote addr; keep it
// here because future iterations will read user identity from it.
func NewRoot(_ ssh.Session, log *slog.Logger, opts ...Option) *Root {
	cfg := browser.DefaultConfig()
	cfg.Wallpaper = makeWallpaper()
	cfg.Frame = true
	cfg.FrameUnion = true

	r := &Root{
		log:         log,
		storage:     storagestub.NewInMemoryService(),
		b:           browser.NewComponent(cfg),
		pages:       pages(),
		pageTabs:    make(map[string]*browser.Tab),
		interrupter: term.NopInterrupter(),
		scheduleNextTick: func(fn func()) bool {
			fn()
			return true
		},
	}
	for _, opt := range opts {
		opt(r)
	}
	r.mustInitLayout()
	return r
}

// Resize satisfies tui.Component.
func (r *Root) Resize(width, height int) {
	r.width = width
	r.height = height
	r.b.Resize(width, height)
}

// Draw satisfies tui.Component. The palette is already a floating
// window inside the browser, so the browser draws both layers for us.
func (r *Root) Draw(w term.Writer) {
	r.b.Draw(w)
}

// Handle satisfies tui.Handler.
//
// Ctrl-C always terminates the session. Otherwise events go to the
// browser, which routes them to the floating palette when it has
// focus. When a page handler reports exit=true (e.g. q/Esc in the
// markdown handler) we don't exit the session — we drop the page
// content and reopen the palette instead.
func (r *Root) Handle(ev term.Event) (exit, handled bool) {
	if isCtrlC(ev) {
		return true, true
	}
	if isCtrlP(ev) {
		if r.cmd != nil {
			r.closePalette()
		} else {
			r.openPalette()
		}
		return false, true
	}
	if r.cmd == nil {
		if exit, handled := r.handleKeyBinding(ev); handled {
			return exit, handled
		}
	}
	wasPage := r.focusedWindowShowsPage()
	exit, handled = r.b.Handle(ev)
	if exit && wasPage {
		r.onPageExit()
		exit = false
	}
	return exit, handled
}

// isCtrlC reports whether ev is the Ctrl-C key combination. The SDK
// surfaces Ctrl-<letter> as (Ch: letter, Mod: ModCtrl); there is no
// distinct Key constant for it.
func isCtrlC(ev term.Event) bool {
	return ev.Type == term.EventKey &&
		ev.Mod == term.ModCtrl &&
		(ev.Ch == 'c' || ev.Ch == 'C')
}

func isCtrlP(ev term.Event) bool {
	return ev.Type == term.EventKey &&
		ev.Mod == term.ModCtrl &&
		(ev.Ch == 'p' || ev.Ch == 'P')
}

// Cursor satisfies tui.Handler.
func (r *Root) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return r.b.Cursor()
}

// Selection satisfies tui.Handler.
func (r *Root) Selection() (string, bool) {
	return r.b.Selection()
}

// Close releases any resources held by the storefront. It is safe to
// call multiple times.
func (r *Root) Close() error {
	return r.b.Close()
}

// --- palette / page navigation -------------------------------------------

// openPalette creates a new command.Prompt populated with the available
// pages and installs it as a floating window centered over the browser.
func (r *Root) openPalette() {
	if r.cmd != nil {
		return
	}
	cfg := command.DefaultConfig()
	cfg.NoMarkdown = false
	// Always surface the manual panel for the focused entry so the
	// user sees the synopsis/description without having to idle.
	cfg.ShowManual = true
	cfg.Editor = paletteEditor{}
	manuals := r.commandManuals()

	prompt := command.NewPrompt(
		r.storage,
		command.FuncCompleter(r.completePromptCommand),
		command.FuncDispatcher(r.dispatchPromptCommand),
		r.interrupter,
		manuals,
		cfg,
	)

	handler := browser.FuncFloating(
		browser.FuncHandler(
			rhandler.WithComponent(prompt,
				component.WithBackground(prompt, term.NewCell(0, 0, term.Attributes{
					Bg:    cfg.ElementAttr.Bg,
					Attrs: cfg.ElementAttr.Attrs,
				})),
			),
			func() error {
				err := prompt.Close()
				if r.cmd == prompt {
					r.cmd = nil
					r.cmdWin = nil
				}
				return err
			},
		),
		prompt.Dimensions,
	)

	r.cmd = prompt
	prev := r.b.Focus()
	if !prev.IsFloating() {
		r.cmdPrev = prev
	}
	r.cmdWin = r.b.Floating(handler, browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
	})
	if r.width > 0 && r.height > 0 {
		r.b.Resize(r.width, r.height)
	}
}

// closePalette tears down the currently-open palette floating window.
// No-op if the palette is already closed.
func (r *Root) closePalette() {
	if r.cmdWin != nil && !r.cmdWin.Closed() {
		_ = r.cmdWin.Close()
	}
	r.cmd = nil
	r.cmdWin = nil
}

// onPageExit is called when a page's handler asks to exit (q/Esc).
// We return the current window to the home tab rather than reopening
// the command palette.
func (r *Root) onPageExit() {
	win := r.invokeWindow()
	home, ok := r.pageTabs["home"]
	if !ok {
		return
	}
	if err := win.SetContent(home); err != nil {
		r.log.Warn("restore home tab", "err", err)
	}
}

func (r *Root) focusedWindowShowsPage() bool {
	win := r.invokeWindow()
	content, err := win.Content()
	if err != nil {
		return false
	}
	tab, ok := content.(*browser.Tab)
	if !ok {
		return false
	}
	for _, pageTab := range r.pageTabs {
		if tab == pageTab {
			return true
		}
	}
	return false
}

func (r *Root) invokeWindow() browser.Window {
	if r.cmd != nil && r.cmdPrev != nil && !r.cmdPrev.Closed() {
		return r.cmdPrev
	}
	return r.b.Focus()
}

func (r *Root) handleKeyBinding(ev term.Event) (exit, handled bool) {
	if ev.Type != term.EventKey {
		return false, false
	}
	if ev.Mod == 0 && ev.Key == term.KeyTab {
		_ = r.toggleFileExplorer()
		return false, true
	}
	if ev.Mod != term.ModCtrl {
		return false, false
	}
	switch ev.Ch {
	case 'h', 'H':
		_ = r.tabprevious()
		return false, true
	case 'l', 'L':
		_ = r.tabnext()
		return false, true
	case 'w', 'W':
		_ = r.tabclose()
		return false, true
	default:
		return false, false
	}
}

func mustParseURI(raw string) workspaceapi.URI {
	uri, err := workspaceapi.ParseURI(raw)
	if err != nil {
		panic(fmt.Errorf("parse uri %q: %w", raw, err))
	}
	return uri
}

// paletteEditor adapts standard.NewHandler to command.Editor for the
// shop's command palette modal edit mode. Using standard.NewHandler
// directly (rather than going through text.Editor.Edit) avoids the
// auxiliary status / icons / location bars that would otherwise shift
// the visible cursor coordinates.
type paletteEditor struct{}

func (paletteEditor) Edit(buf *cell.Buffer) command.EditHandler {
	uri := workspaceapi.RandomURI("memory")
	return standard.NewHandler(buf, uri, text.IndentRuneTab, 0,
		standard.WithCommandBar(false),
		standard.WithWrap(false),
	)
}
