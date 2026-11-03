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

package idestatus

import (
	"cmp"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/ide/idehistory"
	"unstable.build/rune/internal/sounds"
	"unstable.build/rune/internal/term/programstatus"
)

func detachedTestReport(state programstatus.State, msg string) programstatus.Report {
	return programstatus.Report{State: state, Progress: -1, Msg: msg}
}

func statusTestStyle() InboxStyle {
	style := DefaultInboxStyle()
	style.MarkerAttr = term.Attributes{Fg: term.ColorPurple}
	style.SelectedAttr = term.Attributes{Bg: term.ColorNavy}
	style.HoverAttr = term.Attributes{Bg: term.ColorTeal, Attrs: term.AttrBold}
	style.WorkspaceAttr = term.Attributes{Fg: term.ColorBlue}
	style.WorkspaceHighlightAttr = term.Attributes{Fg: term.ColorBlack}
	style.ProgressAttr = term.Attributes{Bg: term.ColorMaroon}
	style.ProgressDoneAttr = term.Attributes{Fg: term.ColorLime}
	style.ProgressRemainingAttr = term.Attributes{Fg: term.ColorSilver}
	style.ProgressRemainingSelectedAttr = term.Attributes{Fg: term.ColorWhite}
	style.ProgressLabelAttr = term.Attributes{Fg: term.ColorAqua}
	// Unlike the default, it shows in snapshots without a Nerd Font.
	style.MoreBelow = '↓'
	style.MoreBelowAttr = term.Attributes{Fg: term.ColorYellow}
	style.DividerAttr = term.Attributes{Fg: term.ColorOlive}
	style.PlaceholderAttr = term.Attributes{Fg: term.ColorFuchsia}
	return style
}

type statusStoreTestHost struct {
	workspaces map[string]string
	order      []workspaceapi.URI
	names      map[statusKey]string
	tabs       map[statusKey]*browser.Tab
	previews   map[statusKey]Preview
	seen       map[statusKey]bool
	inSight    bool
	notified   []string
	// shownNotes holds the notifications on screen by id, as a
	// notifications.Container keeps them: the id stands for the message
	// it was created with.
	shownNotes map[string]*statusTestNote
	changed    []workspaceapi.URI
	shown      []statusKey
	closed     []statusKey
	// onClose, when set, closes what closeStatus is asked to.
	onClose func(statusKey)
	// confirms holds the questions confirmStatus asked, and yes what
	// answering the last one yes does.
	confirms []string
	yes      func()
	mu       sync.Mutex
	queued   []func()
}

func (h *statusStoreTestHost) addWorkspace(ws workspaceapi.URI, name string) {
	h.workspaces[ws.String()] = name
	h.order = append(h.order, ws)
}
func (h *statusStoreTestHost) Workspace(ws workspaceapi.URI) (int, string, bool) {
	name, ok := h.workspaces[ws.String()]
	return slices.IndexFunc(h.order, ws.Equal), name, ok
}
func (h *statusStoreTestHost) TabName(ws, uri workspaceapi.URI) (string, bool) {
	name, ok := h.names[statusKey{ws.String(), uri.String()}]
	return name, ok
}
func (h *statusStoreTestHost) Preview(ws, uri workspaceapi.URI) (Preview, bool) {
	key := statusKey{ws.String(), uri.String()}
	if tab, ok := h.tabs[key]; ok {
		w, hh := tab.ContentSize()
		return Preview{Content: tab, Width: w, Height: hh, Fit: tab.Resize}, true
	}
	p, ok := h.previews[key]
	return p, ok
}
func (h *statusStoreTestHost) TabIcon(ws, uri workspaceapi.URI) (rune, bool) {
	if _, ok := h.tabs[statusKey{ws.String(), uri.String()}]; !ok {
		return 0, false
	}
	return 'x', true
}
func (h *statusStoreTestHost) InSight(workspaceapi.URI, workspaceapi.URI) bool { return h.inSight }
func (h *statusStoreTestHost) Seen(ws, uri workspaceapi.URI) bool {
	return h.seen[statusKey{ws.String(), uri.String()}]
}
func (h *statusStoreTestHost) Notify(_ workspaceapi.URI, level browserapi.NotificationLevel, msg string) string {
	h.notified = append(h.notified, msg)
	h.shownNotes[msg] = &statusTestNote{level: level, msg: msg}
	return msg
}
func (h *statusStoreTestHost) UpdateNotification(_ workspaceapi.URI, id, msg string, progress, total int64) bool {
	n, ok := h.shownNotes[id]
	switch {
	case !ok:
		return false
	case progress == total:
		delete(h.shownNotes, id)
		return true
	case msg != "":
		n.msg = msg
	}
	n.progress, n.total = progress, total
	return true
}

// expire closes the notifications that close on their own: those whose
// progress was never set.
func (h *statusStoreTestHost) expire() {
	for id, n := range h.shownNotes {
		if n.total == 0 {
			delete(h.shownNotes, id)
		}
	}
}

// onScreen returns the notifications on screen, sorted by message.
func (h *statusStoreTestHost) onScreen() []statusTestNote {
	var ret []statusTestNote
	for _, n := range h.shownNotes {
		ret = append(ret, *n)
	}
	slices.SortFunc(ret, func(a, b statusTestNote) int { return strings.Compare(a.msg, b.msg) })
	return ret
}

// statusTestNote is a notification statusStoreTestHost shows. A total
// other than zero pins it until its progress reaches the total.
type statusTestNote struct {
	level           browserapi.NotificationLevel
	msg             string
	progress, total int64
}

func (h *statusStoreTestHost) Changed(ws workspaceapi.URI) { h.changed = append(h.changed, ws) }
func (h *statusStoreTestHost) Show(ws, uri workspaceapi.URI) {
	h.shown = append(h.shown, statusKey{ws.String(), uri.String()})
}
func (h *statusStoreTestHost) Close(ws, uri workspaceapi.URI) {
	key := statusKey{ws.String(), uri.String()}
	h.closed = append(h.closed, key)
	if h.onClose != nil {
		h.onClose(key)
	}
}
func (h *statusStoreTestHost) Confirm(message string, yes func()) {
	h.confirms = append(h.confirms, message)
	h.yes = yes
}
func (h *statusStoreTestHost) schedule(fn func()) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.queued = append(h.queued, fn)
	return true
}
func (h *statusStoreTestHost) drain() {
	h.mu.Lock()
	queued := h.queued
	h.queued = nil
	h.mu.Unlock()
	for _, fn := range queued {
		fn()
	}
}

