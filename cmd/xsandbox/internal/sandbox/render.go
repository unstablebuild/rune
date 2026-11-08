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

package sandbox

import (
	"context"
	"image"
	"image/color"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/cmd/xsandbox/internal/spec"
)

// defaultRenderWidth and defaultRenderHeight size the headless browser
// component at startup. The size is fixed so window installs resize
// their new child synchronously, flushing the install-response
// handshake back to the extension; render() can override it per call.
const (
	defaultRenderWidth  = 80
	defaultRenderHeight = 24
)

// render resizes the headless browser component to width x height and
// draws it into an in-memory cell grid, returning the grid as a plain
// string (one line per row, trailing blanks trimmed). It composites
// every installed handler exactly as a real terminal would.
func (h *browserHost) render(width, height int) string {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.width, h.height = width, height
	w := term.NewStringWriter(width, height)
	h.comp.Resize(width, height)
	h.comp.Draw(w)
	if cur, _, show := h.comp.Cursor(); show {
		w.SetCursor(cur)
	}
	_ = w.Flush()
	return trimGrid(w.String())
}

// renderGraphics draws the browser as render does, in frames of
// cellWidth x cellHeight pixel cells, so that handlers can place images,
// and returns the grid and the images placed over it.
func (h *browserHost) renderGraphics(width, height int, cellWidth, cellHeight float64) spec.Graphics {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.width, h.height = width, height
	w := &graphicsWriter{
		StringWriter: term.NewStringWriter(width, height),
		ctx:          term.ContextWithCellPixelSize(context.Background(), cellWidth, cellHeight),
	}
	h.comp.Resize(width, height)
	h.comp.Draw(w)
	if cur, _, show := h.comp.Cursor(); show {
		w.SetCursor(cur)
	}
	_ = w.Flush()
	return spec.Graphics{Text: trimGrid(w.String()), Images: w.images}
}

// graphicsWriter is a string writer that tells the cell size and records
// the images placed over its cells.
type graphicsWriter struct {
	*term.StringWriter
	ctx    context.Context
	images []spec.PlacedImage
}

func (w *graphicsWriter) Context() context.Context { return w.ctx }

// DrawImage records img, whose pixels it reads now: they are only valid
// until the frame after.
func (w *graphicsWriter) DrawImage(img term.Image) bool {
	if img.Src == nil {
		return true
	}
	area := img.Src.Bounds()
	if !img.Crop.Empty() {
		area = img.Crop.Add(area.Min).Intersect(area)
	}
	w.images = append(w.images, spec.PlacedImage{
		X: img.Pos.X, Y: img.Pos.Y, Width: img.Width, Height: img.Height,
		PixelWidth: area.Dx(), PixelHeight: area.Dy(), Colors: colors(img.Src, area),
	})
	return true
}

// colors counts the distinct colors of a sample of the pixels of src in
// area.
func colors(src image.Image, area image.Rectangle) int {
	const step = 7
	seen := map[color.RGBA]bool{}
	for y := area.Min.Y; y < area.Max.Y; y += step {
		for x := area.Min.X; x < area.Max.X; x += step {
			seen[color.RGBAModel.Convert(src.At(x, y)).(color.RGBA)] = true
		}
	}
	return len(seen)
}

// renderResponsive draws c at width, as tall as it asks to be, and
// returns it as render does.
func renderResponsive(c component.Responsive, width int) string {
	height := max(c.Height(width), 0)
	c.Resize(width, height)
	w := term.NewStringWriter(width, height)
	c.Draw(w)
	_ = w.Flush()
	return trimGrid(w.String())
}

// sendKeys parses a handlertest-style key sequence and delivers each
// resulting key event to the component's focused handler, returning
// whether the last event was handled and whether it requested exit.
func (h *browserHost) sendKeys(sequence string) (handled, quit bool, err error) {
	keys, err := term.ParseKeys(sequence)
	if err != nil {
		return false, false, err
	}
	events := make([]term.Event, len(keys))
	for i, key := range keys {
		events[i] = term.Event{Ch: key.Ch, Mod: key.Mod, Key: key.Key, Type: term.EventKey}
	}
	handled, quit = h.sendEvents(events)
	return handled, quit, nil
}

// selection returns the selection of the component, which is that of
// the handler in focus.
func (h *browserHost) selection() (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.comp.Selection()
}

// sendEvents delivers each event to the component, returning whether the
// last was handled and whether it requested exit.
func (h *browserHost) sendEvents(events []term.Event) (handled, quit bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ev := range events {
		quit, handled = h.comp.Handle(ev)
	}
	return handled, quit
}

// trimGrid removes trailing whitespace from each row and trailing
// blank rows so rendered output is easy to assert against.
func trimGrid(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}
