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

package gui

import (
	"image/color"
	"sync/atomic"
	"testing"
	"time"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/benchdraw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
)

const testCursorShader = `//kage:unit pixels

package main

var Time float
var Cursor vec4
var Other vec2

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	return vec4(Time) + Cursor + vec4(Other, 0, 0)
}
`

func jumpShader(decl string) string {
	return "//kage:unit pixels\n\npackage main\n\n" + decl +
		"\n\nfunc Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {\n\treturn vec4(0)\n}\n"
}

func TestCompileCursorEffect(t *testing.T) {
	defaults := jumpThreshold{rows: defaultMinJumpRows, columns: defaultMinJumpColumns}
	const rowsErr = "MinJumpRows must be a whole number of at least 1"
	const columnsErr = "MinJumpColumns must be a whole number of at least 1"
	tests := []struct {
		name     string
		src      string
		wantJump jumpThreshold
		wantErr  string
	}{
		{name: "built-in trail", src: string(CursorTrailShader), wantJump: defaults},
		{
			name:     "subset of uniforms and uniforms of its own",
			src:      testCursorShader,
			wantJump: defaults,
		},
		{
			name:     "jump constants",
			src:      jumpShader("const MinJumpRows = 1\nconst MinJumpColumns = 3"),
			wantJump: jumpThreshold{rows: 1, columns: 3},
		},
		{
			name:     "one jump constant keeps the default of the other",
			src:      jumpShader("const MinJumpColumns = 1"),
			wantJump: jumpThreshold{rows: defaultMinJumpRows, columns: 1},
		},
		{
			name:     "typed and grouped jump constants",
			src:      jumpShader("const (\n\tMinJumpRows int = 4\n\tMinJumpColumns = 0x10\n)"),
			wantJump: jumpThreshold{rows: 4, columns: 16},
		},
		{
			name:     "jump constants in one declaration",
			src:      jumpShader("const MinJumpRows, MinJumpColumns = 3, 5"),
			wantJump: jumpThreshold{rows: 3, columns: 5},
		},
		{name: "zero", src: jumpShader("const MinJumpRows = 0"), wantErr: rowsErr},
		{name: "negative", src: jumpShader("const MinJumpColumns = -1"), wantErr: columnsErr},
		{name: "fraction", src: jumpShader("const MinJumpRows = 1.5"), wantErr: rowsErr},
		{name: "expression", src: jumpShader("const MinJumpRows = 1 + 1"), wantErr: rowsErr},
		{name: "float type", src: jumpShader("const MinJumpColumns float = 2"), wantErr: columnsErr},
		{
			name:    "not kage",
			src:     "package main\n\nfunc Fragment() {",
			wantErr: "expected",
		},
		{
			name: "uniform of another type",
			src: `package main

var Cursor vec2

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	return vec4(Cursor, 0, 0)
}
`,
			wantErr: "uniform Cursor must be a vec4",
		},
		{
			name: "grouped uniform of another type",
			src: `package main

var (
	Cursor   vec4
	Time, Duration int
)

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	return Cursor + vec4(float(Time+Duration))
}
`,
			wantErr: "uniform Time must be a float",
		},
		{
			name: "array uniform",
			src: `package main

var CursorColor [1]vec4

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	return CursorColor[0]
}
`,
			wantErr: "uniform CursorColor must be a vec4",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shader, jump, err := compileCursorEffect([]byte(tt.src))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Nil(t, shader)
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, shader)
			assert.Equal(t, tt.wantJump, jump)
		})
	}
}

