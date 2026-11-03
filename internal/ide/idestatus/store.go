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

// Package idestatus tracks the statuses the programs of every workspace
// report: it rolls them up per workspace, notifies the user of them and
// lists them in the status inbox.
package idestatus

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/idehistory"
	"unstable.build/rune/internal/sounds"
	"unstable.build/rune/internal/term/programstatus"
)

// WorkspaceStatus is the host of a Store: the workspaces whose statuses
// it tracks. A Store calls it on the host event loop only.
type WorkspaceStatus interface {
	// Workspace returns the slot and the display name of the open
	// workspace at ws, or false when no open workspace is at ws.
	Workspace(ws workspaceapi.URI) (slot int, name string, ok bool)
	// TabName returns the name of the tab at uri in the workspace at ws,
	// or false when there is no such tab.
	TabName(ws, uri workspaceapi.URI) (string, bool)
	// Preview returns what shows uri in the workspace at ws, its tab or
	// its window, or false when nothing does.
	Preview(ws, uri workspaceapi.URI) (Preview, bool)
	// TabIcon returns the icon the tab at uri in the workspace at ws was
	// opened with, or false when there is no such tab.
	TabIcon(ws, uri workspaceapi.URI) (rune, bool)
	// InSight reports whether the workspace at ws is in focus and shows
	// uri: uri is its focused tab, or any terminal that is no tab, as
	// such a terminal floats over its workspace.
	InSight(ws, uri workspaceapi.URI) bool
	// Seen reports whether Rune is in focus and the focused window of
	// the workspace at ws, itself in focus, shows uri: the tab at uri,
	// or what reported at uri outside of a tab. Seen implies InSight.
	Seen(ws, uri workspaceapi.URI) bool
	// Notify raises a notification of msg at level in the workspace at
	// ws and returns its id, or "" when the workspace is closed.
	Notify(ws workspaceapi.URI, level browserapi.NotificationLevel, msg string) string
	// UpdateNotification sets the notification id of the workspace at
	// ws to progress of total, and to msg unless it is empty. The
	// notification then stays until progress reaches total, which
	// closes it. It reports false when the notification is gone.
	UpdateNotification(ws workspaceapi.URI, id, msg string, progress, total int64) bool
	// Changed tells that a status reported in the workspace at ws
	// changed.
	Changed(ws workspaceapi.URI)
	// Show brings uri in the workspace at ws in sight, or tells the user
	// why it cannot.
	Show(ws, uri workspaceapi.URI)
	// Close closes uri in the workspace at ws as tabclose does, statuses
	// included, or tells the user why it cannot.
	Close(ws, uri workspaceapi.URI)
	// Confirm asks the user message, a yes or no question, and calls yes
	// on a later tick if they answer yes.
	Confirm(message string, yes func())
}

// NotifyConfig selects what a Store notifies. The zero value notifies
// nothing.
type NotifyConfig struct {
	// States are the states that raise a notification when a program
	// enters them out of sight.
	States map[programstatus.State]bool
	// Interval is the least time between two such notifications of the
	// same program; the latest state wins.
	Interval time.Duration
	// Messages notifies every new message a program reports, in sight
	// or not, but a blocked one while seen.
	Messages bool
	// Sounds maps a state to the name of the sound a program entering it
	// plays, in sight or not, each time it enters it. A state it leaves
	// out, or maps to "", plays none.
	Sounds map[programstatus.State]string
}

// DefaultNotifyConfig notifies every message, and the blocked, error and
// done states at most every two seconds per program. Entering them plays
// the warning, error and success sounds.
func DefaultNotifyConfig() NotifyConfig {
	return NotifyConfig{
		States: map[programstatus.State]bool{
			programstatus.Blocked: true,
			programstatus.Error:   true,
			programstatus.Done:    true,
		},
		Interval: 2 * time.Second,
		Messages: true,
		Sounds: map[programstatus.State]string{
			programstatus.Blocked: sounds.Warning,
			programstatus.Error:   sounds.Error,
			programstatus.Done:    sounds.Success,
		},
	}
}

