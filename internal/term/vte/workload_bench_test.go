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
	"runtime"
	"strings"
	"testing"

	"unstable.build/rune/internal/term/vte/vteparser"
)

// BenchmarkVTEWorkloads replays synthetic equivalents of the vtebench
// corpora through the production parse path (batched AdvanceBytes at the
// 64 KiB batch size the gather stage uses) so parser and grid costs can be
// attributed without depending on external fixtures.
//
// Width-sensitive workloads run at both 80 and 240 columns: the cost of a
// region scroll is linear in width, so a single geometry hides the
// dominant term.
func BenchmarkVTEWorkloads(b *testing.B) {
	const height = 24

	workloads := []struct {
		name   string
		widths []int
		// setup is replayed before the timer starts.
		setup func(width, height int) []byte
		// payload is replayed once per iteration, under the timer.
		payload func(width, height int) []byte
	}{
		{
			name:   "scroll_short",
			widths: []int{80, 240},
			setup: func(_, _ int) []byte {
				return buildLinefeedPayload(scrollbackPrefillLines)
			},
			payload: func(_, _ int) []byte {
				return buildLinefeedPayload(scrollWorkloadLines)
			},
		},
		{
			name:   "scroll_alt_region",
			widths: []int{80, 240},
			setup: func(_, height int) []byte {
				return fmt.Appendf(nil, "\x1b[?1049h\x1b[1;%dr", height-1)
			},
			payload: func(_, _ int) []byte {
				return buildLinefeedPayload(scrollWorkloadLines)
			},
		},
		{
			// vtebench's dense_cells runs on the alternate screen.
			name:    "dense_cells",
			widths:  []int{80, 240},
			setup:   altScreen,
			payload: buildDenseCellsPayload,
		},
		{
			name:   "sync_cells",
			widths: []int{80},
			payload: func(width, height int) []byte {
				return buildSyncFramesPayload(width, height, true)
			},
		},
		{
			// Control for sync_cells: byte-identical frames with the
			// BSU/ESU markers removed, isolating the synchronized-update
			// replay path from the work the frames themselves cost.
			name:   "sync_cells_control",
			widths: []int{80},
			payload: func(width, height int) []byte {
				return buildSyncFramesPayload(width, height, false)
			},
		},
		{
			// The two screens cost very differently: the alternate
			// buffer indexes cells directly, while the primary buffer
			// tracks visual columns and must consume the cells a wide
			// glyph covers. vtebench only measures the alternate screen,
			// so the primary case — ordinary shell output containing
			// CJK — needs its own entry or regressions there go unseen.
			name:    "unicode_primary",
			widths:  []int{80, 240},
			payload: buildWideUnicodePayload,
		},
		{
			name:    "unicode_alt",
			widths:  []int{80, 240},
			setup:   altScreen,
			payload: buildWideUnicodePayload,
		},
	}

	for _, w := range workloads {
		for _, width := range w.widths {
			b.Run(fmt.Sprintf("%s/w%d", w.name, width), func(b *testing.B) {
				var setup []byte
				if w.setup != nil {
					setup = w.setup(width, height)
				}
				payload := w.payload(width, height)

				// One terminal for the whole run: these workloads all
				// reach a steady state (scrollback full, grid painted)
				// that warmToSteadyState establishes and later replays
				// preserve. Rebuilding it per iteration would leave the
				// setup's garbage to be collected inside the timed
				// window.
				ph := newBenchParserHandler(width, height)
				parser := vteparser.NewParser(ph)
				advanceBatched(parser, setup)
				warmToSteadyState(ph, parser, payload)
				runtime.GC()

				b.ReportAllocs()
				b.SetBytes(int64(len(payload)))
				b.ResetTimer()

				for range b.N {
					advanceBatched(parser, payload)
				}
			})
		}
	}
}

const (
	// scrollbackPrefillLines pins the primary buffer's scrollback at its
	// configured maximum so the measured loop runs at steady state.
	scrollbackPrefillLines = 10_001

	// scrollWorkloadLines yields ~1 MiB of "y\r\n".
	scrollWorkloadLines = (1 << 20) / 3

	// densePasses repaints the whole grid this many times, matching the
	// vtebench dense-cell corpus.
	densePasses = 26
)