func TestJumpThresholdIsJump(t *testing.T) {
	defaults := jumpThreshold{rows: defaultMinJumpRows, columns: defaultMinJumpColumns}
	everyKey := jumpThreshold{rows: 1, columns: 1}
	tests := []struct {
		name     string
		jump     jumpThreshold
		from, to term.Coordinates
		want     bool
	}{
		{name: "typing", jump: defaults, from: term.Coordinates{X: 3, Y: 4}, to: term.Coordinates{X: 4, Y: 4}},
		{name: "backspace", jump: defaults, from: term.Coordinates{X: 4, Y: 4}, to: term.Coordinates{X: 3, Y: 4}},
		{name: "a tab", jump: defaults, from: term.Coordinates{Y: 4}, to: term.Coordinates{X: 8, Y: 4}},
		{
			name: "along a row", jump: defaults,
			from: term.Coordinates{Y: 4}, to: term.Coordinates{X: 9, Y: 4}, want: true,
		},
		{
			name: "back along a row", jump: defaults,
			from: term.Coordinates{X: 20, Y: 4}, to: term.Coordinates{X: 11, Y: 4}, want: true,
		},
		{name: "enter", jump: defaults, from: term.Coordinates{X: 60, Y: 4}, to: term.Coordinates{X: 4, Y: 5}},
		{name: "a wrapped line", jump: defaults, from: term.Coordinates{X: 79, Y: 4}, to: term.Coordinates{Y: 5}},
		{name: "a line up", jump: defaults, from: term.Coordinates{X: 10, Y: 5}, to: term.Coordinates{X: 10, Y: 4}},
		{
			name: "two rows down", jump: defaults,
			from: term.Coordinates{X: 10, Y: 4}, to: term.Coordinates{X: 10, Y: 6}, want: true,
		},
		{
			name: "two rows up", jump: defaults,
			from: term.Coordinates{X: 10, Y: 6}, to: term.Coordinates{X: 10, Y: 4}, want: true,
		},
		{name: "nowhere", jump: defaults, from: term.Coordinates{X: 1, Y: 1}, to: term.Coordinates{X: 1, Y: 1}},
		{
			name: "typing, every key", jump: everyKey,
			from: term.Coordinates{X: 3, Y: 4}, to: term.Coordinates{X: 4, Y: 4}, want: true,
		},
		{
			name: "enter, every key", jump: everyKey,
			from: term.Coordinates{X: 60, Y: 4}, to: term.Coordinates{X: 4, Y: 5}, want: true,
		},
		{name: "nowhere, every key", jump: everyKey, from: term.Coordinates{X: 1, Y: 1}, to: term.Coordinates{X: 1, Y: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.jump.isJump(tt.from, tt.to))
		})
	}
}

func TestCursorEffectDraw(t *testing.T) {
	type frame struct {
		cell    term.Coordinates
		bar     bool
		hidden  bool
		blurred bool
		advance time.Duration
		want    bool
	}
	a := term.Coordinates{}
	b := term.Coordinates{X: 10, Y: 10}
	c := term.Coordinates{Y: 20}
	right := func(p term.Coordinates, n int) term.Coordinates {
		return term.Coordinates{X: p.X + n, Y: p.Y}
	}
	tests := []struct {
		name     string
		everyKey bool
		frames   []frame
	}{
		{
			name:   "nothing to trail from on the first frame",
			frames: []frame{{cell: a}},
		},
		{
			name:   "still cursor",
			frames: []frame{{cell: a}, {cell: a}},
		},
		{
			name: "typing plays nothing",
			frames: []frame{
				{cell: a},
				{cell: right(a, 1)},
				{cell: right(a, 2)},
				{cell: term.Coordinates{X: 4, Y: 1}},
				{cell: term.Coordinates{X: 3, Y: 1}},
			},
		},
		{
			name:   "changing the cursor shape is not a move",
			frames: []frame{{cell: a}, {cell: a, bar: true}},
		},
		{
			name: "plays for its duration after a jump",
			frames: []frame{
				{cell: a},
				{cell: b, want: true},
				{cell: b, advance: 99 * time.Millisecond, want: true},
				{cell: b, advance: time.Millisecond},
			},
		},
		{
			name: "a jump restarts it",
			frames: []frame{
				{cell: a},
				{cell: b, want: true},
				{cell: c, advance: 99 * time.Millisecond, want: true},
				{cell: c, advance: 99 * time.Millisecond, want: true},
			},
		},
		{
			name: "smaller moves keep it playing without restarting it",
			frames: []frame{
				{cell: a},
				{cell: b, want: true},
				{cell: right(b, 1), advance: 99 * time.Millisecond, want: true},
				{cell: right(b, 1), advance: time.Millisecond},
			},
		},
		{
			name: "hiding the cursor stops it",
			frames: []frame{
				{cell: a},
				{cell: b, want: true},
				{hidden: true},
			},
		},
		{
			name: "trails from where the cursor was last shown",
			frames: []frame{
				{cell: a},
				{hidden: true, advance: time.Second},
				{cell: b, want: true},
			},
		},
		{
			name: "moves while unfocused are tracked but not played",
			frames: []frame{
				{cell: a},
				{cell: b, blurred: true},
				{cell: b},
			},
		},
		{
			name: "losing focus stops it",
			frames: []frame{
				{cell: a},
				{cell: b, want: true},
				{cell: b, blurred: true},
			},
		},
		{
			name:     "a shader can play on every keystroke",
			everyKey: true,
			frames: []frame{
				{cell: a},
				{cell: right(a, 1), want: true},
				{cell: right(a, 2), advance: 99 * time.Millisecond, want: true},
				{cell: right(a, 2), advance: 99 * time.Millisecond, want: true},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, now, _ := newTestCursorEffect(t, 100*time.Millisecond)
			if tt.everyKey {
				e.jump = jumpThreshold{rows: 1, columns: 1}
			}
			screen := ebiten.NewImage(100, 400)
			t.Cleanup(screen.Deallocate)
			for i, f := range tt.frames {
				*now = now.Add(f.advance)
				benchdraw.BeginFrame(t)
				e.draw(screen, cursorState{pos: f.cell, show: !f.hidden},
					testCursorBounds(f.cell, f.bar), !f.blurred, color.RGBA{R: 255, A: 255})
				benchdraw.EndFrame(t)
				assert.Equalf(t, f.want, e.needsFrame(), "frame %d", i)
			}
		})
	}
}

