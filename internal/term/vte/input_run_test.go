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

package vte

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/vte/vteparser"
	"unstable.build/rune/internal/term/vte/vtescreen"
)

func TestAdvanceBytesEquivalence(t *testing.T) {
	t.Parallel()
	streams := []struct {
		name   string
		stream string
	}{
		{"plain", "hello world"},
		{"crlf lines", "line one\r\nline two\r\nline three\r\n"},
		{"wrap", strings.Repeat("abcdefgh", 12)},
		{"sgr colors", "\x1b[31mred\x1b[42;1mgreen bg\x1b[0mplain tail after reset"},
		{"utf8 mixed", "héllo wörld … 漢字 tail ascii run 12345"},
		{"cjk only", "夜半钟声到客船月落乌啼霜满天江枫渔火对愁眠姑苏城外寒山寺"},
		{"cjk wraps row", strings.Repeat("漢字", 40)},
		{"cjk at odd row edge", "a" + strings.Repeat("漢", 40)},
		{"cjk then ascii at edge", strings.Repeat("漢", 11) + "xy" + strings.Repeat("字", 8)},
		{"wide overwrites wide", strings.Repeat("漢", 12) + "\x1b[H" + strings.Repeat("字", 12)},
		{"wide overwrites wide offset", strings.Repeat("漢", 12) + "\x1b[H" + "a" + strings.Repeat("字", 8)},
		{"ascii overwrites wide", strings.Repeat("漢", 12) + "\x1b[H" + strings.Repeat("z", 20)},
		{"combining after wide run", "漢字漢字e\u0301 tail"},
		{"combining after cjk", "夜半钟声\u0301 tail"},
		{"hangul run", "한국어단어테스트 tail"},
		{"hangul jamo run", strings.Repeat("\u1100\u1161\u11A8", 6) + " tail"},
		{"wide run then zwj emoji", "漢字👨\u200d👩\u200d👧 tail"},
		{"wide run then variation selector", "漢字❤\ufe0f tail"},
		{"wide run in insert mode", "\x1b[4h漢字漢字\x1b[4lappend"},
		{"wide run with charset", "\x1b(0漢字lqqk\x1b(B漢字"},
		{"combining marks", "e\u0301e\u0301 a\u0300b\u0327c\u030a tail"},
		{"emoji zwj and skin tones", "👨\u200d👩\u200d👧 👍\U0001F3FD 🇺🇸🇯🇵 ❤\ufe0f"},
		{"hangul jamo", "\u1100\u1161\u11A8 한글 tail"},
		{"c1 controls as utf8", "a\u0085b\u009bc\u0090d"},
		{"replacement char literal", "a\ufffdb\ufffd\ufffdc"},
		{"stray continuation bytes", "ab\x80\xbfcd"},
		{"overlong encoding", "ab\xc0\x80\xc1\xbfcd"},
		{"surrogate encoding", "ab\xed\xa0\x80cd"},
		{"out of range lead bytes", "ab\xf5\x80\x80\x80\xffcd"},
		{"truncated sequence at end", "ab\xe6\xbc"},
		{"truncated sequence mid stream", "ab\xe6\xbccd\xf0\x9f\x98"},
		{"utf8 then del", "漢\x7f字\x7f"},
		{"utf8 across escape", "漢\x1b[31m字\x1b[0m漢"},
		{"utf8 in insert mode", "before\x1b[4h漢字\x1b[4lappend"},
		{"rep after utf8", "漢\x1b[5b tail"},
		{"linedraw charset", "\x1b(0lqqqk\x1b(Bascii after"},
		{"insert mode", "before\x1b[4hINSERTED\x1b[4lappend"},
		{"rep after run", "abc\x1b[5b tail"},
		{"rep after escape", "abc\x1b[31m\x1b[2b tail"},
		{"osc title then text", "\x1b]2;mytitle\x1b\\after title"},
		{"cursor movement", "12345\x1b[2Gmid\x1b[Hhome"},
		{"alt buffer", "prim\x1b[?1049hALT SCREEN\x1b[?1049lback"},
		{"clear and rewrite", "first\x1b[2Jsecond"},
		{"del mixed", "abc\x7fdef"},
		{"tabs", "a\tb\tc"},
		{"conceal", "plain\x1b[8mhidden\x1b[28mvisible"},
		{"sync update", "\x1b[?2026habc\x1b[?2026ldef"},
		{"long stream", strings.Repeat("the quick brown fox jumps over the lazy dog\r\n", 40)},
	}
	chunkings := []int{1, 7, 1 << 20}

	for _, tc := range streams {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			want := parserStateDump(t, tc.stream, func(p *vteparser.Parser, b []byte) {
				for _, ch := range b {
					p.Advance(ch)
				}
			}, 1<<20)

			for _, chunk := range chunkings {
				got := parserStateDump(t, tc.stream, (*vteparser.Parser).AdvanceBytes, chunk)
				require.Equal(t, want, got, "AdvanceBytes chunk=%d diverged from Advance", chunk)
			}
		})
	}
}

