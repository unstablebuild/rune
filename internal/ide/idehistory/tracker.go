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

package idehistory

import (
	"context"
	"sort"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/workspace"
)

// tracker subscribes to editor events for one workspace URI and
// translates them into Store calls. It is created by
// Store.SubscribeEvents and removed by Closer.Close.
//
// tracker is not safe for concurrent use; it relies on Store's
// single-goroutine invariant.
type tracker struct {
	store *Store
	uri   workspaceapi.URI
	snap  Snapshotter
	ed    text.Editor
	// ctx is the context handed to SubscribeEvents; it is used for
	// the Store calls fired off from event handlers.
	ctx context.Context

	files map[string]File // keyed by URI string
	// skip lists pseudo-buffer namespaces whose events should be
	// ignored entirely, matched by path prefix.
	skip []workspaceapi.URI
}

// Handle implements text.EventHandler. Open/Close/Flush events trigger
// a Store call (after enriching with snapshotter state). Cursor/Edit
// events update in-memory state but do not persist immediately —
// they're picked up at the next snapshot.
func (t *tracker) Handle(ctx context.Context, ev textapi.Event) bool {
	if ev.URI == (workspaceapi.URI{}) {
		return false
	}
	uriStr := ev.URI.String()
	for _, s := range t.skip {
		if workspace.URIUnderPrefix(ev.URI, s) {
			return false
		}
	}
	prev, ok := t.files[uriStr]

	switch ev.Type {
	case textapi.EventTypeOpen:
		t.files[uriStr] = File{
			URI:      ev.URI,
			Dirty:    false,
			OpenAt:   time.Now(),
			Cursor:   prev.Cursor,
			WindowID: prev.WindowID,
		}
		t.persist()
	case textapi.EventTypeClose:
		delete(t.files, uriStr)
		t.persist()
	case textapi.EventTypeFlush:
		if !ok {
			return false
		}
		t.files[uriStr] = File{
			URI:      ev.URI,
			Dirty:    false,
			OpenAt:   prev.OpenAt,
			Cursor:   prev.Cursor,
			WindowID: prev.WindowID,
		}
		t.persist()
	case textapi.EventTypeCursor:
		t.files[uriStr] = File{
			URI:      ev.URI,
			Dirty:    prev.Dirty,
			OpenAt:   prev.OpenAt,
			Cursor:   ev.From,
			WindowID: prev.WindowID,
		}
	case textapi.EventTypeEdit:
		t.files[uriStr] = File{
			URI:      ev.URI,
			Dirty:    true,
			OpenAt:   prev.OpenAt,
			Cursor:   prev.Cursor,
			WindowID: prev.WindowID,
		}
	}
	return false
}

// persist assembles a State from the in-memory file map and the
// snapshotter, then writes it via the Store. Terminal snapshots are
// deliberately left out: they are captured once, at close.
func (t *tracker) persist() {
	state := t.buildState()
	if err := t.store.storeWorkspaceStateDocument(t.ctx, t.uri, state); err != nil {
		log.WithFields(log.Fields{logging.KeyClass: "ide.idehistory"}).
			Warnf("persist workspace state: %v", err)
	}
}

// buildState gathers everything but the terminal snapshots.
func (t *tracker) buildState() State {
	files := make([]File, 0, len(t.files))
	for _, f := range t.files {
		files = append(files, f)
	}
	if t.snap != nil {
		if winIDs := t.snap.FileWindowIDs(); len(winIDs) > 0 {
			for i, f := range files {
				if wid, ok := winIDs[f.URI.String()]; ok {
					f.WindowID = wid
					files[i] = f
				}
			}
		}
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].OpenAt.Before(files[j].OpenAt)
	})
	state := State{Files: files}
	if t.snap != nil {
		layout, hasLayout := t.snap.Layout()
		state.Layout = layout
		state.HasLayout = hasLayout
		state.Tasks = t.snap.Tasks()
		state.Extensions = t.snap.ExtensionTabs()
		state.Name = t.snap.Name()
	}
	return state
}

// Close unsubscribes from the editor's events and removes the tracker
// from the parent Store.
func (t *tracker) Close() error {
	delete(t.store.trackers, t.uri.String())
	if t.ed != nil {
		if _, err := t.ed.UnsubscribeEvents(t); err != nil {
			log.WithFields(log.Fields{logging.KeyClass: "ide.idehistory"}).
				Warnf("unsubscribe tracker events: %v", err)
		}
	}
	return nil
}

// trackerSnapshot returns the in-memory tracker state for uri, if a
// tracker exists for it. The returned State reflects events received
// since Track was called.
func (s *Store) trackerSnapshot(uri workspaceapi.URI) (State, bool) {
	t, ok := s.trackers[uri.String()]
	if !ok {
		return State{}, false
	}
	return t.buildState(), true
}
