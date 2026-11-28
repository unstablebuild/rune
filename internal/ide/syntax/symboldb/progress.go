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

package symboldb

import (
	"fmt"
	"path"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
)

// Progress is told how a scan of the workspace goes. Its methods are
// called one at a time. A scan that finds no file to parse calls neither
// Start nor File.
type Progress interface {
	// Start says that total files are to be parsed.
	Start(total int)
	// File says that rel, the file at index done of total, is parsed.
	File(done, total int, rel string)
	// Done says that the scan completed after parsing files.
	Done(files int)
	// Stop says why the scan did not complete.
	Stop(err error)
}

// NotifyProgress returns a Progress that reports in one notification.
// Its calls to notify go through schedule, which runs a closure on the
// editor event loop.
func NotifyProgress(notify browserapi.Notifications, schedule func(func()) bool) Progress {
	return &notifyProgress{notify: notify, schedule: schedule, dirs: map[string]struct{}{}}
}

// notifyProgress posts and updates its notification on the event loop,
// where only id is used.
type notifyProgress struct {
	notify   browserapi.Notifications
	schedule func(func()) bool
	id       string
	dirs     map[string]struct{}
}

func (p *notifyProgress) Start(int) {
	p.schedule(func() {
		id, err := p.notify.Notify(browserapi.LevelInfo, "Indexing workspace symbols")
		if err == nil {
			p.id = id
		}
	})
}

// File only reports the first file of each folder: files are parsed in
// parallel, so those of a folder need not come one after the other.
func (p *notifyProgress) File(done, total int, rel string) {
	dir := path.Dir(rel)
	if _, ok := p.dirs[dir]; ok {
		return
	}
	p.dirs[dir] = struct{}{}
	p.schedule(func() {
		if p.id != "" {
			_ = p.notify.UpdateNotificationProgress(p.id,
				"Indexing symbols: "+dir, int64(done), int64(total))
		}
	})
}

func (p *notifyProgress) Done(files int) {
	p.schedule(func() {
		if p.id == "" {
			return
		}
		noun := "files"
		if files == 1 {
			noun = "file"
		}
		total := int64(max(files, 1))
		_ = p.notify.UpdateNotificationProgress(p.id,
			fmt.Sprintf("Indexed the symbols of %d %s", files, noun), total, total)
	})
}

// Stop finishes the notification, since a progress notification
// otherwise stays open until it is complete.
func (p *notifyProgress) Stop(err error) {
	p.schedule(func() {
		if p.id != "" {
			_ = p.notify.UpdateNotificationProgress(p.id,
				"Indexing symbols stopped: "+err.Error(), 1, 1)
		}
	})
}

// REPLProgress returns a Progress that reports the files parsed to pw,
// which draws them as the bar of a running REPL command.
func REPLProgress(pw repl.ProgressWriter) Progress {
	return replProgress{pw: pw}
}

type replProgress struct {
	pw repl.ProgressWriter
}

func (p replProgress) Start(total int) { p.pw.Progress(0, int64(total), "files") }

func (p replProgress) File(done, total int, _ string) {
	p.pw.Progress(int64(done+1), int64(total), "files")
}

func (replProgress) Done(int)   {}
func (replProgress) Stop(error) {}
