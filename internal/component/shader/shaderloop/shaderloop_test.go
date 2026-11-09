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

package shaderloop

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader/glslshader"
	"unstable.build/rune/internal/component/shader/shadertest"
)

var testAttr = term.Attributes{Fg: term.ColorSilver, Bg: term.ColorGray}

// total is the frame budget shader.Component runs these effects against.
var total = int(Duration / (time.Second / DefaultFPS))

// loopFrames is one visual loop at the given cadence.
func loopFrames(fps int, loop time.Duration) int {
	return int(float64(fps) * loop.Seconds())
}

func TestNamesAllResolve(t *testing.T) {
	require.NotEmpty(t, Names())
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			sh, ok := New(name, term.Attributes{}, Params{}, DefaultLoop)
			assert.True(t, ok)
			assert.NotNil(t, sh)
		})
	}
}

func TestRejectsUnknownNames(t *testing.T) {
	for _, name := range []string{
		"", "radarFrame", "incendium", "embers", "flames", "risingChars",
		"fade", "grayFade",
	} {
		_, ok := New(name, term.Attributes{}, Params{}, DefaultLoop)
		assert.False(t, ok, name)
		assert.False(t, slices.Contains(Names(), name), name)
	}
}

// rowSnapshot is a row of text as a status or tab bar would draw it.
func rowSnapshot(width int) [][]term.Cell {
	const text = "Working…"
	row := make([]term.Cell, width)
	runes := []rune(text)
	for x := range width {
		ch := ' '
		if x < len(runes) {
			ch = runes[x]
		}
		row[x] = term.Cell{
			Ch: ch, Width: 1, Fg: testAttr.Fg, Bg: testAttr.Bg,
		}
	}
	return [][]term.Cell{row}
}

func cloneCells(src [][]term.Cell) [][]term.Cell {
	out := make([][]term.Cell, len(src))
	for y, row := range src {
		out[y] = make([]term.Cell, len(row))
		copy(out[y], row)
	}
	return out
}

func cellsSignature(cells [][]term.Cell) string {
	var b strings.Builder
	for _, row := range cells {
		for _, c := range row {
			fmt.Fprintf(&b, "%d/%d/%d,", c.Ch, int(c.Fg), int(c.Bg))
		}
	}
	return b.String()
}

func TestNamesAllAnimate(t *testing.T) {
	frames := loopFrames(DefaultFPS, DefaultLoop)
	// The grid is kept narrow because the simulation effects cost time
	// proportional to it, and a single column already exposes a phase
	// that never advances.
	snapshot := rowSnapshot(8)
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			sh, ok := New(name, testAttr, Params{}, DefaultLoop)
			require.True(t, ok)
			seen := make(map[string]struct{})
			// Effects that open on a still phase need most of the loop
			// to show movement, while the simulation effects cost time
			// proportional to the frame index. Stopping at the first
			// change serves both: only an effect that never moves pays
			// for the whole sweep.
			for frame := range frames {
				cells := cloneCells(snapshot)
				sh.Shade(frame, total, cells)
				seen[cellsSignature(cells)] = struct{}{}
				if len(seen) > 1 {
					break
				}
			}
			assert.Greater(t, len(seen), 1,
				"effect paints identical cells on every frame of a loop")
		})
	}
}

func TestFireDoesNotRepeatEachLoop(t *testing.T) {
	frames := loopFrames(DefaultFPS, DefaultLoop)
	snapshot := rowSnapshot(8)
	for _, name := range []string{"blaze", "inferno"} {
		t.Run(name, func(t *testing.T) {
			sh, ok := New(name, testAttr, Params{}, DefaultLoop)
			require.True(t, ok)
			for _, frame := range []int{4, 4 + frames/2} {
				first, next := cloneCells(snapshot), cloneCells(snapshot)
				sh.Shade(frame, total, first)
				sh.Shade(frame+frames, total, next)
				assert.NotEqual(t, cellsSignature(first), cellsSignature(next),
					"the fire must not jump back to frame %d a loop later", frame)
			}
		})
	}
}

