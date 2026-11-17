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
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vte/vteparser"
	"unstable.build/rune/internal/term/vte/vtesnap"
	"unstable.build/rune/internal/workspace/workspacetest"
)

// TestSnapshotReplicatesEmulatorState pins the core of the attach
// path: a replica built from a snapshot renders what the original
// renders, and keeps agreeing once both are fed the same later bytes.
func TestSnapshotReplicatesEmulatorState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		desc  string
		setup string
		after string
	}{
		{
			desc:  "plain text",
			setup: "hello\r\nworld\r\n",
			after: "more text\r\n",
		},
		{
			desc:  "styled text",
			setup: "\x1b[31;1mred bold\x1b[0m\r\nplain\r\n",
			after: "\x1b[4munderline\x1b[0m\r\n",
		},
		{
			desc:  "cursor addressing and erase",
			setup: "\x1b[2J\x1b[5;3Hplaced\x1b[1;1Htop",
			after: "\x1b[3;1Hthird",
		},
		{
			desc:  "scroll region",
			setup: "\x1b[2;5r\x1b[3;1Hinside\r\n",
			after: "scrolled\r\n",
		},
		{
			desc:  "alternate screen",
			setup: "primary\r\n\x1b[?1049hin alt screen",
			after: "\r\nmore alt",
		},
		{
			desc:  "alternate screen then back",
			setup: "primary\r\n\x1b[?1049halt\x1b[?1049l",
			after: "back on primary\r\n",
		},
		{
			desc:  "saved cursor and charset",
			setup: "\x1b[4;7H\x1b7\x1b[1;1H\x1b(0abc",
			after: "\x1b8xyz",
		},
		{
			desc:  "modes",
			setup: "\x1b[?1h\x1b[?2004h\x1b[?1000h\x1b[?7l",
			after: "text with wrap off",
		},
		{
			desc:  "tab stops",
			setup: "\x1b[3g\x1b[1;5H\x1bH\x1b[1;9H\x1bH\x1b[1;1H",
			after: "a\tb\tc",
		},
		{
			desc:  "scrollback",
			setup: strings.Repeat("line\r\n", 40),
			after: "after scrollback\r\n",
		},
		{
			desc:  "wide glyphs and combining marks",
			setup: "한국어 \u0065\u0301\u0302 ok\r\n",
			after: "日本語\r\n",
		},
		{
			desc:  "title",
			setup: "\x1b]0;my title\x07text\r\n",
			after: "more\r\n",
		},
		{
			desc:  "cursor style and visibility",
			setup: "\x1b[3 q\x1b[?25l",
			after: "hidden cursor",
		},
		{
			desc:  "underline colour",
			setup: "\x1b[4;58;2;255;0;0mred underline\r\n\x1b[58;5;3m",
			after: "palette underline\r\n",
		},
		{
			desc:  "reverse screen",
			setup: "\x1b[?5hreversed",
			after: " still reversed",
		},
		{
			desc:  "kitty keyboard stacks",
			setup: "\x1b[>1u\x1b[>5u\x1b[?1049h\x1b[>8u\x1b[?1049l",
			after: "\x1b[<u",
		},
		{
			desc:  "kitty keyboard on the alternate screen",
			setup: "\x1b[>1u\x1b[?1049h\x1b[>8u",
			after: "\x1b[?1049l",
		},
		{
			desc:  "modifyOtherKeys",
			setup: "\x1b[>4;2m",
			after: "text",
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			origin := newFeedComponent(t, 40, 12)
			origin.feed(t, tc.setup)

			replica := newFeedComponent(t, 40, 12)
			origin.replicateInto(t, replica)
			assertSameScreen(t, origin, replica)

			origin.feed(t, tc.after)
			replica.feed(t, tc.after)
			assertSameScreen(t, origin, replica)
		})
	}
}

