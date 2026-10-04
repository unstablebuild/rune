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
	"bytes"
	"encoding/base64"
	"errors"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/vte/vteparser"
	"unstable.build/rune/internal/workspace/workspacetest"
)

func TestClipboardOSC52(t *testing.T) {
	t.Parallel()
	const (
		sel = selectionRegisterID
		bel = "\x07"
		st  = "\x1b\\"
	)
	clip := clipboard.DefaultRegisterID
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	errUnavailable := errors.New("clipboard unavailable")
	cases := []struct {
		name      string
		registers map[string]string // held before the output
		err       error             // returned by every clipboard operation
		output    string
		// want holds the registers after the output; nil means the output
		// must leave them unchanged.
		want  map[string]string
		reply string
	}{
		// store
		{
			name:   "store c",
			output: "\x1b]52;c;" + b64("hello") + bel,
			want:   map[string]string{clip: "hello"},
		},
		{
			name:   "store p",
			output: "\x1b]52;p;" + b64("hello") + bel,
			want:   map[string]string{sel: "hello"},
		},
		{
			name:   "store s",
			output: "\x1b]52;s;" + b64("hello") + bel,
			want:   map[string]string{sel: "hello"},
		},
		{
			name:   "store without a register selects c",
			output: "\x1b]52;;" + b64("hello") + bel,
			want:   map[string]string{clip: "hello"},
		},
		{
			name:   "store terminated by ST",
			output: "\x1b]52;c;" + b64("hello") + st,
			want:   map[string]string{clip: "hello"},
		},
		{
			name:   "store multiline text",
			output: "\x1b]52;c;" + b64("one\ntwo\n") + bel,
			want:   map[string]string{clip: "one\ntwo\n"},
		},
		{
			name:   "store unicode text",
			output: "\x1b]52;c;" + b64("héllo 🚀") + bel,
			want:   map[string]string{clip: "héllo 🚀"},
		},
		{
			name:      "store replaces the register",
			registers: map[string]string{clip: "old"},
			output:    "\x1b]52;c;" + b64("hello") + bel,
			want:      map[string]string{clip: "hello"},
		},
		{
			name:      "store c keeps the primary selection",
			registers: map[string]string{sel: "kept"},
			output:    "\x1b]52;c;" + b64("hello") + bel,
			want:      map[string]string{clip: "hello", sel: "kept"},
		},
		{
			name:      "store p keeps the clipboard",
			registers: map[string]string{clip: "kept"},
			output:    "\x1b]52;p;" + b64("hello") + bel,
			want:      map[string]string{clip: "kept", sel: "hello"},
		},
		{
			name:      "store empty data clears the register",
			registers: map[string]string{clip: "old"},
			output:    "\x1b]52;c;" + bel,
			want:      map[string]string{clip: ""},
		},
		{
			name:      "store invalid base64 is dropped",
			registers: map[string]string{clip: "old"},
			output:    "\x1b]52;c;not base64!" + bel,
		},
		{
			name:   "store unknown register is dropped",
			output: "\x1b]52;q;" + b64("hello") + bel,
		},
		{
			name:   "store cut buffer is dropped",
			output: "\x1b]52;0;" + b64("hello") + bel,
		},
		{
			name:      "store without data is dropped",
			registers: map[string]string{clip: "old"},
			output:    "\x1b]52;c" + bel,
		},
		{
			name:      "clipboard store error doesn't freeze the terminal",
			registers: map[string]string{clip: "old"},
			err:       errUnavailable,
			output:    "\x1b]52;c;" + b64("hello") + bel,
		},

		// load
		{
			name:      "load c",
			registers: map[string]string{clip: "hello"},
			output:    "\x1b]52;c;?" + bel,
			reply:     "\x1b]52;c;" + b64("hello") + bel,
		},
		{
			name:      "load p",
			registers: map[string]string{sel: "hello"},
			output:    "\x1b]52;p;?" + bel,
			reply:     "\x1b]52;p;" + b64("hello") + bel,
		},
		{
			name:      "load s",
			registers: map[string]string{sel: "hello"},
			output:    "\x1b]52;s;?" + bel,
			reply:     "\x1b]52;s;" + b64("hello") + bel,
		},
		{
			name:      "load without a register selects c",
			registers: map[string]string{clip: "hello"},
			output:    "\x1b]52;;?" + bel,
			reply:     "\x1b]52;c;" + b64("hello") + bel,
		},
		{
			name:      "load replies with the ST terminator",
			registers: map[string]string{clip: "hello"},
			output:    "\x1b]52;c;?" + st,
			reply:     "\x1b]52;c;" + b64("hello") + st,
		},
		{
			name:      "load c ignores the primary selection",
			registers: map[string]string{sel: "hello"},
			output:    "\x1b]52;c;?" + bel,
			reply:     "\x1b]52;c;" + bel,
		},
		{
			name:      "load p ignores the clipboard",
			registers: map[string]string{clip: "hello"},
			output:    "\x1b]52;p;?" + bel,
			reply:     "\x1b]52;p;" + bel,
		},
		{
			name:   "load empty register",
			output: "\x1b]52;c;?" + bel,
			reply:  "\x1b]52;c;" + bel,
		},
		{
			name:      "load multiline text",
			registers: map[string]string{clip: "one\ntwo\n"},
			output:    "\x1b]52;c;?" + bel,
			reply:     "\x1b]52;c;" + b64("one\ntwo\n") + bel,
		},
		{
			name:      "load unicode text",
			registers: map[string]string{clip: "héllo 🚀"},
			output:    "\x1b]52;c;?" + bel,
			reply:     "\x1b]52;c;" + b64("héllo 🚀") + bel,
		},
		{
			name:      "load unknown register is dropped",
			registers: map[string]string{clip: "hello"},
			output:    "\x1b]52;q;?" + bel,
		},
		{
			name:      "clipboard load error doesn't freeze the terminal",
			registers: map[string]string{clip: "hello"},
			err:       errUnavailable,
			output:    "\x1b]52;c;?" + bel,
		},

		// round trips
		{
			name:   "store c then load c",
			output: "\x1b]52;c;" + b64("hello") + bel + "\x1b]52;c;?" + bel,
			want:   map[string]string{clip: "hello"},
			reply:  "\x1b]52;c;" + b64("hello") + bel,
		},
		{
			name:   "store p then load s",
			output: "\x1b]52;p;" + b64("hello") + bel + "\x1b]52;s;?" + bel,
			want:   map[string]string{sel: "hello"},
			reply:  "\x1b]52;s;" + b64("hello") + bel,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			uri, err := workspaceapi.ParseURI("memory:///osc52")
			require.NoError(t, err)
			registers := map[string]string{}
			maps.Copy(registers, tc.registers)
			want := tc.want
			if want == nil {
				want = maps.Clone(registers)
			}
			pty := &workspacetest.File{}
			lock := new(lockCounter)
			ph := newParserHandler(lock, workspaceapi.Pty{Master: pty, Slave: pty},
				&mockTabManager{}, &registerClipboard{registers: registers, err: tc.err},
				func() {}, uri, term.Attributes{}, false, 100, 0, nil, nil, "")

			vteparser.NewParser(ph, new(vteparser.StdTimeout)).AdvanceBytes([]byte(tc.output))

			assert.Equal(t, want, registers)
			assert.Equal(t, tc.reply, string(bytes.Join(pty.Writes, nil)))
			assert.Zero(t, lock.held, "terminal lock left held")
			assert.Zero(t, lock.stray, "terminal lock released without being held")
		})
	}
}

// registerClipboard keeps each register apart, as the clipboard Rune
// configures does, so that a test tells which one a request reached.
type registerClipboard struct {
	registers map[string]string
	err       error
}

func (c *registerClipboard) Copy(registerID string, data clipboard.Data) error {
	if c.err != nil {
		return c.err
	}
	c.registers[registerID] = data.Text
	return nil
}

func (c *registerClipboard) Paste(registerID string) (clipboard.Data, error) {
	if c.err != nil {
		return clipboard.Data{}, c.err
	}
	return clipboard.Data{Text: c.registers[registerID]}, nil
}

// lockCounter stands in for the terminal lock so that releasing it
// while nobody holds it fails the test: on a sync.Mutex that is a fatal
// error, which would take down the test binary instead.
type lockCounter struct {
	held, stray int
}

func (l *lockCounter) Lock() { l.held++ }

func (l *lockCounter) Unlock() {
	if l.held == 0 {
		l.stray++
		return
	}
	l.held--
}
