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

// Package debugshell exposes a "debugger" command usable from the
// ideconsole REPL and from the editor command prompt. It wraps a
// debugapi.Debugger and maintains a single active debug session.
package debugshell

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/google/go-dap"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/debugapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/ide/idedebug"
)

// CommandName is the top-level REPL/prompt command registered by
// the package.
const CommandName = "debugger"

// ErrUnknownSubcommand is returned when the requested subcommand
// is not recognised.
var ErrUnknownSubcommand = errors.New("unknown debugger subcommand")

// Icons controls visual markers rendered by the debug shell.
type Icons struct {
	// Breakpoint is the icon rendered in the editor gutter for
	// a breakpoint location. Empty disables icons.
	Breakpoint string
	// Stopped is the icon rendered on the current stopped line.
	Stopped string
}

// Config configures a Handler. The DAP adapter is spawned by the
// underlying debugapi.Debugger when the user runs `debugger
// initialize <langID>`; adapter configuration comes from the
// `debugger` section of rune.star and is resolved by the
// Debugger itself. This package does not spawn adapters.
type Config struct {
	// WorkspaceURI is the workspace root.
	WorkspaceURI workspaceapi.URI
	// Icons controls visual markers.
	Icons Icons
	// Debugger is the same configuration passed to the
	// underlying idedebug.Manager. Used to drive completion of
	// `debugger initialize <langID>` against the configured
	// adapters.
	Debugger idedebug.Config
	// ScheduleNextTick defers a function to the GUI event loop's
	// next tick. Required: the debug shell mutates IDE resources
	// (browser windows, editors, location lists) from goroutines
	// driven by REPL command dispatch and DAP event delivery, and
	// every such mutation must be hopped onto the event-loop
	// goroutine to avoid racing the renderer. See
	// AGENTS.md "Concurrency: REPL/DAP goroutines vs IDE
	// resources".
	ScheduleNextTick func(func()) bool
}

// Handler is a textapi.REPLHandler that forwards "debugger"
// subcommands to a debugapi.Debugger.
//
// Handler owns at most one active debug session at a time. The
// session is created by the user running `debugger initialize
// <langID>`, which calls debugapi.Debugger.CreateSession and
// subscribes Handler to its event stream. Subsequent subcommands
// dispatch against the stored sessionID. `debugger terminate`
// ends the session, as does the adapter closing on its own.
type Handler struct {
	dbg     debugapi.Debugger
	cfg     Config
	browser browser.Browser
	editor  textapi.Editor
	parser  syntaxapi.Parser
	fs      workspaceapi.FileSystem
	notify  func(level browserapi.NotificationLevel, msg string, args ...any)
	// scheduleNextTick is the GUI event-loop scheduler used to
	// hop IDE-resource calls back onto the main goroutine. Set
	// from Config.ScheduleNextTick by New(); never nil.
	scheduleNextTick func(func()) bool

	mu        sync.Mutex
	sessionID string
	phase     sessionPhase
	events    chan sessionEvent
	// breakpoints tracks 1-based line numbers per source path so
	// set-breakpoint accumulates lines and re-sends the full list
	// (DAP semantics).
	breakpoints map[string][]int
	// stopped tracks the most recent stack frames and the
	// frame index the cursor currently sits on (for the
	// prompt `jump` subcommand).
	stoppedFrames []dap.StackFrame
	stoppedFrame  int
	// stoppedThreadID mirrors the ThreadId from the most
	// recent DAP StoppedEvent while a stop is active. It is
	// used by threadID/topFrameID so that variables, evaluate
	// and friends target the goroutine that actually hit the
	// breakpoint instead of an arbitrary entry from the DAP
	// `Threads` response (which under the race detector is
	// almost never the user's goroutine). Cleared together
	// with stoppedFrames when the session ends.
	stoppedThreadID int
	// installedLocations tracks every (file URI string,
	// location list ID) pair we have ever installed during the
	// session, so we can clear them all on terminate. Cleared
	// when a new session starts.
	installedLocations map[string]map[string]bool
	// output redirects DAP OutputEvent bodies to a temp file
	// per session. Created at launch/attach time; closed on
	// session terminate. Nil when no session is active.
	output *outputSink
	// launchInit is a one-shot channel armed by cmdLaunch /
	// cmdAttach and closed when the adapter delivers a
	// *dap.InitializedEvent. The launch iterator blocks on
	// it so the prompt stays "busy" until the debuggee has
	// actually initialized. Nil when no launch is in flight.
	launchInit chan struct{}
}