func TestLoopsAreSeamless(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			sh, ok := New(name, testAttr, Params{}, DefaultLoop)
			require.True(t, ok)
			shade := func(frame int) string {
				cells := rowSnapshot(8)
				sh.Shade(frame, total, cells)
				return cellsSignature(cells)
			}
			start := []string{shade(0), shade(1), shade(2)}
			var before [][]term.Cell
			prev := rowSnapshot(8)
			sh.Shade(0, total, prev)
			for frame := 1; frame < 4000; frame++ {
				cur := rowSnapshot(8)
				sh.Shade(frame, total, cur)
				if frame > 2 && cellsSignature(cur) == start[0] &&
					shade(frame+1) == start[1] && shade(frame+2) == start[2] {
					next := rowSnapshot(8)
					sh.Shade(frame+1, total, next)
					near := max(shadertest.Distance(before, prev), shadertest.Distance(cur, next))
					assert.LessOrEqual(t, 2*shadertest.Distance(prev, cur), 3*near,
						"the step back to the start, at frame %d, stands out from the steps either side of it", frame)
					return
				}
				before, prev = prev, cur
			}
			assert.Fail(t, "the effect never repeats")
		})
	}
}

func TestPulseIsSubtle(t *testing.T) {
	navy := term.NewRGBColor(0, 0, 128)
	sh, ok := New("pulse", term.Attributes{}, Params{}, DefaultLoop)
	require.True(t, ok)

	cells := [][]term.Cell{{{Ch: 'x', Width: 1, Fg: navy, Bg: navy}}}
	peak := loopFrames(DefaultFPS, DefaultLoop) / 2
	sh.Shade(peak, total, cells)

	r, _, b := cells[0][0].Bg.RGB()
	assert.Greater(t, r, int32(0), "the pulse must still be visible")
	// Under a third of the way to white; the stock 0.6 lands on 153.
	assert.LessOrEqual(t, r, int32(80), "the pulse must stay subtle")
	assert.Greater(t, b, r, "navy must stay blue at the peak")
}

func TestColorReplacesBlendTarget(t *testing.T) {
	black := term.NewRGBColor(0, 0, 0)
	green := term.NewRGBColor(0, 255, 0)
	for _, name := range []string{"pulse", "shine"} {
		for _, tc := range []struct {
			desc  string
			color term.Color
			hue   func(r, g, b int32) bool
		}{
			{"default blends toward white", term.ColorDefault, func(r, g, b int32) bool {
				return r > 0 && r == g && g == b
			}},
			{"color replaces white", green, func(r, g, b int32) bool {
				return r == 0 && g > 0 && b == 0
			}},
		} {
			t.Run(name+"/"+tc.desc, func(t *testing.T) {
				sh, ok := New(name, term.Attributes{}, tuning(name, tc.color, 0, 0), DefaultLoop)
				require.True(t, ok)
				glinted := 0
				for frame := range loopFrames(DefaultFPS, DefaultLoop) {
					row := make([]term.Cell, 8)
					for x := range row {
						row[x] = term.Cell{Ch: 'x', Width: 1, Fg: black, Bg: black}
					}
					sh.Shade(frame, total, [][]term.Cell{row})
					for _, c := range row {
						if c.Fg == black {
							continue
						}
						glinted++
						r, g, b := c.Fg.RGB()
						assert.Truef(t, tc.hue(r, g, b), "frame %d: rgb(%d, %d, %d)", frame, r, g, b)
					}
				}
				assert.Positive(t, glinted, "the effect must be visible")
			})
		}
	}
}

// tuning returns Params that tune only the effect called name.
func tuning(name string, color term.Color, intensity, bandWidth float64) Params {
	switch name {
	case "pulse":
		return Params{Pulse: PulseParams{Color: color, Intensity: intensity}}
	case "shine":
		return Params{Shine: ShineParams{Color: color, Intensity: intensity, BandWidth: bandWidth}}
	}
	return Params{}
}

