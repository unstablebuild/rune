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

package vtesnap

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"io"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestRecordRoundTrip(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	w := NewRecordWriter(&buf)
	require.NoError(t, w.Envelope())
	require.NoError(t, w.Record(TagTerminal, []byte("payload")))
	require.NoError(t, w.Record(TagReady, nil))

	r := NewRecordReader(bytes.NewReader(buf.Bytes()))
	require.NoError(t, r.Envelope())

	tag, payload, err := r.Next()
	require.NoError(t, err)
	assert.Equal(t, TagTerminal, tag)
	assert.Equal(t, "payload", string(payload))

	tag, payload, err = r.Next()
	require.NoError(t, err)
	assert.Equal(t, TagReady, tag)
	assert.Empty(t, payload)

	_, _, err = r.Next()
	assert.ErrorIs(t, err, io.EOF)
}

func TestRecordRejectsCorruption(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	w := NewRecordWriter(&buf)
	require.NoError(t, w.Envelope())
	require.NoError(t, w.Record(TagTerminal, []byte("payload")))
	blob := buf.Bytes()

	t.Run("truncation", func(t *testing.T) {
		t.Parallel()
		for n := range len(blob) {
			r := NewRecordReader(bytes.NewReader(blob[:n]))
			err := r.Envelope()
			if err == nil {
				_, _, err = r.Next()
			}
			assert.Error(t, err, "prefix of %d bytes decoded", n)
		}
	})

	t.Run("magic", func(t *testing.T) {
		t.Parallel()
		corrupt := bytes.Clone(blob)
		corrupt[0] ^= 1
		r := NewRecordReader(bytes.NewReader(corrupt))
		assert.ErrorIs(t, r.Envelope(), ErrInvalidMagic)
	})

	t.Run("version", func(t *testing.T) {
		t.Parallel()
		corrupt := bytes.Clone(blob)
		binary.LittleEndian.PutUint16(corrupt[len(Magic):], Version+1)
		r := NewRecordReader(bytes.NewReader(corrupt))
		assert.ErrorIs(t, r.Envelope(), ErrUnsupportedVersion)
	})

	t.Run("checksum", func(t *testing.T) {
		t.Parallel()
		corrupt := bytes.Clone(blob)
		corrupt[len(corrupt)-1] ^= 1
		r := NewRecordReader(bytes.NewReader(corrupt))
		require.NoError(t, r.Envelope())
		_, _, err := r.Next()
		assert.ErrorIs(t, err, ErrChecksum)
	})

	t.Run("tag", func(t *testing.T) {
		t.Parallel()
		corrupt := bytes.Clone(blob)
		binary.LittleEndian.PutUint16(corrupt[EnvelopeLen:], 999)
		r := NewRecordReader(bytes.NewReader(corrupt))
		require.NoError(t, r.Envelope())
		_, _, err := r.Next()
		assert.ErrorIs(t, err, ErrInvalidTag)
	})
}

func TestModesBitmap(t *testing.T) {
	t.Parallel()

	var modes Modes
	for m := range modeCount {
		assert.False(t, modes.Get(m))
		modes = modes.Set(m, true)
		assert.True(t, modes.Get(m))
	}
	for m := range modeCount {
		modes = modes.Set(m, false)
		assert.False(t, modes.Get(m))
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	t.Parallel()

	// Seeds are fixed so a failure reproduces, but each one shapes a
	// different terminal: sizes, scrollback depth, combining marks and
	// continuation state all vary.
	for _, seed := range []uint64{1, 2, 3, 7, 11, 13, 42, 1337} {
		t.Run(pageName(seed), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewPCG(seed, seed))

			terminal := randomTerminal(rng)
			primary := randomScreen(rng, ScreenPrimary, terminal)
			alternate := randomScreen(rng, ScreenAlternate, terminal)
			primaryRows := randomRows(rng, terminal.Height, terminal.Width)
			alternateRows := randomRows(rng, terminal.Height, terminal.Width)
			continuation := randomContinuation(rng)

			var buf bytes.Buffer
			e, err := NewEncoder(&buf)
			require.NoError(t, err)
			e.SetPageRows(1 + rng.IntN(32))
			require.NoError(t, e.Terminal(terminal))
			require.NoError(t, e.Screen(primary, primaryRows))
			require.NoError(t, e.Screen(alternate, alternateRows))
			require.NoError(t, e.Continuation(continuation))
			require.NoError(t, e.Ready())

			got := decodeSnapshot(t, buf.Bytes())

			assert.Equal(t, terminal, got.Terminal)
			assertScreenEqual(t, primary, primaryRows, got.Primary)
			assertScreenEqual(t, alternate, alternateRows, got.Alternate)
			assert.Equal(t, continuation, got.Continuation)
		})
	}
}

