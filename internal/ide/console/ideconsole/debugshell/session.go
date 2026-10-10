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

package debugshell

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/google/go-dap"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/text"
)

// breakpointsLocationID identifies the breakpoints location list
// installed on editor handlers.
const breakpointsLocationID = "debugger/breakpoints"

// PromptHandler adapts *Handler to text.CommandHandler so the
// debugger command can be dispatched from the editor command
// prompt. The only supported subcommand in the prompt is
// "set-breakpoint", which installs a breakpoint at the cursor's
// current line.
type PromptHandler struct {
	h         *Handler
	openShell func(ctx context.Context, args ...string) error
}

// NewPromptHandler returns a text.CommandHandler that routes
// ":debugger <sub>" invocations from the editor command prompt
// to h. Callers typically register it via
// text.Editor.SubscribeCommand(Manual(), NewPromptHandler(h)).
func NewPromptHandler(h *Handler) PromptHandler {
	return PromptHandler{h: h}
}

// WithOpenShell wires a callback used when ":debugger" is invoked
// with no arguments. The callback typically opens (or focuses) the
// companion shell tab and submits "debugger" on its prompt, making
// the bare ":debugger" command an alias for ":shell debugger".
// When unset, the no-args invocation returns the legacy usage
// error.
func (p PromptHandler) WithOpenShell(
	fn func(ctx context.Context, args ...string) error,
) PromptHandler {
	p.openShell = fn
	return p
}

var _ text.CommandHandler = PromptHandler{}

// HandleCommand satisfies text.CommandHandler.
func (p PromptHandler) HandleCommand(ctx context.Context, cmd textapi.Command) error {
	if cmd.Name != CommandName {
		return fmt.Errorf("unexpected command: %s", cmd.Name)
	}
	if len(cmd.Args) == 0 {
		if p.openShell != nil {
			return p.openShell(ctx, CommandName)
		}
		return errors.New("usage: debugger <subcommand> [...]")
	}
	switch cmd.Args[0] {
	case subSetBreakpoint:
		return p.h.setBreakpointAt(ctx, cmd)
	case subJump:
		return p.h.jumpStackFrame(ctx, cmd)
	default:
		return fmt.Errorf(
			"subcommand %q is only available from the debug shell; "+
				"open it with the shell and run `debugger help`", cmd.Args[0])
	}
}

// Complete satisfies text.CommandHandler.
func (p PromptHandler) Complete(
	_ context.Context, cmd textapi.Command,
) (iterator.Iterator[string], string, error) {
	return iterator.FromSlice(completePromptSubcommands(cmd.Args)), "", nil
}