func TestASCIIRunLen(t *testing.T) {
	t.Parallel()

	for _, high := range []byte{0x80, 0xC3, 0xFF} {
		for prefix := range 24 {
			for suffix := range 3 {
				run := append([]byte(strings.Repeat("a", prefix)), high)
				run = append(run, strings.Repeat("b", suffix)...)
				require.Equal(t, prefix, asciiRunLen(run),
					"prefix=%d high=%#x suffix=%d", prefix, high, suffix)
			}
		}
	}

	for length := range 24 {
		run := []byte(strings.Repeat("a", length))
		require.Equal(t, length, asciiRunLen(run), "all ASCII, length=%d", length)
	}
}

func TestInputRunFastPathsTable(t *testing.T) {
	tests := []struct {
		name           string
		input          []byte
		width          int
		charset        vteparser.CharsetIndex
		glyphWrite     int
		wantRuns       []string
		wantGlyphRuns  [][]vtescreen.Glyph
		wantWrites     []rune
		wantRunCharset []vteparser.CharsetIndex
		wantGlyphSet   []vteparser.CharsetIndex
		wantCursor     term.Coordinates
		wantShouldWrap bool
	}{
		{
			name:           "ascii run",
			input:          []byte("abcdef"),
			width:          8,
			glyphWrite:     -1,
			wantRuns:       []string{"abcdef"},
			wantRunCharset: []vteparser.CharsetIndex{vteparser.CharsetIndexG0},
			wantCursor:     term.Coordinates{X: 6},
		},
		{
			name:       "wide glyph run",
			input:      []byte("漢字"),
			width:      8,
			glyphWrite: -1,
			wantGlyphRuns: [][]vtescreen.Glyph{{
				{Ch: '漢', Width: 2},
				{Ch: '字', Width: 2},
			}},
			wantGlyphSet: []vteparser.CharsetIndex{vteparser.CharsetIndexG0},
			wantCursor:   term.Coordinates{X: 4},
		},
		{
			name:       "ascii and glyph runs retain charset",
			input:      []byte("ab漢字cd"),
			width:      12,
			charset:    vteparser.CharsetIndexG1,
			glyphWrite: -1,
			wantRuns:   []string{"ab", "cd"},
			wantGlyphRuns: [][]vtescreen.Glyph{{
				{Ch: '漢', Width: 2},
				{Ch: '字', Width: 2},
			}},
			wantRunCharset: []vteparser.CharsetIndex{
				vteparser.CharsetIndexG1,
				vteparser.CharsetIndexG1,
			},
			wantGlyphSet: []vteparser.CharsetIndex{vteparser.CharsetIndexG1},
			wantCursor:   term.Coordinates{X: 8},
		},
		{
			name:       "glyph writer fallback consumes one rune",
			input:      []byte("漢字"),
			width:      8,
			glyphWrite: 0,
			wantGlyphRuns: [][]vtescreen.Glyph{
				{{Ch: '漢', Width: 2}, {Ch: '字', Width: 2}},
				{{Ch: '字', Width: 2}},
			},
			wantWrites:   []rune{'漢', '字'},
			wantGlyphSet: []vteparser.CharsetIndex{vteparser.CharsetIndexG0, vteparser.CharsetIndexG0},
			wantCursor:   term.Coordinates{X: 4},
		},
		{
			name:       "wide glyph exact fill leaves delayed wrap",
			input:      []byte("漢字"),
			width:      4,
			glyphWrite: -1,
			wantGlyphRuns: [][]vtescreen.Glyph{{
				{Ch: '漢', Width: 2},
				{Ch: '字', Width: 2},
			}},
			wantGlyphSet:   []vteparser.CharsetIndex{vteparser.CharsetIndexG0},
			wantCursor:     term.Coordinates{X: 2},
			wantShouldWrap: true,
		},
		{
			name:       "malformed utf8 falls back without stalling",
			input:      []byte{0xff, 'a'},
			width:      8,
			glyphWrite: -1,
			wantRuns:   []string{"a"},
			wantWrites: []rune{utf8.RuneError},
			wantRunCharset: []vteparser.CharsetIndex{
				vteparser.CharsetIndexG0,
			},
			wantCursor: term.Coordinates{X: 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ph := newInputParserHandler(t, false)
			ph.Resize(tt.width, 3)
			recorder := &recordingScreenBuffer{
				screenBuffer: ph.sync.buf,
				glyphWrite:   tt.glyphWrite,
			}
			ph.sync.buf = recorder
			ph.currentCharset = tt.charset

			ph.InputRun(tt.input)

			assert.Equal(t, tt.wantRuns, recorder.runs)
			assert.Equal(t, tt.wantGlyphRuns, recorder.glyphRuns)
			assert.Equal(t, tt.wantWrites, recorder.writes)
			assert.Equal(t, tt.wantRunCharset, recorder.runCharsets)
			assert.Equal(t, tt.wantGlyphSet, recorder.glyphCharsets)
			assert.Equal(t, tt.wantCursor, recorder.CursorAtScreen())
			assert.Equal(t, tt.wantShouldWrap, ph.shouldWrap)
		})
	}
}

