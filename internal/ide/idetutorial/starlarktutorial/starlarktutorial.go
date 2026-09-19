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

// Package starlarktutorial parses a starlark tutorial DSL program and
// runs it as an in-process [idetutorial.Tutorial]. The DSL exposes a
// single `tutorial(entry=fn)` registration plus a set of blocking
// builtins (floating_window, wait_command, choice, ...) that authors
// invoke from a regular Starlark function. The entry function runs on
// a dedicated goroutine; each blocking builtin posts a request to the
// TUI loop, waits for a response, and returns a real Starlark value
// so authors can branch, loop, and compose helpers naturally.
// NOTE: this package needs heavy refactoring, it's currently a pile
// of AI slop.
package starlarktutorial

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"

	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/ide/idetutorial"
	"unstable.build/rune/internal/text"
)

// errStopped is returned from a blocking builtin when the Tutorial's
// run context is cancelled. The runLoop recognises it as a clean exit
// path and does not surface a notification.
var errStopped = errors.New("starlarktutorial: stopped")

// errExitRequested is raised by the exit() builtin to terminate the
// entry function from any depth. The runLoop unwraps it from a
// starlark.EvalError and treats it as a clean exit.
var errExitRequested = errors.New("starlarktutorial: exit requested")

// CommandManualLookup resolves a registered command name to its
// manual, so the wait_command hint window can render the command's
// synopsis and description. ok=false when no command (or alias)
// matches name. Implementations may be called from the TUI loop in
// Tutorial.Draw; they must be safe to call concurrently and return
// quickly.
type CommandManualLookup func(name string) (command.Manual, bool)

// Tutorial is the runtime state of a single starlark-defined tutorial.
// It satisfies [idetutorial.Tutorial].
type Tutorial struct {
	name    string
	title   string
	id      string
	version string

	// winOverlay hosts the tutorial's step windows on a dedicated
	// browser component drawn above the whole IDE root. Windows are
	// opened at publish time (on the run goroutine) and closed on
	// resolve/Reset/Stop; OverlayBrowser's internal mutex makes that
	// safe. Its lock is a leaf: never hold t.mu while calling into
	// it, and never block on respond/barrier channels while inside.
	winOverlay       *idetutorial.OverlayBrowser
	editor           text.Editor
	notifications    browserapi.Notifications
	parser           syntaxapi.Parser
	defaultAttr      term.Attributes
	scheduleNextTick func(func()) bool
	storage          storageapi.Service
	// commandKeyDisplay is the prettified, display-ready command-prompt
	// key spec (e.g. ":" rather than "<shift-;>"). It is the single
	// rendered form every tutorial surface uses; nothing re-renders the
	// raw term.KeyComb, so a new render site cannot reintroduce the ugly
	// spec.
	commandKeyDisplay string
	// editorMode is the user's resolved editor mode ("modal",
	// "standard", or "emacs"), exposed to the DSL via editor_mode(). exo is
	// resolved to its fallback by the host before New.
	editorMode string
	// os is the host operating system (runtime.GOOS), exposed to the
	// DSL via os(). Tutorials branch on it to teach OS-specific flows
	// such as the native macOS menu bar on "darwin".
	os string
	// keyForCommand resolves a command (and optional args) to the
	// user's configured key spec, or "" when unbound. nil disables
	// key_for() lookups (they return ""). Used by key_for().
	keyForCommand func(cmd string, args []string) string
	// commandManualLookup resolves a command name to its registered
	// manual, used by the wait_command hint window so the user sees
	// the command's synopsis and description while the request is
	// armed. nil disables manual rendering: the hint falls back to a
	// plain prefix line.
	commandManualLookup CommandManualLookup

	// workspaceOpen reports whether a project workspace is open in the
	// focused slot, exposed to the DSL via workspace_open(). nil makes
	// the builtin return True so tutorials/tests without the wiring are
	// not spuriously blocked.
	workspaceOpen func() bool

	// lspServerRunning reports whether the focused workspace has at
	// least one live, initialized LSP server, exposed to the DSL via
	// is_lsp_server_running(). nil makes the builtin return True so
	// tutorials/tests without the wiring are not spuriously blocked.
	lspServerRunning func() bool

	// parsed entry function. Set once at New time.
	entry *starlark.Function

	// Runtime state. mu guards everything below.
	mu        sync.Mutex
	width     int
	height    int
	active    *request
	runCtx    context.Context
	cancel    context.CancelFunc
	thread    *starlark.Thread
	runDone   chan struct{}
	finished  bool
	completed bool

	// stepCount is the count of "visible content" requests
	// published so far (reqFloatingWindow / reqMarkdown). Each
	// publish snapshots this value into request.stepNum. Reset()
	// zeroes it so re-running the tutorial restarts at Step 1.
	stepCount int

	// firstSignal is set by Reset to a one-shot signal that the
	// runtime has made user-visible progress (posted its first
	// blocking request, scheduled its first side effect, or exited).
	// Reset waits on it so subsequent Draw/Handle/Close observe a
	// stable state — and so a test that drives input immediately
	// after Reset never races with the run goroutine.
	firstSignal func()

	// overlay positions the active step's box on screen and renders
	// it in local coordinates. ComponentAt hit-tests against the
	// same geometry the last Draw painted so the host can tell what
	// the overlay covers. Only the TUI loop touches it.
	overlay component.Virtual[*overlayComponent]
}

