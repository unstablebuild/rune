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
	"image/draw"
	"math"
	"sync/atomic"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader/shaderutils"
)

// GrayFadeParams configures the [GrayFade] effect.
//
// GrayFade has no color parameters: it desaturates each cell's foreground
// toward its own per-channel luminance, so every cell keeps its relative
// brightness instead of collapsing to a single shared gray. Background
// attributes are left untouched.
type GrayFadeParams struct {
	// FadeFrames decouples the visual fade length from the shader's
	// containing-component lifetime.
	//
	//   - FadeFrames > 0: progress ramps linearly from 0 to 1 over
	//     FadeFrames frames and then HOLDS at 1.0 until frame >= total.
	//     Use this when the shader has to outlive its visual animation —
	//     e.g. as the workspace-loading shader, where the host component
	//     is configured with an oversized total to cover the async load,
	//     but the fade itself should finish in a fixed wall-clock budget
	//     and stay there.
	//
	//   - FadeFrames == 0 (default): standard [Shader] convention.
	//     Progress ramps linearly with frame/(total-1) and reaches 1.0
	//     at frame == total-1. Use this for standalone runs where the
	//     caller-supplied duration is the only knob (e.g. `:shaderrun
	//     grayFade <duration>`).
	FadeFrames int
}

// DefaultGrayFadeParams returns a sane set of [GrayFadeParams]. The
// default leaves [GrayFadeParams.FadeFrames] zero so GrayFade behaves
// like a normal duration-driven shader; callers that need the
// fade-then-hold behavior set it explicitly.
func DefaultGrayFadeParams() GrayFadeParams { return GrayFadeParams{} }

// GrayFade is a Shader that progressively removes color from every cell's
// foreground by desaturating it toward its own luminance gray. It is
// intended to be used as the "loading" stage of a transition; the next
// shader can query [GrayFadeShader.Progress] to find out how far the
// desaturation has gone and apply the same amount to its own snapshot,
// so the two effects compose without a visible seam.
//
// It is an [ImageShader] that desaturates placements alike, through
// copies of their pixels.
func GrayFade(params GrayFadeParams, defaultAttr term.Attributes) *GrayFadeShader {
	return &GrayFadeShader{GrayFadeParams: params, defaultAttr: defaultAttr}
}

// GrayFadeShader is the concrete [Shader] type returned by [GrayFade].
// It is exported so callers can recover its [GrayFadeShader.Progress]
// from a [Shader] interface value via a type assertion.
type GrayFadeShader struct {
	GrayFadeParams
	defaultAttr term.Attributes

	// progressBits stores the latest [0,1] progress observed during
	// Shade encoded as math.Float64bits so it can be read atomically by
	// callers (e.g. shaderRunner) without locking the draw thread.
	progressBits atomic.Uint64

	// grays are the desaturated copies of the pictures in the last frame
	// shaded, by the part of the picture they copy.
	grays map[grayKey]*grayImage
}

type grayKey struct {
	id   term.ImageID
	crop image.Rectangle
}

// grayImage is a desaturated copy of a picture.
type grayImage struct {
	id      term.ImageID
	version uint64
	pixels  *image.RGBA
	// The picture's version and the amount it was made with.
	srcVersion uint64
	amount     float64
	used       bool
}

// Shade satisfies [Shader]. See [GrayFadeParams.FadeFrames] for the two
// progress regimes. Frame 0 always leaves cells untouched so the first
// frame of the transition is the live content.
func (s *GrayFadeShader) Shade(frame, total int, cells [][]term.Cell) {
	s.ShadeImages(frame, total, cells, nil)
}

// ShadeImages satisfies [ImageShader].
func (s *GrayFadeShader) ShadeImages(
	frame, total int, cells [][]term.Cell, images []term.Image,
) []term.Image {
	if frame < 0 {
		return images
	}
	rampFrames := s.FadeFrames
	if rampFrames <= 0 {
		// Standard shader semantics: ramp ends at frame == total-1.
		if total <= 0 {
			return images
		}
		rampFrames = total
	}
	progress := 1.0
	switch {
	case rampFrames <= 1:
		progress = 1
	case frame < rampFrames-1:
		progress = float64(frame) / float64(rampFrames-1)
	}
	progress = math.Min(1, math.Max(0, progress))
	s.setProgress(progress)
	if progress <= 0 {
		return images
	}
	for y, row := range cells {
		for x := range row {
			cells[y][x].Fg = shaderutils.DesaturateColor(
				row[x].Fg, progress, s.defaultAttr.Fg,
			)
		}
	}
	for i, img := range images {
		images[i] = s.gray(img, progress)
	}
	for id, g := range s.grays {
		if !g.used {
			delete(s.grays, id)
		}
		g.used = false
	}
	return images
}

// gray returns img showing its picture desaturated by amount.
func (s *GrayFadeShader) gray(img term.Image, amount float64) term.Image {
	if img.Src == nil {
		return img
	}
	if s.grays == nil {
		s.grays = make(map[grayKey]*grayImage)
	}
	crop := cropOf(img)
	key := grayKey{img.ID, crop}
	g := s.grays[key]
	if g == nil {
		g = &grayImage{id: term.NewImageID()}
		s.grays[key] = g
	}
	if g.pixels == nil || g.srcVersion != img.Version || g.amount != amount {
		// The writer is done with the previous copy once it is asked to
		// draw another frame, so its pixels are reused.
		if g.pixels == nil || g.pixels.Rect.Size() != crop.Size() {
			g.pixels = image.NewRGBA(image.Rect(0, 0, crop.Dx(), crop.Dy()))
		}
		draw.Draw(g.pixels, g.pixels.Rect, img.Src, crop.Min, draw.Src)
		desaturatePixels(g.pixels, amount)
		g.srcVersion, g.amount = img.Version, amount
		g.version++
	}
	g.used = true
	img.Src, img.ID, img.Version, img.Crop = g.pixels, g.id, g.version, image.Rectangle{}
	return img
}

// Progress returns the latest fade progress observed by Shade, in the
// range [0, 1]. It is safe to call from another goroutine; the value is
// always consistent with the most recent draw.
func (s *GrayFadeShader) Progress() float64 {
	return math.Float64frombits(s.progressBits.Load())
}

func (s *GrayFadeShader) setProgress(p float64) {
	s.progressBits.Store(math.Float64bits(p))
}
