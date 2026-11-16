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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// ID identifies a terminal among those its owner holds. It is
// meaningless to any other owner, and to the same one once it closes.
type ID string

// NewID returns a fresh identity for a terminal.
func NewID() ID {
	var b [16]byte
	// rand.Read never fails; see crypto/rand.Read documentation.
	_, _ = rand.Read(b[:])
	return ID(hex.EncodeToString(b[:]))
}

// ErrNotFound is returned by an owner asked for a terminal it does not
// have.
var ErrNotFound = errors.New("vte: terminal not found")

// Liveness tells whether a terminal is running something for the user,
// which closing its last view must not kill.
type Liveness int

const (
	// LivenessUnknown is a terminal that may be running something: its
	// owner cannot tell.
	LivenessUnknown Liveness = iota
	// LivenessIdle is a terminal waiting for input at its shell's
	// prompt, or whose process exited.
	LivenessIdle
	// LivenessRunning is a terminal running a program in the
	// foreground of its shell.
	LivenessRunning
)

func (l Liveness) String() string {
	switch l {
	case LivenessIdle:
		return "idle"
	case LivenessRunning:
		return "running"
	}
	return "unknown"
}

// Info describes a terminal independently of its views.
type Info struct {
	ID ID
	// URI is the terminal's own URI, which it reports its tab and
	// status calls under.
	URI   workspaceapi.URI
	Title string
	// CommandAndArgs is the program the terminal was created with,
	// empty when it runs the workspace's default shell.
	CommandAndArgs []string
	// Width and Height are the size of the terminal's pty.
	Width  int
	Height int
	// Attachments is the number of views attached to the terminal. A
	// terminal with none is detached.
	Attachments int
	// Complete reports that the emulator stopped reading its pty.
	Complete  bool
	CreatedAt time.Time
}

// CreateOptions describes a terminal to create.
type CreateOptions struct {
	// CommandAndArgs is the program to run. Empty runs the workspace's
	// default shell.
	CommandAndArgs []string
	// Width and Height are the initial pty size. Zero keeps the owner's
	// default.
	Width  int
	Height int
}

// View is one view's attachment to a terminal.
//
// A View is the attachment, not the terminal: Close detaches this view
// and leaves the emulator, its pty and its process running for its owner
// to terminate. Every attachment to the same terminal shows the same
// grid.
type View interface {
	browserapi.Handler
	component.Scrollable

	// ID identifies the terminal this value is attached to.
	ID() ID
	OnFocusChange(bool)
	SetDefaultAttributes(attr term.Attributes)
	Snapshot() (Snapshot, error)
	RestoreFromSnapshot(Snapshot) error
	IsComplete() bool
	URI() workspaceapi.URI
	Title() string
	UsedAlternateBuffer() bool
	ClearPrimaryBuffer() bool
	// ExitErr returns the error the process exited with: nil while it
	// runs, when it exited cleanly, or before it was spawned.
	ExitErr() error
	// CursorAtScroll returns the cursor's position in the rows
	// Snapshot captures, scrollback included.
	CursorAtScroll() term.Coordinates
}
