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

// Package shaderdevicetest runs a shaderdevice.Device in tests and checks
// properties of shaders with it.
//
// A Harness draws on ebiten's benchdraw driver, which has no GPU: programs
// compile and run and uniforms are checked, but every pixel reads back
// transparent. Under Main, which e2e suites call from TestMain, Harnesses
// draw on the GPU and pixels are real.
package shaderdevicetest

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"os"
	"runtime"
	"sync/atomic"
	"testing"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/benchdraw"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component/gshader"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/gui/shaderdevice"
)

// ebiten runs its game on the main thread, where Main must be called.
func init() { runtime.LockOSThread() }

var gpu atomic.Bool

// Main runs m inside an ebiten game, so that Harnesses draw on the GPU,
// and exits with its result. Call it from TestMain. On a host without a
// display it runs m without a GPU, as tests do without Main.
func Main(m *testing.M) {
	if !hasDisplay() {
		fmt.Fprintln(os.Stderr, "shaderdevicetest: no display, drawing without a GPU")
		os.Exit(m.Run())
	}
	g := &game{m: m}
	ebiten.SetWindowSize(16, 16)
	ebiten.SetWindowTitle("shaderdevicetest")
	// Pixels read back wait for the next frame, which vsync would hold.
	ebiten.SetVsyncEnabled(false)
	err := ebiten.RunGameWithOptions(g, &ebiten.RunGameOptions{InitUnfocused: true, SkipTaskbar: true})
	if err != nil {
		fmt.Fprintln(os.Stderr, "shaderdevicetest:", err)
		os.Exit(1)
	}
	os.Exit(g.code)
}

func hasDisplay() bool {
	if runtime.GOOS != "linux" {
		return true
	}
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

// game runs the tests on a goroutine of their own once the game is up,
// as ebiten draws only while its game runs, and ends with them.
type game struct {
	m    *testing.M
	done chan int
	code int
}

func (g *game) Update() error {
	if g.done == nil {
		g.done = make(chan int, 1)
		gpu.Store(true)
		go debug.CapturePanicReport(func() { g.done <- g.m.Run() })
	}
	select {
	case g.code = <-g.done:
		return ebiten.Termination
	default:
		return nil
	}
}

func (g *game) Draw(*ebiten.Image) {}

func (g *game) Layout(int, int) (int, int) { return 16, 16 }

// GPU reports whether Harnesses draw on the GPU.
func GPU() bool { return gpu.Load() }

// RequireGPU skips t unless Harnesses draw on the GPU, for tests of
// pixels.
func RequireGPU(t testing.TB) {
	t.Helper()
	if !GPU() {
		t.Skip("reads pixels back from the GPU: run with -tags e2e")
	}
}

// Harness draws with a shaderdevice.Device.
type Harness struct {
	t testing.TB
	// Device is the device it draws with, closed when the test ends.
	Device *shaderdevice.Device
	// PixelSize and Theme are what draws begin with.
	PixelSize cell.PixelSize
	Theme     shaderdevice.Theme

	key term.ImageID
}

// New returns a Harness with a Device of its own, on cells of 4x8 pixels
// in a black theme. Its device renders a Snapshot as blocks of its cells'
// background colours, over the theme's background, without text or
// pictures.
func New(t testing.TB) *Harness {
	h := &Harness{
		t:         t,
		PixelSize: cell.PixelSize{Width: 4, Height: 8},
		Theme: shaderdevice.Theme{
			Background: color.RGBA{A: 0xff},
			Foreground: color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
		},
		key: term.NewImageID(),
	}
	h.Device = shaderdevice.New(h.renderCells)
	t.Cleanup(h.Device.Close)
	return h
}

func (h *Harness) renderCells(dst *ebiten.Image, cells [][]term.Cell, _ []term.Image) {
	dst.Fill(h.Theme.Background)
	for y, row := range cells {
		for x, c := range row {
			if c.Bg == term.ColorDefault {
				continue
			}
			r := h.PixelSize.Pixels(image.Rect(x, y, x+1, y+1))
			dst.SubImage(r).(*ebiten.Image).Fill(h.resolve(c.Bg))
		}
	}
}

// resolve resolves c as the device does.
func (h *Harness) resolve(c term.Color) color.RGBA {
	if h.Theme.Resolve != nil {
		return h.Theme.Resolve(c)
	}
	if !c.Valid() || c == term.ColorDefault {
		return h.Theme.Background
	}
	r, g, b := c.TrueColor().RGB()
	return color.RGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 0xff}
}

// Draw runs fn in a draw of h's Device, between its Begin and End.
func (h *Harness) Draw(fn func()) {
	h.t.Helper()
	if !GPU() {
		benchdraw.BeginFrame(h.t)
		defer benchdraw.EndFrame(h.t)
	}
	h.Device.Begin(h.PixelSize, h.Theme)
	defer h.Device.End()
	fn()
}

// Render renders sh at frame of total, as the shader of a generator over
// size cells, in a draw of its own, and returns the premultiplied pixels.
func (h *Harness) Render(sh gshader.Shader, frame, total int, size image.Point) (*image.RGBA, error) {
	h.t.Helper()
	return h.render(sh, frame, total, size, nil)
}

// RenderOver renders sh at frame of total over cells, as the shader of a
// component that draws them, in a draw of its own, and returns the
// premultiplied pixels.
func (h *Harness) RenderOver(sh gshader.Shader, frame, total int, cells [][]term.Cell) (*image.RGBA, error) {
	h.t.Helper()
	size := image.Pt(0, len(cells))
	if len(cells) > 0 {
		size.X = len(cells[0])
	}
	return h.render(sh, frame, total, size, &gshader.Snapshot{Cells: cells})
}

func (h *Harness) render(
	sh gshader.Shader, frame, total int, size image.Point, src *gshader.Snapshot,
) (*image.RGBA, error) {
	h.t.Helper()
	var (
		pixels *image.RGBA
		err    error
	)
	h.Draw(func() {
		var f gshader.Frame
		f, err = h.Device.Render(h.key, size, src, sh.Shade(frame, total, gshader.Source()))
		if err != nil {
			return
		}
		b := f.Src.Bounds()
		pixels = image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(pixels, pixels.Rect, f.Src, b.Min, draw.Src)
	})
	return pixels, err
}
