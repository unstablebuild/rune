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

package plugin

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/standard"
	"unstable.build/rune/internal/text/vi"
)

// Handler implements a browser.Floating that runs a command in a terminal
// emulator, as a plugin. All fields of the given vte.Config will be overriden
// except for term.Attributes.
type Handler struct {
	liveHandler   tui.Handler
	doneHandler   tui.Handler
	emulator      *vte.Handler
	frame         bool
	frameCharSet  component.FrameCharSet
	cfg           vte.Config
	width, height int

	// non-interactive mode state
	nonInteractiveMinWidth  int
	nonInteractiveMinHeight int

	title string

	// interactive mode state
	interactiveHeight int
	interactiveWidth  int
	exitKey           int
	lastExitKey       time.Time

	cancelCtx func()

	bar *pluginHandlerBar
	// watcherWG tracks the initEmulator goroutine that listens for
	// the vte's process-exit error and toggles bar.setDone. Tests
	// can call WaitInflight to block until both the vte spawn and
	// this hand-off have completed.
	watcherWG sync.WaitGroup
}

var _ component.Scrollable = (*Handler)(nil)

const exitKeyRepeatTimeout = 400 * time.Millisecond

// New allocates storage for a new plugin.Handler and initializes it.
// On error the returned handler is nil: a partially-initialized
// handler is not safe to use or Close.
func New(
	publisher browser.EventPublisher, notifications browser.Notifications,
	e schemeapi.Executor, t schemeapi.Terminal, tm browser.TabManager,
	cmdAndArgs []string, maxWidth int, opts ...Option,
) (*Handler, error) {
	ret := new(Handler)
	err := ret.Init(publisher, notifications, e, t, tm,
		cmdAndArgs, maxWidth, opts...)
	if err != nil {
		return nil, err
	}
	return ret, nil
}

// Init initializes this Handler.
func (h *Handler) Init(
	publisher browser.EventPublisher, notifications browser.Notifications,
	e schemeapi.Executor, t schemeapi.Terminal, tm browser.TabManager,
	cmdAndArgs []string, maxWidth int, opts ...Option,
) error {
	err := h.init(publisher, notifications, e, t, tm,
		cmdAndArgs, maxWidth, opts...)
	if err != nil {
		return err
	}
	go debug.CapturePanicReport(func() {
		h.bar.initElapsedTicker()
	})
	return err
}

// WaitInflight blocks until the async vte spawn goroutine completes.
// If the spawn errored, it also waits for the initEmulator watcher
// to hand the error off to bar.setDone so the doneHandler state is
// observable to callers. For successful spawns the watcher then
// just observes the process lifecycle, which is not "inflight" work.
func (e *Handler) WaitInflight() {
	if e.emulator == nil {
		return
	}
	e.emulator.Component().WaitSpawn()
	if e.emulator.Component().SpawnErrored() {
		e.watcherWG.Wait()
	}
}

// Dimensions satisfies browser.Floating.
func (p *Handler) Dimensions() (int, int) {
	if p.emulator.Component().IsComplete() {
		height := int(math.Max(float64(p.emulator.Component().Height()),
			float64(p.nonInteractiveMinHeight)))
		width := int(math.Max(float64(p.emulator.Component().MaxWidth()),
			float64(p.nonInteractiveMinWidth)))
		return width, height
	}
	return p.interactiveWidth, p.interactiveHeight
}

// StatusURI returns the uri the program reports its statuses under,
// or a zero uri when it has no terminal.
func (p *Handler) StatusURI() workspaceapi.URI {
	if p.emulator == nil {
		return workspaceapi.URI{}
	}
	return p.emulator.Component().URI()
}

// Title returns the bar title: the command and args unless
// overridden via WithTitle.
func (p *Handler) Title() string {
	return p.title
}

// Handle satisfies browser.Floating.
func (e *Handler) Handle(ev term.Event) (exit, handled bool) {
	if exit = e.shouldExit(ev); exit {
		return
	}
	handler := e.handler()
	exit, handled = handler.Handle(ev)
	// User might want to inspect the output of a program
	// that ran in the primary buffer. It's assumed that
	// if the program used the alternate buffer, then it's interactive,
	// and so when it exits, the output of the primary will be empty,
	// and so exit should be bubbled up, and handler removed from the UI.
	if exit && !e.emulator.Component().UsedAlternateBuffer() &&
		handler == e.liveHandler {
		exit = false
	}
	return
}

