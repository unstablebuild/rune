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
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/searchbox"
)

// DefaultConfig returns a sane default Config.
func DefaultConfig() Config {
	return Config{
		Clipboard:                clipboard.NewInMemory(),
		ClipboardRegister:        clipboard.DefaultRegisterID,
		ScheduleNextTick:         func(cb func()) bool { cb(); return true },
		RingBell:                 func() {},
		SelectionAttributes:      term.Attributes{Attrs: term.AttrReverse},
		NeedsAttentionAttributes: term.Attributes{Attrs: term.AttrBlink},
		DynamicTabName:           false,
		MaxLines:                 10_000,
		MinWidth:                 0,
	}
}

// Config configures Handler.
type Config struct {
	// CommandAndArgs is the program to run. Otherwise whatever is set
	// on the $SHELL environment variable is used.
	CommandAndArgs    []string
	Clipboard         clipboard.Register
	ClipboardRegister string

	// ScheduleNextTick schedules an arbitrary function to be run
	// in the next event-loop tick.
	ScheduleNextTick func(func()) bool

	// Use the VTE's title as the tab name.
	DynamicTabName bool

	// RingBell writes to the raw pty directly, bypassing the event loop.
	// This should only be used when called from the an event loop goroutine.
	RingBell func()
	Watcher  workspaceapi.ProcessWatcher

	Attributes               term.Attributes
	SelectionAttributes      term.Attributes
	NeedsAttentionAttributes term.Attributes
	MaxLines                 int
	// Bell overrides the default bell trigger. This is useful for non-standard
	// shells like the fish shell, which don't trigger the bell with the standard
	// escape sequence.
	Bell []byte

	// MinWidth helps optimize growing and shrinking rows upon resize.
	MinWidth int

	// Modal enables entering modal mode via Esc key.
	// Changing mode to 'INSERT' mode switches back to
	// the shell being in control of the input.
	Modal bool

	// WidthHint and HeightHint hint allows emulator.Handler to better configure the
	// initial buffer size.
	WidthHint  int
	HeightHint int

	Debug bool

	// CommandExpander, if non-nil, is invoked in a background
	// goroutine after the pty is created but before the foreign
	// command is started. ExpandCommand receives the joined
	// command line (CommandAndArgs joined with " ") and returns
	// the resolved line to actually execute. The pty exists while
	// the expander runs, so the host UI is interactive (resize,
	// draw) but no foreign process is attached yet.
	//
	// Returning an error aborts the spawn; the error is written to
	// the pty slave so it surfaces in the floating window like any
	// other failure, and the Watcher fires with that error.
	CommandExpander CommandExpander

	// SpawnTimeout bounds the synchronous spawn RPCs issued during
	// initialization (Terminal.NewPty and Executor.StartCommand).
	// On a remote workspace whose transport has stalled, those RPCs
	// can otherwise block forever and wedge whoever is constructing
	// the vte (e.g. the host event loop). Zero disables the bound.
	// It does not limit the lifetime of the spawned command.
	SpawnTimeout time.Duration

	// Search enables searching the scrollback of the primary buffer.
	// A nil Search.Editor disables the feature.
	Search SearchConfig

	// CellPixelSize reports the size of a character cell in pixels.
	// It enables the kitty graphics protocol, which needs it to size
	// placements and answer pixel-size queries; leave it nil on a
	// cells-only display so clients probing for graphics support get
	// no answer and fall back.
	CellPixelSize func() (width, height int)

	// FileSystem reads the files a graphics t=f, t=t or t=s
	// transmission names. It must be the filesystem the terminal's
	// command runs on, so a remote workspace reads them there. A nil
	// FileSystem refuses those mediums.
	FileSystem schemeapi.FileSystem

	// TempDir is the temporary directory of the machine FileSystem
	// serves, when known. A t=t graphics transmission is deleted after
	// it is read only from there, /tmp or /dev/shm.
	TempDir string
}

// SearchConfig configures the terminal's scrollback search. The box is
// drawn as an overlay on the terminal itself, so no window manager is
// involved.
type SearchConfig struct {
	searchbox.Config

	MatchAttr        term.Attributes
	CurrentMatchAttr term.Attributes
}

// CommandExpander resolves a vte command line before the foreign
// command is started. Implementations may run arbitrary I/O (e.g.
// resolve $(...) via the workspace executor); they are invoked off
// the event-loop goroutine so they may block.
type CommandExpander interface {
	ExpandCommand(ctx context.Context, line string) (string, error)
}

func (c Config) scheduleBell() {
	c.ScheduleNextTick(c.RingBell)
}