// sweepGreen runs a loop of name over a black row with green as the
// target, and returns the brightest green any cell reached and the mean
// number of cells tinted per frame.
func sweepGreen(t *testing.T, name string, intensity, bandWidth float64) (peak int32, tinted float64) {
	t.Helper()
	black := term.NewRGBColor(0, 0, 0)
	p := tuning(name, term.NewRGBColor(0, 255, 0), intensity, bandWidth)
	sh, ok := New(name, term.Attributes{}, p, DefaultLoop)
	require.True(t, ok)
	frames := loopFrames(DefaultFPS, DefaultLoop)
	count := 0
	for frame := range frames {
		row := make([]term.Cell, 20)
		for x := range row {
			row[x] = term.Cell{Ch: 'x', Width: 1, Fg: black, Bg: black}
		}
		sh.Shade(frame, total, [][]term.Cell{row})
		for _, c := range row {
			if c.Fg == black {
				continue
			}
			count++
			_, g, _ := c.Fg.RGB()
			peak = max(peak, g)
		}
	}
	return peak, float64(count) / float64(frames)
}

func TestParamsIntensity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		catalog  float64
		strength float64
	}{
		{"pulse", pulseIntensity, 0.9},
		{"shine", shineIntensity, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			subtle, _ := sweepGreen(t, tc.name, 0, 0)
			assert.LessOrEqual(t, subtle, int32(tc.catalog*255)+1,
				"zero keeps the catalog's subtle default")
			strong, _ := sweepGreen(t, tc.name, tc.strength, 0)
			assert.GreaterOrEqual(t, strong, int32(200), "the text reaches the colour")
			out, _ := sweepGreen(t, tc.name, 1.5, 0)
			assert.Equal(t, subtle, out, "an out of range intensity keeps the default")
		})
	}
}

func TestParamsBandWidth(t *testing.T) {
	_, wide := sweepGreen(t, "shine", 0, 0)
	_, narrow := sweepGreen(t, "shine", 0, 0.2)
	assert.Less(t, narrow, wide/2, "a narrow band tints fewer cells at once")
	_, outOfRange := sweepGreen(t, "shine", 0, 1.5)
	assert.Equal(t, wide, outOfRange, "an out of range band width keeps the default")
}

// shadeLoop runs one loop of name tuned with p, shading a fresh copy of
// row on every frame and handing it to visit.
func shadeLoop(t *testing.T, name string, p Params, row []term.Cell, visit func(frame int, row []term.Cell)) {
	t.Helper()
	sh, ok := New(name, testAttr, p, DefaultLoop)
	require.True(t, ok)
	for frame := range loopFrames(DefaultFPS, DefaultLoop) {
		cells := [][]term.Cell{slices.Clone(row)}
		sh.Shade(frame, total, cells)
		visit(frame, cells[0])
	}
}

func loopSignature(t *testing.T, name string, p Params) string {
	t.Helper()
	var b strings.Builder
	shadeLoop(t, name, p, rowSnapshot(8)[0], func(_ int, row []term.Cell) {
		b.WriteString(cellsSignature([][]term.Cell{row}))
	})
	return b.String()
}

// textRow is a label of width characters drawn in fg.
func textRow(width int, fg term.Color) []term.Cell {
	row := make([]term.Cell, width)
	for x := range row {
		row[x] = term.Cell{Ch: 'x', Width: 1, Fg: fg, Bg: testAttr.Bg}
	}
	return row
}