// setBreakpointAt toggles a breakpoint at the current cursor
// line in cmd.Resource. Breakpoints are accumulated in memory per
// source file. Once `debugger configured` is sent, the full list
// is replayed via SetBreakpoints so DAP semantics are preserved.
func (h *Handler) setBreakpointAt(ctx context.Context, cmd textapi.Command) error {
	if cmd.Resource == nil {
		return errors.New("no editor in focus")
	}
	editorHandler, ok := cmd.Resource.(text.Handler)
	if !ok {
		return errors.New("editor handler does not support location lists")
	}
	path := cmd.URI.Path()
	if path == "" {
		return errors.New("editor resource has no file path")
	}
	// DAP lines are 1-based; cmd.Cursor.Content.Y is 0-based.
	line := cmd.Cursor.Content.Y + 1

	sid, err := h.requireInitializedOnly()
	if err != nil {
		return err
	}

	// Validate the requested line client-side using the
	// configured parser. This rejects breakpoints set on the
	// closing brace of a function (or any other position with
	// no statement at/after it) before they reach the
	// adapter, where Delve would otherwise silently fail to
	// bind and let the debuggee run to completion. The
	// returned line is the actual location the breakpoint
	// will bind on; it is used as both the in-memory state
	// and the marker location so the user sees exactly where
	// the breakpoint took effect.
	adjusted, nerr := h.normalizeBreakpointLine(path, line)
	if nerr != nil {
		return fmt.Errorf("set-breakpoint: %w", nerr)
	}
	line = adjusted

	h.mu.Lock()
	lines := h.breakpoints[path]
	// toggle: remove if present, else append
	idx := -1
	for i, l := range lines {
		if l == line {
			idx = i
			break
		}
	}
	if idx >= 0 {
		lines = slices.Delete(lines, idx, idx+1)
	} else {
		lines = append(lines, line)
	}
	sort.Ints(lines)
	h.breakpoints[path] = lines
	current := append([]int(nil), lines...)
	h.mu.Unlock()

	h.mu.Lock()
	phase := h.phaseLocked()
	h.mu.Unlock()
	if phase == phaseConfigured {
		sbps := make([]dap.SourceBreakpoint, 0, len(current))
		for _, l := range current {
			sbps = append(sbps, dap.SourceBreakpoint{Line: l})
		}
		_, err = h.dbg.SetBreakpoints(ctx, sid, &dap.SetBreakpointsArguments{
			Source:      dap.Source{Path: path},
			Breakpoints: sbps,
		})
		if err != nil {
			return fmt.Errorf("set-breakpoint: %w", err)
		}
	}

	locs := make([]textapi.Location, 0, len(current))
	for _, l := range current {
		y := l - 1
		locs = append(locs, textapi.Location{
			From:    term.Coordinates{X: 0, Y: y},
			To:      term.Coordinates{X: 0, Y: y},
			Message: "breakpoint",
			Icon:    h.cfg.Icons.Breakpoint,
			Attr:    term.Attributes{Bg: term.ColorRed},
		})
	}
	editorHandler.SetLocationList(
		textapi.LocationPriorityInfo,
		breakpointsLocationID,
		text.LocationSlice(locs),
	)
	h.recordInstalledLocation(cmd.URI, breakpointsLocationID)
	return nil
}

// jumpStackFrame moves the cursor to the next or previous stack
// frame from the most recent stop. It is purely navigational —
// no DAP step-in/step-out request is issued. The frame's source
// file is opened if it is not already in focus, mirroring the
// behaviour used when processing a `stopped` event.
//
// Direction follows program execution: `forward` walks toward
// the deepest (most-recently-entered) frame, `backward` walks
// toward the caller. Frames are indexed with the deepest at 0,
// so `backward` increments the index and `forward` decrements
// it.
func (h *Handler) jumpStackFrame(
	ctx context.Context, cmd textapi.Command,
) error {
	if len(cmd.Args) < 2 {
		return fmt.Errorf("usage: debugger jump <forward|backward>")
	}
	dir := cmd.Args[1]
	if dir != jumpForward && dir != jumpBackward {
		return fmt.Errorf("debugger jump: unknown direction %q", dir)
	}
	sid, err := h.requireInitializedOnly()
	if err != nil {
		return err
	}
	h.mu.Lock()
	frames := append([]dap.StackFrame(nil), h.stoppedFrames...)
	idx := h.stoppedFrame
	h.mu.Unlock()
	if len(frames) == 0 {
		return errors.New("debugger jump: no stack trace available; " +
			"trigger a breakpoint first")
	}
	if dir == jumpBackward {
		idx++
	} else {
		idx--
	}
	if idx < 0 {
		return errors.New("debugger jump: already at the deepest frame")
	}
	if idx >= len(frames) {
		return errors.New("debugger jump: already at the outermost frame")
	}
	frame := frames[idx]
	if frame.Source == nil || frame.Source.Path == "" || frame.Line <= 0 {
		return fmt.Errorf("debugger jump: frame %d has no source", idx)
	}
	uri, err := workspaceapi.ParseURI("file://" + frame.Source.Path)
	if err != nil {
		return fmt.Errorf("debugger jump: parse %s: %w", frame.Source.Path, err)
	}
	if h.browser == nil || h.editor == nil {
		return errors.New("debugger jump: no editor wired")
	}
	// PromptHandler.HandleCommand already runs on the GUI
	// event-loop goroutine — call openFrame directly so the
	// cursor moves before the next render tick.
	if err := h.openFrame(ctx, sid, uri, frame, nil); err != nil {
		return fmt.Errorf("debugger jump: %w", err)
	}
	h.mu.Lock()
	h.stoppedFrame = idx
	h.mu.Unlock()
	return nil
}
