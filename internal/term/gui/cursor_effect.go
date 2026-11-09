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
	_ "embed"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"image/color"
	"strconv"
	"time"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// CursorTrailShader is the Kage source of the built-in cursor effect, a
// flame that trails the cursor when it jumps.
//
//go:embed cursor_trail.kage
var CursorTrailShader []byte

// cursorEffectFrameInterval paces the frames of a playing effect. The GUI
// runs with vsync off, so without pacing it would redraw as fast as the
// GPU allows.
const cursorEffectFrameInterval = time.Second / 120

var cursorEffectCopy = ebiten.DrawImageOptions{Blend: ebiten.BlendCopy}

// cursorEffectUniformTypes are the uniforms a cursor effect shader may
// declare, by name, with the Kage type each must have.
var cursorEffectUniformTypes = map[string]string{
	"Time":           "float",
	"Duration":       "float",
	"Cursor":         "vec4",
	"PreviousCursor": "vec4",
	"CursorColor":    "vec4",
}

// The jump thresholds of shaders that declare none. Moves made by typing,
// pressing Enter or wrapping a line fall below them, so typing never pays
// for the full-window passes of a playing effect.
const (
	defaultMinJumpRows    = 2
	defaultMinJumpColumns = 9
)

// jumpThreshold is how far, in cells, the cursor must move for a cursor
// effect to play: at least rows rows, or columns columns along one row.
// Shaders set it with the MinJumpRows and MinJumpColumns constants.
type jumpThreshold struct {
	rows, columns int
}

func (j jumpThreshold) isJump(from, to term.Coordinates) bool {
	rows := max(to.Y-from.Y, from.Y-to.Y)
	return rows >= j.rows || rows == 0 && max(to.X-from.X, from.X-to.X) >= j.columns
}

// cursorEffect plays a shader over the whole window for a while after the
// cursor jumps.
type cursorEffect struct {
	shader   *ebiten.Shader
	jump     jumpThreshold
	duration time.Duration
	now      func() time.Time
	// wake requests a frame; it is called off the event loop.
	wake  func()
	timer *time.Timer

	// cell and bounds are where the cursor was the last time it was shown.
	cell    term.Coordinates
	bounds  [4]float32
	hasLast bool
	// jumpedAt is zero once the effect has played out, so frames of an
	// idle effect do not read the clock.
	jumpedAt time.Time
	// onScreen reports whether the last frame painted the effect, which
	// then owes another frame: to animate it or to erase it.
	onScreen bool
	// uniforms backs the slices in opts.Uniforms, which ebiten converts
	// when the effect is drawn, so playing a frame boxes no values.
	uniforms struct {
		time, duration                      [1]float32
		cursor, previousCursor, cursorColor [4]float32
	}
	opts ebiten.DrawRectShaderOptions
	// window is the copy of the screen the shader reads, as it cannot read
	// the image it draws onto.
	window *ebiten.Image
}

// compileCursorEffect compiles src and returns the moves it plays on,
// rejecting shaders that declare one of cursorEffectUniformTypes with
// another type, which ebiten would only report by panicking at draw time,
// and jump constants that are not whole numbers of at least 1.
func compileCursorEffect(src []byte) (*ebiten.Shader, jumpThreshold, error) {
	jump := jumpThreshold{rows: defaultMinJumpRows, columns: defaultMinJumpColumns}
	shader, err := ebiten.NewShader(src)
	if err != nil {
		return nil, jump, err
	}
	// It compiled, so it is valid Go syntax.
	f, err := parser.ParseFile(token.NewFileSet(), "", src, parser.SkipObjectResolution)
	if err != nil {
		return nil, jump, err
	}
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR && gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if gen.Tok == token.CONST {
					switch name.Name {
					case "MinJumpRows":
						jump.rows, err = jumpConstant(vs, i)
					case "MinJumpColumns":
						jump.columns, err = jumpConstant(vs, i)
					}
					if err != nil {
						return nil, jump, err
					}
					continue
				}
				want, ok := cursorEffectUniformTypes[name.Name]
				if !ok {
					continue
				}
				if typ, ok := vs.Type.(*ast.Ident); !ok || typ.Name != want {
					return nil, jump, fmt.Errorf("uniform %s must be a %s", name.Name, want)
				}
			}
		}
	}
	return shader, jump, nil
}

