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

// Package emoji rasterizes color emoji clusters to premultiplied RGBA
// sized for a terminal cell, isolating all go-text font-stack and image
// decoding from the rest of the GUI. Only bitmap (PNG/JPG) color glyphs
// are handled; vector color formats are out of scope.
package emoji

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // register the JPEG decoder for JPG-format glyphs
	_ "image/png"  // register the PNG decoder for the common emoji case
	"os"
	"unicode/utf8"

	"github.com/go-text/typesetting/di"
	gofont "github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/language"
	"github.com/go-text/typesetting/shaping"
	"github.com/unstablebuild/rune-go-sdk/term/graphemecluster"
	xdraw "golang.org/x/image/draw"
)

// probeRune verifies a candidate face exposes decodable color bitmaps; a
// face lacking a bitmap for it is rejected so the resolver can fall back.
const probeRune = '😀'

// Face rasterizes color emoji clusters from one bitmap-emoji font. It is
// not safe for concurrent use; the renderer drives it from the GUI
// goroutine.
type Face struct {
	face   *gofont.Face
	shaper shaping.HarfbuzzShaper
}

// NewFace parses a bitmap color-emoji font (e.g. Noto Color Emoji) and
// returns a Face, erroring if the bytes are not a parseable font.
func NewFace(ttf []byte) (*Face, error) {
	if len(ttf) == 0 {
		return nil, errors.New("emoji: empty font data")
	}
	face, err := gofont.ParseTTF(bytes.NewReader(ttf))
	if err != nil {
		return nil, fmt.Errorf("emoji: parse font: %w", err)
	}
	return &Face{face: face}, nil
}

// NewFaceFromFile parses a color-emoji font file (.ttf/.otf or a
// .ttc/.otc collection) and returns a Face backed by the first contained
// face with a decodable color bitmap for the probe emoji. It parses
// through the *os.File so large system collections stay file-backed, and
// errors when the file cannot be parsed or has no bitmap color glyph.
func NewFaceFromFile(path string) (*Face, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("emoji: open font: %w", err)
	}
	defer f.Close()

	faces, err := gofont.ParseTTC(f)
	if err != nil {
		return nil, fmt.Errorf("emoji: parse font: %w", err)
	}
	for _, face := range faces {
		candidate := &Face{face: face}
		if _, ok := candidate.bitmap([]rune{probeRune}); ok {
			return candidate, nil
		}
	}
	return nil, fmt.Errorf("emoji: %q has no bitmap color glyph", path)
}

// Has reports whether the renderer should route cluster to the color
// path: it must be emoji-presented and resolve to a single PNG/JPG bitmap
// glyph. The presentation gate keeps text-presentation runes that emoji
// fonts also carry color bitmaps for (digits, '©', bare '❤', keycaps)
// on the monochrome path.
func (f *Face) Has(cluster []rune) bool {
	// The renderer asks about every glyph it draws, and no ASCII rune is
	// emoji-presented on its own.
	if len(cluster) == 1 && cluster[0] < utf8.RuneSelf {
		return false
	}
	if !emojiPresented(cluster) {
		return false
	}
	_, ok := f.bitmap(cluster)
	return ok
}

func emojiPresented(cluster []rune) bool {
	return graphemecluster.StringWidth(string(cluster)) == 2
}

func (f *Face) bitmap(cluster []rune) (gofont.GlyphBitmap, bool) {
	if len(cluster) == 1 {
		gid, ok := f.face.NominalGlyph(cluster[0])
		if !ok {
			return gofont.GlyphBitmap{}, false
		}
		return f.colorBitmap(gid)
	}
	return f.shape(cluster)
}

func (f *Face) colorBitmap(gid gofont.GID) (gofont.GlyphBitmap, bool) {
	bm, ok := f.face.GlyphData(gid).(gofont.GlyphBitmap)
	if !ok {
		return gofont.GlyphBitmap{}, false
	}
	switch bm.Format {
	case gofont.PNG, gofont.JPG:
		return bm, true
	default:
		return gofont.GlyphBitmap{}, false
	}
}

// shape returns the cluster's sole color bitmap. Fonts disagree on how
// to treat default-ignorable code points: Noto consumes a variation
// selector while shaping, whereas Apple Color Emoji emits a blank glyph
// for it beside the emoji. Keying off the number of color bitmaps rather
// than the number of glyphs keeps those placeholders from disqualifying
// a cluster, while still rejecting a sequence the font cannot ligate,
// which yields one bitmap per component and would otherwise render as
// whichever component happened to come first.
func (f *Face) shape(cluster []rune) (gofont.GlyphBitmap, bool) {
	out := f.shaper.Shape(shaping.Input{
		Text:      cluster,
		RunStart:  0,
		RunEnd:    len(cluster),
		Face:      f.face,
		Size:      64 << 6,
		Script:    language.LookupScript(cluster[0]),
		Direction: di.DirectionLTR,
	})
	var found gofont.GlyphBitmap
	var n int
	for _, g := range out.Glyphs {
		if bm, ok := f.colorBitmap(g.GlyphID); ok {
			found, n = bm, n+1
		}
	}
	if n != 1 {
		return gofont.GlyphBitmap{}, false
	}
	return found, true
}

// Glyph decodes the cluster's color bitmap and scales it to fit a
// cellW×cellH box, preserving aspect ratio. It returns a premultiplied,
// tightly packed RGBA, or false when there is no color glyph or the box
// is empty.
func (f *Face) Glyph(cluster []rune, cellW, cellH int) (*image.RGBA, bool) {
	if cellW <= 0 || cellH <= 0 {
		return nil, false
	}
	bm, ok := f.bitmap(cluster)
	if !ok {
		return nil, false
	}
	src, _, err := image.Decode(bytes.NewReader(bm.Data))
	if err != nil {
		return nil, false
	}
	sb := src.Bounds()
	if sb.Dx() <= 0 || sb.Dy() <= 0 {
		return nil, false
	}

	dw, dh := fitBox(sb.Dx(), sb.Dy(), cellW, cellH)
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	// Scaling into an *image.RGBA destination yields premultiplied
	// alpha regardless of the decoded source's model (NRGBA/RGBA).
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, sb, xdraw.Over, nil)
	return dst, true
}

func fitBox(srcW, srcH, boxW, boxH int) (int, int) {
	// Cross-multiply to pick the binding axis without float error.
	if srcW*boxH >= srcH*boxW {
		return boxW, max(srcH*boxW/srcW, 1)
	}
	return max(srcW*boxH/srcH, 1), boxH
}
