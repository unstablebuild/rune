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

package acpcmd

import (
	"context"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/unstablebuild/rune-go-sdk/api/acpapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/term/programstatus"
)

// StatusReporter takes the reports of sessions no chat shows, made from
// any goroutine at uris no tab has. browser.TabManager is one.
type StatusReporter interface {
	SetTabStatus(uri workspaceapi.URI, report programstatus.Report) error
}

// Limits on what a status carries, within programstatus's own.
const (
	statusTitleMaxBytes = 192
	statusMsgMaxBytes   = 2048
)

// away is a session whose last chat closed while it worked. Until a chat
// shows it again or it ends, it reports what it does at uri, which no tab
// has, for the status inbox to list it.
type away struct {
	name, agent string
	uri         workspaceapi.URI
	// status is nil when nothing takes the reports.
	status StatusReporter
	// ctx ends once a stops.
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	stopped  bool
	reported bool
	last     programstatus.Report
}

func newAway(name, agent string, uri workspaceapi.URI, status StatusReporter) *away {
	ctx, cancel := context.WithCancel(context.Background())
	return &away{name: name, agent: agent, uri: uri, status: status, ctx: ctx, cancel: cancel}
}

// report reports what the session does in state st, which end, if not
// nil, brought to an end, unless a stopped or reported it last.
func (a *away) report(st acpapi.SessionState, end *acpapi.TurnEnd) {
	r, err := programstatus.Normalize(awayReport(a.name, a.agent, st, end))
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped || a.status == nil || (a.reported && a.last == r) {
		return
	}
	a.reported, a.last = true, r
	_ = a.status.SetTabStatus(a.uri, r)
}

// stop ends a and withdraws what it reported.
func (a *away) stop() {
	a.cancel()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped {
		return
	}
	a.stopped = true
	if a.reported {
		_ = a.status.SetTabStatus(a.uri, programstatus.Report{State: programstatus.Clear, Progress: -1})
	}
}

// working reports whether a session in state st needs no chat to go
// on: it runs a turn or waits for a permission.
func working(st acpapi.SessionState) bool {
	return st.Busy || len(st.Pending) > 0
}

// awayReport is what session name of agent reports in state st, which
// end, if not nil, brought to an end. It is labelled with the session's
// name, or its agent's when the name cannot label a status.
func awayReport(name, agent string, st acpapi.SessionState, end *acpapi.TurnEnd) programstatus.Report {
	r := programstatus.Report{Progress: -1, App: name}
	if n, _ := programstatus.Normalize(r); n.App == "" {
		r.App = agent
	}
	switch {
	case len(st.Pending) > 0:
		title := "Allow the agent to continue?"
		if t := st.Pending[0].Request.ToolCall.Title; t != nil && *t != "" {
			title = *t
		}
		r.State, r.Kind = programstatus.Blocked, programstatus.KindPermission
		r.Title, r.Msg = statusText(title, statusTitleMaxBytes), statusText(title, statusMsgMaxBytes)
	case st.Busy:
		r.State = programstatus.Working
	case end != nil && end.Error != "":
		r.State, r.Msg = programstatus.Error, statusText(end.Error, statusMsgMaxBytes)
	case end != nil:
		r.State, r.Msg = programstatus.Done, "Turn completed."
		if msg := stopWarning(end.StopReason); msg != "" {
			r.Msg = msg
		}
	default:
		r.State = programstatus.Done
	}
	return r
}

// statusText is s on one line, cut to at most max bytes.
func statusText(s string, max int) string {
	s = strings.Join(strings.FieldsFunc(strings.ToValidUTF8(s, "�"), func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " ")
	if len(s) <= max {
		return s
	}
	const ellipsis = "…"
	cut := max - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}