func statusStoreTestURI(t *testing.T, raw string) workspaceapi.URI {
	t.Helper()
	uri, err := workspaceapi.ParseURI(raw)
	require.NoError(t, err)
	return uri
}

func newStatusStoreTest(t *testing.T) (*statusStoreTestHost, *Store, workspaceapi.URI) {
	t.Helper()
	return newStatusStoreTestWith(t, browser.TabStatusConfig{}, nil)
}

func newStatusStoreTestWith(
	t *testing.T, cfg browser.TabStatusConfig, interrupter term.Interrupter,
) (*statusStoreTestHost, *Store, workspaceapi.URI) {
	t.Helper()
	ws := statusStoreTestURI(t, "file:///workspace")
	host := &statusStoreTestHost{
		workspaces: make(map[string]string),
		names:      make(map[statusKey]string),
		tabs:       make(map[statusKey]*browser.Tab),
		previews:   make(map[statusKey]Preview),
		seen:       make(map[statusKey]bool),
		shownNotes: make(map[string]*statusTestNote),
	}
	host.addWorkspace(ws, "1 home")
	return host, New(host, cfg, NotifyConfig{}, sounds.Silent, statusTestStyle(), host.schedule, interrupter), ws
}

type statusStoreDetachedTest struct {
	store *Store
	ws    workspaceapi.URI
}

func newStatusStoreDetachedTest(t *testing.T) statusStoreDetachedTest {
	_, store, ws := newStatusStoreTest(t)
	return statusStoreDetachedTest{store, ws}
}
func (d *statusStoreDetachedTest) get(uri workspaceapi.URI) (programstatus.Summary, bool) {
	if d.store == nil {
		return programstatus.Summary{}, false
	}
	return d.store.Status(d.ws, uri)
}
func (d *statusStoreDetachedTest) list() []Item {
	if d.store == nil {
		return nil
	}
	return d.store.Items()
}
func (d *statusStoreDetachedTest) rollup() (programstatus.Summary, workspaceapi.URI) {
	if d.store == nil {
		return programstatus.Summary{Progress: -1}, workspaceapi.URI{}
	}
	return d.store.Rollup(d.ws)
}
func (d *statusStoreDetachedTest) update(uri workspaceapi.URI, fn func(*programstatus.Store)) (programstatus.Summary, programstatus.Summary, bool) {
	prev, ok := d.get(uri)
	if !ok {
		prev = programstatus.Summary{Progress: -1}
	}
	host := d.store.host.(*statusStoreTestHost)
	n := len(host.changed)
	d.store.updateDetached(d.ws, uri, fn)
	next, ok := d.get(uri)
	if !ok {
		next = programstatus.Summary{Progress: -1}
	}
	return prev, next, len(host.changed) != n
}

func TestStatusStoreLiveViews(t *testing.T) {
	host, store, ws := newStatusStoreTest(t)
	a, b := statusStoreTestURI(t, "file:///a"), statusStoreTestURI(t, "file:///b")
	v, other := store.View(nil), store.View(nil)
	t.Cleanup(func() { require.NoError(t, v.Close()); require.NoError(t, other.Close()) })
	v.Resize(60, 14)
	apply := func(uri workspaceapi.URI, id string, state programstatus.State) {
		store.updateDetached(ws, uri, func(r *programstatus.Store) { r.Apply(programstatus.Report{ID: id, State: state, Progress: -1}) })
	}
	program := func(uri workspaceapi.URI) statusRef { return statusRef{ws: ws.String(), uri: uri.String()} }
	_, ok := v.Selection()
	require.False(t, ok, "with no program, nothing is selected")
	apply(a, "", programstatus.Working)
	assert.Equal(t, program(a), v.sel, "the first program is selected as it comes")
	apply(b, "", programstatus.Done)
	require.Len(t, v.entries, 2)
	assert.Equal(t, program(a), v.sel, "the selection stays as programs come")
	assert.Equal(t, program(a), other.sel)
	v.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
	require.Equal(t, program(b), v.sel)
	apply(a, "", programstatus.Blocked)
	assert.Equal(t, program(b), v.sel, "the selection follows a reordered source")
	assert.Equal(t, program(a), other.sel)
	apply(a, "tool/edit", programstatus.Working)
	require.Len(t, v.entries, 2, "a record of a listed source adds no entry")
	apply(a, "", programstatus.Working)
	assert.Equal(t, program(b), v.sel, "the selection follows a reordered source")
	w := term.NewStringWriter(60, 14)
	v.Draw(w)
	require.NoError(t, w.Flush())
	assert.Contains(t, w.String(), detachedStatusLabel, "rebuilt entries retain the open view's size")
	require.NoError(t, other.Close())
	frozen := append([]statusEntry(nil), other.entries...)
	apply(a, "tool/edit", programstatus.Done)
	assert.Equal(t, frozen, other.entries)
	assert.Len(t, store.views, 1)
	exit, handled := v.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, exit)
	assert.True(t, handled)
	assert.Empty(t, host.shown)
	host.drain()
	assert.Equal(t, []statusKey{{ws.String(), b.String()}}, host.shown)
	apply(a, "", programstatus.Clear)
	require.Len(t, v.entries, 1)
	assert.Equal(t, program(b), v.sel)
	apply(b, "", programstatus.Clear)
	assert.Empty(t, v.entries)
	_, ok = v.Selection()
	assert.False(t, ok, "with no program left, nothing is selected")
	w = term.NewStringWriter(60, 14)
	v.Draw(w)
	require.NoError(t, w.Flush())
	assert.Contains(t, w.String(), statusViewEmpty)
}