var _ idetutorial.Tutorial = (*Tutorial)(nil)

// New parses src as a starlark tutorial DSL program and returns a
// runnable Tutorial. The program must call `tutorial(entry=fn)`
// exactly once at top level; fn becomes the entry function the
// Starlark thread executes on Reset. name is the registry name used
// for error reporting and as the default id. The remaining arguments
// are the host services the DSL builtins resolve at runtime.
func New(
	name, src string,
	overlay *idetutorial.OverlayBrowser,
	ed text.Editor,
	notifications browserapi.Notifications,
	parser syntaxapi.Parser,
	defaultAttr term.Attributes,
	scheduleNextTick func(func()) bool,
	storage storageapi.Service,
	commandKey term.KeyComb,
	editorMode string,
	os string,
	keyForCommand func(cmd string, args []string) string,
	commandManualLookup CommandManualLookup,
	workspaceOpen func() bool,
	lspServerRunning func() bool,
) (*Tutorial, error) {
	if src == "" {
		return nil, errors.New("starlarktutorial: empty source")
	}
	t := &Tutorial{
		name:                name,
		winOverlay:          overlay,
		editor:              ed,
		notifications:       notifications,
		parser:              parser,
		defaultAttr:         defaultAttr,
		scheduleNextTick:    scheduleNextTick,
		storage:             storage,
		commandKeyDisplay:   PrettyKeySpec(commandKey.String()),
		editorMode:          editorMode,
		os:                  os,
		keyForCommand:       keyForCommand,
		commandManualLookup: commandManualLookup,
		workspaceOpen:       workspaceOpen,
		lspServerRunning:    lspServerRunning,
	}
	t.overlay.C = &overlayComponent{}

	if err := t.parse(src); err != nil {
		return nil, fmt.Errorf("starlarktutorial %q: %w", name, err)
	}
	if t.entry == nil {
		return nil, fmt.Errorf("starlarktutorial %q: tutorial() never called",
			name)
	}
	return t, nil
}

// parse runs the registration phase: a Starlark module that defines
// `tutorial(entry=fn, ...)` and stops. Blocking-UI builtins are
// exposed but raise an error if invoked at parse time so authors get
// a clear message if they call them outside the entry function.
func (t *Tutorial) parse(src string) error {
	thread := &starlark.Thread{
		Name:  t.name + ".star",
		Print: func(*starlark.Thread, string) {},
		Load: func(_ *starlark.Thread, module string) (starlark.StringDict, error) {
			return nil, fmt.Errorf("load() is not allowed: cannot load %q",
				module)
		},
	}
	opts := &syntax.FileOptions{
		TopLevelControl: true,
		GlobalReassign:  true,
		Recursion:       true,
	}
	predeclared := builtins(t)
	_, err := starlark.ExecFileOptions(opts, thread, t.name+".star",
		[]byte(src), predeclared)
	if err != nil {
		var evalErr *starlark.EvalError
		if errors.As(err, &evalErr) {
			return fmt.Errorf("starlark: %s", evalErr.Backtrace())
		}
		return fmt.Errorf("starlark: %w", err)
	}
	return nil
}

