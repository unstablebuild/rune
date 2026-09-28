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
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vtereservoir"
)

// asyncVTELoadingTitle is the tab title shown while the underlying
// terminal is being spawned.
const asyncVTELoadingTitle = "terminal (connecting…)"

// maxQueuedAsyncVTEEvents bounds the typed-ahead key events queued
// while the underlying terminal is still spawning.
const maxQueuedAsyncVTEEvents = 256

// asyncVTEURICounter distinguishes concurrent placeholder URIs. The
// real vte URI is the pty slave name, which only exists after the
// spawn completes, so the wrapper needs its own stable identity.
var asyncVTEURICounter atomic.Uint64

var _ vtereservoir.VTE = (*asyncVTE)(nil)

// tabNameAliaser is a browser.TabManager decorator that rewrites
// SetTabName URIs through an alias table. Terminal tabs are keyed by
// the URI their creator saw at tab-creation time (an asyncVTE
// placeholder URI, or a terminal-session URI on restore), while the
// vte issues dynamic title updates under its own pty URI, which only
// exists after the spawn completes. Aliases map the vte URI to the
// tab key so those updates reach the tab.
//
// SetTabName is called from vte parser goroutines, so the alias table
// is mutex-guarded.
type tabNameAliaser struct {
	browser.TabManager
	mu    sync.Mutex
	alias map[string]workspaceapi.URI
}

func newTabNameAliaser(tm browser.TabManager) *tabNameAliaser {
	return &tabNameAliaser{
		TabManager: tm,
		alias:      make(map[string]workspaceapi.URI),
	}
}

// SetTabName satisfies browser.TabManager.
func (a *tabNameAliaser) SetTabName(
	uri workspaceapi.URI, name string, attr term.Attributes,
) error {
	return a.TabManager.SetTabName(a.resolve(uri), name, attr)
}

func (a *tabNameAliaser) OnTabExit(uri workspaceapi.URI) bool {
	return a.TabManager.OnTabExit(a.resolve(uri))
}

// resolve follows alias chains (pty URI -> placeholder URI -> session
// URI). The iteration bound is cycle insurance.
func (a *tabNameAliaser) resolve(uri workspaceapi.URI) workspaceapi.URI {
	a.mu.Lock()
	defer a.mu.Unlock()
	for range 4 {
		next, ok := a.alias[uri.String()]
		if !ok {
			break
		}
		uri = next
	}
	return uri
}

func (a *tabNameAliaser) addAlias(from, to workspaceapi.URI) {
	if from.String() == to.String() {
		return
	}
	a.mu.Lock()
	a.alias[from.String()] = to
	a.mu.Unlock()
}

func (a *tabNameAliaser) removeAlias(from workspaceapi.URI) {
	a.mu.Lock()
	delete(a.alias, from.String())
	a.mu.Unlock()
}

// removeAliasesTo drops every alias that resolves directly to target.
// Called when the placeholder goes away.
func (a *tabNameAliaser) removeAliasesTo(target workspaceapi.URI) {
	a.mu.Lock()
	for from, to := range a.alias {
		if to.String() == target.String() {
			delete(a.alias, from)
		}
	}
	a.mu.Unlock()
}

// asyncVTE is a permanent vtereservoir.VTE wrapper that runs the
// blocking VTE factory (reservoir warm-up waits, NewPty/StartCommand
// RPCs) off the host event loop. It renders a loading animation until
// the factory settles, then forwards every call to the real VTE. On a
// remote workspace this keeps the UI responsive while terminal spawn
// RPCs are in flight against a possibly slow or wedged transport.
//
// All mutable state is event-loop-owned: the factory goroutine hands
// its result back through ScheduleNextTick, so no locking is needed.
type asyncVTE struct {
	e   *ex
	uri workspaceapi.URI

	anim        *component.Animation
	animStopped bool

	real   vtereservoir.VTE
	err    error
	closed bool

	width, height int
	resized       bool

	queuedEvents    []term.Event
	pendingSnapshot *vte.Snapshot
	pendingFocus    *bool
	pendingAttrs    *term.Attributes

	errComp component.String
}

