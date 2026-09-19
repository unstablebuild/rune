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
	"fmt"
	"sync/atomic"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/component/markdown"
	mdhandler "unstable.build/rune/internal/handler/markdown"
)

var _ browser.ScrollableFloating = (*floatingWindowContent)(nil)

// skipButtonLabel is the "Skip" button text drawn on the last row of
// every step window. The label is padded so it reads as a button
// rather than part of the step copy.
const skipButtonLabel = " Skip "

// skipButtonAttr renders the button in reverse video so it reads as a
// clickable affordance under any theme.
var skipButtonAttr = term.Attributes{Attrs: term.AttrReverse}

// floatingWindowContent is the browser-window content of a
// floating_window step or a wait_* hint: the step's markdown body
// behind a less-like mouse-scrollable viewer. Dimensions reproduces
// the historical 60%-of-screen width heuristic from the live screen
// size; the window manager re-queries it every draw so terminal
// resizes reflow the window automatically.
type floatingWindowContent struct {
	*mdhandler.Handler
	span *handler.Span
	// screen packs the last known screen size (width<<32 | height).
	// Written by Tutorial.Resize on the TUI loop and read during
	// window layout under the overlay-browser lock, possibly from
	// the run goroutine at open time.
	screen atomic.Uint64
	// promptAware caps the window height so it stays clear of the
	// command prompt's anchor row. It only applies to a step that
	// asks the user to open the prompt from a bottom-anchored hint;
	// any other step would just be truncating its own instructions.
	promptAware bool
	// onClose runs when the browser releases the content: the
	// window-bar close click or a programmatic window close. It must
	// only stamp state — it is called while the overlay-browser lock
	// is held.
	onClose func()
	// onSkip runs when the user clicks the "Skip" button on
	// the content's last row. The same overlay-browser lock rule
	// applies as for onClose.
	onSkip func()
	// width and height record the last Resize dimensions so Draw and
	// Handle can share the button's row geometry.
	width, height int
}

func newFloatingWindowContent(
	md *markdown.Component, width, height int,
	onClose, onSkip func(),
) *floatingWindowContent {
	mdh := mdhandler.New(md)
	c := &floatingWindowContent{
		Handler: mdh,
		span: handler.NewSpan(mdh, component.SpanConfig{
			PadHorizontal: 2,
			PadVertical:   1,
			ContentAlignment: component.AlignmentBottom |
				component.AlignmentHorizontallyCentered,
		}),
		onClose: onClose,
		onSkip:  onSkip,
	}
	c.setScreen(width, height)
	return c
}

func (c *floatingWindowContent) setScreen(width, height int) {
	c.screen.Store(uint64(uint32(width))<<32 | uint64(uint32(height)))
}

func (c *floatingWindowContent) Draw(w term.Writer) {
	c.span.Draw(w)
	c.drawSkipButton(w)
}

func (c *floatingWindowContent) Resize(width, height int) {
	c.width, c.height = width, height
	c.span.Resize(width, height)
}

func (c *floatingWindowContent) Handle(ev term.Event) (bool, bool) {
	if c.onSkip != nil && ev.Type == term.EventMouse &&
		ev.Key == term.MouseLeft && c.inSkipButton(ev.MouseX, ev.MouseY) {
		c.onSkip()
		return false, true
	}
	return c.span.Handle(ev)
}

// drawSkipButton paints the "Skip" button right-aligned on
// the content's last row.
func (c *floatingWindowContent) drawSkipButton(w term.Writer) {
	if c.width < len(skipButtonLabel) || c.height < 1 {
		return
	}
	x0 := c.width - len(skipButtonLabel)
	for i, r := range skipButtonLabel {
		w.SetCell(term.Coordinates{X: x0 + i, Y: c.height - 1},
			term.NewCell(r, 1, skipButtonAttr))
	}
}

// inSkipButton reports whether the content-local point (x, y) is on
// the button row drawn by drawSkipButton.
func (c *floatingWindowContent) inSkipButton(x, y int) bool {
	return c.width >= len(skipButtonLabel) &&
		y == c.height-1 && x >= c.width-len(skipButtonLabel)
}

func (c *floatingWindowContent) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return c.span.Cursor()
}

// Dimensions reports the ideal content size: 60% of the screen width
// (clamped like the bespoke floating window did) and the markdown
// height at that width, capped so the window chrome still fits on
// screen — or, for hint windows, stays above the command prompt.
func (c *floatingWindowContent) Dimensions() (int, int) {
	packed := c.screen.Load()
	sw := int(uint32(packed >> 32))
	sh := int(uint32(packed))
	innerW := (sw * 6) / 10
	innerW = max(innerW, 20)
	innerW = min(innerW, sw-2)
	contentW := max(innerW-2, 4)
	markdownW := max(contentW-2, 1)
	contentH := max(c.Handler.Height(markdownW), 1) + 1
	switch {
	case c.promptAware:
		hintCap := int(float64(sh)*commandPromptTopFraction) +
			hintBoxMaxHeightSlack - 2
		hintCap = max(hintCap, hintBoxMinInnerH-2)
		contentH = min(contentH, hintCap)
	case sh > 4:
		contentH = min(contentH, sh-4)
	}
	return contentW, contentH
}

// Close stamps the close-notification state before releasing the
// underlying viewer.
func (c *floatingWindowContent) Close() error {
	if c.onClose != nil {
		c.onClose()
	}
	return c.Handler.Close()
}