func TestStatusStoreWorkspaceCleanup(t *testing.T) {
	host, store, ws := newStatusStoreTest(t)
	other := statusStoreTestURI(t, "file:///other")
	uri := statusStoreTestURI(t, "file:///terminal")
	host.addWorkspace(other, "2 other")
	for _, workspace := range []workspaceapi.URI{ws, other} {
		store.updateDetached(workspace, uri, func(r *programstatus.Store) { r.Apply(detachedTestReport(programstatus.Error, "boom")) })
		key := statusKey{workspace.String(), uri.String()}
		store.lastNotified[key] = time.Now()
		store.pendingNotify[key] = 1
	}
	v := store.View(nil)
	defer v.Close()
	require.Len(t, v.entries, 2)
	store.DropWorkspace(ws)
	require.Len(t, v.entries, 1)
	assert.Equal(t, other, v.entries[0].ws)
	_, ok := store.Status(ws, uri)
	assert.False(t, ok)
	assert.Len(t, store.lastNotified, 1)
	assert.Len(t, store.pendingNotify, 1)
	n := len(host.changed)
	store.DropWorkspace(ws)
	assert.Len(t, host.changed, n)
	delete(host.workspaces, other.String())
	assert.Empty(t, store.Items(), "workspaces without a slot are hidden")
	store.DropWorkspace(other)
	assert.Empty(t, store.sources)
	assert.Empty(t, store.lastNotified)
	assert.Empty(t, store.pendingNotify)
	assert.Empty(t, v.entries)
}

func TestStatusStoreSourceBecomesTab(t *testing.T) {
	host, store, ws := newStatusStoreTest(t)
	uri := statusStoreTestURI(t, "file:///terminal")
	store.updateDetached(ws, uri, func(r *programstatus.Store) {
		r.Apply(detachedTestReport(programstatus.Working, ""))
	})
	v := store.View(nil)
	defer v.Close()
	host.names[statusKey{ws.String(), uri.String()}] = "build"
	records := programstatus.New()
	records.Apply(detachedTestReport(programstatus.Working, ""))
	store.TabStatusChanged(ws, uri, records.Summary())
	require.Nil(t, store.sources[statusKey{ws.String(), uri.String()}].records)
	require.Len(t, v.entries, 1)
	assert.Equal(t, "build", v.entries[0].name)
}

type statusStoreTestTabs struct {
	browser.TabManager
	component *browser.Component
}

type statusStoreTestHandler struct{ handler.TestHandler }

func (*statusStoreTestHandler) Close() error { return nil }
func (b statusStoreTestTabs) SetTabStatus(uri workspaceapi.URI, r programstatus.Report) error {
	if !b.component.SetTabStatus(uri, r) {
		return errors.New("missing tab")
	}
	return nil
}
func (b statusStoreTestTabs) EndTabStatus(uri workspaceapi.URI) error {
	if !b.component.EndTabStatus(uri) {
		return errors.New("missing tab")
	}
	return nil
}

func TestStatusStoreTabLifecycle(t *testing.T) {
	host, store, ws := newStatusStoreTest(t)
	cfg := browser.DefaultConfig()
	cfg.OnTabStatus = func(uri workspaceapi.URI, _, next programstatus.Summary) { store.TabStatusChanged(ws, uri, next) }
	cfg.OnTabRemoved = func(uri workspaceapi.URI) { store.TabRemoved(ws, uri) }
	b := browser.NewComponent(cfg)
	t.Cleanup(func() { require.NoError(t, b.Close()) })
	uri := statusStoreTestURI(t, "file:///tab")
	key := statusKey{ws.String(), uri.String()}
	host.names[key] = "build"
	h := &statusStoreTestHandler{}
	tab := b.NewTab(uri, 'b', "build", h, h)
	tabs := statusStoreTestTabs{component: b}
	v := store.View(nil)
	defer v.Close()
	for _, state := range []programstatus.State{programstatus.Done, programstatus.Error} {
		store.SetTabStatus(ws, tabs, uri, programstatus.Report{State: state, Progress: -1, App: "make", Msg: "result"})
		require.Len(t, v.entries, 1)
		assert.Equal(t, "build", v.entries[0].name)
		assert.Equal(t, state, v.entries[0].state)
		require.Nil(t, store.sources[key].records)
		changed := len(host.changed)
		tab.AckStatus()
		require.Len(t, v.entries, 1, "seen, the tab stays listed until it is removed")
		assert.Equal(t, programstatus.Idle, v.entries[0].state)
		assert.Equal(t, "idle · make", v.entries[0].sub, "it keeps the app it last reported")
		sum, ok := store.Status(ws, uri)
		require.True(t, ok)
		assert.Empty(t, sum.Records)
		assert.Len(t, host.changed, changed+1, "the workspace learns its status went")
		rollup, _ := store.Rollup(ws)
		assert.Empty(t, rollup.Records, "nothing it no longer reports weighs on the workspace")
	}
	store.SetTabStatus(ws, tabs, uri, programstatus.Report{ID: "working", State: programstatus.Working, Progress: -1})
	store.SetTabStatus(ws, tabs, uri, programstatus.Report{ID: "done", State: programstatus.Done, Progress: -1})
	store.EndTabStatus(ws, tabs, uri)
	sum, ok := store.Status(ws, uri)
	require.True(t, ok)
	require.Len(t, sum.Records, 1)
	assert.Equal(t, programstatus.Done, sum.State)
	changed := len(host.changed)
	require.True(t, b.RemoveTab(tab))
	assert.Empty(t, v.entries)
	_, ok = store.Status(ws, uri)
	assert.False(t, ok)
	assert.Len(t, host.changed, changed+1, "removed with a record, the workspace learns its status went once")

	// Seen before it is removed, the tab goes all the same.
	tab = b.NewTab(uri, 'b', "build", h, h)
	store.SetTabStatus(ws, tabs, uri, detachedTestReport(programstatus.Done, ""))
	tab.AckStatus()
	require.Len(t, v.entries, 1)
	require.True(t, b.RemoveTab(tab))
	assert.Empty(t, v.entries)
	assert.Empty(t, store.sources)

	// A tab that never reported leaves nothing behind.
	silent := statusStoreTestURI(t, "file:///silent")
	require.True(t, b.RemoveTab(b.NewTab(silent, 's', "silent", h, h)))
	assert.Empty(t, store.sources)

	tab = b.NewTab(uri, 'b', "build", h, h)
	require.True(t, b.RemoveTab(tab))
	require.Error(t, store.SetTabStatus(ws, tabs, uri, detachedTestReport(programstatus.Working, "")))
	require.Error(t, store.EndTabStatus(ws, tabs, uri))
	assert.Empty(t, store.sources, "a tab error must not create a detached source")
	delete(host.names, key)
	store.SetTabStatus(ws, tabs, uri, detachedTestReport(programstatus.Working, ""))
	require.Len(t, v.entries, 1)
	assert.Equal(t, detachedStatusLabel, v.entries[0].name)
	store.EndTabStatus(ws, tabs, uri)
	assert.Empty(t, v.entries)
}