// altScreen mirrors the `printf "\e[?1049h"` setup vtebench uses for the
// benchmarks that repaint a full screen rather than scroll history.
func altScreen(_, _ int) []byte {
	return []byte("\x1b[?1049h")
}

func advanceBatched(p *vteparser.Parser, buf []byte) {
	for len(buf) > 0 {
		n := min(len(buf), gatherBatchSize)
		p.AdvanceBytes(buf[:n])
		buf = buf[n:]
	}
}

// warmToSteadyState replays payload until the primary buffer's history
// stops growing, so the timed loop measures steady-state cost. A single
// replay saturates the scroll-heavy corpora, but the wide-unicode corpus
// packs two columns per CJK cell and needs several passes to fill the
// 10k-row scrollback; timing it before then charges the one-time history
// growth (fresh row allocations) to every terminal under test.
func warmToSteadyState(ph *parserHandler, parser *vteparser.Parser, payload []byte) {
	const maxPasses = 32
	prev := -1
	for range maxPasses {
		advanceBatched(parser, payload)
		rows := ph.sync.primBuf.Cells.Rows()
		if rows == prev {
			return
		}
		prev = rows
	}
}

func buildLinefeedPayload(lines int) []byte {
	return []byte(strings.Repeat("y\r\n", lines))
}

// buildDenseCellsPayload writes every cell of the grid with a distinct
// foreground/background pair plus bold, italic and underline, so each cell
// carries a full CSI dispatch rather than landing in a printable run.
func buildDenseCellsPayload(width, height int) []byte {
	const alphabet = "abcdefghijklmnopqrstuvwxyz"
	buf := make([]byte, 0, densePasses*height*width*26)
	for pass := range densePasses {
		for y := range height {
			for x := range width {
				fg := (pass + x) % 256
				bg := (pass + y) % 256
				buf = fmt.Appendf(buf, "\x1b[38;5;%d;48;5;%d;1;3;4m%c",
					fg, bg, alphabet[(x+y)%len(alphabet)])
			}
			buf = append(buf, '\r', '\n')
		}
	}
	return buf
}

// buildSyncFramesPayload emits escape-heavy full-screen repaints. When
// wrapped is true each frame is delimited by BSU/ESU so the parser buffers
// and replays it through the synchronized-update path.
func buildSyncFramesPayload(width, height int, wrapped bool) []byte {
	const (
		frames   = 64
		segments = 8
	)
	buf := make([]byte, 0, frames*height*width*8)
	for frame := range frames {
		if wrapped {
			buf = append(buf, "\x1b[?2026h"...)
		}
		for y := range height {
			buf = fmt.Appendf(buf, "\x1b[%d;1H", y+1)
			for seg := range segments {
				buf = fmt.Appendf(buf, "\x1b[%d;38;5;%dm",
					(seg%2)+1, (frame+seg+y)%256)
				run := width / segments
				for i := range run {
					buf = append(buf, byte('a'+(i+seg+y)%26))
				}
			}
			buf = append(buf, "\x1b[0m"...)
		}
		if wrapped {
			buf = append(buf, "\x1b[?2026l"...)
		}
	}
	return buf
}

// buildWideUnicodePayload mirrors the vtebench unicode corpus, which is
// dominated by East Asian Width W codepoints with effectively no emoji
// sequences, so the cost measured is per-codepoint grid overhead rather
// than grapheme segmentation.
func buildWideUnicodePayload(width, _ int) []byte {
	const wide = "夜半钟声到客船月落乌啼霜满天江枫渔火对愁眠姑苏城外寒山寺" +
		"あの日見た花の名前を僕達はまだ知らない한국어단어테스트"
	runes := []rune(wide)
	perLine := width / 2

	var sb strings.Builder
	sb.Grow(1 << 20)
	next := 0
	for sb.Len() < 1<<20 {
		for range perLine {
			sb.WriteRune(runes[next%len(runes)])
			next++
		}
		sb.WriteString("\r\n")
	}
	return []byte(sb.String())
}
