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

package gshader_test

import (
	"context"
	"errors"
	"fmt"
	"image"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/component/gshader"
	"unstable.build/rune/internal/component/gshader/gshadertest"
)

// clock is a fake clock whose timers fire only when the test fires them.
type clock struct {
	now    time.Time
	timers []*timer
}

type timer struct {
	d       time.Duration
	f       func()
	stopped bool
}

func (c *clock) install(comp *gshader.Component) {
	gshader.SetClock(comp, func() time.Time { return c.now },
		func(d time.Duration, f func()) func() bool {
			t := &timer{d: d, f: f}
			c.timers = append(c.timers, t)
			return func() bool { t.stopped = true; return true }
		})
}

// fire runs the last timer, as it would fire after its delay.
func (c *clock) fire(t *testing.T) {
	t.Helper()
	require.NotEmpty(t, c.timers)
	last := c.timers[len(c.timers)-1]
	c.now = c.now.Add(last.d)
	last.f()
}

type interrupter struct{ n atomic.Int32 }

func (i *interrupter) Interrupt(context.Context) error { i.n.Add(1); return nil }

// closer is a component that records being closed.
type closer struct {
	component.String
	closed        int
	width, height int
}

func (c *closer) Resize(width, height int) {
	c.width, c.height = width, height
	c.String.Resize(width, height)
}
func (c *closer) Close() error { c.closed++; return nil }

func newCloser(text string) *closer { return &closer{String: component.NewString(text)} }

var testProgram = gshader.MustProgram("test.kage", []byte(kage("var Progress float")))

func progressOf(t *testing.T, n gshader.Node) float32 {
	t.Helper()
	return gshadertest.Uniform(t, n, gshader.UniformProgress).(float32)
}

func TestComponentStill(t *testing.T) {
	dev := &gshadertest.Device{CellSize: image.Pt(10, 20)}
	c := gshader.New(nil, gshader.FromProgram(testProgram, nil), gshader.Config{Layer: term.ImageLayerBelowText})
	c.Resize(8, 4)
	w := &gshadertest.Writer{Ctx: dev.Context(context.Background())}

	c.Draw(w)
	require.Len(t, dev.Calls, 1)
	require.Len(t, w.Images, 1)
	first := w.Images[0]
	assert.Equal(t, term.Image{
		Src: first.Src, ID: first.ID, Version: 1, Width: 8, Height: 4,
		Layer: term.ImageLayerBelowText, Fit: term.ImageFitFill,
	}, first)
	assert.Equal(t, image.Rect(0, 0, 80, 80), first.Src.Bounds())
	assert.Equal(t, image.Pt(8, 4), dev.Calls[0].Size)
	assert.Nil(t, dev.Calls[0].Src, "a generator renders over nothing")
	assert.Equal(t, float32(0), progressOf(t, dev.Calls[0].Node))

	w.Reset()
	c.Draw(w)
	require.Len(t, dev.Calls, 2)
	assert.Equal(t, dev.Calls[0].Key, dev.Calls[1].Key, "a stable key")
	assert.Equal(t, first, w.Images[0], "the same frame and version")
	assert.True(t, c.Done(), "a still has no frames left")
}

func TestComponentSharedKey(t *testing.T) {
	dev := &gshadertest.Device{}
	key := term.NewImageID()
	a := gshader.New(nil, gshader.FromProgram(testProgram, nil), gshader.Config{Key: key})
	b := gshader.New(nil, gshader.FromProgram(testProgram, nil), gshader.Config{Key: key})
	a.Resize(4, 4)
	b.Resize(4, 4)
	w := &gshadertest.Writer{Ctx: dev.Context(context.Background())}
	a.Draw(w)
	b.Draw(w)
	require.Len(t, w.Images, 2)
	assert.Equal(t, w.Images[0], w.Images[1], "components sharing a key share the rendering")
	assert.Equal(t, key, dev.Calls[0].Key)
}

