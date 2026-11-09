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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/handler/handlertest"
)

func TestFencedCode(t *testing.T) {
	tests := []struct {
		name string
		text string
		lang string
		want string
	}{
		{
			name: "plain",
			text: "FN@0..12",
			want: "```\nFN@0..12\n```",
		},
		{
			name: "tagged",
			text: "fn main() {}",
			lang: "rust",
			want: "```rust\nfn main() {}\n```",
		},
		{
			name: "trailing newlines are dropped",
			text: "digraph {}\n\n",
			lang: "dot",
			want: "```dot\ndigraph {}\n```",
		},
		{
			name: "fence outgrows the longest backtick run",
			text: "before\n```\nnested\n```\nafter",
			want: "````\nbefore\n```\nnested\n```\nafter\n````",
		},
		{
			name: "short backtick runs still use three",
			text: "let x = `y`;",
			want: "```\nlet x = `y`;\n```",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, fencedCode(tt.text, tt.lang))
		})
	}
}

func TestShowMarkdownFloatsSource(t *testing.T) {
	wm := &fakeWM{}
	require.NoError(t, showMarkdown(wm, nil, nil, "# Title\n\nbody"))
	wm.mu.Lock()
	defer wm.mu.Unlock()
	view, ok := wm.floating.(*markdownView)
	require.True(t, ok, "a markdown view is floated")
	assert.Equal(t, "# Title\n\nbody", view.source)
}

func TestShowMarkdownHighlightsFencedCode(t *testing.T) {
	wm := &fakeWM{}
	parser := &recordingParser{}
	require.NoError(t, showMarkdown(wm, parser, nil, fencedCode("fn main() {}", "rust")))
	requireHighlightRequested(t, parser, ".rs")
}

func TestShowMarkdownCloseLeavesItsWindow(t *testing.T) {
	wm := &fakeWM{}
	require.NoError(t, showMarkdown(wm, nil, nil, "# Title"))
	wm.mu.Lock()
	view := wm.floating
	wm.mu.Unlock()

	require.NoError(t, view.Close())

	wm.mu.Lock()
	defer wm.mu.Unlock()
	assert.Zero(t, wm.closeWindowCalls,
		"the window may already show other content when the view is released")
}

// testMarkdownSource is rust-analyzer style output long enough to scroll
// a 15-row view, mixing prose, a link, lists and fenced code.
const testMarkdownSource = "# Expanded macro `vec!`\n\n" +
	"Recursive expansion of [`vec!`](https://doc.rust-lang.org/std/macro.vec.html):\n\n" +
	"```rust\n" +
	"<[_]>::into_vec(\n" +
	"    #[rustc_box]\n" +
	"    $crate::boxed::Box::new([1, 2, 3]),\n" +
	")\n" +
	"```\n\n" +
	"- first item with some longer text that wraps at narrow widths\n" +
	"- second item\n" +
	"- third item\n\n" +
	"```\nFN@0..12\n  FN_KW@0..2 \"fn\"\n  WHITESPACE@2..3 \" \"\n  NAME@3..7\n```\n\n" +
	"Trailing paragraph one.\n\nTrailing paragraph two.\n\nTrailing paragraph three.\n"

func newTestMarkdownView(t *testing.T, parser *recordingParser) *markdownView {
	t.Helper()
	wm := &fakeWM{}
	var p syntaxapi.Parser
	if parser != nil {
		p = parser
	}
	require.NoError(t, showMarkdown(wm, p, nil, testMarkdownSource))
	wm.mu.Lock()
	defer wm.mu.Unlock()
	view, ok := wm.floating.(*markdownView)
	require.True(t, ok, "a markdown view is floated")
	return view
}

func TestMarkdownViewPointerLeaves(t *testing.T) {
	t.Parallel()
	for _, size := range [][2]int{{60, 15}, {1, 1}} {
		handlertest.RunPointerLeaves(t, newTestMarkdownView(t, &recordingParser{}), size[0], size[1])
	}
}

func TestMarkdownViewPointerLeaveIsInert(t *testing.T) {
	t.Parallel()
	// No parser, so no asynchronous highlight pass redraws between the
	// frames compared.
	requirePointerLeaveInert(t, func(t *testing.T) tui.Handler {
		return newTestMarkdownView(t, nil)
	}, 60, 15, nil)
}

// spanParser is a recordingParser whose highlight pass returns one
// span, so the markdown code block schedules its deferred apply (an
// empty result short-circuits before the tick).
type spanParser struct {
	recordingParser
}

func (p *spanParser) Highlight(
	uri workspaceapi.URI, code string,
) (iterator.Iterator[textapi.Location], error) {
	_, _ = p.recordingParser.Highlight(uri, code)
	return iterator.FromSlice([]textapi.Location{{
		From: term.Coordinates{X: 0, Y: 0},
		To:   term.Coordinates{X: 2, Y: 0},
	}}), nil
}

func TestShowMarkdownInterruptsAfterHighlight(t *testing.T) {
	wm := &fakeWM{}
	parser := &spanParser{}
	ir := &recordingInterrupter{}
	require.NoError(t, showMarkdown(wm, parser, ir, fencedCode("fn main() {}", "rust")))
	requireHighlightRequested(t, &parser.recordingParser, ".rs")
	requireInterrupted(t, ir)
}

// requireInterrupted waits for the view to request an IDE redraw.
func requireInterrupted(t *testing.T, ir *recordingInterrupter) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ir.interrupts() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no redraw interrupt after the highlight pass")
}

// requireHighlightRequested waits for the asynchronous highlight pass
// to request highlighting for a URI containing substr.
func requireHighlightRequested(t *testing.T, parser *recordingParser, substr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, uri := range parser.highlighted() {
			if strings.Contains(uri, substr) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no highlight request for %q; got %v", substr, parser.highlighted())
}