func TestParamsChangeTheirEffect(t *testing.T) {
	green := term.NewRGBColor(0, 255, 0)
	for _, tc := range []struct {
		name, knob string
		p          Params
	}{
		{"blaze", "animate_background", Params{Blaze: FireParams{AnimateBackground: true}}},
		{"blaze", "swap_red_blue", Params{Blaze: FireParams{SwapRedBlue: true}}},
		{"blaze", "color", Params{Blaze: FireParams{Color: green}}},
		{"burn", "animate_background", Params{Burn: BurnParams{AnimateBackground: true}}},
		{"burn", "gradient", Params{Burn: BurnParams{Gradient: []term.Color{green}}}},
		{"burn", "duration", Params{Burn: BurnParams{Duration: 0.5}}},
		{"burn", "seed", Params{Burn: BurnParams{Seed: 7}}},
		{"burn", "origin", Params{Burn: BurnParams{FixedOrigin: true, Origin: 1}}},
		{"inferno", "animate_background", Params{Inferno: FireParams{AnimateBackground: true}}},
		{"inferno", "swap_red_blue", Params{Inferno: FireParams{SwapRedBlue: true}}},
		{"inferno", "color", Params{Inferno: FireParams{Color: green}}},
		{"pulse", "color", Params{Pulse: PulseParams{Color: green}}},
		{"pulse", "intensity", Params{Pulse: PulseParams{Intensity: 1}}},
		{"shine", "color", Params{Shine: ShineParams{Color: green}}},
		{"shine", "intensity", Params{Shine: ShineParams{Intensity: 1}}},
		{"shine", "band_width", Params{Shine: ShineParams{BandWidth: 0.2}}},
		{"shine", "direction", Params{Shine: ShineParams{Direction: glslshader.DirectionRightToLeft}}},
	} {
		t.Run(tc.name+"/"+tc.knob, func(t *testing.T) {
			assert.NotEqual(t, loopSignature(t, tc.name, Params{}), loopSignature(t, tc.name, tc.p))
		})
	}
}

func TestParamsTuneOnlyTheirEffect(t *testing.T) {
	green := term.NewRGBColor(0, 255, 0)
	fire := FireParams{AnimateBackground: true, Color: green, SwapRedBlue: true}
	every := Params{
		Blaze: fire,
		Burn: BurnParams{
			AnimateBackground: true, Gradient: []term.Color{green},
			Duration: 0.5, Seed: 7, FixedOrigin: true, Origin: 1,
		},
		Inferno: fire,
		Pulse:   PulseParams{Color: green, Intensity: 1},
		Shine: ShineParams{
			Color: green, Intensity: 1, BandWidth: 0.2,
			Direction: glslshader.DirectionRightToLeft,
		},
	}
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			others := every
			switch name {
			case "blaze":
				others.Blaze = FireParams{}
			case "burn":
				others.Burn = BurnParams{}
			case "inferno":
				others.Inferno = FireParams{}
			case "pulse":
				others.Pulse = PulseParams{}
			case "shine":
				others.Shine = ShineParams{}
			default:
				require.Failf(t, "untested effect", "%s has no Params entry", name)
			}
			assert.Equal(t, loopSignature(t, name, Params{}), loopSignature(t, name, others))
		})
	}
}

func TestDefaultParamsPaintAsZero(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, loopSignature(t, name, Params{}), loopSignature(t, name, DefaultParams()))
		})
	}
}

func TestAnimateBackground(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    Params
	}{
		{"blaze", Params{Blaze: FireParams{AnimateBackground: true}}},
		{"burn", Params{Burn: BurnParams{AnimateBackground: true}}},
		{"inferno", Params{Inferno: FireParams{AnimateBackground: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := rowSnapshot(8)[0]
			beyondText := func(p Params) bool {
				changed := false
				shadeLoop(t, tc.name, p, row, func(_ int, got []term.Cell) {
					for x, c := range got {
						changed = changed || c.Ch != row[x].Ch || c.Bg != row[x].Bg
					}
				})
				return changed
			}
			assert.False(t, beyondText(Params{}), "by default only the text colour is animated")
			assert.True(t, beyondText(tc.p), "the background or the glyphs are animated")
		})
	}
}

func TestSwapRedBlue(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    Params
	}{
		{"blaze", Params{Blaze: FireParams{SwapRedBlue: true}}},
		{"inferno", Params{Inferno: FireParams{SwapRedBlue: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := rowSnapshot(8)[0]
			var red [][]term.Cell
			shadeLoop(t, tc.name, Params{}, row, func(_ int, got []term.Cell) {
				red = append(red, got)
			})
			shadeLoop(t, tc.name, tc.p, row, func(frame int, got []term.Cell) {
				for x, c := range got {
					r, g, b := red[frame][x].Fg.RGB()
					sr, sg, sb := c.Fg.RGB()
					assert.Equalf(t, [3]int32{b, g, r}, [3]int32{sr, sg, sb}, "frame %d cell %d", frame, x)
				}
			})
		})
	}
}

