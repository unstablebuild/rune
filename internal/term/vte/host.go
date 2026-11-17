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
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// ID identifies a terminal among those its Host holds. It is
// meaningless to any other Host, and to the same one once it closes.
type ID string

// NewID returns a fresh identity for a terminal.
func NewID() ID {
	var b [16]byte
	// rand.Read never fails; see crypto/rand.Read documentation.
	_, _ = rand.Read(b[:])
	return ID(hex.EncodeToString(b[:]))
}

// ErrNotFound is returned by a Host asked for a terminal it does not
// have.
var ErrNotFound = errors.New("vte: terminal not found")

// Liveness tells whether a terminal is running something for the user,
// which closing its last view must not kill.
type Liveness int

const (
	// LivenessUnknown is a terminal that may be running something: its
	// Host cannot tell.
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
	// Liveness is what the Host last found the terminal to run while it
	// was detached, as its last [Change] told: LivenessUnknown while it
	// is attached, and until the Host checked. It is how what was told
	// while nobody listened is caught up with.
	Liveness Liveness
	// Complete reports that the emulator stopped reading its pty.
	Complete  bool
	CreatedAt time.Time
}

// CreateOptions describes a terminal to create.
type CreateOptions struct {
	// CommandAndArgs is the program to run. Empty runs the workspace's
	// default shell.
	CommandAndArgs []string
	// Width and Height are the initial pty size. Zero keeps the Host's
	// default.
	Width  int
	Height int
}

// Change is what a Host tells of one of its terminals when what
// [Host.ListVTEs] reports of it changes, or when it finds out what the
// terminal runs.
type Change struct {
	ID    ID
	URI   workspaceapi.URI
	Title string
	// Attachments is how many views are attached. A terminal with none
	// is detached.
	Attachments int
	// Liveness is what the terminal runs, when the Host found out while
	// it was detached: LivenessUnknown when it did not ask.
	Liveness Liveness
	// Complete reports that the emulator stopped reading its pty.
	Complete bool
	// Deleted reports that the terminal is gone. It is the last change
	// told of it.
	Deleted bool
}

// Host holds the terminals of a workspace: their emulators, ptys,
// processes, grids and scrollback, in this process or on the machine
// that serves the workspace. Callers hold views, never a terminal
// itself, so a terminal outlives the view that opened it.
//
// Views must be used on the host event loop. The other methods may be
// called from any goroutine.
type Host interface {
	// CreateVTE creates a terminal and returns the caller's attachment
	// to it. It blocks while the terminal starts, for no longer than
	// ctx's deadline when it has one.
	CreateVTE(context.Context, CreateOptions) (View, error)
	// AttachVTE returns a new attachment to the terminal id, or
	// [ErrNotFound] when there is none with that id.
	AttachVTE(context.Context, ID) (View, error)
	// PreviewVTE returns a view of the terminal id that leaves it
	// detached, or [ErrNotFound] when there is none with that id.
	PreviewVTE(context.Context, ID) (View, error)
	// ListVTEs reports every terminal, attached or not, in the order
	// they were created. It does no I/O, so the event loop may call it.
	ListVTEs(context.Context) ([]Info, error)
	// VTELiveness tells whether the terminal id runs something for the
	// user, or [ErrNotFound] when there is none with that id. It may
	// ask the host of the terminal's pty, so the event loop must not
	// call it.
	VTELiveness(context.Context, ID) (Liveness, error)
	// DeleteVTE terminates the terminal id: its views, pty and process.
	// It returns [ErrNotFound] when there is none with that id.
	DeleteVTE(context.Context, ID) error
	// Watch calls fn, until ctx ends, with every change to a terminal's
	// attachments, whether it completed or was deleted, with its
	// creation, and with what it runs when the Host finds out while it
	// is detached. fn may be called from any goroutine, so it must
	// return promptly and must not call back into the Host.
	Watch(ctx context.Context, fn func(Change))
	// Close detaches every view and ends the terminals that live in
	// this process; those another machine holds keep running there.
	// Later calls fail.
	io.Closer
}

// View is one view's attachment to a terminal.
//
// A View is the attachment, not the terminal: Close detaches this view
// and leaves the emulator, its pty and its process running for its Host
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