type sessionPhase int

const (
	phaseNone sessionPhase = iota
	phaseInitialized
	phaseStarted
	phaseConfigured
)

// sessionEvent is a single item streamed over Handler.events:
// either a DAP event or a close notification. The iterator
// returned from `debugger initialize` turns these into markdown
// components for the REPL.
type sessionEvent struct {
	ev     dap.EventMessage
	closed bool
	reason string
	// rendered carries an already-formatted markdown body
	// produced by the handler itself (e.g. the auto stack-
	// trace pushed after every StoppedEvent). When set, the
	// iterator emits it verbatim instead of running the
	// generic event formatter.
	rendered string
}

var _ textapi.REPLHandler = (*Handler)(nil)
var _ debugapi.EventSubscriber = (*Handler)(nil)

// New returns a Handler backed by dbg. The DAP adapter must be
// initialised separately by the owning language extension (e.g.
// extension_go calling Debugger.Initialize) — this package does
// not spawn adapters.
//
// b is the browser used to open files and pick a tile window
// when the debuggee stops at a breakpoint; ed is the editor used
// to position the cursor and install the stopped-line marker. b
// or ed may be nil in tests that exercise lifecycle without UI.
//
// The Handler is meant to be wired by the IDE host, which owns
// the "debugger" command (both REPL and command prompt) because
// its semantics are common to every language. Use Manual() for
// the CommandManual and NewPromptHandler(h) for the
// text.CommandHandler that drives the ":debugger ..." command
// prompt.
//
// Panics if dbg is nil — passing a nil debugger is a programmer
// error.
func New(
	dbg debugapi.Debugger, b browser.Browser, ed textapi.Editor,
	parser syntaxapi.Parser, fs workspaceapi.FileSystem, cfg Config,
) *Handler {
	if dbg == nil {
		panic("debugshell: debugger is required")
	}
	if parser == nil {
		panic("debugshell: parser is required")
	}
	if fs == nil {
		panic("debugshell: file system is required")
	}
	if cfg.ScheduleNextTick == nil {
		panic("debugshell: Config.ScheduleNextTick is required")
	}
	return &Handler{
		dbg:                dbg,
		cfg:                cfg,
		browser:            b,
		editor:             ed,
		parser:             parser,
		fs:                 fs,
		scheduleNextTick:   cfg.ScheduleNextTick,
		breakpoints:        make(map[string][]int),
		installedLocations: make(map[string]map[string]bool),
	}
}

// WithNotify installs a best-effort notification sink used for
// non-fatal debugger UI errors.
func (h *Handler) WithNotify(fn func(level browserapi.NotificationLevel, msg string, args ...any)) *Handler {
	h.notify = fn
	return h
}

func defaultClientCapabilities() debugapi.ClientCapabilities {
	return debugapi.ClientCapabilities{
		ClientID:                  "rune",
		ClientName:                "Rune IDE",
		LinesStartAt1:             true,
		ColumnsStartAt1:           true,
		PathFormat:                "path",
		SupportsVariableType:      true,
		SupportsVariablePaging:    true,
		SupportsMemoryReferences:  true,
		SupportsProgressReporting: true,
		SupportsInvalidatedEvent:  true,
		SupportsMemoryEvent:       true,
	}
}