// Store tracks the statuses reported in every workspace by the program
// behind a tab or by a terminal that is no tab. It rolls them up per
// workspace, notifies them in one notification per program and state,
// and lists them live in the status inbox. A tab is listed until it is
// removed; a terminal that is no tab until it holds no record. Both are
// forgotten with their workspace. A Store is not safe for concurrent
// use: call it on the host event loop only. Use New to create one.
type Store struct {
	host        WorkspaceStatus
	cfg         browser.TabStatusConfig
	notify      NotifyConfig
	player      sounds.Player
	style       InboxStyle
	interrupter term.Interrupter
	schedule    func(func()) bool
	sources     map[statusKey]*statusSource
	// pendingNotify holds the sequence number of the deferred state
	// notification of each source, for a superseded one to drop itself.
	pendingNotify map[statusKey]uint64
	lastNotified  map[statusKey]time.Time
	notifySeq     uint64
	notes         map[noteKey]string
	views         map[*View]struct{}
}

// New returns an empty Store over the workspaces of host. cfg styles the
// statuses, notify selects what is notified, player plays the sounds it
// names, and style styles the inbox. schedule must run a function on a
// later tick of the host event loop. interrupter, if not nil, wakes the
// host to keep open inboxes live; without it, they stand still.
func New(
	host WorkspaceStatus, cfg browser.TabStatusConfig,
	notify NotifyConfig, player sounds.Player,
	style InboxStyle, schedule func(func()) bool, interrupter term.Interrupter,
) *Store {
	return &Store{
		host:          host,
		cfg:           cfg,
		notify:        notify,
		player:        player,
		style:         style,
		interrupter:   interrupter,
		schedule:      schedule,
		sources:       make(map[statusKey]*statusSource),
		lastNotified:  make(map[statusKey]time.Time),
		pendingNotify: make(map[statusKey]uint64),
		notes:         make(map[noteKey]string),
		views:         make(map[*View]struct{}),
	}
}

// SetNotifyConfig selects what s notifies from the next status reported
// on. A notification it deferred already is still raised if the state
// it waits for is one notify still notifies.
func (s *Store) SetNotifyConfig(notify NotifyConfig) {
	s.notify = notify
}

// SetTabStatus applies report, made by the program behind uri in the
// workspace at ws, to the tab at uri among tabs, the tabs of the
// workspace, or, when uri is no tab, to the records the store keeps for
// it.
func (s *Store) SetTabStatus(
	ws workspaceapi.URI, tabs browser.TabManager, uri workspaceapi.URI,
	report programstatus.Report,
) error {
	if _, ok := s.host.TabName(ws, uri); ok {
		return tabs.SetTabStatus(uri, report)
	}
	s.updateDetached(ws, uri, func(r *programstatus.Store) { r.Apply(report) })
	return nil
}

// EndTabStatus tells that the program behind uri in the workspace at ws
// ended, which drops its working, blocked and idle records, as
// SetTabStatus applies a report.
func (s *Store) EndTabStatus(
	ws workspaceapi.URI, tabs browser.TabManager, uri workspaceapi.URI,
) error {
	if _, ok := s.host.TabName(ws, uri); ok {
		return tabs.EndTabStatus(uri)
	}
	s.updateDetached(ws, uri, func(r *programstatus.Store) { r.DropTransient() })
	return nil
}

// TabStatusChanged records that the status of the tab at uri in the
// workspace at ws became status. The store learns about tabs from it
// alone, so every change of the status of a tab must be reported.
func (s *Store) TabStatusChanged(ws, uri workspaceapi.URI, status programstatus.Summary) {
	src, ok := s.sources[statusKey{ws: ws.String(), uri: uri.String()}]
	if !ok {
		src = &statusSource{ws: ws, uri: uri, status: programstatus.Summary{Progress: -1}}
	}
	wasDetached := src.records != nil
	src.records = nil
	s.update(src, status)
	if wasDetached {
		s.Refresh()
	}
}

// TabRemoved forgets the tab at uri in the workspace at ws and closes
// its working notification. It must be called once the tab is removed,
// as nothing else tells the store about a tab that holds no record, and
// before the loss of its status is reported, for its row in the inbox
// to go from where it is.
func (s *Store) TabRemoved(ws, uri workspaceapi.URI) {
	key := statusKey{ws: ws.String(), uri: uri.String()}
	src, ok := s.sources[key]
	if !ok || src.records != nil {
		return
	}
	delete(s.sources, key)
	s.closeNote(ws, key, programstatus.Working)
	s.closeNote(ws, key, programstatus.Blocked)
	s.forgetNotes(func(k statusKey) bool { return k == key })
	if len(src.status.Records) > 0 {
		s.host.Changed(ws)
	}
	s.Refresh()
}

