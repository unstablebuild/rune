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
	"sync/atomic"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/ide/idetutorial"
)

// requestKind selects which builtin enqueued a request and which
// per-kind dispatch path runs in the TUI loop.
type requestKind int

const (
	// Blocking UI: live in Tutorial.active and gate Draw/Handle.
	reqFloatingWindow requestKind = iota + 1
	reqMarkdown
	reqWaitKey
	reqWaitCommand
	reqWaitShell
	reqWaitEvent
	reqConfirm
	reqChoice
)

// String renders the requestKind for diagnostics and test assertions.
func (k requestKind) String() string {
	switch k {
	case reqFloatingWindow:
		return "floating_window"
	case reqMarkdown:
		return "markdown"
	case reqWaitKey:
		return "wait_key"
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

	// floating_window / markdown.
	text, title string
	offset      term.Coordinates
	md          *markdown.Component

	// align overrides where the step's window is anchored. Zero means
	// the per-kind default. wait_* steps accept it too so a lesson can
	// keep its page and its hint on the same side of the screen,
	// clear of the layout the step asks the user to work with.
	align component.Alignment

	// win is the overlay-browser window backing this request, opened
	// at publish time on the run goroutine and closed on resolve,
	// Stop, or the user's window-bar ✕ click. winContent tracks the
	// live screen size for the window's Dimensions.
	win        browser.Window
	winContent *floatingWindowContent
	// winClosed is stamped by the content's close callback while the
	// overlay-browser lock is held; the TUI loop reaps it in Handle
	// by resolving the request. Atomic because a Stop-driven close
	// can stamp it from the run goroutine.
	winClosed atomic.Bool
	// skipRequested is stamped by the content's "Skip"
	// button while the overlay-browser lock is held; the TUI loop
	// reaps it in Handle (or via the scheduled exitOnSkip tick) by
	// stopping the run.
	skipRequested atomic.Bool

	// stepNum is the 1-based "visible content" step number snapshot
	// at publish time. Only reqFloatingWindow and reqMarkdown bump
	// the counter; other request kinds inherit the prior value but
	// only floating_window renders it in the title bar.
	stepNum int

	// allowKeys is the set of key combinations that the floating
	// window does NOT swallow: matching events fall through to the
	// IDE root so the user can e.g. press <meta-1>..<meta-9> to
	// switch workspace slots while reading instructions that mention
	// those very bindings. Empty for every other kind.
	allowKeys []term.KeyComb

	// dismissKeys behaves like allowKeys but ALSO resolves the
	// floating window: matching events dismiss the request and
	// reach the IDE root. Authors use this for read-then-act keys
	// such as the command-prompt key when the next step expects
	// the prompt to be open.
	dismissKeys []term.KeyComb

	// wait_key.
	waitKey string

	// wait_command: the awaited command, optionally argument-
	// qualified ("! git log"). Matching only ever uses the command
	// name; the arguments narrow the key the hint offers, so a step
	// that asks for an invocation with arguments does not advertise
	// a key bound to the bare command.
	command string
	onError string

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

	// hasShader arms the floating_window hint pulse after a stray
	// keystroke; the spec itself is derived lazily from the live
	// window geometry and cached until the geometry or theme
	// changes.
	shaderSpec  idetutorial.Shader
	hasShader   bool
	shaderBuilt bool
	shaderPos   term.Coordinates
	shaderW     int
	shaderH     int

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
