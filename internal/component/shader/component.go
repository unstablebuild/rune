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

package shader

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/debug"
)

// Component wraps a tui.Component with a Shader.
type Component struct {
	shader      Shader
	root        tui.Component
	interrupter term.Interrupter
	buf         *cell.BufferWriter
	total       int
	fps         int

	cancelCtx func()
	epoch     atomic.Int64
	done      atomic.Bool
}

// New wraps a tui.Component with the given shader and returns
// a Component that will use the give interrupter to animate it
// at the given fps. If fps is 0, a sane default is used.
func New(
	comp tui.Component, shader Shader,
	interrupter term.Interrupter, fps int,
	duration time.Duration,
) *Component {
	const defaultFPS = 30
	if fps == 0 {
		fps = defaultFPS
	}

	ctx, cancel := context.WithCancel(context.Background())
	ret := &Component{
		shader:      shader,
		root:        comp,
		interrupter: interrupter,
		buf:         cell.NewBufferWriter(context.Background(), 1, 1),
		total:       int(duration / time.Duration(int(time.Second)/fps)),
		fps:         fps,
		cancelCtx:   cancel,
	}

	go debug.CapturePanicReport(func() {
		ret.interrupt(ctx)
	})

	return ret
}

// Draw satisfies tui.Component.
func (c *Component) Draw(w term.Writer) {
	if c.done.Load() {
		c.buf = nil
		c.root.Draw(w)
		return
	}

	c.buf.SetContext(w.Context())
	_ = c.buf.Clear(term.Attributes{})

	c.root.Draw(c.buf)

	cells := c.buf.RawCells()
	c.shader.Shade(int(c.epoch.Load()), c.total, cells)

	for y, row := range cells {
		for x, cell := range row {
			w.SetCell(term.Coordinates{Y: y, X: x}, cell)
		}
	}
	// Placements are cut to the cells still showing them, so they go
	// over the shaded cells the way they went over the root's.
	for _, img := range c.buf.Images() {
		w.DrawImage(img)
	}
}

// Resize satisfies tui.Component.
func (c *Component) Resize(width, height int) {
	if !c.done.Load() {
		c.buf = cell.NewBufferWriter(context.Background(), width, height)
	}
	c.root.Resize(width, height)
}

// Close cleans all resources associated with this Component.
func (s *Component) Close() error {
	s.cancelCtx()
	return nil
}

// Total returns the number of frames this Component was constructed to
// run (duration / (1s/fps)). Exposed for tests that need to assert
// on the shader's configured lifetime.
func (c *Component) Total() int {
	return c.total
}

func (c *Component) interrupt(ctx context.Context) {
	cadence := time.Duration(int(time.Second) / c.fps)
	ticker := time.NewTicker(cadence)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			exit := c.epoch.Add(1) == int64(c.total)
			if exit {
				c.done.Store(true)
			}
			// always interrupt even after done is set to true
			// so we ensure that the root component is drawn intact again.
			_ = c.interrupter.Interrupt(ctx)
			if exit {
				return
			}
		case <-ctx.Done():
			c.done.Store(true)
			return
		}
	}
}