// Name returns the registry name passed to New.
func (t *Tutorial) Name() string { return t.name }

// Title returns the title declared by the DSL, falling back to Name
// when no title was provided.
func (t *Tutorial) Title() string {
	if t.title != "" {
		return t.title
	}
	return t.name
}

// ID returns the stable tutorial ID, falling back to Name when no id
// was provided.
func (t *Tutorial) ID() string {
	if t.id != "" {
		return t.id
	}
	return t.name
}

// Version returns the tutorial version string, or "" when not
// provided.
func (t *Tutorial) Version() string { return t.version }

// Completed reports whether the most recent run reached a normal return.
func (t *Tutorial) Completed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.completed
}

// Reset stops any in-progress run and starts a fresh Starlark thread
// that calls the entry function on its own goroutine. Reset returns
// after launching the goroutine; Draw/Handle observe an empty active
// slot until the entry posts its first blocking request, at which
// point the slot is filled atomically.
func (t *Tutorial) Reset() {
	t.Stop()
	t.mu.Lock()
	t.finished = false
	t.completed = false
	t.active = nil
	t.stepCount = 0
	t.runCtx, t.cancel = context.WithCancel(context.Background())
	t.thread = &starlark.Thread{
		Name:  t.name + ".run",
		Print: func(*starlark.Thread, string) {},
		Load: func(_ *starlark.Thread, module string) (starlark.StringDict, error) {
			return nil, fmt.Errorf("load() is not allowed: cannot load %q",
				module)
		},
	}
	t.runDone = make(chan struct{})
	ready := make(chan struct{})
	var once sync.Once
	t.firstSignal = func() { once.Do(func() { close(ready) }) }
	t.mu.Unlock()
	t.runLoop()
	<-ready
}

// Stop tears down any in-progress run and unblocks the Starlark
// thread. Stop is safe to call multiple times and on a tutorial that
// never ran. Stop waits for the run goroutine to exit before
// returning.
func (t *Tutorial) Stop() {
	t.mu.Lock()
	cancel := t.cancel
	thread := t.thread
	done := t.runDone
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if thread != nil {
		thread.Cancel("stopped")
	}
	if done != nil {
		<-done
	}
	t.mu.Lock()
	t.cancel = nil
	t.thread = nil
	t.runDone = nil
	t.active = nil
	t.completed = false
	t.mu.Unlock()
}

// runLoop starts the goroutine that calls the entry function and
// returns immediately. The goroutine drives request publication and
// completion; Stop() waits for it to exit.
func (t *Tutorial) runLoop() {
	t.mu.Lock()
	if t.entry == nil || t.thread == nil {
		t.finished = true
		if t.runDone != nil {
			close(t.runDone)
			t.runDone = nil
		}
		t.mu.Unlock()
		return
	}
	thread := t.thread
	entry := t.entry
	done := t.runDone
	t.mu.Unlock()

	go func() {
		defer close(done)
		_, err := starlark.Call(thread, entry, nil, nil)
		t.handleRunResult(err)
	}()
}

// handleRunResult finalises a run: marks the tutorial finished and
// surfaces any non-cancellation error via the notifications service.
//
// The run context is kept live across the error notification so a
// concurrent Stop (which cancels it) can abort the finalizing runOnTUI
// instead of deadlocking: Stop waits for this goroutine to exit while
// runOnTUI would otherwise wait for an event-loop tick that the
// Stop-wedged loop can never deliver. The context is cancelled and
// cleared only after the notification path returns.
func (t *Tutorial) handleRunResult(err error) {
	t.mu.Lock()
	t.finished = true
	t.completed = err == nil
	t.active = nil
	t.thread = nil
	signal := t.firstSignal
	t.firstSignal = nil
	t.mu.Unlock()
	if signal != nil {
		signal()
	}

	t.notifyRunError(err)

	t.mu.Lock()
	t.runCtx = nil
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
	t.mu.Unlock()
}

