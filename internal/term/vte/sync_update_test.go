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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"unstable.build/rune/internal/term/vte/vteparser"
)

// syncEquivalenceStreams are escape-heavy payloads whose rendered result
// must not depend on whether the emitter wrapped them in a synchronized
// update, since BSU/ESU only defers presentation.
var syncEquivalenceStreams = []struct {
	name   string
	stream string
}{
	{"plain text", "hello sync world"},
	{"sgr frame", "\x1b[31mred\x1b[42;1mgreen\x1b[0mplain"},
	{"cursor addressing", "\x1b[2;3Habc\x1b[1;1Hdef\x1b[4;1Hghi"},
	{"crlf lines", "line one\r\nline two\r\nline three\r\n"},
	{"wrap", strings.Repeat("abcdefgh", 12)},
	{"utf8", "héllo wörld … 漢字 tail ascii run 12345"},
	{"combining marks", "e\u0301e\u0301 a\u0300b\u0327c\u030a tail"},
	{"rep inside region", "abc\x1b[5b tail"},
	{"rep after utf8", "漢\x1b[5b tail"},
	{"insert mode", "before\x1b[4hINSERTED\x1b[4lappend"},
	{"linedraw charset", "\x1b(0lqqqk\x1b(Bascii after"},
	{"clear and rewrite", "first\x1b[2Jsecond"},
	{"osc title", "\x1b]2;mytitle\x1b\\after title"},
	{"alt buffer", "prim\x1b[?1049hALT SCREEN\x1b[?1049lback"},
	{"tabs and del", "a\tb\tc\x7fd"},
	{"full frame", strings.Repeat("\x1b[1;1H\x1b[38;5;9mrow\x1b[0m\r\n", 12)},
}

func TestSyncUpdateReplayMatchesUnwrapped(t *testing.T) {
	t.Parallel()
	chunkings := []int{1, 2, 3, 5, 7, 8, 9, 13, 1 << 20}

	for _, tc := range syncEquivalenceStreams {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			want := parserStateDump(t, tc.stream,
				(*vteparser.Parser).AdvanceBytes, 1<<20)
			wrapped := "\x1b[?2026h" + tc.stream + "\x1b[?2026l"

			for _, chunk := range chunkings {
				got := parserStateDump(t, wrapped,
					(*vteparser.Parser).AdvanceBytes, chunk)
				require.Equal(t, want, got,
					"synchronized replay chunk=%d diverged", chunk)
			}
		})
	}
}

func TestSyncUpdateReplayMatchesPerByte(t *testing.T) {
	t.Parallel()

	for _, tc := range syncEquivalenceStreams {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wrapped := "\x1b[?2026h" + tc.stream + "\x1b[?2026l"
			want := parserStateDump(t, wrapped, func(p *vteparser.Parser, b []byte) {
				for _, ch := range b {
					p.Advance(ch)
				}
			}, 1<<20)

			got := parserStateDump(t, wrapped,
				(*vteparser.Parser).AdvanceBytes, 1<<20)
			require.Equal(t, want, got)
		})
	}
}

func TestSyncUpdateMarkerSplitAtEveryOffset(t *testing.T) {
	t.Parallel()
	const body = "\x1b[33mpayload\x1b[0m body text"
	want := parserStateDump(t, body, (*vteparser.Parser).AdvanceBytes, 1<<20)

	// A marker is 8 bytes; split before it, inside it at every offset and
	// after it.
	for split := range len("\x1b[?2026l") + 1 {
		t.Run("esu_split_"+string(rune('0'+split)), func(t *testing.T) {
			t.Parallel()
			head := "\x1b[?2026h" + body + "\x1b[?2026l"[:split]
			tail := "\x1b[?2026l"[split:]

			ph := newInputParserHandler(t, false)
			ph.Resize(24, 8)
			parser := vteparser.NewParser(ph)
			parser.AdvanceBytes([]byte(head))
			parser.AdvanceBytes([]byte(tail))

			require.Equal(t, want, dumpParserState(ph))
		})
	}

	// The same for the leading BSU marker.
	for split := range len("\x1b[?2026h") + 1 {
		t.Run("bsu_split_"+string(rune('0'+split)), func(t *testing.T) {
			t.Parallel()
			head := "\x1b[?2026h"[:split]
			tail := "\x1b[?2026h"[split:] + body + "\x1b[?2026l"

			ph := newInputParserHandler(t, false)
			ph.Resize(24, 8)
			parser := vteparser.NewParser(ph)
			parser.AdvanceBytes([]byte(head))
			parser.AdvanceBytes([]byte(tail))

			require.Equal(t, want, dumpParserState(ph))
		})
	}
}

func TestSyncUpdateNestedBSUMatchesPerByte(t *testing.T) {
	t.Parallel()
	stream := "\x1b[?2026habc\x1b[?2026hdef\x1b[?2026lghi"

	want := parserStateDump(t, stream, func(p *vteparser.Parser, b []byte) {
		for _, ch := range b {
			p.Advance(ch)
		}
	}, 1<<20)

	for _, chunk := range []int{1, 5, 8, 1 << 20} {
		got := parserStateDump(t, stream, (*vteparser.Parser).AdvanceBytes, chunk)
		require.Equal(t, want, got, "chunk=%d", chunk)
	}
}
