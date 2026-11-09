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

package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	mdcomp "unstable.build/rune/internal/component/markdown"
	mdhandler "unstable.build/rune/internal/handler/markdown"
)

// markdownView is the floating viewer used for rust-analyzer's textual
// output. The markdown stack gives it less-like scrolling, mouse wheel
// support, `/` search, and selection; source is retained so tests can
// assert on what is displayed. Its mutex stands in for the event loop
// the extension does not have: handler callbacks arrive on the window
// stream goroutine while asynchronous code-block highlighting lands
// via tick, so both run under the same lock.
type markdownView struct {
	mu        sync.Mutex
	inner     browserapi.Floating
	source    string
	interrupt term.Interrupter
}

var _ browserapi.Floating = (*markdownView)(nil)

// tick runs deferred highlight work under the view lock, then wakes the
// IDE event loop: the extension has no loop of its own, so without the
// interrupt the new highlights would render only on the next input
// event.
func (v *markdownView) tick(fn func()) bool {
	v.mu.Lock()
	fn()
	v.mu.Unlock()
	_ = v.interrupt.Interrupt(context.Background())
	return true
}

func (v *markdownView) Handle(ev term.Event) (exit, handled bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.inner.Handle(ev)
}

func (v *markdownView) Draw(w term.Writer) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.inner.Draw(w)
}

func (v *markdownView) Resize(w, h int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.inner.Resize(w, h)
}

func (v *markdownView) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.inner.Cursor()
}

func (v *markdownView) Selection() (string, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.inner.Selection()
}

func (v *markdownView) Dimensions() (int, int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.inner.Dimensions()
}

func (v *markdownView) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.inner.Close()
}

// showMarkdown floats a scrollable, dismiss-on-key markdown view of
// source, with fenced code blocks highlighted through parser. interrupt
// wakes the IDE event loop when the deferred highlight pass lands.
func showMarkdown(
	wm browserapi.WindowManager, parser syntaxapi.Parser,
	interrupt term.Interrupter, source string,
) error {
	if interrupt == nil {
		interrupt = term.NopInterrupter()
	}
	view := &markdownView{source: source, interrupt: interrupt}
	cfg := mdcomp.DefaultConfig()
	cfg.Parser = parser
	cfg.ScheduleNextTick = view.tick
	comp, err := mdcomp.NewWithConfig(source, cfg)
	if err != nil {
		return fmt.Errorf("render markdown: %w", err)
	}
	mdh := mdhandler.New(comp)
	span := handler.NewSpan(mdh, component.SpanConfig{
		PadHorizontal:    2,
		ContentAlignment: component.AlignmentCentered,
	})
	view.inner = browserapi.FuncFloatingHandler(span, mdh.Close)
	_, err = wm.Floating(view, browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
	})
	if err != nil {
		return fmt.Errorf("show viewer: %w", err)
	}
	return nil
}

// fencedCode wraps plain server output in a fenced code block tagged
// with lang, so the markdown viewer renders it verbatim. The fence is
// longer than any backtick run in text, which is what keeps content like
// a macro expansion containing ``` from closing the block early.
func fencedCode(text, lang string) string {
	longest, run := 0, 0
	for _, r := range text {
		if r != '`' {
			run = 0
			continue
		}
		run++
		longest = max(longest, run)
	}
	fence := strings.Repeat("`", max(longest+1, 3))
	return fence + lang + "\n" + strings.TrimRight(text, "\n") + "\n" + fence
}