func TestStatusStoreNotifications(t *testing.T) {
	host, store, ws := newStatusStoreTest(t)
	store.notify = DefaultNotifyConfig()
	store.notify.Interval = 0
	cfg := browser.DefaultConfig()
	cfg.OnTabStatus = func(uri workspaceapi.URI, _, next programstatus.Summary) { store.TabStatusChanged(ws, uri, next) }
	cfg.OnTabRemoved = func(uri workspaceapi.URI) { store.TabRemoved(ws, uri) }
	b := browser.NewComponent(cfg)
	t.Cleanup(func() { require.NoError(t, b.Close()) })
	uri := statusStoreTestURI(t, "rune-agent://model/rolling-fox")
	key := statusKey{ws.String(), uri.String()}
	host.names[key] = "rolling-fox"
	h := &statusStoreTestHandler{}
	tab := b.NewTab(uri, 'x', "rolling-fox", h, h)
	tabs := statusStoreTestTabs{component: b}
	working := func(msg string, progress, total int64) statusTestNote {
		return statusTestNote{level: browserapi.LevelInfo, msg: msg, progress: progress, total: total}
	}
	note := func(level browserapi.NotificationLevel, msg string) statusTestNote {
		return statusTestNote{level: level, msg: msg}
	}
	report := func(state programstatus.State, progress int, msg string) programstatus.Report {
		return programstatus.Report{State: state, Progress: progress, Msg: msg}
	}

	for _, step := range []struct {
		name   string
		report *programstatus.Report
		do     func()
		want   []statusTestNote
	}{{
		name:   "working without a message is not notified",
		report: new(report(programstatus.Working, -1, "")),
	}, {
		name:   "a working message stays, with its progress unknown",
		report: new(report(programstatus.Working, -1, "Compiling")),
		want:   []statusTestNote{working("rolling-fox: Compiling", 0, 1)},
	}, {
		name:   "the next working message replaces it",
		report: new(report(programstatus.Working, 40, "Linking")),
		want:   []statusTestNote{working("rolling-fox: Linking", 40, 100)},
	}, {
		name:   "it follows the progress",
		report: new(report(programstatus.Working, 60, "Linking")),
		want:   []statusTestNote{working("rolling-fox: Linking", 60, 100)},
	}, {
		name: "so do the working messages of child records",
		report: &programstatus.Report{
			ID: "tests", State: programstatus.Working, Progress: -1, App: "go", Msg: "ok ./internal",
		},
		want: []statusTestNote{working("rolling-fox (go): ok ./internal", 0, 1)},
	}, {
		name: "it outlives the notifications that close on their own",
		do:   host.expire,
		want: []statusTestNote{working("rolling-fox (go): ok ./internal", 0, 1)},
	}, {
		name:   "dismissed, progress alone does not bring it back",
		do:     func() { clear(host.shownNotes) },
		report: new(report(programstatus.Working, 70, "Linking")),
	}, {
		name:   "a message does",
		report: new(report(programstatus.Working, 80, "Testing")),
		want:   []statusTestNote{working("rolling-fox: Testing", 80, 100)},
	}, {
		name: "leaving working closes it",
		report: &programstatus.Report{
			State: programstatus.Blocked, Kind: programstatus.KindPermission,
			Progress: -1, App: "claude", Msg: "run make test?",
		},
		want: []statusTestNote{note(browserapi.LevelWarn, "rolling-fox (claude): run make test?")},
	}, {
		name: "a message replaces the last one of its state",
		report: &programstatus.Report{
			State: programstatus.Blocked, Kind: programstatus.KindPermission,
			Progress: -1, App: "claude", Msg: "edit main.go?",
		},
		want: []statusTestNote{note(browserapi.LevelWarn, "rolling-fox (claude): edit main.go?")},
	}, {
		name:   "leaving blocked closes it, as what it asked is settled",
		report: new(report(programstatus.Done, -1, "ok")),
		want:   []statusTestNote{note(browserapi.LevelSuccess, "rolling-fox: ok")},
	}, {
		name:   "each other state has its own",
		report: new(report(programstatus.Error, -1, "boom")),
		want: []statusTestNote{
			note(browserapi.LevelError, "rolling-fox: boom"),
			note(browserapi.LevelSuccess, "rolling-fox: ok"),
		},
	}, {
		name: "they close on their own",
		do:   host.expire,
	}, {
		name:   "a transition without a message names the state",
		report: new(report(programstatus.Done, -1, "")),
		want:   []statusTestNote{note(browserapi.LevelSuccess, "rolling-fox: done")},
	}, {
		name:   "and is replaced by a message in that state",
		report: new(report(programstatus.Done, -1, "shipped")),
		want:   []statusTestNote{note(browserapi.LevelSuccess, "rolling-fox: shipped")},
	}, {
		name: "a blocked message in the window the user looks at is not notified",
		do:   func() { host.inSight, host.seen[key] = true, true },
		report: &programstatus.Report{
			State: programstatus.Blocked, Kind: programstatus.KindQuestion,
			Progress: -1, App: "claude", Msg: "which database?",
		},
		want: []statusTestNote{note(browserapi.LevelSuccess, "rolling-fox: shipped")},
	}, {
		name: "out of sight, it is",
		do:   func() { host.inSight, host.seen[key] = false, false },
		report: &programstatus.Report{
			State: programstatus.Blocked, Kind: programstatus.KindQuestion,
			Progress: -1, App: "claude", Msg: "which table?",
		},
		want: []statusTestNote{
			note(browserapi.LevelWarn, "rolling-fox (claude): which table?"),
			note(browserapi.LevelSuccess, "rolling-fox: shipped"),
		},
	}, {
		name:   "working again closes it",
		report: new(report(programstatus.Working, 10, "Again")),
		want: []statusTestNote{
			working("rolling-fox: Again", 10, 100),
			note(browserapi.LevelSuccess, "rolling-fox: shipped"),
		},
	}, {
		name: "removing the tab closes its working notification",
		do:   func() { require.True(t, b.RemoveTab(tab)) },
		want: []statusTestNote{note(browserapi.LevelSuccess, "rolling-fox: shipped")},
	}} {
		if step.do != nil {
			step.do()
		}
		if step.report != nil {
			require.NoError(t, store.SetTabStatus(ws, tabs, uri, *step.report), step.name)
		}
		assert.Equal(t, step.want, host.onScreen(), step.name)
	}
	assert.Empty(t, store.notes, "a removed tab leaves no notification behind")

	shipped := note(browserapi.LevelSuccess, "rolling-fox: shipped")
	tab = b.NewTab(uri, 'x', "rolling-fox", h, h)
	require.NoError(t, store.SetTabStatus(ws, tabs, uri, programstatus.Report{
		State: programstatus.Blocked, Kind: programstatus.KindQuestion, Progress: -1, Msg: "which database?",
	}))
	assert.Equal(t, []statusTestNote{shipped, note(browserapi.LevelWarn, "rolling-fox: which database?")},
		host.onScreen())
	require.True(t, b.RemoveTab(tab))
	assert.Equal(t, []statusTestNote{shipped}, host.onScreen(), "a removed tab asks nothing anymore")
	assert.Empty(t, store.notes)

	// A terminal that is no tab, which goes once it holds no record.
	pty := statusStoreTestURI(t, "file:///dev/ttys001")
	clear(host.shownNotes)
	require.NoError(t, store.SetTabStatus(ws, tabs, pty, programstatus.Report{
		State: programstatus.Working, Progress: -1, App: "make", Msg: "building",
	}))
	assert.Equal(t, []statusTestNote{working("make: building", 0, 1)}, host.onScreen())
	require.NoError(t, store.EndTabStatus(ws, tabs, pty))
	assert.Empty(t, host.onScreen(), "its working notification goes with it")
	assert.Empty(t, store.notes)

	require.NoError(t, store.SetTabStatus(ws, tabs, pty, programstatus.Report{
		State: programstatus.Working, Progress: -1, App: "make", Msg: "building",
	}))
	require.NotEmpty(t, store.notes)
	store.DropWorkspace(ws)
	assert.Empty(t, store.notes, "the workspace takes its notifications along")
}