// notifyRunError surfaces a non-cancellation entry error via the
// notifications service. Clean exits (errStopped, exit()) produce no
// notification.
func (t *Tutorial) notifyRunError(err error) {
	if err == nil {
		return
	}
	if errors.Is(err, errStopped) {
		return
	}
	if isStarlarkExit(err) {
		return
	}
	var evalErr *starlark.EvalError
	if errors.As(err, &evalErr) {
		t.runOnTUI(func() {
			if t.notifications != nil {
				_, _ = t.notifications.Notify(browserapi.LevelError,
					"%s: %s", t.Title(), evalErr.Msg)
			}
		})
		return
	}
	t.runOnTUI(func() {
		if t.notifications != nil {
			_, _ = t.notifications.Notify(browserapi.LevelError,
				"%s: %v", t.Title(), err)
		}
	})
}

// isStarlarkExit reports whether err originated from the exit()
// builtin. The runLoop treats such errors as a clean exit.
func isStarlarkExit(err error) bool {
	if errors.Is(err, errExitRequested) {
		return true
	}
	var evalErr *starlark.EvalError
	if errors.As(err, &evalErr) && evalErr.Unwrap() != nil {
		return errors.Is(evalErr.Unwrap(), errExitRequested)
	}
	return false
}

// Shader returns the armed hint-pulse spec for the active
// floating_window step, derived from the live overlay-window
// geometry (the last content row above the bottom frame edge). The
// spec is cached until the window moves, resizes, or the theme
// changes, so the composing handler's value comparison only restages
// the pulse when the geometry actually changed. ok=false when the
// tutorial is done, no request is active, or the pulse is not armed.
func (t *Tutorial) Shader() (idetutorial.Shader, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished || t.active == nil || !t.active.hasShader {
		return idetutorial.Shader{}, false
	}
	r := t.active
	if r.kind != reqFloatingWindow || t.winOverlay == nil {
		return idetutorial.Shader{}, false
	}
	pos, width, height, ok := t.winOverlay.WindowRect(r.win)
	if !ok || width < 4 || height < 3 {
		return idetutorial.Shader{}, false
	}
	if r.shaderBuilt && pos == r.shaderPos &&
		width == r.shaderW && height == r.shaderH {
		return r.shaderSpec, true
	}
	hintX := pos.X + 1
	hintY := pos.Y + height - 2
	hintW := width - 2
	r.shaderSpec = idetutorial.Shader{
		Shader:   buildHintPulse(t.defaultAttr, hintX, hintY, hintW),
		Offset:   term.Coordinates{X: hintX, Y: hintY},
		Width:    hintW,
		Height:   1,
		FPS:      hintFPS,
		Duration: hintDuration,
	}
	r.shaderPos, r.shaderW, r.shaderH = pos, width, height
	r.shaderBuilt = true
	return r.shaderSpec, true
}

// SetDefaultAttributes updates the tutorial's view of the default
// terminal attributes and invalidates the active step's cached
// shader spec so the next reconcile rebuilds it with the new
// attributes.
func (t *Tutorial) SetDefaultAttributes(defAttr term.Attributes) {
	t.mu.Lock()
	t.defaultAttr = defAttr
	if t.active != nil {
		t.active.shaderBuilt = false
	}
	t.mu.Unlock()
}

// Resize records the most recent dimensions and forwards them to the
// overlay browser and to the active step's window content so its
// Dimensions heuristic tracks the screen; the window manager
// re-queries it on the next draw.
func (t *Tutorial) Resize(width, height int) {
	t.mu.Lock()
	t.width, t.height = width, height
	active := t.active
	t.mu.Unlock()
	if t.winOverlay != nil {
		t.winOverlay.Resize(width, height)
	}
	if active != nil && active.winContent != nil {
		active.winContent.setScreen(width, height)
	}
}

