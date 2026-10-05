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

package glslshader

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"image"
	"image/png"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component/asciiart"
	"unstable.build/rune/internal/component/shader/shadertest"
)

//go:embed test_logo.png
var testLogo []byte

func openTestLogo() image.Image {
	img, err := png.Decode(bytes.NewReader(testLogo))
	if err != nil {
		panic(fmt.Errorf("png decode: %v", err))
	}
	return img
}

func TestBurning(t *testing.T) {
	sh := Burning(
		DefaultBurningParams(openTestLogo(), component.FrameCharSetDefault()),
		term.Attributes{},
		4.0, 30,
	)
	shadertest.TestShader(t, sh)
}

func TestBurningIgnoresInvalidWallpaperBounds(t *testing.T) {
	t.Parallel()

	params := DefaultBurningParams(openTestLogo(), component.FrameCharSetDefault())
	sh := Burning(params, term.Attributes{}, 4.0, 30)
	grid := make([][]term.Cell, 55)
	for y := range grid {
		grid[y] = make([]term.Cell, 55)
	}
	grid[54][0].Ch = params.Logo.WallpaperInvisibleChar
	grid[0][54].Ch = params.Logo.WallpaperInvisibleChar
	// hasExpectedWallpaper scans diagonally from the middle with a Y step adjusted
	// by the terminal cell aspect ratio, so keep these points contiguous on that
	// sampling path while making the discovered bounds invalid.
	grid[27][27].Ch = params.Logo.WallpaperInvisibleChar
	grid[27][28].Ch = params.Logo.WallpaperInvisibleChar
	grid[28][29].Ch = params.Logo.WallpaperInvisibleChar
	grid[28][30].Ch = params.Logo.WallpaperInvisibleChar

	require.NotPanics(t, func() {
		sh.Shade(7, 90, grid)
	})
}

func BenchmarkBurning(b *testing.B) {
	sh := Burning(
		DefaultBurningParams(openTestLogo(), component.FrameCharSetDefault()),
		term.Attributes{},
		4.0, 30,
	)
	width, height := 600, 400
	cfg := asciiart.DefaultConfig()
	cfg.Color = true
	cfg.MaintainAspectRatio = true
	cfg.DensityCharacters = "\u2009▓▓▓▓▓▓▓▓▓"
	image := asciiart.NewComponent(openTestLogo(), cfg)
	span := component.NewSpan(image, component.SpanConfig{
		PadHorizontalPerc: 0.4,
		PadVerticalPerc:   0.2,
		ContentAlignment:  component.AlignmentCentered,
	})
	writer := cell.NewBufferWriter(context.Background(), width, height)
	span.Resize(width, height)
	span.Draw(writer)
	shadertest.BenchmarkShader(b, sh, width, height, writer.RawCells())
}