// OnEvent implements debugapi.EventSubscriber. It forwards DAP
// events to the active session's iterator. If no session is
// active (e.g. after OnClose) the event is dropped.
func (h *Handler) OnEvent(ev dap.EventMessage) {
	if stopped, ok := ev.(*dap.StoppedEvent); ok {
		go h.handleStoppedBreakpoint(context.Background(), stopped)
	}
	_, isContinued := ev.(*dap.ContinuedEvent)
	_, isTerminated := ev.(*dap.TerminatedEvent)
	if isContinued || isTerminated {
		// Whenever the debuggee resumes, clear the
		// stopped-line marker and the in-scope variables
		// list installed by the previous Stopped: those
		// locations are no longer accurate once execution
		// moves past them, and leaving them on screen would
		// be misleading until the next stop reinstalls them.
		h.clearStoppedLocations()
	}
	if out, ok := ev.(*dap.OutputEvent); ok {
		h.appendOutput(out)
	}
	h.notifyMilestone(ev)
	if _, ok := ev.(*dap.InitializedEvent); ok {
		// Release any in-flight launch/attach iterator
		// waiting on the InitializedEvent so the user sees
		// the prompt return to idle alongside the
		// "Debuggee initialized." line emitted by the
		// session iterator.
		h.signalLaunchInit()
	}
	// Hold mu across the send so resetSession/OnClose cannot
	// nil out and close the channel between us reading it and
	// us sending into it. The send is non-blocking; the
	// channel is buffered (capacity 64), so under lock contention
	// is bounded.
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.events == nil {
		return
	}
	select {
	case h.events <- sessionEvent{ev: ev}:
	default:
		// Drop events rather than block the adapter stream.
	}
}

// notifyMilestone emits a user-visible notification for the key
// DAP events that mark a transition in the session's lifecycle:
// initialized, stopped, continued, exited, terminated. Other
// events (output, thread, module, ...) are forwarded only to the
// REPL iterator. The notify hook is best-effort: when nil
// (e.g. in tests that don't care about notifications) the call
// is a no-op.
func (h *Handler) notifyMilestone(ev dap.EventMessage) {
	if h.notify == nil || ev == nil {
		return
	}
	switch e := ev.(type) {
	case *dap.InitializedEvent:
		h.notify(browserapi.LevelInfo, "debugger: initialized")
	case *dap.StoppedEvent:
		desc := e.Body.Description
		if desc == "" {
			desc = e.Body.Reason
		}
		h.notify(browserapi.LevelInfo,
			"debugger: stopped (%s) on thread %d", desc, e.Body.ThreadId)
	case *dap.ContinuedEvent:
		h.notify(browserapi.LevelInfo,
			"debugger: continued on thread %d", e.Body.ThreadId)
	case *dap.ExitedEvent:
		h.notify(browserapi.LevelInfo,
			"debugger: debuggee exited (code %d)", e.Body.ExitCode)
	case *dap.TerminatedEvent:
		h.notify(browserapi.LevelInfo, "debugger: terminated")
	}
}

const stoppedLocationID = "debugger/stopped"
const variablesLocationID = "debugger/variables"

// pushRenderedStackTrace formats frames the same way
// `debugger stack-trace` does and pushes the rendered
// markdown into the session iterator. It is best-effort:
// when no session is active or the channel buffer is full
// the entry is dropped (the user can always re-request it).
func (h *Handler) pushRenderedStackTrace(frames []dap.StackFrame) {
	body := formatStackTraceMarkdown(frames)
	if body == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.events == nil {
		return
	}
	select {
	case h.events <- sessionEvent{rendered: body}:
	default:
	}
}

// armLaunchInit allocates a fresh one-shot channel used by
// the launch iterator to block until the adapter delivers a
// *dap.InitializedEvent. Any previously-armed channel is
// replaced (and left to be GC'd) so a second launch within
// the same session does not inherit stale state.
func (h *Handler) armLaunchInit() chan struct{} {
	ch := make(chan struct{})
	h.mu.Lock()
	h.launchInit = ch
	h.mu.Unlock()
	return ch
}

