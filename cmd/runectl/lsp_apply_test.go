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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
)

func pos(line, char uint32) semanticapi.Position {
	return semanticapi.Position{Line: line, Character: char}
}

func rng(sl, sc, el, ec uint32) semanticapi.Range {
	return semanticapi.Range{Start: pos(sl, sc), End: pos(el, ec)}
}

func TestApplyEditsToString(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		edits    []semanticapi.TextEdit
		expected string
	}{
		// ── no-ops ──────────────────────────────────
		{
			name:     "no edits",
			content:  "hello\n",
			edits:    nil,
			expected: "hello\n",
		},
		{
			name:     "empty edits slice",
			content:  "hello\n",
			edits:    []semanticapi.TextEdit{},
			expected: "hello\n",
		},
		{
			name:    "nop replacement",
			content: "abc\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 0, 0, 3), NewText: "abc"},
			},
			expected: "abc\n",
		},

		// ── empty content ───────────────────────────
		{
			name:     "empty content no edits",
			content:  "",
			edits:    nil,
			expected: "",
		},
		{
			name:    "empty content insert text",
			content: "",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 0, 0, 0), NewText: "hello\n"},
			},
			expected: "hello\n",
		},
		{
			name:    "empty content insert multiline",
			content: "",
			edits: []semanticapi.TextEdit{
				{
					Range:   rng(0, 0, 0, 0),
					NewText: "a\nb\nc\n",
				},
			},
			expected: "a\nb\nc\n",
		},

		// ── content without trailing newline ────────
		{
			name:    "no trailing newline replace word",
			content: "hello world",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 6, 0, 11), NewText: "Go"},
			},
			expected: "hello Go",
		},
		{
			name:    "no trailing newline insert at end",
			content: "hello",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 5, 0, 5), NewText: " world"},
			},
			expected: "hello world",
		},
		{
			name:    "no trailing newline delete all",
			content: "hello",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 0, 0, 5), NewText: ""},
			},
			expected: "",
		},
		{
			name:    "no trailing newline add trailing newline",
			content: "hello",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 5, 0, 5), NewText: "\n"},
			},
			expected: "hello\n",
		},

		// ── single-line replacements ────────────────
		{
			name:    "replace word mid-line",
			content: "hello world\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 6, 0, 11), NewText: "Go"},
			},
			expected: "hello Go\n",
		},
		{
			name:    "replace first character",
			content: "abc\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 0, 0, 1), NewText: "X"},
			},
			expected: "Xbc\n",
		},
		{
			name:    "replace last character before newline",
			content: "abc\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 2, 0, 3), NewText: "Z"},
			},
			expected: "abZ\n",
		},
		{
			name:    "replace entire line content",
			content: "old\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 0, 0, 3), NewText: "new"},
			},
			expected: "new\n",
		},
		{
			name:    "replace with longer text",
			content: "ab\n",
			edits: []semanticapi.TextEdit{
				{
					Range:   rng(0, 0, 0, 2),
					NewText: "replacement",
				},
			},
			expected: "replacement\n",
		},
		{
			name:    "replace with shorter text",
			content: "longword\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 0, 0, 8), NewText: "hi"},
			},
			expected: "hi\n",
		},

		// ── insertions (zero-width range) ───────────
		{
			name:    "insert at beginning of file",
			content: "world\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 0, 0, 0), NewText: "hello "},
			},
			expected: "hello world\n",
		},
		{
			name:    "insert at end of line before newline",
			content: "hello\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 5, 0, 5), NewText: " world"},
			},
			expected: "hello world\n",
		},
		{
			name:    "insert at end of file after newline",
			content: "hello\n",
			edits: []semanticapi.TextEdit{
				{
					Range:   rng(1, 0, 1, 0),
					NewText: "world\n",
				},
			},
			expected: "hello\nworld\n",
		},
		{
			name:    "insert newline splits a line",
			content: "ab\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 1, 0, 1), NewText: "\n"},
			},
			expected: "a\nb\n",
		},
		{
			name:    "insert multiple lines at a point",
			content: "a\nd\n",
			edits: []semanticapi.TextEdit{
				{
					Range:   rng(0, 1, 0, 1),
					NewText: "\nb\nc",
				},
			},
			// suffix of line 0 is "\n", so result is
			// "a" + "\nb\nc" + "\n" = "a\nb\nc\n"
			// then the remaining "d\n" follows.
			expected: "a\nb\nc\nd\n",
		},
		{
			name:    "insert between two lines",
			content: "first\nthird\n",
			edits: []semanticapi.TextEdit{
				{
					Range:   rng(1, 0, 1, 0),
					NewText: "second\n",
				},
			},
			expected: "first\nsecond\nthird\n",
		},

		// ── deletions (empty NewText) ───────────────
		{
			name:    "delete single character",
			content: "abc\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 1, 0, 2), NewText: ""},
			},
			expected: "ac\n",
		},
		{
			name:    "delete entire line including newline",
			content: "a\nb\nc\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(1, 0, 2, 0), NewText: ""},
			},
			expected: "a\nc\n",
		},
		{
			name:    "delete first line",
			content: "a\nb\nc\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 0, 1, 0), NewText: ""},
			},
			expected: "b\nc\n",
		},
		{
			name:    "delete last line",
			content: "a\nb\nc\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(2, 0, 3, 0), NewText: ""},
			},
			expected: "a\nb\n",
		},
		{
			name:    "delete from middle of one line to middle of another",
			content: "abcd\nefgh\nijkl\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 2, 2, 2), NewText: ""},
			},
			expected: "abkl\n",
		},
		{
			name:    "delete entire file content",
			content: "a\nb\nc\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 0, 3, 0), NewText: ""},
			},
			expected: "",
		},
		{
			name:    "delete entire file no trailing newline",
			content: "abc",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 0, 0, 3), NewText: ""},
			},
			expected: "",
		},
		{
			name:    "delete only newline merges lines",
			content: "a\nb\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 1, 1, 0), NewText: ""},
			},
			expected: "ab\n",
		},

		// ── multi-line replacements ─────────────────
		{
			name:    "replace across two lines",
			content: "aaa\nbbb\nccc\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 1, 1, 2), NewText: "X"},
			},
			expected: "aXb\nccc\n",
		},
		{
			name:    "replace all with new content",
			content: "old line 1\nold line 2\n",
			edits: []semanticapi.TextEdit{
				{
					Range:   rng(0, 0, 2, 0),
					NewText: "new line 1\nnew line 2\n",
				},
			},
			expected: "new line 1\nnew line 2\n",
		},
		{
			name:    "replace one line with multiple",
			content: "a\nb\nc\n",
			edits: []semanticapi.TextEdit{
				{
					Range:   rng(1, 0, 1, 1),
					NewText: "x\ny\nz",
				},
			},
			expected: "a\nx\ny\nz\nc\n",
		},
		{
			name:    "replace multiple lines with one",
			content: "a\nb\nc\nd\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(1, 0, 3, 0), NewText: "X\n"},
			},
			expected: "a\nX\nd\n",
		},
		{
			name:    "delete all then insert via single edit",
			content: "old\n",
			edits: []semanticapi.TextEdit{
				{
					Range:   rng(0, 0, 1, 0),
					NewText: "brand new\ncontent\n",
				},
			},
			expected: "brand new\ncontent\n",
		},

		// ── multiple edits ──────────────────────────
		{
			name:    "two edits on different lines",
			content: "aaa\nbbb\nccc\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 0, 0, 3), NewText: "AAA"},
				{Range: rng(2, 0, 2, 3), NewText: "CCC"},
			},
			expected: "AAA\nbbb\nCCC\n",
		},
		{
			name:    "two edits on same line non-overlapping",
			content: "aabbcc\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 0, 0, 2), NewText: "AA"},
				{Range: rng(0, 4, 0, 6), NewText: "CC"},
			},
			expected: "AAbbCC\n",
		},
		{
			name:    "edits provided in forward order",
			content: "a\nb\nc\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 0, 0, 1), NewText: "A"},
				{Range: rng(1, 0, 1, 1), NewText: "B"},
				{Range: rng(2, 0, 2, 1), NewText: "C"},
			},
			expected: "A\nB\nC\n",
		},
		{
			name:    "edits provided in reverse order",
			content: "a\nb\nc\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(2, 0, 2, 1), NewText: "C"},
				{Range: rng(1, 0, 1, 1), NewText: "B"},
				{Range: rng(0, 0, 0, 1), NewText: "A"},
			},
			expected: "A\nB\nC\n",
		},
		{
			name:    "adjacent edits end of one is start of next",
			content: "abcdef\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 0, 0, 2), NewText: "AB"},
				{Range: rng(0, 2, 0, 4), NewText: "CD"},
				{Range: rng(0, 4, 0, 6), NewText: "EF"},
			},
			expected: "ABCDEF\n",
		},
		{
			name:    "insert at one place delete at another",
			content: "a\nb\nc\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 1, 0, 1), NewText: "X"},
				{Range: rng(2, 0, 2, 1), NewText: ""},
			},
			expected: "aX\nb\n\n",
		},
		{
			name:    "delete multiple separate lines",
			content: "a\nb\nc\nd\ne\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(1, 0, 2, 0), NewText: ""},
				{Range: rng(3, 0, 4, 0), NewText: ""},
			},
			expected: "a\nc\ne\n",
		},
		{
			name:    "many edits scattered across file",
			content: "01234\nabcde\nFGHIJ\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 0, 0, 1), NewText: "_"},
				{Range: rng(0, 4, 0, 5), NewText: "_"},
				{Range: rng(1, 2, 1, 3), NewText: "_"},
				{Range: rng(2, 1, 2, 4), NewText: "ghi"},
			},
			expected: "_123_\nab_de\nFghiJ\n",
		},

		// ── line clamping (edit beyond file end) ────
		{
			name:    "edit line beyond file end clamps to last",
			content: "only\n",
			edits: []semanticapi.TextEdit{
				{
					Range:   rng(99, 0, 99, 0),
					NewText: "X",
				},
			},
			// "only\n" splits to ["only\n", ""];
			// line 99 clamps to line 1 (the trailing ""),
			// so insert appends after the existing content.
			expected: "only\nX",
		},
		{
			name:    "edit spans from valid to beyond end",
			content: "hello\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 0, 99, 0), NewText: "X\n"},
			},
			expected: "X\n",
		},

		// ── whitespace and special characters ───────
		{
			name:    "replace with tabs",
			content: "  code\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 0, 0, 2), NewText: "\t"},
			},
			expected: "\tcode\n",
		},
		{
			name:    "content with blank lines",
			content: "a\n\nb\n\nc\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(1, 0, 2, 0), NewText: ""},
			},
			expected: "a\nb\n\nc\n",
		},
		{
			name:    "content is only newlines",
			content: "\n\n\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(1, 0, 2, 0), NewText: "x\n"},
			},
			expected: "\nx\n\n",
		},
		{
			name:    "single newline content delete it",
			content: "\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 0, 0, 1), NewText: ""},
			},
			expected: "",
		},
		{
			name:    "remove tab",
			content: "a\n\t\tb\nc",
			edits: []semanticapi.TextEdit{
				{Range: rng(1, 1, 1, 2), NewText: ""},
			},
			expected: "a\n\tb\nc",
		},

		// ── wide / multi-byte characters ────────────
		//
		// Character offsets are byte offsets into the
		// Go string. These tests document the current
		// behavior and will catch regressions.
		{
			name:    "multibyte utf8 replace after accent",
			content: "café\n",
			// 'c'=1B 'a'=1B 'f'=1B 'é'=2B → "café" is
			// 5 bytes. Replace "é" (bytes 3..5).
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 3, 0, 5), NewText: "e"},
			},
			expected: "cafe\n",
		},
		{
			name:    "multibyte utf8 insert before accent",
			content: "café\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 0, 0, 0), NewText: "Le "},
			},
			expected: "Le café\n",
		},
		{
			name:    "cjk replace single character",
			content: "日本語\n",
			// '日'=3B '本'=3B '語'=3B → 9 bytes.
			// Replace '本' (bytes 3..6) with '人'.
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 3, 0, 6), NewText: "人"},
			},
			expected: "日人語\n",
		},
		{
			name:    "cjk delete first character",
			content: "漢字テスト\n",
			// '漢'=3B → delete bytes 0..3.
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 0, 0, 3), NewText: ""},
			},
			expected: "字テスト\n",
		},
		{
			name:    "cjk insert between characters",
			content: "AB\n",
			// Insert CJK between A(1B) and B(1B).
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 1, 0, 1), NewText: "中"},
			},
			expected: "A中B\n",
		},
		{
			name:    "emoji replace",
			content: "hi 🎉 bye\n",
			// 'h'=1 'i'=1 ' '=1 '🎉'=4 ' '=1 → 🎉
			// starts at byte 3, ends at byte 7.
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 3, 0, 7), NewText: "🎊"},
			},
			expected: "hi 🎊 bye\n",
		},
		{
			name:    "emoji delete",
			content: "a🎉b\n",
			// 'a'=1 '🎉'=4 → delete bytes 1..5.
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 1, 0, 5), NewText: ""},
			},
			expected: "ab\n",
		},
		{
			name:    "mixed multibyte replace middle",
			content: "aéb漢c🎉d\n",
			// a=1 é=2 b=1 漢=3 c=1 🎉=4 d=1 → 13B+\n.
			// Replace 漢c (bytes 4..8) with "XY".
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 4, 0, 8), NewText: "XY"},
			},
			expected: "aébXY🎉d\n",
		},
		{
			name:    "wide chars multiple edits",
			content: "αβγ\n",
			// α=2B β=2B γ=2B → 6 bytes.
			// Replace α (0..2) and γ (4..6).
			edits: []semanticapi.TextEdit{
				{Range: rng(0, 0, 0, 2), NewText: "A"},
				{Range: rng(0, 4, 0, 6), NewText: "C"},
			},
			expected: "AβC\n",
		},

		// ── idempotency ─────────────────────────────
		{
			name:    "replacing text with itself is identity",
			content: "one\ntwo\nthree\n",
			edits: []semanticapi.TextEdit{
				{Range: rng(1, 0, 1, 3), NewText: "two"},
			},
			expected: "one\ntwo\nthree\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyEditsToString(tt.content, tt.edits)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestTextEditsToFileEdits(t *testing.T) {
	tests := []struct {
		name     string
		uri      string
		edits    []semanticapi.TextEdit
		expected []fileEdit
	}{
		{
			name:     "empty edits",
			uri:      "file:///a.go",
			edits:    nil,
			expected: nil,
		},
		{
			name:  "wraps edits",
			uri:   "file:///a.go",
			edits: []semanticapi.TextEdit{{NewText: "x"}},
			expected: []fileEdit{{
				URI:   "file:///a.go",
				Edits: []semanticapi.TextEdit{{NewText: "x"}},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := textEditsToFileEdits(tt.uri, tt.edits)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestWorkspaceEditToFileEdits(t *testing.T) {
	tests := []struct {
		name     string
		edit     *semanticapi.WorkspaceEdit
		expected []fileEdit
	}{
		{
			name:     "nil edit",
			edit:     nil,
			expected: nil,
		},
		{
			name: "single file",
			edit: &semanticapi.WorkspaceEdit{
				Changes: map[string][]semanticapi.TextEdit{
					"file:///a.go": {{NewText: "x"}},
				},
			},
			expected: []fileEdit{{
				URI:   "file:///a.go",
				Edits: []semanticapi.TextEdit{{NewText: "x"}},
			}},
		},
		{
			name: "multiple files sorted",
			edit: &semanticapi.WorkspaceEdit{
				Changes: map[string][]semanticapi.TextEdit{
					"file:///b.go": {{NewText: "b"}},
					"file:///a.go": {{NewText: "a"}},
				},
			},
			expected: []fileEdit{
				{
					URI:   "file:///a.go",
					Edits: []semanticapi.TextEdit{{NewText: "a"}},
				},
				{
					URI:   "file:///b.go",
					Edits: []semanticapi.TextEdit{{NewText: "b"}},
				},
			},
		},
		{
			name: "skips empty edit lists",
			edit: &semanticapi.WorkspaceEdit{
				Changes: map[string][]semanticapi.TextEdit{
					"file:///a.go": {},
					"file:///b.go": {{NewText: "b"}},
				},
			},
			expected: []fileEdit{{
				URI:   "file:///b.go",
				Edits: []semanticapi.TextEdit{{NewText: "b"}},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := workspaceEditToFileEdits(tt.edit)
			assert.Equal(t, tt.expected, got)
		})
	}
}