func TestComponentBackdrop(t *testing.T) {
	placements := func(w *gshadertest.Writer) []string {
		var ret []string
		for _, img := range w.Images {
			if img.Src == nil {
				continue
			}
			ret = append(ret, fmt.Sprintf("%v %dx%d layer %d clip %v overflow %t",
				img.Pos, img.Width, img.Height, img.Layer, img.Clip, img.Overflow))
		}
		return ret
	}
	tests := []struct {
		name     string
		backdrop bool
		root     bool
		// framed draws the component as the content of a framed tile
		// with a background, 8x5 cells, rather than on its own.
		framed   bool
		wantSize image.Point
		want     []string
	}{
		{
			name:     "a wallpaper shades the frame of its tile under the backgrounds",
			backdrop: true,
			framed:   true,
			wantSize: image.Pt(8, 5),
			want: []string{
				"{0 0} 8x5 layer 0 clip (1,1)-(7,4) overflow false",
				"{0 0} 8x5 layer 2 clip (0,0)-(8,1) overflow true",
				"{0 0} 8x5 layer 2 clip (0,4)-(8,5) overflow true",
				"{0 0} 8x5 layer 2 clip (0,1)-(1,4) overflow true",
				"{0 0} 8x5 layer 2 clip (7,1)-(8,4) overflow true",
			},
		},
		{
			name:     "without Backdrop it shades its own cells",
			framed:   true,
			wantSize: image.Pt(6, 3),
			want:     []string{"{1 1} 6x3 layer 0 clip (1,1)-(7,4) overflow false"},
		},
		{
			name:     "a component with a root shades its own cells",
			backdrop: true,
			root:     true,
			framed:   true,
			wantSize: image.Pt(6, 3),
			want:     []string{"{1 1} 6x3 layer 0 clip (1,1)-(7,4) overflow false"},
		},
		{
			name:     "a frame that does not show it",
			backdrop: true,
			wantSize: image.Pt(6, 3),
			want:     []string{"{0 0} 6x3 layer 0 clip (0,0)-(0,0) overflow false"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dev := &gshadertest.Device{CellSize: image.Pt(2, 4)}
			var root tui.Component
			if tt.root {
				root = component.NewString("r")
			}
			c := gshader.New(root, gshader.FromProgram(testProgram, nil), gshader.Config{Backdrop: tt.backdrop})
			w := &gshadertest.Writer{Ctx: dev.Context(context.Background())}
			if tt.framed {
				wm, _ := tcomponent.NewWindowManager(c, tcomponent.WindowManagerConfig{
					Frame:                true,
					Background:           tcomponent.WindowBackground{Attr: term.Attributes{Bg: term.ColorNavy}},
					FrameGraphicsStorage: storagestub.NewInMemoryService(),
					Interrupter:          new(interrupter),
				})
				t.Cleanup(func() { assert.NoError(t, wm.Close()) })
				wm.Resize(8, 5)
				wm.Draw(w)
			} else {
				c.Resize(6, 3)
				c.Draw(w)
			}
			require.Len(t, dev.Calls, 1)
			assert.Equal(t, tt.wantSize, dev.Calls[0].Size)
			assert.Equal(t, tt.want, placements(w))
		})
	}
}

func TestComponentFallback(t *testing.T) {
	failing := &gshadertest.Device{Err: errors.New("boom")}
	working := &gshadertest.Device{}
	tests := []struct {
		name     string
		ctx      context.Context
		root     bool
		fallback bool
		expected string
	}{
		{name: "no device draws the fallback", ctx: context.Background(), fallback: true, expected: "fall"},
		{name: "no device without a fallback draws the root", ctx: context.Background(), root: true, expected: "root"},
		{name: "no device, no fallback and no root draws nothing", ctx: context.Background(), expected: "    "},
		{name: "a device error draws the fallback", ctx: failing.Context(context.Background()), fallback: true, expected: "fall"},
		{name: "a device error draws the root", ctx: failing.Context(context.Background()), root: true, expected: "root"},
		{name: "a writer without pictures draws the fallback", ctx: working.Context(context.Background()), fallback: true, expected: "fall"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg gshader.Config
			if tt.fallback {
				cfg.Fallback = component.NewString("fall")
			}
			var root *closer
			if tt.root {
				root = newCloser("root")
			}
			var c *gshader.Component
			if root != nil {
				c = gshader.New(root, gshader.Nop(), cfg)
			} else {
				c = gshader.New(nil, gshader.Nop(), cfg)
			}
			c.Resize(4, 1)
			w := term.NewStringWriter(4, 1)
			w.SetContext = tt.ctx
			comptest.TestComponent(t, c, w, []comptest.TestCase{{Expected: tt.expected}})
		})
	}
}