func TestEncoderRejectsOutOfOrderRecords(t *testing.T) {
	t.Parallel()

	t.Run("ready before continuation", func(t *testing.T) {
		t.Parallel()
		e, err := NewEncoder(io.Discard)
		require.NoError(t, err)
		require.NoError(t, e.Terminal(Terminal{}))
		assert.Error(t, e.Ready())
	})

	t.Run("screen before terminal", func(t *testing.T) {
		t.Parallel()
		e, err := NewEncoder(io.Discard)
		require.NoError(t, err)
		assert.Error(t, e.Screen(Screen{}, nil))
	})

	t.Run("screen after ready", func(t *testing.T) {
		t.Parallel()
		e, err := NewEncoder(io.Discard)
		require.NoError(t, err)
		require.NoError(t, e.Terminal(Terminal{}))
		require.NoError(t, e.Continuation(nil))
		require.NoError(t, e.Ready())
		assert.ErrorContains(t, e.Screen(Screen{}, nil), "out of order")
		assert.ErrorContains(t, e.Continuation(nil), "out of order")
	})

	t.Run("duplicate terminal", func(t *testing.T) {
		t.Parallel()
		e, err := NewEncoder(io.Discard)
		require.NoError(t, err)
		require.NoError(t, e.Terminal(Terminal{}))
		assert.Error(t, e.Terminal(Terminal{}))
	})
}

func TestDecoderRejectsMalformedStreams(t *testing.T) {
	t.Parallel()

	t.Run("ready before terminal", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		w := NewRecordWriter(&buf)
		require.NoError(t, w.Envelope())
		require.NoError(t, w.Record(TagReady, nil))

		d, err := NewDecoder(bytes.NewReader(buf.Bytes()))
		require.NoError(t, err)
		_, err = d.Ready()
		assert.ErrorIs(t, err, ErrMalformed)
	})

	t.Run("page outside screen", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		w := NewRecordWriter(&buf)
		require.NoError(t, w.Envelope())
		require.NoError(t, w.Record(TagTerminal, (&Terminal{}).encode()))
		require.NoError(t, w.Record(TagPage, (&Page{}).encode()))

		d, err := NewDecoder(bytes.NewReader(buf.Bytes()))
		require.NoError(t, err)
		_, err = d.Ready()
		assert.ErrorIs(t, err, ErrMalformed)
	})

	t.Run("stream ends before ready", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		w := NewRecordWriter(&buf)
		require.NoError(t, w.Envelope())
		require.NoError(t, w.Record(TagTerminal, (&Terminal{}).encode()))

		d, err := NewDecoder(bytes.NewReader(buf.Bytes()))
		require.NoError(t, err)
		_, err = d.Ready()
		assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	})
}

// TestPageRunLengthEncoding pins that blank rows, which dominate a
// terminal, cost far less than one record per cell.
func TestPageRunLengthEncoding(t *testing.T) {
	t.Parallel()

	blank := make([][]term.Cell, 50)
	for i := range blank {
		blank[i] = make([]term.Cell, 200)
	}
	page := Page{Kind: ScreenPrimary, Rows: blank}
	encoded := page.encode()
	assert.Less(t, len(encoded), 2000,
		"10000 blank cells should collapse into one run per row")

	got, err := decodePage(encoded)
	require.NoError(t, err)
	assertRowsEqual(t, blank, got.Rows)
}

func pageName(seed uint64) string {
	return "seed" + string(rune('0'+seed%10)) + "_" + string(rune('a'+seed%26))
}

func randomTerminal(rng *rand.Rand) Terminal {
	width := 1 + rng.IntN(200)
	height := 1 + rng.IntN(60)
	tabstops := make([]bool, width)
	for i := range tabstops {
		tabstops[i] = i%8 == 0
	}
	var modes Modes
	for m := range modeCount {
		modes = modes.Set(m, rng.IntN(2) == 0)
	}
	var stack []string
	for range rng.IntN(4) {
		stack = append(stack, randomString(rng))
	}
	return Terminal{
		Width:          width,
		Height:         height,
		Title:          randomString(rng),
		TitleStack:     stack,
		Tabstops:       tabstops,
		Modes:          modes,
		CursorStyle:    term.CursorStyle(rng.IntN(7)),
		CursorHidden:   rng.IntN(2) == 0,
		PendingWrap:    rng.IntN(2) == 0,
		CurrentCharset: uint8(rng.IntN(4)),
		UseAlternate:   rng.IntN(2) == 0,
		UsedAlternate:  rng.IntN(2) == 0,
		MaxScrollback:  rng.IntN(100000),
		MinWidth:       rng.IntN(80),
		KittyKeyboard: [2][]uint8{
			randomBytes(rng, rng.IntN(9)), randomBytes(rng, rng.IntN(9)),
		},
		ModifyOtherKeys: uint8(rng.IntN(3)),
		CellPixelWidth:  rng.IntN(40),
		CellPixelHeight: rng.IntN(80),
	}
}

