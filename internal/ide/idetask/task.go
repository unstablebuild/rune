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

package idetask

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ernestrc/logd-go/logging"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/plugin"
	"unstable.build/rune/internal/text/cmdenv"
)

const (
	colorSuccess    = term.ColorGreen
	colorRunning    = term.ColorGray
	colorError      = term.ColorRed
	colorPaused     = term.ColorYellow
	minimizePadding = 0
)

var _ browser.ScrollableFloating = (*Task)(nil)

// Task represents a task that runs a command in response to changes to files.
// Manager.Run must be called before a task can be rendered.
//
// A Task must not be copied after first use.
type Task struct {
	// Name of the task.
	Name string
	// Filter is used as an inclusive pattern match.
	Filter string
	// Cmd is the command to run.
	Cmd string
	// Args are the args to pass to the command.
	Args []string

	// MinimizeAlignment determines the alignment to use
	// when a task's window is minimized.
	MinimizeAlignment component.Alignment

	// Runs represents the number of times this task has been run.
	runs int

	b                Browser
	watchID          int
	watchCancel      func()
	ctx              context.Context
	cancelCtx        func()
	closeHook        func()
	doneWaitCh       chan struct{}
	mu               *sync.Mutex
	win              browser.Window
	tab              *browser.Tab
	cmdAndArgs       []string
	pluginOpts       []plugin.Option
	donech           chan error
	newPlugin        ProgramBuilder
	scheduleNextTick func(fn func()) bool
	// inflightWG tracks in-progress processing of a donech event.
	// The donech receiver Adds(1) immediately after pulling an error
	// off the channel and Dones() once the resulting state transition
	// (setError/setSuccess and any restart/pause follow-up) has been
	// applied. Tests use WaitInflight to observe the settled handler.
	inflightWG *sync.WaitGroup
	// spawnDone (on mu) is broadcast when an in-flight plugin spawn
	// goroutine has installed its result and cleared spawning.
	spawnDone *sync.Cond

	bar              tui.Component
	barColor         term.Color
	defaultFrameAttr term.Attributes
	focusFrameAttr   term.Attributes
	width            int
	maxWidth         int
	minWidth         int
	minHeight        int
	height           int
	running          bool
	spawning         bool
	paused           bool
	restartPending   bool
	loopHalted       bool
	lastExit         error
	lastStart        time.Time
	lastDuration     time.Duration
	handler          browser.ScrollableFloating
	scheme           schemeapi.Scheme
	terminal         schemeapi.Terminal
	// don't use mu to check if closed, so StopTask, followed by
	// browser close handler doesn't deadlock
	closed *atomic.Bool
}

// TaskInfo is an immutable snapshot of the status of a task.
type TaskInfo struct {
	Name                     string
	Filter                   string
	CmdAndArgs               []string
	MinimizeAlignment        component.Alignment
	WindowID                 uint64
	WindowMinimized          bool
	WindowMinimizedAlignment component.Alignment
	Running                  bool
	LastSuccess              bool
	LoopHalted               bool
	// Runs represents the number of times this task has been run.
	Runs         int
	LastDuration time.Duration
}

// Info returns an immutable snapshot of this Task.
func (t *Task) Info() TaskInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	minimizedAlignment, minimized := t.win.IsMinimized()

	return TaskInfo{
		Name:                     t.Name,
		Filter:                   t.Filter,
		CmdAndArgs:               append([]string(nil), t.cmdAndArgs...),
		MinimizeAlignment:        t.MinimizeAlignment,
		WindowID:                 t.win.WindowID(),
		WindowMinimized:          minimized,
		WindowMinimizedAlignment: minimizedAlignment,
		Running:                  t.running,
		Runs:                     t.runs,
		LastSuccess:              t.lastExit == nil,
		LoopHalted:               t.loopHalted,
		LastDuration:             t.lastDuration,
	}
}

// Handle satisfies tui.Handler.
func (t *Task) Handle(ev term.Event) (exit, handled bool) {
	if isCtrlC(ev) {
		t.pause()
		return
	}
	if isCtrlR(ev) {
		t.restart()
		handled = true
		return
	}
	_, handled = t.currentHandler().Handle(ev)
	return
}

