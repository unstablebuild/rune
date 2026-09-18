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
	"errors"
	"os"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/workspace"
)

func dispatchFilesystemEvents(
	ctx context.Context, ex *ex, mu sync.Locker,
	ch chan schemeapi.EventInfo, ignores vctrl.Matcher,
) {
	for {
		select {
		case fsev, ok := <-ch:
			if !ok {
				return
			}
			dispatchFilesystemEvent(ex, mu, ignores, fsev)
		case <-ctx.Done():
			return
		}
	}
}

func dispatchFilesystemEvent(
	ex *ex, mu sync.Locker, ignores vctrl.Matcher, fsev schemeapi.EventInfo,
) {
	uri := fsev.URI()
	flag := fsev.Event()

	ex.log(log.TraceLevel, "received filesystem event %s for %s", flag, uri)

	isDir, _ := fsev.IsDir()
	if ignores.Match(uri, isDir) {
		return
	}

	ex.log(log.DebugLevel, "dispatching filesystem event %s for %s", flag, uri)

	ev := textapi.Event{
		URI: uri,
	}
	switch flag {
	case schemeapi.Create:
		ev.Type = textapi.EventTypeCreate
	case schemeapi.Write:
		ev.Type = textapi.EventTypeChange
	case schemeapi.Rename:
		ev.Type = textapi.EventTypeRename
	case schemeapi.Remove:
		ev.Type = textapi.EventTypeRemove
	default:
		ex.log(log.WarnLevel, "extraneous filesystem event %s for %s", flag, uri)
		return
	}

	mu.Lock()
	handleFSChange(ex, flag, uri)
	ex.comp.DispatchEvent(ev)
	mu.Unlock()
}

func handleFSChange(ex *ex, flag schemeapi.Event, uri workspaceapi.URI) {
	t, open := ex.comp.Resource(uri)
	dirty, _ := ex.comp.IsDirty(uri)
	ex.log(log.DebugLevel, "handling event %d for file %s, open=%t dirty=%t",
		flag, uri.Name(), open, dirty)
	if !open {
		return
	}
	if ex.ed.IsExternal() {
		// Externally-managed editors (e.g. exo) install their own
		// watcher and rewrite the cell.Buffer mirror directly. The
		// IDE-level watcher must not call ReloadTab or surface the
		// "changed on disk and was reloaded" notification — that
		// would duplicate work and spam the user every time the
		// external editor saves.
		ex.log(log.TraceLevel,
			"skipping ide-level fs change for %s: external editor",
			uri.Name())
		return
	}

	// A save can emit Create/Write/Rename before publishing its final mtime.
	// Recheck after completion rather than treating our intermediate state as
	// an external edit, or dropping a real external change that arrived during it.
	if ex.comp.AfterSettled(uri, func() {
		if current, ok := ex.comp.Resource(uri); !ex.closed && ok && current == t {
			handleFSChange(ex, flag, uri)
		}
	}) {
		return
	}

	var modTime time.Time
	lastFlush, _ := ex.comp.LastFlush(t)
	info, err := ex.workspace.Stat(uri.Path())
	if err != nil && !os.IsNotExist(err) {
		if os.IsPermission(err) {
			return
		}
		ex.log(log.WarnLevel, "could not stat file to "+
			"dispatch fs change prompt: %v", err)
		return
	} else if err == nil {
		modTime = info.ModTime()
	}
	if lastFlush.Equal(modTime) && !modTime.IsZero() { // both zero might be a removed file
		ex.log(log.TraceLevel, "ignoring fs %d event: user flushed file", flag)
		return
	}

	ex.log(log.TraceLevel, "continuing processing with fs %d event: "+
		"last flush %s is before mod time %s",
		flag, lastFlush, modTime)

	if dirty {
		switch flag {
		case schemeapi.Create:
			ex.openFileChangedPrompt(uri, t, "created on", true)
		case schemeapi.Write:
			ex.openFileChangedPrompt(uri, t, "changed on", true)
		case schemeapi.Rename:
			_, err := ex.workspace.Stat(uri.Path())
			if err == nil {
				ex.openFileChangedPrompt(uri, t, "renamed into", true)
			} else if os.IsNotExist(err) {
				ex.openFileChangedPrompt(uri, t, "renamed on", false)
			} else {
				ex.config.ScheduleNextTick(func() {
					_, _ = ex.comp.Notify(browserapi.LevelError,
						"Failed to reload renamed file %s: stat: %v",
						uri.Path(), err)
				})
			}
		case schemeapi.Remove:
			ex.openFileChangedPrompt(uri, t, "removed from", false)
		}
		return
	}

	switch flag {
	case schemeapi.Create, schemeapi.Write:
		startReloadAndNotify(ex, uri, t, "changed on disk")

	case schemeapi.Rename:
		_, err := ex.workspace.Stat(uri.Path())
		if err == nil {
			startReloadAndNotify(ex, uri, t, "was renamed on disk")
			return
		}
		if os.IsNotExist(err) {
			if err := ex.comp.RemoveTab(t); err == nil {
				ex.config.ScheduleNextTick(func() {
					_, _ = ex.comp.Notify(browserapi.LevelInfo,
						"File '%s' was renamed on disk and does not have "+
							"unflushed changes so it was closed", uri.Name())
				})
			}
			return
		}
		ex.config.ScheduleNextTick(func() {
			_, _ = ex.comp.Notify(browserapi.LevelError,
				"Failed to reload renamed file %s: stat: %v", uri.Name(), err)
		})

		// don't manage schemeapi.Remove: it's sometimes dispatched
		// in conjunction with other events so it's not useful.
	}
}

// startReloadAndNotify kicks off an async reload via ex.flusher and
// emits a user-facing notification once the underlying reparse has
// settled. The reparse that gates the reload result is scheduled
// onto the host event loop (see syntax.Tree.wrapReparse) so the
// awaiter must run off the host goroutine; ex.flusher schedules the
// final callback back onto the host scheduler, where the
// notification is safe to emit.
func startReloadAndNotify(
	ex *ex, uri workspaceapi.URI, t browserapi.Handler, reason string,
) {
	err := ex.flusher.reloadAsync(uri, t, func() {
		_, _ = ex.comp.Notify(browserapi.LevelInfo,
			"File '%s' %s and does not have unflushed changes "+
				"so it was reloaded", uri.Name(), reason)
	})
	if err == nil || errors.Is(err, workspace.ErrFlushInProgress) {
		// ErrFlushInProgress just means another reload (or save) for
		// the same URI is already running; that one will dispatch its
		// own notification.
		return
	}
	ex.config.ScheduleNextTick(func() {
		_, _ = ex.comp.Notify(browserapi.LevelError,
			"Failed to reload file %s: %v", uri.Name(), err)
	})
}
