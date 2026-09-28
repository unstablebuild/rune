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
// builtins (wait_command, wait_event, choice, ...) that authors
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
	"unicode/utf8"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/term"

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
// manual, so a wait_command step without copy can render the command's
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

	promptStyle      idetutorial.PromptStyle
	editor           text.Editor
	notifications    browserapi.Notifications
	parser           syntaxapi.Parser
	scheduleNextTick func(func()) bool
	storage          storageapi.Service
	// commandKeyDisplay is the prettified, display-ready command-prompt
	// key spec (e.g. ":" rather than "<shift-;>"). It is the single
	// rendered form every tutorial surface uses; nothing re-renders the
	// raw term.KeyComb, so a new render site cannot reintroduce the ugly
	// spec.
	commandKeyDisplay string
	// editorMode is the user's resolved editor mode ("vim",
	// "helix", "standard", or "emacs"), exposed to the DSL via
	// editor_mode(). exo is resolved to its fallback by the host before
	// New.
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
	// manual, used by a wait_command step that declared no copy so
	// the user still sees the command's synopsis and description. nil
	// disables manual rendering: the hint falls back to a plain line.
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

	// configPath is the file the running Rune reads its user
	// configuration from, exposed to the DSL via config_path(). It
	// moves with the data directory, so copy that names it has to ask
	// the host rather than hardcode a path.
	configPath string

	// retired names the first DSL feature the source uses that this
	// Rune no longer implements, or "" when the lesson is supported.
	retired string

	// parsed entry function. Set once at New time.
	entry *starlark.Function

	// Runtime state. mu guards everything below.
	mu     sync.Mutex
	width  int
	height int
	active *request
	// history is every screen the run has shown, oldest first, so the
	// user can page back through copy they already read.
	history []*request
	// viewing indexes history while an earlier screen is on show; -1
	// means the tile shows the live step.
	viewing int
	// closing is the screen a completed run leaves on the tile, where
	// it waits for the user to press Stop. nil until a run returns
	// normally.
	closing   *request
	runCtx    context.Context
	cancel    context.CancelFunc
	thread    *starlark.Thread
	runDone   chan struct{}
	finished  bool
	completed bool

	// skipStranded is set while a skipped step's response has not
	// yet been vindicated by the run publishing another request. A
	// skip hands the script what the step was waiting for, but a
	// lesson can still read information only the real action would
	// have produced (an argument the author never declared, say).
	// The resulting Starlark error is the skip's fault, not the
	// author's, so it ends the run with an explanation instead of an
	// error notification. publishRequest clears it: surviving to the
	// next step proves the skip did no harm.
	skipStranded bool

	// firstSignal is set by Reset to a one-shot signal that the
	// runtime has made user-visible progress (posted its first
	// blocking request, scheduled its first side effect, or exited).
	// Reset waits on it so subsequent Draw/Handle/Close observe a
	// stable state — and so a test that drives input immediately
	// after Reset never races with the run goroutine.
	firstSignal func()
}

var _ idetutorial.Tutorial = (*Tutorial)(nil)

// Option configures a host service that only some embedders wire.
type Option func(*Tutorial)

// WithConfigPath names the file Rune reads its user configuration
// from, which the DSL exposes as config_path().
func WithConfigPath(path string) Option {
	return func(t *Tutorial) { t.configPath = path }
}