// Draw satisfies browser.Floating.
func (e *Handler) Draw(w term.Writer) {
	e.handler().Draw(w)
}

// Resize satisfies browser.Floating.
func (e *Handler) Resize(width, height int) {
	e.width = width
	e.height = height
	// always resize live, so next call to Dimensions "adjusts" height of doneHandler
	// by mirroing what the liveHandler would do.
	e.liveHandler.Resize(width, height)
	if e.doneHandler != nil {
		e.doneHandler.Resize(width, height)
	}
}

// Cursor satisfies browser.Floating.
func (e *Handler) Cursor() (pos term.Coordinates, style term.CursorStyle, show bool) {
	pos, style, show = e.handler().Cursor()
	if !show {
		return
	}
	pos.Y = int(math.Max(0, math.Min(float64(pos.Y), float64(e.height-1))))
	pos.X = int(math.Max(0, math.Min(float64(pos.X), float64(e.width-1))))
	return
}

// Selection satisfies browser.Floating.
func (e *Handler) Selection() (string, bool) {
	return e.handler().Selection()
}

// Close satisfies browser.Floating.
func (p *Handler) Close() error {
	p.cancelCtx()
	p.bar.Close()
	return p.emulator.Close()
}

// OnFocusChange dispatches a change in focus to the underlying
// vte.Handler.
func (p *Handler) OnFocusChange(inFocus bool) {
	p.emulator.OnFocusChange(inFocus)
}

// SeekUp satisfies component.Scrollable.
func (e *Handler) SeekUp() bool {
	return e.emulator.SeekUp()
}

// SeekDown satisfies component.Scrollable.
func (e *Handler) SeekDown() bool {
	return e.emulator.SeekDown()
}

// SeekOffset satisfies component.Scrollable.
func (e *Handler) SeekOffset() int {
	return e.emulator.SeekOffset()
}

// MaxSeekOffset satisfies component.Scrollable.
func (e *Handler) MaxSeekOffset() int {
	return e.emulator.MaxSeekOffset()
}

func (h *Handler) init(
	publisher browser.EventPublisher, notifications browser.Notifications,
	e schemeapi.Executor, t schemeapi.Terminal, tm browser.TabManager,
	cmdAndArgs []string, maxWidth int, opts ...Option,
) error {
	config := defaultConfig()
	for _, o := range opts {
		o(&config)
	}
	if len(cmdAndArgs) == 0 {
		return errors.New("empty command and args")
	}
	ch, interrupter := h.initState(publisher, cmdAndArgs, maxWidth, config)
	return h.initEmulator(publisher, notifications, e, t, tm, interrupter, ch)
}

func (h *Handler) initState(
	publisher browser.EventPublisher,
	cmdAndArgs []string, maxWidth int, config handlerConfig,
) (chan error, term.Interrupter) {
	if h.cancelCtx != nil {
		panic("tried to initialize already initialized plugin.Handler")
	}

	title := config.title
	if config.title == "" {
		title = strings.Join(cmdAndArgs, " ")
	}
	h.title = title
	if config.noBarCommand {
		layout := make([]BarComponent, 0, len(config.bar.Layout))
		for _, c := range config.bar.Layout {
			if c.Type == BarCommand {
				continue
			}
			layout = append(layout, c)
		}
		config.bar.Layout = layout
	}

	interrupter := browser.EventPublisherInterrupter(publisher)
	h.interactiveWidth = int(float64(maxWidth) * 0.8)
	h.interactiveHeight = h.interactiveWidth * 9 / 16
	h.nonInteractiveMinWidth = int(math.Max(float64(maxWidth)*0.2, float64(len(title)+4)*2))
	h.nonInteractiveMinHeight = h.nonInteractiveMinWidth * 9 / 16

	ch := make(chan error)

	topBar := newPluginHandlerBar(title, interrupter, config.bar)
	h.bar = topBar

	h.frame = config.frame
	h.frameCharSet = config.frameCharSet

	config.cfg.CommandAndArgs = cmdAndArgs
	config.cfg.WidthHint = h.interactiveWidth
	config.cfg.HeightHint = h.interactiveHeight
	if config.cfg.Watcher != nil {
		config.cfg.Watcher = workspaceapi.MultiProcessWatcher(
			config.cfg.Watcher, workspaceapi.ChanProcessWatcher(ch))
	} else {
		config.cfg.Watcher = workspaceapi.ChanProcessWatcher(ch)
	}
	h.cfg = config.cfg

	return ch, interrupter
}