func TestCursorEffectUniformsFollowTheCursor(t *testing.T) {
	e, now, _ := newTestCursorEffect(t, time.Second)
	screen := ebiten.NewImage(100, 400)
	t.Cleanup(screen.Deallocate)
	draw := func(cell term.Coordinates) {
		t.Helper()
		e.draw(screen, cursorState{pos: cell, show: true}, testCursorBounds(cell, false),
			true, color.RGBA{R: 255, A: 255})
		require.True(t, e.needsFrame())
	}
	a := term.Coordinates{}
	b := term.Coordinates{X: 10, Y: 10}
	typed := term.Coordinates{X: 11, Y: 10}
	c := term.Coordinates{Y: 20}

	benchdraw.BeginFrame(t)
	defer benchdraw.EndFrame(t)
	e.draw(screen, cursorState{pos: a, show: true}, testCursorBounds(a, false),
		true, color.RGBA{R: 255, A: 255})
	draw(b)
	assert.Equal(t, testCursorBounds(b, false), e.uniforms.cursor)
	assert.Equal(t, testCursorBounds(a, false), e.uniforms.previousCursor)
	assert.Equal(t, [4]float32{1, 0, 0, 1}, e.uniforms.cursorColor)
	assert.Equal(t, [1]float32{1}, e.uniforms.duration)

	*now = now.Add(10 * time.Millisecond)
	draw(typed)
	assert.Equal(t, testCursorBounds(typed, false), e.uniforms.cursor, "follows smaller moves")
	assert.Equal(t, testCursorBounds(a, false), e.uniforms.previousCursor,
		"smaller moves keep where it jumped from")
	assert.InDelta(t, 0.01, e.uniforms.time[0], 1e-6, "time since the jump")

	draw(c)
	assert.Equal(t, testCursorBounds(typed, false), e.uniforms.previousCursor,
		"jumps from where the cursor last was")
	assert.InDelta(t, 0, e.uniforms.time[0], 1e-6)
}

func TestCursorEffectSchedulesFramesWhilePlaying(t *testing.T) {
	e, _, wakes := newTestCursorEffect(t, time.Second)
	screen := ebiten.NewImage(100, 100)
	t.Cleanup(screen.Deallocate)

	benchdraw.BeginFrame(t)
	jump(e, screen)
	benchdraw.EndFrame(t)

	require.Eventually(t, func() bool { return wakes.Load() == 1 },
		time.Second, time.Millisecond)
}

