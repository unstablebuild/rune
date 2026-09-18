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

package text

import (
	"context"
	"time"

	"github.com/ernestrc/go-multierror"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/workspace"
)

var _ workspace.FlusherCloser = (*editorFlusherCloser)(nil)

// inflightFileOps tracks the saves and reloads of one file between the moment
// the operation starts and the moment its result has been applied on the
// event loop. That second edge is what an observer needs: until it passes,
// the file's saved timestamp and dirty state still describe the world before
// the operation, whichever caller started it.
type inflightFileOps struct {
	count int
	after []func()
}

func (c *Component) beginFileOp(uri workspaceapi.URI) {
	c.inflightMu.Lock()
	defer c.inflightMu.Unlock()
	ops := c.inflight[uri.String()]
	if ops == nil {
		ops = new(inflightFileOps)
		c.inflight[uri.String()] = ops
	}
	ops.count++
}

// endFileOp closes out one operation. Deferred callbacks run only when the
// result reached the event loop; a refused schedule means the UI is going
// away, and running them on this goroutine would touch event-loop state from
// the wrong thread.
func (c *Component) endFileOp(uri workspaceapi.URI, applied bool) {
	c.inflightMu.Lock()
	ops := c.inflight[uri.String()]
	if ops == nil {
		c.inflightMu.Unlock()
		return
	}
	if !applied {
		ops.after = nil
	}
	ops.count--
	if ops.count > 0 {
		c.inflightMu.Unlock()
		return
	}
	delete(c.inflight, uri.String())
	c.inflightMu.Unlock()
	for _, fn := range ops.after {
		fn()
	}
}

// AfterSettled defers fn until every in-flight save or reload for uri has
// been applied on the event loop, and reports whether there was anything to
// wait for. Callers that must not act on a half-applied save use the return
// value to skip their own handling.
func (c *Component) AfterSettled(uri workspaceapi.URI, fn func()) bool {
	c.inflightMu.Lock()
	defer c.inflightMu.Unlock()
	ops := c.inflight[uri.String()]
	if ops == nil {
		return false
	}
	ops.after = append(ops.after, fn)
	return true
}

// used to intercept calls to Close and Flush to dispatch
// corresponding events to subscribers.
type editorFlusherCloser struct {
	parent    *Component
	fc        workspace.FlusherCloser
	h         Handler
	uri       workspaceapi.URI
	buf       *cell.Buffer
	commands  []textapi.CommandManual
	lastFlush int
	reloading bool
}

func (c *editorFlusherCloser) OnWillEdit(
	ctx context.Context, start, end term.Coordinates, str string,
) {
}

func (c *editorFlusherCloser) OnDidEdit(
	ctx context.Context, from, to term.Coordinates, old string,
) {
	if c.reloading {
		// A reload replaced the whole buffer. If the file shrank, the caret can
		// be left on a row that no longer exists; the handler's SetCursorAtScroll
		// clamps it back into bounds. Doing this here (synchronously, as the
		// buffer content is swapped) avoids a window where a stale caret could
		// drive a Columns(row) access past the end of the buffer.
		if c.h != nil {
			c.h.SetCursorAtScroll(c.h.CursorAtScroll())
		}
		return
	}
	c.parent.setDirtyFileAttr(c.uri, c.buf, c.lastFlush)
}

// reloadVersion tells wrapAndDispatch to take the buffer version at
// completion: a reload replaces the buffer with what is on disk, so the
// version that ends up saved is only known once the worker is done.
const reloadVersion = -1

func (e *editorFlusherCloser) ForceFlush(ctx context.Context) (<-chan error, error) {
	saved := e.buf.Version()
	inner, err := e.fc.ForceFlush(ctx)
	if err != nil {
		return nil, err
	}
	return e.wrapAndDispatch(inner, false, saved), nil
}

func (e *editorFlusherCloser) LastFlush() time.Time {
	return e.fc.LastFlush()
}

func (e *editorFlusherCloser) Flush(ctx context.Context) (<-chan error, error) {
	// The save writes the buffer as it is now, so this is the version it
	// makes durable. Edits that land while it runs are not in it and must
	// keep the file unflushed.
	saved := e.buf.Version()
	inner, err := e.fc.Flush(ctx)
	if err != nil {
		return nil, err
	}
	return e.wrapAndDispatch(inner, false, saved), nil
}

func (e *editorFlusherCloser) Reload(ctx context.Context) (<-chan error, error) {
	e.reloading = true
	inner, err := e.fc.Reload(ctx)
	if err != nil {
		e.reloading = false
		return nil, err
	}
	return e.wrapAndDispatch(inner, true, reloadVersion), nil
}

func (e *editorFlusherCloser) wrapAndDispatch(
	inner <-chan error, isReload bool, savedVersion int,
) <-chan error {
	out := make(chan error, 1)
	e.parent.beginFileOp(e.uri)
	go debug.CapturePanicReport(func() {
		err := <-inner
		applied := e.parent.config.ScheduleNextTick(func() {
			// Nothing reached disk on failure, so the buffer is still
			// unflushed and subscribers must not be told otherwise.
			if err == nil {
				_ = e.dispatchFlush(savedVersion)
			}
			if isReload {
				e.reloading = false
			}
			e.parent.endFileOp(e.uri, true)
		})
		if !applied {
			e.parent.endFileOp(e.uri, false)
		}
		out <- err
		close(out)
	})
	return out
}

func (e *editorFlusherCloser) dispatchFlush(savedVersion int) error {
	if savedVersion == reloadVersion {
		savedVersion = e.buf.Version()
	}
	e.lastFlush = savedVersion
	e.parent.log(log.TraceLevel, "flushed, new snapshot is at %d", e.lastFlush)
	err := e.parent.dispatchFlush(e.uri, e.h)
	// dispatchFlush clears the tab's dirty marker for the whole file, so
	// raise it again when the buffer has moved past what was written.
	e.parent.setDirtyFileAttr(e.uri, e.buf, e.lastFlush)
	return err
}

func (e *editorFlusherCloser) Close() error {
	ev := textapi.Event{
		Type:     textapi.EventTypeClose,
		URI:      e.uri,
		Resource: e.h,
	}
	e.parent.DispatchEvent(ev)
	ret := e.fc.Close()
	for _, cmd := range e.commands {
		err := e.parent.fileRegistry.UnsubscribeCommandForFile(e.uri, cmd.Name)
		if err != nil {
			ret = multierror.Append(ret, err)
		}
	}
	return ret
}
