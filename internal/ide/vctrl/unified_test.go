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

package vctrl

import (
	"context"
	"fmt"
	"strings"
	"testing"

	godiff "github.com/sourcegraph/go-diff/diff"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// fileDiff builds the FileDiff a real service would return for the
// transition from committed to current, exercising the same -U0 hunk
// shape gitshow consumes.
func fileDiff(t *testing.T, committed, current string) FileDiff {
	t.Helper()
	uri, err := workspaceapi.ParseURI("file:///repo/main.go")
	require.NoError(t, err)
	return ConvertChangesToFileDiff(uri, Diff(context.Background(), committed, current))
}

// renderUnified prints hunks the way a unified diff reads, so a table
// case can state its expectation as text.
func renderUnified(hunks []UnifiedHunk) string {
	var b strings.Builder
	for _, h := range hunks {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n",
			h.OrigStart, h.OrigLines, h.NewStart, h.NewLines)
		for _, l := range h.Lines {
			switch l.Kind {
			case UnifiedContext:
				b.WriteByte(' ')
			case UnifiedAdd:
				b.WriteByte('+')
			case UnifiedDel:
				b.WriteByte('-')
			}
			b.WriteString(l.Text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func TestUnifiedHunks(t *testing.T) {
	for _, tc := range []struct {
		name      string
		committed string
		current   string
		context   int
		want      string
	}{
		{
			name:      "pure insertion",
			committed: "a\nb\nc\n",
			current:   "a\nb\nnew\nc\n",
			context:   1,
			want: "@@ -2,2 +2,3 @@\n" +
				" b\n+new\n c\n",
		},
		{
			name:      "pure deletion",
			committed: "a\nb\nc\n",
			current:   "a\nc\n",
			context:   1,
			want: "@@ -1,3 +1,2 @@\n" +
				" a\n-b\n c\n",
		},
		{
			name:      "modification coalesces delete and insert",
			committed: "a\nold\nc\n",
			current:   "a\nnew\nc\n",
			context:   1,
			want: "@@ -1,3 +1,3 @@\n" +
				" a\n-old\n+new\n c\n",
		},
		{
			name:      "adjacent hunks merge into one window",
			committed: "a\nb\nc\nd\ne\n",
			current:   "A\nb\nc\nd\nE\n",
			context:   3,
			want: "@@ -1,5 +1,5 @@\n" +
				"-a\n+A\n b\n c\n d\n-e\n+E\n",
		},
		{
			name:      "distant hunks stay separate",
			committed: "a\nb\nc\nd\ne\nf\ng\nh\ni\n",
			current:   "A\nb\nc\nd\ne\nf\ng\nh\nI\n",
			context:   1,
			want: "@@ -1,2 +1,2 @@\n" +
				"-a\n+A\n b\n" +
				"@@ -8,2 +8,2 @@\n" +
				" h\n-i\n+I\n",
		},
		{
			name:      "change at file start",
			committed: "a\nb\nc\n",
			current:   "A\nb\nc\n",
			context:   3,
			want: "@@ -1,3 +1,3 @@\n" +
				"-a\n+A\n b\n c\n",
		},
		{
			name:      "change at file end",
			committed: "a\nb\nc\n",
			current:   "a\nb\nC\n",
			context:   3,
			want: "@@ -1,3 +1,3 @@\n" +
				" a\n b\n-c\n+C\n",
		},
		{
			name:      "no trailing newline",
			committed: "a\nb",
			current:   "a\nB",
			context:   3,
			want: "@@ -1,2 +1,2 @@\n" +
				" a\n-b\n+B\n",
		},
		{
			name:      "context larger than the file",
			committed: "a\nb\n",
			current:   "a\nB\n",
			context:   50,
			want: "@@ -1,2 +1,2 @@\n" +
				" a\n-b\n+B\n",
		},
		{
			name:      "zero context",
			committed: "a\nb\nc\n",
			current:   "a\nB\nc\n",
			context:   0,
			want: "@@ -2,1 +2,1 @@\n" +
				"-b\n+B\n",
		},
		{
			name:      "a negative context reads as none",
			committed: "a\nb\nc\n",
			current:   "a\nB\nc\n",
			context:   -5,
			want: "@@ -2,1 +2,1 @@\n" +
				"-b\n+B\n",
		},
		{
			name:      "deletion with zero context reports an empty new range",
			committed: "a\nb\nc\n",
			current:   "a\nc\n",
			context:   0,
			want: "@@ -2,1 +1,0 @@\n" +
				"-b\n",
		},
		{
			name:      "whole file added",
			committed: "",
			current:   "a\nb\n",
			context:   3,
			want: "@@ -0,0 +1,2 @@\n" +
				"+a\n+b\n",
		},
		{
			name:      "whole file emptied",
			committed: "a\nb\n",
			current:   "",
			context:   3,
			want: "@@ -1,2 +0,0 @@\n" +
				"-a\n-b\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := fileDiff(t, tc.committed, tc.current)
			got := UnifiedHunks(d, tc.current, tc.context)
			assert.Equal(t, tc.want, renderUnified(got))
		})
	}
}

func TestUnifiedHunksNoChanges(t *testing.T) {
	d := fileDiff(t, "a\nb\n", "a\nb\n")
	assert.Empty(t, UnifiedHunks(d, "a\nb\n", 3))
	assert.Empty(t, UnifiedHunks(FileDiff{}, "a\nb\n", 3),
		"a service that reports no hunks must yield no hunks")
}

// gitDiffU0 parses a "git diff -U0" patch through the same conversion the
// git CLI service uses. Its hunks have a shape ConvertChangesToFileDiff
// never produces: a modification is one hunk whose body holds the old and
// the new side together, rather than a deletion hunk followed by an
// insertion hunk. Every patch below is verbatim git output.
func gitDiffU0(t *testing.T, patch string) FileDiff {
	t.Helper()
	d, err := godiff.NewFileDiffReader(strings.NewReader(patch)).Read()
	require.NoError(t, err)
	return gitFileDiff(d)
}

// TestUnifiedHunksGitBody covers the git CLI service, whose hunks differ
// from the in-memory differ's in two ways: a modification's body carries
// both sides, and a removal's new-side start names the line it followed.
// Reading every body line as a deletion turned the new side into a
// phantom "-+new" line, and taking the removal's start literally spliced
// it above the line it came after.
func TestUnifiedHunksGitBody(t *testing.T) {
	for _, tc := range []struct {
		name    string
		patch   string
		current string
		want    string
	}{
		{
			name: "a modification keeps only the old side",
			patch: "--- a/a.go\n+++ b/a.go\n" +
				"@@ -2 +2 @@ func main() {\n-old\n+new\n",
			current: "keep1\nnew\nkeep3\n",
			want: "@@ -1,3 +1,3 @@\n" +
				" keep1\n-old\n+new\n keep3\n",
		},
		{
			name: "a removal and a modification share a window",
			patch: "--- a/a.go\n+++ b/a.go\n" +
				"@@ -2 +2 @@ keep1\n-old\n+new\n" +
				"@@ -5,2 +4,0 @@ keep4\n-gone1\n-gone2\n",
			current: "keep1\nnew\nkeep3\nkeep4\nkeep5\n",
			want: "@@ -1,7 +1,5 @@\n" +
				" keep1\n-old\n+new\n keep3\n keep4\n" +
				"-gone1\n-gone2\n keep5\n",
		},
		{
			name: "the last line is removed",
			patch: "--- a/a.go\n+++ b/a.go\n" +
				"@@ -2 +1,0 @@ keep1\n-keep2\n",
			current: "keep1\n",
			want: "@@ -1,2 +1,1 @@\n" +
				" keep1\n-keep2\n",
		},
		{
			name: "a removed blank line is a blank line",
			patch: "--- a/a.go\n+++ b/a.go\n" +
				"@@ -2 +1,0 @@ keep1\n-\n",
			current: "keep1\nkeep2\n",
			want: "@@ -1,3 +1,2 @@\n" +
				" keep1\n-\n keep2\n",
		},
		{
			name: "an insertion has no old side to read",
			patch: "--- a/a.go\n+++ b/a.go\n" +
				"@@ -1,0 +2 @@ x\n+INS\n",
			current: "x\nINS\ny\n",
			want: "@@ -1,2 +1,3 @@\n" +
				" x\n+INS\n y\n",
		},
		{
			name: "a modification of several lines",
			patch: "--- a/a.go\n+++ b/a.go\n" +
				"@@ -1,2 +1,2 @@\n-a\n-b\n+A\n+B\n",
			current: "A\nB\nkeep\n",
			want: "@@ -1,3 +1,3 @@\n" +
				"-a\n-b\n+A\n+B\n keep\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := UnifiedHunks(gitDiffU0(t, tc.patch), tc.current, 1)
			assert.Equal(t, tc.want, renderUnified(got))
		})
	}
}

// TestUnifiedSection follows the enclosing function git reports for a
// hunk from the service's hunk through to the rendered "@@" header.
func TestUnifiedSection(t *testing.T) {
	const current = "func main() {\n\tprintln(1)\n}\n"
	hunks := UnifiedHunks(FileDiff{Hunks: []Hunk{{
		OrigStartLine: 2,
		OrigLines:     1,
		NewStartLine:  2,
		NewLines:      1,
		Section:       "func main() {",
		Body:          "-\tprintln(0)\n",
	}}}, current, 0)
	require.Len(t, hunks, 1)
	assert.Equal(t, "func main() {", hunks[0].Section)

	buf, _ := UnifiedBuffer(hunks, "main.go", "main.go")
	assert.Contains(t, buf.String(), "@@ -2,1 +2,1 @@ func main() {")
}

func TestUnifiedBuffer(t *testing.T) {
	current := "a\nB\nc\nd\ne\nf\ng\nh\nI\n"
	d := fileDiff(t, "a\nb\nc\nd\ne\nf\ng\nh\ni\n", current)
	hunks := UnifiedHunks(d, current, 1)
	require.Len(t, hunks, 2)

	buf, anchors := UnifiedBuffer(hunks, "main.go", "main.go")
	want := "--- a/main.go\n" +
		"+++ b/main.go\n" +
		"@@ -1,3 +1,3 @@\n" +
		" a\n-b\n+B\n c\n" +
		"@@ -8,2 +8,2 @@\n" +
		" h\n-i\n+I"
	assert.Equal(t, want, buf.String())

	assert.Equal(t, []HunkAnchor{
		{Row: 2, NewStart: 1},
		{Row: 7, NewStart: 8},
	}, anchors)

	cells := buf.RawCells()
	for _, row := range []int{0, 1, 2, 7} {
		assert.NotZerof(t, cells[row][0].Attrs&term.AttrBold,
			"row %d must be emphasised", row)
	}
	assert.Equal(t, delAttr.Bg, cells[4][0].Bg, "removed line tint")
	assert.Equal(t, addAttr.Bg, cells[5][0].Bg, "added line tint")
	assert.Zero(t, cells[3][0].Bg, "context line must not be tinted")
}

// renderSnippets prints every snippet as the buffer rows its lines map
// onto, so a case can state the parsed text and its alignment together.
func renderSnippets(t *testing.T, snippets []Snippet) string {
	t.Helper()
	var b strings.Builder
	for i, s := range snippets {
		lines := strings.Split(s.Text, "\n")
		require.Len(t, s.Rows, len(lines),
			"snippet %d must map every line to a row", i)
		fmt.Fprintf(&b, "snippet %d\n", i)
		for j, l := range lines {
			fmt.Fprintf(&b, "%d|%s\n", s.Rows[j], l)
		}
	}
	return b.String()
}

// assertSnippetAlignment checks each snippet line against the buffer row
// it claims, since a drift there paints the syntax overlay onto the
// wrong line.
func assertSnippetAlignment(t *testing.T, buf string, snippets []Snippet) {
	t.Helper()
	rows := strings.Split(buf, "\n")
	for i, s := range snippets {
		for j, line := range strings.Split(s.Text, "\n") {
			row := s.Rows[j]
			require.Less(t, row, len(rows),
				"snippet %d line %d points past the buffer", i, j)
			content := ""
			if rows[row] != "" {
				content = rows[row][1:]
			}
			assert.Equalf(t, line, content,
				"snippet %d line %d must be buffer row %d", i, j, row)
		}
	}
}

func TestUnifiedSnippets(t *testing.T) {
	uri, err := workspaceapi.ParseURI("file:///repo/main.go")
	require.NoError(t, err)
	for _, tc := range []struct {
		name      string
		committed string
		current   string
		context   int
		want      string
	}{
		{
			name:      "an insertion parses as one side",
			committed: "a\nb\nc\n",
			current:   "a\nb\nnew\nc\n",
			context:   1,
			want: "snippet 0\n" +
				"3|b\n4|new\n5|c\n",
		},
		{
			name:      "a modification splits the sides apart",
			committed: "a\nold\nc\n",
			current:   "a\nnew\nc\n",
			context:   1,
			want: "snippet 0\n" +
				"3|a\n5|new\n6|c\n" +
				"snippet 1\n" +
				"3|a\n4|old\n6|c\n",
		},
		{
			name:      "a deletion keeps the removed line on the old side",
			committed: "a\nb\nc\n",
			current:   "a\nc\n",
			context:   1,
			want: "snippet 0\n" +
				"3|a\n5|c\n" +
				"snippet 1\n" +
				"3|a\n4|b\n5|c\n",
		},
		{
			name:      "a whole new file parses as one side",
			committed: "",
			current:   "a\nb\n",
			context:   3,
			want: "snippet 0\n" +
				"3|a\n4|b\n",
		},
		{
			name:      "an emptied file only has an old side",
			committed: "a\nb\n",
			current:   "",
			context:   3,
			want: "snippet 0\n" +
				"3|a\n4|b\n",
		},
		{
			name:      "a blank line stays a line of the snippet",
			committed: "a\nb\n",
			current:   "a\n\nb\n",
			context:   1,
			want: "snippet 0\n" +
				"3|a\n4|\n5|b\n",
		},
		{
			name:      "each hunk is anchored on its own rows",
			committed: "a\nb\nc\nd\ne\nf\ng\nh\ni\n",
			current:   "A\nb\nc\nd\ne\nf\ng\nh\nI\n",
			context:   1,
			want: "snippet 0\n" +
				"4|A\n5|b\n" +
				"snippet 1\n" +
				"3|a\n5|b\n" +
				"snippet 2\n" +
				"7|h\n9|I\n" +
				"snippet 3\n" +
				"7|h\n8|i\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hunks := UnifiedHunks(
				fileDiff(t, tc.committed, tc.current), tc.current, tc.context)
			buf, anchors := UnifiedBuffer(hunks, "main.go", "main.go")
			snippets := UnifiedSnippets(hunks, anchors, uri)
			assert.Equal(t, tc.want, renderSnippets(t, snippets))
			assertSnippetAlignment(t, buf.String(), snippets)
			for i, s := range snippets {
				assert.Equalf(t, uri, s.URI, "snippet %d URI", i)
			}
		})
	}
}