// newAsyncVTE returns immediately with a placeholder VTE and runs
// factory on a background goroutine. The result is installed on the
// host event loop via e.sched.
func newAsyncVTE(e *ex, factory func() (vtereservoir.VTE, error)) *asyncVTE {
	av := &asyncVTE{e: e}
	uri, err := workspaceapi.ParseURI(fmt.Sprintf(
		"terminal://loading/%d", asyncVTEURICounter.Add(1)))
	if err == nil {
		av.uri = uri
	}
	frames, sequence := component.ProgressAnimationFrames()
	av.anim = component.NewAnimation(
		browser.EventPublisherInterrupter(e.Browser()), frames, sequence, 0)
	av.anim.Resize(2, 1)
	e.asyncVTELoads.Add(1)
	go debug.CapturePanicReport(func() {
		defer e.asyncVTELoads.Done()
		v, ferr := factory()
		if !e.sched(func() { av.complete(v, ferr) }) {
			// The event loop is gone: nothing will ever install the
			// result, so release the freshly-built VTE.
			if ferr == nil && v != nil {
				_ = v.Close()
			}
			av.stopAnimation()
		}
	})
	return av
}

// complete installs the factory result. It runs on the host event
// loop.
func (av *asyncVTE) complete(v vtereservoir.VTE, err error) {
	if av.closed {
		// Only a successful build hands over an owned, fully
		// initialized VTE; closing anything else is unsafe.
		if err == nil && v != nil {
			_ = v.Close()
		}
		return
	}
	av.stopAnimation()
	if err != nil {
		av.err = err
		av.errComp = component.NewStringWithConfig(
			fmt.Sprintf("terminal unavailable: %v", err),
			component.StringConfig{Alignment: component.AlignmentCentered})
		av.errComp.Resize(av.width, av.height)
		_, _ = av.e.notifications.Notify(browserapi.LevelError,
			"new terminal: %v", err)
		return
	}
	av.real = v
	av.e.tabAliases.addAlias(v.URI(), av.uri)
	if av.pendingSnapshot != nil {
		if rerr := v.RestoreFromSnapshot(*av.pendingSnapshot); rerr != nil {
			_, _ = av.e.notifications.Notify(browserapi.LevelError,
				"restore terminal session: %v", rerr)
		}
		av.pendingSnapshot = nil
	}
	if av.resized {
		v.Resize(av.width, av.height)
	}
	if av.pendingAttrs != nil {
		v.SetDefaultAttributes(*av.pendingAttrs)
		av.pendingAttrs = nil
	}
	if av.pendingFocus != nil {
		v.OnFocusChange(*av.pendingFocus)
		av.pendingFocus = nil
	}
	for _, ev := range av.queuedEvents {
		_, _ = v.Handle(ev)
	}
	av.queuedEvents = nil
	// Best effort: a tab created while loading carries the
	// placeholder title; pick up the real one. The aliaser resolves
	// av.uri to the final tab key (e.g. a restored session URI) and
	// routes the vte's later dynamic title updates the same way.
	if title := v.Title(); title != "" {
		_ = av.e.tm.SetTabName(av.uri, title, term.Attributes{})
	}
}

func (av *asyncVTE) stopAnimation() {
	if av.animStopped {
		return
	}
	av.animStopped = true
	_ = av.anim.Close()
}

func (av *asyncVTE) Draw(w term.Writer) {
	if av.real != nil {
		av.real.Draw(w)
		return
	}
	if av.err != nil {
		av.errComp.Draw(w)
		return
	}
	dx := max(0, (av.width-2)/2)
	dy := max(0, av.height/2)
	av.anim.Draw(translateWriter{w: w, dx: dx, dy: dy})
}

func (av *asyncVTE) Resize(width, height int) {
	av.width, av.height = width, height
	av.resized = true
	if av.real != nil {
		av.real.Resize(width, height)
		return
	}
	if av.err != nil {
		av.errComp.Resize(width, height)
	}
}

