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
	"image"
	"math"
	"math/rand/v2"

	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/component/braille"
	"unstable.build/rune/internal/component/shader/shaderutils"
)

// BrailleBloomParams configures [BrailleBloom]. Times are fractions of
// the animation's length.
type BrailleBloomParams struct {
	// Seed fixes the path the front takes, so equal params replay the
	// same animation.
	Seed uint64
	// Lead is how long dots brighten before the front reaches them.
	Lead float64
	// Bloom is how long a dot takes to grow into the picture under it,
	// flashing Glow, and cool down. When Lead and Bloom add up to more
	// than the whole animation, both shrink in proportion to fit.
	Bloom float64
	// Roughness, from 0 to 1, is how much the time the front takes to
	// cross a dot varies. An even front sweeps the art, while a rough one
	// branches and races ahead along the quickest dots.
	Roughness float64
	// Glow is the color dots flash as they bloom.
	Glow term.Color
}

// DefaultBrailleBloomParams returns the params of the Rune intro.
func DefaultBrailleBloomParams() BrailleBloomParams {
	return BrailleBloomParams{
		Seed:      1,
		Lead:      0.08,
		Bloom:     0.3,
		Roughness: 0.85,
		Glow:      term.NewRGBColor(255, 255, 255),
	}
}

const (
	// brailleBloomGrow is how far through its bloom a dot's disk covers
	// its whole share of the picture.
	brailleBloomGrow = 0.6
	// brailleBloomPeak is how far through its bloom a dot glows the
	// most, after which it cools down.
	brailleBloomPeak = 0.12
	// brailleBloomSeed is the radius a disk starts at, about that of a
	// braille dot, relative to the shorter side of the dot's share of the
	// picture.
	brailleBloomSeed     = 0.3
	brailleBloomSeedSalt = 0x3c6ef372fe94f82b
)

// BrailleBloom is an [ImageShader] that turns the braille rendering of
// the wallpaper drawn by art into the wallpaper's picture. The wallpaper
// and art are as for [BrailleSwap].
//
// A glowing front enters the art at its topmost dot and spreads through
// it by first-passage percolation, like [BrailleSolidify] does across
// cells, but dot by dot. Dots brighten as the front nears, and as it
// reaches each one, the dot blooms: it turns into a disk of the picture
// under it, which flashes Glow and grows until it covers its share of the
// picture, merging with its neighbors into the whole picture.
//
// The picture is expected to be stretched over its cells, with each of
// its cells' dots over the part of the picture the dot draws. When the
// wallpaper has no picture, such as on screens that do not draw images,
// fallback shades the screen instead, or the screen is left as drawn when
// fallback is nil.
//
// The first frame shows the braille rendering and the last the wallpaper
// as drawn. Cells drawn over the wallpaper, such as a prompt, are left
// untouched, as are screens without blank braille cells.
func BrailleBloom(
	params BrailleBloomParams, defaultAttr term.Attributes,
	art tui.Component, fallback Shader,
) Shader {
	glow, glowRGB := brailleRGB(params.Glow)
	return &brailleBloom{
		BrailleBloomParams: params,
		defaultAttr:        defaultAttr,
		art:                brailleArtCache{art: art},
		fallback:           fallback,
		glow:               glow,
		glowRGB:            glowRGB,
		id:                 term.NewImageID(),
	}
}

type brailleBloom struct {
	BrailleBloomParams
	defaultAttr term.Attributes
	art         brailleArtCache
	fallback    Shader
	glow        [3]float64
	glowRGB     bool

	// picture is the wallpaper's picture, as placed on the wallpaper of
	// region, that the fields below describe.
	picture term.Image
	region  image.Rectangle
	// pixels are the picture's, cropped, which dots divide into cols by
	// rows shares of dotW by dotH pixels.
	pixels     *image.RGBA
	cols, rows int
	dotW, dotH float64
	// arrival is when the front reaches each dot of the picture, row by
	// row, from 0 at the topmost dot of the art to 1 at the last dot of
	// the art it reaches.
	arrival []float64

	// bloomed is the picture as of the last frame shaded.
	bloomed *image.RGBA
	id      term.ImageID
	version uint64
}

func (s *brailleBloom) Shade(frame, total int, cells [][]term.Cell) {
	s.ShadeImages(frame, total, cells, nil)
}