// TestSnapshotResumesTornSequence pins that a snapshot cut in the
// middle of an escape sequence carries enough state for the replica to
// finish that sequence from the bytes that follow.
func TestSnapshotResumesTornSequence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		desc string
		head string
		tail string
	}{
		{desc: "csi", head: "\x1b[1;5", tail: "Hplaced"},
		{desc: "osc", head: "\x1b]0;my ti", tail: "tle\x07text"},
		{desc: "utf8", head: "ab\xed\x95", tail: "\x9c cd"},
		{desc: "escape only", head: "text\x1b", tail: "[2Jcleared"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			origin := newFeedComponent(t, 40, 12)
			origin.feed(t, tc.head)

			replica := newFeedComponent(t, 40, 12)
			origin.replicateInto(t, replica)

			origin.feed(t, tc.tail)
			replica.feed(t, tc.tail)
			assertSameScreen(t, origin, replica)
		})
	}
}

// TestSnapshotHistoryPrepends pins that scrollback delivered after
// READY lands above the live rows, restoring the full buffer.
func TestSnapshotHistoryPrepends(t *testing.T) {
	t.Parallel()

	origin := newFeedComponent(t, 40, 6)
	var lines strings.Builder
	for i := range 40 {
		lines.WriteRune(rune('a' + i%26))
		lines.WriteString("\r\n")
	}
	origin.feed(t, lines.String())

	blob, err := origin.comp.SnapshotThroughReady(origin.continuation())
	require.NoError(t, err)
	history := origin.comp.SnapshotHistory()
	require.NotEmpty(t, history, "40 lines on a 6 row screen must scroll")

	replica := newFeedComponent(t, 40, 6)
	decoder, err := vtesnap.NewDecoder(bytes.NewReader(blob))
	require.NoError(t, err)
	snap, err := decoder.Ready()
	require.NoError(t, err)
	require.NoError(t, replica.comp.ApplySnapshot(snap))

	// Only the live rows are present until history arrives.
	assert.Less(t, replica.rows(), origin.rows())

	for end := len(history); end > 0; end -= 8 {
		start := max(end-8, 0)
		replica.comp.PrependHistory(term.CloneCells(history[start:end]))
	}
	assert.Equal(t, origin.text(), replica.text())
}

// TestSnapshotCarriesCellPixelSize pins that a snapshot records the
// cell size its emulator places images with. Placing an image moves the
// cursor by as many cells as the image covers, so a replica has to
// interpret the stream with the same cell size or its cursor drifts.
func TestSnapshotCarriesCellPixelSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		desc          string
		cellPixelSize func() (int, int)
		width, height int
	}{
		{
			desc:          "measured",
			cellPixelSize: func() (int, int) { return 9, 18 },
			width:         9,
			height:        18,
		},
		{desc: "cells only"},
		{
			desc:          "not measured yet",
			cellPixelSize: func() (int, int) { return 0, 0 },
		},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			t.Parallel()
			c := newFeedComponentWith(t, 20, 5, func(cfg *Config) {
				cfg.CellPixelSize = tt.cellPixelSize
			})
			snap := snapshotOf(t, c)
			assert.Equal(t, tt.width, snap.Terminal.CellPixelWidth)
			assert.Equal(t, tt.height, snap.Terminal.CellPixelHeight)
		})
	}
}

// TestSnapshotRejectsInvalidContinuation pins that a snapshot is never
// emitted with replay bytes that would duplicate committed work.
func TestSnapshotRejectsInvalidContinuation(t *testing.T) {
	t.Parallel()

	c := newFeedComponent(t, 20, 5)
	_, err := c.comp.SnapshotThroughReady([]byte("\x1b[31m"))
	assert.ErrorIs(t, err, vteparser.ErrNoPendingState)
}