func randomBytes(rng *rand.Rand, n int) []uint8 {
	if n == 0 {
		return nil
	}
	out := make([]uint8, n)
	for i := range out {
		out[i] = uint8(rng.Uint32())
	}
	return out
}

func randomScreen(rng *rand.Rand, kind ScreenKind, t Terminal) Screen {
	return Screen{
		Kind:                   kind,
		Width:                  t.Width,
		Height:                 t.Height,
		TopScrollableRegion:    rng.IntN(t.Height),
		BottomScrollableRegion: t.Height,
		Cursor:                 randomCursor(rng, t),
		SavedCursor:            randomCursor(rng, t),
	}
}

func randomCursor(rng *rand.Rand, t Terminal) Cursor {
	var charsets []Charset
	for i := range rng.IntN(4) {
		charsets = append(charsets,
			Charset{Index: uint8(i), Standard: uint8(rng.IntN(3))})
	}
	return Cursor{
		Position: term.Coordinates{X: rng.IntN(t.Width), Y: rng.IntN(t.Height)},
		Attributes: term.Attributes{
			Fg:        term.Color(rng.Uint32()),
			Bg:        term.Color(rng.Uint32()),
			Attrs:     term.AttrMask(rng.Uint32()),
			Underline: term.Color(rng.Uint32()),
		},
		Hidden:   rng.IntN(2) == 0,
		Charsets: charsets,
	}
}

func randomRows(rng *rand.Rand, rows, width int) [][]term.Cell {
	if rows == 0 {
		return nil
	}
	out := make([][]term.Cell, rows)
	for y := range out {
		out[y] = make([]term.Cell, width)
		for x := range out[y] {
			// Bias towards blanks so runs actually form, and sprinkle
			// in the awkward cases: wide glyphs and combining marks.
			switch rng.IntN(10) {
			case 0, 1, 2, 3, 4, 5:
			case 6, 7, 8:
				out[y][x] = term.Cell{
					Ch:    rune('a' + rng.IntN(26)),
					Fg:    term.Color(rng.Uint32()),
					Bg:    term.Color(rng.Uint32()),
					Attrs: term.AttrMask(rng.Uint32()),
					Width: 1,
					Bytes: 1,
				}
				if rng.IntN(3) == 0 {
					out[y][x].SetUnderlineColor(term.PaletteColor(rng.IntN(256)))
				}
			default:
				c := term.Cell{Ch: '한', Width: 2, Bytes: 3}
				c.SetCombining([]rune{0x0301, 0x0302})
				out[y][x] = c
			}
		}
	}
	return out
}

func randomContinuation(rng *rand.Rand) []byte {
	switch rng.IntN(4) {
	case 0:
		return nil
	case 1:
		return []byte("\x1b[31")
	case 2:
		return []byte("\xF0\x9F\x98")
	default:
		return []byte("\x1b]0;partial title")
	}
}

func randomString(rng *rand.Rand) string {
	n := rng.IntN(32)
	out := make([]rune, n)
	for i := range out {
		out[i] = rune('a' + rng.IntN(26))
	}
	return string(out)
}

func assertScreenEqual(t *testing.T, want Screen, wantRows [][]term.Cell, got ScreenState) {
	t.Helper()
	want.Rows = len(wantRows)
	assert.Equal(t, want, got.Screen)
	assertRowsEqual(t, wantRows, got.Rows)
}

func assertRowsEqual(t *testing.T, want, got [][]term.Cell) {
	t.Helper()
	require.Len(t, got, len(want))
	for y := range want {
		require.Len(t, got[y], len(want[y]), "row %d", y)
		for x := range want[y] {
			w, g := want[y][x], got[y][x]
			assert.Equal(t, w.Ch, g.Ch, "row %d col %d", y, x)
			assert.Equal(t, w.Fg, g.Fg, "row %d col %d", y, x)
			assert.Equal(t, w.Bg, g.Bg, "row %d col %d", y, x)
			assert.Equal(t, w.Attrs, g.Attrs, "row %d col %d", y, x)
			assert.Equal(t, w.Width, g.Width, "row %d col %d", y, x)
			assert.Equal(t, w.Bytes, g.Bytes, "row %d col %d", y, x)
			assert.Equal(t, w.CombiningRunes(), g.CombiningRunes(),
				"row %d col %d", y, x)
			assert.Equal(t, w.UnderlineColor(), g.UnderlineColor(),
				"row %d col %d", y, x)
		}
	}
}