func TestStatusStoreNotifyInterval(t *testing.T) {
	host, store, ws := newStatusStoreTest(t)
	const interval = 50 * time.Millisecond
	store.notify = DefaultNotifyConfig()
	store.notify.Interval = interval
	uri := statusStoreTestURI(t, "file:///dev/ttys001")
	key := statusKey{ws.String(), uri.String()}
	send := func(reports ...programstatus.Report) {
		t.Helper()
		for _, r := range reports {
			require.NoError(t, store.SetTabStatus(ws, nil, uri, r))
		}
	}
	report := func(state programstatus.State, msg string) programstatus.Report {
		return programstatus.Report{State: state, Progress: -1, App: "make", Msg: msg}
	}
	taken := 0
	newNotes := func() []string {
		ret := host.notified[taken:]
		taken = len(host.notified)
		return ret
	}
	waitInterval := func() {
		time.Sleep(interval + 20*time.Millisecond)
		host.drain()
	}
	send(report(programstatus.Working, ""))

	store.lastNotified[key] = time.Now()
	send(report(programstatus.Done, ""), report(programstatus.Error, ""))
	assert.Empty(t, newNotes(), "rate limited")
	require.Eventually(t, func() bool {
		host.drain()
		return len(host.notified) > taken
	}, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"make: error"}, newNotes(),
		"transitions are coalesced into one notification, the latest state winning")

	store.lastNotified[key] = time.Now()
	send(report(programstatus.Done, ""), report(programstatus.Done, "shipped"))
	waitInterval()
	assert.Equal(t, []string{"make: shipped"}, newNotes(),
		"a message for the pending state replaces its deferred notification")

	store.lastNotified[key] = time.Now()
	send(report(programstatus.Error, ""), programstatus.Report{State: programstatus.Clear})
	waitInterval()
	assert.Empty(t, newNotes(), "a state gone before its deferred notification is not reported")
}

func TestStatusStoreSounds(t *testing.T) {
	host, store, ws := newStatusStoreTest(t)
	var played []string
	store.player = sounds.PlayerFunc(func(name string) { played = append(played, name) })
	store.SetNotifyConfig(DefaultNotifyConfig())
	uri := statusStoreTestURI(t, "file:///dev/ttys001")
	key := statusKey{ws.String(), uri.String()}
	report := func(state programstatus.State, msg string) *programstatus.Report {
		return &programstatus.Report{State: state, Progress: -1, App: "make", Msg: msg}
	}

	for _, step := range []struct {
		name   string
		do     func()
		report *programstatus.Report
		want   []string
	}{{
		name:   "working plays nothing by default",
		report: report(programstatus.Working, "building"),
	}, {
		name:   "done plays success",
		report: report(programstatus.Done, ""),
		want:   []string{sounds.Success},
	}, {
		name:   "staying done plays nothing",
		report: report(programstatus.Done, "shipped"),
	}, {
		name: "blocked plays warning, within the notify interval",
		do:   func() { store.lastNotified[key] = time.Now() },
		report: &programstatus.Report{
			State: programstatus.Blocked, Kind: programstatus.KindPermission, Progress: -1,
		},
		want: []string{sounds.Warning},
	}, {
		name:   "error plays error, in sight and seen",
		do:     func() { host.inSight, host.seen[key] = true, true },
		report: report(programstatus.Error, "boom"),
		want:   []string{sounds.Error},
	}, {
		name: "a state mapped to a sound plays it, one mapped to none plays nothing",
		do: func() {
			notify := DefaultNotifyConfig()
			notify.Sounds = map[programstatus.State]string{
				programstatus.Working: "tick", programstatus.Done: "",
			}
			store.SetNotifyConfig(notify)
		},
		report: report(programstatus.Working, "again"),
		want:   []string{"tick"},
	}, {
		name:   "unmapped",
		report: report(programstatus.Done, ""),
	}, {
		name: "a disabled status plays nothing",
		do:   func() { store.SetNotifyConfig(NotifyConfig{}) },
		report: &programstatus.Report{
			State: programstatus.Blocked, Kind: programstatus.KindQuestion, Progress: -1,
		},
	}} {
		played = nil
		if step.do != nil {
			step.do()
		}
		require.NoError(t, store.SetTabStatus(ws, nil, uri, *step.report), step.name)
		assert.Equal(t, step.want, played, step.name)
	}
}

