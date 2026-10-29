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

// Package quickmenu draws the quick menu, a column of buttons along the
// right edge of the window that run Rune commands, with an image for
// each button's idle, hover and pressed states.
package quickmenu

import (
	"context"
	"errors"
	"image"
	"log/slog"
	"math"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component/imageuri"
	"unstable.build/rune/internal/component/template"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/sounds"
)

var _ tui.Handler = (*Bar)(nil)

var tooltipAttr = term.Attributes{Attrs: term.AttrReverse}

// nominalCell stands in for the cell size until a writer tells it.
var nominalCell = cell.PixelSize{Width: 1, Height: 2}

// Button is one button of a Bar. Idle, Hover and Pressed are the URIs
// of its images.
type Button struct {
	// Title is shown next to the button while the pointer is over it,
	// and its first letter stands in for an image that cannot be drawn.
	Title                string
	Idle, Hover, Pressed string
}

// Sounds names the sounds a Bar plays; "" plays none.
type Sounds struct {
	// Hover plays when the pointer moves onto a button.
	Hover string
	// Click plays when a button is clicked, as Config.Activate is called.
	Click string
}

// Config configures a Bar.
type Config struct {
	Buttons []Button
	// Columns is the width of the column the buttons fill at the right
	// edge of the window, and Top the row the first one starts at.
	Columns, Top int
	// Storage caches the images, and Interrupter is signalled whenever
	// one settles or a title is due.
	Storage     storageapi.Service
	Interrupter term.Interrupter
	// TooltipDelay is how long the pointer must rest on a button before
	// its title is shown. Zero shows it at once.
	TooltipDelay time.Duration
	// Player plays Sounds. Nil plays none.
	Player sounds.Player
	Sounds Sounds
	// Activate is called with the index of a button when a press that
	// began on it is released over it.
	Activate func(button int)
}

// Bar is a tui.Handler that draws Config.Buttons top to bottom in the
// last Config.Columns of the window. A button shows its Pressed image
// while a press that began on it is held over it, its Hover image while
// the pointer is over it, and its Idle image otherwise; an image that
// cannot be drawn falls back to Hover, then Idle, then the first letter
// of the title. The title shows once the pointer has rested on the
// button for Config.TooltipDelay, and a press hides it until the pointer
// leaves the button. Handle consumes only the left presses that begin
// on a button, through their release.
type Bar struct {
	cfg    Config
	images map[string]*imageuri.Component
	now    func() time.Time

	width, height int
	// cell is the size of a cell in device pixels, which pixels tells
	// whether a writer reported.
	cell           cell.PixelSize
	pixels         bool
	hover, pressed int
	armed, foreign bool

	hoverSince time.Time
	tipHidden  bool
	tipTimer   *time.Timer
}

// New returns a Bar that starts loading the images of cfg.Buttons.
func New(cfg Config) *Bar {
	b := &Bar{
		cfg:     cfg,
		images:  make(map[string]*imageuri.Component),
		now:     time.Now,
		cell:    nominalCell,
		hover:   -1,
		pressed: -1,
	}
	for _, button := range cfg.Buttons {
		for _, uri := range []string{button.Idle, button.Hover, button.Pressed} {
			if _, ok := b.images[uri]; ok {
				continue
			}
			img, err := imageuri.New(uri, template.DefaultImageTTL,
				cfg.Storage, cfg.Interrupter, imageuri.Style{
					Fit:         term.ImageFitContain,
					Layer:       term.ImageLayerAboveText,
					HideLoading: true,
				})
			if err != nil {
				slog.Warn("quick menu: new image", "error", err)
			} else {
				// Every state is loaded up front so that the first hover
				// or press does not wait for its image.
				img.Start()
			}
			b.images[uri] = img
		}
	}
	return b
}

// Resize satisfies tui.Component.
func (b *Bar) Resize(width, height int) {
	b.width, b.height = width, height
}

// Draw satisfies tui.Component.
func (b *Bar) Draw(w term.Writer) {
	if size, ok := cell.PixelSizeFromContext(w.Context()); ok {
		b.cell, b.pixels = size, true
	}
	_, side := b.metrics()
	if side <= 0 {
		return
	}
	bottom := float64(b.height) * b.cell.Height
	for i := range b.cfg.Buttons {
		if b.buttonTop(i) >= bottom {
			break
		}
		b.drawButton(w, i)
	}
	b.drawTooltip(w)
}

