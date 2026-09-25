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
	"fmt"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/debug"
)

var _ pluginHandler = (*asyncPlugin)(nil)

// asyncPlugin is a permanent pluginHandler wrapper that runs the
// blocking plugin build (plugin.New -> vte.NewHandler -> NewPty and
// StartCommand RPCs) off the host event loop. The floating window
// opens immediately with a loading animation; once the build settles
// the wrapper forwards every call to the real handler and replays the
// queued resize/focus/key operations.
//
// All mutable state is event-loop-owned: the factory goroutine hands
// its result back through ScheduleNextTick, so no locking is needed.
type asyncPlugin struct {
	e *ex
	// title mirrors plugin.Handler's default title (the joined argv)
	// so the floating window bar is identical before and after the
	// swap.
	title    string
	maxWidth int

	anim        *component.Animation
	animStopped bool

	real    pluginHandler
	err     error
	errComp component.String
	closed  bool

	width, height int
	resized       bool

	queuedEvents []term.Event
	// queuedFocus preserves every pre-ready focus transition in
	// order: consumers such as the ephemeral-close logic count
	// individual transitions, so collapsing to the last value would
	// change behavior.
	queuedFocus []bool
}

// newAsyncPlugin returns immediately with a placeholder handler and
// runs factory on a background goroutine. The result is installed on
// the host event loop via e.sched.
func newAsyncPlugin(
	e *ex, title string, maxWidth int,
	factory func() (pluginHandler, error),
) *asyncPlugin {
	ap := &asyncPlugin{e: e, title: title, maxWidth: maxWidth}
	frames, sequence := component.ProgressAnimationFrames()
	ap.anim = component.NewAnimation(
		browser.EventPublisherInterrupter(e.Browser()), frames, sequence, 0)
	ap.anim.Resize(2, 1)
	e.asyncVTELoads.Add(1)
	go debug.CapturePanicReport(func() {
		defer e.asyncVTELoads.Done()
		h, ferr := factory()
		if !e.sched(func() { ap.complete(h, ferr) }) {
			// The event loop is gone: nothing will ever install the
			// result, so release the freshly-built handler.
			if ferr == nil && h != nil {
				_ = h.Close()
			}
			ap.stopAnimation()
		}
	})
	return ap
}

// complete installs the factory result. It runs on the host event
// loop.
func (ap *asyncPlugin) complete(h pluginHandler, err error) {
	if ap.closed {
		// Only a successful build hands over an owned, fully
		// initialized handler; closing anything else is unsafe.
		if err == nil && h != nil {
			_ = h.Close()
		}
		return
	}
	ap.stopAnimation()
	if err != nil {
		ap.err = err
		ap.errComp = component.NewStringWithConfig(
			fmt.Sprintf("%s: %v", ap.title, err),
			component.StringConfig{Alignment: component.AlignmentCentered})
		ap.errComp.Resize(ap.width, ap.height)
		_, _ = ap.e.notifications.Notify(browserapi.LevelError,
			"%s: %v", ap.title, err)
		return
	}
	ap.real = h
	if ap.resized {
		h.Resize(ap.width, ap.height)
	}
	for _, focus := range ap.queuedFocus {
		h.OnFocusChange(focus)
	}
	ap.queuedFocus = nil
	for _, ev := range ap.queuedEvents {
		_, _ = h.Handle(ev)
	}
	ap.queuedEvents = nil
}

func (ap *asyncPlugin) stopAnimation() {
	if ap.animStopped {
		return
	}
	ap.animStopped = true
	_ = ap.anim.Close()
}

func (ap *asyncPlugin) Draw(w term.Writer) {
	if ap.real != nil {
		ap.real.Draw(w)
		return
	}
	if ap.err != nil {
		ap.errComp.Draw(w)
		return
	}
	dx := max(0, (ap.width-2)/2)
	dy := max(0, ap.height/2)
	ap.anim.Draw(translateWriter{w: w, dx: dx, dy: dy})
}

func (ap *asyncPlugin) Resize(width, height int) {
	ap.width, ap.height = width, height
	ap.resized = true
	if ap.real != nil {
		ap.real.Resize(width, height)
		return
	}
	if ap.err != nil {
		ap.errComp.Resize(width, height)
	}
}

func (ap *asyncPlugin) Handle(ev term.Event) (exit, handled bool) {
	if ap.real != nil {
		return ap.real.Handle(ev)
	}
	if ev.Type != term.EventKey {
		return false, false
	}
	// Esc / ctrl-c abandon the pending window; complete then closes
	// the freshly-built handler, which kills the spawned process.
	if ev.Key == term.KeyEsc || (ev.Ch == 'c' && ev.Mod == term.ModCtrl) {
		return true, true
	}
	if len(ap.queuedEvents) < maxQueuedAsyncVTEEvents {
		ap.queuedEvents = append(ap.queuedEvents, ev)
	}
	// Queued but unclaimed, mirroring asyncVTE: global bindings keep
	// working while the spawn is in flight.
	return false, false
}

func (ap *asyncPlugin) Cursor() (
	c term.Coordinates, s term.CursorStyle, show bool,
) {
	if ap.real != nil {
		return ap.real.Cursor()
	}
	return
}

func (ap *asyncPlugin) Selection() (string, bool) {
	if ap.real != nil {
		return ap.real.Selection()
	}
	return "", false
}

// Dimensions mirrors plugin.Handler's pre-completion interactive
// sizing so the floating window does not jump when the real handler
// lands.
func (ap *asyncPlugin) Dimensions() (int, int) {
	if ap.real != nil {
		return ap.real.Dimensions()
	}
	width := int(float64(ap.maxWidth) * 0.8)
	return width, width * 9 / 16
}

func (ap *asyncPlugin) OnFocusChange(inFocus bool) {
	if ap.real != nil {
		ap.real.OnFocusChange(inFocus)
		return
	}
	if ap.closed {
		return
	}
	ap.queuedFocus = append(ap.queuedFocus, inFocus)
}

func (ap *asyncPlugin) Title() string {
	if ap.real != nil {
		return ap.real.Title()
	}
	return ap.title
}

func (ap *asyncPlugin) Close() error {
	if ap.closed {
		return nil
	}
	ap.closed = true
	ap.stopAnimation()
	if ap.real != nil {
		return ap.real.Close()
	}
	// Pre-ready: complete observes ap.closed and closes the
	// freshly-built handler silently (the user abandoned the window).
	return nil
}

// translateWriter offsets every coordinate before forwarding so a
// centered animation can be placed without modifying its layout.
type translateWriter struct {
	w      term.Writer
	dx, dy int
}

func (t translateWriter) Context() context.Context { return t.w.Context() }
func (t translateWriter) SetCell(pos term.Coordinates, c term.Cell) {
	t.w.SetCell(term.Coordinates{X: pos.X + t.dx, Y: pos.Y + t.dy}, c)
}
func (t translateWriter) UnionAttributes(pos term.Coordinates, attr term.Attributes) {
	t.w.UnionAttributes(term.Coordinates{X: pos.X + t.dx, Y: pos.Y + t.dy}, attr)
}
func (t translateWriter) DrawImage(img term.Image) bool {
	return t.w.DrawImage(img.Translated(term.Coordinates{X: t.dx, Y: t.dy}))
}