// Programs returns the tabs listed for the workspace at ws, for Restore
// to list them again in a later session.
func (s *Store) Programs(ws workspaceapi.URI) []idehistory.Program {
	var ret []idehistory.Program
	for _, src := range s.sorted() {
		if src.records == nil && src.ws.String() == ws.String() {
			ret = append(ret, idehistory.Program{URI: src.uri, App: src.app})
		}
	}
	return ret
}

// Restore lists again, as having reported nothing yet, the programs of
// a previous session of the workspace at ws whose tab came back. It
// leaves alone what reported since.
func (s *Store) Restore(ws workspaceapi.URI, programs []idehistory.Program) {
	restored := false
	for _, p := range programs {
		key := statusKey{ws: ws.String(), uri: p.URI.String()}
		if _, ok := s.sources[key]; ok {
			continue
		}
		if _, ok := s.host.TabName(ws, p.URI); !ok {
			continue
		}
		s.sources[key] = &statusSource{
			ws: ws, uri: p.URI, status: programstatus.Summary{Progress: -1}, app: p.App,
		}
		restored = true
	}
	if restored {
		s.Refresh()
	}
}

// DropWorkspace forgets every status reported in the workspace at ws,
// and its notifications, which close with the workspace.
func (s *Store) DropWorkspace(ws workspaceapi.URI) {
	dropped := false
	for key := range s.sources {
		if key.ws == ws.String() {
			delete(s.sources, key)
			dropped = true
		}
	}
	for key := range s.lastNotified {
		if key.ws == ws.String() {
			delete(s.lastNotified, key)
		}
	}
	for key := range s.pendingNotify {
		if key.ws == ws.String() {
			delete(s.pendingNotify, key)
		}
	}
	s.forgetNotes(func(k statusKey) bool { return k.ws == ws.String() })
	if dropped {
		s.host.Changed(ws)
		s.Refresh()
	}
}

// Config returns the tab status styling the store was created with.
func (s *Store) Config() browser.TabStatusConfig {
	return s.cfg
}

// Status returns the status last reported at uri in the workspace at
// ws, or false when uri is not listed.
func (s *Store) Status(ws, uri workspaceapi.URI) (programstatus.Summary, bool) {
	src, ok := s.sources[statusKey{ws: ws.String(), uri: uri.String()}]
	if !ok {
		return programstatus.Summary{}, false
	}
	return src.status, true
}

// Rollup returns the most pressing status reported in the workspace at
// ws (blocked > error > done > working > reported idle) and the uri it
// was reported at, the first in uri order on a tie. When nothing was
// reported it returns an empty idle status and a zero uri.
func (s *Store) Rollup(ws workspaceapi.URI) (programstatus.Summary, workspaceapi.URI) {
	sum, uri := programstatus.Summary{Progress: -1}, workspaceapi.URI{}
	for _, src := range s.sorted() {
		if src.ws.String() == ws.String() && src.status.Outranks(sum) {
			sum, uri = src.status, src.uri
		}
	}
	return sum, uri
}

// Items returns the programs the inbox lists: what reported in every
// open workspace, sorted by workspace and uri.
func (s *Store) Items() []Item {
	var ret []Item
	for _, src := range s.sorted() {
		slot, wsName, ok := s.host.Workspace(src.ws)
		if !ok {
			continue
		}
		ret = append(ret, Item{
			Slot: slot, WS: src.ws, WorkspaceName: wsName,
			URI: src.uri, Name: s.name(src), App: src.app, Status: src.status,
		})
		if src.records == nil {
			ret[len(ret)-1].Icon, _ = s.host.TabIcon(src.ws, src.uri)
		}
	}
	return ret
}

// View returns a new status inbox, kept up to date until it is closed.
// minWidth, if not nil, returns the least width it asks for.
func (s *Store) View(minWidth func() int) *View {
	v := &View{store: s, hover: -1, minWidth: minWidth}
	v.set(statusInbox{cfg: s.cfg}.entries(s.Items()))
	s.views[v] = struct{}{}
	return v
}

// Ack drops the done, error and idle records kept for uri in the
// workspace at ws, which the user saw. A tab is left alone: its browser
// acknowledges it.
func (s *Store) Ack(ws, uri workspaceapi.URI) {
	src, ok := s.sources[statusKey{ws: ws.String(), uri: uri.String()}]
	if !ok || src.records == nil || !src.records.Ack() {
		return
	}
	s.update(src, src.records.Summary())
}