// currentHandler returns the active handler under the lock so render and
// event accessors never race the donech goroutine swapping t.handler.
func (t *Task) currentHandler() browser.ScrollableFloating {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.handler
}

// StatusURI returns the uri the program of the current run reports its
// statuses under, or false before a run started.
func (t *Task) StatusURI() (workspaceapi.URI, bool) {
	r, ok := t.currentHandler().(interface{ StatusURI() workspaceapi.URI })
	if !ok {
		return workspaceapi.URI{}, false
	}
	uri := r.StatusURI()
	return uri, uri.String() != ""
}

// Program returns the handler of the current run, which t draws
// unless minimized.
func (t *Task) Program() tui.Handler {
	return t.currentHandler()
}

// Shown returns the window showing t whole: its tab's, or its own
// while not minimized. It returns false while t is out of sight.
func (t *Task) Shown() (browser.Window, bool) {
	t.mu.Lock()
	win, tab := t.win, t.tab
	t.mu.Unlock()
	if tab != nil {
		if w, ok := tab.Window(); ok && !w.Closed() {
			return w, true
		}
	}
	if win.Closed() {
		return nil, false
	}
	if _, minimized := win.IsMinimized(); minimized {
		return nil, false
	}
	return win, true
}

// Cursor satisfies tui.Handler.
func (t *Task) Cursor() (c term.Coordinates, s term.CursorStyle, show bool) {
	return t.currentHandler().Cursor()
}

// Selection satisfies tui.Handler.
func (t *Task) Selection() (string, bool) {
	return t.currentHandler().Selection()
}

// Draw satisfies tui.Component.
func (t *Task) Draw(w term.Writer) {
	if _, is := t.win.IsMinimized(); is {
		t.bar.Draw(w)
		return
	}
	t.currentHandler().Draw(w)
}

// Dimensions satisfies browserapi.Floating.
func (t *Task) Dimensions() (width int, height int) {
	width, height = t.currentHandler().Dimensions()
	width = int(math.Max(
		float64(width),
		float64(t.minWidth)),
	)
	height = int(math.Max(
		float64(height),
		float64(t.minHeight)),
	)
	return
}

// Resize satisfies tui.Component.
func (t *Task) Resize(width, height int) {
	if t.closed.Load() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	t.width = width
	t.height = height
	t.bar.Resize(width, height)
	if t.handler != nil {
		t.handler.Resize(width, height)
	}
}

// MaxSeekOffset satisfies component.Scrollable.
func (e *Task) MaxSeekOffset() int {
	return e.currentHandler().MaxSeekOffset()
}

// SeekDown satisfies component.Scrollable.
func (e *Task) SeekDown() bool {
	return e.currentHandler().SeekDown()
}

// SeekOffset satisfies component.Scrollable.
func (e *Task) SeekOffset() int {
	return e.currentHandler().SeekOffset()
}

// SeekUp satisfies component.Scrollable.
func (e *Task) SeekUp() bool {
	return e.currentHandler().SeekUp()
}

// Close satisfies browserapi.Handler. It stops the task but leaves its
// window, which may already show other content.
func (t *Task) Close() error {
	t.closeHook()
	return t.doClose()
}

func calcMinSize(width, height int) (int, int) {
	const (
		factor    = 4
		minWidth  = 8
		minHeight = 5
	)
	return int(math.Max(float64(width/factor), minWidth)),
		int(math.Max(float64(height/factor), minHeight))
}