func (s *brailleBloom) ShadeImages(
	frame, total int, cells [][]term.Cell, images []term.Image,
) []term.Image {
	if frame < 0 || frame >= total-1 {
		return images
	}
	region, art, ok := s.art.find(cells)
	if !ok {
		return images
	}
	picture, ok := wallpaperPicture(images, region)
	if !ok {
		if s.fallback == nil {
			return images
		}
		return ShadeImages(s.fallback, frame, total, cells, images)
	}
	if picture.ID != s.picture.ID || picture.Version != s.picture.Version ||
		picture.Bounds() != s.picture.Bounds() || picture.Crop != s.picture.Crop ||
		region != s.region || s.arrival == nil {
		s.plan(picture, region, art)
	}

	progress := float64(frame) / float64(total-1)
	lead := math.Max(0, s.Lead)
	bloom := math.Max(brailleShortestMelt, s.Bloom)
	if sum := lead + bloom; sum > 1 {
		lead, bloom = lead/sum, bloom/sum
	}
	at := func(dot int) float64 {
		return (progress - lead - s.arrival[dot]*(1-lead-bloom)) / bloom
	}
	s.drawCells(cells, art, lead/bloom, at)
	s.drawPicture(at)

	s.version++
	kept := images[:0]
	for _, img := range images {
		if !isWallpaperPicture(img, region) {
			kept = append(kept, img)
			continue
		}
		if img.ID != picture.ID {
			continue
		}
		img.Src, img.ID, img.Version, img.Crop = s.bloomed, s.id, s.version, image.Rectangle{}
		kept = append(kept, img)
	}
	return kept
}

// wallpaperPicture returns the first of images that is a picture of the
// wallpaper whose area is region.
func wallpaperPicture(images []term.Image, region image.Rectangle) (term.Image, bool) {
	for _, img := range images {
		if img.Src != nil && img.Width > 0 && img.Height > 0 && isWallpaperPicture(img, region) {
			return img, true
		}
	}
	return term.Image{}, false
}

// plan lays out the dots of picture, placed on the wallpaper of region
// whose braille rendering is art, and works out when the front reaches
// each one.
func (s *brailleBloom) plan(picture term.Image, region image.Rectangle, art [][]term.Cell) {
	s.picture, s.region = picture, region
	s.pixels = rgbaPixels(picture)
	s.bloomed = image.NewRGBA(s.pixels.Rect)
	s.cols, s.rows = picture.Width*braille.CellDotsX, picture.Height*braille.CellDotsY
	s.dotW = float64(s.pixels.Rect.Dx()) / float64(s.cols)
	s.dotH = float64(s.pixels.Rect.Dy()) / float64(s.rows)

	n := s.cols * s.rows
	rng := rand.New(rand.NewPCG(s.Seed, brailleBloomSeedSalt))
	roughness := math.Min(brailleMaxRoughness, math.Max(0, s.Roughness))
	cost := make([]float64, n)
	raised := make([]bool, n)
	source := -1
	for i := range n {
		raised[i] = s.raised(art, i)
		cost[i] = 1 + roughness*(2*rng.Float64()-1)
		if !raised[i] {
			cost[i] = brailleGapCost
		} else if source < 0 {
			source = i
		}
	}

	s.arrival = make([]float64, n)
	if source < 0 {
		return
	}
	reach := frontReach(cost, s.cols, s.rows, source, s.dotH/s.dotW)
	var last float64
	for i, t := range reach {
		if raised[i] {
			last = math.Max(last, t)
		}
	}
	if last == 0 {
		return
	}
	for i, t := range reach {
		s.arrival[i] = math.Min(1, t/last)
	}
}

// raised reports whether art raises the picture's dot at index i.
func (s *brailleBloom) raised(art [][]term.Cell, i int) bool {
	dx, dy := i%s.cols, i/s.cols
	a, ok := brailleArtCell(art,
		s.picture.Pos.X+dx/braille.CellDotsX-s.region.Min.X,
		s.picture.Pos.Y+dy/braille.CellDotsY-s.region.Min.Y)
	return ok && braille.IsPattern(a.Ch) &&
		a.Ch&braille.Bit(dx%braille.CellDotsX, dy%braille.CellDotsY) != 0
}

