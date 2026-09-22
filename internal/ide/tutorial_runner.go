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

package ide

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"

	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"

	"unstable.build/rune/internal/ide/idetutorial"
	"unstable.build/rune/internal/text"
)

type tutorialRunner struct {
	tui.Handler
	tutorials      map[string]idetutorial.Tutorial
	overlay        *idetutorial.Handler
	browserOverlay *idetutorial.OverlayBrowser
	activeName     string
	interrupter    term.Interrupter
	width, height  int
	onCompleted    func(name string)
	exitRequested  func(term.Event) bool
	// active mirrors overlay != nil for readers off the event loop,
	// such as the package-install gate called from syntax and LSP
	// goroutines.
	active atomic.Bool
}

var _ commandObserver = (*tutorialRunner)(nil)

// init wires the runner. exitRequested decides whether an event is a
// quit request that must abort a running tutorial; it is required
// because without it the tutorial overlay silently makes the IDE
// unquittable.
func (r *tutorialRunner) init(
	root tui.Handler,
	tutorials map[string]idetutorial.Tutorial,
	browserOverlay *idetutorial.OverlayBrowser,
	interrupter term.Interrupter,
	onCompleted func(name string),
	exitRequested func(term.Event) bool,
) {
	if exitRequested == nil {
		panic("tutorialRunner: exitRequested is required")
	}
	r.Handler = root
	r.tutorials = tutorials
	r.browserOverlay = browserOverlay
	r.interrupter = interrupter
	r.onCompleted = onCompleted
	r.exitRequested = exitRequested
}

func (r *tutorialRunner) setActive(name string, t idetutorial.Tutorial) {
	if r.overlay != nil {
		r.clearActive()
	}
	r.overlay = idetutorial.New(r.Handler, t, r.browserOverlay, r.interrupter)
	r.activeName = name
	r.active.Store(true)
	if r.width > 0 && r.height > 0 {
		r.overlay.Resize(r.width, r.height)
	}
	r.overlay.Reset()
}

func (r *tutorialRunner) clearActive() {
	if r.overlay != nil {
		_ = r.overlay.Close()
	}
	r.overlay = nil
	r.activeName = ""
	r.active.Store(false)
}

func (r *tutorialRunner) finishActive() {
	if r.overlay == nil {
		return
	}
	name := r.activeName
	completed := r.overlay.Completed()
	r.clearActive()
	if completed && r.onCompleted != nil {
		r.onCompleted(name)
	}
}

// register adds a tutorial under name and reports whether it was added. It
// returns false without overwriting when a tutorial is already registered
// under name. It must be called on the event loop because the tutorials map
// is read there by HandleCommand and Complete.
func (r *tutorialRunner) register(name string, t idetutorial.Tutorial) bool {
	if _, ok := r.tutorials[name]; ok {
		return false
	}
	if r.tutorials == nil {
		r.tutorials = make(map[string]idetutorial.Tutorial, 1)
	}
	r.tutorials[name] = t
	return true
}

func (r *tutorialRunner) has(name string) bool {
	_, ok := r.tutorials[name]
	return ok
}

// running reports whether a tutorial overlay is currently active. It
// is safe to call from any goroutine.
func (r *tutorialRunner) running() bool {
	return r.active.Load()
}

func (r *tutorialRunner) Resize(width, height int) {
	r.width, r.height = width, height
	if r.overlay != nil {
		r.overlay.Resize(width, height)
		return
	}
	r.Handler.Resize(width, height)
}

func (r *tutorialRunner) Draw(w term.Writer) {
	if r.overlay != nil {
		r.overlay.Draw(w)
		return
	}
	r.Handler.Draw(w)
}

func (r *tutorialRunner) Handle(ev term.Event) (bool, bool) {
	if r.overlay == nil {
		return r.Handler.Handle(ev)
	}
	// The overlay swallows stray keys so they cannot reach the IDE
	// underneath, which would otherwise make the app unquittable
	// mid-tutorial. Abort the tutorial first so the confirm-exit
	// prompt is drawn and answerable.
	if r.exitRequested(ev) {
		r.finishActive()
		return r.Handler.Handle(ev)
	}
	// A command dispatched from this frame reaches observeCommand
	// reentrantly and may already have cleared the overlay, so
	// compare identity rather than dereferencing r.overlay again.
	overlay := r.overlay
	exit, handled := overlay.Handle(ev)
	// finishActive closes the overlay, so it must run after its
	// Handle frame has returned.
	if r.overlay == overlay && overlay.Finished() {
		r.finishActive()
	}
	return exit, handled
}