func (t *Task) doClose() (ret error) {
	if !t.closed.CompareAndSwap(false, true) {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.cancelCtx()

	if t.handler != nil {
		ret = t.handler.Close()
	}
	return
}

// closeView removes the window or tab that shows t.
func (t *Task) closeView() error {
	if t.tab != nil {
		return t.b.RemoveTab(t.tab)
	}
	if !t.win.Closed() {
		return t.win.Close()
	}
	return nil
}

func (t *Task) init(
	ctx context.Context, b Browser,
	scheme schemeapi.Scheme, terminal schemeapi.Terminal,
	newPlugin ProgramBuilder,
	maxWidth, maxHeight int, closeHook func(),
	scheduleNextTick func(func()) bool,
	pluginOpts ...plugin.Option,
) (context.Context, func(), error) {
	t.closeHook = closeHook
	t.b = b
	t.scheme = scheme
	t.terminal = terminal
	t.maxWidth = maxWidth
	t.minWidth, t.minHeight = calcMinSize(maxWidth, maxHeight)
	t.mu = new(sync.Mutex)
	t.inflightWG = new(sync.WaitGroup)
	t.spawnDone = sync.NewCond(t.mu)
	t.scheduleNextTick = scheduleNextTick
	t.newPlugin = newPlugin
	t.pluginOpts = pluginOpts
	t.donech = make(chan error)
	t.pluginOpts = append(t.pluginOpts, plugin.WithProcessWatcher(
		workspaceapi.ChanProcessWatcher(t.donech),
	))
	t.closed = new(atomic.Bool)
	t.doneWaitCh = make(chan struct{})
	t.cmdAndArgs = append([]string{t.Cmd}, t.Args...)
	t.bar = tcomponent.WithAttrSetter(component.NewString(""))
	// always have a valid handler so methods do not need to check for nil
	t.handler = browser.NopScrollableFloatingHandler(handler.NopScrollableFloating())
	ctx, cancel := context.WithCancel(ctx)
	t.cancelCtx = cancel
	t.ctx = ctx
	t.win = noopWindow{} // placeholder to avoid panics or checking for nil

	prev, err := b.Focus()
	if err != nil {
		cancel()
		return context.Background(), nil, err
	}
	alignment := t.MinimizeAlignment
	switch t.MinimizeAlignment {
	case component.AlignmentBottom:
		alignment |= component.AlignmentHorizontallyCentered
	case component.AlignmentLeft:
		alignment |= component.AlignmentVerticallyCentered
	case component.AlignmentTop:
		alignment |= component.AlignmentHorizontallyCentered
	case component.AlignmentRight:
		alignment |= component.AlignmentVerticallyCentered
	}
	win, err := b.Floating(t, browserapi.FloatingConfig{
		Alignment: alignment,
	})
	if err != nil {
		cancel()
		return context.Background(), nil, fmt.Errorf("new floating window: %w", err)
	}
	t.win = win

	// process exit errors
	go debug.CapturePanicReport(func() {
		defer close(t.doneWaitCh)
		for {
			select {
			case err := <-t.donech:
				t.inflightWG.Add(1)
				t.mu.Lock()
				paused := t.paused
				restarting := t.restartPending
				t.restartPending = false
				t.mu.Unlock()
				if err != nil {
					t.setError(err)
				} else {
					t.setSuccess()
				}
				if restarting {
					t.tryRunning(t.b, t.scheme, t.terminal, "  task")
				} else if paused {
					t.setPause()
				}
				t.inflightWG.Done()
			case <-t.ctx.Done():
				t.setError(t.ctx.Err())
				return
			}
		}
	})

	t.tryRunning(b, scheme, terminal, "")
	t.mu.Lock()
	t.minimize()
	t.mu.Unlock()
	_, err = b.SetFocus(prev)
	if err != nil {
		cancel()
		return context.Background(), nil, fmt.Errorf("set focus: %w", err)
	}
	return ctx, cancel, nil
}

func (t *Task) tryRunning(
	b browser.Browser, scheme schemeapi.Scheme,
	terminal schemeapi.Terminal, reason string,
) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.log(log.TraceLevel, "attempt to run task, reason: %s", reason)

	if t.running || t.spawning {
		return false
	}
	t.spawning = true

	var title strings.Builder
	if reason != "" {
		title.WriteString(reason)
		title.WriteString("  ")
	}
	title.WriteString(t.Cmd)
	for _, arg := range t.Args {
		title.WriteString(" ")
		title.WriteString(arg)
	}
	opts := append([]plugin.Option{}, t.pluginOpts...)
	opts = append(opts, plugin.WithTitle(title.String()))
	// The run counts from spawn initiation (as it did when the spawn
	// was synchronous): a run that gets loop-halted or closed while
	// the plugin is still being built must still be observable via
	// Info().Runs.
	t.runs++
	t.lastStart = time.Now()
	// Immediate feedback: paint the bar as running while the spawn is
	// in flight, matching the previously synchronous behavior.
	t.barColor = colorRunning
	t.setBarColor(t.barColor)
	// The plugin build blocks on transport RPCs (NewPty/StartCommand)
	// which can stall on a slow or wedged remote workspace, and
	// tryRunning runs on the host event loop for session restores and
	// user commands. Build the handler off the caller's goroutine and
	// install it when it settles.
	// The vte field-splits the command line against the editor host's
	// environment, so anything needing shell interpretation (`~`,
	// `$VAR`, pipes, globs) must be routed through the workspace's own
	// shell instead. On a remote workspace, expanding here would
	// resolve paths against the wrong machine.
	cmdAndArgs := cmdenv.BuildPluginArgv(t.cmdAndArgs)
	maxWidth := t.maxWidth
	go debug.CapturePanicReport(func() {
		h, err := t.newPlugin(b, b, scheme, terminal, b,
			cmdAndArgs, maxWidth, opts...)
		t.installSpawned(h, err)
	})
	return true
}

