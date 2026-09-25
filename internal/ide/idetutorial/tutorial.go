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

// Package idetutorial hosts interactive tutorials in a tile laid out
// beside the IDE's workspaces.
package idetutorial

import (
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// PromptStyle styles the option buttons a tutorial draws: the Skip and
// Stop buttons of the tile and the options of a confirm or choice step.
// It mirrors the prompts the IDE opens elsewhere.
type PromptStyle struct {
	TextAttr       term.Attributes
	HighlightAttr  term.Attributes
	BackgroundAttr term.Attributes
}

// Tutorial is the in-process state machine driving a single tutorial
// run. Implementations draw the active step as the body of the tile,
// which frames and scrolls it like any other window. A step
// only ends when its milestone is met (a command is dispatched, an
// event is observed, a prompt is answered) or when the user skips it;
// Handle therefore never returns exit=true, since that would close the
// tile. Reset zeroes runtime progress so the same instance can be
// dispatched again; host services and parsed content are retained.
type Tutorial interface {
	handler.Scrollable
	Reset()

	// Stop tears down any background work the tutorial owns (the
	// Starlark goroutine, the active step's content, etc.) without
	// implying that the tutorial will be re-run. Stop must be safe
	// to call multiple times and on a tutorial that never ran.
	Stop()

	// Skip resolves the active step as though the user had performed
	// it, advancing the run. It is a no-op when no step is active.
	// Returns exit=true when the skipped step was the last one. Must
	// be called on the event loop.
	Skip() (exit bool)

	// Back shows the screen before the one on show, wrapping from the
	// oldest back to the live step. It rewinds nothing: the live step
	// stays armed while an earlier screen is read. Reports whether the
	// tile now shows a different screen. Must be called on the event
	// loop.
	Back() bool

	// Forward shows the screen after the one on show, landing on the
	// live step from the newest screen already read. Reports whether
	// the tile now shows a different screen; false when the live
	// screen is already on show. Must be called on the event loop.
	Forward() bool

	// ViewingPast reports whether the tile shows a screen the user
	// paged back to rather than the one the tutorial is on.
	ViewingPast() bool

	// Finished reports whether the most recent run has ended, for
	// any reason: it returned, failed, or was stopped.
	Finished() bool

	// Completed reports whether the most recent run reached a normal
	// return.
	Completed() bool

	// PromptActive reports whether the screen on show is a question
	// waiting for an answer. Its options are walked with the arrow
	// keys and taken with Enter, so the host hands those keys to the
	// tutorial while one is up instead of to the workspace.
	PromptActive() bool

	// ObserveCommand reports a dispatched IDE command to the tutorial.
	// typed is the user-typed name (possibly an alias), resolved is the
	// alias-expanded target, args are positional arguments, and err is
	// the dispatch result. When err is non-nil the tutorial state must
	// not advance. Returns exit=true when the tutorial finishes as a
	// result of this observation.
	ObserveCommand(typed, resolved string, args []string, err error) (exit bool)

	// ObserveEvent reports an observed editor event to the tutorial.
	// eventType is the lowercase event-type name (e.g. "open") and uri
	// is the affected document URI. Returns exit=true when the tutorial
	// finishes as a result of this observation.
	ObserveEvent(eventType, uri string) (exit bool)
}
