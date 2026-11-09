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
	"slices"
	"strings"
	"sync/atomic"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/programstatus"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vtereservoir"
)

const asyncVTELoadingTitle = "terminal (connecting…)"

const maxQueuedAsyncVTEEvents = 256

var asyncVTEURICounter atomic.Uint64

var _ vtereservoir.VTE = (*asyncVTE)(nil)

type tabNameAliaser struct {
	browser.TabManager
	sched   func(func()) bool
	alias   map[string]workspaceapi.URI
	pending map[string][]programstatus.Report
	closed  map[string]bool
}

func newTabNameAliaser(
	tm browser.TabManager, sched func(func()) bool,
) *tabNameAliaser {
	return &tabNameAliaser{
		TabManager: tm,
		sched:      sched,
		alias:      make(map[string]workspaceapi.URI),
		pending:    make(map[string][]programstatus.Report),
		closed:     make(map[string]bool),
	}
}

func (a *tabNameAliaser) SetTabName(
	uri workspaceapi.URI, name string, attr term.Attributes,
) error {
	a.sched(func() {
		_ = a.TabManager.SetTabName(a.resolve(uri), name, attr)
	})
	return nil
}

func (a *tabNameAliaser) OnTabExit(uri workspaceapi.URI) bool {
	return a.TabManager.OnTabExit(a.resolve(uri))
}

func (a *tabNameAliaser) SetTabStatus(
	uri workspaceapi.URI, report programstatus.Report,
) error {
	a.sched(func() {
		if a.closed[uri.String()] {
			return
		}
		a.retainStatus(a.resolve(uri), report)
		_ = a.TabManager.SetTabStatus(a.resolve(uri), report)
	})
	return nil
}

func (a *tabNameAliaser) EndTabStatus(uri workspaceapi.URI) error {
	a.sched(func() {
		if a.closed[uri.String()] {
			return
		}
		uri = a.resolve(uri)
		a.pending[uri.String()] = slices.DeleteFunc(a.pending[uri.String()], func(r programstatus.Report) bool {
			return r.State == programstatus.Working || r.State == programstatus.Blocked || r.State == programstatus.Idle
		})
		if len(a.pending[uri.String()]) == 0 {
			delete(a.pending, uri.String())
		}
		_ = a.TabManager.EndTabStatus(a.resolve(uri))
	})
	return nil
}

func (a *tabNameAliaser) resolve(uri workspaceapi.URI) workspaceapi.URI {
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
	a.alias[from.String()] = to
	delete(a.closed, from.String())
	if reports := a.pending[from.String()]; len(reports) != 0 {
		delete(a.pending, from.String())
		_ = a.TabManager.SetTabStatus(from, programstatus.Report{State: programstatus.Clear})
		for _, report := range reports {
			a.retainStatus(a.resolve(to), report)
			_ = a.TabManager.SetTabStatus(a.resolve(to), report)
		}
	}
}

// retainStatus preserves live raw reports in update order across identity
// changes. Detached terminals must still report immediately.
func (a *tabNameAliaser) retainStatus(uri workspaceapi.URI, report programstatus.Report) {
	key := uri.String()
	reports := a.pending[key]
	reports = slices.DeleteFunc(reports, func(r programstatus.Report) bool {
		return r.ID == report.ID || report.State == programstatus.Clear &&
			(report.ID == "" || strings.HasPrefix(r.ID, report.ID+"/"))
	})
	if report.State != programstatus.Clear {
		if len(reports) == programstatus.MaxRecords {
			reports = slices.Delete(reports, 0, 1)
		}
		reports = append(reports, report)
	}
	if len(reports) == 0 {
		delete(a.pending, key)
	} else {
		a.pending[key] = reports
	}
}

func (a *tabNameAliaser) discardStatus(uri workspaceapi.URI) {
	delete(a.pending, uri.String())
	delete(a.pending, a.resolve(uri).String())
	a.closed[uri.String()] = true
	_ = a.TabManager.SetTabStatus(a.resolve(uri), programstatus.Report{State: programstatus.Clear})
}

func (a *tabNameAliaser) removeAlias(from workspaceapi.URI) {
	delete(a.alias, from.String())
}

func (a *tabNameAliaser) removeAliasesTo(target workspaceapi.URI) {
	for from, to := range a.alias {
		if to.String() == target.String() {
			delete(a.alias, from)
		}
	}
}

// asyncVTE is a permanent vtereservoir.VTE wrapper that runs the
// blocking VTE factory (reservoir warm-up waits, NewPty/StartCommand
// RPCs) off the host event loop.
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

// complete installs the factory result. It runs on the host event loop.
func (av *asyncVTE) complete(v vtereservoir.VTE, err error) {
	if av.closed {
		// Only a successful build hands over an owned, fully
		// initialized VTE; closing anything else is unsafe.
		if err == nil && v != nil {
			av.e.tabAliases.discardStatus(v.URI())
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
		exit, handled = av.real.Handle(ev)
		// The tab drops content that reports exit, which may
		// happen before the vte asks for it through OnTabExit.
		if exit && av.e.keepFailedTerminalTab(av.e.tabAliases.resolve(av.uri)) {
			exit = false
		}
		return exit, handled
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

func (av *asyncVTE) ExitErr() error {
	if av.real != nil {
		return av.real.ExitErr()
	}
	return nil
}

func (av *asyncVTE) CursorAtScroll() term.Coordinates {
	if av.real != nil {
		return av.real.CursorAtScroll()
	}
	return term.Coordinates{}
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
	if av.real != nil {
		av.e.tabAliases.discardStatus(av.real.URI())
	}
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
