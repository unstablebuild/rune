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

package shader

import (
	"sort"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// Decrypt types the text out as ciphertext, character by character in
// reading order, each one blocking in through TypingSymbols, then
// decrypts it: every character cycles quickly through CipherSymbols,
// slows down for a while of its own, and is discovered in
// DiscoverColor before settling into its own color. The typing
// overwrites [DecryptParams.From] as it goes.
//
// The last frame of the animation matches the input exactly.
//
// Zero params take their defaults; params out of their documented range
// panic with an error wrapping [ErrInvalidParams].
//
// Inspired by terminaltexteffects' "decrypt" effect.
func Decrypt(params DecryptParams, defaultAttr term.Attributes) ImageShader {
	d := DefaultDecryptParams()
	params.CiphertextColors = orDefaults(params.CiphertextColors, d.CiphertextColors)
	params.CipherSymbols = checkSymbols("DecryptParams.CipherSymbols", params.CipherSymbols, d.CipherSymbols)
	params.TypingSymbols = checkSymbols("DecryptParams.TypingSymbols", params.TypingSymbols, d.TypingSymbols)
	params.DiscoverColor = orDefault(params.DiscoverColor, d.DiscoverColor)
	params.TypingDuration = checkDuration("DecryptParams.TypingDuration", orDefault(params.TypingDuration, d.TypingDuration))
	params.FastDuration = checkDuration("DecryptParams.FastDuration", orDefault(params.FastDuration, d.FastDuration))
	params.DiscoverDuration = checkDuration("DecryptParams.DiscoverDuration",
		orDefault(params.DiscoverDuration, d.DiscoverDuration))
	checkPhases("DecryptParams.TypingDuration+FastDuration+DiscoverDuration",
		params.TypingDuration+params.FastDuration+params.DiscoverDuration)
	params.SymbolPeriod = max(checkFloat("DecryptParams.SymbolPeriod",
		orDefault(params.SymbolPeriod, d.SymbolPeriod), 0, 1, true, false), 1e-6)
	params.Seed = orDefault(params.Seed, d.Seed)
	from := newBackdrop(params.From, defaultAttr)
	params.From = Snapshot{}
	return &decrypt{DecryptParams: params, defaultAttr: defaultAttr, from: from}
}

// DecryptParams configures [Decrypt].
type DecryptParams struct {
	// CiphertextColors are the colors ciphertext is picked from.
	CiphertextColors []term.Color
	// CipherSymbols are the glyphs ciphertext is picked from.
	CipherSymbols []rune
	// TypingSymbols is the sequence a character blocks in through as it
	// is typed.
	TypingSymbols []rune
	// DiscoverColor is the color a character is discovered in.
	DiscoverColor term.Color
	// TypingDuration, FastDuration and DiscoverDuration are the
	// fractions of the animation spent typing, cycling quickly, and
	// discovering each character; the slow cycling in between takes up
	// to the rest, each character stopping at its own time, so their sum
	// must be below 1. (range (0, 1))
	TypingDuration   float64
	FastDuration     float64
	DiscoverDuration float64
	// SymbolPeriod is the fraction of the animation a cipher symbol
	// stays for while cycling quickly; slow cycling holds symbols five
	// times longer. (range (0, 1], capped below at 1e-6)
	SymbolPeriod float64
	// Seed picks the ciphertext. Defaults to a fixed value if zero.
	Seed int64
	// From is the screen the typing starts from instead of a blank one.
	From Snapshot
}

// DefaultDecryptParams returns the terminaltexteffects defaults: green
// ciphertext picked from keyboard, block, box drawing and
// miscellaneous symbols.
func DefaultDecryptParams() DecryptParams {
	symbols := make([]rune, 0, 512)
	for _, span := range [][2]rune{{33, 126}, {9608, 9631}, {9472, 9598}, {174, 451}} {
		for r := span[0]; r <= span[1]; r++ {
			symbols = append(symbols, r)
		}
	}
	return DecryptParams{
		CiphertextColors: []term.Color{
			term.NewRGBColor(0x00, 0x80, 0x00),
			term.NewRGBColor(0x00, 0xcb, 0x00),
			term.NewRGBColor(0x00, 0xff, 0x00),
		},
		CipherSymbols:    symbols,
		TypingSymbols:    []rune{'▉', '▓', '▒', '░'},
		DiscoverColor:    term.NewRGBColor(255, 255, 255),
		TypingDuration:   0.3,
		FastDuration:     0.3,
		DiscoverDuration: 0.08,
		SymbolPeriod:     0.01,
		Seed:             1,
	}
}

type decrypt struct {
	DecryptParams
	defaultAttr term.Attributes
	from        backdrop
}

func (s *decrypt) Shade(frame, total int, cells [][]term.Cell) {
	s.ShadeImages(frame, total, cells, nil)
}

func (s *decrypt) ShadeImages(frame, total int, cells [][]term.Cell, images []term.Image) []term.Image {
	return s.from.reveal(frame, total, cells, images, s.shade)
}

func (s *decrypt) shade(t float64, cells [][]term.Cell) uncover {
	rows, cols := canvasSize(cells)
	if rows == 0 || cols == 0 {
		return nil
	}
	glyphs := collectContent(cells, s.defaultAttr.Bg)
	typeAnim := min(s.TypingDuration*0.3, 0.03)
	typing := s.TypingDuration - typeAnim
	fastStart := s.TypingDuration
	slowStart := fastStart + s.FastDuration
	slowMax := 1 - slowStart - s.DiscoverDuration
	for i, g := range glyphs {
		start := typing * float64(i) / float64(len(glyphs))
		if t < start {
			blankCell(cells, g.x, g.y, s.defaultAttr.Bg)
			continue
		}
		// Painted blanks have nothing to decrypt: they just show up once
		// typed.
		if !hasOriginalChar(g.cell) {
			continue
		}
		color := s.CiphertextColors[pick3(s.Seed, g.x, g.y, 1, len(s.CiphertextColors))]
		cipher := func(step int) rune {
			return s.CipherSymbols[pick3(s.Seed, g.x, g.y, 100+step, len(s.CipherSymbols))]
		}
		switch {
		case t < fastStart:
			if p := window(t, start, typeAnim); p < 1 {
				step := min(len(s.TypingSymbols)-1, int(p*float64(len(s.TypingSymbols))))
				putChar(cells, g.x, g.y, s.TypingSymbols[step],
					s.CiphertextColors[pick3(s.Seed, g.x, g.y, 2+step, len(s.CiphertextColors))])
				continue
			}
			putChar(cells, g.x, g.y, cipher(0), color)
		case t < slowStart:
			putChar(cells, g.x, g.y, cipher(1+int((t-fastStart)/s.SymbolPeriod)), color)
		default:
			slow := slowMax * unit3(s.Seed, g.x, g.y, 3)
			if t < slowStart+slow {
				putChar(cells, g.x, g.y, cipher(-1-int((t-slowStart)/(5*s.SymbolPeriod))), color)
				continue
			}
			p := window(t, slowStart+slow, s.DiscoverDuration)
			paintFg(cells, g.x, g.y, fadeFg(p, s.DiscoverColor, g.cell.Fg, s.defaultAttr.Fg))
		}
	}
	// The typing overwrites the backdrop in reading order.
	return func(x, y int) float64 {
		at := y*cols + x
		if len(glyphs) == 0 {
			return typing * float64(at) / float64(rows*cols)
		}
		next := sort.Search(len(glyphs), func(i int) bool { return glyphs[i].y*cols+glyphs[i].x >= at })
		return typing * float64(next) / float64(len(glyphs))
	}
}