func (e *Handler) initEmulator(
	publisher browser.EventPublisher, notifications browser.Notifications,
	executor schemeapi.Executor, t schemeapi.Terminal, tm browser.TabManager,
	interrupter term.Interrupter, ch chan error,
) error {
	ctx, cancel := context.WithCancel(context.Background())
	e.cancelCtx = cancel

	vteh, err := vte.NewHandler(publisher, notifications, t, executor, tm, e.cfg)
	if err != nil {
		cancel()
		return fmt.Errorf("new vte: %w", err)
	}
	e.emulator = vteh
	e.liveHandler = e.newUnion(e.emulator)

	go debug.CapturePanicReport(func() {
		term.InterruptAt(ctx, interrupter, 1)
	})
	e.watcherWG.Add(1)
	go debug.CapturePanicReport(func() {
		var err error

		defer func() {
			defer e.watcherWG.Done()
			e.bar.setDone(err)
			cancel()
		}()

		select {
		case err = <-ch:
		case <-ctx.Done():
			err = ctx.Err()
		}
	})
	return nil
}

func (e *Handler) shouldExit(ev term.Event) (exit bool) {
	if ev.Type != term.EventKey {
		return
	}
	if ev.Ch == 'c' && ev.Mod == term.ModCtrl {
		exit = true
		return
	}
	if ev.Key == term.KeyEsc {
		e.exitKey++
		if e.exitKey >= 2 && time.Since(e.lastExitKey) < exitKeyRepeatTimeout {
			exit = true
			return
		}
		e.lastExitKey = time.Now()
	} else {
		e.exitKey = 0
	}
	return
}

func (e *Handler) handler() tui.Handler {
	e.bar.mu.Lock()
	isDone := e.bar.done
	e.bar.mu.Unlock()
	if !isDone {
		return e.liveHandler
	}

	if e.doneHandler == nil {
		e.initializeDoneHandler()
	}

	return e.doneHandler
}

func (e *Handler) initializeDoneHandler() {
	if e.emulator.Component().UsedAlternateBuffer() {
		// ensure that doneHandler is not nil; liveHandler will return
		// exit on the next call to Handle
		e.doneHandler = e.liveHandler
		return
	}
	snap, err := e.emulator.Component().Snapshot()
	if err != nil {
		return
	}
	buf := cell.CellsToBuffer(snap.Active().Cells)

	uri := e.emulator.Component().URI()

	clipboard := nullReplaceClipboard{root: e.cfg.Clipboard}
	var main text.Handler
	if e.cfg.Modal {
		main = vi.NewWithIndent(buf, uri, text.IndentRuneTab, 0,
			vi.WithResAttr(e.cfg.SelectionAttributes),
			vi.WithAttr(e.cfg.Attributes),
			vi.WithWrap(false),
			vi.WithTabspaces(1),
			vi.WithClipboard(clipboard),
		)
	} else {
		main = standard.NewHandler(buf, uri, text.IndentRuneTab, 0,
			standard.WithResAttr(e.cfg.SelectionAttributes),
			standard.WithAttr(e.cfg.Attributes),
			standard.WithWrap(false),
			standard.WithTabspaces(1),
			standard.WithCommandBar(true),
			standard.WithClipboard(clipboard),
		)
	}

	e.doneHandler = e.newUnion(main)
	e.doneHandler.Resize(e.width, e.height)
	main.SetCursorAtScroll(e.emulator.Component().CursorAtScroll())
}

func (e *Handler) newUnion(unionMain tui.Handler) tui.Handler {
	union := handler.NewFrameUnion(unionMain)
	union.Frame = false
	if e.bar.AlignBottom {
		union.UnionBottom(handler.NopFromComponent(e.bar), barHeight)
	} else {
		union.UnionTop(handler.NopFromComponent(e.bar), barHeight)
	}
	return union
}