// Refresh lists the programs again in every open inbox. The store is
// not told when a tab or a workspace is renamed: call it then.
func (s *Store) Refresh() {
	if len(s.views) == 0 {
		return
	}
	entries := statusInbox{cfg: s.cfg}.entries(s.Items())
	for v := range s.views {
		v.set(entries)
	}
}

// Item is a program the status inbox lists.
type Item struct {
	// Slot is the slot of its workspace, at WS.
	Slot int
	WS   workspaceapi.URI
	// WorkspaceName is the workspace's display name.
	WorkspaceName string
	// URI is the uri it reported at: that of its tab, or of a terminal
	// that is no tab.
	URI workspaceapi.URI
	// Name is the name of its tab, or "terminal" for a terminal that is
	// no tab.
	Name string
	// App is the app it last reported, kept once Status reports none.
	App string
	// Icon is the icon the tab was opened with, or zero.
	Icon   rune
	Status programstatus.Summary
}

const detachedStatusLabel = "terminal"

func (s *Store) sorted() []*statusSource {
	ret := slices.Collect(maps.Values(s.sources))
	slices.SortFunc(ret, func(a, b *statusSource) int {
		return cmp.Or(
			strings.Compare(a.ws.String(), b.ws.String()),
			strings.Compare(a.uri.String(), b.uri.String()))
	})
	return ret
}

func (s *Store) name(src *statusSource) string {
	if src.records != nil {
		return detachedStatusLabel
	}
	name, _ := s.host.TabName(src.ws, src.uri)
	return name
}

func (s *Store) updateDetached(
	ws, uri workspaceapi.URI, fn func(*programstatus.Store),
) {
	src, ok := s.sources[statusKey{ws: ws.String(), uri: uri.String()}]
	if !ok || src.records == nil {
		src = &statusSource{
			ws: ws, uri: uri, records: programstatus.New(),
			status: programstatus.Summary{Progress: -1},
		}
	}
	fn(src.records)
	s.update(src, src.records.Summary())
	if s.host.Seen(ws, uri) && src.records.Ack() {
		s.update(src, src.records.Summary())
	}
}

func (s *Store) update(src *statusSource, status programstatus.Summary) {
	if src.status.Equal(status) {
		return
	}
	prev := src.status
	src.status = status
	if status.App != "" {
		src.app = status.App
	}
	if src.records != nil && len(status.Records) == 0 {
		delete(s.sources, src.key())
	} else {
		s.sources[src.key()] = src
	}
	s.host.Changed(src.ws)
	s.announce(src, prev)
	if _, ok := s.sources[src.key()]; !ok {
		key := src.key()
		s.forgetNotes(func(k statusKey) bool { return k == key })
	}
	s.Refresh()
}

func (s *Store) announce(src *statusSource, prev programstatus.Summary) {
	key, status := src.key(), src.status
	s.followWorking(src, prev)
	if prev.State == programstatus.Blocked && status.State != programstatus.Blocked {
		s.closeNote(src.ws, key, programstatus.Blocked)
	}
	if name := s.notify.Sounds[status.State]; name != "" && prev.State != status.State {
		s.player.Play(name)
	}
	announced := false
	if s.notify.Messages {
		// In sight or not, as nothing else shows the message, but for a
		// blocked one seen: the window shows what the program asks.
		seen := s.host.Seen(src.ws, src.uri)
		for _, rec := range newStatusMessages(prev, status) {
			if seen && rec.State == programstatus.Blocked {
				continue
			}
			s.post(src, rec.State, statusSourceLabel(s.name(src), rec.App)+": "+rec.Msg)
			announced = announced || rec.State == status.State
		}
	}
	if announced {
		delete(s.pendingNotify, key)
	}
	if prev.State == status.State || !s.notify.States[status.State] {
		return
	}
	if announced {
		s.lastNotified[key] = time.Now()
		return
	}
	if _, pending := s.pendingNotify[key]; pending {
		return
	}
	wait := s.notify.Interval - time.Since(s.lastNotified[key])
	if wait <= 0 {
		s.notifyState(key)
		return
	}
	s.notifySeq++
	seq := s.notifySeq
	s.pendingNotify[key] = seq
	time.AfterFunc(wait, func() {
		debug.CapturePanicReport(func() {
			_ = s.schedule(func() {
				if pending, ok := s.pendingNotify[key]; !ok || pending != seq {
					return
				}
				delete(s.pendingNotify, key)
				s.notifyState(key)
			})
		})
	})
}

