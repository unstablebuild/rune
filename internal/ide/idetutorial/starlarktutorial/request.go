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

package starlarktutorial

import (
	"sync"

	"github.com/unstablebuild/rune-go-sdk/handler"

	mdhandler "unstable.build/rune/internal/handler/markdown"
)

// requestKind selects which builtin enqueued a request and which
// per-kind dispatch path runs in the TUI loop.
type requestKind int

const (
	// Blocking screens: live in Tutorial.active and gate Draw/Handle.
	reqWaitCommand requestKind = iota + 1
	reqWaitShell
	reqWaitEvent
	reqConfirm
	reqChoice
)

// String renders the requestKind for diagnostics and test assertions.
func (k requestKind) String() string {
	switch k {
	case reqWaitCommand:
		return "wait_command"
	case reqWaitShell:
		return "wait_shell"
	case reqWaitEvent:
		return "wait_event"
	case reqConfirm:
		return "confirm"
	case reqChoice:
		return "choice"
	}
	return "unknown"
}

// request is the message a blocking Starlark builtin posts to the
// TUI loop. The TUI loop drains active, performs per-kind Draw and
// Handle work, and delivers exactly one response by reading from
// respond. once guards a single delivery so adapters that fire
// OnSelect followed by OnClose never panic on a closed channel and
// the second callback becomes a no-op.
type request struct {
	kind requestKind

	// title heads the screen; text is the step's markdown copy. Both
	// are optional for the wait_* kinds, which fall back to a
	// generated hint when text is empty.
	text, title string

	// viewer is the markdown viewer of a wait_* screen, prompt the
	// in-tile prompt of a confirm/choice screen; exactly one is set.
	// body wraps whichever is set with the screen's padding and is
	// what Tutorial.Draw/Resize/Handle actually drive. All are built
	// at publish time on the run goroutine and only ever touched by
	// the TUI loop afterwards.
	viewer *mdhandler.Handler
	prompt *handler.Prompt
	body   *handler.Span

	// closed is stamped when a prompt screen resolves itself (a
	// selection or Esc); the TUI loop reads it right after routing
	// the event to decide whether to resolve the request.
	closed bool

	// wait_command: the awaited command, optionally argument-
	// qualified ("! git log"). Matching only ever uses the command
	// name; the arguments narrow the key the hint offers, so a step
	// that asks for an invocation with arguments does not advertise
	// a key bound to the bare command.
	command string

	// wait_event: the awaited editor event-type name (e.g. "open").
	event string

	// wait_event: optional substring the observed event URI must
	// contain. Empty matches any URI.
	eventURI string

	// wait_shell: the expected companion-shell argument tokens (e.g.
	// ["pkg", "install", "rune-agent"]). Always non-empty — wait_shell
	// is exclusively for commands run inside the shell.
	shellArgs []string

	// choice / confirm.
	message string
	options []string
	// pendingResp / pendingSelected capture the prompt's OnSelect
	// outcome so Tutorial.Handle can resolve with the correct
	// response after the barrier is in place. pendingSelected stays
	// false on a pure dismissal (Esc) and the per-kind dismissal
	// response is synthesised at resolve time.
	pendingResp     response
	pendingSelected bool

	respond chan response
	once    sync.Once
}

// deliver sends res to respond exactly once. Subsequent calls are
// no-ops so a prompt that fires both OnSelect and OnClose on a normal
// selection cannot deadlock the runLoop nor panic on a closed channel.
func (r *request) deliver(res response) {
	r.once.Do(func() {
		r.respond <- res
	})
}

// response carries the user-visible result of a blocking builtin
// back to the runLoop. Unused fields are zero values; each builtin
// reads only the fields it needs.
type response struct {
	// wait_command.
	cmdName string
	cmdArgs []string

	// choice.
	selectedIdx   int
	selectedValue string
	selected      bool

	// confirm.
	confirmed bool
}