func TestBurningWallpaperPresenceGuard(t *testing.T) {
	t.Parallel()

	const (
		rows  = 24
		cols  = 40
		total = 90
		frame = 7
	)

	tests := []struct {
		name string
		// next returns the matrix fed to the asserted second Shade. It
		// is built fresh (mirroring how shader.Component re-renders the
		// wallpaper into a cleared buffer every frame) so the first
		// frame's flame output never leaks into the second frame's
		// input.
		next       func(invisible rune) [][]term.Cell
		wantCancel bool
	}{
		{
			name: "marker still present keeps rendering",
			next: func(invisible rune) [][]term.Cell {
				return newWallpaperGrid(rows, cols, invisible)
			},
			wantCancel: false,
		},
		{
			name: "marker overwritten cancels",
			next: func(invisible rune) [][]term.Cell {
				// Simulate the centered command prompt painting a
				// block over the middle of the screen, where the
				// marker is captured.
				g := newWallpaperGrid(rows, cols, invisible)
				for y := rows / 5; y < rows*3/5; y++ {
					for x := cols/2 - 8; x < cols/2+8; x++ {
						g[y][x].Ch = '#'
					}
				}
				return g
			},
			wantCancel: true,
		},
		{
			name: "whole screen replaced cancels",
			next: func(_ rune) [][]term.Cell {
				return newWallpaperGrid(rows, cols, ' ')
			},
			wantCancel: true,
		},
		{
			name: "more rows cancels",
			next: func(invisible rune) [][]term.Cell {
				return newWallpaperGrid(rows+10, cols, invisible)
			},
			wantCancel: true,
		},
		{
			name: "fewer rows cancels",
			next: func(invisible rune) [][]term.Cell {
				return newWallpaperGrid(rows-10, cols, invisible)
			},
			wantCancel: true,
		},
		{
			name: "more cols cancels",
			next: func(invisible rune) [][]term.Cell {
				return newWallpaperGrid(rows, cols+10, invisible)
			},
			wantCancel: true,
		},
		{
			name: "fewer cols cancels",
			next: func(invisible rune) [][]term.Cell {
				return newWallpaperGrid(rows, cols-10, invisible)
			},
			wantCancel: true,
		},
		{
			name: "shrink to 1x1 cancels",
			next: func(invisible rune) [][]term.Cell {
				return newWallpaperGrid(1, 1, invisible)
			},
			wantCancel: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			params := DefaultBurningParams(
				openTestLogo(), component.FrameCharSetDefault())
			sh := Burning(params, term.Attributes{}, 4.0, 30)
			burn := sh.(*burning)

			invisible := params.Logo.WallpaperInvisibleChar
			sh.Shade(frame, total, newWallpaperGrid(rows, cols, invisible))
			require.True(t, burn.markerSet,
				"first Shade must locate and cache the wallpaper marker")
			require.False(t, burn.skipRenders,
				"shader must render while the wallpaper is behind it")

			next := tc.next(invisible)
			before := cloneGrid(next)
			require.NotPanics(t, func() {
				sh.Shade(frame+1, total, next)
			})

			assert.Equal(t, tc.wantCancel, burn.skipRenders,
				"skipRenders after mutation")
			if tc.wantCancel {
				assert.Equal(t, before, next,
					"a cancelled shader must leave the live cells untouched")
			}
		})
	}
}

func TestBurningRealWallpaperPromptCancels(t *testing.T) {
	t.Parallel()

	for _, dim := range [][2]int{{120, 40}, {80, 24}, {200, 60}} {
		w, h := dim[0], dim[1]
		t.Run(fmt.Sprintf("%dx%d", w, h), func(t *testing.T) {
			t.Parallel()

			params := DefaultBurningParams(
				openTestLogo(), component.FrameCharSetDefault())
			sh := Burning(params, term.Attributes{}, 4.0, 30)
			burn := sh.(*burning)

			sh.Shade(7, 90, renderLogoWallpaper(w, h))
			require.True(t, burn.markerSet)
			require.False(t, burn.skipRenders)

			// Centered command prompt: top at 20% height (see ide/ex.go),
			// horizontally centered, tall because it lists every command.
			grid := renderLogoWallpaper(w, h)
			top := int(0.2 * float64(h))
			bottom := top + h/2
			if bottom > h {
				bottom = h
			}
			left := w/2 - 20
			if left < 0 {
				left = 0
			}
			right := w/2 + 20
			if right > w {
				right = w
			}
			for y := top; y < bottom; y++ {
				for x := left; x < right; x++ {
					grid[y][x].Ch = '#'
				}
			}
			sh.Shade(8, 90, grid)

			assert.True(t, burn.skipRenders,
				"opening the command prompt must cancel the boot shader")
		})
	}
}

