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

package lspcmd

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// bufCellEditor is a CellEditor that maintains an in-memory text buffer.
type bufCellEditor struct {
	lines []string
}

func newBufCellEditor(initial string) *bufCellEditor {
	return &bufCellEditor{lines: strings.Split(initial, "\n")}
}

func (b *bufCellEditor) Edit(
	_ context.Context, start, end term.Coordinates, text string,
) (term.Coordinates, term.Coordinates, string, error) {
	if start.Y >= len(b.lines) {
		start.Y = len(b.lines) - 1
	}
	if start.X > len(b.lines[start.Y]) {
		start.X = len(b.lines[start.Y])
	}
	if end.Y >= len(b.lines) {
		end.Y = len(b.lines) - 1
	}
	if end.X > len(b.lines[end.Y]) {
		end.X = len(b.lines[end.Y])
	}
	before := b.lines[start.Y][:start.X]
	after := b.lines[end.Y][end.X:]
	combined := before + text + after
	newLines := strings.Split(combined, "\n")
	result := make([]string, 0, start.Y+len(newLines)+len(b.lines)-end.Y-1)
	result = append(result, b.lines[:start.Y]...)
	result = append(result, newLines...)
	result = append(result, b.lines[end.Y+1:]...)
	b.lines = result
	return start, end, "", nil
}

func (b *bufCellEditor) String() string {
	return strings.Join(b.lines, "\n")
}

func pos(line, char uint32) semanticapi.Position {
	return semanticapi.Position{Line: line, Character: char}
}

func TestLspToURI_RebasesOntoWorkspaceScheme(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		base string
		lsp  string
		want string
	}{
		{
			name: "file base keeps file URI",
			base: "file:///project",
			lsp:  "file:///project/a.go",
			want: "file:///project/a.go",
		},
		{
			name: "ssh base rebases file URI inside root",
			base: "ssh://host/home/user/src/rune",
			lsp:  "file:///home/user/src/rune/cell/buffer.go",
			want: "ssh://host/home/user/src/rune/cell/buffer.go",
		},
		{
			name: "ssh base rebases file URI outside root",
			base: "ssh://host/home/user/src/rune",
			lsp:  "file:///home/user/go/pkg/mod/dep/x.go",
			want: "ssh://host/home/user/go/pkg/mod/dep/x.go",
		},
		{
			name: "ssh base preserves user and port",
			base: "ssh://alice@host:2222/home/alice/ws",
			lsp:  "file:///home/alice/ws/main.go",
			want: "ssh://alice@host:2222/home/alice/ws/main.go",
		},
		{
			name: "non-file LSP URI passes through",
			base: "ssh://host/home/user/ws",
			lsp:  "untitled:Untitled-1",
			want: "untitled:Untitled-1",
		},
		{
			name: "zero base passes through",
			base: "",
			lsp:  "file:///project/a.go",
			want: "file:///project/a.go",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var base workspaceapi.URI
			if tt.base != "" {
				var err error
				base, err = workspaceapi.ParseURI(tt.base)
				require.NoError(t, err)
			}
			got, err := LspToURI(base, tt.lsp)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.String())
		})
	}
}

func insertAt(line, char uint32, text string) semanticapi.TextEdit {
	p := pos(line, char)
	return semanticapi.TextEdit{
		Range:   semanticapi.Range{Start: p, End: p},
		NewText: text,
	}
}

func TestApplyEdits_SamePositionInserts(t *testing.T) {
	t.Parallel()

	// Three inserts at (0,0): the array order must define
	// the resulting text order per the LSP spec.
	edits := []semanticapi.TextEdit{
		insertAt(0, 0, "package main\n\n"),
		insertAt(0, 0, "import \"testing\"\n\n"),
		insertAt(0, 0, "func TestFoo(t *testing.T) {}\n"),
	}

	buf := newBufCellEditor("")
	err := ApplyEdits(t.Context(), buf, edits)
	require.NoError(t, err)

	got := buf.String()
	assert.True(t, strings.HasPrefix(got, "package main"),
		"result should start with 'package main', got:\n%s", got)
	assert.Contains(t, got, "import \"testing\"")
	assert.Contains(t, got, "func TestFoo")

	// Verify the ordering: package before import before func.
	pkgIdx := strings.Index(got, "package main")
	impIdx := strings.Index(got, "import \"testing\"")
	funIdx := strings.Index(got, "func TestFoo")
	assert.Less(t, pkgIdx, impIdx, "package should come before import")
	assert.Less(t, impIdx, funIdx, "import should come before func")
}