// TestApplySnapshotSurvivesInconsistentState pins that a snapshot whose
// fields disagree with each other cannot crash or wedge the replica
// applying it. Every field here is decoded from a socket, so a bug on
// the producing side must degrade this terminal rather than take the
// process down with it.
func TestApplySnapshotSurvivesInconsistentState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// mutate corrupts an otherwise valid snapshot.
		mutate func(*vtesnap.Snapshot)
	}{
		{
			name:   "unsized terminal",
			mutate: func(s *vtesnap.Snapshot) { s.Terminal.Width, s.Terminal.Height = 0, 0 },
		},
		{
			name: "negative dimensions",
			mutate: func(s *vtesnap.Snapshot) {
				s.Terminal.Width, s.Terminal.Height = -5, -5
			},
		},
		{
			name:   "one dimension only",
			mutate: func(s *vtesnap.Snapshot) { s.Terminal.Height = 0 },
		},
		{
			name: "cursor past the last row",
			mutate: func(s *vtesnap.Snapshot) {
				s.Primary.Cursor.Position = term.Coordinates{X: 5, Y: 9999}
			},
		},
		{
			name: "cursor past the last column",
			mutate: func(s *vtesnap.Snapshot) {
				s.Primary.Cursor.Position = term.Coordinates{X: 9999, Y: 0}
			},
		},
		{
			name: "negative cursor",
			mutate: func(s *vtesnap.Snapshot) {
				s.Primary.Cursor.Position = term.Coordinates{X: -3, Y: -3}
				s.Primary.SavedCursor.Position = term.Coordinates{X: -1, Y: -1}
			},
		},
		{
			name: "more rows than the screen has",
			mutate: func(s *vtesnap.Snapshot) {
				s.Primary.Rows = append(s.Primary.Rows, s.Primary.Rows...)
			},
		},
		{
			name:   "no rows at all",
			mutate: func(s *vtesnap.Snapshot) { s.Primary.Rows = nil },
		},
		{
			name: "rows wider than the terminal",
			mutate: func(s *vtesnap.Snapshot) {
				for i := range s.Primary.Rows {
					s.Primary.Rows[i] = append(s.Primary.Rows[i],
						make([]term.Cell, 200)...)
				}
			},
		},
		{
			name: "ragged rows",
			mutate: func(s *vtesnap.Snapshot) {
				for i := range s.Primary.Rows {
					s.Primary.Rows[i] = s.Primary.Rows[i][:min(i, len(s.Primary.Rows[i]))]
				}
			},
		},
		{
			name: "screen size disagrees with the terminal",
			mutate: func(s *vtesnap.Snapshot) {
				s.Primary.Screen.Width, s.Primary.Screen.Height = 3, 1
				s.Alternate.Screen.Width, s.Alternate.Screen.Height = 999, 999
			},
		},
		{
			name: "inverted scroll region",
			mutate: func(s *vtesnap.Snapshot) {
				s.Primary.TopScrollableRegion = 8
				s.Primary.BottomScrollableRegion = 2
			},
		},
		{
			name: "scroll region past the screen",
			mutate: func(s *vtesnap.Snapshot) {
				s.Primary.TopScrollableRegion = -4
				s.Primary.BottomScrollableRegion = 9999
			},
		},
		{
			name: "alternate screen active but empty",
			mutate: func(s *vtesnap.Snapshot) {
				s.Terminal.UseAlternate = true
				s.Alternate.Rows = nil
			},
		},
		{
			name:   "negative scrollback bound",
			mutate: func(s *vtesnap.Snapshot) { s.Terminal.MaxScrollback = -1 },
		},
		{
			name:   "no tabstops",
			mutate: func(s *vtesnap.Snapshot) { s.Terminal.Tabstops = nil },
		},
		{
			name: "fewer tabstops than columns",
			mutate: func(s *vtesnap.Snapshot) {
				s.Terminal.Tabstops = []bool{true}
			},
		},
		{
			name: "charset slots out of range",
			mutate: func(s *vtesnap.Snapshot) {
				s.Primary.Cursor.Charsets = []vtesnap.Charset{
					{Index: 200, Standard: 200},
				}
			},
		},
		{
			name: "pending wrap on an empty screen",
			mutate: func(s *vtesnap.Snapshot) {
				s.Terminal.PendingWrap = true
				s.Primary.Rows = nil
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			source := newFeedComponent(t, 20, 6)
			source.feed(t, "first line\r\nsecond line\r\n")
			snap := snapshotOf(t, source)
			tc.mutate(snap)

			replica := newFeedComponent(t, 20, 6)
			require.NotPanics(t, func() {
				_ = replica.comp.ApplySnapshot(snap)
			})

			// A replica that accepted a broken snapshot must still be
			// able to parse, reflow and render: the attachment carries
			// on whatever the snapshot said.
			require.NotPanics(t, func() {
				_ = replica.comp.Resize(30, 8)
				replica.feed(t, "\x1b[2J\x1b[HAFTER")
				_ = replica.comp.Resize(10, 4)
				_ = replica.text()
			})
		})
	}
}