func TestInputGlyphRunScratchDropsStaleGlyphs(t *testing.T) {
	ph := newInputParserHandler(t, false)
	ph.Resize(20, 3)
	recorder := &recordingScreenBuffer{screenBuffer: ph.sync.buf, glyphWrite: -1}
	ph.sync.buf = recorder

	ph.InputRun([]byte("漢字語"))
	ph.InputRun([]byte("界"))

	require.Len(t, recorder.glyphRuns, 2)
	assert.Equal(t, []vtescreen.Glyph{
		{Ch: '漢', Width: 2},
		{Ch: '字', Width: 2},
		{Ch: '語', Width: 2},
	}, recorder.glyphRuns[0])
	assert.Equal(t, []vtescreen.Glyph{{Ch: '界', Width: 2}}, recorder.glyphRuns[1])
}

func TestInputRunScreenStateTable(t *testing.T) {
	tests := []struct {
		name           string
		alt            bool
		width          int
		setup          func(*parserHandler)
		inputs         [][]byte
		wantRows       []string
		wantCursor     term.Coordinates
		wantShouldWrap bool
	}{
		{
			name:           "ascii exact fill",
			width:          5,
			inputs:         [][]byte{[]byte("abcde")},
			wantRows:       []string{"abcde", "     ", "     "},
			wantCursor:     term.Coordinates{X: 4},
			wantShouldWrap: true,
		},
		{
			name:       "ascii delayed wrap",
			width:      5,
			inputs:     [][]byte{[]byte("abcde"), []byte("f")},
			wantRows:   []string{"abcde", "f    ", "     "},
			wantCursor: term.Coordinates{X: 1, Y: 1},
		},
		{
			name:       "wide cannot straddle primary margin",
			width:      5,
			inputs:     [][]byte{[]byte("abcd漢")},
			wantRows:   []string{"abcd ", "漢    ", "     "},
			wantCursor: term.Coordinates{X: 2, Y: 1},
		},
		{
			name:       "mixed wide cursor uses visual columns on primary",
			width:      8,
			inputs:     [][]byte{[]byte("ab漢cd")},
			wantRows:   []string{"ab漢 cd  ", "        ", "        "},
			wantCursor: term.Coordinates{X: 6},
		},
		{
			name:       "mixed wide cursor uses glyph cells on alternate",
			alt:        true,
			width:      8,
			inputs:     [][]byte{[]byte("ab漢cd")},
			wantRows:   []string{"ab漢 cd  ", "        ", "        "},
			wantCursor: term.Coordinates{X: 5},
		},
		{
			name:  "insert mode preserves per-glyph semantics",
			width: 5,
			setup: func(ph *parserHandler) {
				ph.InputRun([]byte("abcde"))
				ph.GotoCol(1)
				ph.SetMode(vteparser.ModeInsert)
			},
			inputs:     [][]byte{[]byte("XY")},
			wantRows:   []string{"aXYbc", "     ", "     "},
			wantCursor: term.Coordinates{X: 3},
		},
		{
			name:       "combining mark after bulk glyph run",
			width:      8,
			inputs:     [][]byte{[]byte("漢字e\u0301")},
			wantRows:   []string{"漢 字 e   ", "        ", "        "},
			wantCursor: term.Coordinates{X: 5},
		},
		{
			name:       "malformed utf8 writes replacement then ascii",
			width:      5,
			inputs:     [][]byte{{0xff, 'a'}},
			wantRows:   []string{"�a   ", "     ", "     "},
			wantCursor: term.Coordinates{X: 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ph := newInputParserHandler(t, tt.alt)
			ph.Resize(tt.width, 3)
			if tt.setup != nil {
				tt.setup(ph)
			}
			for _, input := range tt.inputs {
				ph.InputRun(input)
			}

			assertScreenRows(t, ph, tt.wantRows)
			assert.Equal(t, tt.wantCursor, ph.sync.buf.CursorAtScreen())
			assert.Equal(t, tt.wantShouldWrap, ph.shouldWrap)
			if tt.name == "combining mark after bulk glyph run" {
				cells := firstRowCells(ph)
				require.GreaterOrEqual(t, len(cells), 3)
				assert.Equal(t, 'e', cells[2].Ch)
				assert.Equal(t, []rune{'\u0301'}, cells[2].CombiningRunes())
			}
		})
	}
}