func TestApplyEdits_MixedPositions(t *testing.T) {
	t.Parallel()

	// Edits at different positions plus same-position inserts.
	buf := newBufCellEditor("line0\nline1\nline2\n")
	edits := []semanticapi.TextEdit{
		// Two inserts at (1,0)
		insertAt(1, 0, "A"),
		insertAt(1, 0, "B"),
		// One edit at (2,0)
		insertAt(2, 0, "C"),
	}

	err := ApplyEdits(t.Context(), buf, edits)
	require.NoError(t, err)

	got := buf.String()
	// At line 1, "A" should appear before "B" per array order.
	lines := strings.Split(got, "\n")
	// line0 is unchanged, line1 should start with AB.
	assert.True(t, strings.HasPrefix(lines[1], "AB"),
		"same-position inserts at line 1 should be AB, got line: %q", lines[1])
}

func TestApplyEdits_SingleEdit(t *testing.T) {
	t.Parallel()

	// Single edit: no ordering concerns.
	buf := newBufCellEditor("")
	edits := []semanticapi.TextEdit{
		insertAt(0, 0, "package main\n"),
	}

	err := ApplyEdits(t.Context(), buf, edits)
	require.NoError(t, err)
	assert.Equal(t, "package main\n", buf.String())
}

func cellsOf(s string) [][]term.Cell {
	lines := strings.Split(s, "\n")
	cells := make([][]term.Cell, len(lines))
	for i, line := range lines {
		row := make([]term.Cell, 0, len(line))
		for _, r := range line {
			row = append(row, term.Cell{
				Ch: r, Width: 1, Bytes: uint8(utf8.RuneLen(r)),
			})
		}
		cells[i] = row
	}
	return cells
}

func replaceEdit(
	startLine, startChar, endLine, endChar uint32, text string,
) semanticapi.TextEdit {
	return semanticapi.TextEdit{
		Range: semanticapi.Range{
			Start: pos(startLine, startChar),
			End:   pos(endLine, endChar),
		},
		NewText: text,
	}
}

func TestEditsChangeText(t *testing.T) {
	t.Parallel()

	// zls answers source.organizeImports with an insert of the whole
	// rewritten import block plus one deletion per original import line,
	// whether or not the imports are already in that order.
	const src = "const std = @import(\"std\");\n" +
		"const foo = @import(\"foo.zig\");\n" +
		"\n" +
		"pub fn main() void {}\n"
	organizeNoop := []semanticapi.TextEdit{
		insertAt(0, 0, "const std = @import(\"std\");\n"+
			"const foo = @import(\"foo.zig\");\n"),
		replaceEdit(0, 0, 1, 0, ""),
		replaceEdit(1, 0, 2, 0, ""),
	}

	tests := []struct {
		name  string
		src   string
		edits []semanticapi.TextEdit
		want  bool
	}{
		{
			name:  "organize imports rewrite that reorders nothing",
			src:   src,
			edits: organizeNoop,
			want:  false,
		},
		{
			name: "organize imports rewrite that reorders",
			src:  src,
			edits: []semanticapi.TextEdit{
				insertAt(0, 0, "const foo = @import(\"foo.zig\");\n"+
					"const std = @import(\"std\");\n"),
				replaceEdit(0, 0, 1, 0, ""),
				replaceEdit(1, 0, 2, 0, ""),
			},
			want: true,
		},
		{
			name:  "no edits",
			src:   src,
			edits: nil,
			want:  false,
		},
		{
			name:  "replacement with identical text",
			src:   src,
			edits: []semanticapi.TextEdit{replaceEdit(3, 0, 3, 21, "pub fn main() void {}")},
			want:  false,
		},
		{
			name:  "out of bounds range is treated as a change",
			src:   src,
			edits: []semanticapi.TextEdit{replaceEdit(9, 0, 9, 1, "x")},
			want:  true,
		},
		{
			name:  "empty document is treated as a change",
			src:   "",
			edits: []semanticapi.TextEdit{insertAt(0, 0, "")},
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cells := cellsOf(tt.src)
			if tt.src == "" {
				cells = nil
			}
			assert.Equal(t, tt.want, EditsChangeText(cells, tt.edits))
		})
	}
}

func TestEditsChangeTextMatchesApplyEdits(t *testing.T) {
	t.Parallel()

	const src = "const std = @import(\"std\");\n" +
		"const foo = @import(\"foo.zig\");\n" +
		"\n" +
		"pub fn main() void {}\n"
	edits := []semanticapi.TextEdit{
		insertAt(0, 0, "const std = @import(\"std\");\n"+
			"const foo = @import(\"foo.zig\");\n"),
		replaceEdit(0, 0, 1, 0, ""),
		replaceEdit(1, 0, 2, 0, ""),
	}

	buf := newBufCellEditor(src)
	require.NoError(t, ApplyEdits(t.Context(), buf, edits))
	assert.Equal(t, src, buf.String())
	assert.False(t, EditsChangeText(cellsOf(src), edits))
}