// drawCells draws the art over the wallpaper's cells, without the dots
// that bloomed. at tells how far through its bloom each dot of the
// picture is, and lead how far before its bloom a dot starts to brighten,
// both in blooms. Dots the picture does not cover never bloom.
func (s *brailleBloom) drawCells(
	cells [][]term.Cell, art [][]term.Cell, lead float64, at func(dot int) float64,
) {
	region := s.region
	for y := region.Min.Y; y < region.Max.Y && y < len(cells); y++ {
		for x := region.Min.X; x < region.Max.X && x < len(cells[y]); x++ {
			c := &cells[y][x]
			a, ok := brailleArtCell(art, x-region.Min.X, y-region.Min.Y)
			if !ok || !isBrailleWallpaper(*c) || !braille.IsPattern(a.Ch) || a.Ch == braille.Blank {
				continue
			}
			ch, warmth := a.Ch, 0.0
			for dy := range braille.CellDotsY {
				for dx := range braille.CellDotsX {
					bit := braille.Bit(dx, dy)
					dot, ok := s.dotAt(x, y, dx, dy)
					if a.Ch&bit == 0 || !ok {
						continue
					}
					u := at(dot)
					if u >= 0 {
						ch &^= bit
						continue
					}
					if lead > 0 {
						warmth = math.Max(warmth, 1+u/lead)
					}
				}
			}
			if ch == braille.Blank {
				continue
			}
			c.Ch, c.Fg = ch, a.Fg
			if warmth > 0 {
				c.Fg = shaderutils.InterpolateColor(brailleLeadHeat*warmth, a.Fg, s.Glow, s.defaultAttr.Fg)
			}
		}
	}
}

// dotAt returns the index of the picture's dot dx, dy of the cell at x,
// y, reporting false when the picture does not cover it.
func (s *brailleBloom) dotAt(x, y, dx, dy int) (int, bool) {
	px := (x-s.picture.Pos.X)*braille.CellDotsX + dx
	py := (y-s.picture.Pos.Y)*braille.CellDotsY + dy
	if px < 0 || py < 0 || px >= s.cols || py >= s.rows {
		return 0, false
	}
	return py*s.cols + px, true
}

// drawPicture draws bloomed: every dot's share of the picture, as far
// through its bloom as at tells. Before it blooms a share is clear, and
// once it bloomed it is the picture.
func (s *brailleBloom) drawPicture(at func(dot int) float64) {
	seed := brailleBloomSeed * math.Min(s.dotW, s.dotH)
	full := math.Hypot(s.dotW, s.dotH)/2 + 1
	for dy := range s.rows {
		y0, y1 := pixelEdge(dy, s.dotH), pixelEdge(dy+1, s.dotH)
		for dx := range s.cols {
			x0, x1 := pixelEdge(dx, s.dotW), pixelEdge(dx+1, s.dotW)
			u := at(dy*s.cols + dx)
			for y := y0; y < y1; y++ {
				src := s.pixels.Pix[s.pixels.PixOffset(x0, y):s.pixels.PixOffset(x1, y)]
				dst := s.bloomed.Pix[s.bloomed.PixOffset(x0, y):s.bloomed.PixOffset(x1, y)]
				switch {
				case u < 0:
					clear(dst)
				case u >= 1:
					copy(dst, src)
				default:
					shrink := 1 - math.Min(1, u/brailleBloomGrow)
					radius := seed + (full-seed)*(1-shrink*shrink*shrink)
					cx, cy := (float64(dx)+0.5)*s.dotW, (float64(dy)+0.5)*s.dotH
					s.bloomRow(dst, src, x0, float64(y)+0.5-cy, cx, radius, brailleBloomHeat(u))
				}
			}
		}
	}
}

// bloomRow draws into dst the pixels src of a row of a dot's share of the
// picture, from column x0, that its disk of radius centered at column cx
// covers, oy pixels below the row, blended toward Glow by heat.
func (s *brailleBloom) bloomRow(dst, src []uint8, x0 int, oy, cx, radius, heat float64) {
	if !s.glowRGB {
		heat = 0
	}
	for i := 0; i < len(dst); i += 4 {
		ox := float64(x0+i/4) + 0.5 - cx
		cover := math.Max(0, math.Min(1, radius-math.Hypot(ox, oy)+0.5))
		a := float64(src[i+3]) * cover
		for c := range 3 {
			v := float64(src[i+c]) * cover
			dst[i+c] = uint8(math.Round(v + (s.glow[c]*a/255-v)*heat))
		}
		dst[i+3] = uint8(math.Round(a))
	}
}

// brailleBloomHeat returns how close to Glow a dot u through its bloom
// is: heating up from the lead's warmth until the peak, then cooling.
func brailleBloomHeat(u float64) float64 {
	if u < brailleBloomPeak {
		return brailleLeadHeat + (1-brailleLeadHeat)*u/brailleBloomPeak
	}
	k := (1 - u) / (1 - brailleBloomPeak)
	return k * k * k
}

// pixelEdge returns the pixel where the share of dot i begins, for shares
// size pixels wide.
func pixelEdge(i int, size float64) int {
	return int(math.Round(float64(i) * size))
}