func TestShineDirection(t *testing.T) {
	row := textRow(20, term.NewRGBColor(0, 0, 0))
	sweep := func(d glslshader.Direction) [][]term.Cell {
		var frames [][]term.Cell
		shadeLoop(t, "shine", Params{Shine: ShineParams{Direction: d}}, row, func(_ int, got []term.Cell) {
			frames = append(frames, got)
		})
		return frames
	}
	ltr := sweep(glslshader.DirectionLeftToRight)
	rtl := sweep(glslshader.DirectionRightToLeft)
	for frame := range ltr {
		mirrored := slices.Clone(rtl[frame])
		slices.Reverse(mirrored)
		assert.Equalf(t, ltr[frame], mirrored, "frame %d", frame)
	}
	assert.NotEqual(t, ltr, rtl)
}

func TestBurnOrigin(t *testing.T) {
	black := term.NewRGBColor(0, 0, 0)
	for _, tc := range []struct {
		origin float64
		// ordered reports whether a cell igniting at frame a, left of one
		// igniting at frame b, is in the order the wave travels.
		ordered func(a, b int) bool
	}{
		{0, func(a, b int) bool { return a <= b }},
		{1, func(a, b int) bool { return a >= b }},
	} {
		t.Run(fmt.Sprint(tc.origin), func(t *testing.T) {
			ignited := make([]int, 20)
			for x := range ignited {
				ignited[x] = -1
			}
			p := Params{Burn: BurnParams{FixedOrigin: true, Origin: tc.origin}}
			shadeLoop(t, "burn", p, textRow(len(ignited), black), func(frame int, got []term.Cell) {
				for x, c := range got {
					if ignited[x] < 0 && c.Fg != black {
						ignited[x] = frame
					}
				}
			})
			var frames []int
			for _, f := range ignited {
				if f >= 0 {
					frames = append(frames, f)
				}
			}
			require.Greater(t, len(frames), len(ignited)/2, "the wave crosses the label: %v", ignited)
			for i := 1; i < len(frames); i++ {
				assert.Truef(t, tc.ordered(frames[i-1], frames[i]),
					"the wave travels away from the origin: %v", ignited)
			}
		})
	}
}

func TestBurnGradientAndDuration(t *testing.T) {
	black := term.NewRGBColor(0, 0, 0)
	green := term.NewRGBColor(0, 255, 0)
	burning := func(p Params) (colors map[term.Color]bool, perFrame float64) {
		colors = map[term.Color]bool{}
		count, frames := 0, 0
		shadeLoop(t, "burn", p, textRow(20, black), func(_ int, got []term.Cell) {
			frames++
			for _, c := range got {
				if c.Fg != black {
					colors[c.Fg] = true
					count++
				}
			}
		})
		return colors, float64(count) / float64(frames)
	}

	colors, _ := burning(Params{Burn: BurnParams{Gradient: []term.Color{green}}})
	assert.Equal(t, map[term.Color]bool{green: true}, colors, "a cell burns through the gradient alone")

	_, wide := burning(Params{Burn: BurnParams{Duration: 0.5}})
	_, narrow := burning(Params{Burn: BurnParams{Duration: 0.05}})
	assert.Less(t, narrow, wide/2, "a short burn makes a narrow wave front")
}

func TestLoopChangesLoopedEffects(t *testing.T) {
	snapshot := rowSnapshot(8)
	var got []string
	for _, loop := range []time.Duration{DefaultLoop, 10 * DefaultLoop} {
		sh, ok := New("shine", term.Attributes{}, Params{}, loop)
		require.True(t, ok)
		cells := cloneCells(snapshot)
		sh.Shade(4, total, cells)
		got = append(got, cellsSignature(cells))
	}
	assert.NotEqual(t, got[0], got[1])
}
