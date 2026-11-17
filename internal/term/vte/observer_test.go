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
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/vte/vteparser"
	"unstable.build/rune/internal/workspace/workspacetest"
)

func newObserverParserHandler(t *testing.T, observer bool) (*parserHandler, *workspacetest.File) {
	t.Helper()
	uri, err := workspaceapi.ParseURI("memory:///observer")
	require.NoError(t, err)
	pty := &workspacetest.File{}
	tm := &mockTabManager{}
	cellPixelSize := func() (int, int) { return 10, 20 }
	ph := newParserHandler(
		new(sync.Mutex), pty,
		testParserHost{access: ClipboardAccess{Write: true, Read: true}}, tm,
		clipboard.NewInMemory(), tm.bell, uri, term.Attributes{},
		false, 1000, 0, cellPixelSize, nil, "")
	ph.observer = observer
	ph.Resize(20, 5)
	return ph, pty
}

// TestObserverSuppressesQueryReplies pins the single-responder rule: a
// replica watching the same byte stream must never answer a query, or
// the program would read one answer per attached client.
func TestObserverSuppressesQueryReplies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		desc  string
		query func(*parserHandler)
		reply string
	}{
		{
			desc:  "primary device attributes",
			query: func(p *parserHandler) { p.IdentifyTerminal(false) },
			reply: "\x1b[?6c",
		},
		{
			desc:  "secondary device attributes",
			query: func(p *parserHandler) { p.IdentifyTerminal(true) },
			reply: fmt.Sprintf("\x1b[>0;%d;1c", pkgVersion),
		},
		{
			desc:  "device status",
			query: func(p *parserHandler) { p.DeviceStatus(5) },
			reply: "\x1b[0n",
		},
		{
			desc:  "cursor position report",
			query: func(p *parserHandler) { p.DeviceStatus(6) },
			reply: "\x1b[1;1R",
		},
		{
			desc:  "text area size in chars",
			query: func(p *parserHandler) { p.TextAreaSizeChars() },
			reply: "\x1b[8;5;20t",
		},
		{
			desc: "private mode report",
			query: func(p *parserHandler) {
				p.ReportPrivateMode(vteparser.PrivateModeBracketedPaste)
			},
			reply: "\x1b[?2004;2$y",
		},
		{
			desc: "clipboard load",
			query: func(p *parserHandler) {
				p.ClipboardStore(int('c'), []byte("aGk="))
				p.ClipboardLoad(int('c'), "\x07")
			},
			reply: "\x1b]52;c;aGk=\x07",
		},
		{
			desc:  "kitty keyboard mode report",
			query: func(p *parserHandler) { p.ReportKeyboardMode() },
			reply: "\x1b[?0u",
		},
		{
			desc:  "modifyOtherKeys report",
			query: func(p *parserHandler) { p.ReportModifyOtherKeys() },
			reply: "\x1b[>4;0m",
		},
		{
			desc:  "text area size in pixels",
			query: func(p *parserHandler) { p.TextAreaSizePixels() },
			reply: "\x1b[4;100;200t",
		},
		{
			desc:  "cell size in pixels",
			query: func(p *parserHandler) { p.CellSizePixels() },
			reply: "\x1b[6;20;10t",
		},
		{
			desc: "graphics query",
			query: func(p *parserHandler) {
				parser := vteparser.NewParser(p)
				parser.AdvanceBytes([]byte("\x1b_Ga=q,i=31,s=1,v=1,f=24;AAAA\x1b\\"))
			},
			reply: "\x1b_Gi=31;OK\x1b\\",
		},
		{
			desc:  "termcap query",
			query: func(p *parserHandler) { p.RequestTermcap([]string{"Ms"}) },
			reply: fmt.Sprintf("\x1bP1+r%x=%x\x1b\\", "Ms", termcapMs),
		},
		{
			desc:  "program status query",
			query: func(p *parserHandler) { p.ProgramStatusQuery("\x07") },
			reply: "\x1b]7501;?\x07",
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()

			canonical, canonicalPty := newObserverParserHandler(t, false)
			tc.query(canonical)
			require.Len(t, canonicalPty.Writes, 1)
			assert.Equal(t, tc.reply, string(canonicalPty.Writes[0]))

			observer, observerPty := newObserverParserHandler(t, true)
			tc.query(observer)
			assert.Empty(t, observerPty.Writes)
		})
	}
}