func TestUnifiedSnippetsEmpty(t *testing.T) {
	var uri workspaceapi.URI
	assert.Empty(t, UnifiedSnippets(nil, nil, uri))

	current := "a\nB\nc\nd\ne\nf\ng\nh\nI\n"
	hunks := UnifiedHunks(
		fileDiff(t, "a\nb\nc\nd\ne\nf\ng\nh\ni\n", current), current, 1)
	_, anchors := UnifiedBuffer(hunks, "main.go", "main.go")
	require.Len(t, anchors, 2)

	// An anchor list that does not cover every hunk cannot place the
	// remaining rows, so those hunks are dropped rather than guessed.
	snippets := UnifiedSnippets(hunks, anchors[:1], uri)
	for i, s := range snippets {
		for _, row := range s.Rows {
			assert.Lessf(t, row, anchors[1].Row,
				"snippet %d must stay inside the anchored hunk", i)
		}
	}
	assert.Len(t, snippets, 2, "only the first hunk's two sides")
}

// TestUnifiedSnippetsHighlight pins the whole overlay path: the rows and
// the gutter offset must line up, so every content cell of a hunk gets
// the syntax foreground while the diff tints and the gutter survive.
func TestUnifiedSnippetsHighlight(t *testing.T) {
	uri, err := workspaceapi.ParseURI("file:///repo/main.go")
	require.NoError(t, err)
	const current = "a\nnew\nc\n"
	hunks := UnifiedHunks(fileDiff(t, "a\nold\nc\n", current), current, 1)
	buf, anchors := UnifiedBuffer(hunks, "main.go", "main.go")
	snippets := UnifiedSnippets(hunks, anchors, uri)
	require.Len(t, snippets, 2)

	parser := &stubParser{byText: map[string][]textapi.Location{}}
	for _, s := range snippets {
		var locs []textapi.Location
		for y, line := range strings.Split(s.Text, "\n") {
			locs = append(locs, highlightAt(0, y, len(line), y))
		}
		parser.byText[s.Text] = locs
	}

	cells := buf.RawCells()
	HighlightSnippets(t.Context(), cells, snippets, parser, 1)

	rows := strings.Split(buf.String(), "\n")
	for y, row := range rows {
		if y <= anchors[0].Row {
			for x := range cells[y] {
				assert.Equalf(t, term.ColorDefault, cells[y][x].Fg,
					"header row %d must not be highlighted", y)
			}
			continue
		}
		assert.Equalf(t, term.ColorDefault, cells[y][0].Fg,
			"the gutter of row %d is not parsed source", y)
		for x := 1; x < len(row); x++ {
			assert.Equalf(t, highlightFg, cells[y][x].Fg,
				"row %d column %d must be highlighted", y, x)
		}
	}

	assert.Equal(t, delAttr.Bg, cells[anchors[0].Row+2][1].Bg,
		"the removed line must keep its tint under the overlay")
	assert.Equal(t, addAttr.Bg, cells[anchors[0].Row+3][1].Bg,
		"the added line must keep its tint under the overlay")
}
