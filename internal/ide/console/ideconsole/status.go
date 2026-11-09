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

package ideconsole

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
)

// TabManager is the subset of browserapi.WindowManager a Handler
// reports the status of the commands it runs through. SetTabStatus is
// called from the goroutines the commands run on, so it must be safe
// for concurrent use and must not wait on the event loop the Handler
// runs on.
type TabManager interface {
	SetTabStatus(uri workspaceapi.URI, status browserapi.TabStatus) error
}

// statusTitleMaxBytes is the limit browserapi.TabStatus puts on Title.
const statusTitleMaxBytes = 192

// commandStatus reports every command a Handler runs as a record of
// the status of the Handler's tab: working, with its progress, while it
// runs, then done or error. An interrupted command drops its record,
// as the user who interrupted it needs no telling.
type commandStatus struct {
	tabs TabManager
	uri  workspaceapi.URI

	mu     sync.Mutex
	seq    uint64
	closed bool
}

// start reports cmd working and returns its run, which forwards
// progress to pw.
func (s *commandStatus) start(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) *commandRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	r := &commandRun{
		ctx:    ctx,
		pw:     pw,
		status: s,
		last: browserapi.TabStatus{
			ID:       strconv.FormatUint(s.seq, 10),
			State:    browserapi.TabWorking,
			Progress: -1,
			Title:    commandTitle(cmd),
		},
	}
	s.reportLocked(r.last)
	return r
}

// close stops reporting for good, as the tab goes away with every
// record it holds.
func (s *commandStatus) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

func (s *commandStatus) reportLocked(status browserapi.TabStatus) {
	if !s.closed {
		_ = s.tabs.SetTabStatus(s.uri, status)
	}
}

// commandRun is the status of one command from start to end. It is the
// ProgressWriter the command reports its progress to.
type commandRun struct {
	ctx    context.Context
	pw     repl.ProgressWriter
	status *commandStatus

	// last and ended are guarded by status.mu.
	last  browserapi.TabStatus
	ended bool
}

var _ repl.ProgressWriter = (*commandRun)(nil)

// Progress satisfies repl.ProgressWriter. A total that is not positive
// is indeterminate.
func (r *commandRun) Progress(progress, total int64, units string) {
	r.pw.Progress(progress, total, units)
	pct := -1
	if total > 0 {
		pct = int(min(max(progress, 0), total) * 100 / total)
	}
	r.status.mu.Lock()
	defer r.status.mu.Unlock()
	if r.ended || r.last.Progress == pct {
		return
	}
	r.last.Progress = pct
	r.status.reportLocked(r.last)
}

// end reports the command ended with err. Only the first call counts.
func (r *commandRun) end(err error) {
	r.status.mu.Lock()
	defer r.status.mu.Unlock()
	if r.ended {
		return
	}
	r.ended = true
	switch {
	case r.ctx.Err() != nil:
		r.last.State = browserapi.TabClear
	case err != nil:
		r.last.State = browserapi.TabError
	default:
		r.last.State = browserapi.TabDone
	}
	r.last.Progress = -1
	r.status.reportLocked(r.last)
}

// commandTitle returns the command line cut to fit the title of a
// status.
func commandTitle(cmd repl.Command) string {
	line := strings.Map(func(c rune) rune {
		if unicode.IsControl(c) {
			return -1
		}
		return c
	}, strings.Join(append([]string{cmd.Name}, cmd.Args...), " "))
	if len(line) <= statusTitleMaxBytes {
		return line
	}
	end := statusTitleMaxBytes - len("…")
	for !utf8.RuneStart(line[end]) {
		end--
	}
	return line[:end] + "…"
}
