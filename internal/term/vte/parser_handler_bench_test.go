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
	"sync"
	"testing"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"unstable.build/rune/internal/term/vte/vteparser"
	"unstable.build/rune/internal/workspace/workspacetest"
)

// BenchmarkParserHandlerStream simulates a shell streaming printable
// characters into the primary VTE buffer the same way the parser
// driver does at runtime: a tight loop of Input(rune) calls with
// CarriageReturn/Linefeed at end-of-line boundaries. This is what
// `cat large_file`, build logs, or any chatty command produces.
//
// Sub-benchmarks vary terminal width and line width so we can tell
// apart three regimes:
//   - narrow lines on a wide terminal (typical log output: lots of
//     blank tail columns per row)
//   - lines that fit the terminal exactly (no wrap, dense rows)
//   - lines wider than the terminal (line wrapping triggers
//     scrollUp -> PrimaryBuffer.InsertLines which materializes a
//     width-wide blank row)
//
// totalChars is held constant across sub-benchmarks so b.N iterations
// produce comparable B/op numbers.
func BenchmarkParserHandlerStream(b *testing.B) {
	const (
		// Total printable characters written per iteration. Sized so
		// that with the smallest line length (40) we overflow the
		// default 10_000-row scrollback at least once, exercising the
		// trim path in PrimaryBuffer.InsertLines.
		totalChars = 600_000
		height     = 24
	)

	cases := []struct {
		name    string
		width   int
		lineLen int
		// prefillLines streams this many full-width lines before the
		// timer starts, pinning the scrollback at its configured max.
		prefillLines int
	}{
		// Logs / shell output: lines much shorter than the terminal,
		// so each row carries lots of trailing capacity that is never
		// written. This is the regime that today's defColumnCap=64
		// (clamped up by the primary-buffer width on Resize) over-
		// allocates the most.
		{name: "logs_w80_l40", width: 80, lineLen: 40},
		{name: "logs_w200_l60", width: 200, lineLen: 60},

		// Output that exactly fills the terminal width: rows are
		// fully written before Linefeed, so columnCap savings come
		// only from the empty trailing row, not the body.
		{name: "fit_w80", width: 80, lineLen: 80},
		{name: "fit_w200", width: 200, lineLen: 200},

		// Wide payloads that wrap. Each wrap inserts a width-wide
		// blank row via PrimaryBuffer.InsertLines, which is the
		// dominant allocator in the production heap snapshot.
		{name: "wrap_w80_l160", width: 80, lineLen: 160},
		{name: "wrap_w200_l400", width: 200, lineLen: 400},

		// Scrollback pinned at max history: the regime a long
		// `cat large_file` spends nearly all its time in, where every
		// Linefeed scrolls a full-length row slice.
		{name: "steady_w80", width: 80, lineLen: 80, prefillLines: 10_001},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			payload := buildParserStreamPayload(totalChars, tc.lineLen)
			prefill := buildParserStreamPayload(tc.prefillLines*tc.lineLen, tc.lineLen)

			b.ReportAllocs()
			b.ResetTimer()

			for range b.N {
				b.StopTimer()
				ph := newBenchParserHandler(tc.width, height)
				writeStreamToParserHandler(ph, prefill)
				b.StartTimer()

				writeStreamToParserHandler(ph, payload)
			}
		})
	}
}

// buildParserStreamPayload returns a deterministic byte payload that,
// when written one rune at a time, totals `totalChars` printable
// characters interspersed with '\n' every `lineLen` characters.
// '\n' bytes are not counted toward totalChars (they translate to
// CarriageReturn+Linefeed, not Input).
func buildParserStreamPayload(totalChars, lineLen int) string {
	if lineLen <= 0 {
		lineLen = 1
	}
	buf := make([]byte, 0, totalChars+totalChars/lineLen+1)
	// Use a small repeating alphabet so each rune is single-byte
	// width=1, which matches the dominant case in real shell output.
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789 -_/."
	col := 0
	for written := range totalChars {
		buf = append(buf, alphabet[written%len(alphabet)])
		col++
		if col == lineLen {
			buf = append(buf, '\n')
			col = 0
		}
	}
	return string(buf)
}

func writeStreamToParserHandler(ph *parserHandler, payload string) {
	for _, c := range payload {
		if c == '\n' {
			ph.CarriageReturn()
			ph.Linefeed()
			continue
		}
		ph.Input(c)
	}
}

