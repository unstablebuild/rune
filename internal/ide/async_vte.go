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

var _ vte.View = (*asyncVTE)(nil)

// vteFactory brings the terminal an asyncVTE stands in for. attached
// reports that the terminal ran before this view asked for it, which
// abandoning the view must not end, and whose screen a snapshot the
// view was given must not replace.
type vteFactory func() (v vte.View, attached bool, err error)

// tabNameAliaser routes the tab and status calls a terminal makes under
// its own URI to the tabs showing it, through the placeholder and
// session URIs in between. A terminal shown by no tab keeps its own URI,
// which the workspace lists as a terminal that is no tab.
type tabNameAliaser struct {
	browser.TabManager
	sched func(func()) bool
	// alias maps a URI to the URIs standing for it: a terminal to the
	// placeholders of every view attached to it, a placeholder to its
	// session tab.
	alias   map[string][]workspaceapi.URI
	pending map[string][]programstatus.Report
	closed  map[string]bool
}

func newTabNameAliaser(
	tm browser.TabManager, sched func(func()) bool,
) *tabNameAliaser {
	return &tabNameAliaser{
		TabManager: tm,
		sched:      sched,
		alias:      make(map[string][]workspaceapi.URI),
		pending:    make(map[string][]programstatus.Report),
		closed:     make(map[string]bool),
	}
}

func (a *tabNameAliaser) SetTabName(
	uri workspaceapi.URI, name string, attr term.Attributes,
) error {
	a.sched(func() {
		for _, to := range a.resolve(uri) {
			_ = a.TabManager.SetTabName(to, name, attr)
		}
	})
	return nil
}

func (a *tabNameAliaser) OnTabExit(uri workspaceapi.URI) bool {
	exited := false
	for _, to := range a.resolve(uri) {
		exited = a.TabManager.OnTabExit(to) || exited
	}
	return exited
}

func (a *tabNameAliaser) SetTabStatus(
	uri workspaceapi.URI, report programstatus.Report,
) error {
	a.sched(func() {
		if a.closed[uri.String()] {
			return
		}
		for _, to := range a.resolve(uri) {
			a.retainStatus(to, report)
			_ = a.TabManager.SetTabStatus(to, report)
		}
	})
	return nil
}

func (a *tabNameAliaser) EndTabStatus(uri workspaceapi.URI) error {
	a.sched(func() {
		if a.closed[uri.String()] {
			return
		}
		for _, to := range a.resolve(uri) {
			key := to.String()
			a.pending[key] = slices.DeleteFunc(a.pending[key], func(r programstatus.Report) bool {
				return r.State == programstatus.Working || r.State == programstatus.Blocked || r.State == programstatus.Idle
			})
			if len(a.pending[key]) == 0 {
				delete(a.pending, key)
			}
			_ = a.TabManager.EndTabStatus(to)
		}
	})
	return nil
}

// resolve returns the tab keys uri stands for, uri itself when it is
// aliased to none.
func (a *tabNameAliaser) resolve(uri workspaceapi.URI) []workspaceapi.URI {
	return a.resolveDepth(uri, 4)
}

func (a *tabNameAliaser) resolveDepth(uri workspaceapi.URI, depth int) []workspaceapi.URI {
	next := a.alias[uri.String()]
	if len(next) == 0 || depth == 0 {
		return []workspaceapi.URI{uri}
	}
	var ret []workspaceapi.URI
	for _, to := range next {
		ret = append(ret, a.resolveDepth(to, depth-1)...)
	}
	return ret
}

// addAlias makes to stand for from too. The reports retained for from
// move to to; when from already had tabs, to gets a copy of theirs.
func (a *tabNameAliaser) addAlias(from, to workspaceapi.URI) {
	if from.String() == to.String() || slices.ContainsFunc(a.alias[from.String()],
		func(u workspaceapi.URI) bool { return u.String() == to.String() }) {
		return
	}
	var siblings []programstatus.Report
	if len(a.alias[from.String()]) != 0 {
		siblings = slices.Clone(a.pending[a.resolve(from)[0].String()])
	}
	a.alias[from.String()] = append(a.alias[from.String()], to)
	delete(a.closed, from.String())
	reports := a.pending[from.String()]
	if len(reports) != 0 {
		delete(a.pending, from.String())
		_ = a.TabManager.SetTabStatus(from, programstatus.Report{State: programstatus.Clear})
	}
	for _, report := range append(siblings, reports...) {
		for _, leaf := range a.resolve(to) {
			a.retainStatus(leaf, report)
			_ = a.TabManager.SetTabStatus(leaf, report)
		}
	}
}

