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

package ide

import (
	"context"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/workspace"
)

// asyncTerminalResizeQueue only has to hold the one resize that the
// worker has not picked up yet: anything older is already superseded
// by the newest size, so SetPtySize drops it rather than wait.
const asyncTerminalResizeQueue = 1

// errInvalidMasterPtyFd is the message the workspace schemes report
// once the master descriptor is gone. gRPC flattens it to an untyped
// status error, so the message is all the client can match on.
var errInvalidMasterPtyFd = workspace.ErrInvalidMasterPtyFd.Error()

type ptyResize struct {
	pty  workspaceapi.Pty
	size workspaceapi.PtySize
}

type asyncTerminal struct {
	schemeapi.Terminal
	notifications browserapi.Notifications

	resizes chan ptyResize
	stop    chan struct{}
	// closing serializes Close so a double teardown cannot close
	// stop twice.
	closing chan struct{}
}

func newAsyncTerminal(
	t schemeapi.Terminal, n browserapi.Notifications,
) *asyncTerminal {
	ret := &asyncTerminal{
		Terminal:      t,
		notifications: n,
		resizes:       make(chan ptyResize, asyncTerminalResizeQueue),
		stop:          make(chan struct{}),
		closing:       make(chan struct{}, 1),
	}
	go debug.CapturePanicReport(ret.run)
	return ret
}

// SetPtySize queues the resize and returns nil immediately. Transport
// failures surface later, as a notification raised by the worker.
func (t *asyncTerminal) SetPtySize(
	p workspaceapi.Pty, size workspaceapi.PtySize,
) error {
	req := ptyResize{pty: p, size: size}
	if t.offer(req) {
		return nil
	}
	// Whatever is queued is superseded by this request, so drop it
	// instead of stalling the event loop behind the transport.
	select {
	case dropped := <-t.resizes:
		t.log(log.DebugLevel, "dropped superseded pty resize %dx%d",
			dropped.size.Columns, dropped.size.Rows)
	default:
	}
	if !t.offer(req) {
		t.log(log.WarnLevel, "dropped pty resize %dx%d: queue is saturated",
			size.Columns, size.Rows)
	}
	return nil
}

func (t *asyncTerminal) offer(req ptyResize) bool {
	select {
	case t.resizes <- req:
		return true
	default:
		return false
	}
}

// Close stops the resize worker. It does not wait for an in-flight
// transport RPC; closing the underlying workspace unwinds that.
func (t *asyncTerminal) Close() {
	select {
	case t.closing <- struct{}{}:
		close(t.stop)
	default:
	}
}

func (t *asyncTerminal) run() {
	for {
		// Checked before every dispatch so a resize queued before
		// Close is not delivered once a blocked RPC unwinds.
		select {
		case <-t.stop:
			return
		default:
		}
		select {
		case <-t.stop:
			return
		case req := <-t.resizes:
			err := t.Terminal.SetPtySize(req.pty, req.size)
			if err != nil {
				t.log(log.ErrorLevel, "set pty size %dx%d: %v",
					req.size.Columns, req.size.Rows, err)
				// A pty that is already gone cannot be resized, and
				// the terminal is tearing down anyway, so the toast
				// would only be noise.
				if strings.Contains(err.Error(), errInvalidMasterPtyFd) {
					continue
				}
				_, _ = t.notifications.Notify(browserapi.LevelError,
					"resize terminal to %dx%d: %v",
					req.size.Columns, req.size.Rows, err)
			}
		}
	}
}

// OnDisconnect forwards [workspace.RemoteScheme.OnDisconnect] when the
// wrapped terminal is remote. Without it the decorator would hide the
// optional interface from vtereservoir.New's type assertion and warm
// VTEs would survive a dead transport.
func (t *asyncTerminal) OnDisconnect() <-chan struct{} {
	if rs, ok := t.Terminal.(workspace.RemoteScheme); ok {
		return rs.OnDisconnect()
	}
	return nil
}

// WaitConnected forwards [workspace.RemoteScheme.WaitConnected] when
// the wrapped terminal is remote, and is a no-op otherwise.
func (t *asyncTerminal) WaitConnected(ctx context.Context) error {
	if rs, ok := t.Terminal.(workspace.RemoteScheme); ok {
		return rs.WaitConnected(ctx)
	}
	return nil
}

func (t *asyncTerminal) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "ide.asyncTerminal").
		Logf(level, msg, args...)
}