// Draw paints the reqMarkdown banner, the only step surface still
// rendered bespoke — every other step lives in a window on the
// overlay browser, drawn by the composing handler. Out-of-range
// writes are silently dropped. The only state Draw mutates is the
// overlay virtual geometry that backs ComponentAt.
func (t *Tutorial) Draw(w term.Writer) {
	t.mu.Lock()
	active := t.active
	width, height := t.width, t.height
	finished := t.finished
	t.mu.Unlock()
	// Cover nothing unless the per-kind draw claims geometry.
	t.overlay.Resize(0, 0)
	if finished || active == nil {
		return
	}
	if active.kind == reqMarkdown {
		drawBanner(&t.overlay, w, width, height, []string{active.text})
	}
}

// Handle advances the state machine on input. exit=true once the
// tutorial finishes or is dismissed. Before per-kind dispatch it
// reaps a step window the browser closed out from under the request
// (the window-bar ✕ click), resolving dismissible kinds with the
// per-kind dismissal response; wait_* steps stay armed with their
// hint gone, since only the awaited key/command/event resolves them.
func (t *Tutorial) Handle(ev term.Event) (bool, bool) {
	t.mu.Lock()
	active := t.active
	finished := t.finished
	t.mu.Unlock()
	if !finished && active != nil {
		if active.skipRequested.Load() {
			t.Stop()
			return t.exitState(), true
		}
		if active.winClosed.Load() {
			switch active.kind {
			case reqFloatingWindow, reqConfirm, reqChoice:
				t.resolve(active, dismissalOrPendingResponse(active))
				return t.exitState(), true
			}
		}
	}
	if ev.Type != term.EventKey {
		return t.exitState(), false
	}
	if finished {
		return true, false
	}
	if active == nil {
		return false, false
	}
	switch active.kind {
	case reqFloatingWindow:
		return t.handleFloatingWindow(active, ev)
	case reqMarkdown:
		return t.handleMarkdown(active, ev)
	case reqWaitKey:
		return t.handleWaitKey(active, ev)
	case reqWaitCommand:
		return t.handleWaitCommand(active, ev)
	case reqWaitShell:
		return t.handleWaitCommand(active, ev)
	case reqWaitEvent:
		return t.handleWaitEvent(active, ev)
	case reqChoice, reqConfirm:
		return t.handlePrompt(active, ev)
	}
	return false, false
}

func (t *Tutorial) handleFloatingWindow(r *request, ev term.Event) (bool, bool) {
	// allow_keys takes precedence over the default Enter/Esc/Space
	// dismissal so an author can reclaim one of those keys for
	// pass-through. The modal-surfaces step needs <esc> to reach a
	// focused terminal or console (switching it to NORMAL mode)
	// instead of dismissing the overlay.
	if ev.Type == term.EventKey && slices.Contains(r.allowKeys, ev.KeyComb()) {
		return false, false
	}
	if ev.Mod == 0 && (ev.Key == term.KeyEnter || ev.Key == term.KeyEsc ||
		ev.Key == term.KeySpace || ev.Ch == ' ') {
		t.resolve(r, response{})
		return t.exitState(), true
	}
	if ev.Type == term.EventKey {
		kc := ev.KeyComb()
		// dismiss_keys: resolve the window AND let the event reach
		// the IDE root, so a read-then-act binding (e.g. "Press
		// `:` to open the command prompt") advances the tutorial
		// at the same time the user's keypress triggers the
		// described action.
		if slices.Contains(r.dismissKeys, kc) {
			t.resolve(r, response{})
			return t.exitState(), false
		}
	}
	// Swallow stray keys so the IDE root never sees a stray ':' that
	// would open a command prompt under the overlay; arm the hint
	// pulse so the user notices.
	t.mu.Lock()
	r.hasShader = true
	t.mu.Unlock()
	return false, true
}

func (t *Tutorial) handleMarkdown(r *request, ev term.Event) (bool, bool) {
	if ev.Key == term.KeyEnter || ev.Key == term.KeyEsc ||
		ev.Key == term.KeySpace || ev.Ch == ' ' {
		t.resolve(r, response{})
		return t.exitState(), true
	}
	return false, false
}