func (r *tutorialRunner) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	if r.overlay != nil {
		return r.overlay.Cursor()
	}
	return r.Handler.Cursor()
}

func (r *tutorialRunner) Selection() (string, bool) {
	if r.overlay != nil {
		return r.overlay.Selection()
	}
	return r.Handler.Selection()
}

func (r *tutorialRunner) Close() error {
	r.clearActive()
	return nil
}

func (r *tutorialRunner) setDefaultAttributes(defAttr term.Attributes) {
	for _, tut := range r.tutorials {
		tut.SetDefaultAttributes(defAttr)
	}
	if r.overlay != nil {
		r.overlay.SetDefaultAttributes(defAttr)
	}
}

func (r *tutorialRunner) observeCommand(
	typed, resolved string, args []string, err error,
) {
	if r.overlay == nil {
		return
	}
	// The tutorial's floating windows draw over the confirm-exit
	// prompt, so a user who typed :quit would be answering a prompt
	// they cannot see. Abort the tutorial instead.
	if err == nil && isExitCommand(resolved) {
		r.finishActive()
		return
	}
	exit := r.overlay.ObserveCommand(typed, resolved, args, err)
	if exit {
		r.finishActive()
	}
}

func (r *tutorialRunner) observeEvent(eventType, uri string) {
	if r.overlay == nil {
		return
	}
	if r.overlay.ObserveEvent(eventType, uri) {
		r.finishActive()
	}
}

// tutorialEventObserver builds the text-event subscriber that feeds the
// tutorial runner. Text/LSP events are delivered on background
// subscriber goroutines, but the runner is otherwise only driven from
// the event loop (Handle/HandleCommand); observe therefore hops onto
// the loop via schedule so the runner's active overlay is never torn
// from two goroutines at once. schedule must be the host's
// event-loop scheduler.
func tutorialEventObserver(
	schedule func(func()) bool, observe func(eventType, uri string),
) text.EventHandler {
	return text.FuncEventHandler(func(_ context.Context, ev textapi.Event) bool {
		eventType, uri := ev.Type.String(), ev.URI.String()
		schedule(func() { observe(eventType, uri) })
		return false
	})
}

func (r *tutorialRunner) HandleCommand(_ context.Context, cmd textapi.Command) error {
	if len(cmd.Args) == 0 {
		return errors.New(
			"usage: tutorial <start|stop> [<name>]")
	}
	switch cmd.Args[0] {
	case "start":
		if len(cmd.Args) != 2 {
			return errors.New("usage: tutorial start <name>")
		}
		name := cmd.Args[1]
		tut, ok := r.tutorials[name]
		if !ok {
			return fmt.Errorf("unknown tutorial %q", name)
		}
		r.setActive(name, tut)
		return nil
	case "stop":
		if len(cmd.Args) > 1 {
			return fmt.Errorf("tutorial stop: takes no arguments, got %d",
				len(cmd.Args)-1)
		}
		if r.overlay == nil {
			return errors.New("no tutorial is running")
		}
		r.clearActive()
		return nil
	}
	return fmt.Errorf("unknown subcommand %q: "+
		"want one of start, stop", cmd.Args[0])
}

func (r *tutorialRunner) Complete(_ context.Context, cmd textapi.Command) (
	iterator.Iterator[string], string, error,
) {
	// First argument: list subcommands.
	if len(cmd.Args) <= 1 {
		return iterator.FromSlice([]string{
			"start", "stop",
		}), "", nil
	}
	// Second argument after `start`: tutorial names.
	if len(cmd.Args) == 2 {
		switch cmd.Args[0] {
		case "start":
			names := make([]string, 0, len(r.tutorials))
			for n := range r.tutorials {
				names = append(names, n)
			}
			sort.Strings(names)
			return iterator.FromSlice(names), "", nil
		}
	}
	return iterator.Empty[string](), "", nil
}