func TestComponentReportsEachErrorOnce(t *testing.T) {
	dev := &gshadertest.Device{}
	var reported []string
	c := gshader.New(nil, gshader.Nop(), gshader.Config{
		OnError: func(err error) { reported = append(reported, err.Error()) },
	})
	c.Resize(4, 1)
	w := &gshadertest.Writer{Ctx: dev.Context(context.Background())}
	for _, err := range []error{
		errors.New("a"), errors.New("a"), errors.New("b"), gshader.ErrUnsupported, errors.New("a"),
	} {
		dev.Err = err
		c.Draw(w)
	}
	assert.Equal(t, []string{"a", "b"}, reported)
	assert.Empty(t, w.Images)
}

func TestComponentAnimation(t *testing.T) {
	start := time.Unix(100, 0)
	type step struct {
		advance time.Duration
		fire    bool
		draw    bool
		// wantProgress is the progress drawn, or -1 for the root.
		wantProgress float32
		wantTimers   int
		wantDone     bool
	}
	tests := []struct {
		name  string
		cfg   gshader.Config
		root  bool
		steps []step
	}{
		{
			name: "frames follow the clock and the timer is armed once per draw",
			cfg:  gshader.Config{FPS: 10, Duration: time.Second},
			steps: []step{
				{draw: true, wantProgress: 0, wantTimers: 1},
				{draw: true, wantProgress: 0, wantTimers: 1},
				{fire: true, draw: true, wantProgress: 0.1, wantTimers: 2},
				{advance: 350 * time.Millisecond, draw: true, wantProgress: 0.4, wantTimers: 2},
				{fire: true, wantTimers: 2},
				{fire: true, wantTimers: 2},
				{draw: true, wantProgress: 0.6, wantTimers: 3},
			},
		},
		{
			name: "a loop wraps",
			cfg:  gshader.Config{FPS: 10, Duration: time.Second, Loop: true},
			steps: []step{
				{draw: true, wantProgress: 0, wantTimers: 1},
				{advance: 1250 * time.Millisecond, draw: true, wantProgress: 0.2, wantTimers: 1},
				{advance: 10 * time.Second, draw: true, wantProgress: 0.2, wantTimers: 1},
			},
		},
		{
			name: "a finished generator holds its last frame",
			cfg:  gshader.Config{FPS: 10, Duration: time.Second},
			steps: []step{
				{draw: true, wantProgress: 0, wantTimers: 1},
				{advance: 2 * time.Second, draw: true, wantProgress: 0.9, wantTimers: 1, wantDone: true},
				{advance: time.Second, draw: true, wantProgress: 0.9, wantTimers: 1, wantDone: true},
			},
		},
		{
			name: "a finished run shows the root",
			cfg:  gshader.Config{FPS: 10, Duration: time.Second},
			root: true,
			steps: []step{
				{draw: true, wantProgress: 0, wantTimers: 1},
				{advance: 2 * time.Second, draw: true, wantProgress: -1, wantTimers: 1, wantDone: true},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dev := &gshadertest.Device{Unsupported: false}
			intr := new(interrupter)
			tt.cfg.Interrupter = intr
			tt.cfg.Start = start
			var c *gshader.Component
			root := newCloser("root")
			if tt.root {
				c = gshader.New(root, gshader.FromProgram(testProgram, nil), tt.cfg)
			} else {
				c = gshader.New(nil, gshader.FromProgram(testProgram, nil), tt.cfg)
			}
			clk := &clock{now: start}
			clk.install(c)
			c.Resize(4, 1)
			interrupts := int32(0)
			for i, s := range tt.steps {
				clk.now = clk.now.Add(s.advance)
				if s.fire {
					clk.fire(t)
					interrupts++
					assert.Equal(t, interrupts, intr.n.Load(), "step %d: a fired timer interrupts", i)
				}
				if s.draw {
					calls := len(dev.Calls)
					w := &gshadertest.Writer{Ctx: dev.Context(context.Background())}
					c.Draw(w)
					if s.wantProgress < 0 {
						assert.Len(t, dev.Calls, calls, "step %d: the root is drawn alone", i)
						assert.Equal(t, 'r', w.Cells[term.Coordinates{}].Ch, "step %d", i)
					} else {
						require.Len(t, dev.Calls, calls+1, "step %d", i)
						assert.InDelta(t, s.wantProgress, progressOf(t, dev.Calls[calls].Node), 1e-6, "step %d", i)
					}
				}
				assert.Len(t, clk.timers, s.wantTimers, "step %d", i)
				assert.Equal(t, s.wantDone, c.Done(), "step %d", i)
			}
		})
	}
}