func (t *Tutorial) handleWaitKey(r *request, ev term.Event) (bool, bool) {
	want, err := term.ParseKeys(r.waitKey)
	if err != nil || len(want) != 1 {
		// Bad key spec: advance on any keystroke so the user is
		// never stuck on an unreachable step.
		t.resolve(r, response{})
		return t.exitState(), true
	}
	k := want[0]
	if ev.Key == k.Key && ev.Mod == k.Mod && ev.Ch == k.Ch {
		t.resolve(r, response{})
		return t.exitState(), true
	}
	return false, false
}

// handleWaitCommand never resolves from keystrokes: the host's
// command observer is the source of truth for command dispatches and
// carries the real args. handleWaitCommand only swallows events when
// the on_error hint has been swapped in so the user sees the hint
// instead of falling through to the root.
func (t *Tutorial) handleWaitCommand(_ *request, _ term.Event) (bool, bool) {
	return false, false
}

// handleWaitEvent never resolves from keystrokes and never swallows:
// the host's editor-event observer is the source of truth, so every
// key falls through to the IDE root while the hint stays up.
func (t *Tutorial) handleWaitEvent(_ *request, _ term.Event) (bool, bool) {
	return false, false
}

// handlePrompt routes ev to the overlay browser, whose focused window
// is the active confirm/choice prompt. Enter fires OnSelect (which
// stamps r.pendingResp/pendingSelected); both selection and Esc close
// the prompt window, which fires OnClose and stamps winClosed. The
// request is then resolved with the pending response or the per-kind
// dismissal response. resolve() installs the barrier before delivery
// so the run goroutine's next publish is observed.
func (t *Tutorial) handlePrompt(r *request, ev term.Event) (bool, bool) {
	if r.win == nil || t.winOverlay == nil {
		return false, false
	}
	_, handled := t.winOverlay.Handle(ev)
	if r.skipRequested.Load() {
		t.Stop()
		return t.exitState(), true
	}
	if !r.winClosed.Load() {
		return false, handled
	}
	t.resolve(r, dismissalOrPendingResponse(r))
	return t.exitState(), handled
}

// dismissalOrPendingResponse returns the response a closed request
// resolves with: the OnSelect-stamped pending response when a
// selection was made, the per-kind dismissal response otherwise.
func dismissalOrPendingResponse(r *request) response {
	if r.pendingSelected {
		return r.pendingResp
	}
	switch r.kind {
	case reqConfirm:
		return response{confirmed: false}
	case reqChoice:
		return response{selectedIdx: -1, selected: false}
	}
	return response{}
}

// Cursor returns no cursor; tutorial overlays do not own the cursor.
func (t *Tutorial) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, term.CursorStyleDefault, false
}

// Selection returns no selection.
func (t *Tutorial) Selection() (string, bool) { return "", false }

// ComponentAt returns the handler behind the bespoke overlay cell at
// pos (the wait-hint boxes and banner); browser-hosted windows are
// hit-tested by the composing handler against the overlay browser
// instead. ok=false when the last Draw painted nothing at pos.
func (t *Tutorial) ComponentAt(pos term.Coordinates) (tui.Handler, bool) {
	t.mu.Lock()
	active := t.active
	finished := t.finished
	t.mu.Unlock()
	if finished || active == nil {
		return nil, false
	}
	vpos := t.overlay.Position()
	if pos.X < vpos.X || pos.Y < vpos.Y ||
		pos.X >= vpos.X+t.overlay.Width() ||
		pos.Y >= vpos.Y+t.overlay.Height() {
		return nil, false
	}
	return t, true
}

// ObserveCommand advances the state machine when the current step is
// wait_command, the dispatched command matches typed or resolved, and
// err is nil. On dispatch error the request is kept armed and the
// hint is swapped to the on_error message. Returns exit=true when
// the tutorial finishes as a result of this observation.
func (t *Tutorial) ObserveCommand(
	typed, resolved string, args []string, err error,
) bool {
	t.mu.Lock()
	active := t.active
	finished := t.finished
	t.mu.Unlock()
	if finished {
		return true
	}
	if active == nil {
		return false
	}
	if active.kind == reqWaitShell {
		return t.observeShellCommand(active, typed, resolved, args, err)
	}
	if active.kind != reqWaitCommand {
		return false
	}
	want := commandName(active.command)
	if want != typed && want != resolved {
		return false
	}
	if err != nil {
		// Do not surface err as a tutorial notification: the IDE's
		// command prompt already shows the underlying error.
		if active.onError != "" {
			active.text = expandCmdTemplate(active.onError, t.commandKeyDisplay)
			t.refreshHintWindow(active)
		}
		return false
	}
	t.resolve(active, response{cmdName: want, cmdArgs: args})
	return t.exitState()
}

