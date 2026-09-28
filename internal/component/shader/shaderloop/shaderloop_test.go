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
)

var testAttr = term.Attributes{Fg: term.ColorSilver, Bg: term.ColorGray}

// total is the frame budget shader.Component runs these effects against.
var total = int(Duration / (time.Second / DefaultFPS))

// Every name the catalog lists must resolve, or a valid config would
// silently leave its target unshaded.
func TestNamesAllResolve(t *testing.T) {
	require.NotEmpty(t, Names())
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			sh, ok := New(name, term.Attributes{}, DefaultFPS, DefaultLoop)
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
		_, ok := New(name, term.Attributes{}, DefaultFPS, DefaultLoop)
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

// An effect that builds but paints the same cells on every frame is
// indistinguishable from no effect at all. timeshader.Loop advances the
// inner shader in strides of Duration/loop, so an effect keyed off
// absolute frame counts rather than against total can alias to a single
// phase and silently stop animating.
func TestNamesAllAnimate(t *testing.T) {
	frames := loopFrames(DefaultFPS, DefaultLoop)
	// The grid is kept narrow because the simulation effects cost time
	// proportional to it, and a single column already exposes a phase
	// that never advances.
	snapshot := rowSnapshot(8)
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			sh, ok := New(name, testAttr, DefaultFPS, DefaultLoop)
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

// blaze, inferno, noise and trippy run their clock off the frame index
// alone and never read total. timeshader.Loop replays the whole
// Duration inside one loop window, which steps their clock by thousands
// of seconds per drawn frame and makes them a blur, so they have to be
// returned unwrapped. A shader that ignores total paints the same cells
// whatever frame budget it is handed.
func TestTimeContinuousShadersIgnoreTotal(t *testing.T) {
	// Past one loop window, so a Loop wrapper would fold the frame
	// against a different framesPerLoop for each budget.
	frame := loopFrames(DefaultFPS, DefaultLoop) + 4
	snapshot := rowSnapshot(8)
	for _, name := range []string{"blaze", "inferno", "noise", "trippy"} {
		t.Run(name, func(t *testing.T) {
			var got []string
			for _, budget := range []int{total, 2 * total} {
				sh, ok := New(name, testAttr, DefaultFPS, DefaultLoop)
				require.True(t, ok)
				cells := cloneCells(snapshot)
				sh.Shade(frame, budget, cells)
				got = append(got, cellsSignature(cells))
			}
			assert.Equal(t, got[0], got[1],
				"effect is time-continuous and must not be wrapped in Loop")
		})
	}
}

// The UI these effects run over carries meaning in colour, so pulse
// must only brighten it. At the stock intensity its peak washed a navy
// cell most of the way to white and read as a different colour.
func TestPulseIsSubtle(t *testing.T) {
	navy := term.NewRGBColor(0, 0, 128)
	sh, ok := New("pulse", term.Attributes{}, DefaultFPS, DefaultLoop)
	require.True(t, ok)

	cells := [][]term.Cell{{{Ch: 'x', Width: 1, Fg: navy, Bg: navy}}}
	peak := loopFrames(DefaultFPS, DefaultLoop) / 2
	sh.Shade(peak, 1, cells)

	r, _, b := cells[0][0].Bg.RGB()
	assert.Greater(t, r, int32(0), "the pulse must still be visible")
	// Under a third of the way to white; the stock 0.6 lands on 153.
	assert.LessOrEqual(t, r, int32(80), "the pulse must stay subtle")
	assert.Greater(t, b, r, "navy must stay blue at the peak")
}

// The loop knob sets how much of a looped effect's animation is replayed
// per window, so widening it must change what the effect paints.
func TestLoopChangesLoopedEffects(t *testing.T) {
	snapshot := rowSnapshot(8)
	var got []string
	for _, loop := range []time.Duration{DefaultLoop, 10 * DefaultLoop} {
		sh, ok := New("shine", term.Attributes{}, DefaultFPS, loop)
		require.True(t, ok)
		cells := cloneCells(snapshot)
		sh.Shade(4, total, cells)
		got = append(got, cellsSignature(cells))
	}
	assert.NotEqual(t, got[0], got[1])
}