// New parses src as a starlark tutorial DSL program and returns a
// runnable Tutorial. The program must call `tutorial(entry=fn)`
// exactly once at top level; fn becomes the entry function the
// Starlark thread executes on Reset. name is the registry name used
// for error reporting and as the default id. The remaining arguments
// are the host services the DSL builtins resolve at runtime.
func New(
	name, src string,
	promptStyle idetutorial.PromptStyle,
	ed text.Editor,
	notifications browserapi.Notifications,
	parser syntaxapi.Parser,
	scheduleNextTick func(func()) bool,
	storage storageapi.Service,
	commandKey term.KeyComb,
	editorMode string,
	os string,
	keyForCommand func(cmd string, args []string) string,
	commandManualLookup CommandManualLookup,
	workspaceOpen func() bool,
	lspServerRunning func() bool,
	opts ...Option,
) (*Tutorial, error) {
	if src == "" {
		return nil, errors.New("starlarktutorial: empty source")
	}
	t := &Tutorial{
		name:                name,
		promptStyle:         promptStyle,
		editor:              ed,
		notifications:       notifications,
		parser:              parser,
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
	for _, opt := range opts {
		opt(t)
	}

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
	t.retired = retiredUse(opts, t.name+".star", src, predeclared)
	for name, stub := range retiredBuiltins() {
		if _, taken := predeclared[name]; !taken {
			predeclared[name] = stub
		}
	}
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

// Finished reports whether the most recent run has ended.
func (t *Tutorial) Finished() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.finished
}

// PromptActive reports whether the screen on show is a live prompt:
// the confirm or choice step the run is blocked on, rather than copy
// or a screen the user paged back to.
func (t *Tutorial) PromptActive() bool {
	r, live := t.visibleScreen()
	return live && r.prompt != nil
}

// Reset stops any in-progress run and starts a fresh Starlark thread
// that calls the entry function on its own goroutine. Reset returns
// after launching the goroutine; Draw/Handle observe an empty active
// slot until the entry posts its first blocking request, at which
// point the slot is filled atomically.
func (t *Tutorial) Reset() {
	t.Stop()
	t.mu.Lock()
	if t.retired != "" {
		t.finished = true
		t.completed = false
		t.active = nil
		t.closing = nil
		t.mu.Unlock()
		t.notifyRetired()
		return
	}
	t.finished = false
	t.completed = false
	t.active = nil
	t.history = nil
	t.viewing = -1
	t.closing = nil
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
	t.closing = nil
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
	width, height := t.width, t.height
	t.mu.Unlock()

	var closing *request
	if err == nil {
		closing = t.closingScreen(width, height)
	}

	t.mu.Lock()
	t.finished = true
	t.completed = err == nil
	t.active = nil
	t.thread = nil
	t.closing = closing
	t.viewing = -1
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
	if isRetiredDSL(err) {
		t.notifyRetired()
		return
	}
	t.mu.Lock()
	stranded := t.skipStranded
	t.skipStranded = false
	t.mu.Unlock()
	if stranded {
		t.runOnTUI(func() {
			if t.notifications != nil {
				_, _ = t.notifications.Notify(browserapi.LevelInfo,
					"%s: ended because a skipped step left the lesson "+
						"without something it needed", t.Title())
			}
		})
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

// isRetiredDSL reports whether err came from a DSL feature this Rune
// no longer implements.
func isRetiredDSL(err error) bool {
	if errors.Is(err, errRetiredDSL) {
		return true
	}
	var evalErr *starlark.EvalError
	if errors.As(err, &evalErr) && evalErr.Unwrap() != nil {
		return errors.Is(evalErr.Unwrap(), errRetiredDSL)
	}
	return false
}

// notifyRetired tells the user their tutorial is too old for this
// Rune and where the fix lives: the package that ships the lesson,
// not their config.
func (t *Tutorial) notifyRetired() {
	t.runOnTUI(func() {
		if t.notifications == nil {
			return
		}
		_, _ = t.notifications.Notify(browserapi.LevelError,
			"%s: this tutorial is not supported by this version of Rune. "+
				"Upgrade the package that provides it.", t.Title())
	})
}

// Resize records the tile body's dimensions and reflows the active
// screen inside them.
func (t *Tutorial) Resize(width, height int) {
	t.mu.Lock()
	t.width, t.height = width, height
	visible, _ := t.visibleLocked()
	t.mu.Unlock()
	if visible != nil {
		visible.body.Resize(width, height)
	}
}

// Draw paints the screen on show in body-local coordinates. The tile
// clips writes to the body rectangle, so a screen taller than the body
// is simply truncated.
func (t *Tutorial) Draw(w term.Writer) {
	if r, _ := t.visibleScreen(); r != nil {
		r.body.Draw(w)
	}
}

// visibleScreen returns the screen on show and whether it is the live
// step rather than one the user paged back to, or nil when there is
// nothing to draw.
func (t *Tutorial) visibleScreen() (r *request, live bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.visibleLocked()
}

func (t *Tutorial) visibleLocked() (r *request, live bool) {
	if t.finished && t.closing == nil {
		return nil, false
	}
	if t.viewing >= 0 && t.viewing < len(t.history) {
		return t.history[t.viewing], false
	}
	if t.closing != nil {
		return t.closing, false
	}
	if t.active == nil || t.active.body == nil {
		return nil, false
	}
	return t.active, true
}

// Back pages the tile one screen back through the copy the lesson
// already showed, wrapping from the oldest screen to the live step so
// a reader is never stranded in the past. Nothing is rewound: the live
// step stays armed, and reaching its milestone (or skipping it) brings
// the tile back to it. A finished lesson pages from its closing screen
// instead, so the whole lesson is still readable after the last step.
func (t *Tutorial) Back() bool {
	t.mu.Lock()
	if len(t.history) == 0 || (t.finished && t.closing == nil) {
		t.mu.Unlock()
		return false
	}
	switch {
	case t.viewing < 0 && t.closing != nil:
		// The closing screen is not part of the lesson, so the step
		// behind it is the last one the lesson showed.
		t.viewing = len(t.history) - 1
	case t.viewing < 0:
		if len(t.history) < 2 {
			t.mu.Unlock()
			return false
		}
		t.viewing = len(t.history) - 2
	case t.viewing == 0:
		t.viewing = -1
	default:
		t.viewing--
	}
	visible, _ := t.visibleLocked()
	width, height := t.width, t.height
	t.mu.Unlock()
	if visible != nil {
		visible.body.Resize(width, height)
	}
	return true
}

// Forward pages the tile one screen back towards the step the lesson
// is on, landing on the live step itself rather than on the read-only
// copy of it kept in history. It reports whether the tile now shows a
// different screen; false when the live screen is already on show.
func (t *Tutorial) Forward() bool {
	t.mu.Lock()
	if t.viewing < 0 || t.viewing >= len(t.history) {
		t.mu.Unlock()
		return false
	}
	// The live step is the newest entry in history; a finished lesson
	// has none, and its closing screen sits after them all instead.
	last := len(t.history) - 1
	if t.closing == nil {
		last--
	}
	if t.viewing >= last {
		t.viewing = -1
	} else {
		t.viewing++
	}
	visible, _ := t.visibleLocked()
	width, height := t.width, t.height
	t.mu.Unlock()
	if visible != nil {
		visible.body.Resize(width, height)
	}
	return true
}

// ViewingPast reports whether the tile shows a screen the user paged
// back to rather than the one the lesson is on.
func (t *Tutorial) ViewingPast() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.viewing >= 0 && t.viewing < len(t.history)
}

// Handle routes ev to the content of the screen on show: the markdown
// viewer of a wait_* step or the prompt of a confirm/choice step. A
// step only ends when its milestone is met, so a wait_* screen never
// resolves from here and the viewer's own quit keys are ignored; a
// prompt resolves its step when an option is selected or the prompt is
// dismissed. A screen the user paged back to is read-only, since its
// step is long resolved. exit is always false: the tutorial is the content of the
// tile and reporting exit would close it. handled reflects
// what the content did with ev so unhandled keys reach the IDE's
// bindings.
func (t *Tutorial) Handle(ev term.Event) (bool, bool) {
	r, live := t.visibleScreen()
	if r == nil {
		return false, false
	}
	if ev.Type != term.EventMouse && ev.Type != term.EventKey {
		return false, false
	}
	exit, handled := r.body.Handle(ev)
	if r.prompt == nil || !live {
		// The viewer's quit keys are dropped, so they were not
		// really handled: let them reach the IDE's bindings.
		return false, handled && !exit
	}
	if exit {
		_ = r.prompt.Close()
	}
	if r.closed {
		t.resolve(r, dismissalOrPendingResponse(r))
	}
	return false, handled
}

// SeekUp satisfies component.Scrollable by scrolling the active
// screen's viewer, so the tile's scroll bar drives the step's copy.
func (t *Tutorial) SeekUp() bool {
	if r, _ := t.visibleScreen(); r != nil && r.viewer != nil {
		return r.viewer.SeekUp()
	}
	return false
}

// SeekDown satisfies component.Scrollable.
func (t *Tutorial) SeekDown() bool {
	if r, _ := t.visibleScreen(); r != nil && r.viewer != nil {
		return r.viewer.SeekDown()
	}
	return false
}

// SeekOffset satisfies component.Scrollable.
func (t *Tutorial) SeekOffset() int {
	if r, _ := t.visibleScreen(); r != nil && r.viewer != nil {
		return r.viewer.SeekOffset()
	}
	return 0
}

// MaxSeekOffset satisfies component.Scrollable.
func (t *Tutorial) MaxSeekOffset() int {
	if r, _ := t.visibleScreen(); r != nil && r.viewer != nil {
		return r.viewer.MaxSeekOffset()
	}
	return 0
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

// skipResponse is the response a skipped step resolves with, and
// whether that response may strand the lesson.
//
// The wait_command and wait_shell builtins hand their response to the
// script as a command_result, so a zero response would give the lesson
// an empty name and an empty args tuple. Both kinds know what they
// were waiting for, so a skip reports that: the script sees the
// command it asked the user to run. Every other kind either discards
// its response or already defines a dismissal, so skipping one cannot
// strand anything.
//
// mayStrand is true only for a wait_command whose spec declared no
// arguments. The live path resolves such a step with the arguments the
// user actually typed, so a lesson may read an argument the author
// never wrote down (basics.star awaits "edit" and then reads
// .args[0]). That is the one case where a skip cannot reproduce what
// the real action would have produced.
func skipResponse(r *request) (res response, mayStrand bool) {
	switch r.kind {
	case reqWaitCommand:
		fields := strings.Fields(r.command)
		if len(fields) == 0 {
			return response{}, true
		}
		return response{cmdName: fields[0], cmdArgs: fields[1:]},
			len(fields) == 1
	case reqWaitShell:
		return response{
			cmdName: shellCommandName, cmdArgs: r.shellArgs,
		}, false
	}
	return dismissalOrPendingResponse(r), false
}

// Cursor returns no cursor; tutorial screens do not own the cursor.
func (t *Tutorial) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, term.CursorStyleDefault, false
}

// Selection returns the text selected with the mouse in the viewer of
// the screen on show, if any.
func (t *Tutorial) Selection() (string, bool) {
	if r, _ := t.visibleScreen(); r != nil && r.viewer != nil {
		return r.viewer.Selection()
	}
	return "", false
}

// ObserveCommand advances the state machine when the current step is
// wait_command, the dispatched command matches typed or resolved, and
// err is nil. On dispatch error the step stays armed with its copy
// unchanged: the IDE's command prompt already reports the error, and a
// screen that rewrites itself mid-step moves the instructions the user
// is reading. Returns exit=true when the tutorial finishes as a result
// of this observation.
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
// dispatch error keeps the step armed with its copy unchanged.
func (t *Tutorial) observeShellCommand(
	active *request, typed, resolved string, args []string, err error,
) bool {
	if typed != shellCommandName && resolved != shellCommandName {
		return false
	}
	if err != nil {
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
// they run inline via runOnTUI instead. The screen the tile draws for
// r is built here, on the run goroutine, before the request becomes
// active, so nothing else can be touching it yet.
func (t *Tutorial) publishRequest(r *request) (response, error) {
	t.mu.Lock()
	if t.runCtx == nil {
		t.mu.Unlock()
		return response{}, errStopped
	}
	ctx := t.runCtx
	width, height := t.width, t.height
	r.respond = make(chan response, 1)
	t.skipStranded = false
	t.mu.Unlock()

	t.openScreen(r, width)
	if r.body != nil {
		r.body.Resize(width, height)
	}

	t.mu.Lock()
	if t.runCtx == nil {
		t.mu.Unlock()
		return response{}, errStopped
	}
	t.active = r
	if r.body != nil {
		t.history = append(t.history, r)
	}
	t.viewing = -1
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
		return response{}, errStopped
	}
}

// padPromptOptions surrounds each option label with pad spaces on
// either side so the rendered prompt buttons are naturally padded,
// matching the IDE's browser-driven prompts. The returned slice is for
// display only; the unpadded labels remain the authoritative selection
// values via request.options.
func padPromptOptions(options []string, pad int) []string {
	gutter := strings.Repeat(" ", max(pad, 0))
	padded := make([]string, len(options))
	for i, o := range options {
		padded[i] = gutter + o + gutter
	}
	return padded
}

// promptOptionPad is how much every option label can be padded in a
// prompt width cells wide. The prompt gives each button the width of
// the widest one and spreads what is left over the gaps between them,
// so padding labels that leave nothing over buries the buttons in one
// another. A cramped row drops the padding rather than the air that
// tells the buttons apart.
func promptOptionPad(options []string, width int) int {
	widest := 0
	for _, o := range options {
		widest = max(widest, utf8.RuneCountInString(o))
	}
	n := len(options)
	// One cell between every pair of buttons and at both edges.
	if n*(widest+2)+n+1 <= width {
		return 1
	}
	return 0
}

// resolve delivers res to r and clears the active slot. The TUI loop
// calls this from Draw/Handle; multiple resolves on the same request
// are no-ops thanks to request.deliver's sync.Once. After delivery,
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
		r.deliver(res)
		return
	}
	t.firstSignal = signal
	t.mu.Unlock()
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

// Skip resolves the active step as though the user had performed it,
// advancing the run past steps a user cannot or does not want to do.
// wait_* steps have no dismissal of their own, so this is their only
// escape. See skipResponse for what the script receives.
func (t *Tutorial) Skip() (exit bool) {
	t.mu.Lock()
	active := t.active
	finished := t.finished
	t.mu.Unlock()
	if finished || active == nil {
		return finished
	}
	res, mayStrand := skipResponse(active)
	if mayStrand {
		t.mu.Lock()
		t.skipStranded = true
		t.mu.Unlock()
	}
	t.resolve(active, res)
	return t.exitState()
}

// WaitActive blocks until the run goroutine publishes a request whose
// kind matches want or the tutorial finishes, whichever happens
// first. It is intended for tests that drive the runtime
// synchronously through Handle/ObserveCommand and need a barrier
// between events. Returns true when the matching request became
// active, false on timeout or when the tutorial finished without
// publishing such a request. want is one of "wait_command",
// "wait_shell", "wait_event", "confirm", "choice".
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

// ActiveTitle returns the title the active step declared, or "" when
// no step is active. Intended for tests.
func (t *Tutorial) ActiveTitle() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active == nil {
		return ""
	}
	return t.active.title
}

// ActiveKind returns the name of the builtin the run goroutine is
// blocked on ("wait_command", "confirm", ...), or "" when no step is
// active. Intended for tests.
func (t *Tutorial) ActiveKind() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished || t.active == nil {
		return ""
	}
	return t.active.kind.String()
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