// jumpConstant returns the value of the i-th constant of vs, which only
// an integer literal can set: Rune cannot evaluate Kage expressions.
func jumpConstant(vs *ast.ValueSpec, i int) (int, error) {
	invalid := fmt.Errorf("%s must be a whole number of at least 1", vs.Names[i].Name)
	if typ, ok := vs.Type.(*ast.Ident); vs.Type != nil && (!ok || typ.Name != "int") {
		return 0, invalid
	}
	if len(vs.Values) != len(vs.Names) {
		return 0, invalid
	}
	lit, ok := vs.Values[i].(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return 0, invalid
	}
	n, err := strconv.ParseInt(lit.Value, 0, 0)
	if err != nil || n < 1 {
		return 0, invalid
	}
	return int(n), nil
}

// reset forgets where the cursor was, so a change of cell geometry is
// not mistaken for a jump, and releases the copy of the window.
func (e *cursorEffect) reset() {
	e.hasLast = false
	e.jumpedAt = time.Time{}
	e.onScreen = false
	e.releaseWindow()
}

func (e *cursorEffect) releaseWindow() {
	if e.window != nil {
		e.window.Deallocate()
		e.window = nil
	}
}

// enabled reports whether draw must be called every frame.
func (e *cursorEffect) enabled() bool {
	return e != nil && e.shader != nil
}

// draw records the cursor drawn this frame, at bounds given as x, y,
// width and height in pixels, and while the effect plays replaces screen
// with what the shader makes of it. Moves smaller than a jump keep a
// playing effect going without restarting it. Jumps while the window is
// unfocused are tracked but not played.
func (e *cursorEffect) draw(
	screen *ebiten.Image, cursor cursorState, bounds [4]float32, focused bool,
	cursorColor color.RGBA,
) {
	e.onScreen = false
	if !cursor.show {
		return
	}
	if e.hasLast && focused && e.jump.isJump(e.cell, cursor.pos) {
		e.uniforms.previousCursor, e.jumpedAt = e.bounds, e.now()
	}
	e.cell, e.bounds, e.hasLast = cursor.pos, bounds, true
	if !focused || e.jumpedAt.IsZero() {
		return
	}
	elapsed := e.now().Sub(e.jumpedAt)
	if elapsed >= e.duration {
		e.jumpedAt = time.Time{}
		return
	}

	u := &e.uniforms
	if e.opts.Uniforms == nil {
		e.opts.Uniforms = map[string]any{
			"Time":           u.time[:],
			"Duration":       u.duration[:],
			"Cursor":         u.cursor[:],
			"PreviousCursor": u.previousCursor[:],
			"CursorColor":    u.cursorColor[:],
		}
	}
	u.time[0] = float32(elapsed.Seconds())
	u.duration[0] = float32(e.duration.Seconds())
	u.cursor = bounds
	u.cursorColor = [4]float32{
		float32(cursorColor.R) / 0xff, float32(cursorColor.G) / 0xff,
		float32(cursorColor.B) / 0xff, float32(cursorColor.A) / 0xff,
	}
	b := screen.Bounds()
	if e.window == nil || e.window.Bounds().Size() != b.Size() {
		e.releaseWindow()
		e.window = ebiten.NewImage(b.Dx(), b.Dy())
	}
	e.window.DrawImage(screen, &cursorEffectCopy)
	e.opts.Images[0] = e.window
	e.opts.Blend = ebiten.BlendCopy
	screen.DrawRectShader(b.Dx(), b.Dy(), e.shader, &e.opts)
	e.onScreen = true
	e.scheduleFrame()
}

func (e *cursorEffect) scheduleFrame() {
	if e.timer == nil {
		e.timer = time.AfterFunc(cursorEffectFrameInterval, e.wake)
		return
	}
	e.timer.Reset(cursorEffectFrameInterval)
}

// needsFrame reports whether the screen must be repainted even though its
// content did not change.
func (e *cursorEffect) needsFrame() bool {
	return e.onScreen
}

func (e *cursorEffect) close() {
	if e.timer != nil {
		e.timer.Stop()
	}
	e.reset()
}