func TestStatusStoreRestore(t *testing.T) {
	parse := func(raw string) workspaceapi.URI { return statusStoreTestURI(t, raw) }
	other, closed := parse("file:///other"), parse("file:///closed")
	// The tabs back, silent and live came back with the workspace, and
	// elsewhere with other; gone did not, and pty is a terminal that is
	// no tab.
	back, silent, live := parse("rune-agent://model/back"), parse("rune-agent://model/silent"),
		parse("rune-agent://model/live")
	gone, pty, elsewhere := parse("rune-agent://model/gone"), parse("file:///dev/ttys001"),
		parse("rune-agent://model/elsewhere")
	setup := func(t *testing.T) (*statusStoreTestHost, *Store, workspaceapi.URI, *View) {
		host, store, ws := newStatusStoreTest(t)
		host.addWorkspace(other, "2 other")
		for _, uri := range []workspaceapi.URI{back, silent, live} {
			host.names[statusKey{ws.String(), uri.String()}] = uri.Name()
		}
		host.names[statusKey{other.String(), elsewhere.String()}] = elsewhere.Name()
		v := store.View(nil)
		t.Cleanup(func() { _ = v.Close() })
		return host, store, ws, v
	}
	working := func(app string) programstatus.Summary {
		records := programstatus.New()
		records.Apply(programstatus.Report{State: programstatus.Working, Progress: -1, App: app})
		return records.Summary()
	}
	type row struct{ uri, sub string }
	rows := func(v *View) []row {
		var ret []row
		for _, e := range v.entries {
			ret = append(ret, row{e.uri.String(), e.sub})
		}
		return ret
	}

	suite := []struct {
		name string
		// reported is where a program reported working with the app
		// fresh before the restore, as a tab or as a terminal that is
		// no tab, whichever it is.
		reported []workspaceapi.URI
		// into is the workspace restored: the one setup opens when it
		// is zero.
		into     workspaceapi.URI
		programs []idehistory.Program
		want     []row
	}{
		{
			name: "with no program saved, no tab that came back is listed",
		},
		{
			name:     "a program whose tab came back is listed idle, with the app it last reported",
			programs: []idehistory.Program{{URI: back, App: "rune-agent"}},
			want:     []row{{back.String(), "idle · rune-agent"}},
		},
		{
			name:     "a program that reported no app is listed idle",
			programs: []idehistory.Program{{URI: back}},
			want:     []row{{back.String(), "idle"}},
		},
		{
			name:     "a program whose tab did not come back is not listed",
			programs: []idehistory.Program{{URI: gone, App: "rune-agent"}},
		},
		{
			name: "the programs whose tab came back are listed, those whose tab did not are not",
			programs: []idehistory.Program{
				{URI: gone, App: "a"}, {URI: live, App: "b"}, {URI: pty, App: "c"}, {URI: back, App: "d"},
			},
			want: []row{{back.String(), "idle · d"}, {live.String(), "idle · b"}},
		},
		{
			name:     "a program saved twice is listed once, with the app saved first",
			programs: []idehistory.Program{{URI: back, App: "first"}, {URI: back, App: "second"}},
			want:     []row{{back.String(), "idle · first"}},
		},
		{
			name:     "a program that reported since keeps what it reported",
			reported: []workspaceapi.URI{live},
			programs: []idehistory.Program{{URI: live, App: "stale"}},
			want:     []row{{live.String(), "working · fresh"}},
		},
		{
			name:     "a program saved for a terminal that is no tab is not listed",
			programs: []idehistory.Program{{URI: pty, App: "make"}},
		},
		{
			name:     "a terminal that is no tab and reported since keeps what it reported",
			reported: []workspaceapi.URI{pty},
			programs: []idehistory.Program{{URI: pty, App: "stale"}},
			want:     []row{{pty.String(), "working"}},
		},
		{
			name:     "a program whose tab came back in another workspace is not listed in this one",
			programs: []idehistory.Program{{URI: elsewhere, App: "rune-agent"}},
		},
		{
			name:     "a program is listed in the workspace restored only",
			into:     other,
			programs: []idehistory.Program{{URI: elsewhere, App: "a"}, {URI: back, App: "b"}},
			want:     []row{{elsewhere.String(), "idle · a"}},
		},
		{
			name:     "a program restored into a workspace that is not open is not listed",
			into:     closed,
			programs: []idehistory.Program{{URI: back, App: "rune-agent"}},
		},
	}
	for _, tcase := range suite {
		t.Run(tcase.name, func(t *testing.T) {
			host, store, ws, v := setup(t)
			for _, uri := range tcase.reported {
				if _, ok := host.TabName(ws, uri); ok {
					store.TabStatusChanged(ws, uri, working("fresh"))
					continue
				}
				store.updateDetached(ws, uri, func(r *programstatus.Store) {
					r.Apply(programstatus.Report{State: programstatus.Working, Progress: -1, App: "fresh"})
				})
			}
			into := cmp.Or(tcase.into, ws)
			changed := len(host.changed)
			sources := len(store.sources)

			store.Restore(into, tcase.programs)
			assert.Equal(t, tcase.want, rows(v))
			assert.Len(t, store.sources, sources+len(tcase.want)-len(tcase.reported),
				"what is not listed is not kept either")
			assert.Len(t, host.changed, changed, "what was restored weighs nothing on any workspace")
		})
	}

	t.Run("restoring twice lists a program once", func(t *testing.T) {
		_, store, ws, v := setup(t)
		programs := []idehistory.Program{{URI: back, App: "rune-agent"}}
		store.Restore(ws, programs)
		store.Restore(ws, programs)
		assert.Equal(t, []row{{back.String(), "idle · rune-agent"}}, rows(v))
	})

	t.Run("a restored program is saved as it was, for the next session to list it again", func(t *testing.T) {
		_, store, ws, _ := setup(t)
		programs := []idehistory.Program{{URI: back, App: "rune-agent"}, {URI: live}}
		store.Restore(ws, programs)
		assert.Equal(t, programs, store.Programs(ws))
		assert.Empty(t, store.Programs(other))

		_, next, _, v := setup(t)
		next.Restore(ws, store.Programs(ws))
		assert.Equal(t, []row{{back.String(), "idle · rune-agent"}, {live.String(), "idle"}}, rows(v))
	})

	t.Run("a restored program lists what it reports next, and keeps the app it reports", func(t *testing.T) {
		host, store, ws, v := setup(t)
		store.Restore(ws, []idehistory.Program{{URI: back, App: "stale"}})
		store.TabStatusChanged(ws, back, working("fresh"))
		assert.Equal(t, []row{{back.String(), "working · fresh"}}, rows(v))
		assert.Len(t, host.changed, 1, "what it reports weighs on its workspace")
		store.TabStatusChanged(ws, back, programstatus.Summary{Progress: -1})
		assert.Equal(t, []row{{back.String(), "idle · fresh"}}, rows(v))
		assert.Equal(t, []idehistory.Program{{URI: back, App: "fresh"}}, store.Programs(ws))
	})

	t.Run("a restored program goes with its tab", func(t *testing.T) {
		host, store, ws, v := setup(t)
		store.Restore(ws, []idehistory.Program{{URI: back, App: "rune-agent"}, {URI: live}})
		store.TabRemoved(ws, back)
		assert.Equal(t, []row{{live.String(), "idle"}}, rows(v))
		assert.Empty(t, host.changed, "as it reported nothing, no workspace learns it went")
		assert.Equal(t, []idehistory.Program{{URI: live}}, store.Programs(ws))
	})

	t.Run("a restored program goes with its workspace", func(t *testing.T) {
		_, store, ws, v := setup(t)
		store.Restore(ws, []idehistory.Program{{URI: back, App: "rune-agent"}})
		store.DropWorkspace(ws)
		assert.Empty(t, rows(v))
		assert.Empty(t, store.Programs(ws))
	})
}