func TestBurningShadeInputSurface(t *testing.T) {
	t.Parallel()

	const total = 60

	newSh := func(hideLogo bool) *burning {
		var params BurningParams
		if hideLogo {
			params = BurningPresetGentleOnlyFlames(
				false, component.FrameCharSetDefault())
		} else {
			params = DefaultBurningParams(
				openTestLogo(), component.FrameCharSetDefault())
		}
		return Burning(params, term.Attributes{}, 4.0, 30).(*burning)
	}

	tests := []struct {
		name string
		run  func(t *testing.T, sh *burning, invisible rune)
	}{
		{
			name: "nil matrix",
			run: func(t *testing.T, sh *burning, _ rune) {
				sh.Shade(1, total, nil)
			},
		},
		{
			name: "empty matrix",
			run: func(t *testing.T, sh *burning, _ rune) {
				sh.Shade(1, total, [][]term.Cell{})
			},
		},
		{
			name: "row of zero width",
			run: func(t *testing.T, sh *burning, _ rune) {
				sh.Shade(1, total, [][]term.Cell{{}, {}})
			},
		},
		{
			name: "single cell",
			run: func(t *testing.T, sh *burning, invisible rune) {
				sh.Shade(1, total, newWallpaperGrid(1, 1, invisible))
			},
		},
		{
			name: "negative frame",
			run: func(t *testing.T, sh *burning, invisible rune) {
				sh.Shade(-5, total, newWallpaperGrid(10, 10, invisible))
			},
		},
		{
			name: "frame equal to total",
			run: func(t *testing.T, sh *burning, invisible rune) {
				sh.Shade(total, total, newWallpaperGrid(10, 10, invisible))
			},
		},
		{
			name: "frame beyond total",
			run: func(t *testing.T, sh *burning, invisible rune) {
				sh.Shade(total+100, total, newWallpaperGrid(10, 10, invisible))
			},
		},
		{
			name: "zero total",
			run: func(t *testing.T, sh *burning, invisible rune) {
				sh.Shade(0, 0, newWallpaperGrid(10, 10, invisible))
			},
		},
		{
			name: "ragged rows",
			run: func(t *testing.T, sh *burning, invisible rune) {
				g := newWallpaperGrid(12, 12, invisible)
				g[4] = g[4][:3]
				g[7] = g[7][:1]
				g[9] = nil
				sh.Shade(3, total, g)
			},
		},
		{
			name: "1x1 then huge",
			run: func(t *testing.T, sh *burning, invisible rune) {
				sh.Shade(1, total, newWallpaperGrid(1, 1, invisible))
				sh.Shade(2, total, newWallpaperGrid(200, 300, invisible))
			},
		},
		{
			name: "grow shrink grow",
			run: func(t *testing.T, sh *burning, invisible rune) {
				for i, dim := range [][2]int{
					{20, 40}, {80, 160}, {4, 4}, {120, 200}, {1, 1}, {40, 90},
				} {
					sh.Shade(i+1, total, newWallpaperGrid(dim[0], dim[1], invisible))
				}
			},
		},
		{
			name: "full frame sweep",
			run: func(t *testing.T, sh *burning, invisible rune) {
				g := newWallpaperGrid(18, 30, invisible)
				for f := 0; f <= total; f++ {
					sh.Shade(f, total, g)
				}
			},
		},
		{
			name: "no wallpaper markers",
			run: func(t *testing.T, sh *burning, _ rune) {
				g := make([][]term.Cell, 20)
				for y := range g {
					g[y] = make([]term.Cell, 30)
					for x := range g[y] {
						g[y][x].Ch = ' '
					}
				}
				sh.Shade(5, total, g)
			},
		},
	}

	for _, hideLogo := range []bool{false, true} {
		for _, tc := range tests {
			name := tc.name
			if hideLogo {
				name += " (HideLogo)"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				sh := newSh(hideLogo)
				invisible := sh.Logo.WallpaperInvisibleChar
				require.NotPanics(t, func() {
					tc.run(t, sh, invisible)
				})
			})
		}
	}
}

// newWallpaperGrid builds a rectangular cell matrix filled with the
// wallpaper invisible char so the burning shader detects a valid,
// maskable wallpaper covering the whole matrix (top-left marker at 0,0).
func newWallpaperGrid(rows, cols int, invisible rune) [][]term.Cell {
	g := make([][]term.Cell, rows)
	for y := range g {
		g[y] = make([]term.Cell, cols)
		for x := range g[y] {
			g[y][x].Ch = invisible
		}
	}
	return g
}

func cloneGrid(in [][]term.Cell) [][]term.Cell {
	out := make([][]term.Cell, len(in))
	for y := range in {
		out[y] = append([]term.Cell(nil), in[y]...)
	}
	return out
}

// renderLogoWallpaper renders the real centered logo wallpaper into a
// w*h cell matrix using the same asciiart density characters and span
// padding as the production wallpaper, so the invisible-char background
// surrounds a centered logo just as it does at runtime.
func renderLogoWallpaper(w, h int) [][]term.Cell {
	cfg := asciiart.DefaultConfig()
	cfg.Color = true
	cfg.MaintainAspectRatio = true
	cfg.DensityCharacters = "\u2009▓▓▓▓▓▓▓▓▓"
	art := asciiart.NewComponent(openTestLogo(), cfg)
	span := component.NewSpan(art, component.SpanConfig{
		PadHorizontalPerc: 0.4,
		PadVerticalPerc:   0.2,
		ContentAlignment:  component.AlignmentCentered,
	})
	writer := cell.NewBufferWriter(context.Background(), w, h)
	span.Resize(w, h)
	span.Draw(writer)
	return writer.RawCells()
}