// TestPageRoundTripEdgeCases pins what run-length encoding preserves.
// Cells are merged only when they are genuinely identical, so a run
// must never smear one cell's colour or width over its neighbour.
func TestPageRoundTripEdgeCases(t *testing.T) {
	t.Parallel()

	cell := func(ch rune) term.Cell {
		return term.Cell{Ch: ch, Width: 1, Bytes: uint8(len(string(ch)))}
	}
	withCombining := func(c term.Cell, marks ...rune) term.Cell {
		c.SetCombining(marks)
		return c
	}

	tests := []struct {
		desc string
		kind ScreenKind
		rows [][]term.Cell
		want [][]term.Cell // nil means "identical to rows"
	}{
		{desc: "no rows at all", kind: ScreenPrimary},
		{
			desc: "a row with no cells decodes as nil",
			kind: ScreenPrimary,
			rows: [][]term.Cell{{}},
			want: [][]term.Cell{nil},
		},
		{
			desc: "empty rows between populated ones keep their place",
			kind: ScreenPrimary,
			rows: [][]term.Cell{{cell('a')}, {}, {cell('b')}},
			want: [][]term.Cell{{cell('a')}, nil, {cell('b')}},
		},
		{
			desc: "cells differing only in foreground do not merge",
			kind: ScreenPrimary,
			rows: [][]term.Cell{{
				term.Cell{Ch: 'x', Width: 1, Fg: term.ColorRed},
				term.Cell{Ch: 'x', Width: 1, Fg: term.ColorBlue},
			}},
		},
		{
			desc: "cells differing only in attributes do not merge",
			kind: ScreenPrimary,
			rows: [][]term.Cell{{
				term.Cell{Ch: 'x', Width: 1, Attrs: term.AttrBold},
				term.Cell{Ch: 'x', Width: 1, Attrs: term.AttrReverse},
			}},
		},
		{
			desc: "cells differing only in width do not merge",
			kind: ScreenPrimary,
			rows: [][]term.Cell{{
				term.Cell{Ch: 'x', Width: 1},
				term.Cell{Ch: 'x', Width: 2},
			}},
		},
		{
			desc: "wide characters keep their width and byte count",
			kind: ScreenPrimary,
			rows: [][]term.Cell{{
				term.Cell{Ch: '世', Width: 2, Bytes: 3},
				term.Cell{Ch: 0, Width: 0},
				term.Cell{Ch: '界', Width: 2, Bytes: 3},
			}},
		},
		{
			desc: "zero cells round trip as the blanks they are",
			kind: ScreenPrimary,
			rows: [][]term.Cell{make([]term.Cell, 40)},
		},
		{
			desc: "combining marks survive and never merge",
			kind: ScreenPrimary,
			rows: [][]term.Cell{{
				withCombining(cell('e'), 0x0301),
				withCombining(cell('e'), 0x0301),
			}},
		},
		{
			desc: "a run longer than one length prefix splits into several",
			kind: ScreenPrimary,
			rows: [][]term.Cell{repeat(cell('.'), maxRun+7)},
		},
		{
			desc: "the alternate screen kind survives",
			kind: ScreenAlternate,
			rows: [][]term.Cell{{cell('a')}},
		},
		{
			desc: "astral plane runes survive",
			kind: ScreenPrimary,
			rows: [][]term.Cell{{term.Cell{Ch: 0x1F600, Width: 2, Bytes: 4}}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()

			kind, rows, err := DecodePage(EncodePage(tc.kind, tc.rows))
			require.NoError(t, err)
			assert.Equal(t, tc.kind, kind)

			want := tc.want
			if want == nil {
				want = tc.rows
			}
			require.Len(t, rows, len(want))
			for i := range want {
				assert.Equal(t, want[i], rows[i], "row %d", i)
			}
		})
	}
}

// TestEncodeCellTruncatesExcessCombiningMarks pins a deliberate loss:
// the count is one byte, so a cell carrying more marks than that keeps
// only the first 255 rather than corrupting the run that follows it.
func TestEncodeCellTruncatesExcessCombiningMarks(t *testing.T) {
	t.Parallel()

	marks := make([]rune, 300)
	for i := range marks {
		marks[i] = rune(0x0300 + i%16)
	}
	cell := term.Cell{Ch: 'a', Width: 1}
	cell.SetCombining(marks)

	_, rows, err := DecodePage(EncodePage(ScreenPrimary, [][]term.Cell{{cell}}))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Len(t, rows[0], 1)
	assert.Equal(t, marks[:0xFF], rows[0][0].CombiningRunes())
}