func TestAdvanceBytesEverySplitRealisticStreams(t *testing.T) {
	tests := []struct {
		name   string
		width  int
		height int
		stream []byte
	}{
		{
			name:   "shell prompt and colored listing",
			width:  32,
			height: 6,
			stream: []byte("\x1b]2;user@host: ~/src\x07\x1b[32muser@host\x1b[0m:\x1b[34m~/src\x1b[0m$ \x1b[1;31mREADME.md\x1b[0m\r\n"),
		},
		{
			name:   "progress redraw rep and cjk",
			width:  24,
			height: 5,
			stream: []byte("build [          ]\r\x1b[7C\x1b[42m \x1b[0m\x1b[4b 40% 漢字\r\x1b[2Kdone\r\n"),
		},
		{
			name:   "alternate cursor addressed interface",
			width:  20,
			height: 5,
			stream: []byte("before\x1b[?1049h\x1b[2J\x1b[H\x1b[1;34mDashboard\x1b[0m\x1b[3;2H漢字 e\u0301\x1b[?1049l after"),
		},
		{
			name:   "synchronized tmux style frame",
			width:  24,
			height: 6,
			stream: []byte("prefix\x1b[?2026h\x1b[H\x1b[38;5;9mrow one\x1b[0m\r\nrow two 漢字\x1b[2;5H!\x1b[?2026ltail\x1b[3b"),
		},
		{
			name:   "malformed and truncated boundaries",
			width:  24,
			height: 4,
			stream: []byte{'a', 'b', 0xe6, 0xbc, 'X', 0x80, 0xff, 0x1b, '[', '3', '1', 'm', 'z', 0x1b, '[', '0', 'm'},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := parserStateDumpBytes(t, tt.stream, tt.width, tt.height, func(
				p *vteparser.Parser, b []byte,
			) {
				for _, ch := range b {
					p.Advance(ch)
				}
			})

			for split := 0; split <= len(tt.stream); split++ {
				got := parserStateDumpBytes(t, tt.stream, tt.width, tt.height, func(
					p *vteparser.Parser, b []byte,
				) {
					p.AdvanceBytes(b[:split])
					p.AdvanceBytes(b[split:])
				})
				require.Equal(t, want, got, "split=%d", split)
			}
		})
	}
}