// FuzzBurningShade drives Shade with arbitrary frame/total values and a
// byte-scripted sequence of matrices (varying shapes, ragged rows,
// wallpaper markers present or absent) on a single shader instance, so
// the cross-call reshape and marker-guard paths are exercised. The
// invariant under test is that no input can make Shade panic; state
// correctness is asserted by the table-driven tests above.
func FuzzBurningShade(f *testing.F) {
	f.Add(24, 40, 7, 90, []byte{0x01, 0x02, 0x03})
	f.Add(1, 1, 0, 1, []byte{})
	f.Add(0, 0, -3, 0, []byte{0xff})
	f.Add(80, 160, 200, 90, []byte{0x10, 0x00, 0x7f, 0x40})
	f.Add(13, 5, 5, 60, []byte{0xaa, 0x55, 0xaa, 0x55, 0x10})

	f.Fuzz(func(t *testing.T,
		rows, cols, frame, total int, script []byte,
	) {
		params := DefaultBurningParams(
			openTestLogo(), component.FrameCharSetDefault())
		sh := Burning(params, term.Attributes{}, 4.0, 30)
		invisible := params.Logo.WallpaperInvisibleChar

		// One Shade call per script byte (at least one), each on a
		// matrix whose shape and contents are derived from that byte so
		// the sequence reshapes the matrix between calls.
		if len(script) == 0 {
			script = []byte{0}
		}
		require.NotPanics(t, func() {
			for i, b := range script {
				grid := fuzzGrid(rows, cols, i, b, invisible)
				sh.Shade(frame+i, total, grid)
			}
		})
	})
}

// fuzzGrid derives a bounded cell matrix from a fuzz byte. It varies the
// dimensions per step, sometimes lays down wallpaper markers (so the
// shader captures bounds and later frames can lose them), and sometimes
// produces ragged rows or empty matrices.
func fuzzGrid(rows, cols, step int, b byte, invisible rune) [][]term.Cell {
	const maxDim = 256
	// Perturb the requested dimensions per step and clamp into a sane,
	// non-negative range so fuzzing cannot exhaust memory while still
	// covering empty, 1x1, and large matrices.
	r := clampDim(rows+step*int(b%5)-int(b%3), maxDim)
	c := clampDim(cols+int(b%7)-step, maxDim)
	if r == 0 || c == 0 {
		return make([][]term.Cell, r)
	}

	g := make([][]term.Cell, r)
	fill := b&0x01 == 0 // half the time, lay down wallpaper markers
	ragged := b&0x02 != 0
	for y := range g {
		w := c
		if ragged && y%3 == 0 {
			w = y % (c + 1)
		}
		g[y] = make([]term.Cell, w)
		for x := range g[y] {
			if fill {
				g[y][x].Ch = invisible
			} else {
				g[y][x].Ch = rune(' ' + (int(b)+x+y)%64)
			}
		}
	}
	return g
}

func clampDim(v, max int) int {
	if v < 0 {
		return 0
	}
	if v > max {
		return max
	}
	return v
}

// FuzzBurningParams drives Burning across its configuration surface: every
// float knob (including NaN/Inf and out-of-range values), arbitrary-length
// animated-parameter and gradient slices (including empty), and the bool
// flags. The two documented programmer-error invariants (a logo image is
// required unless HideLogo, and Ripples.StartFadeInAt must precede
// EndFadeInAt) are honoured by construction so the fuzzer explores the
// remaining surface. The invariant under test is that no such config can
// make Shade panic.
func FuzzBurningParams(f *testing.F) {
	f.Add([]byte{0x00})
	f.Add([]byte{0xff, 0x00, 0x80, 0x40, 0x01})
	f.Add(bytes.Repeat([]byte{0x7f}, 32))
	f.Add([]byte{0x10, 0x20, 0x30, 0x00, 0x00, 0x00})

	f.Fuzz(func(t *testing.T, seed []byte) {
		if len(seed) == 0 {
			seed = []byte{0}
		}
		r := &byteReader{data: seed}
		params := fuzzParams(r)

		sh := Burning(params, term.Attributes{}, 4.0, 30)
		invisible := params.Logo.WallpaperInvisibleChar

		require.NotPanics(t, func() {
			// Drive a few frames across the timeline on a valid
			// wallpaper so the logo/ripple/fade-out config paths run.
			for _, frame := range []int{0, 1, 30, 60, 89} {
				sh.Shade(frame, 90, newWallpaperGrid(20, 40, invisible))
			}
		})
	})
}

