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
	"unstable.build/rune/internal/term/programstatus"
	"unstable.build/rune/internal/term/vte"
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
	// reported is whether the host report of a detached terminal is
	// posted, and detachedTitle the title it was posted with.
	reported      bool
	detachedTitle string
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
	defer c.mu.Unlock()
	if a.preview {
		delete(c.previews, a)
		if c.resizeOwner == a {
			c.resizeOwner = c.nextResizeOwnerLocked()
		}
		return
	}
	if _, ok := c.attachments[a]; !ok {
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
	if len(c.attachments) > 0 || c.destroyed {
		return
	}
	// It may be running something the user wants back: list it, and
	// tell them when it is done.
	c.startWatchLocked()
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

// followLocked gives the pty the size a was last given.
func (c *canonical) followLocked(a *attachment) {
	if a.width <= 0 || a.height <= 0 || (a.width == c.width && a.height == c.height) {
		return
	}
	c.width, c.height = a.width, a.height
	c.h.Resize(a.width, a.height)
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
	comp := c.h.Component()
	return vte.Info{
		ID:             c.id,
		URI:            c.uri,
		Title:          comp.Title(),
		CommandAndArgs: c.commandAndArgs,
		Width:          c.width,
		Height:         c.height,
		Attachments:    len(c.attachments),
		Complete:       comp.IsComplete(),
		CreatedAt:      c.createdAt,
	}
}

func (c *canonical) hostReport(state programstatus.State) programstatus.Report {
	report := programstatus.Report{
		ID: HostReportID, State: state, Progress: -1,
		App: programstatus.HostApp, Title: c.detachedTitle,
	}
	if normalized, err := programstatus.Normalize(report); err == nil {
		return normalized
	}
	report.Title = ""
	return report
}

func (c *canonical) report(report programstatus.Report) {
	_ = c.r.tm.SetTabStatus(c.uri, report)
}

// startWatchLocked watches a detached terminal off the event loop, as
// telling what it runs asks the host of its pty: it is reported working
// while it runs a program, then done once its shell is idle again. It
// is checked at once, whenever it prints and every poll interval.
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

// working reports that a detached terminal runs a program.
func (c *canonical) working(stop chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopWatch != stop {
		// It was attached again, or destroyed, since.
		return
	}
	c.detachedTitle = c.h.Component().Title()
	c.reported = true
	c.report(c.hostReport(programstatus.Working))
}

// done reports that the program a detached terminal ran ended, if it
// ran one. The terminal stays: it is still listed and attachable.
func (c *canonical) done(stop chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopWatch != stop {
		// It was attached again, or destroyed, since.
		return
	}
	c.stopWatch = nil
	if !c.reported || c.h.Component().IsComplete() {
		// It ran nothing, or the shell exited: OnTabExit deletes it.
		return
	}
	c.report(c.hostReport(programstatus.Done))
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
	return c.h.Close()
}