type recordingScreenBuffer struct {
	screenBuffer
	glyphWrite    int
	runs          []string
	glyphRuns     [][]vtescreen.Glyph
	writes        []rune
	runCharsets   []vteparser.CharsetIndex
	glyphCharsets []vteparser.CharsetIndex
}

func (b *recordingScreenBuffer) Write(c rune, width int, charset vteparser.CharsetIndex) {
	b.writes = append(b.writes, c)
	b.screenBuffer.Write(c, width, charset)
}

func (b *recordingScreenBuffer) WriteRun(
	run []byte, charset vteparser.CharsetIndex,
) int {
	b.runs = append(b.runs, string(append([]byte(nil), run...)))
	b.runCharsets = append(b.runCharsets, charset)
	return b.screenBuffer.WriteRun(run, charset)
}

func (b *recordingScreenBuffer) WriteGlyphRun(
	glyphs []vtescreen.Glyph, charset vteparser.CharsetIndex,
) int {
	b.glyphRuns = append(b.glyphRuns, append([]vtescreen.Glyph(nil), glyphs...))
	b.glyphCharsets = append(b.glyphCharsets, charset)
	if b.glyphWrite >= 0 {
		return b.glyphWrite
	}
	return b.screenBuffer.WriteGlyphRun(glyphs, charset)
}

func assertScreenRows(t *testing.T, ph *parserHandler, want []string) {
	t.Helper()
	writer := term.NewStringWriter(ph.width, ph.height)
	switch buf := ph.sync.buf.(type) {
	case *vtescreen.PrimaryBuffer:
		buf.Draw(writer)
	case *vtescreen.AltBuffer:
		buf.Draw(writer)
	default:
		require.Failf(t, "unexpected screen buffer", "%T", ph.sync.buf)
	}
	require.NoError(t, writer.Flush())
	assert.Equal(t, strings.Join(want, "\n"), writer.String())
}

func parserStateDumpBytes(
	t *testing.T,
	stream []byte,
	width, height int,
	feed func(*vteparser.Parser, []byte),
) string {
	t.Helper()
	ph := newInputParserHandler(t, false)
	ph.Resize(width, height)
	parser := vteparser.NewParser(ph, new(vteparser.StdTimeout))
	feed(parser, stream)
	return dumpParserState(ph)
}

func parserStateDump(
	t *testing.T, stream string,
	feed func(*vteparser.Parser, []byte), chunk int,
) string {
	t.Helper()
	ph := newInputParserHandler(t, false)
	ph.Resize(24, 8)
	parser := vteparser.NewParser(ph, new(vteparser.StdTimeout))

	data := []byte(stream)
	for len(data) > 0 {
		n := min(chunk, len(data))
		feed(parser, data[:n])
		data = data[n:]
	}

	return dumpParserState(ph)
}

func dumpParserState(ph *parserHandler) string {
	var sb strings.Builder
	dumpCells(&sb, "prim", ph.sync.primBuf.Cells.RawCells())
	dumpCells(&sb, "alt", ph.sync.altBuf.Cells.RawCells())
	fmt.Fprintf(&sb, "cursor:%+v shouldWrap:%t useAlt:%t title:%q attrs:%+v",
		ph.sync.buf.CursorAtScroll(), ph.shouldWrap, ph.useAlt,
		ph.title, ph.sync.buf.CursorAttributes())
	return sb.String()
}

// dumpCells renders the grid by value. term.Cell holds its combining
// runes behind a pointer, so the default %+v formatting embeds a heap
// address and two runs that agree on content still compare unequal.
func dumpCells(sb *strings.Builder, name string, cells [][]term.Cell) {
	for y, row := range cells {
		fmt.Fprintf(sb, "%s[%d]:", name, y)
		for _, c := range row {
			fmt.Fprintf(sb, " %U%U/w%d/fg%v/bg%v/a%d/b%d",
				c.Ch, c.CombiningRunes(), c.Width, c.Fg, c.Bg, c.Attrs, c.Bytes)
		}
		sb.WriteByte('\n')
	}
}