// Handle satisfies tui.Handler.
func (b *Bar) Handle(ev term.Event) (exit, handled bool) {
	if ev.Type != term.EventMouse {
		return false, false
	}
	at := b.buttonAt(ev.MouseX, ev.MouseY)
	switch ev.Key {
	case 0:
		// The window only reports a motion while no button is held, so
		// any press has ended, even if its release went elsewhere.
		b.pressed, b.armed, b.foreign = -1, false, false
		if b.hoverOver(b.hoverAt(ev.MouseX, ev.MouseY)) {
			b.play(b.cfg.Sounds.Hover)
		}
	case term.MouseLeft:
		switch {
		case b.pressed >= 0:
			b.armed = at == b.pressed
		case b.foreign || at < 0:
			b.foreign = true
			b.hoverOver(-1)
			return false, false
		default:
			b.pressed, b.armed = at, true
			b.hoverOver(at)
			b.tipHidden = true
			b.stopTooltip()
		}
		handled = true
	case term.MouseRight, term.MouseMiddle:
		if b.pressed < 0 {
			b.foreign = true
			b.hoverOver(-1)
			return false, false
		}
		handled = true
	case term.MouseRelease:
		pressed := b.pressed
		b.pressed, b.armed, b.foreign = -1, false, false
		entered := b.hoverOver(b.hoverAt(ev.MouseX, ev.MouseY))
		clicked := pressed >= 0 && at == pressed
		if entered && !clicked {
			b.play(b.cfg.Sounds.Hover)
		}
		if pressed < 0 {
			return false, false
		}
		if clicked {
			b.play(b.cfg.Sounds.Click)
			if b.cfg.Activate != nil {
				b.cfg.Activate(pressed)
			}
		}
		handled = true
	default:
		return false, false
	}
	if b.armed || (b.pressed < 0 && b.hover >= 0) {
		term.SetPointerShape(ev, term.PointerLink)
	}
	return false, handled
}

// Cursor satisfies tui.Handler.
func (b *Bar) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, 0, false
}

// Selection satisfies tui.Handler.
func (b *Bar) Selection() (string, bool) { return "", false }

// Close stops loading the images and drops a title that is due.
func (b *Bar) Close() error {
	b.stopTooltip()
	var errs []error
	for _, img := range b.images {
		if img != nil {
			errs = append(errs, img.Close())
		}
	}
	return errors.Join(errs...)
}

// hoverOver restarts the title's delay when i is not the hovered button,
// and reports whether the pointer has moved onto a button.
func (b *Bar) hoverOver(i int) bool {
	if i == b.hover {
		return false
	}
	b.hover, b.tipHidden = i, false
	b.stopTooltip()
	if i < 0 {
		return false
	}
	b.hoverSince = b.now()
	if b.cfg.TooltipDelay > 0 {
		irq := b.cfg.Interrupter
		b.tipTimer = time.AfterFunc(b.cfg.TooltipDelay, func() {
			debug.CapturePanicReport(func() { _ = irq.Interrupt(context.Background()) })
		})
	}
	return true
}

func (b *Bar) play(name string) {
	if name != "" && b.cfg.Player != nil {
		b.cfg.Player.Play(name)
	}
}

func (b *Bar) stopTooltip() {
	if b.tipTimer != nil {
		b.tipTimer.Stop()
		b.tipTimer = nil
	}
}

func (b *Bar) metrics() (pad, side float64) {
	pad = b.cell.Width / 2
	return pad, float64(b.cfg.Columns-1) * b.cell.Width
}

func (b *Bar) buttonTop(i int) float64 {
	pad, side := b.metrics()
	return float64(b.cfg.Top)*b.cell.Height + pad + float64(i)*(side+pad)
}

func (b *Bar) columnLeft() int { return b.width - b.cfg.Columns }

func (b *Bar) buttonCells(i int) image.Rectangle {
	pad, side := b.metrics()
	left := float64(b.columnLeft())*b.cell.Width + pad
	top := b.buttonTop(i)
	px := image.Rect(
		int(math.Round(left)), int(math.Round(top)),
		int(math.Round(left+side)), int(math.Round(top+side)))
	return b.cell.Cells(px).Intersect(image.Rect(0, 0, b.width, b.height))
}

