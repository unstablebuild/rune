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

package vte

import (
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/term/programstatus"
)

type nopTabManager struct {
}

func (nopTabManager) Tab(
	uri workspaceapi.URI, icon rune, name string, h browserapi.Handler,
) (browserapi.Handler, error) {
	panic("not implemented")
}

func (nopTabManager) SetTabName(workspaceapi.URI, string, term.Attributes) error {
	return nil
}

func (nopTabManager) OnTabExit(workspaceapi.URI) bool {
	return false
}

func (nopTabManager) SetTabStatus(workspaceapi.URI, programstatus.Report) error { return nil }
func (nopTabManager) EndTabStatus(workspaceapi.URI) error                       { return nil }

type nopNotifications struct {
}

func (nopNotifications) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return "", nil
}

func (nopNotifications) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return "", nil
}

func (n nopNotifications) UpdateNotificationProgress(
	id, message string, progress, total int64,
) error {
	return nil
}

func TestLastPromptLine(t *testing.T) {
	t.Parallel()
	suite := []struct {
		description string
		content     string
		width       int
		// cursorY=0 (zero value) defaults to -1 (legacy
		// bottom-up search). Set setCursorY explicitly to
		// anchor the prompt block at a specific row.
		cursorY        int
		setCursorY     bool
		considerSpaces bool
		expectedStart  term.Coordinates
		expectedEnd    term.Coordinates
	}{
		{
			description:   "empty just returns zero",
			content:       "",
			width:         10,
			expectedStart: term.Coordinates{},
			expectedEnd:   term.Coordinates{},
		},
		{
			description:   "exactly one line shell",
			content:       "$",
			width:         10,
			expectedStart: term.Coordinates{},
			expectedEnd:   term.Coordinates{X: 1},
		},
		{
			description: "one line shell with history",
			content: `
blablab  
$ echo`,
			width:         10,
			expectedStart: term.Coordinates{Y: 2},
			expectedEnd:   term.Coordinates{Y: 2, X: 6},
		},
		{
			description: "empty last line should return everything since last shell",
			content: `
blablabla
$ echo    
       `,
			width:         10,
			expectedStart: term.Coordinates{Y: 2},
			expectedEnd:   term.Coordinates{Y: 3, X: 7},
		},
		{
			description: "multiline simple",
			content: `
blablabla
$ echo bla
aaaaaaaaaa`,
			width:         10,
			expectedStart: term.Coordinates{Y: 2},
			expectedEnd:   term.Coordinates{Y: 3, X: 10},
		},
		{
			description: "multiline with space (previous less than width)",
			content: `
blablabla
$ echo
$ aaaaaaa `,
			width:         10,
			expectedStart: term.Coordinates{Y: 3},
			expectedEnd:   term.Coordinates{Y: 3, X: 10},
		},
		{
			description: "multiline with space (previous filled with blank chars)",
			content: "\n" +
				"blablabla\n" +
				"$ echo\x00\x00\x00\x00\n" +
				"$ aaaaaaa \n",
			width:         10,
			expectedStart: term.Coordinates{Y: 3},
			expectedEnd:   term.Coordinates{Y: 3, X: 10},
		},
		{
			description: "AMBIGUOUS multiline",
			content: `
blablabla
$ echo bla
$ aaaaaaaa`,
			width:         10,
			expectedStart: term.Coordinates{Y: 2},
			expectedEnd:   term.Coordinates{Y: 3, X: 10},
		},
		{
			description: "single line with blank lines under",
			content: "blablabla \n" +
				"$ echo\n" +
				"$ aaaaaaa\x00\x00\n" +
				"\x00\x00\x00",
			width:         10,
			expectedStart: term.Coordinates{Y: 2},
			expectedEnd:   term.Coordinates{Y: 2, X: 9},
		},
		{
			description: "multiline with blank lines under",
			content: "blablabla\n" +
				"$ echo aaa\n" +
				"aaaaaaaaa\x00\x00\n" +
				"\x00\x00\x00",
			width:         10,
			expectedStart: term.Coordinates{Y: 1},
			expectedEnd:   term.Coordinates{Y: 2, X: 9},
		},
		{
			// RUNE-193: zsh menu-select tab completion below the
			// prompt leaves an orphan `$ ` row below the real
			// prompt row after <esc>. Without a prompt anchor,
			// lastPromptLine picked the orphan as the prompt and
			// excluded the real prompt row — vi edits on the real
			// prompt row would then be rejected with a bell.
			// Anchoring at the PTY cursor row (the shell prompt
			// start) makes lastPromptLine return the real prompt
			// row instead.
			description: "RUNE-193 orphan prompt row below the real prompt",
			// row 0: `$ ls` + 16 NULs (real prompt, cursor here)
			// row 1: `$ ` + 18 NULs (orphan from menu cleanup)
			content: "$ ls" + strings.Repeat("\x00", 16) + "\n" +
				"$ " + strings.Repeat("\x00", 18),
			width:         20,
			cursorY:       0,
			setCursorY:    true,
			expectedStart: term.Coordinates{Y: 0, X: 0},
			expectedEnd:   term.Coordinates{Y: 0, X: 4},
		},
		{
			description: "considering spaces, multiline simple",
			content: `
blablabla
$ echo bla
aaaaaaaaaa`,
			width:          10,
			expectedStart:  term.Coordinates{Y: 2},
			expectedEnd:    term.Coordinates{Y: 3, X: 10},
			considerSpaces: true,
		},
		{
			description: "considering spaces multiline with space (previous less than width)",
			content: `
blablabla
$ echo
$ aaaaaaa `,
			width:          10,
			expectedStart:  term.Coordinates{Y: 3},
			expectedEnd:    term.Coordinates{Y: 3, X: 9},
			considerSpaces: true,
		},
		{
			description: "considering spaces, AMBIGUOUS multiline",
			content: `
blablabla
$ echo bla
$ aaaaaaaa`,
			width:          10,
			expectedStart:  term.Coordinates{Y: 2},
			expectedEnd:    term.Coordinates{Y: 3, X: 10},
			considerSpaces: true,
		},
		{
			description: "considering spaces, single line with blank lines under",
			content: "blablabla \n" +
				"$ echo\n" +
				"$ aaaaa  \x00\x00\n" +
				"\x00\x00\x00",
			width:          10,
			expectedStart:  term.Coordinates{Y: 2},
			expectedEnd:    term.Coordinates{Y: 2, X: 7},
			considerSpaces: true,
		},
		{
			description: "considering spaces, multiline with blank lines under",
			content: "blablabla\n" +
				"$ echo aaa\n" +
				"aaaaaaa  \x00\x00\n" +
				"\x00\x00\x00",
			width:          10,
			expectedStart:  term.Coordinates{Y: 1},
			expectedEnd:    term.Coordinates{Y: 2, X: 7},
			considerSpaces: true,
		},
	}

	for _, test := range suite {
		t.Run(test.description, func(t *testing.T) {
			t.Parallel()
			buf := cell.NewBuffer()
			// do not use ReadFrom or InsertString as null characters
			// will be elided.
			var next term.Coordinates
			for _, ch := range test.content {
				next = buf.Insert(next, ch)
			}

			// sut
			cursorY := -1
			if test.setCursorY {
				cursorY = test.cursorY
			}
			actualStart, actualEnd := lastPromptLine(buf, test.width, cursorY, test.considerSpaces)
			assert.Equal(t, test.expectedStart, actualStart, "start")
			assert.Equal(t, test.expectedEnd, actualEnd, "end")
		})
	}
}

type tabExiterRecorder struct {
	nopTabManager
	mu     sync.Mutex
	exited []workspaceapi.URI
}

func (r *tabExiterRecorder) OnTabExit(uri workspaceapi.URI) bool {
	r.mu.Lock()
	r.exited = append(r.exited, uri)
	r.mu.Unlock()
	return true
}

func (r *tabExiterRecorder) SetTabStatus(workspaceapi.URI, programstatus.Report) error { return nil }
func (r *tabExiterRecorder) EndTabStatus(workspaceapi.URI) error                       { return nil }

func (r *tabExiterRecorder) ExitedURIs() []workspaceapi.URI {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]workspaceapi.URI(nil), r.exited...)
}