// TestPrependHistoryEdgeCases pins that scrollback arriving after the
// screen cannot corrupt it, whatever shape the pages have.
func TestPrependHistoryEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rows [][]term.Cell
	}{
		{name: "no pages", rows: nil},
		{name: "empty page", rows: [][]term.Cell{}},
		{name: "empty row", rows: [][]term.Cell{{}}},
		{name: "nil row", rows: [][]term.Cell{nil, nil}},
		{
			name: "row wider than the screen",
			rows: [][]term.Cell{make([]term.Cell, 500)},
		},
		{
			name: "many rows",
			rows: func() [][]term.Cell {
				out := make([][]term.Cell, 500)
				for i := range out {
					out[i] = []term.Cell{{Ch: 'h', Width: 1, Bytes: 1}}
				}
				return out
			}(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c := newFeedComponent(t, 20, 6)
			c.feed(t, "live line\r\n")

			require.NotPanics(t, func() {
				c.comp.PrependHistory(tc.rows)
				c.comp.PrependHistory(tc.rows)
			})
			assert.Contains(t, c.text(), "live line",
				"prepending scrollback must not disturb the live screen")

			require.NotPanics(t, func() {
				_ = c.comp.Resize(12, 4)
				c.feed(t, "more\r\n")
			})
		})
	}
}

// feedComponent drives a Component through a pipe standing in for the
// pty master, so tests can write exact byte streams with no shell in
// the way.
type feedComponent struct {
	comp    *Component
	tracker *vteparser.ContinuationTracker
	pty     *pipeFile
	events  chan struct{}
}

func newFeedComponent(t *testing.T, width, height int) *feedComponent {
	t.Helper()
	return newFeedComponentWith(t, width, height, func(*Config) {})
}

func newFeedComponentWith(
	t *testing.T, width, height int, configure func(*Config),
) *feedComponent {
	t.Helper()

	pty := newPipeFile(t)
	events := make(chan struct{}, 1024)
	cfg := DefaultConfig()
	cfg.WidthHint = width
	cfg.HeightHint = height
	configure(&cfg)

	pt := &pipeTerminal{pty: pty}
	comp, err := NewComponent(pt, pt, nopTabManager{}, cfg)
	require.NoError(t, err)

	tracker := vteparser.NewContinuationTracker(vteparser.DefaultContinuationBytes)
	comp.parser.TrackContinuation(tracker)
	require.NoError(t, comp.Resize(width, height))

	done := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(done)
		_ = comp.Run(dropEventPublisher{events})
	})
	// The parse goroutine touches the pipe, so it must finish before
	// newPipeFile's cleanup closes the read end.
	t.Cleanup(func() {
		_ = comp.Close()
		<-done
	})

	return &feedComponent{comp: comp, tracker: tracker, pty: pty, events: events}
}

// feed writes bytes to the emulator and waits for the parser to consume
// them, so assertions never race the parse goroutine.
func (c *feedComponent) feed(t *testing.T, data string) {
	t.Helper()
	if data == "" {
		return
	}
	c.pty.feed(t, data)
	c.settle(t)
}

// settle waits for the parser to have consumed everything fed so far:
// the pipe is empty and the parse loop is back waiting on it, so the
// last batch it read was applied. A quiet period would not do: under
// load the parse goroutine can be starved for longer than any window
// short enough for a test.
func (c *feedComponent) settle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !c.pty.consumed() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the parser to consume input")
		}
		time.Sleep(time.Millisecond)
	}
	for {
		select {
		case <-c.events:
		default:
			return
		}
	}
}

// continuation reads the tracked replay bytes. It runs on the test
// goroutine while the parser is idle, which feed guarantees.
func (c *feedComponent) continuation() []byte {
	return c.tracker.Bytes()
}