// handBack drops the reports retained for the tab leaf, which stops
// showing the terminal at canonical. When no other tab shows it, they
// go back to the terminal itself, which is then listed as no tab.
func (a *tabNameAliaser) handBack(leaf, canonical workspaceapi.URI) {
	reports := a.pending[leaf.String()]
	if len(reports) == 0 {
		return
	}
	delete(a.pending, leaf.String())
	_ = a.TabManager.SetTabStatus(leaf, programstatus.Report{State: programstatus.Clear})
	if len(a.alias[canonical.String()]) != 0 {
		return
	}
	for _, report := range reports {
		a.retainStatus(canonical, report)
		_ = a.TabManager.SetTabStatus(canonical, report)
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
	for _, to := range a.resolve(uri) {
		delete(a.pending, to.String())
		_ = a.TabManager.SetTabStatus(to, programstatus.Report{State: programstatus.Clear})
	}
	a.closed[uri.String()] = true
}

func (a *tabNameAliaser) removeAlias(from workspaceapi.URI) {
	delete(a.alias, from.String())
}

func (a *tabNameAliaser) removeAliasesTo(target workspaceapi.URI) {
	for from, to := range a.alias {
		to = slices.DeleteFunc(to, func(u workspaceapi.URI) bool {
			return u.String() == target.String()
		})
		if len(to) == 0 {
			delete(a.alias, from)
		} else {
			a.alias[from] = to
		}
	}
}

// vteCloseMode is what closing an asyncVTE does to its terminal.
type vteCloseMode int

const (
	// vteCloseAuto detaches a terminal that may be running something
	// for the user or that another view shows, and terminates any
	// other.
	vteCloseAuto vteCloseMode = iota
	// vteCloseDetach leaves the terminal running.
	vteCloseDetach
	// vteCloseDelete terminates the terminal, and every view of it.
	vteCloseDelete
)

// asyncVTE is a permanent vte.View wrapper that runs the
// blocking VTE factory (reservoir warm-up waits, NewPty/StartCommand
// RPCs) off the host event loop. It is a view of a terminal s keeps.
type asyncVTE struct {
	s   *terminalSessions
	uri workspaceapi.URI

	anim        *component.Animation
	animStopped bool

	real   vte.View
	err    error
	closed bool
	// pool is the reservoir real came from, which takes it back when
	// it is idle.
	pool *vtereservoir.Facility
	// existing is whether the factory attaches a terminal that was
	// running before, which abandoning the placeholder must not end.
	existing  bool
	closeMode vteCloseMode
	// pendingID names the terminal the factory attaches while it
	// loads, so a state save meanwhile keeps the session's terminal.
	pendingID vte.ID

	width, height int
	resized       bool

	queuedEvents    []term.Event
	pendingSnapshot *vte.Snapshot
	pendingFocus    *bool
	pendingAttrs    *term.Attributes

	errComp component.String
}

func newAsyncVTE(s *terminalSessions, factory vteFactory) *asyncVTE {
	av := &asyncVTE{s: s}
	uri, err := workspaceapi.ParseURI(fmt.Sprintf(
		"terminal://loading/%d", asyncVTEURICounter.Add(1)))
	if err == nil {
		av.uri = uri
	}
	frames, sequence := component.ProgressAnimationFrames()
	av.anim = component.NewAnimation(
		browser.EventPublisherInterrupter(s.cfg.Publisher), frames, sequence, 0)
	av.anim.Resize(2, 1)
	s.inflight.Add(1)
	go debug.CapturePanicReport(func() {
		defer s.inflight.Done()
		v, attached, ferr := factory()
		if !s.cfg.VTE.ScheduleNextTick(func() { av.complete(v, attached, ferr) }) {
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
func (av *asyncVTE) complete(v vte.View, attached bool, err error) {
	if err == nil {
		av.existing = attached
	}
	if av.closed {
		// Only a successful build hands over an owned, fully
		// initialized VTE; closing anything else is unsafe.
		if err == nil && v != nil {
			av.s.abandonView(av, v)
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
		_, _ = av.s.cfg.Notifications.Notify(browserapi.LevelError,
			"new terminal: %v", err)
		return
	}
	av.real = v
	av.s.aliasTab(v.URI(), av.uri)
	// A terminal that ran before shows its own screen; the snapshot
	// is what the session had when the terminal could not be reached.
	if av.pendingSnapshot != nil && !attached {
		if rerr := v.RestoreFromSnapshot(*av.pendingSnapshot); rerr != nil {
			_, _ = av.s.cfg.Notifications.Notify(browserapi.LevelError,
				"restore terminal session: %v", rerr)
		}
	}
	av.pendingSnapshot = nil
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
		_ = av.s.tabs.SetTabName(av.uri, title, term.Attributes{})
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
		if exit && av.s.tabs.keepFailed(av.uri) {
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

// ID returns the terminal's id: while it loads, that of the terminal
// the view restores, if any.
func (av *asyncVTE) ID() vte.ID {
	if av.real != nil {
		return av.real.ID()
	}
	return av.pendingID
}

// Close drops this view of the terminal. See
// [terminalSessions.closeView] for what becomes of the terminal.
func (av *asyncVTE) Close() error {
	if av.closed {
		return nil
	}
	av.closed = true
	av.stopAnimation()
	if av.real == nil {
		// complete observes av.closed and disposes of what it gets.
		av.s.forgetView(av.uri)
		return nil
	}
	return av.s.closeView(av)
}