func (s *Store) notifyState(key statusKey) {
	src, ok := s.sources[key]
	if !ok || !s.notify.States[src.status.State] || s.host.InSight(src.ws, src.uri) {
		return
	}
	s.lastNotified[key] = time.Now()
	status := src.status
	// Messages are notified on their own: name the state only.
	text := status.State.String()
	if status.State == programstatus.Blocked && status.Kind != programstatus.KindNone {
		text += " (" + status.Kind.String() + ")"
	}
	if status.Title != "" {
		text = status.Title + " — " + text
	}
	s.post(src, status.State, statusSourceLabel(s.name(src), status.App)+": "+text)
}

func (s *Store) post(src *statusSource, state programstatus.State, msg string) {
	key := noteKey{src.key(), state}
	pinned := state == programstatus.Working && src.status.State == programstatus.Working
	progress, total := notificationProgress(src.status.Progress)
	if id, ok := s.notes[key]; ok {
		delete(s.notes, key)
		if pinned && s.host.UpdateNotification(src.ws, id, msg, progress, total) {
			s.notes[key] = id
			return
		}
		if !pinned {
			// What closes on its own cannot be updated without staying.
			s.host.UpdateNotification(src.ws, id, "", 1, 1)
		}
	}
	id := s.host.Notify(src.ws, statusNotificationLevel(state), msg)
	if id == "" || pinned && !s.host.UpdateNotification(src.ws, id, "", progress, total) {
		return
	}
	s.notes[key] = id
}

func (s *Store) followWorking(src *statusSource, prev programstatus.Summary) {
	key := noteKey{src.key(), programstatus.Working}
	id, ok := s.notes[key]
	if !ok {
		return
	}
	switch {
	case src.status.State != programstatus.Working:
		if prev.State == programstatus.Working {
			s.closeNote(src.ws, src.key(), programstatus.Working)
		}
	case src.status.Progress != prev.Progress:
		progress, total := notificationProgress(src.status.Progress)
		if !s.host.UpdateNotification(src.ws, id, "", progress, total) {
			delete(s.notes, key)
		}
	}
}

// closeNote closes the notification of key in state, which a working
// one, pinned to follow the progress, never does on its own.
func (s *Store) closeNote(ws workspaceapi.URI, key statusKey, state programstatus.State) {
	note := noteKey{key, state}
	id, ok := s.notes[note]
	if !ok {
		return
	}
	delete(s.notes, note)
	s.host.UpdateNotification(ws, id, "", 1, 1)
}

func (s *Store) forgetNotes(match func(statusKey) bool) {
	for key := range s.notes {
		if match(key.statusKey) {
			delete(s.notes, key)
		}
	}
}

type statusKey struct {
	ws, uri string
}

type noteKey struct {
	statusKey
	state programstatus.State
}

type statusSource struct {
	ws, uri workspaceapi.URI
	// records is nil for a tab, whose records its browser holds.
	records *programstatus.Store
	status  programstatus.Summary
	// app outlives the records, for a tab to keep showing it.
	app string
}

func (src *statusSource) key() statusKey {
	return statusKey{ws: src.ws.String(), uri: src.uri.String()}
}

func notificationProgress(progress int) (int64, int64) {
	if progress < 0 {
		return 0, 1
	}
	// Reaching the total would close the notification.
	return int64(min(progress, 99)), 100
}

func newStatusMessages(prev, status programstatus.Summary) []programstatus.Record {
	var ret []programstatus.Record
	for _, rec := range status.Records {
		if rec.Msg == "" {
			continue
		}
		i := slices.IndexFunc(prev.Records, func(p programstatus.Record) bool {
			return p.ID == rec.ID
		})
		if i < 0 || prev.Records[i].Msg != rec.Msg {
			ret = append(ret, rec)
		}
	}
	return ret
}

func statusNotificationLevel(state programstatus.State) browserapi.NotificationLevel {
	switch state {
	case programstatus.Blocked:
		return browserapi.LevelWarn
	case programstatus.Error:
		return browserapi.LevelError
	case programstatus.Done:
		return browserapi.LevelSuccess
	}
	return browserapi.LevelInfo
}

func statusSourceLabel(name, app string) string {
	switch {
	case app == "" || app == name:
		return name
	case name == "" || name == detachedStatusLabel:
		return app
	}
	return name + " (" + app + ")"
}