func (c *feedComponent) replicateInto(t *testing.T, replica *feedComponent) {
	t.Helper()

	blob, err := c.comp.SnapshotThroughReady(c.continuation())
	require.NoError(t, err)
	history := c.comp.SnapshotHistory()

	decoder, err := vtesnap.NewDecoder(bytes.NewReader(blob))
	require.NoError(t, err)
	snap, err := decoder.Ready()
	require.NoError(t, err)
	require.NoError(t, replica.comp.ApplySnapshot(snap))
	replica.comp.PrependHistory(history)

	if len(snap.Continuation) > 0 {
		replica.feed(t, string(snap.Continuation))
	}
}

func (c *feedComponent) rows() int {
	c.comp.mu.Lock()
	defer c.comp.mu.Unlock()
	return c.comp.parserHandler.sync.primBuf.Rows()
}

// text renders the active screen, which is what a user would see.
func (c *feedComponent) text() string {
	c.comp.mu.Lock()
	defer c.comp.mu.Unlock()

	ph := c.comp.parserHandler
	if ph.useAlt {
		return term.CellsToString(ph.sync.altBuf.Cells.RawCells())
	}
	return term.CellsToString(ph.sync.primBuf.Cells.RawCells())
}

// emulatorState is everything outside the cell grid that has to agree
// between an emulator and its replica.
type emulatorState struct {
	Terminal  vtesnap.Terminal
	Primary   vtesnap.Screen
	Alternate vtesnap.Screen
	// Keys is the encoding the input path reads, which is published
	// separately from the stacks the terminal record carries.
	Keys keyEncoding
	// Reverse is read from the emulator rather than through the mode
	// bindings, so a mode missing from them still shows up.
	Reverse bool
}

func (c *feedComponent) cells() [][]term.Cell {
	c.comp.mu.Lock()
	defer c.comp.mu.Unlock()

	ph := c.comp.parserHandler
	if ph.useAlt {
		return term.CloneCells(ph.sync.altBuf.Cells.RawCells())
	}
	return term.CloneCells(ph.sync.primBuf.Cells.RawCells())
}

func (c *feedComponent) state() emulatorState {
	c.comp.mu.Lock()
	defer c.comp.mu.Unlock()

	ph := c.comp.parserHandler
	return emulatorState{
		Terminal:  ph.terminalRecord(),
		Primary:   screenRecord(vtesnap.ScreenPrimary, &ph.sync.primBuf.AltBuffer),
		Alternate: screenRecord(vtesnap.ScreenAlternate, ph.sync.altBuf),
		Keys:      ph.keyboard.encoding(),
		Reverse:   ph.modeReverseScreen,
	}
}

func assertSameScreen(t *testing.T, want, got *feedComponent) {
	t.Helper()
	assert.Equal(t, want.text(), got.text(), "screen contents")
	assert.Equal(t, want.cells(), got.cells(), "screen cells")
	assert.Equal(t, want.state(), got.state(), "emulator state")
}

// dropEventPublisher signals the test that the parser consumed a
// batch. It drops rather than blocks, so a test that stops listening
// never wedges the parse goroutine.
type dropEventPublisher struct{ ch chan struct{} }

func (p dropEventPublisher) PublishEvent(term.Event) error {
	select {
	case p.ch <- struct{}{}:
	default:
	}
	return nil
}

// pipeFile stands in for a pty master: reads deliver whatever a test
// feeds, writes are recorded so query replies can be inspected.
type pipeFile struct {
	r *os.File
	w *os.File

	closeOnce sync.Once
	eofOnce   sync.Once
	// eof is closed once a read reports the end of the stream.
	eof    chan struct{}
	mu     sync.Mutex
	writes bytes.Buffer

	// fed and read count the bytes put in and taken out. reading is
	// whether the reader is blocked in Read, which it only is once it
	// applied what the previous Read returned.
	fed, read atomic.Int64
	reading   atomic.Bool
}

func newPipeFile(t *testing.T) *pipeFile {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	f := &pipeFile{r: r, w: w, eof: make(chan struct{})}
	t.Cleanup(func() { _ = r.Close() })
	return f
}