// signalLaunchInit closes the currently-armed launchInit
// channel and clears the field so it is closed at most once.
// Safe to call when no launch is in flight: the call is a
// no-op in that case.
func (h *Handler) signalLaunchInit() {
	h.mu.Lock()
	ch := h.launchInit
	h.launchInit = nil
	h.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// runOnUI schedules fn to run on the GUI event-loop goroutine.
// It is used by code paths that originate off the event loop
// (DAP event subscribers, REPL command goroutines) and need to
// mutate IDE resources (browser windows, editors, location
// lists). Fire-and-forget: any error inside fn must be reported
// via the notify hook rather than returned. Drops fn silently
// when the scheduler refuses (event loop shutting down).
func (h *Handler) runOnUI(fn func()) {
	_ = h.scheduleNextTick(fn)
}

// recordInstalledLocation remembers that locID was installed
// against the file at uri so a subsequent terminate can clear
// it via SetLocationList(nil). Safe to call concurrently with
// other Handler methods.
func (h *Handler) recordInstalledLocation(uri workspaceapi.URI, locID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.installedLocations == nil {
		h.installedLocations = make(map[string]map[string]bool)
	}
	key := uri.String()
	ids := h.installedLocations[key]
	if ids == nil {
		ids = make(map[string]bool)
		h.installedLocations[key] = ids
	}
	ids[locID] = true
}

// clearInstalledLocations clears every location list ever
// installed during the active session by calling
// SetLocationList with an empty slice. Resets the tracking
// map. Best-effort: any per-file failure is reported via the
// notify hook but does not stop processing of other files.
//
// Editor mutations are hopped onto the event loop because
// this method may be invoked from off-loop call sites:
// OnClose runs on the DAP event delivery goroutine and
// resetSession is reachable from REPL command goroutines.
func (h *Handler) clearInstalledLocations() {
	h.mu.Lock()
	installed := h.installedLocations
	h.installedLocations = make(map[string]map[string]bool)
	h.mu.Unlock()
	if h.editor == nil || len(installed) == 0 {
		return
	}
	h.runOnUI(func() {
		for uriStr, ids := range installed {
			uri, err := workspaceapi.ParseURI(uriStr)
			if err != nil {
				continue
			}
			ed, err := h.editor.Editor(uri)
			if err != nil {
				continue
			}
			for id := range ids {
				_ = h.editor.SetLocationList(
					ed,
					textapi.LocationPriorityWarning,
					id,
					textapi.LocationSlice(nil),
				)
			}
		}
	})
}

// clearStoppedLocations removes only the stopped-line and
// in-scope-variables location lists installed by the most
// recent handleStoppedBreakpoint, leaving user breakpoints
// (and any other lists) intact. It is invoked on every
// ContinuedEvent so the markers don't outlive the stop they
// describe.
//
// Like clearInstalledLocations, mutations hop onto the GUI
// event loop because this can be called from the DAP event
// delivery goroutine.
func (h *Handler) clearStoppedLocations() {
	if h.editor == nil {
		return
	}
	h.mu.Lock()
	installed := h.installedLocations
	// Build per-file ID set restricted to the stopped
	// markers, and prune those entries from the tracking
	// map. Other location lists (breakpoints, etc.) are
	// preserved.
	toClear := make(map[string][]string, len(installed))
	for uriStr, ids := range installed {
		for id := range ids {
			switch id {
			case stoppedLocationID, variablesLocationID:
				toClear[uriStr] = append(toClear[uriStr], id)
				delete(ids, id)
			}
		}
		if len(ids) == 0 {
			delete(installed, uriStr)
		}
	}
	h.mu.Unlock()
	if len(toClear) == 0 {
		return
	}
	h.runOnUI(func() {
		for uriStr, ids := range toClear {
			uri, err := workspaceapi.ParseURI(uriStr)
			if err != nil {
				continue
			}
			ed, err := h.editor.Editor(uri)
			if err != nil {
				continue
			}
			for _, id := range ids {
				prio := textapi.LocationPriorityWarning
				if id == variablesLocationID {
					prio = textapi.LocationPriorityCritical
				}
				_ = h.editor.SetLocationList(
					ed, prio, id,
					textapi.LocationSlice(nil),
				)
			}
		}
	})
}

func (h *Handler) handleStoppedBreakpoint(
	ctx context.Context, ev *dap.StoppedEvent,
) {
	if h.browser == nil || h.editor == nil {
		return
	}
	sid, err := h.requireInitializedOnly()
	if err != nil {
		return
	}
	resp, err := h.dbg.StackTrace(ctx, sid, &dap.StackTraceArguments{
		ThreadId:   ev.Body.ThreadId,
		StartFrame: 0,
		Levels:     20,
	})
	if err != nil {
		if h.notify != nil {
			h.notify(browserapi.LevelWarn, "debugger stack-trace: %v", err)
		}
		return
	}
	if resp == nil || len(resp.StackFrames) == 0 {
		return
	}
	// Push a pre-rendered stack-trace into the session
	// transcript so the user sees the same output as if they
	// had typed `debugger stack-trace`. This is the most
	// common follow-up to a breakpoint hit and surfacing it
	// automatically saves a round-trip.
	h.pushRenderedStackTrace(resp.StackFrames)
	frame := resp.StackFrames[0]
	if frame.Source == nil || frame.Source.Path == "" || frame.Line <= 0 {
		return
	}
	uri, err := workspaceapi.ParseURI("file://" + frame.Source.Path)
	if err != nil {
		if h.notify != nil {
			h.notify(browserapi.LevelWarn, "debugger parse stopped file: %v", err)
		}
		return
	}
	h.mu.Lock()
	h.stoppedFrames = append([]dap.StackFrame(nil), resp.StackFrames...)
	h.stoppedFrame = 0
	h.stoppedThreadID = ev.Body.ThreadId
	h.mu.Unlock()

	frames := resp.StackFrames
	h.runOnUI(func() {
		if err := h.openFrame(ctx, sid, uri, frame, frames); err != nil {
			if h.notify != nil {
				h.notify(browserapi.LevelWarn,
					"debugger open stopped location: %v", err)
			}
		}
	})
}

// openFrame opens the file referenced by frame, focuses its
// window, moves the cursor to the frame's line, installs the
// stopped-line marker covering the full line, and (when a
// parser is configured and frames represents the live stack
// trace) installs the in-scope variables location list.
//
// frames may be nil for navigational jumps that should not
// touch the variables list (those calls re-open already-known
// frames after a stop has already populated everything).
func (h *Handler) openFrame(
	ctx context.Context,
	sid string,
	uri workspaceapi.URI,
	frame dap.StackFrame,
	frames []dap.StackFrame,
) error {
	hd, err := h.browser.Open(uri)
	if err != nil {
		return err
	}
	target, err := h.placeFrameWindow(hd)
	if err != nil {
		return err
	}
	_, _ = h.browser.SetFocus(target)
	ed, err := h.editor.Editor(uri)
	if err != nil {
		return err
	}
	y := frame.Line - 1
	if y < 0 {
		y = 0
	}
	if err := h.setCursor(ed, term.Coordinates{X: 0, Y: y}); err != nil {
		return err
	}
	if frames == nil {
		return nil
	}
	lineLen := h.lineLen(ed, y)
	stopMsg := formatStackTrace(frames)
	stopLoc := textapi.Location{
		From:    term.Coordinates{X: 0, Y: y},
		To:      term.Coordinates{X: lineLen, Y: y},
		Message: stopMsg,
		Icon:    h.cfg.Icons.Stopped,
		Attr: term.Attributes{
			Fg: term.ColorBlack,
			Bg: term.ColorYellow,
		},
	}
	if err := h.editor.SetLocationList(
		ed,
		textapi.LocationPriorityWarning,
		stoppedLocationID,
		textapi.LocationSlice([]textapi.Location{stopLoc}),
	); err != nil {
		return err
	}
	h.recordInstalledLocation(uri, stoppedLocationID)
	h.installVariablesLocations(ctx, sid, ed, uri, frame)
	return nil
}

// setCursor moves ed's cursor to pos, tolerating the editor
// reporting failure because the cursor already sits there.
// text.Handler.SetCursorAtScroll returns false when the cursor
// does not move, which is the common case when the user leaves
// the cursor on the breakpoint line: treating it as fatal would
// skip the stopped marker and the variables overlay.
func (h *Handler) setCursor(
	ed textapi.Handler, pos term.Coordinates,
) error {
	err := h.editor.SetCursor(ed, pos)
	if err == nil {
		return nil
	}
	if cur, cerr := h.editor.Cursor(ed); cerr == nil && cur == pos {
		return nil
	}
	return err
}

// placeFrameWindow installs hd in the first non-floating,
// non-minimized window via SetContent so the stopped/jumped
// frame replaces whatever the user is currently looking at in
// that tile. ErrTabNotFree is treated as success: the tile
// already shows hd's URI, so the cursor move that follows is
// all that is needed.
func (h *Handler) placeFrameWindow(
	hd browserapi.Handler,
) (browser.Window, error) {
	target := h.firstUsableTile()
	if target == nil {
		return nil, errors.New("no browser window available")
	}
	if err := target.SetContent(hd); err != nil &&
		!errors.Is(err, browserapi.ErrTabNotFree) {
		return nil, err
	}
	return target, nil
}

// firstUsableTile returns the first non-floating, non-minimized
// window, or nil when none exists. It is the placement target
// shared by frame opening and output-log placement.
func (h *Handler) firstUsableTile() browser.Window {
	var target browser.Window
	h.browser.IterateWindows(func(w browser.Window) {
		if target != nil || w.IsFloating() {
			return
		}
		if _, minimized := w.IsMinimized(); minimized {
			return
		}
		target = w
	})
	return target
}

// lineLen returns the number of cells in line y of the editor's
// content buffer. Returns 0 when the line cannot be read.
func (h *Handler) lineLen(ed textapi.Handler, y int) int {
	view := h.editor.CellView(ed)
	if view == nil {
		return 0
	}
	rows, err := view.RawCells()
	if err != nil || y < 0 || y >= len(rows) {
		return 0
	}
	return len(rows[y])
}

// formatStackTrace renders frames as a compact human-readable
// stack-trace string used as the Message on the stopped-line
// location. The first frame is the deepest (most recent).
func formatStackTrace(frames []dap.StackFrame) string {
	var b strings.Builder
	for i, f := range frames {
		if i > 0 {
			b.WriteString("\n")
		}
		path := ""
		if f.Source != nil {
			path = f.Source.Path
		}
		fmt.Fprintf(&b, "%d. %s (%s:%d)", i, f.Name, path, f.Line)
	}
	return b.String()
}

// installVariablesLocations queries DAP for the in-scope
// variables of frame, then uses the configured syntax parser
// to map each variable name to its identifier ranges in uri.
// The resulting Locations are installed at critical priority
// so they sit on top of the stopped-line warning marker.
//
// Best-effort: any failure (no parser configured, DAP error,
// no matching identifier) clears the variables list rather
// than leaving stale entries from a previous stop.
func (h *Handler) installVariablesLocations(
	ctx context.Context,
	sid string,
	ed textapi.Handler,
	uri workspaceapi.URI,
	frame dap.StackFrame,
) {
	clear := func() {
		_ = h.editor.SetLocationList(
			ed,
			textapi.LocationPriorityCritical,
			variablesLocationID,
			textapi.LocationSlice(nil),
		)
	}
	scopes, err := h.dbg.Scopes(ctx, sid, &dap.ScopesArguments{
		FrameId: frame.Id,
	})
	if err != nil || len(scopes) == 0 {
		clear()
		if h.notify != nil && err != nil {
			h.notify(browserapi.LevelWarn, "debugger scopes: %v", err)
		}
		return
	}
	values := make(map[string]string)
	types := make(map[string]string)
	for _, scope := range scopes {
		if scope.PresentationHint == "registers" {
			continue
		}
		vars, err := h.dbg.Variables(ctx, sid, &dap.VariablesArguments{
			VariablesReference: scope.VariablesReference,
		})
		if err != nil {
			continue
		}
		for _, v := range vars {
			if v.Name == "" {
				continue
			}
			if _, ok := values[v.Name]; ok {
				continue
			}
			values[v.Name] = v.Value
			types[v.Name] = v.Type
		}
	}
	if len(values) == 0 {
		clear()
		return
	}
	idents, err := h.identifierLocations(uri)
	if err != nil || len(idents) == 0 {
		clear()
		if h.notify != nil {
			if err != nil {
				h.notify(browserapi.LevelWarn, "debugger identifiers: %v", err)
			} else {
				h.notify(browserapi.LevelWarn, "debugger identifiers: empty for %s", uri.Path())
			}
		}
		return
	}
	stopY := frame.Line - 1
	if stopY < 0 {
		stopY = 0
	}
	stopX := frame.Column - 1
	if stopX < 0 {
		stopX = 0
	}
	stopAt := term.Coordinates{X: stopX, Y: stopY}
	// Find the smallest enclosing scope at the stopped
	// position. Identifiers outside this range belong to a
	// different function/block and must not be highlighted —
	// e.g. variables declared in a sibling helper function
	// would otherwise share names with locals at the stop.
	scope, hasScope := h.enclosingScope(uri, stopAt)
	var locs []textapi.Location
	for _, ident := range idents {
		val, ok := values[ident.Text]
		if !ok {
			continue
		}
		if hasScope {
			if !coordInRange(ident.From, scope.From, scope.To) {
				continue
			}
		} else if ident.From.Y > stopY {
			// Fallback when we cannot resolve the scope:
			// keep the coarse "before stop" heuristic.
			continue
		}
		msg := val
		if t := types[ident.Text]; t != "" {
			msg = fmt.Sprintf("%s = %s", t, val)
		}
		locs = append(locs, textapi.Location{
			From:    ident.From,
			To:      ident.To,
			Message: msg,
			Attr:    term.Attributes{Bg: term.ColorGray},
		})
	}
	if len(locs) == 0 {
		clear()
		return
	}
	if err := h.editor.SetLocationList(
		ed,
		textapi.LocationPriorityCritical,
		variablesLocationID,
		textapi.LocationSlice(locs),
	); err != nil && h.notify != nil {
		h.notify(browserapi.LevelWarn,
			"debugger install variables locations: %v", err)
	}
	h.recordInstalledLocation(uri, variablesLocationID)
}

// identifierLocations runs a tree-sitter query against uri to
// return every identifier reference, with its name and source
// coordinates. The returned slice is empty when the parser does
// not support the file's language.
func (h *Handler) identifierLocations(
	uri workspaceapi.URI,
) ([]syntaxapi.Result, error) {
	it, err := h.parser.Query(uri,
		"(identifier) @ref", []string{"ref"})
	if err != nil {
		return nil, err
	}
	defer func() { _ = it.Close() }()
	var out []syntaxapi.Result
	for {
		v, ok := it.Next(context.Background())
		if !ok {
			break
		}
		out = append(out, v)
	}
	return out, nil
}

// enclosingScope returns the smallest tree-sitter scope range
// in uri that contains pos. Returns ok=false when the parser
// reports no scope (e.g. unsupported language). Used to filter
// in-scope variable identifiers so locals from sibling
// functions are not highlighted.
func (h *Handler) enclosingScope(
	uri workspaceapi.URI, pos term.Coordinates,
) (syntaxapi.Result, bool) {
	it, err := h.parser.QueryNode(uri, syntaxapi.NodeCaptureScope)
	if err != nil {
		return syntaxapi.Result{}, false
	}
	defer func() { _ = it.Close() }()
	var best syntaxapi.Result
	have := false
	for {
		v, ok := it.Next(context.Background())
		if !ok {
			break
		}
		if !coordInRange(pos, v.From, v.To) {
			continue
		}
		if !have || rangeContains(best.From, best.To, v.From, v.To) {
			best = v
			have = true
		}
	}
	return best, have
}

// coordInRange reports whether p lies within [from, to)
// (line/column ordering, end-exclusive).
func coordInRange(p, from, to term.Coordinates) bool {
	if p.Y < from.Y || p.Y > to.Y {
		return false
	}
	if p.Y == from.Y && p.X < from.X {
		return false
	}
	if p.Y == to.Y && p.X >= to.X {
		return false
	}
	return true
}

// rangeContains reports whether the range [innerFrom, innerTo)
// is strictly contained within [outerFrom, outerTo) — i.e. the
// inner range is more specific (narrower) than the outer one.
func rangeContains(outerFrom, outerTo, innerFrom, innerTo term.Coordinates) bool {
	if !coordInRange(innerFrom, outerFrom, outerTo) {
		return false
	}
	// innerTo is end-exclusive; treat innerTo - 1 column as the
	// last contained position. Cheap approximation: require
	// innerTo <= outerTo strictly when ranges differ.
	if innerTo.Y < outerTo.Y {
		return true
	}
	if innerTo.Y == outerTo.Y && innerTo.X <= outerTo.X {
		return innerFrom != outerFrom || innerTo != outerTo
	}
	return false
}

// OnClose implements debugapi.EventSubscriber. It marks the
// session ended and closes the iterator's channel. All
// in-memory session state (breakpoints, stop frames,
// installed-location tracking) is dropped so the next
// `debugger initialize` starts from a clean slate rather than
// silently inheriting breakpoints from the previous run.
func (h *Handler) OnClose(reason string) {
	h.clearInstalledLocations()
	// Close the per-session output sink so the next session
	// gets its own log file. The file itself is left on disk
	// for the user to inspect post-mortem.
	h.stopOutputCapture()
	// Release any launch iterator still blocked waiting for
	// InitializedEvent so the user's prompt is not left
	// hanging when the session ends early (adapter crash,
	// debuggee refused to start, etc.).
	h.signalLaunchInit()
	h.mu.Lock()
	ch := h.events
	h.events = nil
	h.sessionID = ""
	h.phase = phaseNone
	h.breakpoints = make(map[string][]int)
	h.stoppedFrames = nil
	h.stoppedFrame = 0
	h.stoppedThreadID = 0
	h.mu.Unlock()
	if ch == nil {
		return
	}
	// Best-effort final event; then close.
	select {
	case ch <- sessionEvent{closed: true, reason: reason}:
	default:
	}
	close(ch)
}

// resetSession clears local debug-session state so a new session
// can be started. It is safe to call when no session is active
// and is used as a recovery path when the adapter rejects RPCs
// like Terminate (e.g. servers that do not implement it). Like
// OnClose, it drops in-memory breakpoints and stop-frame state
// so the next initialize/launch cycle does not silently
// re-submit them.
func (h *Handler) resetSession() {
	h.clearInstalledLocations()
	h.stopOutputCapture()
	h.signalLaunchInit()
	h.mu.Lock()
	ch := h.events
	h.events = nil
	h.sessionID = ""
	h.phase = phaseNone
	h.breakpoints = make(map[string][]int)
	h.stoppedFrames = nil
	h.stoppedFrame = 0
	h.stoppedThreadID = 0
	h.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// sessionIDOrError returns the active sessionID or an error if
// no session is active.
func (h *Handler) sessionIDOrError() (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessionID == "" {
		return "", errNoSession
	}
	return h.sessionID, nil
}

func (h *Handler) phaseLocked() sessionPhase {
	return h.phase
}

// HandleCommand satisfies textapi.REPLHandler.
func (h *Handler) HandleCommand(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if cmd.Name != CommandName {
		return nil, repl.ErrNotFound
	}
	if len(cmd.Args) == 0 {
		return h.Help(ctx, nil)
	}
	sub, rest := cmd.Args[0], cmd.Args[1:]
	return h.dispatch(ctx, sub, rest, pw)
}

// Complete satisfies textapi.REPLHandler.
func (h *Handler) Complete(
	_ context.Context, cmd string, args []string,
) (iterator.Iterator[string], error) {
	if cmd != CommandName {
		return iterator.Empty[string](), nil
	}
	return iterator.FromSlice(h.completeArgs(args)), nil
}

// completeArgs dispatches argument completion to either
// subcommand completion or, when the user is typing
// `debugger initialize <prefix>`, the configured adapter list.
func (h *Handler) completeArgs(args []string) []string {
	if len(args) <= 1 {
		return completeSubcommands(args)
	}
	if args[0] == subInitialize && len(args) == 2 {
		return h.completeAdapters(args[1])
	}
	return nil
}

// completeAdapters returns the configured adapter language IDs
// matching prefix, sorted lexicographically. Reads the adapter
// map straight from the configured idedebug.Config.
func (h *Handler) completeAdapters(prefix string) []string {
	if len(h.cfg.Debugger.Adapters) == 0 {
		return nil
	}
	langs := make([]string, 0, len(h.cfg.Debugger.Adapters))
	for lang := range h.cfg.Debugger.Adapters {
		if strings.HasPrefix(lang, prefix) {
			langs = append(langs, lang)
		}
	}
	sort.Strings(langs)
	return langs
}

// Help satisfies textapi.REPLHandler.
func (h *Handler) Help(
	_ context.Context, _ []string,
) (iterator.Iterator[component.Responsive], error) {
	return helpLines(), nil
}