func (av *asyncVTE) Handle(ev term.Event) (exit, handled bool) {
	if av.real != nil {
		return av.real.Handle(ev)
	}
	if ev.Type != term.EventKey {
		return false, false
	}
	if len(av.queuedEvents) < maxQueuedAsyncVTEEvents {
		av.queuedEvents = append(av.queuedEvents, ev)
	}
	return false, false
}

func (av *asyncVTE) Cursor() (
	c term.Coordinates, s term.CursorStyle, show bool,
) {
	if av.real != nil {
		return av.real.Cursor()
	}
	return
}

func (av *asyncVTE) Selection() (string, bool) {
	if av.real != nil {
		return av.real.Selection()
	}
	return "", false
}

func (av *asyncVTE) SeekUp() bool {
	if av.real != nil {
		return av.real.SeekUp()
	}
	return false
}

func (av *asyncVTE) SeekDown() bool {
	if av.real != nil {
		return av.real.SeekDown()
	}
	return false
}

func (av *asyncVTE) SeekOffset() int {
	if av.real != nil {
		return av.real.SeekOffset()
	}
	return 0
}

func (av *asyncVTE) MaxSeekOffset() int {
	if av.real != nil {
		return av.real.MaxSeekOffset()
	}
	return 0
}

func (av *asyncVTE) OnFocusChange(inFocus bool) {
	if av.real != nil {
		av.real.OnFocusChange(inFocus)
		return
	}
	av.pendingFocus = &inFocus
}

func (av *asyncVTE) SetDefaultAttributes(attr term.Attributes) {
	if av.real != nil {
		av.real.SetDefaultAttributes(attr)
		return
	}
	av.pendingAttrs = &attr
}

// Snapshot forwards when ready. Pre-ready it returns the queued
// restore snapshot if present so a state save during load keeps the
// session, and a zero snapshot otherwise.
func (av *asyncVTE) Snapshot() (vte.Snapshot, error) {
	if av.real != nil {
		return av.real.Snapshot()
	}
	if av.pendingSnapshot != nil {
		return *av.pendingSnapshot, nil
	}
	return vte.Snapshot{}, nil
}

func (av *asyncVTE) RestoreFromSnapshot(s vte.Snapshot) error {
	if av.real != nil {
		return av.real.RestoreFromSnapshot(s)
	}
	av.pendingSnapshot = &s
	return nil
}

// IsComplete reports false while loading so consumers treat the
// terminal as live, and true on factory failure so they can dispose
// of the placeholder and retry (e.g. the companion terminal toggle).
func (av *asyncVTE) IsComplete() bool {
	if av.real != nil {
		return av.real.IsComplete()
	}
	return av.err != nil
}

// URI returns the permanent wrapper URI: tab identity must not
// change when the real VTE arrives.
func (av *asyncVTE) URI() workspaceapi.URI {
	return av.uri
}

func (av *asyncVTE) Title() string {
	if av.real != nil {
		return av.real.Title()
	}
	return asyncVTELoadingTitle
}

func (av *asyncVTE) UsedAlternateBuffer() bool {
	if av.real != nil {
		return av.real.UsedAlternateBuffer()
	}
	return false
}

func (av *asyncVTE) ClearPrimaryBuffer() bool {
	if av.real != nil {
		return av.real.ClearPrimaryBuffer()
	}
	return false
}

func (av *asyncVTE) Close() error {
	if av.closed {
		return nil
	}
	av.closed = true
	av.stopAnimation()
	// Drop both directions: the vte URI pointing at this placeholder
	// and this placeholder pointing at a session tab key.
	av.e.tabAliases.removeAliasesTo(av.uri)
	av.e.tabAliases.removeAlias(av.uri)
	if av.real != nil {
		return av.real.Close()
	}
	// Pre-ready: complete observes av.closed and closes the
	// freshly-built VTE silently (the user abandoned the terminal).
	return nil
}