func TestStatusSourceLabel(t *testing.T) {
	for _, tc := range []struct {
		name, app, want string
	}{
		{"rolling-fox", "", "rolling-fox"},
		{"rolling-fox", "rune-agent", "rolling-fox (rune-agent)"},
		{"cargo", "cargo", "cargo"},
		{detachedStatusLabel, "", detachedStatusLabel},
		{detachedStatusLabel, "make", "make"},
		{"", "make", "make"},
		{"", "", ""},
	} {
		assert.Equal(t, tc.want, statusSourceLabel(tc.name, tc.app), "%q, %q", tc.name, tc.app)
	}
}

func TestStatusStoreDetached(t *testing.T) {
	idle := programstatus.Summary{Progress: -1}
	parse := func(s string) workspaceapi.URI {
		uri, err := workspaceapi.ParseURI(s)
		require.NoError(t, err)
		return uri
	}
	ttyA := parse("file:///dev/ttys001")
	ttyB := parse("file:///dev/ttys002")
	clearAll := programstatus.Report{State: programstatus.Clear}
	apply := func(d *statusStoreDetachedTest, uri workspaceapi.URI, r programstatus.Report) (
		prev, status programstatus.Summary, changed bool,
	) {
		return d.update(uri, func(s *programstatus.Store) { s.Apply(r) })
	}

	t.Run("zero value reads as empty", func(t *testing.T) {
		var d statusStoreDetachedTest
		_, ok := d.get(ttyA)
		assert.False(t, ok)
		assert.Empty(t, d.list())
		sum, uri := d.rollup()
		assert.Equal(t, idle, sum)
		assert.Equal(t, workspaceapi.URI{}, uri)
	})

	t.Run("update reports both statuses and whether they differ", func(t *testing.T) {
		d := newStatusStoreDetachedTest(t)
		prev, status, changed := apply(&d, ttyA, detachedTestReport(programstatus.Working, ""))
		assert.True(t, changed)
		assert.Equal(t, idle, prev, "an unknown uri was idle")
		assert.Equal(t, programstatus.Working, status.State)

		working := status
		prev, status, changed = apply(&d, ttyA, detachedTestReport(programstatus.Working, ""))
		assert.False(t, changed, "the same report again is not a change")
		assert.True(t, working.Equal(prev))
		assert.True(t, working.Equal(status))

		prev, status, changed = apply(&d, ttyA, detachedTestReport(programstatus.Done, "ok"))
		assert.True(t, changed)
		assert.True(t, working.Equal(prev))
		got, ok := d.get(ttyA)
		require.True(t, ok)
		assert.True(t, status.Equal(got))
		assert.Equal(t, "ok", got.Msg)
	})

	t.Run("ending keeps done and error", func(t *testing.T) {
		d := newStatusStoreDetachedTest(t)
		_, _, _ = apply(&d, ttyA, programstatus.Report{ID: "w", State: programstatus.Working, Progress: -1})
		_, _, _ = apply(&d, ttyA, programstatus.Report{ID: "d", State: programstatus.Done, Progress: -1})
		_, status, changed := d.update(ttyA, func(s *programstatus.Store) { s.DropTransient() })
		assert.True(t, changed)
		assert.Equal(t, programstatus.Done, status.State)
		require.Len(t, status.Records, 1)
	})

	t.Run("no record left forgets the uri", func(t *testing.T) {
		d := newStatusStoreDetachedTest(t)
		_, done, _ := apply(&d, ttyA, detachedTestReport(programstatus.Done, "ok"))

		prev, status, changed := apply(&d, ttyA, clearAll)
		assert.True(t, changed)
		assert.True(t, done.Equal(prev))
		assert.Equal(t, idle, status)
		_, ok := d.get(ttyA)
		assert.False(t, ok)
		assert.Empty(t, d.list())

		_, _, changed = apply(&d, ttyA, clearAll)
		assert.False(t, changed, "clearing an unknown uri is not a change")
		assert.Empty(t, d.list())
	})

	t.Run("a reported idle is kept apart from silence", func(t *testing.T) {
		d := newStatusStoreDetachedTest(t)
		_, reportedIdle, changed := apply(&d, ttyA, detachedTestReport(programstatus.Idle, ""))
		assert.True(t, changed)
		require.Len(t, reportedIdle.Records, 1)
		got, ok := d.get(ttyA)
		require.True(t, ok)
		assert.True(t, reportedIdle.Equal(got))
		sum, uri := d.rollup()
		assert.True(t, reportedIdle.Equal(sum))
		assert.Equal(t, ttyA, uri)
	})

	t.Run("list is sorted by uri", func(t *testing.T) {
		d := newStatusStoreDetachedTest(t)
		_, _, _ = apply(&d, ttyB, detachedTestReport(programstatus.Working, ""))
		_, _, _ = apply(&d, ttyA, detachedTestReport(programstatus.Done, ""))
		got := d.list()
		require.Len(t, got, 2)
		assert.Equal(t, ttyA, got[0].URI)
		assert.Equal(t, programstatus.Done, got[0].Status.State)
		assert.Equal(t, ttyB, got[1].URI)
		assert.Equal(t, programstatus.Working, got[1].Status.State)
	})

	t.Run("rollup picks the most pressing status", func(t *testing.T) {
		d := newStatusStoreDetachedTest(t)
		_, _, _ = apply(&d, ttyA, detachedTestReport(programstatus.Working, ""))
		sum, uri := d.rollup()
		assert.Equal(t, programstatus.Working, sum.State)
		assert.Equal(t, ttyA, uri)

		_, _, _ = apply(&d, ttyB, detachedTestReport(programstatus.Error, "boom"))
		sum, uri = d.rollup()
		assert.Equal(t, programstatus.Error, sum.State)
		assert.Equal(t, "boom", sum.Msg)
		assert.Equal(t, ttyB, uri)

		_, _, _ = apply(&d, ttyA, detachedTestReport(programstatus.Blocked, "approve?"))
		sum, uri = d.rollup()
		assert.Equal(t, programstatus.Blocked, sum.State)
		assert.Equal(t, ttyA, uri)

		_, _, _ = apply(&d, ttyA, clearAll)
		_, _, _ = apply(&d, ttyB, clearAll)
		sum, uri = d.rollup()
		assert.Equal(t, idle, sum)
		assert.Equal(t, workspaceapi.URI{}, uri)
	})

	t.Run("rollup breaks ties by uri order", func(t *testing.T) {
		d := newStatusStoreDetachedTest(t)
		_, _, _ = apply(&d, ttyB, detachedTestReport(programstatus.Error, "b"))
		_, _, _ = apply(&d, ttyA, detachedTestReport(programstatus.Error, "a"))
		sum, uri := d.rollup()
		assert.Equal(t, "a", sum.Msg)
		assert.Equal(t, ttyA, uri)
	})
}

