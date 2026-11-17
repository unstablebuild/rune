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

package vteregistry

import (
	"context"
	"sync"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vtewire"
)

// canonical is a terminal and the state its attachments share.
//
// Its mutex is taken on the host event loop around calls into the
// emulator, so nothing the emulator calls back from its parse goroutine
// with its own lock held may take it.
type canonical struct {
	r              *Registry
	id             vte.ID
	shell          bool
	commandAndArgs []string
	createdAt      time.Time
	// poke tells the watcher of a detached terminal that it printed.
	poke chan struct{}
	// hub fans the terminal's raw pty stream out to its wire members.
	hub teeHub
	// cell is the size of a cell in pixels in force when the registry
	// has no display of its own: that of the wire member whose size
	// the pty follows. The emulator reads it from its parse goroutine,
	// which a resize holding mu waits on.
	cell atomicCellSize
	// pixels reports the cell size the emulator uses: the registry's
	// display when it has one, else cell.
	pixels func() (width, height int)
	// wireCells is whether wire members decide the cell size.
	wireCells bool

	mu  sync.Mutex
	h   *vte.Handler
	uri workspaceapi.URI
	// attachments are the views attached. A terminal with none is
	// detached.
	attachments map[*attachment]struct{}
	// previews are the views that leave it detached.
	previews map[*attachment]struct{}
	// resizeOwner is the attachment whose size the pty follows.
	resizeOwner   *attachment
	focusSeq      uint64
	attachSeq     uint64
	width, height int
	defAttr       term.Attributes
	destroyed     bool
	// told is what the watcher of this terminal last told it runs,
	// unknown while attached, so a listing carries what a watcher
	// that was not listening missed.
	told vte.Liveness
	// stopWatch stops the watcher of a detached terminal, if any.
	stopWatch chan struct{}
}

func (c *canonical) attachLocked() *attachment {
	a := &attachment{c: c}
	c.attachSeq++
	a.seq = c.attachSeq
	c.attachments[a] = struct{}{}
	if c.resizeOwner == nil || c.resizeOwner.preview {
		c.resizeOwner = a
		c.followLocked(a)
	}
	return a
}

func (c *canonical) detach(a *attachment) {
	c.mu.Lock()
	if a.preview {
		delete(c.previews, a)
		if c.resizeOwner == a {
			c.resizeOwner = c.nextResizeOwnerLocked()
		}
		c.mu.Unlock()
		return
	}
	if _, ok := c.attachments[a]; !ok {
		c.mu.Unlock()
		return
	}
	delete(c.attachments, a)
	if c.resizeOwner == a {
		c.resizeOwner = c.nextResizeOwnerLocked()
		// The heir would otherwise show a grid of the departed view's
		// size until its own view changes.
		if heir := c.resizeOwner; heir != nil {
			c.followLocked(heir)
		}
	}
	if len(c.attachments) == 0 && !c.destroyed {
		// It may be running something the user wants back: list it,
		// and tell them when it is done.
		c.startWatchLocked()
	}
	c.mu.Unlock()
	c.changed(vte.LivenessUnknown)
}

// nextResizeOwnerLocked picks the most recently focused attachment,
// the oldest one when none was focused.
func (c *canonical) nextResizeOwnerLocked() *attachment {
	var ret *attachment
	for a := range c.attachments {
		if ret == nil || a.focusedAt > ret.focusedAt ||
			(a.focusedAt == ret.focusedAt && a.seq < ret.seq) {
			ret = a
		}
	}
	return ret
}

// followLocked gives the pty the size a was last given. Whatever a has
// not named stays in force.
//
// The emulator is resized while its feed is held still, so every wire
// member sees the resize at the same point in the byte stream it
// happened for this emulator, which is what makes the replicas reflow
// identically.
func (c *canonical) followLocked(a *attachment) {
	if a.width <= 0 || a.height <= 0 {
		return
	}
	cell := c.cell.load()
	if a.cell != (cellSize{}) && c.wireCells {
		cell = a.cell
	}
	if a.width == c.width && a.height == c.height && cell == c.cell.load() {
		return
	}
	c.width, c.height = a.width, a.height
	c.h.Component().WithFeedHeld(func() {
		// The cell changes between two feeds, as the grid does, so
		// every replica places an image at the size this emulator did.
		c.cell.store(cell)
		c.h.Resize(a.width, a.height)
		g := c.geometryLocked()
		c.hub.broadcast(&g)
	})
}