func (b *Bar) buttonAt(x, y int) int {
	pad, side := b.metrics()
	if side <= 0 || x < b.columnLeft() || x >= b.width ||
		y < b.cfg.Top || y >= b.height {
		return -1
	}
	// Each button owns half the gap on either side of it.
	mid := (float64(y)+0.5)*b.cell.Height -
		(float64(b.cfg.Top)*b.cell.Height + pad/2)
	if mid < 0 {
		return -1
	}
	if i := int(mid / (side + pad)); i < len(b.cfg.Buttons) {
		return i
	}
	return -1
}

// hoverAt skips the window's last column and row, where the window
// reports a pointer that has left it.
func (b *Bar) hoverAt(x, y int) int {
	if x >= b.width-1 || y >= b.height-1 {
		return -1
	}
	return b.buttonAt(x, y)
}

func (b *Bar) drawButton(w term.Writer, i int) {
	pw := b.placedWriter(w, i)
	for _, uri := range b.sprites(i) {
		img := b.images[uri]
		if img == nil {
			continue
		}
		img.Resize(pw.width, pw.height)
		img.Draw(&pw)
		if pw.drawn {
			return
		}
	}
	r := b.buttonCells(i)
	letter := component.NewSpan(
		component.NewString(initial(b.cfg.Buttons[i].Title)),
		component.SpanConfig{
			PadAutoFloating:  true,
			ContentAlignment: component.AlignmentCentered,
		})
	letter.Resize(r.Dx(), r.Dy())
	letter.Draw(&component.VirtualWriter{
		Writer: w,
		Offset: term.Coordinates{X: r.Min.X, Y: r.Min.Y},
		Width:  r.Dx(),
		Height: r.Dy(),
	})
}

func (b *Bar) placedWriter(w term.Writer, i int) placedWriter {
	pad, side := b.metrics()
	rows := max(1, int(math.Ceil(side/b.cell.Height)))
	top := b.buttonTop(i) + side/2 - float64(rows)*b.cell.Height/2
	pw := placedWriter{
		Writer: w,
		pos:    term.Coordinates{X: b.columnLeft(), Y: int(math.Floor(top / b.cell.Height))},
		clip:   image.Rect(b.columnLeft(), b.cfg.Top, b.width, b.height),
		width:  b.cfg.Columns - 1,
		height: rows,
	}
	if b.pixels {
		pw.offset = image.Pt(int(math.Round(pad)),
			int(math.Round(top-float64(pw.pos.Y)*b.cell.Height)))
	}
	return pw
}

func (b *Bar) sprites(i int) []string {
	button := b.cfg.Buttons[i]
	switch {
	case i == b.pressed && b.armed:
		return slices.Compact([]string{button.Pressed, button.Hover, button.Idle})
	case i == b.hover && b.pressed < 0:
		return slices.Compact([]string{button.Hover, button.Idle})
	}
	return []string{button.Idle}
}

func (b *Bar) drawTooltip(w term.Writer) {
	if b.hover < 0 || b.tipHidden ||
		b.now().Sub(b.hoverSince) < b.cfg.TooltipDelay {
		return
	}
	_, side := b.metrics()
	row := int(math.Floor((b.buttonTop(b.hover) + side/2) / b.cell.Height))
	left := b.columnLeft()
	if row >= b.height {
		return
	}
	tip := component.NewStringWithConfig(
		" "+b.cfg.Buttons[b.hover].Title+" ",
		component.StringConfig{Attributes: tooltipAttr})
	tipWidth, _ := tip.Dimensions()
	x := max(left-tipWidth, 0)
	if x >= left {
		return
	}
	tip.Resize(left-x, 1)
	tip.Draw(&component.VirtualWriter{
		Writer: w,
		Offset: term.Coordinates{X: x, Y: row},
		Width:  left - x,
		Height: 1,
	})
}

func initial(title string) string {
	for _, r := range strings.TrimSpace(title) {
		return string(unicode.ToUpper(r))
	}
	return "?"
}

type placedWriter struct {
	term.Writer
	pos           term.Coordinates
	offset        image.Point
	clip          image.Rectangle
	width, height int
	drawn         bool
}

func (w *placedWriter) DrawImage(img term.Image) bool {
	img.Pos, img.Offset, img.Clip = w.pos, w.offset, w.clip
	ok := w.Writer.DrawImage(img)
	w.drawn = w.drawn || ok
	return ok
}