func TestCursorEffectResetForgetsTheCursor(t *testing.T) {
	e, _, _ := newTestCursorEffect(t, time.Second)
	screen := ebiten.NewImage(100, 100)
	t.Cleanup(screen.Deallocate)

	benchdraw.BeginFrame(t)
	defer benchdraw.EndFrame(t)
	e.draw(screen, cursorState{show: true}, testCursorBounds(term.Coordinates{}, false),
		true, color.RGBA{})
	e.reset()
	far := term.Coordinates{Y: 20}
	e.draw(screen, cursorState{pos: far, show: true}, testCursorBounds(far, false),
		true, color.RGBA{})
	assert.False(t, e.needsFrame(), "a new grid is not a jump")
}

func TestCursorEffectReadsACopyOfTheWindow(t *testing.T) {
	e, _, _ := newTestCursorEffect(t, time.Second)
	move := func(screen *ebiten.Image) {
		t.Helper()
		e.reset()
		jump(e, screen)
		require.True(t, e.needsFrame())
	}
	small := ebiten.NewImage(100, 100)
	t.Cleanup(small.Deallocate)
	large := ebiten.NewImage(200, 150)
	t.Cleanup(large.Deallocate)

	benchdraw.BeginFrame(t)
	defer benchdraw.EndFrame(t)
	move(small)
	window := e.opts.Images[0]
	require.NotNil(t, window)
	assert.NotSame(t, small, window)
	assert.Equal(t, small.Bounds(), window.Bounds())

	move(large)
	assert.Equal(t, large.Bounds(), e.opts.Images[0].Bounds(), "follows the window size")

	e.reset()
	assert.Nil(t, e.window, "reset releases the copy")
}

// BenchmarkCursorEffectDraw reports, as played/op, the fraction of frames
// that run the shader, each of which costs two full-window GPU passes
// that the benchmark's no-op graphics driver does not time.
func BenchmarkCursorEffectDraw(b *testing.B) {
	for _, typing := range []bool{true, false} {
		name := "jumping"
		if typing {
			name = "typing"
		}
		b.Run(name, func(b *testing.B) {
			e, now, _ := newTestCursorEffect(b, 250*time.Millisecond)
			screen := ebiten.NewImage(1920, 1080)
			b.Cleanup(screen.Deallocate)
			played := 0
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// A keystroke every 100ms at 120 frames per second.
				*now = now.Add(time.Second / 120)
				cell := term.Coordinates{X: i / 12 % 80, Y: i / 12 / 80}
				if !typing {
					cell = term.Coordinates{Y: i / 12 % 2 * 20}
				}
				benchdraw.BeginFrame(b)
				e.draw(screen, cursorState{pos: cell, show: true}, testCursorBounds(cell, false),
					true, color.RGBA{R: 255, A: 255})
				benchdraw.EndFrame(b)
				if e.needsFrame() {
					played++
				}
			}
			b.ReportMetric(float64(played)/float64(b.N), "played/op")
		})
	}
}

func testCursorBounds(cell term.Coordinates, bar bool) [4]float32 {
	width := float32(8)
	if bar {
		width = 2
	}
	return [4]float32{float32(cell.X * 8), float32(cell.Y * 16), width, 16}
}

func jump(e *cursorEffect, screen *ebiten.Image) {
	for _, cell := range []term.Coordinates{{}, {X: 5, Y: 5}} {
		e.draw(screen, cursorState{pos: cell, show: true}, testCursorBounds(cell, false),
			true, color.RGBA{})
	}
}

func newTestCursorEffect(
	t testing.TB, duration time.Duration,
) (*cursorEffect, *time.Time, *atomic.Int32) {
	t.Helper()
	shader, jump, err := compileCursorEffect(CursorTrailShader)
	require.NoError(t, err)
	now := time.Unix(1, 0)
	wakes := new(atomic.Int32)
	e := &cursorEffect{
		shader:   shader,
		jump:     jump,
		duration: duration,
		now:      func() time.Time { return now },
		wake:     func() { wakes.Add(1) },
	}
	t.Cleanup(e.close)
	return e, &now, wakes
}
