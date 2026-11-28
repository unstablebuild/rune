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

package gshader

import (
	"context"
	"errors"
	"image"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/debug"
)

// DefaultFPS is the frame rate of a Component whose Config sets none. At
// 1920x1080, a frame of a full-window wallpaper costs the GUI about 80µs
// on the CPU (gui's BenchmarkAnimatedWallpaper) and the built-in shaders
// from 0.4 to 1.9ms on an Apple M-series GPU (kageshader's
// BenchmarkBuiltins, under -tags e2e): at 30 frames a second, at most
// about a sixteenth of each frame's time.
const DefaultFPS = 30

// Config controls how a Component runs its shader.
type Config struct {
	// FPS is the frame rate of an animation. Zero is DefaultFPS.
	FPS int
	// Duration is how long an animation runs. Zero makes a still: frame 0
	// of 1, rendered only when its inputs change, with no timer.
	Duration time.Duration
	// Loop repeats the animation until Close.
	Loop bool
	// Layer is where the picture is composited relative to the cells it
	// covers.
	Layer term.ImageLayer
	// Backdrop lets a component without a root, which fills a window,
	// shade the frame around it as well, where
	// component.FrameBackdropFromContext reports that the frame shows it,
	// so that the picture reaches the frame's lines. The shader then sees
	// the window as its picture, one cell wider on every side.
	Backdrop bool
	// Key identifies the rendering to the device: components sharing a
	// key and a size render once per draw. Zero gives the component its
	// own.
	Key term.ImageID
	// Start is when the animation starts. Zero is the first Draw on a
	// device. Components sharing a Key should share a Start, so that
	// they show equal frames.
	Start time.Time
	// Fallback is drawn instead where the shader cannot run: without a
	// device, when the device fails and when the writer draws no
	// pictures. Nil draws the root alone.
	Fallback tui.Component
	// Interrupter requests the Draws of an animation. It is required when
	// Duration is positive.
	Interrupter term.Interrupter
	// OnError is called on the drawing goroutine once per distinct error
	// the device returns, other than ErrUnsupported.
	OnError func(error)
}

// Component draws the picture a Shader makes of a root component, or of
// nothing for a generator, as a term.Image over its cells. The root's
// cells are drawn under it, so selection and hit-testing still see them.
//
// An animation draws only when it is drawn: after each Draw it requests
// the Draw of the next frame, once, so a component that is hidden stops
// ticking until it is drawn again. Once a run that does not loop is over,
// a Component with a root draws the root alone, and one without keeps
// showing the last frame.
type Component struct {
	root          tui.Component
	shader        Shader
	cfg           Config
	key           term.ImageID
	width, height int
	// buf is what root draws into, nil without a root.
	buf *cell.BufferWriter
	// start is when the animation started, zero until it does.
	start    time.Time
	reported map[string]bool

	now       func() time.Time
	afterFunc func(time.Duration, func()) (stop func() bool)

	mu    sync.Mutex
	armed bool
	stop  func() bool

	closed   atomic.Bool
	finished atomic.Bool
}

var _ tui.Component = (*Component)(nil)

// New returns a Component that shades root, which may be nil for a
// generator, with sh. It panics when cfg animates without an Interrupter.
func New(root tui.Component, sh Shader, cfg Config) *Component {
	if cfg.Duration > 0 && cfg.Interrupter == nil {
		panic("gshader: an animation needs an Interrupter")
	}
	if cfg.FPS <= 0 {
		cfg.FPS = DefaultFPS
	}
	key := cfg.Key
	if key == 0 {
		key = term.NewImageID()
	}
	return &Component{
		root:   root,
		shader: sh,
		cfg:    cfg,
		key:    key,
		start:  cfg.Start,
		now:    time.Now,
		afterFunc: func(d time.Duration, f func()) func() bool {
			return time.AfterFunc(d, f).Stop
		},
	}
}

// Resize satisfies tui.Component.
func (c *Component) Resize(width, height int) {
	c.width, c.height = width, height
	if c.root != nil {
		c.buf = cell.NewBufferWriter(context.Background(), width, height)
		c.root.Resize(width, height)
	}
	if c.cfg.Fallback != nil {
		c.cfg.Fallback.Resize(width, height)
	}
}