// TestDecodePageRejectsMalformedPayload pins that a page validates its
// own runs. A run that does not fit the row it declares would otherwise
// decide how much memory the decoder reserves.
func TestDecodePageRejectsMalformedPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		desc    string
		payload []byte
	}{
		{desc: "empty payload"},
		{
			desc:    "row count larger than the payload can back",
			payload: build(func(e *enc) { e.u8(uint8(ScreenPrimary)); e.u32(math.MaxUint32) }),
		},
		{
			desc: "a zero-length run would never advance",
			payload: build(func(e *enc) {
				e.u8(uint8(ScreenPrimary))
				e.u32(1)
				e.u32(4)
				e.u16(0)
				encodeCell(e, term.Cell{Ch: 'a'})
			}),
		},
		{
			desc: "a run wider than the row it belongs to",
			payload: build(func(e *enc) {
				e.u8(uint8(ScreenPrimary))
				e.u32(1)
				e.u32(2)
				e.u16(3)
				encodeCell(e, term.Cell{Ch: 'a'})
			}),
		},
		{
			desc: "a row wider than the cell limit",
			payload: build(func(e *enc) {
				e.u8(uint8(ScreenPrimary))
				e.u32(1)
				e.u32(maxRowCells + 1)
			}),
		},
		{
			// Each row is within the row limit and costs a few hundred
			// bytes; together they would expand into gigabytes.
			desc: "rows that together exceed the page cell limit",
			payload: build(func(e *enc) {
				rows := maxPageCells/maxRowCells + 1
				e.u8(uint8(ScreenPrimary))
				e.u32(uint32(rows))
				for range rows {
					e.u32(maxRowCells)
					for left := maxRowCells; left > 0; left -= maxRun {
						e.u16(uint16(min(left, maxRun)))
						encodeCell(e, term.Cell{Ch: ' ', Width: 1})
					}
				}
			}),
		},
		{
			desc: "a cell truncated mid-field",
			payload: build(func(e *enc) {
				e.u8(uint8(ScreenPrimary))
				e.u32(1)
				e.u32(1)
				e.u16(1)
				e.rune('a')
			}),
		},
		{
			desc: "combining marks promised but absent",
			payload: build(func(e *enc) {
				e.u8(uint8(ScreenPrimary))
				e.u32(1)
				e.u32(1)
				e.u16(1)
				e.rune('a')
				e.u32(0)
				e.u32(0)
				e.u16(0)
				e.u8(1)
				e.u8(1)
				e.u8(4)
			}),
		},
		{
			desc: "trailing bytes after the last row",
			payload: append(EncodePage(ScreenPrimary, [][]term.Cell{
				{term.Cell{Ch: 'a', Width: 1}},
			}), 0x00),
		},
		{
			desc: "an unknown screen kind",
			payload: build(func(e *enc) {
				e.u8(9)
				e.u32(0)
			}),
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()

			_, _, err := DecodePage(tc.payload)
			assert.ErrorIs(t, err, ErrMalformed)
		})
	}
}

// TestRecordFramingRejectsCorruption pins the framing guarantees a
// decoder relies on before it trusts a payload at all.
func TestRecordFramingRejectsCorruption(t *testing.T) {
	t.Parallel()

	valid := func() []byte {
		var buf bytes.Buffer
		w := NewRecordWriter(&buf)
		require.NoError(t, w.Envelope())
		require.NoError(t, w.Record(TagTerminal, []byte("payload")))
		return buf.Bytes()
	}

	tests := []struct {
		desc    string
		corrupt func(blob []byte) []byte
		wantErr error
	}{
		{
			desc:    "foreign magic",
			corrupt: func(b []byte) []byte { copy(b, "NOTRUNES"); return b },
			wantErr: ErrInvalidMagic,
		},
		{
			desc: "future version",
			corrupt: func(b []byte) []byte {
				binary.LittleEndian.PutUint16(b[len(Magic):], Version+1)
				return b
			},
			wantErr: ErrUnsupportedVersion,
		},
		{
			desc: "tag below the defined range",
			corrupt: func(b []byte) []byte {
				binary.LittleEndian.PutUint16(b[EnvelopeLen:], 0)
				return b
			},
			wantErr: ErrInvalidTag,
		},
		{
			desc: "tag above the defined range",
			corrupt: func(b []byte) []byte {
				binary.LittleEndian.PutUint16(b[EnvelopeLen:], uint16(TagContinuation)+1)
				return b
			},
			wantErr: ErrInvalidTag,
		},
		{
			desc: "payload longer than a decoder will allocate",
			corrupt: func(b []byte) []byte {
				binary.LittleEndian.PutUint32(b[EnvelopeLen+2:], MaxPayloadLen+1)
				return b
			},
			wantErr: ErrPayloadTooLarge,
		},
		{
			desc: "a flipped payload byte",
			corrupt: func(b []byte) []byte {
				b[len(b)-1] ^= 0xFF
				return b
			},
			wantErr: ErrChecksum,
		},
		{
			desc: "a length that disagrees with the checksum",
			corrupt: func(b []byte) []byte {
				binary.LittleEndian.PutUint32(b[EnvelopeLen+2:], 3)
				return b
			},
			wantErr: ErrChecksum,
		},
		{
			desc:    "a truncated payload",
			corrupt: func(b []byte) []byte { return b[:len(b)-1] },
			wantErr: io.ErrUnexpectedEOF,
		},
		{
			desc:    "a truncated header",
			corrupt: func(b []byte) []byte { return b[:EnvelopeLen+HeaderLen-1] },
			wantErr: io.ErrUnexpectedEOF,
		},
		{
			desc:    "a truncated envelope",
			corrupt: func(b []byte) []byte { return b[:EnvelopeLen-1] },
			wantErr: io.ErrUnexpectedEOF,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()

			r := NewRecordReader(bytes.NewReader(tc.corrupt(valid())))
			err := r.Envelope()
			if err == nil {
				_, _, err = r.Next()
			}
			assert.ErrorIs(t, err, tc.wantErr)
		})
	}
}