func TestComponentSchedulesTheNextFrame(t *testing.T) {
	start := time.Unix(100, 0)
	clk := &clock{now: start.Add(130 * time.Millisecond)}
	dev := &gshadertest.Device{}
	c := gshader.New(nil, gshader.FromProgram(testProgram, nil), gshader.Config{
		FPS: 10, Duration: time.Second, Start: start, Interrupter: new(interrupter),
	})
	clk.install(c)
	c.Resize(4, 1)
	c.Draw(&gshadertest.Writer{Ctx: dev.Context(context.Background())})
	require.Len(t, clk.timers, 1)
	assert.Equal(t, 70*time.Millisecond, clk.timers[0].d, "wakes up when the next frame is due")
}

func TestComponentDoesNotTickWithoutADraw(t *testing.T) {
	clk := &clock{now: time.Unix(100, 0)}
	c := gshader.New(nil, gshader.FromProgram(testProgram, nil), gshader.Config{
		Duration: time.Second, Interrupter: new(interrupter),
	})
	clk.install(c)
	c.Resize(4, 1)
	assert.Empty(t, clk.timers, "nothing before the first draw")
	c.Draw(&gshadertest.Writer{})
	assert.Empty(t, clk.timers, "nor where the fallback is drawn")
	dev := &gshadertest.Device{Err: errors.New("boom")}
	c.Draw(&gshadertest.Writer{Ctx: dev.Context(context.Background())})
	assert.Empty(t, clk.timers, "nor when the device fails")
}

func TestComponentClose(t *testing.T) {
	clk := &clock{now: time.Unix(100, 0)}
	intr := new(interrupter)
	fallback, root := newCloser("fall"), newCloser("root")
	c := gshader.New(root, gshader.Nop(), gshader.Config{
		Duration: time.Second, Interrupter: intr, Fallback: fallback,
	})
	clk.install(c)
	c.Resize(4, 1)
	dev := &gshadertest.Device{}
	c.Draw(&gshadertest.Writer{Ctx: dev.Context(context.Background())})
	require.Len(t, clk.timers, 1)

	require.NoError(t, c.Close())
	assert.True(t, clk.timers[0].stopped, "Close stops the timer")
	assert.Equal(t, 1, fallback.closed)
	assert.Equal(t, 1, root.closed)
	assert.True(t, c.Done())

	clk.timers[0].f()
	assert.Zero(t, intr.n.Load(), "a closed component does not interrupt")
	require.NoError(t, c.Close())
	assert.Equal(t, 1, root.closed, "Close is idempotent")
}

func TestComponentResizeIsForwarded(t *testing.T) {
	fallback, root := newCloser("fall"), newCloser("root")
	c := gshader.New(root, gshader.Nop(), gshader.Config{Fallback: fallback})
	c.Resize(7, 3)
	assert.Equal(t, [2]int{7, 3}, [2]int{root.width, root.height})
	assert.Equal(t, [2]int{7, 3}, [2]int{fallback.width, fallback.height})
}

func TestComponentShadesTheRoot(t *testing.T) {
	root := newCloser("ab")
	tests := []struct {
		name       string
		dev        *gshadertest.Device
		wantImages int
	}{
		{name: "the device renders the root", dev: &gshadertest.Device{}, wantImages: 1},
		{name: "a device that cannot falls back to the root", dev: &gshadertest.Device{Unsupported: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := gshader.New(root, gshader.Nop(), gshader.Config{})
			c.Resize(2, 1)
			w := &gshadertest.Writer{Ctx: tt.dev.Context(context.Background())}
			c.Draw(w)
			require.Len(t, tt.dev.Calls, 1)
			src := tt.dev.Calls[0].Src
			require.NotNil(t, src)
			require.Len(t, src.Cells, 1)
			assert.Equal(t, 'a', src.Cells[0][0].Ch)
			assert.Equal(t, 'a', w.Cells[term.Coordinates{}].Ch, "the root's cells are drawn under the picture")
			assert.Equal(t, 'b', w.Cells[term.Coordinates{X: 1}].Ch)
			assert.Len(t, w.Images, tt.wantImages)
		})
	}
}