// ObserveEvent advances the state machine when the current step is
// wait_event and the observed editor event's type name matches the
// armed event name. When the step declared a URI filter, uri must also
// contain it as a substring. Returns exit=true when the tutorial
// finishes as a result of this observation.
func (t *Tutorial) ObserveEvent(eventType, uri string) bool {
	t.mu.Lock()
	active := t.active
	finished := t.finished
	t.mu.Unlock()
	if finished {
		return true
	}
	if active == nil || active.kind != reqWaitEvent || active.event != eventType {
		return false
	}
	if active.eventURI != "" && !strings.Contains(uri, active.eventURI) {
		return false
	}
	t.resolve(active, response{})
	return t.exitState()
}

// shellCommandName is the typed/resolved command name under which the
// IDE reports companion-console REPL submissions to the command
// observer. The console wrapper prepends the REPL command name to the
// observed args (e.g. ["pkg", "install", "rune-agent"]) so a
// wait_shell step can match on argument tokens alone.
const shellCommandName = "console"

// observeShellCommand advances a reqWaitShell step. It only reacts to
// companion-shell observations (typed/resolved == shellCommandName)
// and requires every expected token to be present in the observed
// args (containment, so completion and alias variants still match). A
// dispatch error keeps the step armed and swaps in the on_error hint.
func (t *Tutorial) observeShellCommand(
	active *request, typed, resolved string, args []string, err error,
) bool {
	if typed != shellCommandName && resolved != shellCommandName {
		return false
	}
	if err != nil {
		if active.onError != "" {
			active.text = expandCmdTemplate(active.onError, t.commandKeyDisplay)
			t.refreshHintWindow(active)
		}
		return false
	}
	if !argsContainAll(args, active.shellArgs) {
		return false
	}
	t.resolve(active, response{cmdName: shellCommandName, cmdArgs: args})
	return t.exitState()
}

// argsContainAll reports whether every token in want appears in have.
func argsContainAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

// publishRequest blocks until the runLoop is allowed to install r as
// the active request and waits for the TUI loop to deliver a
// response or for the run context to be cancelled. Side-effect
// builtins (notify, open_file, highlight_window) do not call this;
// they run inline via runOnTUI instead. A floating_window's browser
// window is opened here, on the run goroutine, before the request
// becomes active — the overlay browser's leaf lock makes that safe —
// and is closed by resolve, by the user's ✕ click, or below when the
// run is cancelled while the request is in flight.
func (t *Tutorial) publishRequest(r *request) (response, error) {
	t.mu.Lock()
	if t.runCtx == nil {
		t.mu.Unlock()
		return response{}, errStopped
	}
	ctx := t.runCtx
	width, height := t.width, t.height
	r.respond = make(chan response, 1)
	if r.kind == reqFloatingWindow || r.kind == reqMarkdown {
		t.stepCount++
		r.stepNum = t.stepCount
	} else {
		r.stepNum = t.stepCount
	}
	t.mu.Unlock()

	switch r.kind {
	case reqFloatingWindow:
		t.openFloatingWindow(r, width, height)
	case reqConfirm, reqChoice:
		t.openPromptWindow(r)
	case reqWaitKey, reqWaitCommand, reqWaitShell, reqWaitEvent:
		t.openHintWindow(r, width, height)
	}

	t.mu.Lock()
	if t.runCtx == nil {
		t.mu.Unlock()
		t.closeRequestWindow(r)
		return response{}, errStopped
	}
	t.active = r
	signal := t.firstSignal
	t.firstSignal = nil
	t.mu.Unlock()
	if signal != nil {
		signal()
	}

	select {
	case res := <-r.respond:
		return res, nil
	case <-ctx.Done():
		t.closeRequestWindow(r)
		return response{}, errStopped
	}
}

