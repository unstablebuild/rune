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
	"strings"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"

	"unstable.build/rune/internal/component/markdown"
	mdhandler "unstable.build/rune/internal/handler/markdown"
)

// screenPadHorizontal keeps the step's body off the tile's frame
// columns so text never runs into the border. Span splits absolute
// padding across both sides only when the content is horizontally
// centered, so this is the total, not the per-side gutter.
const screenPadHorizontal = 2

// openScreen builds the body r's screen draws inside the tile. It runs
// on the run goroutine before the request becomes active, so nothing
// else can be touching r yet. A screen with nothing to render leaves
// body nil and the tile empty.
func (t *Tutorial) openScreen(r *request, width int) {
	switch r.kind {
	case reqWaitCommand, reqWaitShell, reqWaitEvent:
		md, ok := newScreenMarkdown(screenMarkdown(r.title, t.stepBody(r)))
		if !ok {
			return
		}
		r.viewer, r.body = newScreenContent(md)
	case reqConfirm, reqChoice:
		t.openPrompt(r, width)
	}
}

// newScreenContent builds the body of a wait_* screen: md behind a
// less-like, mouse-scrollable viewer, padded off the tile frame and
// anchored top-left so a step's first line is the one the user reads
// first.
func newScreenContent(md *markdown.Component) (*mdhandler.Handler, *handler.Span) {
	viewer := mdhandler.New(md)
	return viewer, handler.NewSpan(viewer, component.SpanConfig{
		PadHorizontal: screenPadHorizontal,
		ContentAlignment: component.AlignmentTop |
			component.AlignmentHorizontallyCentered,
	})
}

// screenMarkdown heads body with title, as the tile has no title bar
// of its own.
func screenMarkdown(title, body string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return body
	}
	if body == "" {
		return "# " + title + "\n"
	}
	return "# " + title + "\n\n" + body
}

// openPrompt builds the in-tile prompt of a confirm/choice screen.
// OnSelect stamps the request's pending response and OnClose stamps
// closed; the TUI loop resolves the request with the stamped or
// per-kind dismissal response right after routing the event that
// triggered them.
func (t *Tutorial) openPrompt(r *request, width int) {
	ph := handler.FuncPromptHandler(
		func(idx int, option string) {
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
			r.closed = true
			return nil
		},
	)
	message := newPromptMessage(r.message)
	r.prompt = handler.NewPrompt(handler.PromptConfig{
		PromptConfig: component.PromptConfig{
			Message: r.message,
			Options: padPromptOptions(r.options,
				promptOptionPad(r.options, width-screenPadHorizontal)),
			NewMessage:           func(string) component.Floating { return message },
			BackgroundAttributes: t.promptStyle.BackgroundAttr,
		},
		PromptHandler: ph,
		OptionAttr:    t.promptStyle.TextAttr,
		HighlightAttr: t.promptStyle.HighlightAttr,
	})
	_, promptHeight := r.prompt.Dimensions()
	_, messageHeight := message.Dimensions()
	body := &promptBody{
		Prompt:  r.prompt,
		message: message,
		options: max(promptHeight-messageHeight, 1),
	}
	r.body = handler.NewSpan(body, component.SpanConfig{
		PadHorizontal: screenPadHorizontal,
		ContentAlignment: component.AlignmentTop |
			component.AlignmentHorizontallyCentered,
	})
}

// promptBody keeps a prompt screen's buttons under the copy that asks
// the question. component.Prompt pins its options to the bottom edge
// of whatever height it is given, and the tile's body is as tall as
// the pane, so the prompt is given only the height its message needs.
type promptBody struct {
	*handler.Prompt
	message *component.Span
	options int
}

// Resize satisfies tui.Component.
func (b *promptBody) Resize(width, height int) {
	b.Prompt.Resize(width, min(b.message.Height(width)+b.options, height))
}

// newPromptMessage renders a prompt's message as markdown so a
// confirm/choice screen reads like every other step in the tile.
func newPromptMessage(msg string) *component.Span {
	md, ok := newScreenMarkdown(msg)
	if !ok {
		return component.NewSpan(
			component.NewResponsiveString(msg,
				component.StringResponsiveConfig{NoSplitWords: true}),
			component.SpanConfig{PadVertical: 1})
	}
	return component.NewSpan(md, component.SpanConfig{
		PadVertical:      1,
		ContentAlignment: component.AlignmentTop | component.AlignmentLeft,
	})
}

// stepBody is the markdown copy of a wait_* screen: the author's text
// when the step declared one, otherwise a generated hint naming what
// the step is waiting for.
func (t *Tutorial) stepBody(r *request) string {
	switch r.kind {
	case reqWaitCommand:
		return buildWaitCommandHint(r, t.commandKeyDisplay,
			t.commandManualLookup, t.keyForCommand)
	case reqWaitShell:
		return buildWaitShellHint(r, t.commandKeyDisplay)
	case reqWaitEvent:
		return expandCmdTemplate(r.text, t.commandKeyDisplay)
	}
	return ""
}

// newScreenMarkdown parses body as the markdown of a tile screen.
// ok=false for empty copy, which renders nothing.
func newScreenMarkdown(body string) (*markdown.Component, bool) {
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

// closingScreen is what a completed lesson leaves on the tile. The
// final step is usually the one the user is still practising, so
// reaching it does not take the copy away: the tile stays until the
// user closes it.
func (t *Tutorial) closingScreen(width, height int) *request {
	body := "You finished **" + t.Title() + "**.\n\n" +
		"Press **Stop** to close this tile when you are done, or " +
		"**Back** to read through the lesson again.\n"
	md, ok := newScreenMarkdown(screenMarkdown("All done", body))
	if !ok {
		return nil
	}
	r := &request{}
	r.viewer, r.body = newScreenContent(md)
	r.body.Resize(width, height)
	return r
}