// openFloatingWindow opens the browser window for a reqFloatingWindow
// request. It runs on the run goroutine before the request becomes
// active; the overlay browser's lock makes that safe.
func (t *Tutorial) openFloatingWindow(r *request, width, height int) {
	if t.winOverlay == nil || r.md == nil {
		return
	}
	content := newFloatingWindowContent(r.md, width, height,
		func() { r.winClosed.Store(true) },
		func() { r.requestSkip(t) })
	align := r.align
	if align == 0 {
		align = defaultStepAlignment
	}
	offset := r.offset
	offset.X = max(offset.X, 0)
	offset.Y = max(offset.Y, 0)
	r.winContent = content
	r.win = t.winOverlay.Floating(content, browserapi.FloatingConfig{
		Alignment: align,
		Offset:    offset,
		Title:     floatingWindowTitle(r),
	})
}

// closeRequestWindow closes r's browser window, if any. Idempotent.
func (t *Tutorial) closeRequestWindow(r *request) {
	if t.winOverlay == nil || r.win == nil {
		return
	}
	t.winOverlay.CloseWindow(r.win)
}

// openPromptWindow opens the confirm/choice prompt window for r on
// the overlay browser. It runs on the run goroutine before the
// request becomes active. OnSelect stamps the request's pending
// response; the prompt window closing (selection, Esc, or the ✕
// click) fires OnClose, which stamps winClosed so the TUI loop
// resolves the request with the stamped or per-kind dismissal
// response. Both callbacks fire while the overlay-browser lock is
// held and must only stamp state.
func (t *Tutorial) openPromptWindow(r *request) {
	if t.winOverlay == nil {
		return
	}
	ph := handler.FuncPromptHandler(
		func(idx int, option string) {
			if idx == len(r.options) {
				r.requestSkip(t)
				return
			}
			value := option
			if idx >= 0 && idx < len(r.options) {
				value = r.options[idx]
			}
			r.pendingResp = response{
				selectedIdx:   idx,
				selectedValue: value,
				selected:      true,
			}
			if r.kind == reqConfirm {
				r.pendingResp.confirmed = idx == 0
			}
			r.pendingSelected = true
		},
		func() error {
			r.winClosed.Store(true)
			return nil
		},
	)
	options := append(padPromptOptions(r.options), skipButtonLabel)
	r.win = t.winOverlay.Prompt(r.message, options, nil, ph)
}

// defaultStepAlignment anchors step windows at the bottom of the
// screen: the command prompt opens near the top, and keeping every
// step in the same place stops the tutorial from jumping between the
// top and bottom of the screen from one step to the next.
const defaultStepAlignment = component.AlignmentBottom |
	component.AlignmentHorizontallyCentered

// consoleStepAlignment anchors the hint of a console step at the top:
// Rune's console draws its own prompt at the bottom of the screen,
// which the default bottom anchor would cover.
const consoleStepAlignment = component.AlignmentTop |
	component.AlignmentHorizontallyCentered

// openHintWindow opens the non-modal hint window for a wait_* request,
// clear of the prompt the step asks the user to type into. Keys are
// never routed to it — they fall through to the IDE root while the
// request is armed — but the user can drag it aside, scroll it, or
// close it via the window bar without resolving the step.
func (t *Tutorial) openHintWindow(r *request, width, height int) {
	if t.winOverlay == nil {
		return
	}
	md, ok := newHintMarkdown(t.hintBody(r))
	if !ok {
		return
	}
	content := newFloatingWindowContent(md, width, height,
		func() { r.winClosed.Store(true) },
		func() { r.requestSkip(t) })
	r.winContent = content
	align := defaultStepAlignment
	switch {
	case r.align != 0:
		align = r.align
	case r.kind == reqWaitShell:
		align = consoleStepAlignment
	}
	content.promptAware = r.kind == reqWaitCommand && align == defaultStepAlignment
	r.win = t.winOverlay.Floating(content, browserapi.FloatingConfig{
		Alignment: align,
		Title:     floatingWindowTitle(r),
	})
}

// refreshHintWindow re-renders r's hint body into its live window,
// used when ObserveCommand swaps in the on_error recovery hint.
func (t *Tutorial) refreshHintWindow(r *request) {
	if t.winOverlay == nil || r.winContent == nil {
		return
	}
	md, ok := newHintMarkdown(t.hintBody(r))
	if !ok {
		return
	}
	t.winOverlay.Update(func() {
		r.winContent.SetComponent(md)
	})
}

// hintBody composes the markdown body of a wait_* hint window.
func (t *Tutorial) hintBody(r *request) string {
	switch r.kind {
	case reqWaitKey:
		return "Press " + r.waitKey + " to continue."
	case reqWaitCommand:
		return buildWaitCommandHint(r, t.commandKeyDisplay,
			t.commandManualLookup, t.keyForCommand)
	case reqWaitShell:
		return buildWaitShellHint(r, t.commandKeyDisplay)
	case reqWaitEvent:
		return r.text
	}
	return ""
}

func newHintMarkdown(body string) (*markdown.Component, bool) {
	if body == "" {
		return nil, false
	}
	mdCfg := markdown.DefaultConfig()
	mdCfg.HeaderPrefix = false
	md, err := markdown.NewWithConfig(body, mdCfg)
	if err != nil {
		return nil, false
	}
	return md, true
}

func floatingWindowTitle(r *request) string {
	switch {
	case r.title != "" && r.stepNum > 0:
		return fmt.Sprintf("Step %d — %s", r.stepNum, r.title)
	case r.title != "":
		return r.title
	case r.stepNum > 0:
		return fmt.Sprintf("Step %d", r.stepNum)
	}
	return ""
}