// BenchmarkParserStreamBytes measures the full parse stage the way the
// pty pipeline drives it: raw bytes (lines terminated with \r\n as the
// pty line discipline emits them) through vteparser.Parser. The bytes
// variant exercises the batched printable-run path; the byte variant is
// the per-byte dispatch baseline.
func BenchmarkParserStreamBytes(b *testing.B) {
	const (
		totalChars = 600_000
		height     = 24
		width      = 80
		lineLen    = 80
	)
	payload := []byte(strings.ReplaceAll(
		buildParserStreamPayload(totalChars, lineLen), "\n", "\r\n"))
	prefill := []byte(strings.ReplaceAll(
		buildParserStreamPayload(10_001*lineLen, lineLen), "\n", "\r\n"))

	feeds := []struct {
		name string
		feed func(*vteparser.Parser, []byte)
	}{
		{"per_byte", func(p *vteparser.Parser, buf []byte) {
			for _, ch := range buf {
				p.Advance(ch)
			}
		}},
		{"batched", (*vteparser.Parser).AdvanceBytes},
	}

	for _, tc := range feeds {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for range b.N {
				b.StopTimer()
				ph := newBenchParserHandler(width, height)
				parser := vteparser.NewParser(ph, new(vteparser.StdTimeout))
				parser.AdvanceBytes(prefill)
				b.StartTimer()

				tc.feed(parser, payload)
			}
		})
	}
}

// BenchmarkParserStreamUnicode measures the parse stage on non-ASCII
// text, the regime `kitten __benchmark__` reports as "Unicode chars".
// Every codepoint here misses the printable-ASCII run fast path, so the
// benchmark isolates the per-codepoint UTF-8 decode, grapheme width and
// continuation-merge costs.
func BenchmarkParserStreamUnicode(b *testing.B) {
	const (
		height  = 24
		width   = 80
		lineLen = 40
	)

	corpora := []struct {
		name string
		text string
	}{
		{"cjk", "夜半钟声到客船月落乌啼霜满天江枫渔火对愁眠姑苏城外寒山寺"},
		{"latin1", "größer Ärger naïve café résumé Fußgängerübergänge Straße"},
		{"mixed", "Καλημέρα κόσμε Привет мир こんにちは世界 안녕하세요 مرحبا"},
		{"emoji", "🙂🚀🌍🎉🔥💡📦🧪🧵🪝🧬🛰️🪐🧭🗺️"},
	}

	for _, tc := range corpora {
		b.Run(tc.name, func(b *testing.B) {
			payload := []byte(buildUnicodePayload(tc.text, lineLen, 64*1024))
			ph := newBenchParserHandler(width, height)
			parser := vteparser.NewParser(ph, new(vteparser.StdTimeout))
			// Warm the scrollback to its steady state so the measured
			// loop is not dominated by first-touch row growth.
			parser.AdvanceBytes(payload)

			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for range b.N {
				parser.AdvanceBytes(payload)
			}
		})
	}
}

// buildUnicodePayload repeats text until the result reaches at least
// minBytes, inserting \r\n every lineLen runes so the stream scrolls
// like real output rather than overwriting one row.
func buildUnicodePayload(text string, lineLen, minBytes int) string {
	runes := []rune(text)
	var sb strings.Builder
	col := 0
	for i := 0; sb.Len() < minBytes; i++ {
		sb.WriteRune(runes[i%len(runes)])
		col++
		if col == lineLen {
			sb.WriteString("\r\n")
			col = 0
		}
	}
	return sb.String()
}

func newBenchParserHandler(width, height int) *parserHandler {
	uri, err := workspaceapi.ParseURI("memory:///bench")
	if err != nil {
		panic(err)
	}
	mockPty := &workspacetest.File{}
	tm := &mockTabManager{}
	pty := workspaceapi.Pty{Master: mockPty, Slave: mockPty}
	cfg := DefaultConfig()
	ph := newParserHandler(
		new(sync.Mutex), pty, tm,
		clipboard.NewInMemory(), tm.bell, uri,
		cfg.NeedsAttentionAttributes, false, cfg.MaxLines, cfg.MinWidth, nil, nil, "")
	ph.sync.primBuf.SetDefaultChar(' ')
	ph.sync.altBuf.SetDefaultChar(' ')
	ph.Resize(width, height)
	return ph
}