// Draw satisfies tui.Component.
func (c *Component) Draw(w term.Writer) {
	if c.width <= 0 || c.height <= 0 {
		return
	}
	device, ok := DeviceFromContext(w.Context())
	if !ok {
		c.drawFallback(w, nil)
		return
	}
	frame, total, next, over := c.frame()
	if over && c.root != nil {
		c.root.Draw(w)
		return
	}
	src := c.drawRoot(w)
	out := 0
	if c.cfg.Backdrop && c.root == nil && tcomponent.FrameBackdropFromContext(w.Context()) {
		out = 1
	}
	f, err := device.Render(c.key, image.Pt(c.width+2*out, c.height+2*out), src,
		c.shader.Shade(frame, total, Source()))
	if err != nil {
		c.report(err)
		c.drawFallback(w, src)
		return
	}
	img := term.Image{
		Src: f.Src, ID: f.ID, Version: f.Version,
		Pos:   term.Coordinates{X: -out, Y: -out},
		Width: c.width + 2*out, Height: c.height + 2*out,
		Layer: c.cfg.Layer, Fit: term.ImageFitFill,
	}
	if out > 0 {
		img.Clip = image.Rect(0, 0, c.width, c.height)
	}
	if !w.DrawImage(img) {
		c.drawFallback(w, src)
		return
	}
	if out > 0 {
		c.drawBackdrop(w, img)
	}
	if c.cfg.Duration > 0 && !over {
		c.arm(next)
	}
}

// drawBackdrop draws img, the picture of the component and the cell
// around it, under the backgrounds of that cell alone: under the
// component's own cells it would show only through their default
// backgrounds, and the renderer would draw every cell it goes under
// again.
func (c *Component) drawBackdrop(w term.Writer, img term.Image) {
	img.Layer, img.Overflow = term.ImageLayerBelowBackground, true
	for _, clip := range [4]image.Rectangle{
		image.Rect(-1, -1, c.width+1, 0),
		image.Rect(-1, c.height, c.width+1, c.height+1),
		image.Rect(-1, 0, 0, c.height),
		image.Rect(c.width, 0, c.width+1, c.height),
	} {
		img.Clip = clip
		w.DrawImage(img)
	}
}

// frame returns the frame to show of total, when the next one is due and
// whether a run that does not loop is over.
func (c *Component) frame() (frame, total int, next time.Time, over bool) {
	if c.cfg.Duration <= 0 {
		return 0, 1, time.Time{}, false
	}
	now := c.now()
	if c.start.IsZero() {
		c.start = now
	}
	fps := float64(c.cfg.FPS)
	total = max(1, int(c.cfg.Duration.Seconds()*fps))
	played := int(max(0, now.Sub(c.start)).Seconds() * fps)
	next = c.start.Add(time.Duration(float64(played+1) * float64(time.Second) / fps))
	switch {
	case c.cfg.Loop:
		frame = played % total
	case played >= total:
		frame, over = total-1, true
		c.finished.Store(true)
	default:
		frame = played
	}
	return frame, total, next, over
}

// drawRoot draws the root's cells to w and returns its rendering, nil
// without a root.
func (c *Component) drawRoot(w term.Writer) *Snapshot {
	if c.root == nil {
		return nil
	}
	c.buf.SetContext(w.Context())
	_ = c.buf.Clear(term.Attributes{})
	c.root.Draw(c.buf)
	cells := c.buf.RawCells()
	for y, row := range cells {
		for x, cl := range row {
			w.SetCell(term.Coordinates{X: x, Y: y}, cl)
		}
	}
	return &Snapshot{Cells: cells, Images: c.buf.Images()}
}

// drawFallback draws the Fallback, or else the root, whose rendering src
// is when its cells are already drawn.
func (c *Component) drawFallback(w term.Writer, src *Snapshot) {
	switch {
	case c.cfg.Fallback != nil:
		c.cfg.Fallback.Draw(w)
	case src != nil:
		for _, img := range src.Images {
			w.DrawImage(img)
		}
	case c.root != nil:
		c.root.Draw(w)
	}
}

func (c *Component) report(err error) {
	if c.cfg.OnError == nil || errors.Is(err, ErrUnsupported) {
		return
	}
	msg := err.Error()
	if c.reported[msg] {
		return
	}
	if c.reported == nil {
		c.reported = make(map[string]bool)
	}
	c.reported[msg] = true
	c.cfg.OnError(err)
}

// arm requests a Draw at next, unless one is already requested.
func (c *Component) arm(next time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.armed || c.closed.Load() {
		return
	}
	c.armed = true
	c.stop = c.afterFunc(max(time.Millisecond, next.Sub(c.now())), func() {
		debug.CapturePanicReport(c.wake)
	})
}

func (c *Component) wake() {
	c.mu.Lock()
	c.armed = false
	c.mu.Unlock()
	if !c.closed.Load() {
		_ = c.cfg.Interrupter.Interrupt(context.Background())
	}
}

// Close stops the animation and closes the Fallback and the root when
// they are io.Closers.
func (c *Component) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	c.mu.Lock()
	stop := c.stop
	c.stop = nil
	c.mu.Unlock()
	if stop != nil {
		stop()
	}
	var errs []error
	for _, comp := range []tui.Component{c.cfg.Fallback, c.root} {
		if closer, ok := comp.(io.Closer); ok {
			errs = append(errs, closer.Close())
		}
	}
	return errors.Join(errs...)
}

// Done reports whether c has no frames left to show: it is a still, its
// run is over or it is closed.
func (c *Component) Done() bool {
	return c.cfg.Duration <= 0 || c.finished.Load() || c.closed.Load()
}
