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
	"fmt"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

// UnifiedLineKind classifies a line of a unified diff.
type UnifiedLineKind int

const (
	// UnifiedContext is a line both sides share.
	UnifiedContext UnifiedLineKind = iota
	// UnifiedAdd is a line only the new side has.
	UnifiedAdd
	// UnifiedDel is a line only the original side has.
	UnifiedDel
)

// UnifiedLine is one rendered line of a unified diff, without its
// ' ', '+' or '-' gutter.
type UnifiedLine struct {
	Kind UnifiedLineKind
	Text string
}

// UnifiedHunk is a contiguous block of a unified diff together with the
// line ranges its "@@" header advertises.
type UnifiedHunk struct {
	OrigStart int
	OrigLines int
	NewStart  int
	NewLines  int
	Section   string
	Lines     []UnifiedLine
}

// HunkAnchor records where a hunk header landed in a rendered buffer
// and which new-file line the hunk starts at.
type HunkAnchor struct {
	Row      int
	NewStart int
}

// unifiedChange is one edit against the new side: del lines replaced by
// the newCount lines of newContent starting at newStart. A pure
// deletion has newCount == 0 and sits immediately before newStart.
type unifiedChange struct {
	newStart  int
	newCount  int
	origStart int
	del       []string
	section   string
}

// UnifiedHunks turns d into unified-diff hunks against newContent,
// splicing contextLines of surrounding source around each change and
// coalescing changes whose context windows touch.
//
// The coalescing matters: ConvertChangesToFileDiff emits -U0 hunks, so
// a modification arrives as a deletion hunk immediately followed by an
// insertion hunk. Merging them back produces the single -old/+new block
// a reader expects.
func UnifiedHunks(d FileDiff, newContent string, contextLines int) []UnifiedHunk {
	if contextLines < 0 {
		contextLines = 0
	}
	newLines := splitUnifiedLines(newContent)
	changes := collectUnifiedChanges(d, len(newLines))
	if len(changes) == 0 {
		return nil
	}

	var hunks []UnifiedHunk
	for i := 0; i < len(changes); {
		start := max(0, changes[i].newStart-contextLines)
		end := min(len(newLines),
			changes[i].newStart+changes[i].newCount+contextLines)
		j := i + 1
		for j < len(changes) && changes[j].newStart-contextLines <= end {
			end = min(len(newLines),
				changes[j].newStart+changes[j].newCount+contextLines)
			j++
		}
		hunks = append(hunks,
			renderUnifiedHunk(newLines, changes[i:j], start, end))
		i = j
	}
	return hunks
}

func renderUnifiedHunk(
	newLines []string, group []unifiedChange, start, end int,
) UnifiedHunk {
	ret := UnifiedHunk{
		// Every line between start and the first change exists on both
		// sides, so both indexes shift by the same amount.
		OrigStart: group[0].origStart - (group[0].newStart - start),
		NewStart:  start,
	}
	pos := start
	for _, c := range group {
		if ret.Section == "" {
			ret.Section = c.section
		}
		for ; pos < c.newStart; pos++ {
			ret.Lines = append(ret.Lines,
				UnifiedLine{Kind: UnifiedContext, Text: newLines[pos]})
		}
		for _, text := range c.del {
			ret.Lines = append(ret.Lines,
				UnifiedLine{Kind: UnifiedDel, Text: text})
		}
		for ; pos < c.newStart+c.newCount; pos++ {
			ret.Lines = append(ret.Lines,
				UnifiedLine{Kind: UnifiedAdd, Text: newLines[pos]})
		}
	}
	for ; pos < end; pos++ {
		ret.Lines = append(ret.Lines,
			UnifiedLine{Kind: UnifiedContext, Text: newLines[pos]})
	}

	for _, l := range ret.Lines {
		switch l.Kind {
		case UnifiedContext:
			ret.OrigLines++
			ret.NewLines++
		case UnifiedAdd:
			ret.NewLines++
		case UnifiedDel:
			ret.OrigLines++
		}
	}
	// An empty range in a unified header is reported at the line it
	// would follow, not at the line it would start on.
	if ret.OrigLines > 0 {
		ret.OrigStart++
	}
	if ret.NewLines > 0 {
		ret.NewStart++
	}
	return ret
}

