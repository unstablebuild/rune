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
	"context"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
)

// Update satisfies ebiten.Game. It's called every time a new frame is to be scheduled.
func (g *GUI) Update() error {
	applyNativeWindowEffects(g.enableTransparent, g.bgBlurRadius)
	g.pendingEvents = append(g.pendingEvents, g.mouse.processMouse()...)
	g.pendingEvents = g.input.processEvents(g.pendingEvents)
	g.pendingEvents = append(g.pendingEvents, g.processWindowClosed()...)
	needsDraw := g.needsDraw

	if g.links.setMeta(g.input.metaHeld()) {
		g.needsRender = true
	}
	if g.links.armed() {
		g.links.pointAt(g.mouse.clampedCoordinates(), g.writer.RawCells())
	}

	interruptPending := g.interruptPending.Swap(false)
	for len(g.updateChan) > 0 {
		g.pendingEvents = append(g.pendingEvents, <-g.updateChan)
	}
	if interruptPending {
		g.pendingEvents = append(g.pendingEvents, term.Event{Type: term.EventInterrupt})
		if g.prevTickKey {
			g.echoLikely = true
		}
	}

	ctx := tui.ContextWithIteration(g.ctx, g.iteration)

	g.mu.Lock()
	defer g.mu.Unlock()

	needsDraw = g.drag.poll() || needsDraw

	keyEvents := 0
	sawInterrupt := interruptPending
	for _, ev := range g.pendingEvents {
		switch ev.Type {
		case term.EventInterrupt:
			sawInterrupt = true
			if ev.UserFunc != nil {
				ev.UserFunc()
				needsDraw = true
				continue
			}
			var payloadCtx context.Context
			if ev.Raw == nil {
				payloadCtx = context.Background()
			} else {
				if id, ok := tui.IterationFromRawBytes(ev.Raw); ok {
					payloadCtx = tui.ContextWithIteration(g.ctx, id)
				} else {
					payloadCtx = term.ContextWithPayload(g.ctx, ev.Raw)
				}
			}
			needsDraw = false
			g.drawHandler(payloadCtx)
		case term.EventError, term.EventResize:
		case term.EventMouse:
			if g.links.handleMouse(ev, g.writer.RawCells()) {
				continue
			}
			fallthrough
		default:
			if ev.Type == term.EventKey {
				keyEvents++
			}
			needsDraw = true
			exit, _ := g.handler.Handle(ev)
			if exit {
				if ebiten.IsFocused() {
					g.lastPositionX, g.lastPositionY = ebiten.WindowPosition()
				}
				return ErrHandlerExited
			}
		}
	}
	g.prevTickKey = keyEvents == 1
	if keyEvents == 1 && !sawInterrupt && g.echoLikely && len(g.updateChan) == 0 {
		if g.awaitEchoInterrupt() && g.interruptPending.Swap(false) {
			needsDraw = false
			g.drawHandler(context.Background())
		} else {
			g.echoLikely = false
		}
	}
	if needsDraw {
		g.drawHandler(ctx)
	}

	clear(g.pendingEvents)
	g.pendingEvents = g.pendingEvents[:0]
	g.iteration++
	return nil
}