// TestRecordWriterRejectsOversizedPayload pins that the writer refuses
// what no reader would accept, rather than emitting a record that only
// fails on the far side.
func TestRecordWriterRejectsOversizedPayload(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	w := NewRecordWriter(&buf)
	require.NoError(t, w.Envelope())
	err := w.Record(TagPage, make([]byte, MaxPayloadLen+1))
	assert.ErrorIs(t, err, ErrPayloadTooLarge)
	assert.Equal(t, EnvelopeLen, buf.Len(),
		"a rejected record must not reach the writer")
}

// TestRecordRoundTripBoundaries covers the record sizes that sit on a
// boundary: a record with no payload at all, and one large enough to
// exercise the reader's buffer reuse.
func TestRecordRoundTripBoundaries(t *testing.T) {
	t.Parallel()

	payloads := [][]byte{
		nil,
		{0x00},
		[]byte(strings.Repeat("x", 64<<10)),
		{0xFF},
	}

	var buf bytes.Buffer
	w := NewRecordWriter(&buf)
	require.NoError(t, w.Envelope())
	for _, p := range payloads {
		require.NoError(t, w.Record(TagPage, p))
	}

	r := NewRecordReader(bytes.NewReader(buf.Bytes()))
	require.NoError(t, r.Envelope())
	for i, want := range payloads {
		tag, got, err := r.Next()
		require.NoError(t, err, "record %d", i)
		assert.Equal(t, TagPage, tag)
		assert.Equal(t, want, bytes.Clone(trimNil(got)), "record %d", i)
	}
	_, _, err := r.Next()
	assert.ErrorIs(t, err, io.EOF)
}

// TestChecksumCoversTheHeader pins that the CRC is taken over the tag
// and length as well as the payload, so a rewritten header cannot pass
// as a valid record.
func TestChecksumCoversTheHeader(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	w := NewRecordWriter(&buf)
	require.NoError(t, w.Envelope())
	require.NoError(t, w.Record(TagPage, []byte("body")))

	blob := buf.Bytes()
	payloadCRC := crc32.Update(0, crcTable, []byte("body"))
	// A reader that only checksummed the payload would accept this.
	binary.LittleEndian.PutUint16(blob[EnvelopeLen:], uint16(TagScreen))
	binary.LittleEndian.PutUint32(blob[EnvelopeLen+6:], payloadCRC)

	r := NewRecordReader(bytes.NewReader(blob))
	require.NoError(t, r.Envelope())
	_, _, err := r.Next()
	assert.ErrorIs(t, err, ErrChecksum)
}

func repeat(c term.Cell, n int) []term.Cell {
	row := make([]term.Cell, n)
	for i := range row {
		row[i] = c
	}
	return row
}

// trimNil normalizes the empty payload a reader returns as a zero
// length slice so it compares equal to the nil that was written.
func trimNil(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}

// build assembles a payload with the encoder the records themselves
// use, so a case describes a layout rather than a byte blob.
func build(fn func(*enc)) []byte {
	var e enc
	fn(&e)
	return e.b
}