// installSpawned installs the plugin handler built by the tryRunning
// spawn goroutine, or the spawn error when the build failed. A task
// that was closed or loop-halted while the spawn was in flight
// abandons the handler.
func (t *Task) installSpawned(h browser.ScrollableFloating, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.spawning = false
	t.spawnDone.Broadcast()
	if t.closed.Load() || t.loopHalted {
		// Only a successful build hands over an owned, fully
		// initialized handler; closing anything else is unsafe.
		if err == nil && h != nil {
			_ = h.Close()
		}
		return
	}
	if err != nil {
		_ = t.handler.Close()
		t.handler = browser.NopScrollableFloatingHandler(
			handler.NopScrollableFloatingFromComponent(
				component.NewResponsiveString(
					err.Error(),
					component.StringResponsiveConfig{
						NoSplitWords: true,
						StringConfig: component.StringConfig{
							Alignment: component.AlignmentCentered,
						},
					},
				),
			))
		t.doSetError(err)
		return
	}
	h.Resize(t.width, t.height)
	t.setRunning(h)
}

func (t *Task) setRunning(h browser.ScrollableFloating) {
	_ = t.handler.Close()
	t.running = true
	t.paused = false
	t.restartPending = false
	t.handler = h
	t.barColor = colorRunning
	t.setBarColor(t.barColor)
}

func (t *Task) setError(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.loopHalted {
		return
	}
	t.doSetError(err)
}

func (t *Task) doSetError(err error) {
	t.lastDuration = time.Since(t.lastStart)
	t.lastExit = err
	t.running = false
	t.barColor = colorError
	t.setBarColor(t.barColor)
}

func (t *Task) haltLoop(file string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.haltWith(fmt.Errorf(
		"%q keeps retriggering this task, creating an infinite loop. "+
			"The task filter is an inclusive, comma-separated list of glob "+
			"patterns: only matching files retrigger the task. Recreate the "+
			"task with a filter that matches your source files but not %q, "+
			"e.g. filter \"*.go,src/**\"", file, file))
}

func (t *Task) watchFailed(watchErr error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.loopHalted {
		return
	}
	t.haltWith(fmt.Errorf(
		"the workspace file watch could not be started, so this task will no "+
			"longer re-run automatically when files change. Recreate the task "+
			"to restore it: %w", watchErr))
}

func (t *Task) watchEnded() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.loopHalted {
		return
	}
	t.haltWith(errors.New(
		"the workspace file watch ended, so this task will no longer " +
			"re-run automatically when files change. Recreate the task " +
			"to restore it"))
}

// haltWith marks the task as permanently halted and replaces its handler
// with a centered message rendering err, so the task can no longer
// auto-rerun and the user is shown why. Callers must hold t.mu.
func (t *Task) haltWith(err error) {
	t.loopHalted = true
	h := browser.NopScrollableFloatingHandler(
		handler.NopScrollableFloatingFromComponent(
			component.NewResponsiveString(
				err.Error(),
				component.StringResponsiveConfig{
					StringConfig: component.StringConfig{
						Alignment: component.AlignmentCentered,
					},
				},
			),
		))
	h.Resize(t.width, t.height)
	_ = t.handler.Close()
	t.handler = h
	t.doSetError(err)
}

func (t *Task) setSuccess() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.loopHalted {
		return
	}
	t.lastDuration = time.Since(t.lastStart)
	t.lastExit = nil
	t.running = false
	t.barColor = colorSuccess
	t.setBarColor(t.barColor)
}