// fuzzParams builds a BurningParams from fuzz bytes. It always supplies a
// logo image and a valid ripples fade-in window so the two panic-guarded
// programmer-error invariants in initialize hold; everything else is left
// free, including empty gradients and NaN/Inf floats.
func fuzzParams(r *byteReader) BurningParams {
	// Keep StartFadeInAt strictly below EndFadeInAt as the contract
	// requires, and ensure the inequality still holds after initialize
	// clamps both to [0,1]: pick start in [0, 0.9] and end in
	// (start, 1.0].
	startFadeIn := r.unitFloat() * 0.9
	endFadeIn := startFadeIn + 0.05 + r.unitFloat()*(1.0-startFadeIn-0.05)

	return BurningParams{
		SwitchLightsOff:      r.boolean(),
		FlameSpeed:           r.wildFloat(),
		FlameTimeOffset:      r.wildFloat(),
		Squash:               r.wildFloat(),
		Laterals:             r.wildFloat(),
		Fiery:                r.wildFloat(),
		HeatGain:             r.wildFloat(),
		Intensity:            r.floatSlice(),
		Wide:                 r.floatSlice(),
		StartFadeOut:         r.wildFloat(),
		HideLogo:             r.boolean(),
		HideBackgroundFlames: r.boolean(),
		Colors: BurningColors{
			HeatColorGradientOverride: r.gradient(),
			SwapRedBlue:               r.boolean(),
			RipplesGradientStart:      r.gradient(),
			RipplesGradientEnd:        r.gradient(),
		},
		Logo: LogoParams{
			Image:                        openTestLogo(),
			FrameCharSet:                 component.FrameCharSetDefault(),
			WallpaperInvisibleChar:       '\u2009',
			HeatBrushFlow:                r.wildFloat(),
			HeatDissipation:              r.wildFloat(),
			HeatDissipationGradientAware: r.wildFloat(),
			HeatDissipationCutOff:        r.wildFloat(),
			Ripples: LogoRipplesParams{
				StartFadeInAt: startFadeIn,
				EndFadeInAt:   endFadeIn,
				Detail:        r.wildFloat(),
				Speed:         r.wildFloat(),
			},
		},
	}
}

// byteReader deterministically derives typed values from a fuzz byte
// slice, wrapping around when exhausted so short inputs still produce a
// full config.
type byteReader struct {
	data []byte
	pos  int
}

func (r *byteReader) next() byte {
	if len(r.data) == 0 {
		return 0
	}
	b := r.data[r.pos%len(r.data)]
	r.pos++
	return b
}

func (r *byteReader) boolean() bool { return r.next()&0x01 == 1 }

// unitFloat returns a value in [0,1].
func (r *byteReader) unitFloat() float { return float(r.next()) / 255.0 }

// wildFloat returns values spanning the normal range plus deliberately
// hostile ones (negatives, large magnitudes, NaN, +/-Inf) to probe the
// shader's numeric robustness.
func (r *byteReader) wildFloat() float {
	switch b := r.next(); b % 8 {
	case 0:
		return float(math.NaN())
	case 1:
		return float(math.Inf(1))
	case 2:
		return float(math.Inf(-1))
	case 3:
		return -float(b)
	case 4:
		return float(b) * 1e6
	default:
		return float(b) / 255.0
	}
}

func (r *byteReader) floatSlice() []float {
	n := int(r.next() % 6) // 0..5, including empty
	if n == 0 {
		return nil
	}
	out := make([]float, n)
	for i := range out {
		out[i] = r.wildFloat()
	}
	return out
}

func (r *byteReader) gradient() []term.Color {
	n := int(r.next() % 5) // 0..4, including empty
	if n == 0 {
		return nil
	}
	out := make([]term.Color, n)
	for i := range out {
		out[i] = term.NewRGBColor(
			int32(r.next()), int32(r.next()), int32(r.next()))
	}
	return out
}