// TestDecoderRejectsIllFormedStreams drives the decoder with streams a
// correct encoder cannot produce. Every one of them must be refused:
// the alternative is a client that renders a terminal quietly missing
// rows it was promised.
func TestDecoderRejectsIllFormedStreams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		records []record
	}{
		{
			name:    "no terminal at all",
			records: []record{{TagReady, nil}},
		},
		{
			name: "screen before terminal is only caught at ready",
			records: []record{
				{TagScreen, screenPayload(ScreenPrimary, 1)},
				{TagPage, pagePayload(ScreenPrimary, 1)},
				{TagReady, nil},
			},
		},
		{
			name: "duplicate terminal",
			records: []record{
				{TagTerminal, terminalPayload()},
				{TagTerminal, terminalPayload()},
				{TagReady, nil},
			},
		},
		{
			name: "duplicate screen of the same kind",
			records: []record{
				{TagTerminal, terminalPayload()},
				{TagScreen, screenPayload(ScreenPrimary, 1)},
				{TagPage, pagePayload(ScreenPrimary, 1)},
				{TagScreen, screenPayload(ScreenPrimary, 1)},
				{TagPage, pagePayload(ScreenPrimary, 1)},
				{TagReady, nil},
			},
		},
		{
			name: "page of the other kind inside a screen",
			records: []record{
				{TagTerminal, terminalPayload()},
				{TagScreen, screenPayload(ScreenPrimary, 1)},
				{TagPage, pagePayload(ScreenAlternate, 1)},
				{TagReady, nil},
			},
		},
		{
			name: "screen promises more rows than its pages carry",
			records: []record{
				{TagTerminal, terminalPayload()},
				{TagScreen, screenPayload(ScreenPrimary, 4)},
				{TagPage, pagePayload(ScreenPrimary, 2)},
				{TagReady, nil},
			},
		},
		{
			name: "screen pages overshoot the promised rows",
			records: []record{
				{TagTerminal, terminalPayload()},
				{TagScreen, screenPayload(ScreenPrimary, 2)},
				{TagPage, pagePayload(ScreenPrimary, 2)},
				{TagPage, pagePayload(ScreenPrimary, 1)},
				{TagReady, nil},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := readSnapshot(t, stream(t, tc.records...))
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrMalformed)
		})
	}
}

// TestDecoderAcceptsDegenerateStreams covers the shapes a real
// encoder does produce at the boundaries: no screens, zero-row
// screens, an empty continuation.
func TestDecoderAcceptsDegenerateStreams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		records []record
	}{
		{
			name: "terminal and ready only",
			records: []record{
				{TagTerminal, terminalPayload()},
				{TagReady, nil},
			},
		},
		{
			name: "screen with no rows carries no pages",
			records: []record{
				{TagTerminal, terminalPayload()},
				{TagScreen, screenPayload(ScreenPrimary, 0)},
				{TagReady, nil},
			},
		},
		{
			name: "empty continuation",
			records: []record{
				{TagTerminal, terminalPayload()},
				{TagContinuation, nil},
				{TagReady, nil},
			},
		},
		{
			name: "both screens with rows",
			records: []record{
				{TagTerminal, terminalPayload()},
				{TagScreen, screenPayload(ScreenPrimary, 2)},
				{TagPage, pagePayload(ScreenPrimary, 2)},
				{TagScreen, screenPayload(ScreenAlternate, 1)},
				{TagPage, pagePayload(ScreenAlternate, 1)},
				{TagReady, nil},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := readSnapshot(t, stream(t, tc.records...))
			assert.NoError(t, err)
		})
	}
}

// TestDecoderRejectsTruncationAtEveryOffset pins that a connection
// dying mid-snapshot is never mistaken for a complete one, whatever
// byte it dies on.
func TestDecoderRejectsTruncationAtEveryOffset(t *testing.T) {
	t.Parallel()

	full := stream(t,
		record{TagTerminal, terminalPayload()},
		record{TagScreen, screenPayload(ScreenPrimary, 2)},
		record{TagPage, pagePayload(ScreenPrimary, 2)},
		record{TagContinuation, []byte("\x1b[")},
		record{TagReady, nil},
	)

	for cut := range len(full) {
		_, err := readSnapshot(t, full[:cut])
		assert.Error(t, err, "truncation at byte %d decoded cleanly", cut)
	}

	snap := decodeSnapshot(t, full)
	assert.Equal(t, []byte("\x1b["), snap.Continuation)
}

// TestDecoderRejectsReuse pins the calling protocol: Ready is once.
func TestDecoderRejectsReuse(t *testing.T) {
	t.Parallel()

	blob := stream(t,
		record{TagTerminal, terminalPayload()},
		record{TagReady, nil},
	)

	d, err := NewDecoder(bytes.NewReader(blob))
	require.NoError(t, err)
	_, err = d.Ready()
	require.NoError(t, err)
	_, err = d.Ready()
	assert.Error(t, err)
}