// WaitInflight blocks until the task's in-flight plugin spawn
// goroutine has installed its result, then until the installed
// handler's own async vte spawn goroutine and watcher hand-off have
// completed. Used by tests to settle task state before asserting
// rendered output.
func (t *Task) WaitInflight() {
	t.mu.Lock()
	for t.spawning {
		t.spawnDone.Wait()
	}
	h := t.handler
	t.mu.Unlock()
	if w, ok := h.(interface{ WaitInflight() }); ok {
		w.WaitInflight()
	}
}

func (t *Task) pause() {
	t.mu.Lock()
	defer t.mu.Unlock()

	_ = t.handler.Close()
	t.paused = true
}

func (t *Task) restart() {
	t.mu.Lock()
	t.restartPending = true
	t.mu.Unlock()
	t.pause()
	t.tryRunning(t.b, t.scheme, t.terminal, "  task")
}

func (t *Task) setPause() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.barColor = colorPaused
	t.setBarColor(t.barColor)
}

// setBarColor paints the minimized task frame background with the given status
// color while preserving the configured non-focus frame Fg/Attrs.
func (t *Task) setBarColor(color term.Color) {
	t.scheduleNextTick(func() {
		if _, is := t.win.IsMinimized(); is && !t.win.Closed() {
			attr := t.defaultFrameAttr
			attr.Bg = color
			t.win.SetFrameAttr(attr)
		}
		t.setTabColor(color)
	})
}

func (t *Task) setTabColor(color term.Color) {
	if t.tab != nil {
		t.tab.SetAttrs(t.Name, term.Attributes{Fg: color})
	}
}

// OnFocus satisfies browser.TabSubscriber.
func (t *Task) OnFocus(tab *browser.Tab) {
	// task was converted to tab; assign and
	// ensure that attributes are "cleared"
	t.tab = tab
	t.tab.ResetAttrs()
}

// OnFree satisfies browser.TabSubscriber.
func (t *Task) OnFree(tab *browser.Tab) {
}

func isCtrlC(ev term.Event) bool {
	return ev.Type == term.EventKey &&
		(ev.Ch == 'c' || ev.Ch == 'C') && ev.Mod == term.ModCtrl
}

func isCtrlR(ev term.Event) bool {
	return ev.Type == term.EventKey &&
		(ev.Ch == 'r' || ev.Ch == 'R') && ev.Mod == term.ModCtrl
}

func (t *Task) setMaxWidthHeight(width, height int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.maxWidth = width
	t.minWidth, t.minHeight = calcMinSize(width, height)
	t.minHeight = height / 8
}

func (t *Task) onFocus() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed.Load() {
		return
	}
	t.unminimize()
}

func (t *Task) onUnfocus() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed.Load() || t.win.Closed() {
		return
	}
	t.minimize()
}

func (t *Task) unminimize() {
	if t.win.Closed() {
		return
	}
	t.win.Unminimize()
	t.win.SetFrameAttr(t.focusFrameAttr)
	t.scheduleNextTick(func() { t.setTabColor(term.ColorDefault) })
}

func (t *Task) minimize() {
	if t.win.Closed() {
		return
	}
	switch t.MinimizeAlignment {
	case component.AlignmentTop:
		t.win.MinimizeUp(minimizePadding)
	case component.AlignmentBottom:
		t.win.MinimizeDown(minimizePadding)
	case component.AlignmentLeft:
		t.win.MinimizeLeft(minimizePadding)
	default:
		t.win.MinimizeRight(minimizePadding)
	}
	t.setBarColor(t.barColor)
}

func (t *Task) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithFields(log.Fields{
		logging.KeyClass: "idetask.task",
		"name":           t.Name,
		"cmd":            t.Cmd,
		"args":           strings.Join(t.Args, " "),
		"filter":         t.Filter,
	}).Logf(level, msg, args...)
}

// ProgramBuilder starts the program of a run of a task and returns its
// handler, or nil and an error.
type ProgramBuilder func(
	publisher browser.EventPublisher, notifications browser.Notifications,
	e schemeapi.Executor, t schemeapi.Terminal, tm browser.TabManager,
	cmdAndArgs []string, maxWidth int, opts ...plugin.Option,
) (browser.ScrollableFloating, error)
