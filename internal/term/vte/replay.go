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
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/term/vte/vteparser"
)

// Replay feeds raw ANSI bytes through a headless VTE sized at width x height
// and returns a snapshot of the resulting primary buffer plus the final
// cursor position in scroll-relative coordinates (matching the row indices
// of the returned [][]term.Cell).
//
// The returned buffer is independent from the internal VTE state: its cells
// are cloned, so callers may retain it without holding any locks.
//
// Replay is intended for offline, deterministic analysis of recorded ANSI
// output (for example, terminal screen captures used by vteprobe tests).
// It does not start a pty, does not read from a master, and does not touch
// the clipboard or any tab manager.
func Replay(width, height int, data []byte) (*cell.Buffer, term.Coordinates, error) {
	if width <= 0 || height <= 0 {
		return nil, term.Coordinates{}, fmt.Errorf(
			"vte.Replay: width and height must be > 0 (got %dx%d)",
			width, height)
	}
	uri, err := workspaceapi.ParseURI("memory:///replay")
	if err != nil {
		return nil, term.Coordinates{}, fmt.Errorf("vte.Replay: parse uri: %w", err)
	}

	pty := workspaceapi.Pty{Master: replayFile{}, Slave: replayFile{}}
	ph := newParserHandler(
		new(sync.Mutex), pty, replayTabManager{}, clipboard.NewInMemory(),
		func() {}, uri, term.Attributes{},
		false, // useTitleAsTabname=false: SetTitle becomes a no-op
		0,     // maxScrollLength: caller controls history via screen size
		0,     // minWidth: width is the authoritative dimension
	)
	// Match the convention used by parser_handler tests so empty cells
	// render as visible spaces rather than the implementation-detail
	// zero rune. Callers (e.g. vteprobe) treat the cell grid as text.
	ph.sync.primBuf.SetDefaultChar(' ')
	ph.sync.altBuf.SetDefaultChar(' ')
	ph.Resize(width, height)

	parser := vteparser.NewParser(ph, new(vteparser.StdTimeout))
	for _, b := range data {
		parser.Advance(b)
	}

	cells := term.CloneCells(ph.sync.primBuf.Cells.RawCells())
	cursor := ph.sync.primBuf.CursorAtScroll()
	return cell.CellsToBuffer(cells), cursor, nil
}

// replayTabManager satisfies browser.TabManager with no side effects.
// It is only used during Replay, which keeps the parser handler in focus
// so the tab-name path is never reached.
type replayTabManager struct{}

func (replayTabManager) Tab(
	workspaceapi.URI, rune, string, browserapi.Handler,
) (browserapi.Handler, error) {
	return nil, nil
}

func (replayTabManager) SetTabName(
	workspaceapi.URI, string, term.Attributes,
) error {
	return nil
}

func (replayTabManager) OnTabExit(workspaceapi.URI) bool {
	return false
}

// replayFile satisfies workspaceapi.File for the replay pty.
// Writes are dropped (the parser only writes responses to queries we do not
// care about during replay); reads return EOF.
type replayFile struct{}

func (replayFile) Name() string                      { return "" }
func (replayFile) Stat() (os.FileInfo, error)        { return nil, io.EOF }
func (replayFile) Sync() error                       { return nil }
func (replayFile) Truncate(int64) error              { return nil }
func (replayFile) Fd() uintptr                       { return 0 }
func (replayFile) Seek(int64, int) (int64, error)    { return 0, nil }
func (replayFile) Read([]byte) (int, error)          { return 0, io.EOF }
func (replayFile) ReadAt([]byte, int64) (int, error) { return 0, io.EOF }
func (replayFile) Write(p []byte) (int, error)       { return len(p), nil }
func (replayFile) Close() error                      { return nil }