func TestStatusStoreAckDetached(t *testing.T) {
	report := func(id string, state programstatus.State) programstatus.Report {
		return programstatus.Report{ID: id, State: state, Progress: -1}
	}
	for _, tc := range []struct {
		name    string
		tab     bool
		reports []programstatus.Report
		// kept holds the ids of the records left, or nil when nothing
		// is listed.
		kept    []string
		changed bool
	}{{
		name: "nothing reported",
	}, {
		name: "done", reports: []programstatus.Report{report("", programstatus.Done)}, changed: true,
	}, {
		name: "error", reports: []programstatus.Report{report("", programstatus.Error)}, changed: true,
	}, {
		name: "idle", reports: []programstatus.Report{report("", programstatus.Idle)}, changed: true,
	}, {
		name: "working and blocked stay",
		reports: []programstatus.Report{
			report("w", programstatus.Working), report("b", programstatus.Blocked),
			report("e", programstatus.Error),
		},
		kept: []string{"b", "w"}, changed: true,
	}, {
		name:    "nothing to acknowledge",
		reports: []programstatus.Report{report("w", programstatus.Working)},
		kept:    []string{"w"},
	}, {
		name: "tab", tab: true,
		reports: []programstatus.Report{report("", programstatus.Error)},
		kept:    []string{""},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			host, store, ws := newStatusStoreTest(t)
			uri := statusStoreTestURI(t, "file:///dev/ttys001")
			if tc.tab {
				records := programstatus.New()
				for _, r := range tc.reports {
					records.Apply(r)
				}
				store.TabStatusChanged(ws, uri, records.Summary())
			} else {
				for _, r := range tc.reports {
					store.updateDetached(ws, uri, func(s *programstatus.Store) { s.Apply(r) })
				}
			}
			n := len(host.changed)

			store.Ack(ws, uri)

			assert.Equal(t, tc.changed, len(host.changed) != n, "the change is reported")
			status, listed := store.Status(ws, uri)
			if tc.kept == nil {
				assert.False(t, listed)
				assert.Empty(t, store.Items())
				return
			}
			require.True(t, listed)
			var ids []string
			for _, rec := range status.Records {
				ids = append(ids, rec.ID)
			}
			assert.Equal(t, tc.kept, ids)
		})
	}
}

func TestStatusStoreUpdateDetachedSeen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		seen   bool
		state  programstatus.State
		listed bool
		// quiet is whether its message goes unnotified.
		quiet bool
	}{
		{name: "done seen", seen: true, state: programstatus.Done},
		{name: "error seen", seen: true, state: programstatus.Error},
		{name: "idle seen", seen: true, state: programstatus.Idle},
		{name: "working seen", seen: true, state: programstatus.Working, listed: true},
		{name: "blocked seen", seen: true, state: programstatus.Blocked, listed: true, quiet: true},
		{name: "blocked out of sight", state: programstatus.Blocked, listed: true},
		{name: "error out of sight", state: programstatus.Error, listed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, store, ws := newStatusStoreTest(t)
			store.notify.Messages = true
			uri := statusStoreTestURI(t, "file:///dev/ttys001")
			host.seen[statusKey{ws.String(), uri.String()}] = tc.seen

			store.updateDetached(ws, uri, func(s *programstatus.Store) {
				s.Apply(programstatus.Report{State: tc.state, Progress: -1, App: "make", Msg: "boom"})
			})

			want := []string{"make: boom"}
			if tc.quiet {
				want = nil
			}
			assert.Equal(t, want, host.notified,
				"its message is notified all the same, unless the window shows what blocks it")
			_, listed := store.Status(ws, uri)
			assert.Equal(t, tc.listed, listed)
			sum, _ := store.Rollup(ws)
			wantState := programstatus.Idle
			if tc.listed {
				wantState = tc.state
			}
			assert.Equal(t, wantState, sum.State)
		})
	}
}