// padPromptOptions surrounds each option label with a single space on
// either side so the rendered prompt buttons are naturally padded,
// matching the IDE's browser-driven prompts. The returned slice is for
// display only; the unpadded labels remain the authoritative selection
// values via request.options.
func padPromptOptions(options []string) []string {
	padded := make([]string, len(options))
	for i, o := range options {
		padded[i] = " " + o + " "
	}
	return padded
}

// resolve delivers res to r and clears the active slot. The TUI loop
// calls this from Draw/Handle; multiple resolves on the same request
// are no-ops thanks to request.deliver's sync.Once. The request's
// browser window is closed before delivery so the old window is gone
// before the run goroutine can open the next one. After delivery,
// resolve installs a one-shot barrier and blocks until the run
// goroutine reaches its next observable state (a freshly published
// request, an inline side effect, or run completion), so that
// Handle/ObserveCommand callers can assume the runtime is quiescent
// on return.
func (t *Tutorial) resolve(r *request, res response) {
	barrier := make(chan struct{})
	var once sync.Once
	signal := func() { once.Do(func() { close(barrier) }) }

	t.mu.Lock()
	if t.active == r {
		t.active = nil
	}
	if t.finished {
		// The run goroutine has already exited (e.g. raced an
		// exit/fail with the dispatch). Skip the barrier — there
		// will be no more state transitions.
		t.mu.Unlock()
		t.closeRequestWindow(r)
		r.deliver(res)
		return
	}
	t.firstSignal = signal
	t.mu.Unlock()

	t.closeRequestWindow(r)
	r.deliver(res)
	<-barrier
}

// exitState reports whether the runtime has finished. Used as the
// `exit` return for Handle/ObserveCommand after resolving a request.
func (t *Tutorial) exitState() bool {
	t.mu.Lock()
	finished := t.finished
	t.mu.Unlock()
	return finished
}

// requestSkip stamps the request's skip flag and schedules a TUI-loop
// reap. It runs while the overlay-browser lock is held (a button
// click dispatched by the browser), so it may only stamp state and
// queue the tick — never call back into the overlay or block on the
// loop.
func (r *request) requestSkip(t *Tutorial) {
	r.skipRequested.Store(true)
	if t.scheduleNextTick != nil {
		t.scheduleNextTick(t.exitOnSkip)
	}
}

// exitOnSkip is the scheduled reap for a "Skip" click: the
// tutorial exits as if `tutorial stop` ran, provided the armed
// request is still the clicked one.
func (t *Tutorial) exitOnSkip() {
	t.mu.Lock()
	active := t.active
	finished := t.finished
	t.mu.Unlock()
	if finished || active == nil || !active.skipRequested.Load() {
		return
	}
	t.Stop()
}

// WaitActive blocks until the run goroutine publishes a request whose
// kind matches want or the tutorial finishes, whichever happens
// first. It is intended for tests that drive the runtime
// synchronously through Handle/ObserveCommand and need a barrier
// between events. Returns true when the matching request became
// active, false on timeout or when the tutorial finished without
// publishing such a request. want is one of "floating_window",
// "markdown", "wait_key", "wait_command", "confirm", "choice".
func (t *Tutorial) WaitActive(want string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		t.mu.Lock()
		active := t.active
		finished := t.finished
		t.mu.Unlock()
		if finished {
			return false
		}
		if active != nil && active.kind.String() == want {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
}

// ActiveText returns the hint markdown the active step declared, or
// "" when no step is active or the step has none. Intended for tests
// that assert a lesson's copy without a browser wired.
func (t *Tutorial) ActiveText() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active == nil {
		return ""
	}
	return t.active.text
}

// WaitFinished blocks until the run goroutine has exited or d
// elapses. Returns true once finished. Intended for tests.
func (t *Tutorial) WaitFinished(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		t.mu.Lock()
		done := t.runDone
		finished := t.finished
		t.mu.Unlock()
		if finished && done == nil {
			return true
		}
		if done != nil {
			select {
			case <-done:
				return true
			case <-time.After(time.Until(deadline)):
				return false
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
}