// TestEncoderRejectsEveryOutOfOrderCall walks the encoder's state
// machine and pins which call is legal in which stage, so a partial
// blob can never be mistaken for a whole one.
func TestEncoderRejectsEveryOutOfOrderCall(t *testing.T) {
	t.Parallel()

	calls := map[string]func(*Encoder) error{
		"terminal":     func(e *Encoder) error { return e.Terminal(Terminal{}) },
		"screen":       func(e *Encoder) error { return e.Screen(Screen{}, nil) },
		"continuation": func(e *Encoder) error { return e.Continuation(nil) },
		"ready":        func(e *Encoder) error { return e.Ready() },
	}

	tests := []struct {
		name  string
		setup []string
		legal []string
	}{
		{
			name:  "fresh encoder",
			legal: []string{"terminal"},
		},
		{
			name:  "after terminal",
			setup: []string{"terminal"},
			legal: []string{"screen", "continuation"},
		},
		{
			name:  "after screens",
			setup: []string{"terminal", "screen"},
			legal: []string{"screen", "continuation"},
		},
		{
			name:  "after continuation",
			setup: []string{"terminal", "continuation"},
			legal: []string{"ready"},
		},
		{
			name:  "after ready",
			setup: []string{"terminal", "continuation", "ready"},
			legal: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			for name, call := range calls {
				e, err := NewEncoder(io.Discard)
				require.NoError(t, err)
				for _, step := range tc.setup {
					require.NoError(t, calls[step](e), "setup step %q", step)
				}

				err = call(e)
				if slices.Contains(tc.legal, name) {
					assert.NoError(t, err, "%q must be legal here", name)
				} else {
					assert.Error(t, err, "%q must be rejected here", name)
				}
			}
		})
	}
}

// TestEncoderPaginationBoundaries pins that page splitting keeps every
// row exactly once whatever the row count is relative to the page size.
func TestEncoderPaginationBoundaries(t *testing.T) {
	t.Parallel()

	for _, pageRows := range []int{1, 2, 3, 64} {
		for _, rows := range []int{0, 1, 2, 3, 4, 5, 7, 8} {
			t.Run(pageName(uint64(pageRows*100+rows)), func(t *testing.T) {
				t.Parallel()

				live := labelledRows("l", rows)

				var buf bytes.Buffer
				e, err := NewEncoder(&buf)
				require.NoError(t, err)
				e.SetPageRows(pageRows)
				require.NoError(t, e.Terminal(Terminal{}))
				require.NoError(t, e.Screen(Screen{Kind: ScreenPrimary}, live))
				require.NoError(t, e.Continuation(nil))
				require.NoError(t, e.Ready())

				assertRowsEqual(t, live, decodeSnapshot(t, buf.Bytes()).Primary.Rows)
			})
		}
	}
}

// TestSetPageRowsIgnoresNonPositive pins that a bad page size cannot
// turn into an infinite pagination loop.
func TestSetPageRowsIgnoresNonPositive(t *testing.T) {
	t.Parallel()

	for _, rows := range []int{0, -1, -1 << 30} {
		var buf bytes.Buffer
		e, err := NewEncoder(&buf)
		require.NoError(t, err)
		e.SetPageRows(rows)
		require.NoError(t, e.Terminal(Terminal{}))
		require.NoError(t, e.Screen(Screen{Kind: ScreenPrimary}, labelledRows("l", 3)))
		require.NoError(t, e.Continuation(nil))
		require.NoError(t, e.Ready())

		assert.Len(t, decodeSnapshot(t, buf.Bytes()).Primary.Rows, 3)
	}
}

type record struct {
	tag     Tag
	payload []byte
}

// stream frames records without the ordering the encoder enforces, so
// a test can hand the decoder a stream no encoder would write.
func stream(t *testing.T, records ...record) []byte {
	t.Helper()

	var buf bytes.Buffer
	w := NewRecordWriter(&buf)
	require.NoError(t, w.Envelope())
	for _, rec := range records {
		require.NoError(t, w.Record(rec.tag, rec.payload))
	}
	return buf.Bytes()
}

// readSnapshot reports an envelope failure and a record failure alike,
// since a caller rejecting a stream does not care which one it was.
func readSnapshot(t *testing.T, b []byte) (*Snapshot, error) {
	t.Helper()

	d, err := NewDecoder(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	return d.Ready()
}

func decodeSnapshot(t *testing.T, b []byte) *Snapshot {
	t.Helper()

	snap, err := readSnapshot(t, b)
	require.NoError(t, err)
	return snap
}

func terminalPayload() []byte {
	return (&Terminal{Width: 4, Height: 1}).encode()
}

func screenPayload(kind ScreenKind, rows int) []byte {
	return (&Screen{Kind: kind, Width: 4, Height: rows, Rows: rows}).encode()
}

func pagePayload(kind ScreenKind, rows int) []byte {
	return (&Page{Kind: kind, Rows: labelledRows("x", rows)}).encode()
}

// labelledRows builds rows whose first cell names the row, so a test
// can assert the order pages were reassembled in.
func labelledRows(prefix string, n int) [][]term.Cell {
	if n == 0 {
		return nil
	}
	rows := make([][]term.Cell, n)
	for i := range rows {
		rows[i] = []term.Cell{
			{Ch: rune(prefix[0]), Width: 1, Bytes: 1},
			{Ch: rune('0' + i), Width: 1, Bytes: 1},
		}
	}
	return rows
}