// collectUnifiedChanges walks d's hunks against a new-side cursor,
// merging hunks that meet with no intervening shared line.
func collectUnifiedChanges(d FileDiff, newLen int) []unifiedChange {
	var (
		changes               []unifiedChange
		newCursor, origCursor int
	)
	for _, h := range d.Hunks {
		start := min(max(int(h.NewStartLine)-1, newCursor), newLen)
		origCursor += start - newCursor
		newCursor = start

		if len(changes) == 0 ||
			changes[len(changes)-1].newStart+
				changes[len(changes)-1].newCount != start {
			changes = append(changes, unifiedChange{
				newStart: start, origStart: origCursor, section: h.Section,
			})
		}
		cur := &changes[len(changes)-1]
		if cur.section == "" {
			cur.section = h.Section
		}
		if n := min(int(h.NewLines), newLen-newCursor); n > 0 {
			cur.newCount += n
			newCursor += n
		}
		if h.OrigLines > 0 {
			del := bodyLines(h.Body, '-')
			cur.del = append(cur.del, del...)
			origCursor += len(del)
		}
	}
	return changes
}

// bodyLines returns the lines of a hunk body that belong to the side
// prefix marks. The git CLI service reports a modification as a single
// hunk whose body holds both sides, so the other side's lines have to be
// dropped rather than read as content.
func bodyLines(body string, prefix byte) []string {
	var ret []string
	for l := range strings.SplitSeq(body, "\n") {
		// A line that is exactly the prefix was empty in the source;
		// only the trailing newline's remainder is truly blank.
		if l == "" || l[0] != prefix {
			continue
		}
		ret = append(ret, l[1:])
	}
	return ret
}

func splitUnifiedLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// UnifiedBuffer renders hunks as a unified diff and returns the buffer
// together with the row each hunk header landed on. Changed lines carry
// the same background tints DiffBuffer uses, leaving the foreground
// free for a syntax overlay.
func UnifiedBuffer(
	hunks []UnifiedHunk, origName, newName string,
) (*cell.Buffer, []HunkAnchor) {
	buf := cell.NewBuffer()
	cursor := term.Coordinates{}
	row := -1
	write := func(text string, attr term.Attributes) {
		row++
		if row > 0 {
			_, cursor = buf.InsertString(cursor, "\n")
		}
		_, cursor = buf.InsertStringWithAttr(cursor, text, attr)
	}

	type span struct{ row, width int }
	var bold []span
	emphasised := func(text string) {
		write(text, term.Attributes{})
		bold = append(bold, span{row: row, width: len([]rune(text))})
	}
	emphasised("--- a/" + origName)
	emphasised("+++ b/" + newName)

	anchors := make([]HunkAnchor, 0, len(hunks))
	for _, h := range hunks {
		header := fmt.Sprintf("@@ -%d,%d +%d,%d @@",
			h.OrigStart, h.OrigLines, h.NewStart, h.NewLines)
		if h.Section != "" {
			header += " " + h.Section
		}
		emphasised(header)
		anchors = append(anchors, HunkAnchor{Row: row, NewStart: h.NewStart})
		for _, l := range h.Lines {
			switch l.Kind {
			case UnifiedAdd:
				write("+"+l.Text, addAttr)
			case UnifiedDel:
				write("-"+l.Text, delAttr)
			case UnifiedContext:
				write(" "+l.Text, term.Attributes{})
			}
		}
	}

	cells := buf.RawCells()
	for _, s := range bold {
		BoldRow(cells, s.row, s.width)
	}
	return buf, anchors
}

// UnifiedSnippets gathers each side of every hunk as gutter-free
// source: context plus additions for the new side, context plus
// deletions for the old one, since a mixed block is not parseable as
// either. anchors must be the ones UnifiedBuffer returned for hunks.
func UnifiedSnippets(
	hunks []UnifiedHunk, anchors []HunkAnchor, uri workspaceapi.URI,
) []Snippet {
	var ret []Snippet
	for i, h := range hunks {
		if i >= len(anchors) {
			break
		}
		first := anchors[i].Row + 1
		ret = appendUnifiedSnippet(ret, uri, h, first, UnifiedDel)
		if hunkHasKind(h, UnifiedDel) {
			ret = appendUnifiedSnippet(ret, uri, h, first, UnifiedAdd)
		}
	}
	return ret
}

func appendUnifiedSnippet(
	dst []Snippet, uri workspaceapi.URI,
	h UnifiedHunk, first int, exclude UnifiedLineKind,
) []Snippet {
	s := Snippet{URI: uri}
	var b strings.Builder
	for i, l := range h.Lines {
		if l.Kind == exclude {
			continue
		}
		if len(s.Rows) > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(l.Text)
		s.Rows = append(s.Rows, first+i)
	}
	if len(s.Rows) == 0 {
		return dst
	}
	s.Text = b.String()
	return append(dst, s)
}

func hunkHasKind(h UnifiedHunk, kind UnifiedLineKind) bool {
	for _, l := range h.Lines {
		if l.Kind == kind {
			return true
		}
	}
	return false
}