func (f *pipeFile) feed(t *testing.T, data string) {
	t.Helper()
	require.NoError(t, f.put(data))
}

// put is feed for a goroutine other than the test's, which must not
// end the test.
func (f *pipeFile) put(data string) error {
	f.fed.Add(int64(len(data)))
	_, err := f.w.Write([]byte(data))
	return err
}

// consumed reports whether the reader applied every byte put so far.
func (f *pipeFile) consumed() bool {
	return f.reading.Load() && f.read.Load() == f.fed.Load()
}

func (f *pipeFile) Name() string               { return "pipe" }
func (f *pipeFile) Fd() uintptr                { return f.r.Fd() }
func (f *pipeFile) Stat() (os.FileInfo, error) { return workspacetest.FileInfo{}, nil }
func (f *pipeFile) Sync() error                { return nil }
func (f *pipeFile) Truncate(int64) error       { return nil }
func (f *pipeFile) Seek(int64, int) (int64, error) {
	return 0, errors.New("pipe is not seekable")
}
func (f *pipeFile) Read(b []byte) (int, error) {
	f.reading.Store(true)
	n, err := f.r.Read(b)
	f.reading.Store(false)
	f.read.Add(int64(n))
	if err != nil {
		f.eofOnce.Do(func() { close(f.eof) })
	}
	return n, err
}

func (f *pipeFile) ReadAt([]byte, int64) (int, error) { return 0, io.EOF }

// Close ends the stream so the component's read loop returns.
func (f *pipeFile) Close() error {
	f.closeOnce.Do(func() { _ = f.w.Close() })
	return nil
}

func (f *pipeFile) Write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes.Write(b)
}

// pipeSlave is the child's end of the pipe pty. The component closes
// its slave copy right after spawning, which must not end the stream
// the test is still feeding through the master.
type pipeSlave struct{ *pipeFile }

func (pipeSlave) Close() error { return nil }

// pipeTerminal hands out one pipe-backed pty and starts no process.
type pipeTerminal struct{ pty *pipeFile }

func (p *pipeTerminal) NewPty(context.Context) (workspaceapi.Pty, error) {
	return workspaceapi.Pty{Master: p.pty, Slave: pipeSlave{p.pty}}, nil
}

func (p *pipeTerminal) SetPtySize(workspaceapi.Pty, workspaceapi.PtySize) error { return nil }

func (p *pipeTerminal) ForegroundProcessGroup(workspaceapi.Pty) (int, error) {
	return 0, errors.ErrUnsupported
}

// newPipeHandler builds a Handler reading a pipe-backed pty. Its cleanup
// waits for the handler's read loop to reach the end of the stream
// before the pipe is closed: the loop takes the pipe's descriptor when
// it starts, and a descriptor closed under it may already belong to
// another test running in parallel.
func newPipeHandler(
	t *testing.T, tm browser.TabManager, cfg Config,
) (*Handler, *pipeFile, chan struct{}) {
	t.Helper()
	pty := newPipeFile(t)
	pt := &pipeTerminal{pty: pty}
	events := make(chan struct{}, 16)
	h, err := NewHandler(dropEventPublisher{events}, nopNotifications{},
		pt, pt, tm, cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = h.Close()
		select {
		case <-pty.eof:
		case <-time.After(10 * time.Second):
			t.Error("the handler never read the end of its stream")
		}
	})
	return h, pty, events
}

func (p *pipeTerminal) StartCommand(
	context.Context, workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	return 0, nil
}

func (p *pipeTerminal) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }

func (p *pipeTerminal) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}

func (p *pipeTerminal) Close() error { return nil }

// snapshotOf round-trips a component through the wire format, which is
// the only way a replica ever receives one.
func snapshotOf(t *testing.T, c *feedComponent) *vtesnap.Snapshot {
	t.Helper()

	blob, err := c.comp.SnapshotThroughReady(c.continuation())
	require.NoError(t, err)
	decoder, err := vtesnap.NewDecoder(bytes.NewReader(blob))
	require.NoError(t, err)
	snap, err := decoder.Ready()
	require.NoError(t, err)
	return snap
}