// TestObserverSurvivesReset pins that a full terminal reset does not
// turn a replica back into a responder.
func TestObserverSurvivesReset(t *testing.T) {
	t.Parallel()

	ph, pty := newObserverParserHandler(t, true)
	ph.ResetState()
	ph.IdentifyTerminal(false)
	assert.Empty(t, pty.Writes)
}

// TestObserverMarkExitedNotifiesTabManager pins that a replica drops
// its tab when the stream reports the process exit. A replica starts
// no process, so the pty run loop's own exit report never fires for
// it, and an event publish would only reach the focused handler. The
// status the stream reported is final by then.
func TestObserverMarkExitedNotifiesTabManager(t *testing.T) {
	t.Parallel()

	for _, exitErr := range []error{nil, errors.New("exit status 3")} {
		uri, err := workspaceapi.ParseURI("memory:///observer")
		require.NoError(t, err)
		tm := &tabExiterRecorder{}
		cfg := DefaultConfig()
		cfg.Observer = true
		cfg.URI = uri
		h, pty, events := newPipeHandler(t, tm, cfg)

		// A replica is told about the exit from inside its read loop,
		// so let the loop consume something first.
		pty.feed(t, "x")
		select {
		case <-events:
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for the replica to consume input")
		}

		h.MarkExited(exitErr)

		assert.True(t, h.Component().IsComplete())
		assert.Equal(t, exitErr, h.ExitErr())
		assert.Equal(t, []workspaceapi.URI{uri}, tm.ExitedURIs())
		select {
		case <-h.Component().Exited():
		default:
			t.Errorf("the status of %v was reported but is not final", exitErr)
		}
	}
}

// A replica attached to a terminal whose size its window already has is
// never resized, so the snapshot is all that sizes its modal view.
func TestObserverApplySnapshotSizesModalView(t *testing.T) {
	t.Parallel()

	origin := newFeedComponent(t, 20, 4)
	origin.feed(t, "$ echo hello\r\nhello\r\n$ ")

	cfg := DefaultConfig()
	cfg.Modal = true
	cfg.Observer = true
	cfg.WidthHint, cfg.HeightHint = 20, 4
	replica, _, _ := newPipeHandler(t, nopTabManager{}, cfg)
	require.NoError(t, replica.ApplySnapshot(snapshotOf(t, origin)))

	replica.enterViMode(replica.Component().CursorAtScroll())
	w := term.NewStringWriter(20, 4)
	replica.Draw(w)
	require.NoError(t, w.Flush())
	assert.Contains(t, w.String(), "hello")
	pos, _, show := replica.Cursor()
	assert.True(t, show)
	assert.Equal(t, 2, pos.Y, "the vi cursor starts on the prompt")
}

func TestObserverApplySnapshotIgnoresInvalidSize(t *testing.T) {
	t.Parallel()

	origin := newFeedComponent(t, 20, 4)
	origin.feed(t, "hello")

	for _, tc := range []struct {
		desc          string
		width, height int
	}{
		{desc: "negative width", width: -1, height: 4},
		{desc: "negative height", width: 20, height: -1},
		{desc: "unsized", width: 0, height: 0},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			cfg := DefaultConfig()
			cfg.Modal = true
			cfg.Observer = true
			replica, _, _ := newPipeHandler(t, nopTabManager{}, cfg)
			replica.Resize(30, 6)
			snap := snapshotOf(t, origin)
			snap.Terminal.Width, snap.Terminal.Height = tc.width, tc.height

			require.NoError(t, replica.ApplySnapshot(snap))
			assert.Equal(t, 30, replica.vi.width, "the modal view keeps its size")
			assert.Equal(t, 6, replica.vi.height, "the modal view keeps its size")
		})
	}
}