// geometryLocked reports the size in force for every view.
func (c *canonical) geometryLocked() vtewire.Resize {
	w, h := c.pixels()
	return vtewire.Resize{
		Width: c.width, Height: c.height,
		CellPixelWidth: w, CellPixelHeight: h,
	}
}

// liveness asks the host of the terminal's pty what it runs. Neither
// the event loop nor a holder of c.mu, which the event loop takes to
// draw, may wait on it.
func (c *canonical) liveness() vte.Liveness {
	comp := c.h.Component()
	if !c.shell {
		if comp.IsComplete() {
			return vte.LivenessIdle
		}
		return vte.LivenessRunning
	}
	return shellLiveness(comp)
}

func (c *canonical) info() vte.Info {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.infoLocked()
}

func (c *canonical) infoLocked() vte.Info {
	comp := c.h.Component()
	return vte.Info{
		ID:             c.id,
		URI:            c.uri,
		Title:          comp.Title(),
		CommandAndArgs: c.commandAndArgs,
		Width:          c.width,
		Height:         c.height,
		Attachments:    len(c.attachments),
		Liveness:       c.told,
		Complete:       comp.IsComplete(),
		CreatedAt:      c.createdAt,
	}
}

// changed tells the registry's watchers of the terminal's state, with l
// as what it runs when that was just found out. c.mu must not be held:
// the state is read and told under the registry's notify lock, so
// watchers see changes in the order they happened.
func (c *canonical) changed(l vte.Liveness) {
	c.r.notifyMu.Lock()
	defer c.r.notifyMu.Unlock()
	c.mu.Lock()
	c.told = l
	info := c.infoLocked()
	deleted := c.destroyed
	c.mu.Unlock()
	c.r.notify(vte.Change{
		ID: info.ID, URI: info.URI, Title: info.Title,
		Attachments: info.Attachments, Liveness: l,
		Complete: info.Complete, Deleted: deleted,
	})
}

// startWatchLocked watches a detached terminal off the event loop, as
// telling what it runs asks the host of its pty: the watchers are told
// it runs a program, then that its shell is idle again. It is checked
// at once, whenever it prints and every poll interval.
func (c *canonical) startWatchLocked() {
	if c.stopWatch != nil {
		return
	}
	stop := make(chan struct{})
	c.stopWatch = stop
	go debug.CapturePanicReport(func() {
		ticker := time.NewTicker(c.r.cfg.PollInterval)
		defer ticker.Stop()
		working := false
		for c.liveness() != vte.LivenessIdle {
			if !working {
				working = true
				c.r.schedule(func() { c.working(stop) })
			}
			select {
			case <-stop:
				return
			case <-c.poke:
			case <-ticker.C:
			}
		}
		c.r.schedule(func() { c.done(stop) })
	})
}

func (c *canonical) stopWatchLocked() {
	if c.stopWatch != nil {
		close(c.stopWatch)
		c.stopWatch = nil
	}
}

// working tells that a detached terminal runs a program.
func (c *canonical) working(stop chan struct{}) {
	c.mu.Lock()
	// It may have been attached again, or destroyed, since.
	current := c.stopWatch == stop
	c.mu.Unlock()
	if current {
		c.changed(vte.LivenessRunning)
	}
}

// done tells that a detached terminal is idle: the program it ran, if
// any, ended. The terminal stays: it is still listed and attachable.
func (c *canonical) done(stop chan struct{}) {
	c.mu.Lock()
	// It may have been attached again, or destroyed, since.
	current := c.stopWatch == stop
	if current {
		c.stopWatch = nil
	}
	c.mu.Unlock()
	if current {
		c.changed(vte.LivenessIdle)
	}
}

// exited is told that the terminal's process exited. A detached
// terminal has nothing left to show, so it is deleted.
func (c *canonical) exited() bool {
	c.mu.Lock()
	detached := c.h != nil && len(c.attachments) == 0 && !c.destroyed
	c.mu.Unlock()
	if !detached {
		return false
	}
	err := c.r.DeleteVTE(context.Background(), c.id)
	return err == nil
}

func (c *canonical) destroy() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.destroyed {
		return nil
	}
	c.destroyed = true
	c.stopWatchLocked()
	for a := range c.attachments {
		a.gone.Store(true)
	}
	for a := range c.previews {
		a.gone.Store(true)
	}
	clear(c.attachments)
	clear(c.previews)
	c.resizeOwner = nil
	c.hub.close(&vtewire.Dropped{
		Code: vtewire.CodeNotFound, Message: "terminal deleted",
	})
	return c.h.Close()
}
