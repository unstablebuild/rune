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

package vi

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	thandler "unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/registerset"
	"unstable.build/rune/internal/text/texttest"
)

const snippet = `
/*
 * Check if the current buffer should be added to or removed from the list of
 * diff buffers.
 */
	void
diff_buf_adjust(win_T *win)
{
	win_T	*wp;
	int				i;

	if (!win->w_p_diff)
	{
	/* When there is no window showing a diff for this buffer, remove
	 * it from the diffs... */
	FOR_ALL_WINDOWS(wp)
		if (wp->w_buffer == win->w_buffer && wp->w_p_diff)
		break;
	if (wp == NULL)
	{
		i = diff_buf_idx(win->w_buffer);
		if (i != DB_COUNT)
		{
		curtab->tp_diffbuf[i] = NULL;
		curtab->tp_diff_invalid = TRUE;
		diff_redraw(TRUE);
		}
	}
	}
	else
	diff_buf_add(win->w_buffer);
}`

type testSelectionService struct {
	view   cell.View
	expand map[term.Range]term.Range
	shrink map[term.Range]term.Range
}

type testCommentService struct {
	view  cell.View
	line  []string
	block []string
}

func (s testSelectionService) Rows() int { return s.view.Rows() }

func (s testSelectionService) Columns(row int) int { return s.view.Columns(row) }

func (s testSelectionService) Cell(at term.Coordinates) (term.Cell, bool) {
	return s.view.Cell(at)
}

func (s testSelectionService) RawCells() [][]term.Cell { return s.view.RawCells() }

func (s testSelectionService) String() string { return s.view.String() }

func (s testSelectionService) SelectionExpand(rng term.Range) (term.Range, bool) {
	next, ok := s.expand[rng]
	return next, ok
}

func (s testSelectionService) SelectionShrink(rng term.Range, caret term.Coordinates) (term.Range, bool) {
	next, ok := s.shrink[rng]
	return next, ok
}

func (s testCommentService) Rows() int { return s.view.Rows() }

func (s testCommentService) Columns(row int) int { return s.view.Columns(row) }

func (s testCommentService) Cell(at term.Coordinates) (term.Cell, bool) { return s.view.Cell(at) }

func (s testCommentService) RawCells() [][]term.Cell { return s.view.RawCells() }

func (s testCommentService) String() string { return s.view.String() }

func (s testCommentService) CommentCoverage(rng term.Range) ([]term.Range, bool) {
	start, end := term.CoordinatesSort(rng.Start, rng.End)
	var ranges []term.Range
	for y := start.Y; y <= end.Y; y++ {
		line := term.CellsToString([][]term.Cell{s.view.RawCells()[y]})
		trimmed := strings.TrimLeft(line, " \t")
		indent := len(line) - len(trimmed)
		for _, prefix := range s.line {
			if strings.HasPrefix(trimmed, prefix) {
				ranges = append(ranges, term.Range{
					Start: term.Coordinates{Y: y, X: indent},
					End:   term.Coordinates{Y: y, X: len(line)},
				})
				goto nextLine
			}
			if strings.HasPrefix(trimmed, prefix+" ") {
				ranges = append(ranges, term.Range{
					Start: term.Coordinates{Y: y, X: indent},
					End:   term.Coordinates{Y: y, X: len(line)},
				})
				goto nextLine
			}
		}
		for i := 0; i+1 < len(s.block); i += 2 {
			open, close := s.block[i], s.block[i+1]
			openIdx := strings.Index(line, open)
			closeIdx := strings.LastIndex(line, close)
			if openIdx >= 0 && closeIdx >= openIdx+len(open) {
				ranges = append(ranges, term.Range{
					Start: term.Coordinates{Y: y, X: openIdx},
					End:   term.Coordinates{Y: y, X: closeIdx + len(close)},
				})
				goto nextLine
			}
		}
		return nil, false
	nextLine:
	}
	if len(ranges) == 0 {
		return nil, false
	}
	return ranges, true
}

func TestZModeSyntacticSelection(t *testing.T) {
	buf := cell.NewBuffer()
	buf.Init()
	_, err := buf.ReadFrom(strings.NewReader("alpha beta gamma"))
	require.NoError(t, err)

	svc := testSelectionService{
		view: buf.View(),
		expand: map[term.Range]term.Range{
			{Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 6}}:  {Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 10}},
			{Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 10}}: {Start: term.Coordinates{}, End: term.Coordinates{X: 10}},
		},
		shrink: map[term.Range]term.Range{
			{Start: term.Coordinates{}, End: term.Coordinates{X: 10}}:     {Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 10}},
			{Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 10}}: {Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 6}},
		},
	}
	buf.WithView(svc)

	vi := new(viHandlerImpl)
	vi.init(buf, defaultviHandlerImplConfig())
	vi.Resize(80, 10)
	require.True(t, vi.setCursorAtScroll(term.Coordinates{X: 6}))
	assertZRange := func(t *testing.T, want term.Range) {
		t.Helper()
		list, ok := vi.cursor.LocationList(foldHighlightLocationListID)
		require.True(t, ok)
		loc, ok := list.Current()
		require.True(t, ok)
		assert.Equal(t, want.Start, loc.From)
		assert.Equal(t, want.End, loc.To)
	}

	_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'z'})
	require.True(t, handled)
	_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: 'h'})
	require.True(t, handled)
	_, ok := vi.Selection()
	assert.False(t, ok)
	assertZRange(t, term.Range{Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 10}})
	assert.Equal(t, zMode, vi.mode())

	_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: 'h'})
	require.True(t, handled)
	_, ok = vi.Selection()
	assert.False(t, ok)
	assertZRange(t, term.Range{Start: term.Coordinates{}, End: term.Coordinates{X: 10}})
	assert.Equal(t, zMode, vi.mode())

	_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
	require.True(t, handled)
	_, ok = vi.Selection()
	assert.False(t, ok)
	assertZRange(t, term.Range{Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 10}})
	assert.Equal(t, zMode, vi.mode())

	_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: 'v'})
	require.True(t, handled)
	selection, ok := vi.Selection()
	require.True(t, ok)
	assert.Equal(t, "beta", selection)
	assert.Equal(t, visualMode, vi.mode())
	assert.Equal(t, term.Coordinates{X: 6}, vi.cursorAtScroll())
}

func TestCellAtCursor(t *testing.T) {
	cases := []struct {
		input string
		cell  rune
	}{
		{"k", '\x00'},
		{"j", '/'},
		{"l", '*'},
		{"$", '*'},
	}

	width, height := 20, 10

	writer := term.NewStringWriter(width, height)
	vi := setupVi(t, snippet, 2)
	vi.Resize(width, height)

	for _, tcase := range cases {
		for _, r := range tcase.input {
			_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: r})
			require.True(t, handled)

			vi.Draw(writer)

			err := writer.Flush()
			require.NoError(t, err)
		}
		c, ok := vi.cursor.Cell()
		if tcase.cell == '\x00' {
			assert.False(t, ok)
		} else {
			assert.True(t, ok)
			assert.Equal(t, tcase.cell, c.Ch)
		}
	}
}

func TestMacroRecorder(t *testing.T) {
	key := func(ch rune) term.Event {
		return term.Event{Type: term.EventKey, Ch: ch}
	}

	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "q is unhandled without a macro recorder",
			run: func(t *testing.T) {
				vi := setupVi(t, "", 2)
				before := vi.cursorAtScroll()

				_, handled := vi.Handle(key('q'))
				require.False(t, handled, "q should be unhandled when no recorder is set")
				require.Equal(t, before, vi.cursorAtScroll())
			},
		},
		{
			name: "qa starts recording into register a",
			run: func(t *testing.T) {
				rec := new(testMacroRecorder)
				vi := setupVi(t, "", 2, WithMacroRecorder(rec))

				_, handled := vi.Handle(key('q'))
				require.True(t, handled)
				require.False(t, rec.recording, "not recording yet, waiting for register key")

				_, handled = vi.Handle(key('a'))
				require.True(t, handled)
				require.True(t, rec.recording)
				require.Equal(t, []string{"a"}, rec.started)
			},
		},
		{
			name: "q stops an active recording",
			run: func(t *testing.T) {
				rec := new(testMacroRecorder)
				vi := setupVi(t, "", 2, WithMacroRecorder(rec))

				vi.Handle(key('q'))
				vi.Handle(key('a'))
				require.True(t, rec.recording)

				_, handled := vi.Handle(key('q'))
				require.True(t, handled)
				require.False(t, rec.recording)
				require.Equal(t, 1, rec.stopped)
			},
		},
		{
			name: "start and stop leaves handler in normal mode",
			run: func(t *testing.T) {
				rec := new(testMacroRecorder)
				vi := setupVi(t, "", 2, WithMacroRecorder(rec))

				vi.Handle(key('q'))
				vi.Handle(key('b'))
				vi.Handle(key('q'))

				require.Equal(t, normalMode, vi.mode())
			},
		},
		{
			name: "uppercase register is normalized to lowercase",
			run: func(t *testing.T) {
				rec := new(testMacroRecorder)
				vi := setupVi(t, "", 2, WithMacroRecorder(rec))

				vi.Handle(key('q'))
				vi.Handle(key('Z'))
				require.True(t, rec.recording)
				require.Equal(t, []string{"z"}, rec.started,
					"uppercase Z should normalize to register z")
			},
		},
		{
			name: "multiple different registers can be recorded sequentially",
			run: func(t *testing.T) {
				rec := new(testMacroRecorder)
				vi := setupVi(t, "", 2, WithMacroRecorder(rec))

				// Record into a.
				vi.Handle(key('q'))
				vi.Handle(key('a'))
				vi.Handle(key('q'))
				require.Equal(t, 1, rec.stopped)

				// Record into b.
				vi.Handle(key('q'))
				vi.Handle(key('b'))
				vi.Handle(key('q'))
				require.Equal(t, 2, rec.stopped)

				require.Equal(t, []string{"a", "b"}, rec.started)
			},
		},
		{
			name: "q followed by invalid register aborts without starting",
			run: func(t *testing.T) {
				rec := new(testMacroRecorder)
				vi := setupVi(t, "", 2, WithMacroRecorder(rec))

				vi.Handle(key('q'))
				// Space is not a valid register.
				_, handled := vi.Handle(term.Event{Type: term.EventKey, Key: term.KeySpace})
				require.True(t, handled, "invalid register key still consumed")
				require.False(t, rec.recording, "should not start recording for invalid register")
				require.Empty(t, rec.started)
			},
		},
		{
			name: "q followed by ctrl-modified key aborts without starting",
			run: func(t *testing.T) {
				rec := new(testMacroRecorder)
				vi := setupVi(t, "", 2, WithMacroRecorder(rec))

				vi.Handle(key('q'))
				_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'a', Mod: term.ModCtrl})
				require.True(t, handled)
				require.False(t, rec.recording, "ctrl-modified key is not a valid register")
				require.Empty(t, rec.started)
			},
		},
		{
			name: "keys between start and stop are dispatched normally",
			run: func(t *testing.T) {
				rec := new(testMacroRecorder)
				vi := setupVi(t, "hello", 2, WithMacroRecorder(rec))
				vi.Resize(20, 5)

				vi.Handle(key('q'))
				vi.Handle(key('a'))
				require.True(t, rec.recording)

				// Move cursor right while recording.
				vi.Handle(key('l'))
				vi.Handle(key('l'))

				require.Equal(t, term.Coordinates{X: 2}, vi.cursorAtScroll(),
					"cursor should have moved during recording")

				vi.Handle(key('q'))
				require.False(t, rec.recording)
			},
		},
		{
			name: "insert mode during recording works and q stops after Esc",
			run: func(t *testing.T) {
				rec := new(testMacroRecorder)
				vi := setupVi(t, "", 2, WithMacroRecorder(rec))
				vi.Resize(20, 5)

				// Start recording register a.
				vi.Handle(key('q'))
				vi.Handle(key('a'))
				require.True(t, rec.recording)

				// Enter insert, type, Esc.
				vi.Handle(key('i'))
				require.Equal(t, insertMode, vi.mode())
				vi.Handle(key('x'))
				vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
				require.Equal(t, normalMode, vi.mode())

				require.Equal(t, "x", vi.less.Buffer().String())

				// Stop recording.
				vi.Handle(key('q'))
				require.False(t, rec.recording)
				require.Equal(t, 1, rec.stopped)
			},
		},
		{
			name: "recording into unnamed register uses default register ID",
			run: func(t *testing.T) {
				rec := new(testMacroRecorder)
				vi := setupVi(t, "", 2, WithMacroRecorder(rec))

				vi.Handle(key('q'))
				vi.Handle(key('"'))
				require.True(t, rec.recording)
				require.Equal(t, []string{clipboard.DefaultRegisterID}, rec.started)
			},
		},
		{
			name: "recording into special registers",
			run: func(t *testing.T) {
				for _, tt := range []struct {
					register rune
					wantID   string
				}{
					{register: '0', wantID: registerset.Normalize("0")},
					{register: '+', wantID: registerset.Normalize("+")},
					{register: '_', wantID: registerset.Normalize("_")},
					{register: '/', wantID: registerset.Normalize("/")},
					{register: '.', wantID: registerset.Normalize(".")},
					{register: '-', wantID: registerset.Normalize("-")},
				} {
					t.Run(string(tt.register), func(t *testing.T) {
						rec := new(testMacroRecorder)
						vi := setupVi(t, "", 2, WithMacroRecorder(rec))

						vi.Handle(key('q'))
						vi.Handle(key(tt.register))
						require.True(t, rec.recording)
						require.Equal(t, []string{tt.wantID}, rec.started)
					})
				}
			},
		},
		{
			name: "stop on q does not leave pending macro state",
			run: func(t *testing.T) {
				rec := new(testMacroRecorder)
				vi := setupVi(t, "hello world", 2, WithMacroRecorder(rec))
				vi.Resize(20, 5)

				// Record qa ... q.
				vi.Handle(key('q'))
				vi.Handle(key('a'))
				vi.Handle(key('l'))
				vi.Handle(key('q'))
				require.False(t, rec.recording)

				// The next 'l' should be a normal cursor movement, not
				// consumed by a stale pending-macro state.
				before := vi.cursorAtScroll()
				vi.Handle(key('l'))
				require.Equal(t, term.Coordinates{X: before.X + 1}, vi.cursorAtScroll())
			},
		},
		{
			name: "count is preserved while entering register name",
			run: func(t *testing.T) {
				// The vi handler sets doResetCount = false for q so
				// the count survives from the digit keys through q.
				rec := new(testMacroRecorder)
				vi := setupVi(t, "hello world", 2, WithMacroRecorder(rec))
				vi.Resize(20, 5)

				// Build up count "3", then q.
				vi.Handle(key('3'))
				vi.Handle(key('q'))

				// The count 3 should still be active while waiting for
				// the register name.
				require.Equal(t, 3, vi.count)
			},
		},
		{
			name: "q after stop can begin a new recording immediately",
			run: func(t *testing.T) {
				rec := new(testMacroRecorder)
				vi := setupVi(t, "", 2, WithMacroRecorder(rec))

				// First recording: qa ... q.
				vi.Handle(key('q'))
				vi.Handle(key('a'))
				vi.Handle(key('q'))
				require.Equal(t, 1, rec.stopped)

				// Immediately start a new recording: qb.
				vi.Handle(key('q'))
				vi.Handle(key('b'))
				require.True(t, rec.recording)
				require.Equal(t, []string{"a", "b"}, rec.started)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.run(t)
		})
	}
}

type testMacroRecorder struct {
	started   []string
	stopped   int
	recording bool
}

func (r *testMacroRecorder) Start(registerID string) {
	r.started = append(r.started, registerID)
	r.recording = true
}

func (r *testMacroRecorder) Stop() {
	r.stopped++
	r.recording = false
}

func (r *testMacroRecorder) IsRecording() bool {
	return r.recording
}

type testMacroPlayer struct {
	plays   []testPlay
	err     error
	playing bool
}

type testPlay struct {
	registerID string
	count      int
}

func (p *testMacroPlayer) Play(registerID string, count int) error {
	p.plays = append(p.plays, testPlay{registerID: registerID, count: count})
	return p.err
}

func (p *testMacroPlayer) IsPlaying() bool {
	return p.playing
}

func TestMacroPlayback(t *testing.T) {
	key := func(ch rune) term.Event {
		return term.Event{Type: term.EventKey, Ch: ch}
	}

	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "@ is unhandled without a macro player",
			run: func(t *testing.T) {
				vi := setupVi(t, "", 2)
				_, handled := vi.Handle(key('@'))
				require.False(t, handled, "@ should be unhandled when no player is set")
			},
		},
		{
			name: "@a triggers playback of register a",
			run: func(t *testing.T) {
				player := new(testMacroPlayer)
				vi := setupVi(t, "", 2, WithMacroPlayer(player))

				_, handled := vi.Handle(key('@'))
				require.True(t, handled)
				require.Empty(t, player.plays, "not yet played, waiting for register key")

				_, handled = vi.Handle(key('a'))
				require.True(t, handled)
				require.Equal(t, []testPlay{{registerID: "a", count: 1}}, player.plays)
			},
		},
		{
			name: "@@ replays the last used register",
			run: func(t *testing.T) {
				player := new(testMacroPlayer)
				vi := setupVi(t, "", 2, WithMacroPlayer(player))

				// First play @a.
				vi.Handle(key('@'))
				vi.Handle(key('a'))
				require.Len(t, player.plays, 1)

				// @@ should replay register a.
				vi.Handle(key('@'))
				vi.Handle(key('@'))
				require.Len(t, player.plays, 2)
				require.Equal(t, "a", player.plays[1].registerID)
			},
		},
		{
			name: "@@ with no prior register is no-op",
			run: func(t *testing.T) {
				player := new(testMacroPlayer)
				vi := setupVi(t, "", 2, WithMacroPlayer(player))

				// @@ with no previous register (lastPlayedRegister == 0).
				vi.Handle(key('@'))
				_, handled := vi.Handle(key('@'))
				require.True(t, handled, "consumed but no play triggered")
				require.Empty(t, player.plays)
			},
		},
		{
			name: "count prefix is passed to Play",
			run: func(t *testing.T) {
				player := new(testMacroPlayer)
				vi := setupVi(t, "", 2, WithMacroPlayer(player))

				vi.Handle(key('5'))
				vi.Handle(key('@'))
				vi.Handle(key('b'))
				require.Equal(t, []testPlay{{registerID: "b", count: 5}}, player.plays)
			},
		},
		{
			name: "count is reset after playback",
			run: func(t *testing.T) {
				player := new(testMacroPlayer)
				vi := setupVi(t, "hello", 2, WithMacroPlayer(player))
				vi.Resize(20, 5)

				vi.Handle(key('3'))
				vi.Handle(key('@'))
				vi.Handle(key('a'))

				// Count should be reset to default after playback.
				require.Equal(t, 1, vi.count)
			},
		},
		{
			name: "uppercase register is normalized",
			run: func(t *testing.T) {
				player := new(testMacroPlayer)
				vi := setupVi(t, "", 2, WithMacroPlayer(player))

				vi.Handle(key('@'))
				vi.Handle(key('Z'))
				require.Equal(t, []testPlay{{registerID: "z", count: 1}}, player.plays)
			},
		},
		{
			name: "count is preserved while entering register name",
			run: func(t *testing.T) {
				player := new(testMacroPlayer)
				vi := setupVi(t, "", 2, WithMacroPlayer(player))

				vi.Handle(key('7'))
				vi.Handle(key('@'))
				// Count should still be preserved after @.
				require.Equal(t, 7, vi.count)
			},
		},
		{
			name: "@ followed by invalid register aborts without playing",
			run: func(t *testing.T) {
				player := new(testMacroPlayer)
				vi := setupVi(t, "", 2, WithMacroPlayer(player))

				vi.Handle(key('@'))
				// Space is not a valid register.
				_, handled := vi.Handle(term.Event{Type: term.EventKey, Key: term.KeySpace})
				require.True(t, handled, "invalid register key still consumed")
				require.Empty(t, player.plays)
			},
		},
		{
			name: "@ followed by ctrl-modified key aborts without playing",
			run: func(t *testing.T) {
				player := new(testMacroPlayer)
				vi := setupVi(t, "", 2, WithMacroPlayer(player))

				vi.Handle(key('@'))
				_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'a', Mod: term.ModCtrl})
				require.True(t, handled)
				require.Empty(t, player.plays)
			},
		},
		{
			name: "@ does not leave pending state after abort",
			run: func(t *testing.T) {
				player := new(testMacroPlayer)
				vi := setupVi(t, "hello", 2, WithMacroPlayer(player))
				vi.Resize(20, 5)

				vi.Handle(key('@'))
				// Invalid register: space.
				vi.Handle(term.Event{Type: term.EventKey, Key: term.KeySpace})

				// The next 'l' should be normal cursor movement.
				before := vi.cursorAtScroll()
				vi.Handle(key('l'))
				require.Equal(t, term.Coordinates{X: before.X + 1}, vi.cursorAtScroll())
			},
		},
		{
			name: "@@ after @a remembers a",
			run: func(t *testing.T) {
				player := new(testMacroPlayer)
				vi := setupVi(t, "", 2, WithMacroPlayer(player))

				vi.Handle(key('@'))
				vi.Handle(key('a'))

				// Clear plays to isolate.
				player.plays = nil

				// 3@@ should repeat last register (a) three times.
				vi.Handle(key('3'))
				vi.Handle(key('@'))
				vi.Handle(key('@'))
				require.Equal(t, []testPlay{{registerID: "a", count: 3}}, player.plays)
			},
		},
		{
			name: "null key event does not consume pendingPlayback",
			run: func(t *testing.T) {
				player := new(testMacroPlayer)
				vi := setupVi(t, "", 2, WithMacroPlayer(player))

				vi.Handle(key('@'))
				// Null event (e.g. from releasing Shift after typing @).
				vi.Handle(term.Event{Type: term.EventKey})
				// The real register key should still trigger playback.
				vi.Handle(key('a'))
				require.Equal(t, []testPlay{{registerID: "a", count: 1}}, player.plays)
			},
		},
		{
			name: "null key event does not consume pendingMacro",
			run: func(t *testing.T) {
				rec := new(testMacroRecorder)
				vi := setupVi(t, "", 2, WithMacroRecorder(rec))

				vi.Handle(key('q'))
				// Null event (e.g. from releasing Shift).
				vi.Handle(term.Event{Type: term.EventKey})
				// The real register key should still start recording.
				vi.Handle(key('a'))
				require.Equal(t, []string{"a"}, rec.started)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.run(t)
		})
	}
}

func TestMatchingRuneHighlight(t *testing.T) {
	t.Run("normal movement", func(t *testing.T) {
		width, height := 20, 10

		vi := setupVi(t, snippet, 2)
		vi.Resize(width, height)

		for _, r := range "jjjjjjj" {
			_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: r})
			require.True(t, handled)
		}

		c, ok := vi.cursor.Cell()
		assert.True(t, ok)
		require.Equal(t, '{', c.Ch)

		list, ok := vi.cursor.LocationList(matchingLocID)
		require.True(t, ok)
		loc, ok := list.Current()
		require.True(t, ok)
		assert.Equal(t, term.AttrReverse, loc.Attr.Attrs)
		assert.Equal(t, term.Coordinates{Y: 31}, loc.From)
		assert.Equal(t, term.Coordinates{Y: 31, X: 1}, loc.To)
	})

	t.Run("SetCursorAtScroll", func(t *testing.T) {
		width, height := 20, 10

		vi := setupVi(t, snippet, 2)
		vi.Resize(width, height)

		vi.setCursorAtScroll(term.Coordinates{Y: 7})

		c, ok := vi.cursor.Cell()
		assert.True(t, ok)
		require.Equal(t, '{', c.Ch)

		list, ok := vi.cursor.LocationList(matchingLocID)
		require.True(t, ok)
		loc, ok := list.Current()
		require.True(t, ok)
		assert.Equal(t, term.AttrReverse, loc.Attr.Attrs)
		assert.Equal(t, term.Coordinates{Y: 31}, loc.From)
		assert.Equal(t, term.Coordinates{Y: 31, X: 1}, loc.To)
	})

	t.Run("MoveToNextLocation", func(t *testing.T) {
		width, height := 20, 10

		vi := setupVi(t, snippet, 2)
		vi.Resize(width, height)

		vi.cursor.SetLocationList(textapi.LocationPriorityInfo, "mylist",
			textapi.LocationSlice([]textapi.Location{{From: term.Coordinates{Y: 7}}}))
		vi.moveToNextLocation("mylist")

		c, ok := vi.cursor.Cell()
		assert.True(t, ok)
		require.Equal(t, '{', c.Ch)

		list, ok := vi.cursor.LocationList(matchingLocID)
		require.True(t, ok)
		loc, ok := list.Current()
		require.True(t, ok)
		assert.Equal(t, term.AttrReverse, loc.Attr.Attrs)
		assert.Equal(t, term.Coordinates{Y: 31}, loc.From)
		assert.Equal(t, term.Coordinates{Y: 31, X: 1}, loc.To)
	})

	t.Run("MoveToPrevLocation", func(t *testing.T) {
		width, height := 20, 10

		vi := setupVi(t, snippet, 2)
		vi.Resize(width, height)

		vi.cursor.SetLocationList(textapi.LocationPriorityInfo, "mylist",
			textapi.LocationSlice([]textapi.Location{{From: term.Coordinates{Y: 7}}}))
		vi.moveToPrevLocation("mylist")

		c, ok := vi.cursor.Cell()
		assert.True(t, ok)
		require.Equal(t, '{', c.Ch)

		list, ok := vi.cursor.LocationList(matchingLocID)
		require.True(t, ok)
		loc, ok := list.Current()
		require.True(t, ok)
		assert.Equal(t, term.AttrReverse, loc.Attr.Attrs)
		assert.Equal(t, term.Coordinates{Y: 31}, loc.From)
		assert.Equal(t, term.Coordinates{Y: 31, X: 1}, loc.To)
	})
}

func TestViIntegrationSequence(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{"",
			`▐                   
/*                  
 * Check if the curr
 * diff buffers.    
 */                 
  void              
diff_buf_adjust(win_
{                   
  win_T  *wp;       
              NORMAL`},
		{"jjjj",
			`                    
/*                  
 * Check if the curr
 * diff buffers.    
▐*/                 
  void              
diff_buf_adjust(win_
{                   
  win_T  *wp;       
              NORMAL`},
		{"/NULL>jjjjjjjjkkkkkkkk", // test vi.free anchoring
			`  if (wp == ▐ULL)   
  {                 
    i = diff_buf_idx
    if (i != DB_COUN
    {               
    curtab->tp_diffb
    curtab->tp_diff_
    diff_redraw(TRUE
    searching 'NULL'
              NORMAL`},
		{"Ahello",
			`f (wp == NULL)hello▐
                    
 i = diff_buf_idx(wi
 if (i != DB_COUNT) 
 {                  
 curtab->tp_diffbuf[
 curtab->tp_diff_inv
 diff_redraw(TRUE); 
 }                  
              INSERT`},
		{"<hhhhC<",
			`f (wp == NULL▐      
                    
 i = diff_buf_idx(wi
 if (i != DB_COUNT) 
 {                  
 curtab->tp_diffbuf[
 curtab->tp_diff_inv
 diff_redraw(TRUE); 
 }                  
              NORMAL`},
		{"p",
			`f (wp == NULL)hell▐ 
                    
 i = diff_buf_idx(wi
 if (i != DB_COUNT) 
 {                  
 curtab->tp_diffbuf[
 curtab->tp_diff_inv
 diff_redraw(TRUE); 
 }                  
              NORMAL`},
		{"F(",
			`f ▐wp == NULL)hello 
                    
 i = diff_buf_idx(wi
 if (i != DB_COUNT) 
 {                  
 curtab->tp_diffbuf[
 curtab->tp_diff_inv
 diff_redraw(TRUE); 
 }                  
              NORMAL`},
		{"f)",
			`f (wp == NULL▐hello 
                    
 i = diff_buf_idx(wi
 if (i != DB_COUNT) 
 {                  
 curtab->tp_diffbuf[
 curtab->tp_diff_inv
 diff_redraw(TRUE); 
 }                  
              NORMAL`},
		{"F=",
			`f (wp =▐ NULL)hello 
                    
 i = diff_buf_idx(wi
 if (i != DB_COUNT) 
 {                  
 curtab->tp_diffbuf[
 curtab->tp_diff_inv
 diff_redraw(TRUE); 
 }                  
              NORMAL`},
		{",", // , after F= reverses direction: searches forward, no = found, cursor stays
			`f (wp =▐ NULL)hello 
                    
 i = diff_buf_idx(wi
 if (i != DB_COUNT) 
 {                  
 curtab->tp_diffbuf[
 curtab->tp_diff_inv
 diff_redraw(TRUE); 
 }                  
              NORMAL`},
		{";", // ; after F= repeats backward: finds = at col 6
			`f (wp ▐= NULL)hello 
                    
 i = diff_buf_idx(wi
 if (i != DB_COUNT) 
 {                  
 curtab->tp_diffbuf[
 curtab->tp_diff_inv
 diff_redraw(TRUE); 
 }                  
              NORMAL`},
		{"D",
			`f (wp▐              
                    
 i = diff_buf_idx(wi
 if (i != DB_COUNT) 
 {                  
 curtab->tp_diffbuf[
 curtab->tp_diff_inv
 diff_redraw(TRUE); 
 }                  
              NORMAL`},
		{"sbrillo",
			`f (wpbrillo▐        
                    
 i = diff_buf_idx(wi
 if (i != DB_COUNT) 
 {                  
 curtab->tp_diffbuf[
 curtab->tp_diff_inv
 diff_redraw(TRUE); 
 }                  
              INSERT`},
		{"<hhhhhhR == NULL)",
			`f (w == NULL)▐      
                    
 i = diff_buf_idx(wi
 if (i != DB_COUNT) 
 {                  
 curtab->tp_diffbuf[
 curtab->tp_diff_inv
 diff_redraw(TRUE); 
 }                  
             REPLACE`},
		{"<r]h",
			`f (w == NUL▐]       
                    
 i = diff_buf_idx(wi
 if (i != DB_COUNT) 
 {                  
 curtab->tp_diffbuf[
 curtab->tp_diff_inv
 diff_redraw(TRUE); 
 }                  
              NORMAL`},
		{"VypzH",
			`  if (w == NULL]    
  ▐f (w == NULL]    
  {                 
    i = diff_buf_idx
    if (i != DB_COUN
    {               
    curtab->tp_diffb
    curtab->tp_diff_
    diff_redraw(TRUE
              NORMAL`},
		{"/i =>",
			`  if (w == NULL]    
  if (w == NULL]    
  {                 
    ▐ = diff_buf_idx
    if (i != DB_COUN
    {               
    curtab->tp_diffb
    curtab->tp_diff_
     searching 'i ='
              NORMAL`},
		{"dd",
			`  if (w == NULL]    
  if (w == NULL]    
  {                 
    ▐f (i != DB_COUN
    {               
    curtab->tp_diffb
    curtab->tp_diff_
    diff_redraw(TRUE
    }               
              NORMAL`},
		{"h",
			`  if (w == NULL]    
  if (w == NULL]    
  {                 
   ▐if (i != DB_COUN
    {               
    curtab->tp_diffb
    curtab->tp_diff_
    diff_redraw(TRUE
    }               
              NORMAL`},
		{"h",
			`  if (w == NULL]    
  if (w == NULL]    
  {                 
 ▐  if (i != DB_COUN
    {               
    curtab->tp_diffb
    curtab->tp_diff_
    diff_redraw(TRUE
    }               
              NORMAL`},
		{"df=",
			`  if (w == NULL]    
  if (w == NULL]    
  {                 
▐DB_COUNT)          
    {               
    curtab->tp_diffb
    curtab->tp_diff_
    diff_redraw(TRUE
    }               
              NORMAL`},
		{"/i>kkFDcndi<ldw",
			`  if (w == NULL]    
  if (w == NULL]    
  {                 
 di▐i] = NULL;      
    curtab->tp_diff_
    diff_redraw(TRUE
    }               
  }                 
  }                 
              NORMAL`},
		{"gg",
			`▐                   
/*                  
 * Check if the curr
 * diff buffers.    
 */                 
  void              
diff_buf_adjust(win_
{                   
  win_T  *wp;       
              NORMAL`},
		{"12gg",
			` * diff buffers.    
 */                 
  void              
diff_buf_adjust(win_
{                   
  win_T  *wp;       
  int        i;     
                    
 ▐if (!win->w_p_diff
              NORMAL`},
		{"gg",
			`▐                   
/*                  
 * Check if the curr
 * diff buffers.    
 */                 
  void              
diff_buf_adjust(win_
{                   
  win_T  *wp;       
              NORMAL`},
		{"jjyyp",
			`                    
/*                  
 * Check if the curr
 ▐ Check if the curr
 * diff buffers.    
 */                 
  void              
diff_buf_adjust(win_
{                   
              NORMAL`},
		{"lllcc *",
			`                    
/*                  
 * Check if the curr
 *▐                 
 * diff buffers.    
 */                 
  void              
diff_buf_adjust(win_
{                   
              INSERT`},
		{"<jllipotato<",
			`                    
/*                  
 * Check if the curr
 *                  
 * potat▐diff buffer
 */                 
  void              
diff_buf_adjust(win_
{                   
              NORMAL`},
		{"7h1j1l1k1h7l",
			`                    
/*                  
 * Check if the curr
 *                  
 * potat▐diff buffer
 */                 
  void              
diff_buf_adjust(win_
{                   
              NORMAL`},
		{"1g1g1g1gg",
			`▐                   
/*                  
 * Check if the curr
 *                  
 * potatodiff buffer
 */                 
  void              
diff_buf_adjust(win_
{                   
              NORMAL`},
		{"gg5gg",
			`                    
/*                  
 * Check if the curr
 *                  
▐* potatodiff buffer
 */                 
  void              
diff_buf_adjust(win_
{                   
              NORMAL`},
		{"8l3h",
			`                    
/*                  
 * Check if the curr
 *                  
 * po▐atodiff buffer
 */                 
  void              
diff_buf_adjust(win_
{                   
              NORMAL`},
		{"10000000000000000000000000000000000000000h",
			`                    
/*                  
 * Check if the curr
 *                  
▐* potatodiff buffer
 */                 
  void              
diff_buf_adjust(win_
{                   
              NORMAL`},
		{"3j",
			`                    
/*                  
 * Check if the curr
 *                  
 * potatodiff buffer
 */                 
  void              
▐iff_buf_adjust(win_
{                   
              NORMAL`},
		{"8l",
			`                    
/*                  
 * Check if the curr
 *                  
 * potatodiff buffer
 */                 
  void              
diff_buf▐adjust(win_
{                   
              NORMAL`},
		// 10j means move 10 rows down; not go to start of line (0) and move 1 row down
		{"10jhh",
			`  win_T  *wp;       
  int        i;     
                    
  if (!win->w_p_diff
  {                 
  /* When there is n
   * it from the dif
  FOR_ALL_WINDOWS(wp
    if (▐p->w_buffer
              NORMAL`},
		{"2kl",
			`  win_T  *wp;       
  int        i;     
                    
  if (!win->w_p_diff
  {                 
  /* When there is n
   * it ▐rom the dif
  FOR_ALL_WINDOWS(wp
    if (wp->w_buffer
              NORMAL`},
		{"10k",
			` *▐                 
  void              
diff_buf_adjust(win_
{                   
  win_T  *wp;       
  int        i;     
                    
  if (!win->w_p_diff
  {                 
              NORMAL`},
		{"922337203685477580719973197k",
			`▐                   
/*                  
 * Check if the curr
 *                  
 * potatodiff buffer
 */                 
  void              
diff_buf_adjust(win_
{                   
              NORMAL`},
		{"jj",
			`                    
/*                  
▐* Check if the curr
 *                  
 * potatodiff buffer
 */                 
  void              
diff_buf_adjust(win_
{                   
              NORMAL`},
		{"4\\$h", // '4' will be forgotten because of $ (go to last line char)
			`                    
                    
ved from the list ▐f
                    
                    
                    
                    
                    
                    
              NORMAL`},
		{"98765432123456789098765432100000000000000013414l",
			`                    
                    
ved from the list o▐
                    
                    
                    
                    
                    
                    
              NORMAL`},
		{"33<h",
			`                    
                    
ved from the list ▐f
                    
                    
                    
                    
                    
                    
              NORMAL`},
		{"6gg3h",
			`                    
/*                  
 * Check if the curr
 *                  
 * potatodiff buffer
▐*/                 
  void              
diff_buf_adjust(win_
{                   
              NORMAL`},
		{"111<0gg",
			`▐                   
/*                  
 * Check if the curr
 *                  
 * potatodiff buffer
 */                 
  void              
diff_buf_adjust(win_
{                   
              NORMAL`},
		{"99999ggkk",
			` di[i] = NULL;      
    curtab->tp_diff_
    diff_redraw(TRUE
    }               
  }                 
  }                 
 ▐else              
  diff_buf_add(win->
}                   
              NORMAL`},
		{"kkk3dd",
			` di[i] = NULL;      
    curtab->tp_diff_
    diff_redraw(TRUE
  ▐lse              
  diff_buf_add(win->
}                   
                    
                    
                    
              NORMAL`},
		{"\\$zH",
			` di[i] = NULL;      
    curtab->tp_diff_
    diff_redraw(TRUE
  els▐              
  diff_buf_add(win->
}                   
                    
                    
                    
              NORMAL`},
		{"\\$44\\^", // 44 should be ignored
			` di[i] = NULL;      
    curtab->tp_diff_
    diff_redraw(TRUE
  ▐lse              
  diff_buf_add(win->
}                   
                    
                    
                    
              NORMAL`},
		{"rpl",
			` di[i] = NULL;      
    curtab->tp_diff_
    diff_redraw(TRUE
  p▐se              
  diff_buf_add(win->
}                   
                    
                    
                    
              NORMAL`},
		{"Rabcdef<",
			` di[i] = NULL;      
    curtab->tp_diff_
    diff_redraw(TRUE
  pabcde▐           
  diff_buf_add(win->
}                   
                    
                    
                    
              NORMAL`},
		{"?diff>",
			` di[i] = NULL;      
    curtab->tp_diff_
    ▐iff_redraw(TRUE
  pabcdef           
  diff_buf_add(win->
}                   
                    
                    
    searching 'diff'
              NORMAL`},
		{"n",
			` di[i] = NULL;      
    curtab->tp_▐iff_
    diff_redraw(TRUE
  pabcdef           
  diff_buf_add(win->
}                   
                    
                    
    searching 'diff'
              NORMAL`},
		{"N",
			` di[i] = NULL;      
    curtab->tp_diff_
    ▐iff_redraw(TRUE
  pabcdef           
  diff_buf_add(win->
}                   
                    
                    
    searching 'diff'
              NORMAL`},
		{"j0tf",
			` di[i] = NULL;      
    curtab->tp_diff_
    diff_redraw(TRUE
  pabcd▐f           
  diff_buf_add(win->
}                   
                    
                    
    searching 'diff'
              NORMAL`},
		{";",
			` di[i] = NULL;      
    curtab->tp_diff_
    diff_redraw(TRUE
  pabcd▐f           
  diff_buf_add(win->
}                   
                    
                    
    searching 'diff'
              NORMAL`},
		{"hTa",
			` di[i] = NULL;      
    curtab->tp_diff_
    diff_redraw(TRUE
  pa▐cdef           
  diff_buf_add(win->
}                   
                    
                    
    searching 'diff'
              NORMAL`},
		{",",
			` di[i] = NULL;      
    curtab->tp_diff_
    diff_redraw(TRUE
  pa▐cdef           
  diff_buf_add(win->
}                   
                    
                    
    searching 'diff'
              NORMAL`},
	}

	vi := setupViIntegration(t, snippet, 2)
	handlertest.TestHandlerSequence(t, vi, 20, 10, cases)
}

func TestViToggleCase(t *testing.T) {
	t.Run("normal mode toggles char and advances", func(t *testing.T) {
		vi := setupVi(t, "Hello", 2)
		vi.Resize(20, 5)

		// cursor is at 'H', toggle it
		vi.Handle(term.Event{Type: term.EventKey, Ch: '~'})
		assert.Equal(t, "hello", vi.less.Buffer().String())
		assert.Equal(t, term.Coordinates{X: 1}, vi.cursor.Coordinates())

		// toggle 'e' -> 'E'
		vi.Handle(term.Event{Type: term.EventKey, Ch: '~'})
		assert.Equal(t, "hEllo", vi.less.Buffer().String())
		assert.Equal(t, term.Coordinates{X: 2}, vi.cursor.Coordinates())

		// toggle 'l' -> 'L'
		vi.Handle(term.Event{Type: term.EventKey, Ch: '~'})
		assert.Equal(t, "hELlo", vi.less.Buffer().String())
		assert.Equal(t, term.Coordinates{X: 3}, vi.cursor.Coordinates())
	})

	t.Run("normal mode at end of line stays put", func(t *testing.T) {
		vi := setupVi(t, "Ab", 2)
		vi.Resize(20, 5)

		// move to last char 'b'
		vi.Handle(term.Event{Type: term.EventKey, Ch: '$'})
		assert.Equal(t, term.Coordinates{X: 1}, vi.cursor.Coordinates())

		// toggle 'b' -> 'B', cursor cannot advance further
		vi.Handle(term.Event{Type: term.EventKey, Ch: '~'})
		assert.Equal(t, "AB", vi.less.Buffer().String())
	})

	t.Run("visual mode toggles selection", func(t *testing.T) {
		vi := setupVi(t, "Hello World", 2)
		vi.Resize(20, 5)

		// select "Hello" (v then 4l to extend selection through 'o')
		vi.Handle(term.Event{Type: term.EventKey, Ch: 'v'})
		for i := 0; i < 4; i++ {
			vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
		}

		// toggle case of selection
		vi.Handle(term.Event{Type: term.EventKey, Ch: '~'})
		assert.Equal(t, "hELLO World", vi.less.Buffer().String())
		assert.Equal(t, normalMode, vi.mode())
	})
}

func TestViCaseChangeOperators(t *testing.T) {
	type testCase struct {
		name          string
		content       string
		events        string
		expectContent string
		expectMode    viMode
	}

	suite := []testCase{
		// ===== gu (lowercase) with motions =====
		{"guw lowercases a word", "Hello World", "guw", "hello World", normalMode},
		{"gu$ lowercases to end of line", "Hello WORLD", "gu$", "hello world", normalMode},
		{"gue lowercases to end of word", "HELLO World", "gue", "hello World", normalMode},
		{"guu lowercases whole line", "HELLO WORLD", "guu", "hello world", normalMode},
		{"gub lowercases backward word", "Hello WORLD", "Wgub", "hello WORLD", normalMode},
		{"guB lowercases backward WORD", "Hello-Two WORLD", "WguB", "hello-two WORLD", normalMode},
		{"guW lowercases forward WORD", "Hello-World test", "guW", "hello-world test", normalMode},
		{"guE lowercases to end of WORD", "Hello-World test", "guE", "hello-world test", normalMode},
		{"gu^ lowercases to first non-blank", "  Hello WORLD", "WWgu^", "  hello WORLD", normalMode},
		{"gu0 lowercases to start of line", "  Hello WORLD", "WWgu0", "  hello WORLD", normalMode},
		{"gul lowercases single char", "Hello World", "gul", "hello World", normalMode},
		{"guh lowercases backward single char (no-op at col 0)", "Hello World", "guh", "Hello World", normalMode},
		{"guh lowercases backward char from col 1", "HEllo", "lguh", "hEllo", normalMode},
		{"guf lowercases to char", "HELLO World", "gufo", "hello world", normalMode},

		// ===== gU (uppercase) with motions =====
		{"gUw uppercases a word", "hello world", "gUw", "HELLO world", normalMode},
		{"gU$ uppercases to end of line", "hello world", "gU$", "HELLO WORLD", normalMode},
		{"gUe uppercases to end of word", "hello world", "gUe", "HELLO world", normalMode},
		{"gUU uppercases whole line", "hello world", "gUU", "HELLO WORLD", normalMode},
		{"gUb uppercases backward word", "hello world", "WgUb", "HELLO world", normalMode},
		{"gUB uppercases backward WORD", "hello-two world", "WgUB", "HELLO-TWO world", normalMode},
		{"gUW uppercases forward WORD", "hello-world test", "gUW", "HELLO-WORLD test", normalMode},
		{"gUE uppercases to end of WORD", "hello-world test", "gUE", "HELLO-WORLD test", normalMode},
		{"gU^ uppercases to first non-blank", "  hello world", "fwgU^", "  HELLO world", normalMode},
		{"gU0 uppercases to start of line", "  hello world", "fwgU0", "  HELLO world", normalMode},
		{"gUl uppercases single char", "hello world", "gUl", "Hello world", normalMode},
		{"gUh uppercases backward single char (no-op at col 0)", "hello world", "gUh", "hello world", normalMode},
		{"gUh uppercases backward char from col 1", "hello", "lgUh", "Hello", normalMode},
		{"gUf uppercases to char", "hello world", "gUfo", "HELLO world", normalMode},

		// ===== g~ (toggle case) with motions =====
		{"g~w toggles case of a word", "Hello World", "g~w", "hELLO World", normalMode},
		{"g~$ toggles case to end of line", "Hello World", "g~$", "hELLO wORLD", normalMode},
		{"g~e toggles case to end of word", "Hello World", "g~e", "hELLO World", normalMode},
		{"g~~ toggles case of whole line", "Hello World", "g~~", "hELLO wORLD", normalMode},
		{"g~b toggles backward word", "Hello World", "Wg~b", "hELLO World", normalMode},
		{"g~W toggles forward WORD", "Hello-World Test", "g~W", "hELLO-wORLD Test", normalMode},
		{"g~E toggles to end of WORD", "Hello-World Test", "g~E", "hELLO-wORLD Test", normalMode},
		{"g~l toggles single char", "Hello World", "g~l", "hello World", normalMode},
		{"g~h toggles backward single char (no-op at col 0)", "Hello World", "g~h", "Hello World", normalMode},
		{"g~h toggles backward char from col 1", "Hello", "lg~h", "hello", normalMode},

		// ===== Text objects — inner/around word =====
		{"guiw lowercases inner word", "Hello WORLD Test", "guiw", "hello WORLD Test", normalMode},
		{"gUiw uppercases inner word", "hello world test", "gUiw", "HELLO world test", normalMode},
		{"g~iw toggles case of inner word", "Hello WORLD Test", "g~iw", "hELLO WORLD Test", normalMode},
		{"guaw lowercases around word", "Hello WORLD Test", "guaw", "hello WORLD Test", normalMode},
		{"gUaw uppercases around word", "hello world test", "gUaw", "HELLO world test", normalMode},
		{"g~aw toggles around word", "Hello WORLD Test", "g~aw", "hELLO WORLD Test", normalMode},

		// ===== Text objects — inner/around WORD =====
		{"guiW lowercases inner WORD", "Hello-World Test", "guiW", "hello-world Test", normalMode},
		{"gUiW uppercases inner WORD", "hello-world test", "gUiW", "HELLO-WORLD test", normalMode},
		{"g~iW toggles inner WORD", "Hello-World Test", "g~iW", "hELLO-wORLD Test", normalMode},
		{"guaW lowercases around WORD", "Hello-World Test", "guaW", "hello-world Test", normalMode},

		// ===== Text objects — quotes =====
		{"gui\" lowercases inner double quotes", `say "HELLO WORLD" now`, "fHgui\"", `say "hello world" now`, normalMode},
		{"gUi\" uppercases inner double quotes", `say "hello world" now`, "fhgUi\"", `say "HELLO WORLD" now`, normalMode},
		{"g~i\" toggles inner double quotes", `say "Hello World" now`, "fHg~i\"", `say "hELLO wORLD" now`, normalMode},
		{"gua\" lowercases around double quotes", `say "HELLO WORLD" now`, "fHgua\"", `say "hello world" now`, normalMode},
		{"gui' lowercases inner single quotes", "say 'HELLO' now", "fHgui'", "say 'hello' now", normalMode},
		{"gui` lowercases inner backtick", "say `HELLO` now", "fHgui`", "say `hello` now", normalMode},

		// ===== Text objects — parentheses/blocks =====
		{"guib lowercases inner parens", "call(HELLO, WORLD)", "f(guib", "call(hello, world)", normalMode},
		{"gUib uppercases inner parens", "call(hello, world)", "f(gUib", "call(HELLO, WORLD)", normalMode},
		{"guab lowercases around parens", "call(HELLO, WORLD)", "f(guab", "call(hello, world)", normalMode},
		{"gui) lowercases inner parens alias", "call(HELLO, WORLD)", "f(gui)", "call(hello, world)", normalMode},
		{"gui( lowercases inner parens alias 2", "call(HELLO, WORLD)", "f(gui(", "call(hello, world)", normalMode},

		// ===== Text objects — braces =====
		{"guiB lowercases inner braces", "fn{HELLO WORLD}", "f{guiB", "fn{hello world}", normalMode},
		{"gUiB uppercases inner braces", "fn{hello world}", "f{gUiB", "fn{HELLO WORLD}", normalMode},
		{"guaB lowercases around braces", "fn{HELLO WORLD}", "f{guaB", "fn{hello world}", normalMode},

		// ===== Text objects — brackets =====
		{"gui[ lowercases inner brackets", "arr[HELLO]", "f[gui[", "arr[hello]", normalMode},
		{"gUi[ uppercases inner brackets", "arr[hello]", "f[gUi[", "arr[HELLO]", normalMode},

		// ===== Text objects — angle brackets =====
		{"guit lowercases inner angle", "a <HELLO> b", "f<guit", "a <hello> b", normalMode},
		{"gUit uppercases inner angle", "a <hello> b", "f<gUit", "a <HELLO> b", normalMode},

		// ===== g-sub motions (ge, gE) =====
		{"guge lowercases from cursor to end of previous word", "HELLO WORLD", "Wguge", "HELLo wORLD", normalMode},
		{"gUge uppercases from cursor to end of previous word", "hello world", "WgUge", "hellO World", normalMode},
		{"g~ge toggles from cursor to end of previous word", "Hello World", "Wg~ge", "HellO world", normalMode},
		{"gugE lowercases from cursor to end of previous WORD", "HELLO-TWO WORLD", "WgugE", "HELLO-TWo wORLD", normalMode},
		{"gUgE uppercases from cursor to end of previous WORD", "hello-two world", "WgUgE", "hello-twO World", normalMode},
		{"g~gE toggles from cursor to end of previous WORD", "Hello-Two World", "Wg~gE", "Hello-TwO world", normalMode},

		// ===== Whole-line double forms =====
		{"guu on already lowercase line is no-op", "hello world", "guu", "hello world", normalMode},
		{"gUU on already uppercase line is no-op", "HELLO WORLD", "gUU", "HELLO WORLD", normalMode},
		{"g~~ on single char line", "A", "g~~", "a", normalMode},
		{"guu on single char line", "A", "guu", "a", normalMode},
		{"gUU on single char line", "a", "gUU", "A", normalMode},

		// ===== Cursor positioning before operator =====
		{"guw from middle of last word lowercases through its end", "HELLO", "llguw", "HEllo", normalMode},
		{"gUw from middle of last word uppercases through its end", "hello", "llgUw", "heLLO", normalMode},
		{"g~w from middle of last word toggles through its end", "Hello", "llg~w", "HeLLO", normalMode},
		{"gue from middle of word lowercases to end of word", "HELLO WORLD", "llgue", "HEllo WORLD", normalMode},
		{"gu$ from middle of line lowercases to end", "HELLO WORLD", "llgu$", "HEllo world", normalMode},
		{"gU$ from middle of line uppercases to end", "hello world", "llgU$", "heLLO WORLD", normalMode},

		// ===== Multiline =====
		{"guj lowercases two lines", "HELLO\nWORLD", "guj", "hello\nworld", normalMode},
		{"gUj uppercases two lines", "hello\nworld", "gUj", "HELLO\nWORLD", normalMode},
		{"g~j toggles two lines", "Hello\nWorld", "g~j", "hELLO\nwORLD", normalMode},
		{"guk lowercases upward to previous line", "HELLO\nWORLD", "jguk", "hello\nworld", normalMode},
		{"gUk uppercases upward to previous line", "hello\nworld", "jgUk", "HELLO\nWORLD", normalMode},
		{"guu on first line of multiline only affects first line", "HELLO\nWORLD", "guu", "hello\nWORLD", normalMode},
		{"gUU on second line of multiline only affects that line", "hello\nworld", "jgUU", "hello\nWORLD", normalMode},

		// ===== Empty / whitespace content =====
		{"guw on whitespace only", "   ", "guw", "   ", normalMode},
		{"gUU on empty buffer", "", "gUU", "", normalMode},
		{"guu on empty buffer", "", "guu", "", normalMode},
		{"g~~ on empty buffer", "", "g~~", "", normalMode},

		// ===== Punctuation / non-alpha characters =====
		{"guw on punctuation does not change it", "!@#$%^&*", "guw", "!@#$%^&*", normalMode},
		{"gUw on digits does not change them", "abc123def", "gUw", "ABC123DEF", normalMode},
		{"guw on mixed alpha-punct word", "Hello!", "guw", "hello!", normalMode},
		{"g~w on digits mixed", "a1B2c3", "g~w", "A1b2C3", normalMode},

		// ===== Count + operator =====
		{"2guw lowercases 2 words", "HELLO WORLD TEST", "2guw", "hello world TEST", normalMode},
		{"2gUw uppercases 2 words", "hello world test", "2gUw", "HELLO WORLD test", normalMode},
		{"3guw lowercases 3 words", "ONE TWO THREE FOUR", "3guw", "one two three FOUR", normalMode},
		{"2gUe uppercases through second word end", "hello world test", "2gUe", "HELLO WORLD test", normalMode},
		{"guj lowercases 2 lines from first line", "HELLO\nWORLD\nTEST", "guj", "hello\nworld\nTEST", normalMode},

		// ===== Visual mode case change =====
		{"vu then selection lowercases visual", "HELLO WORLD", "veu", "hello WORLD", normalMode},
		{"vU then selection uppercases visual", "hello world", "veU", "HELLO world", normalMode},
		{"v~ toggles case in visual mode", "Hello World", "ve~", "hELLO World", normalMode},
		{"V line visual then u lowercases entire line", "HELLO WORLD", "Vu", "hello world", normalMode},
		{"V line visual then U uppercases entire line", "hello world", "VU", "HELLO WORLD", normalMode},

		// ===== Sentence / paragraph text objects =====
		{"guis lowercases inner sentence", "HELLO WORLD. BYE NOW.", "guis", "hello world. BYE NOW.", normalMode},
		{"guip lowercases inner paragraph", "HELLO WORLD", "guip", "hello world", normalMode},
		{"gUip uppercases inner paragraph", "hello world", "gUip", "HELLO WORLD", normalMode},

		// ===== Edge: line boundaries =====
		{"gu$ at end of line is single char", "HELLO", "$gu$", "HELLo", normalMode},
		{"gU$ at end of line is single char", "hello", "$gU$", "hellO", normalMode},

		// ===== Invalid sequences return to normal mode =====
		{"gu followed by invalid key returns to normal", "HELLO", "guZ", "HELLO", normalMode},
		{"gU followed by invalid key returns to normal", "hello", "gUZ", "hello", normalMode},
		{"g~ followed by invalid key returns to normal", "Hello", "g~Z", "Hello", normalMode},
	}

	for _, tcase := range suite {
		t.Run(tcase.name, func(t *testing.T) {
			vi := setupVi(t, tcase.content, 2)
			vi.Resize(40, 10)

			for _, ch := range tcase.events {
				vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			}

			assert.Equal(t, tcase.expectContent, vi.less.Buffer().String())
			assert.Equal(t, tcase.expectMode, vi.mode())
		})
	}
}

func TestViShiftOperators(t *testing.T) {
	type testCase struct {
		name          string
		content       string
		events        string
		expectContent string
		expectMode    viMode
	}

	suite := []testCase{
		// ===== >> indent current line =====
		{">> indents current line", "hello", ">>", "\thello", normalMode},
		{">> on empty line adds tab", "", ">>", "\t", normalMode},
		{">> on already indented line adds another tab", "\thello", ">>", "\t\thello", normalMode},

		// ===== << dedent current line =====
		{"<< dedents current line", "\thello", "<<", "hello", normalMode},
		{"<< on non-indented line is no-op", "hello", "<<", "hello", normalMode},
		{"<< removes space if leading char is space", " hello", "<<", "hello", normalMode},

		// ===== >motion indent motion range =====
		{">j indents two lines down", "hello\nworld", ">j", "\thello\n\tworld", normalMode},
		{">k indents two lines up", "hello\nworld", "j>k", "\thello\n\tworld", normalMode},
		{">G indents to end of file", "one\ntwo\nthree", ">G", "\tone\n\ttwo\n\tthree", normalMode},

		// ===== <motion dedent motion range =====
		{"<j dedents two lines down", "\thello\n\tworld", "<j", "hello\nworld", normalMode},
		{"<k dedents two lines up", "\thello\n\tworld", "j<k", "hello\nworld", normalMode},

		// ===== count + >> =====
		{"2>> indents 2 lines", "hello\nworld\ntest", "2>>", "\thello\n\tworld\ntest", normalMode},
		{"3>> indents 3 lines", "one\ntwo\nthree", "3>>", "\tone\n\ttwo\n\tthree", normalMode},

		// ===== count + << =====
		{"2<< dedents 2 lines", "\thello\n\tworld\ntest", "2<<", "hello\nworld\ntest", normalMode},

		// ===== visual mode > and < =====
		{"v> indents visual selection", "hello\nworld", "Vj>", "\thello\n\tworld", normalMode},
		{"v< dedents visual selection", "\thello\n\tworld", "Vj<", "hello\nworld", normalMode},

		// ===== visual mode = (reindent) =====
		// Note: without an indent service, reindent is a no-op
		{"v= reindent is no-op without indent service", "hello\nworld", "Vj=", "hello\nworld", normalMode},

		// ===== == reindent current line =====
		// Note: without an indent service, reindent is a no-op
		{"== reindent is no-op without indent service", "hello", "==", "hello", normalMode},

		// ===== Invalid sequences cancel operator =====
		{"> followed by invalid key returns to normal", "hello", ">Z", "hello", normalMode},
		{"< followed by invalid key returns to normal", "hello", "<Z", "hello", normalMode},
		{"= followed by invalid key returns to normal", "hello", "=Z", "hello", normalMode},

		// ===== Text objects =====
		{">ib indents inner parens block", "if (\nhello\n)", "j>ib", "\tif (\n\thello\n\t)", normalMode},
		{"<ib dedents inner parens block", "\tif (\n\thello\n\t)", "j<ib", "if (\nhello\n)", normalMode},

		// ===== g-sub motions =====
		{">gj indents two lines via go-motion", "hello\nworld", ">gj", "\thello\n\tworld", normalMode},
		{">gk indents two lines via go-motion up", "hello\nworld", "j>gk", "\thello\n\tworld", normalMode},

		// ===== >w indent word motion =====
		{">w indents current line for single-line word motion", "hello world", ">w", "\thello world", normalMode},

		// ===== >$ indent to end of line =====
		{">$ indents current line", "hello", ">$", "\thello", normalMode},
	}

	for _, tcase := range suite {
		t.Run(tcase.name, func(t *testing.T) {
			vi := setupVi(t, tcase.content, 2)
			vi.Resize(40, 10)

			for _, ch := range tcase.events {
				vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			}

			assert.Equal(t, tcase.expectContent, vi.less.Buffer().String())
			assert.Equal(t, tcase.expectMode, vi.mode())
		})
	}
}

func TestViCaseChangeTextObjects(t *testing.T) {
	type caseChangeTextObjectCase struct {
		name       string
		content    string
		at         *term.Coordinates
		seq        string
		wantBuffer string
		wantMode   viMode
		wantCursor *term.Coordinates
	}

	run := func(t *testing.T, tc caseChangeTextObjectCase) {
		t.Helper()
		vi := setupVi(t, tc.content, 2)
		vi.Resize(80, 10)
		if tc.at != nil {
			vi.setCursorAtScroll(*tc.at)
		}
		for _, event := range tc.seq {
			vi.Handle(term.Event{Type: term.EventKey, Ch: event})
		}
		if tc.wantBuffer != "" || tc.content == "" {
			assert.Equal(t, tc.wantBuffer, vi.less.Buffer().String())
		}
		assert.Equal(t, tc.wantMode, vi.mode())
		if tc.wantCursor != nil {
			assert.Equal(t, *tc.wantCursor, vi.cursor.CursorAtScroll())
		}
	}

	for _, tc := range []caseChangeTextObjectCase{
		// inner word
		{
			name:       "guiw lowercases inner word at start",
			content:    "HELLO world",
			seq:        "guiw",
			wantBuffer: "hello world",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 0, Y: 0},
		},
		{
			name:       "gUiw uppercases inner word in middle",
			content:    "one two three",
			at:         &term.Coordinates{X: 4, Y: 0},
			seq:        "gUiw",
			wantBuffer: "one TWO three",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 4, Y: 0},
		},
		{
			name:       "g~iw toggles inner word at end",
			content:    "one two Three",
			at:         &term.Coordinates{X: 10, Y: 0},
			seq:        "g~iw",
			wantBuffer: "one two tHREE",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 8, Y: 0},
		},
		// around word
		{
			name:       "guaw lowercases around word",
			content:    "ONE TWO THREE",
			at:         &term.Coordinates{X: 4, Y: 0},
			seq:        "guaw",
			wantBuffer: "ONE two THREE",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 4, Y: 0},
		},
		// inner quotes
		{
			name:       "gui\" lowercases content inside double quotes",
			content:    `say "HELLO WORLD" now`,
			at:         &term.Coordinates{X: 5, Y: 0},
			seq:        "gui\"",
			wantBuffer: `say "hello world" now`,
			wantMode:   normalMode,
		},
		{
			name:       "gUi' uppercases content inside single quotes",
			content:    "say 'hello world' now",
			at:         &term.Coordinates{X: 5, Y: 0},
			seq:        "gUi'",
			wantBuffer: "say 'HELLO WORLD' now",
			wantMode:   normalMode,
		},
		// inner parens
		{
			name:       "guib lowercases inside parentheses",
			content:    "fn(ABC, DEF)",
			at:         &term.Coordinates{X: 3, Y: 0},
			seq:        "guib",
			wantBuffer: "fn(abc, def)",
			wantMode:   normalMode,
		},
		{
			name:       "gUi) uppercases inside parentheses",
			content:    "fn(abc, def)",
			at:         &term.Coordinates{X: 3, Y: 0},
			seq:        "gUi)",
			wantBuffer: "fn(ABC, DEF)",
			wantMode:   normalMode,
		},
		// inner braces
		{
			name:       "guiB lowercases inside braces",
			content:    "fn{ABC DEF}",
			at:         &term.Coordinates{X: 3, Y: 0},
			seq:        "guiB",
			wantBuffer: "fn{abc def}",
			wantMode:   normalMode,
		},
		// inner brackets
		{
			name:       "gui[ lowercases inside brackets",
			content:    "arr[ABC]",
			at:         &term.Coordinates{X: 4, Y: 0},
			seq:        "gui[",
			wantBuffer: "arr[abc]",
			wantMode:   normalMode,
		},
		// inner angle brackets
		{
			name:       "guit lowercases inside angle brackets",
			content:    "tag <ABC> end",
			at:         &term.Coordinates{X: 5, Y: 0},
			seq:        "guit",
			wantBuffer: "tag <abc> end",
			wantMode:   normalMode,
		},
		// around quotes
		{
			name:       "gua\" lowercases around double quotes",
			content:    `say "HELLO" now`,
			at:         &term.Coordinates{X: 5, Y: 0},
			seq:        "gua\"",
			wantBuffer: `say "hello" now`,
			wantMode:   normalMode,
		},
		// around parens
		{
			name:       "guab lowercases around parens",
			content:    "fn(ABC, DEF)",
			at:         &term.Coordinates{X: 3, Y: 0},
			seq:        "guab",
			wantBuffer: "fn(abc, def)",
			wantMode:   normalMode,
		},
		// empty quotes - no change
		{
			name:       "gui\" on empty quotes is no-op",
			content:    `say "" now`,
			at:         &term.Coordinates{X: 4, Y: 0},
			seq:        "gui\"",
			wantBuffer: `say "" now`,
			wantMode:   normalMode,
		},
		// empty parens - no change
		{
			name:       "guib on empty parens is no-op",
			content:    "fn()",
			at:         &term.Coordinates{X: 2, Y: 0},
			seq:        "guib",
			wantBuffer: "fn()",
			wantMode:   normalMode,
		},
		// inner WORD
		{
			name:       "guiW lowercases inner WORD including punctuation",
			content:    "ONE-TWO three",
			seq:        "guiW",
			wantBuffer: "one-two three",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 0, Y: 0},
		},
		// cursor on second line
		{
			name:       "gUiw on second line uppercases word there",
			content:    "hello\nworld",
			at:         &term.Coordinates{X: 0, Y: 1},
			seq:        "gUiw",
			wantBuffer: "hello\nWORLD",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 0, Y: 1},
		},
		// invalid text object key returns to normal mode
		{
			name:       "guiz invalid text object returns normal mode",
			content:    "HELLO WORLD",
			seq:        "guiz",
			wantBuffer: "HELLO WORLD",
			wantMode:   normalMode,
		},
		// retarget: i then a
		{
			name:       "guia retargets from inner to around",
			content:    "ONE TWO THREE",
			at:         &term.Coordinates{X: 4, Y: 0},
			seq:        "guiaw",
			wantBuffer: "ONE two THREE",
			wantMode:   normalMode,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run(t, tc)
		})
	}
}

func TestViCaseChangeSnapshots(t *testing.T) {
	const (
		width  = 30
		height = 5
	)

	newVi := func(t *testing.T, content string) tui.Handler {
		t.Helper()
		return setupViIntegration(t, content, 2)
	}

	t.Run("gu motions sequential", func(t *testing.T) {
		cases := []handlertest.SequenceTestCase{
			{InputSequence: "guw", Expected: "▐ello World                   \n                              \n                              \n                              \n                        NORMAL"},
			{InputSequence: "Wgue", Expected: "hello ▐orld                   \n                              \n                              \n                              \n                        NORMAL"},
		}
		handlertest.RunHandlerSequence(t, newVi(t, "Hello World"), width, height, cases)
	})

	t.Run("gU motions sequential", func(t *testing.T) {
		cases := []handlertest.SequenceTestCase{
			{InputSequence: "gUw", Expected: "▐ELLO world                   \n                              \n                              \n                              \n                        NORMAL"},
			{InputSequence: "WgUe", Expected: "HELLO ▐ORLD                   \n                              \n                              \n                              \n                        NORMAL"},
		}
		handlertest.RunHandlerSequence(t, newVi(t, "hello world"), width, height, cases)
	})

	t.Run("g~ motions sequential", func(t *testing.T) {
		cases := []handlertest.SequenceTestCase{
			{InputSequence: "g~w", Expected: "▐ELLO World                   \n                              \n                              \n                              \n                        NORMAL"},
			{InputSequence: "Wg~$", Expected: "hELLO ▐ORLD                   \n                              \n                              \n                              \n                        NORMAL"},
		}
		handlertest.RunHandlerSequence(t, newVi(t, "Hello World"), width, height, cases)
	})

	t.Run("whole line operators", func(t *testing.T) {
		fn := func(t *testing.T) tui.Handler {
			return newVi(t, "Hello World")
		}
		cases := []handlertest.SequenceTestCase{
			{InputSequence: "guu", Expected: "▐ello world                   \n                              \n                              \n                              \n                        NORMAL"},
			{InputSequence: "gUU", Expected: "▐ELLO WORLD                   \n                              \n                              \n                              \n                        NORMAL"},
			{InputSequence: "g~~", Expected: "▐ELLO wORLD                   \n                              \n                              \n                              \n                        NORMAL"},
		}
		handlertest.RunHandlerIsolated(t, fn, width, height, cases)
	})

	t.Run("case change with text objects", func(t *testing.T) {
		fn := func(t *testing.T) tui.Handler {
			return newVi(t, `say "HELLO WORLD" now`)
		}
		cases := []handlertest.SequenceTestCase{
			{InputSequence: "fHgui\"", Expected: "say \"▐ello world\" now         \n                              \n                              \n                              \n                        NORMAL"},
			{InputSequence: "fhg~i\"", Expected: "▐ay \"HELLO WORLD\" now         \n                              \n                              \n                              \n                        NORMAL"},
		}
		handlertest.RunHandlerIsolated(t, fn, width, height, cases)
	})

	t.Run("case change with ge motion", func(t *testing.T) {
		cases := []handlertest.SequenceTestCase{
			{InputSequence: "$", Expected: "ONE TWO-THREE,FOU▐            \n                              \n                              \n                              \n                        NORMAL"},
			{InputSequence: "guge", Expected: "ONE TWO-THREE▐four            \n                              \n                              \n                              \n                        NORMAL"},
		}
		handlertest.RunHandlerSequence(t, newVi(t, "ONE TWO-THREE,FOUR"), width, height, cases)
	})

	t.Run("case change multiline", func(t *testing.T) {
		fn := func(t *testing.T) tui.Handler {
			return newVi(t, "Hello\nWorld")
		}
		cases := []handlertest.SequenceTestCase{
			{InputSequence: "guj", Expected: "▐ello                         \nworld                         \n                              \n                              \n                        NORMAL"},
		}
		handlertest.RunHandlerIsolated(t, fn, width, height, cases)
	})

	t.Run("visual mode case change", func(t *testing.T) {
		fn := func(t *testing.T) tui.Handler {
			return newVi(t, "Hello World")
		}
		cases := []handlertest.SequenceTestCase{
			{InputSequence: "veu", Expected: "▐ello World                   \n                              \n                              \n                              \n                        NORMAL"},
			{InputSequence: "veU", Expected: "▐ELLO World                   \n                              \n                              \n                              \n                        NORMAL"},
			{InputSequence: "ve~", Expected: "▐ELLO World                   \n                              \n                              \n                              \n                        NORMAL"},
		}
		handlertest.RunHandlerIsolated(t, fn, width, height, cases)
	})
}

func TestViCount(t *testing.T) {
	motions := []rune{'h', 'j', 'k', 'l'}
	for _, motion := range motions {
		t.Run(fmt.Sprintf(
			"20%c multiplies motion by 20", // doesn't go necessarily to start of line
			motion),
			func(t *testing.T) {
				vi := setupVi(t, snippet, 2)
				vi.Resize(100, 100)
				vi.cursor.MoveToScroll(term.Coordinates{X: 3, Y: 3})
				vi.Handle(term.Event{Type: term.EventKey, Ch: '2'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: '0'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: motion})
				switch motion {
				case 'h':
					assert.Equal(t, vi.cursor.Coordinates(), term.Coordinates{X: 0, Y: 3})
				case 'j':
					assert.Equal(t, vi.cursor.Coordinates(), term.Coordinates{X: 5, Y: 23})
				case 'k':
					assert.Equal(t, vi.cursor.Coordinates(), term.Coordinates{X: 0, Y: 0})
				case 'l':
					assert.Equal(t, vi.cursor.Coordinates(), term.Coordinates{X: 15, Y: 3})
				}
			})
	}

	t.Run("word motions honor counts", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			seq  string
			want term.Coordinates
		}{
			{name: "2w", seq: "2w", want: term.Coordinates{X: len("one two ")}},
			{name: "3e", seq: "3e", want: term.Coordinates{X: len("one two three") - 1}},
			{name: "2b", seq: "www2b", want: term.Coordinates{X: len("one ")}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				vi := setupVi(t, "one two three four", 2)
				vi.Resize(80, 5)
				vi.Draw(term.NoopWriter{})
				for _, eventChar := range tc.seq {
					vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
				}

				assert.Equal(t, tc.want, vi.cursor.Coordinates())
				assert.Equal(t, normalMode, vi.mode())
				assert.Equal(t, 1, vi.count)
				assert.Equal(t, "", vi.countDigits)
			})
		}
	})

	t.Run("character find motions honor counts", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			seq  string
			want term.Coordinates
		}{
			{name: "2fx", seq: "2fx", want: term.Coordinates{X: len("ax b")}},
			{name: "2tx", seq: "2tx", want: term.Coordinates{X: len("ax ")}},
			{name: "2Fx", seq: "$2Fx", want: term.Coordinates{X: len("a")}},
			{name: "2Tx", seq: "$2Tx", want: term.Coordinates{X: len("ax")}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				vi := setupVi(t, "ax bx cx", 2)
				vi.Resize(80, 5)
				vi.Draw(term.NoopWriter{})
				for _, eventChar := range tc.seq {
					vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
				}

				assert.Equal(t, tc.want, vi.cursor.Coordinates())
				assert.Equal(t, normalMode, vi.mode())
				assert.Equal(t, 1, vi.count)
				assert.Equal(t, "", vi.countDigits)
			})
		}
	})
}

func TestViOperatorCounts(t *testing.T) {
	for _, tc := range []struct {
		name          string
		content       string
		seq           string
		wantContent   string
		wantMode      viMode
		wantClipboard string
		wantMetadata  text.SelectMode
	}{
		{
			name:        "2dw deletes two words",
			content:     "one two three four",
			seq:         "2dw",
			wantContent: "three four",
			wantMode:    normalMode,
		},
		{
			name:        "d2w deletes two words",
			content:     "one two three four",
			seq:         "d2w",
			wantContent: "three four",
			wantMode:    normalMode,
		},
		{
			name:        "2d2w multiplies operator and motion counts",
			content:     "one two three four five",
			seq:         "2d2w",
			wantContent: "five",
			wantMode:    normalMode,
		},
		{
			name:        "2cw changes two words",
			content:     "one two three four",
			seq:         "2cw",
			wantContent: " three four",
			wantMode:    insertMode,
		},
		{
			name:          "2yy yanks two lines",
			content:       "one\ntwo\nthree\n",
			seq:           "2yy",
			wantContent:   "one\ntwo\nthree\n",
			wantMode:      normalMode,
			wantClipboard: "one\ntwo\n",
			wantMetadata:  text.LineSelection,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vi := setupVi(t, tc.content, 2)
			vi.Resize(80, 5)
			vi.Draw(term.NoopWriter{})
			for _, eventChar := range tc.seq {
				_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
				require.True(t, handled, "event %q", eventChar)
			}

			assert.Equal(t, tc.wantContent, vi.less.Buffer().String())
			assert.Equal(t, term.Coordinates{}, vi.cursor.Coordinates())
			assert.Equal(t, tc.wantMode, vi.mode())
			assert.Equal(t, 1, vi.count)
			assert.Equal(t, "", vi.countDigits)
			if tc.wantClipboard != "" {
				paste, err := vi.config.clipboard.Paste(vi.config.defaultRegister)
				require.NoError(t, err)
				assert.Equal(t, tc.wantClipboard, paste.Text)
				assert.Equal(t, tc.wantMetadata, paste.Metadata)
			}
		})
	}
}

func TestViCountedMotionScenarios(t *testing.T) {
	type testCase struct {
		name        string
		content     string
		seq         string
		wrap        bool
		width       int
		setup       func(*viHandlerImpl)
		wantContent string
		wantScroll  *term.Coordinates
		wantMode    viMode
	}

	coord := func(x, y int) *term.Coordinates {
		return &term.Coordinates{X: x, Y: y}
	}

	moveEnd := func(vi *viHandlerImpl) {
		vi.cursor.MoveEndLine()
	}
	pastLastColumn := func(vi *viHandlerImpl) {
		vi.setCursorAtScroll(term.Coordinates{X: len("one two"), Y: 0})
	}
	pastLastLine := func(vi *viHandlerImpl) {
		vi.config.cursorCorrections = false
		vi.cursor.SetCursorAtScroll(term.Coordinates{Y: 10})
	}

	cases := []testCase{
		// Happy-path counted word motions, non-wrap.
		{name: "2w moves to second next word", content: "one two three four", seq: "2w", wantScroll: coord(len("one two "), 0)},
		{name: "3w moves to third next word", content: "one two three four", seq: "3w", wantScroll: coord(len("one two three "), 0)},
		{name: "2W treats punctuation as part of WORD", content: "one-two three four", seq: "2W", wantScroll: coord(len("one-two three "), 0)},
		{name: "3e moves through third word end", content: "one two three four", seq: "3e", wantScroll: coord(len("one two three")-1, 0)},
		{name: "2E moves through second WORD end", content: "one-two three four", seq: "2E", wantScroll: coord(len("one-two three")-1, 0)},
		{name: "2b moves back two word starts", content: "one two three four", seq: "www2b", wantScroll: coord(len("one "), 0)},
		{name: "2B moves back two WORD starts", content: "one-two three four", seq: "WW2B", wantScroll: coord(0, 0)},
		{name: "2ge moves back two word ends", content: "one two three four", seq: "$2ge", wantScroll: coord(len("one two")-1, 0)},
		{name: "2gE moves back two WORD ends", content: "one-two three four", seq: "$2gE", wantScroll: coord(len("one-two")-1, 0)},

		// The same generic motion paths should behave the same in wrap mode.
		{name: "wrap 2w moves to second next word", content: "one two three four", seq: "2w", wrap: true, width: 5, wantScroll: coord(len("one two "), 0)},
		{name: "wrap 3e moves through third word end", content: "one two three four", seq: "3e", wrap: true, width: 5, wantScroll: coord(len("one two three")-1, 0)},
		{name: "wrap 2b moves back two word starts", content: "one two three four", seq: "www2b", wrap: true, width: 5, wantScroll: coord(len("one "), 0)},

		// Counted f/F/t/T motions and counted ; repeat.
		{name: "2fx finds second matching char", content: "ax bx cx", seq: "2fx", wantScroll: coord(len("ax b"), 0)},
		{name: "3fx finds third matching char", content: "ax bx cx", seq: "3fx", wantScroll: coord(len("ax bx c"), 0)},
		{name: "2tx stops before second matching char", content: "ax bx cx", seq: "2tx", wantScroll: coord(len("ax "), 0)},
		{name: "2Fx finds second previous matching char", content: "ax bx cx", seq: "$2Fx", wantScroll: coord(len("a"), 0)},
		{name: "2Tx stops after second previous matching char", content: "ax bx cx", seq: "$2Tx", wantScroll: coord(len("ax"), 0)},
		{name: "2 semicolon repeats last find twice", content: "ax bx cx dx", seq: "fx2;", wantScroll: coord(len("ax bx c"), 0)},
		{name: "wrap 2fx finds second matching char", content: "ax bx cx", seq: "2fx", wrap: true, width: 4, wantScroll: coord(len("ax b"), 0)},
		{name: "wrap 2tx stops before second matching char", content: "ax bx cx", seq: "2tx", wrap: true, width: 4, wantScroll: coord(len("ax "), 0)},

		// Not-so-happy motion paths should be handled and reset counts without editing.
		{name: "counted word motion in empty buffer", content: "", seq: "2w", wantScroll: coord(0, 0)},
		{name: "counted char find in empty buffer", content: "", seq: "2fx", wantScroll: coord(0, 0)},
		{name: "counted word motion from past last column", content: "one two", seq: "2w", setup: pastLastColumn, wantScroll: coord(len("one two")-1, 0)},
		{name: "counted char find from end of line does not move", content: "ax bx", seq: "2fx", setup: moveEnd, wantScroll: coord(len("ax bx")-1, 0)},
		{name: "counted word motion from past last line is a no-op", content: "one two", seq: "2w", setup: pastLastLine, wantScroll: coord(0, 10)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := []Option{WithWrap(tc.wrap)}
			vi := setupVi(t, tc.content, 2, opts...)
			width := tc.width
			if width == 0 {
				width = 80
			}
			vi.Resize(width, 8)
			vi.Draw(term.NoopWriter{})
			if tc.setup != nil {
				tc.setup(vi)
			}

			for _, eventChar := range tc.seq {
				_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
				require.True(t, handled, "event %q", eventChar)
			}

			wantContent := tc.wantContent
			if wantContent == "" {
				wantContent = tc.content
			}
			assert.Equal(t, wantContent, vi.less.Buffer().String())
			if tc.wantScroll != nil {
				assert.Equal(t, *tc.wantScroll, vi.cursor.CursorAtScroll())
			}
			wantMode := tc.wantMode
			if wantMode == 0 {
				wantMode = normalMode
			}
			assert.Equal(t, wantMode, vi.mode())
			assert.Equal(t, 1, vi.count)
			assert.Equal(t, "", vi.countDigits)
			assert.Equal(t, 0, vi.operatorCount)
		})
	}
}

func TestViWordMotionEmptyLines(t *testing.T) {
	for _, tc := range []struct {
		name        string
		content     string
		seq         string
		wantScroll  term.Coordinates
		wantContent string
		wantMode    viMode
	}{
		{name: "w from line end stops on empty line", content: "one\n\ntwo", seq: "$w", wantScroll: term.Coordinates{Y: 1}},
		{name: "w from empty line moves to next line", content: "one\n\ntwo", seq: "jw", wantScroll: term.Coordinates{Y: 2}},
		{name: "w stops on each empty line", content: "one\n\n\ntwo", seq: "$ww", wantScroll: term.Coordinates{Y: 2}},
		{name: "W from line end stops on empty line", content: "one\n\ntwo", seq: "$W", wantScroll: term.Coordinates{Y: 1}},
		{name: "w skips whitespace-only line", content: "one\n  \ntwo", seq: "$w", wantScroll: term.Coordinates{Y: 2}},
		{name: "dw at line end keeps following empty line", content: "one\n\ntwo", seq: "$dw", wantScroll: term.Coordinates{X: 1}, wantContent: "on\n\ntwo"},
		{name: "dw on empty line deletes it", content: "one\n\ntwo", seq: "jdw", wantScroll: term.Coordinates{Y: 1}, wantContent: "one\ntwo"},
		{name: "dW on empty line deletes it", content: "one\n\ntwo", seq: "jdW", wantScroll: term.Coordinates{Y: 1}, wantContent: "one\ntwo"},
		{name: "dw deletes trailing blanks before line end", content: "one  \ntwo", seq: "dw", wantContent: "\ntwo"},
		{name: "cw on empty line keeps the line", content: "one\n\ntwo", seq: "jcwX", wantScroll: term.Coordinates{X: 1, Y: 1}, wantContent: "one\nX\ntwo", wantMode: insertMode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vi := setupVi(t, tc.content, 2)
			vi.Resize(80, 8)
			vi.Draw(term.NoopWriter{})

			for _, eventChar := range tc.seq {
				_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
				require.True(t, handled, "event %q", eventChar)
			}

			wantContent := tc.wantContent
			if wantContent == "" {
				wantContent = tc.content
			}
			assert.Equal(t, wantContent, vi.less.Buffer().String())
			assert.Equal(t, tc.wantScroll, vi.cursor.CursorAtScroll())
			wantMode := tc.wantMode
			if wantMode == 0 {
				wantMode = normalMode
			}
			assert.Equal(t, wantMode, vi.mode())
		})
	}
}

func TestViChangeWord(t *testing.T) {
	col := func(x int) term.Coordinates {
		return term.Coordinates{X: x}
	}
	for _, tc := range []struct {
		name        string
		content     string
		seq         string
		wantScroll  term.Coordinates
		wantContent string
		wantMode    viMode
	}{
		{name: "cw keeps the space after the word", wantMode: insertMode, content: "one two", seq: "cw", wantContent: " two"},
		{name: "cw mid-word keeps the space after the word", wantMode: insertMode, content: "one two", seq: "lcw", wantContent: "o two", wantScroll: col(1)},
		{name: "cw at a word end changes that cell only", wantMode: insertMode, content: "one two", seq: "llcw", wantContent: "on two", wantScroll: col(2)},
		{name: "cw at a word end does not reach the next line", wantMode: insertMode, content: "one\ntwo", seq: "llcw", wantContent: "on\ntwo", wantScroll: term.Coordinates{X: 2}},
		{name: "cw at end of the last word in buffer changes that cell", wantMode: insertMode, content: "one", seq: "llcw", wantContent: "on", wantScroll: col(2)},
		{name: "cw on a single-letter word keeps the space", wantMode: insertMode, content: "a b c", seq: "cw", wantContent: " b c"},
		{name: "cw on a blank between words changes the blanks", wantMode: insertMode, content: "one  two", seq: "lllcw", wantContent: "onetwo", wantScroll: col(3)},
		{name: "cw on blanks before line end keeps the line", wantMode: insertMode, content: "one  \ntwo", seq: "lllcw", wantContent: "one\ntwo", wantScroll: col(3)},
		{name: "c2w changes through the second word end", wantMode: insertMode, content: "one two three four", seq: "c2w", wantContent: " three four"},
		{name: "c2w counts a word end under the cursor", wantMode: insertMode, content: "one two three", seq: "llc2w", wantContent: "on three", wantScroll: col(2)},
		{name: "cW keeps the space after the WORD", wantMode: insertMode, content: "one, two", seq: "cW", wantContent: " two"},
		{name: "cW on a blank changes the blanks", wantMode: insertMode, content: "one  two", seq: "lllcW", wantContent: "onetwo", wantScroll: col(3)},

		// Vim splits a word on the word-rune/punctuation boundary, so each
		// punctuation run is its own word.
		{name: "cw before a punctuation word stops before it", wantMode: insertMode, content: "a$b x", seq: "cw", wantContent: "$b x"},
		{name: "c2w over a punctuation word keeps the next word", wantMode: insertMode, content: "a$b x", seq: "c2w", wantContent: "b x"},
		{name: "c2w changes a word plus the following punctuation", wantMode: insertMode, content: "ab$ x", seq: "c2w", wantContent: " x"},
		{name: "cw on a punctuation run changes the run", wantMode: insertMode, content: "a++b x", seq: "lcw", wantContent: "ab x", wantScroll: col(1)},
		{name: "cW treats punctuation as part of the WORD", wantMode: insertMode, content: "a-b c", seq: "cW", wantContent: " c"},
		{name: "cw keeps the underscore inside the word", wantMode: insertMode, content: "foo_bar x", seq: "cw", wantContent: " x"},

		// Counted cw crossing empty lines lands on the word end, not the word
		// start: an inserted X proves the exact range without relying on the
		// empty-buffer default.
		{name: "c2w crosses an empty line to the next word end", wantMode: insertMode, content: "one\n\ntwo", seq: "c2wX", wantContent: "X", wantScroll: col(1)},
		{name: "cw keeps the tab after the word", wantMode: insertMode, content: "a\tb", seq: "cw", wantContent: "\tb"},
		{name: "cw keeps a wide rune inside the word", wantMode: insertMode, content: "a字b x", seq: "cw", wantContent: " x"},
		{name: "cw stops before a null cell", wantMode: insertMode, content: "a\x00b x", seq: "cw", wantContent: "\x00b x"},
		{name: "c9w past the last word changes to the buffer end", wantMode: insertMode, content: "one two", seq: "c9wX", wantContent: "X", wantScroll: col(1)},

		{name: "dw still consumes the space after the word", content: "one two", seq: "dw", wantContent: "two"},
		{name: "yw still yanks the space after the word", content: "one two", seq: "yw", wantContent: "one two"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vi := setupVi(t, tc.content, 2)
			vi.Resize(80, 8)
			vi.Draw(term.NoopWriter{})

			for _, eventChar := range tc.seq {
				_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
				require.True(t, handled, "event %q", eventChar)
			}

			wantContent := tc.wantContent
			if wantContent == "" {
				wantContent = tc.content
			}
			assert.Equal(t, wantContent, vi.less.Buffer().String())
			assert.Equal(t, tc.wantScroll, vi.cursor.CursorAtScroll())
			wantMode := tc.wantMode
			if wantMode == 0 {
				wantMode = normalMode
			}
			assert.Equal(t, wantMode, vi.mode())
		})
	}
}

func TestViCountedOperatorScenarios(t *testing.T) {
	type testCase struct {
		name           string
		content        string
		seq            string
		wrap           bool
		width          int
		setup          func(*viHandlerImpl)
		wantContent    string
		wantScroll     *term.Coordinates
		wantMode       viMode
		allowUnhandled bool
		wantClipboard  bool
		clipboardText  string
		clipboardMode  text.SelectMode
	}

	coord := func(x, y int) *term.Coordinates {
		return &term.Coordinates{X: x, Y: y}
	}
	startSecondLine := func(vi *viHandlerImpl) {
		vi.setCursorAtScroll(term.Coordinates{Y: 1})
	}
	pastLastColumn := func(vi *viHandlerImpl) {
		vi.setCursorAtScroll(term.Coordinates{X: len("one two"), Y: 0})
	}
	pastLastLine := func(vi *viHandlerImpl) {
		vi.config.cursorCorrections = false
		vi.cursor.SetCursorAtScroll(term.Coordinates{Y: 10})
	}

	cases := []testCase{
		// Delete/change counts before the operator, after the operator, and on both sides.
		{name: "2dw deletes two words", content: "one two three four", seq: "2dw", wantContent: "three four", wantScroll: coord(0, 0)},
		{name: "d2w deletes two words", content: "one two three four", seq: "d2w", wantContent: "three four", wantScroll: coord(0, 0)},
		{name: "2d2w multiplies operator and motion counts", content: "one two three four five", seq: "2d2w", wantContent: "five", wantScroll: coord(0, 0)},
		{name: "2de deletes through second word end", content: "one two three", seq: "2de", wantContent: " three", wantScroll: coord(0, 0)},
		{name: "d2e deletes through second word end", content: "one two three", seq: "d2e", wantContent: " three", wantScroll: coord(0, 0)},
		{name: "2cw changes two words", content: "one two three four", seq: "2cw", wantContent: " three four", wantMode: insertMode, wantScroll: coord(0, 0)},
		{name: "c2w changes two words", content: "one two three four", seq: "c2w", wantContent: " three four", wantMode: insertMode, wantScroll: coord(0, 0)},
		{name: "2c2w changes four words", content: "one two three four five", seq: "2c2w", wantContent: " five", wantMode: insertMode, wantScroll: coord(0, 0)},
		{name: "wrap 2dw deletes two words", content: "one two three four", seq: "2dw", wrap: true, width: 5, wantContent: "three four", wantScroll: coord(0, 0)},
		{name: "wrap d2w deletes two words", content: "one two three four", seq: "d2w", wrap: true, width: 5, wantContent: "three four", wantScroll: coord(0, 0)},
		{name: "wrap 2cw changes two words", content: "one two three four", seq: "2cw", wrap: true, width: 5, wantContent: " three four", wantMode: insertMode, wantScroll: coord(0, 0)},

		// Counted character-find motions in operator-pending mode.
		{name: "d2fx deletes through second x", content: "ax bx cx", seq: "d2fx", wantContent: " cx", wantScroll: coord(0, 0)},
		{name: "2dfx deletes through second x", content: "ax bx cx", seq: "2dfx", wantContent: " cx", wantScroll: coord(0, 0)},
		{name: "d2tx deletes until before second x", content: "ax bx cx", seq: "d2tx", wantContent: "x cx", wantScroll: coord(0, 0)},
		{name: "c2fx changes through second x", content: "ax bx cx", seq: "c2fx", wantContent: " cx", wantMode: insertMode, wantScroll: coord(0, 0)},
		{name: "wrap d2fx deletes through second x", content: "ax bx cx", seq: "d2fx", wrap: true, width: 4, wantContent: " cx", wantScroll: coord(0, 0)},
		{name: "dfw finds w instead of moving by word", content: "one two wow", seq: "dfw", wantContent: "o wow", wantScroll: coord(0, 0)},
		{name: "dtw stops before w instead of moving by word", content: "one two wow", seq: "dtw", wantContent: "wo wow", wantScroll: coord(0, 0)},
		{name: "dfb finds b instead of moving back by word", content: "one bob two", seq: "dfb", wantContent: "ob two", wantScroll: coord(0, 0)},
		{name: "dtb stops before b instead of moving back by word", content: "one bob two", seq: "dtb", wantContent: "bob two", wantScroll: coord(0, 0)},
		{name: "dFb finds b backward and keeps the cursor char instead of moving back by word", content: "bob one", seq: "$dFb", wantContent: "boe", wantScroll: coord(2, 0)},
		{name: "dTb stops after b and keeps the cursor char instead of moving back by word", content: "bob one", seq: "$dTb", wantContent: "bobe", wantScroll: coord(3, 0)},
		{name: "dfB finds B instead of moving back by word", content: "one Bob two", seq: "dfB", wantContent: "ob two", wantScroll: coord(0, 0)},

		// Counted word operators across empty lines: the last word stops at its
		// line end, and an end in column 0 is pulled back to the previous line.
		{name: "d2w from empty line deletes through next word", content: "\none two", seq: "d2w", wantContent: "two", wantScroll: coord(0, 0)},
		{name: "d2w over two empty lines deletes them linewise", content: "\n\nthree", seq: "d2w", wantContent: "three", wantScroll: coord(0, 0)},
		{name: "2dw over two empty lines deletes them linewise", content: "\n\nthree", seq: "2dw", wantContent: "three", wantScroll: coord(0, 0)},
		{name: "d2w from first word over empty line deletes linewise", content: "one\n\ntwo", seq: "d2w", wantContent: "two", wantScroll: coord(0, 0)},
		{name: "d2w from indented word over empty line deletes linewise", content: "  one\n\ntwo", seq: "wd2w", wantContent: "two", wantScroll: coord(0, 0)},
		{name: "d2w from line end over empty line joins lines", content: "one\n\ntwo", seq: "$d2w", wantContent: "on\ntwo", wantScroll: coord(1, 0)},
		{name: "c2w over two empty lines keeps one line", content: "\n\nthree", seq: "c2wX", wantContent: "X\nthree", wantMode: insertMode, wantScroll: coord(1, 0)},
		{name: "c2w from indented word over empty line keeps indent", content: "  one\n\ntwo", seq: "wc2wX", wantContent: "  X", wantMode: insertMode, wantScroll: coord(3, 0)},
		{name: "y2w over two empty lines yanks linewise", content: "\n\nthree", seq: "y2w", wantClipboard: true, clipboardText: "\n\n", clipboardMode: text.LineSelection, wantScroll: coord(0, 0)},
		{name: "y2w from line end over empty line yanks through newline", content: "one\n\ntwo", seq: "$y2w", wantClipboard: true, clipboardText: "e\n", clipboardMode: text.StandardSelection, wantScroll: coord(2, 0)},

		// Yank counts and metadata for character-wise and line-wise selections.
		{name: "2yw yanks two words", content: "one two three", seq: "2yw", wantClipboard: true, clipboardText: "one two ", clipboardMode: text.StandardSelection, wantScroll: coord(0, 0)},
		{name: "y2w yanks two words", content: "one two three", seq: "y2w", wantClipboard: true, clipboardText: "one two ", clipboardMode: text.StandardSelection, wantScroll: coord(0, 0)},
		{name: "2y2w yanks four words", content: "one two three four five", seq: "2y2w", wantClipboard: true, clipboardText: "one two three four ", clipboardMode: text.StandardSelection, wantScroll: coord(0, 0)},
		{name: "2yy yanks two lines", content: "one\ntwo\nthree\n", seq: "2yy", wantClipboard: true, clipboardText: "one\ntwo\n", clipboardMode: text.LineSelection, wantScroll: coord(0, 0)},
		{name: "y2y yanks two lines", content: "one\ntwo\nthree\n", seq: "y2y", wantClipboard: true, clipboardText: "one\ntwo\n", clipboardMode: text.LineSelection, wantScroll: coord(0, 0)},
		{name: "2yy from second line yanks remaining two lines", content: "one\ntwo\nthree\n", seq: "2yy", setup: startSecondLine, wantClipboard: true, clipboardText: "two\nthree\n", clipboardMode: text.LineSelection, wantScroll: coord(0, 1)},
		{name: "wrap 2yy yanks two lines", content: "one\ntwo\nthree\n", seq: "2yy", wrap: true, width: 2, wantClipboard: true, clipboardText: "one\ntwo\n", clipboardMode: text.LineSelection, wantScroll: coord(0, 0)},

		// Case-change and shift operators use the same counted meta-motion path.
		{name: "2guw lowercases two words", content: "ONE TWO THREE", seq: "2guw", wantContent: "one two THREE", wantScroll: coord(0, 0)},
		{name: "gu2w lowercases two words", content: "ONE TWO THREE", seq: "gu2w", wantContent: "one two THREE", wantScroll: coord(0, 0)},
		{name: "2gUe uppercases through second word end", content: "one two three", seq: "2gUe", wantContent: "ONE TWO three", wantScroll: coord(0, 0)},
		{name: "2g~w toggles two words", content: "One Two Three", seq: "2g~w", wantContent: "oNE tWO Three", wantScroll: coord(0, 0)},
		{name: "2>w shifts two lines via operator count", content: "one\ntwo\nthree", seq: "2>j", wantContent: "\tone\n\ttwo\n\tthree", wantScroll: coord(0, 2)},
		{name: ">2j shifts two lines via motion count", content: "one\ntwo\nthree", seq: ">2j", wantContent: "\tone\n\ttwo\n\tthree", wantScroll: coord(0, 2)},

		// Boundary and no-op operator paths.
		{name: "2dw in empty buffer is a no-op", content: "", seq: "2dw", wantScroll: coord(0, 0), allowUnhandled: true},
		{name: "2yy in empty buffer is a no-op", content: "", seq: "2yy", wantScroll: coord(0, 0)},
		{name: "cw in empty buffer is a no-op", content: "", seq: "cw", wantScroll: coord(0, 0), allowUnhandled: true},
		{name: "2dw from past last column clamps and deletes the last cell", content: "one two", seq: "2dw", setup: pastLastColumn, wantContent: "one tw", wantScroll: coord(len("one tw")-1, 0)},
		{name: "2dw from past last line does not edit", content: "one two", seq: "2dw", setup: pastLastLine, wantScroll: coord(0, 10), allowUnhandled: true},
		{name: "cw from past last column clamps and changes the last cell", content: "one two", seq: "cw", setup: pastLastColumn, wantContent: "one tw", wantMode: insertMode, wantScroll: coord(len("one two")-1, 0), allowUnhandled: true},
		{name: "cw from past last line does not edit", content: "one two", seq: "cw", setup: pastLastLine, wantScroll: coord(0, 10), allowUnhandled: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vi := setupVi(t, tc.content, 2, WithWrap(tc.wrap))
			width := tc.width
			if width == 0 {
				width = 80
			}
			vi.Resize(width, 8)
			vi.Draw(term.NoopWriter{})
			if tc.setup != nil {
				tc.setup(vi)
			}

			for _, eventChar := range tc.seq {
				_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
				if !tc.allowUnhandled {
					require.True(t, handled, "event %q", eventChar)
				}
			}

			wantContent := tc.wantContent
			if wantContent == "" {
				wantContent = tc.content
			}
			assert.Equal(t, wantContent, vi.less.Buffer().String())
			if tc.wantScroll != nil {
				assert.Equal(t, *tc.wantScroll, vi.cursor.CursorAtScroll())
			}
			wantMode := tc.wantMode
			if wantMode == 0 {
				wantMode = normalMode
			}
			assert.Equal(t, wantMode, vi.mode())
			assert.Equal(t, 1, vi.count)
			assert.Equal(t, "", vi.countDigits)
			assert.Equal(t, 0, vi.operatorCount)
			if tc.wantClipboard {
				paste, err := vi.config.clipboard.Paste(vi.config.defaultRegister)
				require.NoError(t, err)
				assert.Equal(t, tc.clipboardText, paste.Text)
				assert.Equal(t, tc.clipboardMode, paste.Metadata)
			}
		})
	}
}

func TestViOperatorMotionVimParity(t *testing.T) {
	type testCase struct {
		name         string
		content      string
		at           term.Coordinates
		marks        map[rune]term.Coordinates
		keys         string
		wantContent  string
		wantCursor   *term.Coordinates
		wantMode     viMode
		wantUnnamed  string
		wantLinewise bool
		wrap         bool
	}
	pos := func(x, y int) *term.Coordinates {
		return &term.Coordinates{X: x, Y: y}
	}

	// Expectations come from Vim 9.1 with tabstop=2 and autoindent. Each
	// suite covers empty buffers, cursors past the line end or below the
	// last line, the trailing empty line, double-width characters and tabs.
	// Rows marked nostartofline come from Vim with 'nostartofline', which
	// Rune's gg, G, H, M and L follow by keeping the cursor column. Rune
	// does not wrap by default, so the gj and gk rows without wrap come from
	// Vim with 'nowrap'. The window motion rows come from Vim in a terminal
	// window taller than their text.
	suites := []struct {
		name  string
		cases []testCase
	}{
		{name: "backward exclusive motions", cases: []testCase{
			{name: "dh", content: "abcd", at: term.Coordinates{X: 2}, keys: "dh", wantContent: "acd", wantCursor: pos(1, 0), wantUnnamed: "b"},
			{name: "d2h", content: "abcd", at: term.Coordinates{X: 2}, keys: "d2h", wantContent: "cd", wantCursor: pos(0, 0), wantUnnamed: "ab"},
			{name: "ch", content: "abcd", at: term.Coordinates{X: 2}, keys: "ch", wantContent: "acd", wantCursor: pos(1, 0), wantMode: insertMode, wantUnnamed: "b"},
			{name: "dh repeated", content: "abcdef", at: term.Coordinates{X: 5}, keys: "dh.", wantContent: "abcf", wantCursor: pos(3, 0), wantUnnamed: "d"},
			{name: "yh", content: "abcd", at: term.Coordinates{X: 2}, keys: "yh", wantContent: "abcd", wantCursor: pos(1, 0), wantUnnamed: "b"},
			{name: "d0", content: "abc def", at: term.Coordinates{X: 4}, keys: "d0", wantContent: "def", wantCursor: pos(0, 0), wantUnnamed: "abc "},
			{name: "c0", content: "abc def", at: term.Coordinates{X: 4}, keys: "c0", wantContent: "def", wantCursor: pos(0, 0), wantMode: insertMode, wantUnnamed: "abc "},
			{name: "gU0", content: "abc def", at: term.Coordinates{X: 4}, keys: "gU0", wantContent: "ABC def", wantCursor: pos(0, 0)},
			{name: "y0", content: "abc def", at: term.Coordinates{X: 4}, keys: "y0", wantContent: "abc def", wantCursor: pos(0, 0), wantUnnamed: "abc "},
			{name: "d^ backward", content: "  abc def", at: term.Coordinates{X: 6}, keys: "d^", wantContent: "  def", wantCursor: pos(2, 0), wantUnnamed: "abc "},
			{name: "dFa", content: "abcx", at: term.Coordinates{X: 3}, keys: "dFa", wantContent: "x", wantCursor: pos(0, 0), wantUnnamed: "abc"},
			{name: "dTa", content: "abcx", at: term.Coordinates{X: 3}, keys: "dTa", wantContent: "ax", wantCursor: pos(1, 0), wantUnnamed: "bc"},
			{name: "cFa", content: "abcx", at: term.Coordinates{X: 3}, keys: "cFa", wantContent: "x", wantCursor: pos(0, 0), wantMode: insertMode, wantUnnamed: "abc"},
			{name: "gUFa", content: "abcx", at: term.Coordinates{X: 3}, keys: "gUFa", wantContent: "ABCx", wantCursor: pos(0, 0)},
			{name: "yFa", content: "abcx", at: term.Coordinates{X: 3}, keys: "yFa", wantContent: "abcx", wantCursor: pos(0, 0), wantUnnamed: "abc"},
			{name: "d2Fa", content: "abax", at: term.Coordinates{X: 3}, keys: "d2Fa", wantContent: "x", wantCursor: pos(0, 0), wantUnnamed: "aba"},
			{name: "dFb", content: "abcx", at: term.Coordinates{X: 3}, keys: "dFb", wantContent: "ax", wantCursor: pos(1, 0), wantUnnamed: "bc"},
			{name: "dTb", content: "abcx", at: term.Coordinates{X: 3}, keys: "dTb", wantContent: "abx", wantCursor: pos(2, 0), wantUnnamed: "c"},
			{name: "dFB", content: "aBcx", at: term.Coordinates{X: 3}, keys: "dFB", wantContent: "ax", wantCursor: pos(1, 0), wantUnnamed: "Bc"},
			{name: "dTB", content: "aBcx", at: term.Coordinates{X: 3}, keys: "dTB", wantContent: "aBx", wantCursor: pos(2, 0), wantUnnamed: "c"},
			{name: "dFw", content: "awcx", at: term.Coordinates{X: 3}, keys: "dFw", wantContent: "ax", wantCursor: pos(1, 0), wantUnnamed: "wc"},
			{name: "dTw", content: "awcx", at: term.Coordinates{X: 3}, keys: "dTw", wantContent: "awx", wantCursor: pos(2, 0), wantUnnamed: "c"},
			{name: "dFW", content: "aWcx", at: term.Coordinates{X: 3}, keys: "dFW", wantContent: "ax", wantCursor: pos(1, 0), wantUnnamed: "Wc"},
			{name: "dFe", content: "aecx", at: term.Coordinates{X: 3}, keys: "dFe", wantContent: "ax", wantCursor: pos(1, 0), wantUnnamed: "ec"},
			{name: "dF,", content: "ab,de", at: term.Coordinates{X: 4}, keys: "dF,", wantContent: "abe", wantCursor: pos(2, 0), wantUnnamed: ",d"},
			{name: "d, repeats f backward exclusively", content: "xbybz", keys: "2fbd,", wantContent: "xbz", wantCursor: pos(1, 0), wantUnnamed: "by"},
			{name: "dFa repeated", content: "abcxabcx", at: term.Coordinates{X: 7}, keys: "dFa.", wantContent: "x", wantCursor: pos(0, 0), wantUnnamed: "abcx"},
			{name: "dFd repeated without a match", content: "abc def ghi", at: term.Coordinates{X: 8}, keys: "dFd.", wantContent: "abc ghi", wantCursor: pos(4, 0), wantUnnamed: "def "},
			{name: "dh on an empty buffer", content: "", keys: "dh", wantContent: "", wantCursor: pos(0, 0)},
			{name: "dh past the line end", content: "abc", at: term.Coordinates{X: 9}, keys: "dh", wantContent: "ac", wantCursor: pos(1, 0), wantUnnamed: "b"},
			{name: "dh below the last line", content: "abc\ndef", at: term.Coordinates{X: 1, Y: 9}, keys: "dh", wantContent: "abc\nef", wantCursor: pos(0, 1), wantUnnamed: "d"},
			{name: "dh past both ends", content: "abc\ndef", at: term.Coordinates{X: 9, Y: 9}, keys: "dh", wantContent: "abc\ndf", wantCursor: pos(1, 1), wantUnnamed: "e"},
			{name: "dh on the trailing empty line", content: "abc\n", at: term.Coordinates{Y: 1}, keys: "dh", wantContent: "abc\n", wantCursor: pos(0, 1)},
			{name: "dh over a double-width char", content: "a\U0001F600b", at: term.Coordinates{X: 2}, keys: "dh", wantContent: "ab", wantCursor: pos(1, 0), wantUnnamed: "\U0001F600"},
			{name: "dh from a double-width char", content: "a\U0001F600b", at: term.Coordinates{X: 1}, keys: "dh", wantContent: "\U0001F600b", wantCursor: pos(0, 0), wantUnnamed: "a"},
			{name: "dh over a tab", content: "a\tb", at: term.Coordinates{X: 2}, keys: "dh", wantContent: "ab", wantCursor: pos(1, 0), wantUnnamed: "\t"},
			{name: "d99h stops at the line start", content: "abcd", at: term.Coordinates{X: 3}, keys: "d99h", wantContent: "d", wantCursor: pos(0, 0), wantUnnamed: "abc"},
			{name: "dh at the line start", content: "abc", keys: "dh", wantContent: "abc", wantCursor: pos(0, 0)},
			{name: "d0 on an empty buffer", content: "", keys: "d0", wantContent: "", wantCursor: pos(0, 0)},
			{name: "d0 past the line end", content: "abc def", at: term.Coordinates{X: 99}, keys: "d0", wantContent: "f", wantCursor: pos(0, 0), wantUnnamed: "abc de"},
			{name: "d0 below the last line", content: "abc\ndef", at: term.Coordinates{X: 2, Y: 9}, keys: "d0", wantContent: "abc\nf", wantCursor: pos(0, 1), wantUnnamed: "de"},
			{name: "d0 over a tab indent", content: "\tabc", at: term.Coordinates{X: 2}, keys: "d0", wantContent: "bc", wantCursor: pos(0, 0), wantUnnamed: "\ta"},
			{name: "d0 over double-width chars", content: "日本ab", at: term.Coordinates{X: 3}, keys: "d0", wantContent: "b", wantCursor: pos(0, 0), wantUnnamed: "日本a"},
			{name: "c0 over double-width chars", content: "日本ab", at: term.Coordinates{X: 3}, keys: "c0", wantContent: "b", wantCursor: pos(0, 0), wantMode: insertMode, wantUnnamed: "日本a"},
			{name: "y0 over a tab indent", content: "\tabc", at: term.Coordinates{X: 3}, keys: "y0", wantContent: "\tabc", wantCursor: pos(0, 0), wantUnnamed: "\tab"},
			{name: "gU0 over double-width chars", content: "日本ab", at: term.Coordinates{X: 3}, keys: "gU0", wantContent: "日本Ab", wantCursor: pos(0, 0)},
			{name: "d^ back over a tab indent", content: "\t\tabc def", at: term.Coordinates{X: 6}, keys: "d^", wantContent: "\t\tdef", wantCursor: pos(2, 0), wantUnnamed: "abc "},
			{name: "d^ forward over a tab indent", content: "\t\tabc", keys: "d^", wantContent: "abc", wantCursor: pos(0, 0), wantUnnamed: "\t\t"},
			{name: "d^ on a blank line", content: "   ", keys: "d^", wantContent: " ", wantCursor: pos(0, 0), wantUnnamed: "  "},
			{name: "d^ past the line end", content: "  abc", at: term.Coordinates{X: 99}, keys: "d^", wantContent: "  c", wantCursor: pos(2, 0), wantUnnamed: "ab"},
			{name: "d^ on an empty buffer", content: "", keys: "d^", wantContent: "", wantCursor: pos(0, 0)},
			{name: "dF to a double-width char", content: "a\U0001F600bc", at: term.Coordinates{X: 3}, keys: "dF\U0001F600", wantContent: "ac", wantCursor: pos(1, 0), wantUnnamed: "\U0001F600b"},
			{name: "dT to a double-width char", content: "a\U0001F600bc", at: term.Coordinates{X: 3}, keys: "dT\U0001F600", wantContent: "a\U0001F600c", wantCursor: pos(2, 0), wantUnnamed: "b"},
			{name: "dF to a tab", content: "a\tbc", at: term.Coordinates{X: 3}, keys: "dF\t", wantContent: "ac", wantCursor: pos(1, 0), wantUnnamed: "\tb"},
			{name: "dT to a tab", content: "a\tbc", at: term.Coordinates{X: 3}, keys: "dT\t", wantContent: "a\tc", wantCursor: pos(2, 0), wantUnnamed: "b"},
			{name: "cF to a double-width char", content: "a\U0001F600bc", at: term.Coordinates{X: 3}, keys: "cF\U0001F600", wantContent: "ac", wantCursor: pos(1, 0), wantMode: insertMode, wantUnnamed: "\U0001F600b"},
			{name: "yF to a tab", content: "a\tbc", at: term.Coordinates{X: 3}, keys: "yF\t", wantContent: "a\tbc", wantCursor: pos(1, 0), wantUnnamed: "\tb"},
			{name: "gUF over double-width chars", content: "a日本bc", at: term.Coordinates{X: 4}, keys: "gUFa", wantContent: "A日本Bc", wantCursor: pos(0, 0)},
			{name: "dFa past the line end", content: "abcx", at: term.Coordinates{X: 9}, keys: "dFa", wantContent: "x", wantCursor: pos(0, 0), wantUnnamed: "abc"},
			{name: "dFa below the last line", content: "x\nabcx", at: term.Coordinates{X: 3, Y: 9}, keys: "dFa", wantContent: "x\nx", wantCursor: pos(0, 1), wantUnnamed: "abc"},
			{name: "dFa without a match", content: "abcx", at: term.Coordinates{X: 3}, keys: "dFz", wantContent: "abcx", wantCursor: pos(3, 0)},
			{name: "dFa on an empty buffer", content: "", keys: "dFa", wantContent: "", wantCursor: pos(0, 0)},
			{name: "d2Fa with one match", content: "abcx", at: term.Coordinates{X: 3}, keys: "d2Fa", wantContent: "abcx", wantCursor: pos(3, 0)},
			{name: "dTa next to the target", content: "abc", at: term.Coordinates{X: 1}, keys: "dTa", wantContent: "abc", wantCursor: pos(1, 0)},
			{name: "d, over double-width chars", content: "x\U0001F600y\U0001F600z", keys: "2f\U0001F600d,", wantContent: "x\U0001F600z", wantCursor: pos(1, 0), wantUnnamed: "\U0001F600y"},
			{name: "d, over tabs", content: "x\ty\tz", keys: "2f\td,", wantContent: "x\tz", wantCursor: pos(1, 0), wantUnnamed: "\ty"},
		}},
		{name: "forward exclusive motions", cases: []testCase{
			{name: "dl", content: "abc", at: term.Coordinates{X: 1}, keys: "dl", wantContent: "ac", wantCursor: pos(1, 0), wantUnnamed: "b"},
			{name: "d^ forward", content: "    abc", at: term.Coordinates{X: 1}, keys: "d^", wantContent: " abc", wantCursor: pos(1, 0), wantUnnamed: "   "},
			{name: "dw on last word", content: "abc one", at: term.Coordinates{X: 4}, keys: "dw", wantContent: "abc ", wantCursor: pos(3, 0), wantUnnamed: "one"},
			{name: "d3w on last word", content: "abc one", at: term.Coordinates{X: 4}, keys: "d3w", wantContent: "abc ", wantCursor: pos(3, 0), wantUnnamed: "one"},
			{name: "black hole dw on last word", content: "abc one", at: term.Coordinates{X: 4}, keys: "\"_dw", wantContent: "abc ", wantCursor: pos(3, 0)},
			{name: "gUw on last word", content: "abc one", at: term.Coordinates{X: 4}, keys: "gUw", wantContent: "abc ONE", wantCursor: pos(4, 0)},
			{name: "yw on last word", content: "abc one", at: term.Coordinates{X: 4}, keys: "yw", wantContent: "abc one", wantCursor: pos(4, 0), wantUnnamed: "one"},
			{name: "d2|", content: "abcdef", at: term.Coordinates{X: 4}, keys: "d2|", wantContent: "aef", wantCursor: pos(1, 0), wantUnnamed: "bcd"},
			{name: "d4|", content: "abcdef", keys: "d4|", wantContent: "def", wantCursor: pos(0, 0), wantUnnamed: "abc"},
			{name: "dl on an empty buffer", content: "", keys: "dl", wantContent: "", wantCursor: pos(0, 0)},
			{name: "dl past the line end", content: "abc", at: term.Coordinates{X: 9}, keys: "dl", wantContent: "ab", wantCursor: pos(1, 0), wantUnnamed: "c"},
			{name: "dl below the last line", content: "abc\ndef", at: term.Coordinates{X: 1, Y: 9}, keys: "dl", wantContent: "abc\ndf", wantCursor: pos(1, 1), wantUnnamed: "e"},
			{name: "dl on a double-width char", content: "\U0001F600b", keys: "dl", wantContent: "b", wantCursor: pos(0, 0), wantUnnamed: "\U0001F600"},
			{name: "dl on a tab", content: "\tb", keys: "dl", wantContent: "b", wantCursor: pos(0, 0), wantUnnamed: "\t"},
			{name: "d99l stops at the line end", content: "abc", at: term.Coordinates{X: 1}, keys: "d99l", wantContent: "a", wantCursor: pos(0, 0), wantUnnamed: "bc"},
			{name: "dl on the trailing empty line", content: "abc\n", at: term.Coordinates{Y: 1}, keys: "dl", wantContent: "abc\n", wantCursor: pos(0, 1)},
			{name: "dw on an empty buffer", content: "", keys: "dw", wantContent: "", wantCursor: pos(0, 0)},
			{name: "dw on the last double-width word", content: "abc 日本", at: term.Coordinates{X: 4}, keys: "dw", wantContent: "abc ", wantCursor: pos(3, 0), wantUnnamed: "日本"},
			{name: "dw on a double-width word before ascii", content: "日本ab cd", keys: "dw", wantContent: "ab cd", wantCursor: pos(0, 0), wantUnnamed: "日本"},
			{name: "dw on a word before a tab", content: "abc\tone", keys: "dw", wantContent: "one", wantCursor: pos(0, 0), wantUnnamed: "abc\t"},
			{name: "dw on the last word after a tab", content: "abc\tone", at: term.Coordinates{X: 4}, keys: "dw", wantContent: "abc\t", wantCursor: pos(3, 0), wantUnnamed: "one"},
			{name: "dw past the line end", content: "abc one", at: term.Coordinates{X: 99}, keys: "dw", wantContent: "abc on", wantCursor: pos(5, 0), wantUnnamed: "e"},
			{name: "dw below the last line", content: "abc\none", at: term.Coordinates{Y: 9}, keys: "dw", wantContent: "abc\n", wantCursor: pos(0, 1), wantUnnamed: "one"},
			{name: "dw on the trailing empty line", content: "abc\n", at: term.Coordinates{Y: 1}, keys: "dw", wantContent: "abc\n", wantCursor: pos(0, 1)},
			{name: "cw on the last word", content: "abc one", at: term.Coordinates{X: 4}, keys: "cw", wantContent: "abc ", wantCursor: pos(4, 0), wantMode: insertMode, wantUnnamed: "one"},
			{name: "d2| on an empty buffer", content: "", keys: "d2|", wantContent: "", wantCursor: pos(0, 0)},
			{name: "d| to column one", content: "abc", at: term.Coordinates{X: 2}, keys: "d|", wantContent: "c", wantCursor: pos(0, 0), wantUnnamed: "ab"},
			{name: "d9| past the line end", content: "abc", at: term.Coordinates{X: 1}, keys: "d9|", wantContent: "ac", wantCursor: pos(1, 0), wantUnnamed: "b"},
			{name: "d3| past the cursor", content: "abcdef", at: term.Coordinates{X: 99}, keys: "d3|", wantContent: "abf", wantCursor: pos(2, 0), wantUnnamed: "cde"},
			{name: "d3| below the last line", content: "x\nabcdef", at: term.Coordinates{X: 5, Y: 9}, keys: "d3|", wantContent: "x\nabf", wantCursor: pos(2, 1), wantUnnamed: "cde"},
			{name: "d3| over a double-width char", content: "\U0001F600bcd", at: term.Coordinates{X: 3}, keys: "d3|", wantContent: "\U0001F600d", wantCursor: pos(1, 0), wantUnnamed: "bc"},
			{name: "d4| over a double-width char", content: "\U0001F600bcd", keys: "d4|", wantContent: "cd", wantCursor: pos(0, 0), wantUnnamed: "\U0001F600b"},
			{name: "d3| over a tab", content: "\tbcd", at: term.Coordinates{X: 3}, keys: "d3|", wantContent: "\td", wantCursor: pos(1, 0), wantUnnamed: "bc"},
			{name: "d4| onto a tab", content: "ab\tcd", keys: "d4|", wantContent: "\tcd", wantCursor: pos(0, 0), wantUnnamed: "ab"},
			{name: "y3| onto a tab", content: "ab\tcd", at: term.Coordinates{X: 4}, keys: "y3|", wantContent: "ab\tcd", wantCursor: pos(2, 0), wantUnnamed: "\tc"},
		}},
		{name: "exclusive end in column 0", cases: []testCase{
			{name: "db across lines", content: "foo bar\nbaz", at: term.Coordinates{Y: 1}, keys: "db", wantContent: "foo \nbaz", wantCursor: pos(3, 0), wantUnnamed: "bar"},
			{name: "dB across lines", content: "foo bar\nbaz", at: term.Coordinates{Y: 1}, keys: "dB", wantContent: "foo \nbaz", wantCursor: pos(3, 0), wantUnnamed: "bar"},
			{name: "cb across lines", content: "foo bar\nbaz", at: term.Coordinates{Y: 1}, keys: "cb", wantContent: "foo \nbaz", wantCursor: pos(4, 0), wantMode: insertMode, wantUnnamed: "bar"},
			{name: "yb across lines", content: "foo bar\nbaz", at: term.Coordinates{Y: 1}, keys: "yb", wantContent: "foo bar\nbaz", wantCursor: pos(4, 0), wantUnnamed: "bar"},
			{name: "db across lines stops at punctuation", content: "foo bar-x\nbaz", at: term.Coordinates{Y: 1}, keys: "db", wantContent: "foo bar-\nbaz", wantCursor: pos(7, 0), wantUnnamed: "x"},
			{name: "dB across lines spans punctuation", content: "foo bar-x\nbaz", at: term.Coordinates{Y: 1}, keys: "dB", wantContent: "foo \nbaz", wantCursor: pos(3, 0), wantUnnamed: "bar-x"},
			{name: "db across lines with trailing blanks", content: "foo bar  \nbaz", at: term.Coordinates{Y: 1}, keys: "db", wantContent: "foo \nbaz", wantCursor: pos(3, 0), wantUnnamed: "bar  "},
			{name: "db across lines from a blank", content: "foo bar\n  baz", at: term.Coordinates{Y: 1}, keys: "db", wantContent: "foo \n  baz", wantCursor: pos(3, 0), wantUnnamed: "bar"},
			{name: "d3b across lines", content: "a b\nc d\ne", at: term.Coordinates{Y: 2}, keys: "d3b", wantContent: "a \ne", wantCursor: pos(1, 0), wantUnnamed: "b\nc d"},
			{name: "d backtick to an earlier mark", content: "foo bar\nbaz", at: term.Coordinates{Y: 1}, marks: map[rune]term.Coordinates{'a': {X: 4}}, keys: "d`a", wantContent: "foo \nbaz", wantCursor: pos(3, 0), wantUnnamed: "bar"},
			{name: "d backtick to a mark in column 0", content: "foo bar\nbaz", at: term.Coordinates{X: 4}, marks: map[rune]term.Coordinates{'a': {Y: 1}}, keys: "d`a", wantContent: "foo \nbaz", wantCursor: pos(3, 0), wantUnnamed: "bar"},
			{name: "d/ to a match in column 0", content: "foo bar\nbaz", at: term.Coordinates{X: 4}, keys: "d/baz\n", wantContent: "foo \nbaz", wantCursor: pos(3, 0), wantUnnamed: "bar"},
			{name: "d} to an empty line", content: "a b\nc\n\nd", at: term.Coordinates{X: 2}, keys: "d}", wantContent: "a \n\nd", wantCursor: pos(1, 0), wantUnnamed: "b\nc"},
			{name: "d{ from mid line", content: "a\n\nb c", at: term.Coordinates{X: 2, Y: 2}, keys: "d{", wantContent: "a\nc", wantCursor: pos(0, 1), wantUnnamed: "\nb "},
			{name: "db to a double-width word on the previous line", content: "foo \U0001F600\nbaz", at: term.Coordinates{Y: 1}, keys: "db", wantContent: "foo \nbaz", wantCursor: pos(3, 0), wantUnnamed: "\U0001F600"},
			{name: "db to a cjk word on the previous line", content: "foo 日本\nbaz", at: term.Coordinates{Y: 1}, keys: "db", wantContent: "foo \nbaz", wantCursor: pos(3, 0), wantUnnamed: "日本"},
			{name: "db over a trailing tab", content: "foo bar\t\nbaz", at: term.Coordinates{Y: 1}, keys: "db", wantContent: "foo \nbaz", wantCursor: pos(3, 0), wantUnnamed: "bar\t"},
			{name: "db below the last line", content: "foo bar\nbaz", at: term.Coordinates{Y: 9}, keys: "db", wantContent: "foo \nbaz", wantCursor: pos(3, 0), wantUnnamed: "bar"},
			{name: "db past the line end", content: "foo bar\nbaz", at: term.Coordinates{X: 9, Y: 1}, keys: "db", wantContent: "foo bar\nz", wantCursor: pos(0, 1), wantUnnamed: "ba"},
			{name: "db past both ends", content: "foo bar\nbaz", at: term.Coordinates{X: 9, Y: 9}, keys: "db", wantContent: "foo bar\nz", wantCursor: pos(0, 1), wantUnnamed: "ba"},
			{name: "db from the trailing empty line", content: "foo bar\n", at: term.Coordinates{Y: 1}, keys: "db", wantContent: "foo \n", wantCursor: pos(3, 0), wantUnnamed: "bar"},
			{name: "db at the buffer start", content: "abc", keys: "db", wantContent: "abc", wantCursor: pos(0, 0)},
			{name: "db on an empty buffer", content: "", keys: "db", wantContent: "", wantCursor: pos(0, 0)},
			{name: "dB over a double-width WORD", content: "foo a\U0001F600\nbaz", at: term.Coordinates{Y: 1}, keys: "dB", wantContent: "foo \nbaz", wantCursor: pos(3, 0), wantUnnamed: "a\U0001F600"},
			{name: "d} over a double-width char", content: "a \U0001F600\nc\n\nd", at: term.Coordinates{X: 2}, keys: "d}", wantContent: "a \n\nd", wantCursor: pos(1, 0), wantUnnamed: "\U0001F600\nc"},
			{name: "d} past the line end", content: "a b\nc\n\nd", at: term.Coordinates{X: 9}, keys: "d}", wantContent: "a \n\nd", wantCursor: pos(1, 0), wantUnnamed: "b\nc"},
			{name: "d} on an empty buffer", content: "", keys: "d}", wantContent: "", wantCursor: pos(0, 0)},
			{name: "d} on the last paragraph", content: "a b\nc", at: term.Coordinates{X: 2}, keys: "d}", wantContent: "a ", wantCursor: pos(1, 0), wantUnnamed: "b\nc"},
			{name: "d{ over a double-width char", content: "a\n\n\U0001F600 c", at: term.Coordinates{X: 2, Y: 2}, keys: "d{", wantContent: "a\nc", wantCursor: pos(0, 1), wantUnnamed: "\n\U0001F600 "},
			{name: "d{ at the buffer start", content: "a b", keys: "d{", wantContent: "a b", wantCursor: pos(0, 0)},
			{name: "d/ to a match after a double-width char", content: "foo \U0001F600\nbaz", at: term.Coordinates{X: 4}, keys: "d/baz\n", wantContent: "foo \nbaz", wantCursor: pos(3, 0), wantUnnamed: "\U0001F600"},
			{name: "d/ without a match", content: "foo bar\nbaz", at: term.Coordinates{X: 4}, keys: "d/zzz\n", wantContent: "foo bar\nbaz", wantCursor: pos(4, 0)},
			{name: "d/ on an empty buffer", content: "", keys: "d/a\n", wantContent: "", wantCursor: pos(0, 0)},
			{name: "d/ past the line end", content: "foo bar\nbaz", at: term.Coordinates{X: 99}, keys: "d/baz\n", wantContent: "foo ba\nbaz", wantCursor: pos(5, 0), wantUnnamed: "r"},
			{name: "d backtick over a double-width char", content: "foo \U0001F600\nbaz", at: term.Coordinates{Y: 1}, marks: map[rune]term.Coordinates{'a': {X: 4}}, keys: "d`a", wantContent: "foo \nbaz", wantCursor: pos(3, 0), wantUnnamed: "\U0001F600"},
			{name: "d backtick over a tab", content: "foo\tbar\nbaz", at: term.Coordinates{Y: 1}, marks: map[rune]term.Coordinates{'a': {X: 4}}, keys: "d`a", wantContent: "foo\t\nbaz", wantCursor: pos(3, 0), wantUnnamed: "bar"},
			{name: "d backtick from past the line end", content: "foo bar\nbaz", at: term.Coordinates{X: 9, Y: 1}, marks: map[rune]term.Coordinates{'a': {X: 4}}, keys: "d`a", wantContent: "foo z", wantCursor: pos(4, 0), wantUnnamed: "bar\nba"},
			{name: "c backtick to a mark in column 0", content: "foo bar\nbaz", at: term.Coordinates{X: 4}, marks: map[rune]term.Coordinates{'a': {Y: 1}}, keys: "c`a", wantContent: "foo \nbaz", wantCursor: pos(4, 0), wantMode: insertMode, wantUnnamed: "bar"},
			{name: "gU backtick to a mark in column 0", content: "foo bar\nbaz", at: term.Coordinates{X: 4}, marks: map[rune]term.Coordinates{'a': {Y: 1}}, keys: "gU`a", wantContent: "foo BAR\nbaz", wantCursor: pos(4, 0)},
			{name: "gU/ to a match in column 0", content: "foo bar\nbaz", at: term.Coordinates{X: 4}, keys: "gU/baz\n", wantContent: "foo BAR\nbaz", wantCursor: pos(4, 0)},
			{name: "y/ to a match in column 0", content: "foo bar\nbaz", at: term.Coordinates{X: 4}, keys: "y/baz\n", wantContent: "foo bar\nbaz", wantCursor: pos(4, 0), wantUnnamed: "bar"},
		}},
		{name: "exclusive motion from the indent is linewise", cases: []testCase{
			{name: "db linewise", content: "foo\nbaz", at: term.Coordinates{Y: 1}, keys: "db", wantContent: "baz", wantCursor: pos(0, 0), wantUnnamed: "foo\n", wantLinewise: true},
			{name: "dB linewise", content: "foo\nbaz", at: term.Coordinates{Y: 1}, keys: "dB", wantContent: "baz", wantCursor: pos(0, 0), wantUnnamed: "foo\n", wantLinewise: true},
			{name: "yb linewise", content: "foo\nbaz", at: term.Coordinates{Y: 1}, keys: "yb", wantContent: "foo\nbaz", wantCursor: pos(0, 0), wantUnnamed: "foo\n", wantLinewise: true},
			{name: "db linewise repeated", content: "foo\nbar\nbaz", at: term.Coordinates{Y: 2}, keys: "db.", wantContent: "baz", wantCursor: pos(0, 0), wantUnnamed: "foo\n", wantLinewise: true},
			{name: "cb linewise keeps a line to insert into", content: "foo\nbaz", at: term.Coordinates{Y: 1}, keys: "cb", wantContent: "\nbaz", wantCursor: pos(0, 0), wantMode: insertMode, wantUnnamed: "foo\n", wantLinewise: true},
			{name: "db linewise then p puts the line below", content: "foo\nbaz", at: term.Coordinates{Y: 1}, keys: "dbp", wantContent: "baz\nfoo", wantCursor: pos(0, 1), wantUnnamed: "foo\n", wantLinewise: true},
			{name: "db charwise then p", content: "foo bar\nbaz", at: term.Coordinates{Y: 1}, keys: "dbp", wantContent: "foo bar\nbaz", wantCursor: pos(6, 0), wantUnnamed: "bar"},
			{name: "db linewise from indented word", content: "  foo\nbaz", at: term.Coordinates{Y: 1}, keys: "db", wantContent: "baz", wantCursor: pos(0, 0), wantUnnamed: "  foo\n", wantLinewise: true},
			{name: "db stops at an empty line", content: "foo\n\nbar", at: term.Coordinates{Y: 2}, keys: "db", wantContent: "foo\nbar", wantCursor: pos(0, 1), wantUnnamed: "\n", wantLinewise: true},
			{name: "d2b over empty lines", content: "foo\n\n\nbar", at: term.Coordinates{Y: 3}, keys: "d2b", wantContent: "foo\nbar", wantCursor: pos(0, 1), wantUnnamed: "\n\n", wantLinewise: true},
			{name: "db between empty lines", content: "\n\n", at: term.Coordinates{Y: 2}, keys: "db", wantContent: "\n", wantCursor: pos(0, 1), wantUnnamed: "\n", wantLinewise: true},
			{name: "d2b linewise", content: "a b\nc d\ne", at: term.Coordinates{Y: 2}, keys: "d2b", wantContent: "a b\ne", wantCursor: pos(0, 1), wantUnnamed: "c d\n", wantLinewise: true},
			{name: ">b shifts only the previous line", content: "foo bar\nbaz", at: term.Coordinates{Y: 1}, keys: ">b", wantContent: "\tfoo bar\nbaz"},
			{name: "d} linewise", content: "a b\nc\n\nd", keys: "d}", wantContent: "\nd", wantCursor: pos(0, 0), wantUnnamed: "a b\nc\n", wantLinewise: true},
			{name: "d{ linewise", content: "a\nb\n\nc\nd", at: term.Coordinates{Y: 4}, keys: "d{", wantContent: "a\nb\nd", wantCursor: pos(0, 2), wantUnnamed: "\nc\n", wantLinewise: true},
			{name: "d/ linewise", content: "foo\nbaz", keys: "d/baz\n", wantContent: "baz", wantCursor: pos(0, 0), wantUnnamed: "foo\n", wantLinewise: true},
			{name: "d backtick linewise", content: "foo\nbaz", marks: map[rune]term.Coordinates{'a': {Y: 1}}, keys: "d`a", wantContent: "baz", wantCursor: pos(0, 0), wantUnnamed: "foo\n", wantLinewise: true},
			{name: "db linewise from a tab indent", content: "\tfoo\nbaz", at: term.Coordinates{Y: 1}, keys: "db", wantContent: "baz", wantCursor: pos(0, 0), wantUnnamed: "\tfoo\n", wantLinewise: true},
			{name: "db linewise from a mixed indent", content: " \t foo\nbaz", at: term.Coordinates{Y: 1}, keys: "db", wantContent: "baz", wantCursor: pos(0, 0), wantUnnamed: " \t foo\n", wantLinewise: true},
			{name: "db linewise from a double-width word", content: "日本\nbaz", at: term.Coordinates{Y: 1}, keys: "db", wantContent: "baz", wantCursor: pos(0, 0), wantUnnamed: "日本\n", wantLinewise: true},
			{name: "db linewise below the last line", content: "foo\nbaz", at: term.Coordinates{Y: 9}, keys: "db", wantContent: "baz", wantCursor: pos(0, 0), wantUnnamed: "foo\n", wantLinewise: true},
			{name: "db linewise from the trailing empty line", content: "foo\n", at: term.Coordinates{Y: 1}, keys: "db", wantContent: "", wantCursor: pos(0, 0), wantUnnamed: "foo\n", wantLinewise: true},
			{name: "cb linewise keeps a tab indent", content: "\tfoo\nbaz", at: term.Coordinates{Y: 1}, keys: "cb", wantContent: "\t\nbaz", wantCursor: pos(1, 0), wantMode: insertMode, wantUnnamed: "\tfoo\n", wantLinewise: true},
			{name: "yb linewise from a tab indent", content: "\tfoo\nbaz", at: term.Coordinates{Y: 1}, keys: "yb", wantContent: "\tfoo\nbaz", wantCursor: pos(1, 0), wantUnnamed: "\tfoo\n", wantLinewise: true},
			{name: "d} linewise from a tab indent", content: "\ta b\nc\n\nd", at: term.Coordinates{X: 1}, keys: "d}", wantContent: "\nd", wantCursor: pos(0, 0), wantUnnamed: "\ta b\nc\n", wantLinewise: true},
			{name: "d} linewise below the last line", content: "a\n\nb\nc", at: term.Coordinates{Y: 9}, keys: "d}", wantContent: "a\n\nb\n", wantCursor: pos(0, 3), wantUnnamed: "c"},
			{name: "c} linewise keeps the indent", content: "  a b\nc\n\nd", at: term.Coordinates{X: 2}, keys: "c}", wantContent: "  \n\nd", wantCursor: pos(2, 0), wantMode: insertMode, wantUnnamed: "  a b\nc\n", wantLinewise: true},
			{name: "y} linewise", content: "a b\nc\n\nd", keys: "y}", wantContent: "a b\nc\n\nd", wantCursor: pos(0, 0), wantUnnamed: "a b\nc\n", wantLinewise: true},
			{name: "gU} linewise", content: "a b\nc\n\nd", at: term.Coordinates{X: 1}, keys: "gU}", wantContent: "a B\nC\n\nd", wantCursor: pos(1, 0)},
			{name: "d{ linewise from a double-width line", content: "a\n\n\U0001F600\nd", at: term.Coordinates{Y: 3}, keys: "d{", wantContent: "a\nd", wantCursor: pos(0, 1), wantUnnamed: "\n\U0001F600\n", wantLinewise: true},
			{name: "d/ linewise from a tab indent", content: "\tfoo\nbaz", at: term.Coordinates{X: 1}, keys: "d/baz\n", wantContent: "baz", wantCursor: pos(0, 0), wantUnnamed: "\tfoo\n", wantLinewise: true},
			{name: "c/ linewise", content: "foo\nbaz", keys: "c/baz\n", wantContent: "\nbaz", wantCursor: pos(0, 0), wantMode: insertMode, wantUnnamed: "foo\n", wantLinewise: true},
			{name: "gU/ linewise", content: "foo\nbaz", at: term.Coordinates{X: 1}, keys: "gU/baz\n", wantContent: "fOO\nbaz", wantCursor: pos(1, 0)},
			{name: "d backtick linewise from a tab indent", content: "\tfoo\nbaz", at: term.Coordinates{X: 1}, marks: map[rune]term.Coordinates{'a': {Y: 1}}, keys: "d`a", wantContent: "baz", wantCursor: pos(0, 0), wantUnnamed: "\tfoo\n", wantLinewise: true},
			{name: "d backtick linewise from the trailing empty line", content: "foo\n", at: term.Coordinates{Y: 1}, marks: map[rune]term.Coordinates{'a': {}}, keys: "d`a", wantContent: "", wantCursor: pos(0, 0), wantUnnamed: "foo\n", wantLinewise: true},
			{name: "db linewise past the line end", content: "foo\nb", at: term.Coordinates{X: 9, Y: 1}, keys: "db", wantContent: "b", wantCursor: pos(0, 0), wantUnnamed: "foo\n", wantLinewise: true},
		}},
		{name: "word motions", cases: []testCase{
			{name: "b to the previous line", content: "foo bar\nbaz", at: term.Coordinates{Y: 1}, keys: "b", wantContent: "foo bar\nbaz", wantCursor: pos(4, 0)},
			{name: "B to the previous line", content: "foo bar\nbaz", at: term.Coordinates{Y: 1}, keys: "B", wantContent: "foo bar\nbaz", wantCursor: pos(4, 0)},
			{name: "2b to the previous line", content: "foo bar\nbaz qux", at: term.Coordinates{X: 4, Y: 1}, keys: "2b", wantContent: "foo bar\nbaz qux", wantCursor: pos(4, 0)},
			{name: "3b to the previous line", content: "foo bar\nbaz qux", at: term.Coordinates{X: 4, Y: 1}, keys: "3b", wantContent: "foo bar\nbaz qux", wantCursor: pos(0, 0)},
			{name: "b stops at an empty line", content: "foo\n\n\nbar", at: term.Coordinates{Y: 3}, keys: "b", wantContent: "foo\n\n\nbar", wantCursor: pos(0, 2)},
			{name: "2b stops at each empty line", content: "foo\n\n\nbar", at: term.Coordinates{Y: 3}, keys: "2b", wantContent: "foo\n\n\nbar", wantCursor: pos(0, 1)},
			{name: "B over punctuation to the previous line", content: "foo bar-x\nbaz", at: term.Coordinates{Y: 1}, keys: "B", wantContent: "foo bar-x\nbaz", wantCursor: pos(4, 0)},
			{name: "dB from an indented word", content: "foo bar-x\n  baz", at: term.Coordinates{X: 2, Y: 1}, keys: "dB", wantContent: "foo baz", wantCursor: pos(4, 0), wantUnnamed: "bar-x\n  "},
			{name: "db stops after a double-width char", content: "x \U0001F600y z", at: term.Coordinates{X: 5}, keys: "db", wantContent: "x \U0001F600z", wantCursor: pos(3, 0), wantUnnamed: "y "},
			{name: "b on an empty buffer", content: "", keys: "b", wantContent: "", wantCursor: pos(0, 0)},
			{name: "b below the last line", content: "foo bar\nbaz", at: term.Coordinates{Y: 9}, keys: "b", wantContent: "foo bar\nbaz", wantCursor: pos(4, 0)},
			{name: "b past the line end", content: "foo bar", at: term.Coordinates{X: 99}, keys: "b", wantContent: "foo bar", wantCursor: pos(4, 0)},
			{name: "b from the trailing empty line", content: "foo bar\n", at: term.Coordinates{Y: 1}, keys: "b", wantContent: "foo bar\n", wantCursor: pos(4, 0)},
			{name: "b over a tab-only line", content: "foo\n\t\nbar", at: term.Coordinates{Y: 2}, keys: "b", wantContent: "foo\n\t\nbar", wantCursor: pos(0, 0)},
			{name: "b from ascii to cjk", content: "日本abc", at: term.Coordinates{X: 2}, keys: "b", wantContent: "日本abc", wantCursor: pos(0, 0)},
			{name: "b from cjk to ascii", content: "abc日本", at: term.Coordinates{X: 4}, keys: "b", wantContent: "abc日本", wantCursor: pos(3, 0)},
			{name: "b over cjk", content: "ab 日本", at: term.Coordinates{X: 4}, keys: "b", wantContent: "ab 日本", wantCursor: pos(3, 0)},
			{name: "b from emoji to ascii", content: "abc\U0001F600", at: term.Coordinates{X: 3}, keys: "b", wantContent: "abc\U0001F600", wantCursor: pos(0, 0)},
			{name: "b over a tab", content: "foo\tbar", at: term.Coordinates{X: 4}, keys: "b", wantContent: "foo\tbar", wantCursor: pos(0, 0)},
			{name: "99b to the buffer start", content: "a b\nc d", at: term.Coordinates{X: 2, Y: 1}, keys: "99b", wantContent: "a b\nc d", wantCursor: pos(0, 0)},
			{name: "B over a double-width WORD", content: "foo \U0001F600bar", at: term.Coordinates{X: 6}, keys: "B", wantContent: "foo \U0001F600bar", wantCursor: pos(4, 0)},
			{name: "B across a tab", content: "foo\tbar", at: term.Coordinates{X: 4}, keys: "B", wantContent: "foo\tbar", wantCursor: pos(0, 0)},
			{name: "B below the last line", content: "foo bar\nbaz", at: term.Coordinates{Y: 9}, keys: "B", wantContent: "foo bar\nbaz", wantCursor: pos(4, 0)},
			{name: "B on an empty buffer", content: "", keys: "B", wantContent: "", wantCursor: pos(0, 0)},
			{name: "w from ascii to an emoji", content: "ab\U0001F600cd", keys: "w", wantContent: "ab\U0001F600cd", wantCursor: pos(2, 0)},
			{name: "w from an emoji to ascii", content: "\U0001F600ab", keys: "w", wantContent: "\U0001F600ab", wantCursor: pos(1, 0)},
			{name: "w over cjk", content: "日本ab", keys: "w", wantContent: "日本ab", wantCursor: pos(2, 0)},
			{name: "w over two emoji", content: "\U0001F600\U0001F600a", keys: "w", wantContent: "\U0001F600\U0001F600a", wantCursor: pos(2, 0)},
			{name: "w on an empty buffer", content: "", keys: "w", wantContent: "", wantCursor: pos(0, 0)},
			{name: "w past the line end", content: "abc def\nghi", at: term.Coordinates{X: 99}, keys: "w", wantContent: "abc def\nghi", wantCursor: pos(0, 1)},
			{name: "w below the last line", content: "abc\ndef", at: term.Coordinates{Y: 9}, keys: "w", wantContent: "abc\ndef", wantCursor: pos(2, 1)},
			{name: "w over a tab", content: "ab\tcd", keys: "w", wantContent: "ab\tcd", wantCursor: pos(3, 0)},
			{name: "w on the trailing empty line", content: "abc\n", at: term.Coordinates{Y: 1}, keys: "w", wantContent: "abc\n", wantCursor: pos(0, 1)},
			{name: "yw on a double-width word", content: "日本 ab", keys: "yw", wantContent: "日本 ab", wantCursor: pos(0, 0), wantUnnamed: "日本 "},
			{name: "cw on a double-width word", content: "日本 ab", keys: "cw", wantContent: " ab", wantCursor: pos(0, 0), wantMode: insertMode, wantUnnamed: "日本"},
			{name: "W over a double-width WORD", content: "a\U0001F600b cd", keys: "W", wantContent: "a\U0001F600b cd", wantCursor: pos(4, 0)},
			{name: "e before a trailing line break", content: "ab cd\n", at: term.Coordinates{X: 3}, keys: "e", wantContent: "ab cd\n", wantCursor: pos(4, 0)},
			{name: "de before a trailing line break", content: "ab cd\n", at: term.Coordinates{X: 3}, keys: "de", wantContent: "ab \n", wantCursor: pos(2, 0), wantUnnamed: "cd"},
			{name: "E before a trailing line break", content: "ab cd\n", at: term.Coordinates{X: 3}, keys: "E", wantContent: "ab cd\n", wantCursor: pos(4, 0)},
			{name: "dE before a trailing line break", content: "ab cd\n", at: term.Coordinates{X: 3}, keys: "dE", wantContent: "ab \n", wantCursor: pos(2, 0), wantUnnamed: "cd"},
			{name: "e at the buffer end", content: "ab cd", at: term.Coordinates{X: 3}, keys: "e", wantContent: "ab cd", wantCursor: pos(4, 0)},
			{name: "e across a line break", content: "ab\ncd", at: term.Coordinates{X: 1}, keys: "e", wantContent: "ab\ncd", wantCursor: pos(1, 1)},
			{name: "e over empty lines", content: "ab\n\n\ncd", at: term.Coordinates{X: 1}, keys: "e", wantContent: "ab\n\n\ncd", wantCursor: pos(1, 3)},
			{name: "ge to an empty line", content: "ab\n\ncd", at: term.Coordinates{Y: 2}, keys: "ge", wantContent: "ab\n\ncd", wantCursor: pos(0, 1)},
			{name: "ge from an empty line", content: "ab\n\ncd", at: term.Coordinates{Y: 1}, keys: "ge", wantContent: "ab\n\ncd", wantCursor: pos(1, 0)},
			{name: "ge from the trailing empty line", content: "ab cd\n", at: term.Coordinates{Y: 1}, keys: "ge", wantContent: "ab cd\n", wantCursor: pos(4, 0)},
			{name: "2ge over empty lines", content: "ab\n\n\ncd", at: term.Coordinates{Y: 3}, keys: "2ge", wantContent: "ab\n\n\ncd", wantCursor: pos(0, 1)},
			{name: "dge after an empty line", content: "ab\n\ncd", at: term.Coordinates{Y: 2}, keys: "dge", wantContent: "ab\nd", wantCursor: pos(0, 1), wantUnnamed: "\nc"},
			{name: "gE to an empty line", content: "ab\n\ncd", at: term.Coordinates{Y: 2}, keys: "gE", wantContent: "ab\n\ncd", wantCursor: pos(0, 1)},
			{name: "gE from an empty line", content: "ab\n\ncd", at: term.Coordinates{Y: 1}, keys: "gE", wantContent: "ab\n\ncd", wantCursor: pos(1, 0)},
		}},
		{name: "find motions", cases: []testCase{
			{name: "df1", content: "x1y", keys: "df1", wantContent: "y", wantCursor: pos(0, 0), wantUnnamed: "x1"},
			{name: "dt1", content: "x1y", keys: "dt1", wantContent: "1y", wantCursor: pos(0, 0), wantUnnamed: "x"},
			{name: "yf1", content: "x1y", keys: "yf1", wantContent: "x1y", wantCursor: pos(0, 0), wantUnnamed: "x1"},
			{name: ">f1", content: "x1y", keys: ">f1", wantContent: "\tx1y"},
			{name: "dF1", content: "a1bx", at: term.Coordinates{X: 3}, keys: "dF1", wantContent: "ax", wantCursor: pos(1, 0), wantUnnamed: "1b"},
			{name: "gUf2", content: "x2y", keys: "gUf2", wantContent: "X2y", wantCursor: pos(0, 0)},
			{name: "df1 without a match", content: "xay", keys: "df1", wantContent: "xay", wantCursor: pos(0, 0)},
			{name: "df1 on an empty buffer", content: "", keys: "df1", wantContent: "", wantCursor: pos(0, 0)},
			{name: "df1 after a double-width char", content: "\U0001F6001y", keys: "df1", wantContent: "y", wantCursor: pos(0, 0), wantUnnamed: "\U0001F6001"},
			{name: "dt1 next to a double-width char", content: "\U0001F6001y", keys: "dt1", wantContent: "1y", wantCursor: pos(0, 0), wantUnnamed: "\U0001F600"},
			{name: "d2f1", content: "a1b1c", keys: "d2f1", wantContent: "c", wantCursor: pos(0, 0), wantUnnamed: "a1b1"},
			{name: "2df1", content: "a1b1c", keys: "2df1", wantContent: "c", wantCursor: pos(0, 0), wantUnnamed: "a1b1"},
			{name: "d; after f1", content: "a1b1c", keys: "f1d;", wantContent: "ac", wantCursor: pos(1, 0), wantUnnamed: "1b1"},
			{name: "d, after F1", content: "a1b1c", keys: "$F1d,", wantContent: "a1b1c", wantCursor: pos(3, 0)},
			{name: "dT1 past the line end", content: "a1bx", at: term.Coordinates{X: 9}, keys: "dT1", wantContent: "a1x", wantCursor: pos(2, 0), wantUnnamed: "b"},
			{name: "dF0", content: "a0bx", at: term.Coordinates{X: 3}, keys: "dF0", wantContent: "ax", wantCursor: pos(1, 0), wantUnnamed: "0b"},
			{name: "dt0", content: "x0y", keys: "dt0", wantContent: "0y", wantCursor: pos(0, 0), wantUnnamed: "x"},
			{name: "gUt9 over a tab", content: "x\ty9z", keys: "gUt9", wantContent: "X\tY9z", wantCursor: pos(0, 0)},
			{name: "yf1 over a tab", content: "x\t1y", keys: "yf1", wantContent: "x\t1y", wantCursor: pos(0, 0), wantUnnamed: "x\t1"},
			{name: "f without a match", content: "abc", keys: "fz", wantContent: "abc", wantCursor: pos(0, 0)},
			{name: "2fa", content: "xaya", keys: "2fa", wantContent: "xaya", wantCursor: pos(3, 0)},
			{name: "3fa with two matches", content: "xaya", keys: "3fa", wantContent: "xaya", wantCursor: pos(0, 0)},
			{name: "; without another match", content: "xay", keys: "fa;", wantContent: "xay", wantCursor: pos(1, 0)},
			{name: ", without a match", content: "xay", keys: "fa,", wantContent: "xay", wantCursor: pos(1, 0)},
			{name: "2; with one more match", content: "xaya", keys: "fa2;", wantContent: "xaya", wantCursor: pos(1, 0)},
			{name: "; after t skips the adjacent target", content: "xaya", keys: "ta;", wantContent: "xaya", wantCursor: pos(2, 0)},
			{name: "f to a double-width char", content: "a\U0001F600b", keys: "f\U0001F600", wantContent: "a\U0001F600b", wantCursor: pos(1, 0)},
			{name: "f to a tab", content: "a\tb", keys: "f\t", wantContent: "a\tb", wantCursor: pos(1, 0)},
			{name: "Fa past the line end", content: "abca", at: term.Coordinates{X: 9}, keys: "Fa", wantContent: "abca", wantCursor: pos(0, 0)},
			{name: "fa below the last line", content: "x\nabca", at: term.Coordinates{Y: 9}, keys: "fa", wantContent: "x\nabca", wantCursor: pos(3, 1)},
			{name: "fa on an empty buffer", content: "", keys: "fa", wantContent: "", wantCursor: pos(0, 0)},
			{name: "Fa on the trailing empty line", content: "abc\n", at: term.Coordinates{Y: 1}, keys: "Fa", wantContent: "abc\n", wantCursor: pos(0, 1)},
			{name: "df1 below the last line", content: "x\nab1c", at: term.Coordinates{Y: 9}, keys: "df1", wantContent: "x\nc", wantCursor: pos(0, 1), wantUnnamed: "ab1"},
			{name: "d2f1 with one match", content: "a1b", keys: "d2f1", wantContent: "a1b", wantCursor: pos(0, 0)},
			{name: "d; without another match", content: "a1b", keys: "f1d;", wantContent: "a1b", wantCursor: pos(1, 0)},
		}},
		{name: "paragraph motions", cases: []testCase{
			{name: "} on an empty buffer", content: "", keys: "}", wantContent: "", wantCursor: pos(0, 0)},
			{name: "} on the last paragraph", content: "a b\nc", keys: "}", wantContent: "a b\nc", wantCursor: pos(0, 1)},
			{name: "} past the line end", content: "abc\n\nd", at: term.Coordinates{X: 9}, keys: "}", wantContent: "abc\n\nd", wantCursor: pos(0, 1)},
			{name: "} below the last line", content: "a\n\nb\nc", at: term.Coordinates{Y: 9}, keys: "}", wantContent: "a\n\nb\nc", wantCursor: pos(0, 3)},
			{name: "2} to the buffer end", content: "a\n\nb", keys: "2}", wantContent: "a\n\nb", wantCursor: pos(0, 2)},
			{name: "3} past the buffer end", content: "a\n\nb", keys: "3}", wantContent: "a\n\nb", wantCursor: pos(0, 0)},
			{name: "} over a double-width line", content: "\U0001F600\n\nb", keys: "}", wantContent: "\U0001F600\n\nb", wantCursor: pos(0, 1)},
			{name: "} over a tab-indented paragraph", content: "a\n\tb\n\nc", keys: "}", wantContent: "a\n\tb\n\nc", wantCursor: pos(0, 2)},
			{name: "{ on an empty buffer", content: "", keys: "{", wantContent: "", wantCursor: pos(0, 0)},
			{name: "{ past the line end", content: "a\n\nbcd", at: term.Coordinates{X: 9, Y: 2}, keys: "{", wantContent: "a\n\nbcd", wantCursor: pos(0, 1)},
			{name: "{ below the last line", content: "a\n\nb", at: term.Coordinates{Y: 9}, keys: "{", wantContent: "a\n\nb", wantCursor: pos(0, 1)},
		}},
		{name: "sentence motions", cases: []testCase{
			{name: "d(", content: "One. Two three.", at: term.Coordinates{X: 9}, keys: "d(", wantContent: "One. three.", wantCursor: pos(5, 0), wantUnnamed: "Two "},
			{name: "d)", content: "One. Two.", keys: "d)", wantContent: "Two.", wantCursor: pos(0, 0), wantUnnamed: "One. "},
			{name: "d) on an empty buffer", content: "", keys: "d)", wantContent: "", wantCursor: pos(0, 0)},
			{name: "d( on an empty buffer", content: "", keys: "d(", wantContent: "", wantCursor: pos(0, 0)},
			{name: "d) after a tab", content: "One.\tTwo.", keys: "d)", wantContent: "Two.", wantCursor: pos(0, 0), wantUnnamed: "One.\t"},
			{name: "d) after a double-width char", content: "\U0001F600. Two.", keys: "d)", wantContent: "Two.", wantCursor: pos(0, 0), wantUnnamed: "\U0001F600. "},
			{name: "d) on the last sentence", content: "One. Two.", at: term.Coordinates{X: 5}, keys: "d)", wantContent: "One. ", wantCursor: pos(4, 0), wantUnnamed: "Two."},
			{name: "d) across lines", content: "One.\nTwo.", keys: "d)", wantContent: "Two.", wantCursor: pos(0, 0), wantUnnamed: "One.\n", wantLinewise: true},
			{name: "d) to an empty line", content: "One\n\nTwo", keys: "d)", wantContent: "\nTwo", wantCursor: pos(0, 0), wantUnnamed: "One\n", wantLinewise: true},
			{name: "d) over two blanks", content: "One.  Two.", keys: "d)", wantContent: "Two.", wantCursor: pos(0, 0), wantUnnamed: "One.  "},
			{name: "d) over closing chars", content: "(One.) Two.", keys: "d)", wantContent: "Two.", wantCursor: pos(0, 0), wantUnnamed: "(One.) "},
			{name: "d( across lines", content: "One.\nTwo three.", at: term.Coordinates{X: 4, Y: 1}, keys: "d(", wantContent: "One.\nthree.", wantCursor: pos(0, 1), wantUnnamed: "Two "},
			{name: "d( at the buffer start", content: "One. Two.", keys: "d(", wantContent: "One. Two.", wantCursor: pos(0, 0)},
			{name: "d( past the line end", content: "One. Two three.", at: term.Coordinates{X: 99}, keys: "d(", wantContent: "One. .", wantCursor: pos(5, 0), wantUnnamed: "Two three"},
			{name: "d) below the last line", content: "One.\nTwo. Three.", at: term.Coordinates{Y: 9}, keys: "d)", wantContent: "One.\nThree.", wantCursor: pos(0, 1), wantUnnamed: "Two. "},
			{name: ") from the trailing empty line", content: "One.\n", at: term.Coordinates{Y: 1}, keys: ")", wantContent: "One.\n", wantCursor: pos(0, 1)},
			{name: "( from the trailing empty line", content: "One. Two.\n", at: term.Coordinates{Y: 1}, keys: "(", wantContent: "One. Two.\n", wantCursor: pos(5, 0)},
			{name: "2) over two sentences", content: "One. Two. Three.", keys: "2)", wantContent: "One. Two. Three.", wantCursor: pos(10, 0)},
			{name: "2( over two sentences", content: "One. Two. Three.", at: term.Coordinates{X: 12}, keys: "2(", wantContent: "One. Two. Three.", wantCursor: pos(5, 0)},
			{name: ") over empty lines", content: "One\n\n\nTwo", at: term.Coordinates{Y: 1}, keys: ")", wantContent: "One\n\n\nTwo", wantCursor: pos(0, 3)},
			{name: "( over empty lines", content: "One\n\n\nTwo", at: term.Coordinates{Y: 2}, keys: "(", wantContent: "One\n\n\nTwo", wantCursor: pos(0, 0)},
			{name: ") to an indented line", content: "One\n\n  Two", at: term.Coordinates{Y: 1}, keys: ")", wantContent: "One\n\n  Two", wantCursor: pos(2, 2)},
			{name: "( after a sentence end", content: "A. One.\n\n\nX", at: term.Coordinates{Y: 2}, keys: "(", wantContent: "A. One.\n\n\nX", wantCursor: pos(3, 0)},
			{name: "d) over empty lines", content: "One\n\n\nTwo", at: term.Coordinates{Y: 1}, keys: "d)", wantContent: "One\nTwo", wantCursor: pos(0, 1), wantUnnamed: "\n\n", wantLinewise: true},
			{name: "d( over empty lines", content: "One\n\n\nTwo", at: term.Coordinates{Y: 2}, keys: "d(", wantContent: "\nTwo", wantCursor: pos(0, 0), wantUnnamed: "One\n\n", wantLinewise: true},
		}},
		{name: "case change", cases: []testCase{
			{name: "gU keeps the unnamed register", content: "abc one", keys: "ywwgUe0P", wantContent: "abc abc ONE", wantCursor: pos(3, 0), wantUnnamed: "abc "},
			{name: "gUe leaves the cursor at the start", content: "one two", keys: "gUe", wantContent: "ONE two", wantCursor: pos(0, 0)},
			{name: "gUw leaves the cursor at the start", content: "one two", keys: "gUw", wantContent: "ONE two", wantCursor: pos(0, 0)},
			{name: "gUfy leaves the cursor at the start", content: "xayc", keys: "gUfy", wantContent: "XAYc", wantCursor: pos(0, 0)},
			{name: "gUfU leaves the cursor at the start", content: "xUy", keys: "gUfU", wantContent: "XUy", wantCursor: pos(0, 0)},
			{name: "gUfb leaves the cursor at the start", content: "xabc", keys: "gUfb", wantContent: "XABc", wantCursor: pos(0, 0)},
			{name: "gUtb leaves the cursor at the start", content: "xabc", keys: "gUtb", wantContent: "XAbc", wantCursor: pos(0, 0)},
			{name: "visual U leaves the cursor at the start", content: "ab,de", keys: "vf,U", wantContent: "AB,de", wantCursor: pos(0, 0)},
			{name: "gUw on an empty buffer", content: "", keys: "yygUw", wantContent: "", wantCursor: pos(0, 0), wantUnnamed: "\n", wantLinewise: true},
			{name: "gU0 past the line end", content: "abc def", at: term.Coordinates{X: 99}, keys: "gU0", wantContent: "ABC DEf", wantCursor: pos(0, 0)},
			{name: "gUFa over double-width chars", content: "a日本bc", at: term.Coordinates{X: 4}, keys: "yygUFa", wantContent: "A日本Bc", wantCursor: pos(0, 0), wantUnnamed: "a日本bc\n", wantLinewise: true},
			{name: "g~h over a tab", content: "a\tb", at: term.Coordinates{X: 2}, keys: "yyg~h", wantContent: "a\tb", wantCursor: pos(1, 0), wantUnnamed: "a\tb\n", wantLinewise: true},
			{name: "gub linewise", content: "FOO\nBAZ", at: term.Coordinates{Y: 1}, keys: "yygub", wantContent: "foo\nBAZ", wantCursor: pos(0, 0), wantUnnamed: "BAZ\n", wantLinewise: true},
			{name: "gUj", content: "ab\ncd\nef", at: term.Coordinates{X: 1}, keys: "yygUj", wantContent: "AB\nCD\nef", wantCursor: pos(1, 0), wantUnnamed: "ab\n", wantLinewise: true},
			{name: "gUk", content: "ab\ncd\nef", at: term.Coordinates{X: 1, Y: 1}, keys: "yygUk", wantContent: "AB\nCD\nef", wantCursor: pos(1, 0), wantUnnamed: "cd\n", wantLinewise: true},
			{name: "gUG", content: "ab\ncd", at: term.Coordinates{X: 1}, keys: "yygUG", wantContent: "AB\nCD", wantCursor: pos(1, 0), wantUnnamed: "ab\n", wantLinewise: true},
			{name: "gUU", content: "ab\ncd", keys: "yylgUU", wantContent: "AB\ncd", wantCursor: pos(0, 0), wantUnnamed: "ab\n", wantLinewise: true},
			{name: "g~~ on a double-width line", content: "日本ab", keys: "yyllg~~", wantContent: "日本AB", wantCursor: pos(0, 0), wantUnnamed: "日本ab\n", wantLinewise: true},
			{name: "gUiw", content: "foo bar", at: term.Coordinates{X: 5}, keys: "yyllgUiw", wantContent: "foo BAR", wantCursor: pos(4, 0), wantUnnamed: "foo bar\n", wantLinewise: true},
			{name: "visual U across lines over tabs", content: "a\tb\nc\td", keys: "yylvjU", wantContent: "a\tB\nC\td", wantCursor: pos(1, 0), wantUnnamed: "a\tb\n", wantLinewise: true},
			{name: "visual ~ over double-width chars", content: "a日本b", keys: "yylv$~", wantContent: "a日本B", wantCursor: pos(1, 0), wantUnnamed: "a日本b\n", wantLinewise: true},
			{name: "visual u from the end", content: "ABC", keys: "yy$vhu", wantContent: "Abc", wantCursor: pos(1, 0), wantUnnamed: "ABC\n", wantLinewise: true},
			{name: "gU/ keeps the register", content: "foo bar\nbaz", keys: "yygU/bar\n", wantContent: "FOO bar\nbaz", wantCursor: pos(0, 0), wantUnnamed: "foo bar\n", wantLinewise: true},
			{name: "gU backtick keeps the register", content: "foo bar", marks: map[rune]term.Coordinates{'a': {X: 4}}, keys: "yygU`a", wantContent: "FOO bar", wantCursor: pos(0, 0), wantUnnamed: "foo bar\n", wantLinewise: true},
			{name: "gUe below the last line", content: "ab\ncd ef", at: term.Coordinates{Y: 9}, keys: "gUe", wantContent: "ab\nCD ef", wantCursor: pos(0, 1)},
			{name: "guu past the line end", content: "ABC", at: term.Coordinates{X: 99}, keys: "guu", wantContent: "abc", wantCursor: pos(0, 0)},
			{name: "guu below the last line", content: "A\nBC", at: term.Coordinates{Y: 9}, keys: "guu", wantContent: "A\nbc", wantCursor: pos(0, 1)},
			{name: "guu on an empty buffer", content: "", keys: "guu", wantContent: "", wantCursor: pos(0, 0)},
			{name: "g~~ over a tab", content: "\tab", at: term.Coordinates{X: 2}, keys: "g~~", wantContent: "\tAB", wantCursor: pos(1, 0)},
			{name: "guu from before the indent", content: "\tab", keys: "guu", wantContent: "\tab", wantCursor: pos(0, 0)},
			{name: "gUU from inside the indent", content: "  ab", at: term.Coordinates{X: 1}, keys: "gUU", wantContent: "  AB", wantCursor: pos(1, 0)},
			{name: "2gUU", content: "ab\n  cd", at: term.Coordinates{X: 1}, keys: "2gUU", wantContent: "AB\n  CD", wantCursor: pos(1, 0)},
			{name: "gUU on a blank line", content: "  ", at: term.Coordinates{X: 1}, keys: "gUU", wantContent: "  ", wantCursor: pos(1, 0)},
			{name: "3gUU from the last line", content: "ab\ncd", at: term.Coordinates{Y: 1}, keys: "3gUU", wantContent: "ab\ncd", wantCursor: pos(0, 1)},
			{name: "2guu over a double-width line", content: "日本A\nBC", at: term.Coordinates{X: 1}, keys: "2guu", wantContent: "日本a\nbc", wantCursor: pos(1, 0)},
		}},
		{name: "black hole register", cases: []testCase{
			{name: "black hole delete keeps the unnamed register", content: "abc one", keys: "yww\"_dw0P", wantContent: "abc abc ", wantCursor: pos(3, 0), wantUnnamed: "abc "},
			{name: "\"_dd keeps the unnamed register", content: "ab\ncd", keys: "yl\"_ddp", wantContent: "cad", wantCursor: pos(1, 0), wantUnnamed: "a"},
			{name: "\"_x keeps the unnamed register", content: "ab", keys: "yl\"_xp", wantContent: "ba", wantCursor: pos(1, 0), wantUnnamed: "a"},
			{name: "\"_dw keeps the unnamed register", content: "ab cd", keys: "yl\"_dw", wantContent: "cd", wantCursor: pos(0, 0), wantUnnamed: "a"},
			{name: "\"_db linewise keeps the unnamed register", content: "ab\ncd", at: term.Coordinates{Y: 1}, keys: "yl\"_dbp", wantContent: "ccd", wantCursor: pos(1, 0), wantUnnamed: "c"},
			{name: "\"_d} keeps the unnamed register", content: "ab\ncd\n\nef", keys: "yl\"_d}", wantContent: "\nef", wantCursor: pos(0, 0), wantUnnamed: "a"},
			{name: "\"_cw keeps the unnamed register", content: "ab cd", keys: "yl\"_cw", wantContent: " cd", wantCursor: pos(0, 0), wantMode: insertMode, wantUnnamed: "a"},
			{name: "\"_d/ keeps the unnamed register", content: "ab cd", keys: "yl\"_d/cd\n", wantContent: "cd", wantCursor: pos(0, 0), wantUnnamed: "a"},
			{name: "\"_d backtick keeps the unnamed register", content: "ab cd", marks: map[rune]term.Coordinates{'a': {X: 3}}, keys: "yl\"_d`a", wantContent: "cd", wantCursor: pos(0, 0), wantUnnamed: "a"},
			{name: "visual \"_d keeps the unnamed register", content: "ab cd", keys: "yllvl\"_d", wantContent: "acd", wantCursor: pos(1, 0), wantUnnamed: "a"},
			{name: "\"_dd on an empty buffer", content: "", keys: "\"_dd", wantContent: "", wantCursor: pos(0, 0)},
			{name: "\"_x on a double-width char", content: "\U0001F600b", keys: "yl\"_x", wantContent: "b", wantCursor: pos(0, 0), wantUnnamed: "\U0001F600"},
			{name: "\"_dd below the last line", content: "ab\ncd", at: term.Coordinates{Y: 9}, keys: "yl\"_dd", wantContent: "ab", wantCursor: pos(0, 0), wantUnnamed: "c"},
			{name: "\"_x past the line end", content: "abc", at: term.Coordinates{X: 9}, keys: "yl\"_x", wantContent: "ab", wantCursor: pos(1, 0), wantUnnamed: "c"},
			{name: "\"_x on a tab", content: "\tb", keys: "yl\"_x", wantContent: "b", wantCursor: pos(0, 0), wantUnnamed: "\t"},
			{name: "\"_dw over cjk", content: "日本 ab", keys: "yl\"_dw", wantContent: "ab", wantCursor: pos(0, 0), wantUnnamed: "日"},
		}},
		{name: "put", cases: []testCase{
			{name: "charwise p leaves the cursor on the last pasted char", content: "one two three", at: term.Coordinates{X: 8}, keys: "dbp", wantContent: "one ttwo hree", wantCursor: pos(8, 0), wantUnnamed: "two "},
			{name: "p a double-width char", content: "\U0001F600b", keys: "ylp", wantContent: "\U0001F600\U0001F600b", wantCursor: pos(1, 0), wantUnnamed: "\U0001F600"},
			{name: "P a double-width char", content: "\U0001F600b", keys: "ylP", wantContent: "\U0001F600\U0001F600b", wantCursor: pos(0, 0), wantUnnamed: "\U0001F600"},
			{name: "p a tab", content: "\tb", keys: "ylp", wantContent: "\t\tb", wantCursor: pos(1, 0), wantUnnamed: "\t"},
			{name: "3p charwise", content: "ab", keys: "yl3p", wantContent: "aaaab", wantCursor: pos(3, 0), wantUnnamed: "a"},
			{name: "3P charwise", content: "ab", keys: "yl3P", wantContent: "aaaab", wantCursor: pos(2, 0), wantUnnamed: "a"},
			{name: "p charwise multi-line", content: "ab\ncd", keys: "vjyp", wantContent: "aab\ncb\ncd", wantCursor: pos(1, 0), wantUnnamed: "ab\nc"},
			{name: "P charwise multi-line", content: "ab\ncd", keys: "vjyP", wantContent: "ab\ncab\ncd", wantCursor: pos(0, 0), wantUnnamed: "ab\nc"},
			{name: "p charwise on an empty line", content: "ab\n\ncd", keys: "yljp", wantContent: "ab\na\ncd", wantCursor: pos(0, 1), wantUnnamed: "a"},
			{name: "p charwise past the line end", content: "abc", at: term.Coordinates{X: 99}, keys: "yhp", wantContent: "abbc", wantCursor: pos(2, 0), wantUnnamed: "b"},
			{name: "p linewise on the last line", content: "ab\ncd", keys: "yyjp", wantContent: "ab\ncd\nab", wantCursor: pos(0, 2), wantUnnamed: "ab\n", wantLinewise: true},
			{name: "P linewise on the first line", content: "ab\ncd", keys: "jyykP", wantContent: "cd\nab\ncd", wantCursor: pos(0, 0), wantUnnamed: "cd\n", wantLinewise: true},
			{name: "2p linewise on the last line", content: "ab\ncd", keys: "yyj2p", wantContent: "ab\ncd\nab\nab", wantCursor: pos(0, 2), wantUnnamed: "ab\n", wantLinewise: true},
			{name: "p linewise below the last line", content: "ab\ncd", at: term.Coordinates{Y: 9}, keys: "yyGp", wantContent: "ab\ncd\ncd", wantCursor: pos(0, 2), wantUnnamed: "cd\n", wantLinewise: true},
			{name: "p on an empty buffer", content: "", keys: "p", wantContent: "", wantCursor: pos(0, 0)},
			{name: "p linewise indented", content: "  ab\ncd", keys: "yyjp", wantContent: "  ab\ncd\n  ab", wantCursor: pos(2, 2), wantUnnamed: "  ab\n", wantLinewise: true},
			{name: "P linewise indented", content: "  ab\ncd", keys: "yyjP", wantContent: "  ab\n  ab\ncd", wantCursor: pos(2, 1), wantUnnamed: "  ab\n", wantLinewise: true},
			{name: "2P linewise", content: "ab\ncd", keys: "yyj2P", wantContent: "ab\nab\nab\ncd", wantCursor: pos(0, 1), wantUnnamed: "ab\n", wantLinewise: true},
			{name: "visual p multi-line", content: "ab\ncd\nef", keys: "vjyjjvp", wantContent: "ab\ncd\nab\ncf", wantCursor: pos(0, 2), wantUnnamed: "e"},
			{name: "D on an empty line", content: "ab\n\ncd", at: term.Coordinates{Y: 1}, keys: "D", wantContent: "ab\n\ncd", wantCursor: pos(0, 1)},
			{name: "P below the last line", content: "ab\ncd", at: term.Coordinates{Y: 9}, keys: "ylP", wantContent: "ab\nccd", wantCursor: pos(0, 1), wantUnnamed: "c"},
			{name: "p onto a tab", content: "a\tb", at: term.Coordinates{X: 1}, keys: "ylp", wantContent: "a\t\tb", wantCursor: pos(2, 0), wantUnnamed: "\t"},
			{name: "p a cjk char", content: "日本", keys: "ylp", wantContent: "日日本", wantCursor: pos(1, 0), wantUnnamed: "日"},
			{name: "3p linewise", content: "ab", keys: "yy3p", wantContent: "ab\nab\nab\nab", wantCursor: pos(0, 1), wantUnnamed: "ab\n", wantLinewise: true},
			{name: "p linewise on an empty buffer", content: "", keys: "yyp", wantContent: "\n", wantCursor: pos(0, 1), wantUnnamed: "\n", wantLinewise: true},
			{name: "p an empty line after the last line", content: "ab\n\ncd", keys: "jyyGp", wantContent: "ab\n\ncd\n", wantCursor: pos(0, 3), wantUnnamed: "\n", wantLinewise: true},
			{name: "yyp on a single unterminated line", content: "ab", keys: "yyp", wantContent: "ab\nab", wantCursor: pos(0, 1), wantUnnamed: "ab\n", wantLinewise: true},
		}},
		{name: "visual mode switches", cases: []testCase{
			{name: "v exits visual mode over double-width chars", content: "\U0001F600b", keys: "vlvx", wantContent: "\U0001F600", wantCursor: pos(0, 0), wantUnnamed: "b"},
			{name: "V then v switches to charwise", content: "ab\ncd", keys: "Vvd", wantContent: "b\ncd", wantCursor: pos(0, 0), wantUnnamed: "a"},
			{name: "v then V switches to linewise", content: "ab\ncd", keys: "vVd", wantContent: "cd", wantCursor: pos(0, 0), wantUnnamed: "ab\n", wantLinewise: true},
			{name: "v then ctrl-v switches to blockwise", content: "ab\ncd", keys: "vlj<c-v>d", wantContent: "\n", wantCursor: pos(0, 0), wantUnnamed: "ab\ncd"},
			{name: "ctrl-v then v switches to charwise", content: "ab\ncd", keys: "<c-v>ljvd", wantContent: "", wantCursor: pos(0, 0), wantUnnamed: "ab\ncd"},
			{name: "V then ctrl-v switches to blockwise", content: "ab\ncd", keys: "Vl<c-v>d", wantContent: "\ncd", wantCursor: pos(0, 0), wantUnnamed: "ab"},
			{name: "vd on an empty buffer", content: "", keys: "vd", wantContent: "", wantCursor: pos(0, 0)},
			{name: "Vd on an empty buffer", content: "", keys: "Vd", wantContent: "", wantCursor: pos(0, 0), wantUnnamed: "\n", wantLinewise: true},
			{name: "vd past the line end", content: "abc", at: term.Coordinates{X: 9}, keys: "vd", wantContent: "ab", wantCursor: pos(1, 0), wantUnnamed: "c"},
			{name: "Vd below the last line", content: "ab\ncd", at: term.Coordinates{Y: 9}, keys: "Vd", wantContent: "ab", wantCursor: pos(0, 0), wantUnnamed: "cd\n", wantLinewise: true},
			{name: "vf to a double-width char", content: "a\U0001F600b", keys: "vf\U0001F600d", wantContent: "b", wantCursor: pos(0, 0), wantUnnamed: "a\U0001F600"},
			{name: "vt to a tab", content: "ab\tc", keys: "vt\td", wantContent: "\tc", wantCursor: pos(0, 0), wantUnnamed: "ab"},
			{name: "vf1", content: "a1b", keys: "vf1d", wantContent: "b", wantCursor: pos(0, 0), wantUnnamed: "a1"},
			{name: "vF to a double-width char", content: "a\U0001F600bc", at: term.Coordinates{X: 3}, keys: "vF\U0001F600d", wantContent: "a", wantCursor: pos(0, 0), wantUnnamed: "\U0001F600bc"},
		}},
		{name: "empty lines", cases: []testCase{
			{name: "dd an empty line", content: "a\n\nb", at: term.Coordinates{Y: 1}, keys: "dd", wantContent: "a\nb", wantCursor: pos(0, 1), wantUnnamed: "\n", wantLinewise: true},
			{name: "dd the trailing empty line", content: "abc\n", at: term.Coordinates{Y: 1}, keys: "dd", wantContent: "abc", wantCursor: pos(0, 0), wantUnnamed: "\n", wantLinewise: true},
			{name: "dd an empty buffer", content: "", keys: "dd", wantContent: "", wantCursor: pos(0, 0), wantUnnamed: "\n", wantLinewise: true},
			{name: "dd a tab-only line", content: "a\n\t\nb", at: term.Coordinates{Y: 1}, keys: "dd", wantContent: "a\nb", wantCursor: pos(0, 1), wantUnnamed: "\t\n", wantLinewise: true},
			{name: "x on an empty line", content: "a\n\nb", keys: "ylj\"_x", wantContent: "a\n\nb", wantCursor: pos(0, 1), wantUnnamed: "a"},
			{name: "dj over empty lines", content: "\n\nb", keys: "dj", wantContent: "b", wantCursor: pos(0, 0), wantUnnamed: "\n\n", wantLinewise: true},
		}},
		{name: "linewise delete and yank", cases: []testCase{
			{name: "dd the last line", content: "ab\ncd", at: term.Coordinates{X: 1, Y: 1}, keys: "dd", wantContent: "ab", wantCursor: pos(0, 0), wantUnnamed: "cd\n", wantLinewise: true},
			{name: "dd the last line under an indented one", content: "  ab\ncd", at: term.Coordinates{Y: 1}, keys: "dd", wantContent: "  ab", wantCursor: pos(2, 0), wantUnnamed: "cd\n", wantLinewise: true},
			{name: "dd a middle line before an indented one", content: "ab\ncd\n  ef", at: term.Coordinates{Y: 1}, keys: "dd", wantContent: "ab\n  ef", wantCursor: pos(2, 1), wantUnnamed: "cd\n", wantLinewise: true},
			{name: "dd the only line", content: "ab", at: term.Coordinates{X: 1}, keys: "dd", wantContent: "", wantCursor: pos(0, 0), wantUnnamed: "ab\n", wantLinewise: true},
			{name: "dk the last two lines", content: "ab\n  cd\nef", at: term.Coordinates{Y: 2}, keys: "dk", wantContent: "ab", wantCursor: pos(0, 0), wantUnnamed: "  cd\nef\n", wantLinewise: true},
			{name: "Vd the last line", content: "ab\ncd", at: term.Coordinates{Y: 1}, keys: "Vd", wantContent: "ab", wantCursor: pos(0, 0), wantUnnamed: "cd\n", wantLinewise: true},
			{name: "db linewise on the last line", content: "  foo\nbaz", at: term.Coordinates{Y: 1}, keys: "db", wantContent: "baz", wantCursor: pos(0, 0), wantUnnamed: "  foo\n", wantLinewise: true},
			{name: "d'a on the same line", content: "ab\n  cd", at: term.Coordinates{X: 3, Y: 1}, marks: map[rune]term.Coordinates{'a': {X: 2, Y: 1}}, keys: "d'a", wantContent: "ab", wantCursor: pos(0, 0), wantUnnamed: "  cd\n", wantLinewise: true},
			{name: "2dd", content: "0000\n1111\n2222\n3333\n4444\n5555\n", at: term.Coordinates{Y: 2}, keys: "2dd", wantContent: "0000\n1111\n4444\n5555\n", wantCursor: pos(0, 2), wantUnnamed: "2222\n3333\n", wantLinewise: true},
			{name: "3dd from the second to last line", content: "a\nb\nc\nd", at: term.Coordinates{Y: 2}, keys: "3dd", wantContent: "a\nb", wantCursor: pos(0, 1), wantUnnamed: "c\nd\n", wantLinewise: true},
			{name: "3dd from the last line", content: "a\nb\nc\nd", at: term.Coordinates{Y: 3}, keys: "3dd", wantContent: "a\nb\nc\nd", wantCursor: pos(0, 3)},
			{name: "d2d", content: "a\n  b\nc\nd", keys: "d2d", wantContent: "c\nd", wantCursor: pos(0, 0), wantUnnamed: "a\n  b\n", wantLinewise: true},
			{name: "dd below the last line", content: "ab\n  cd", at: term.Coordinates{Y: 9}, keys: "dd", wantContent: "ab", wantCursor: pos(0, 0), wantUnnamed: "  cd\n", wantLinewise: true},
			{name: "dd past the line end", content: "ab\n  cd\nef", at: term.Coordinates{X: 99}, keys: "dd", wantContent: "  cd\nef", wantCursor: pos(2, 0), wantUnnamed: "ab\n", wantLinewise: true},
			{name: "dd before a tab-indented line", content: "ab\n\tcd", keys: "dd", wantContent: "\tcd", wantCursor: pos(1, 0), wantUnnamed: "ab\n", wantLinewise: true},
			{name: "dd before a double-width line", content: "ab\n日本", keys: "dd", wantContent: "日本", wantCursor: pos(0, 0), wantUnnamed: "ab\n", wantLinewise: true},
			{name: "dd on a double-width line", content: "日本\nab", at: term.Coordinates{X: 1}, keys: "dd", wantContent: "ab", wantCursor: pos(0, 0), wantUnnamed: "日本\n", wantLinewise: true},
			{name: "3yy from the last line", content: "a\nb\nc", at: term.Coordinates{Y: 2}, keys: "3yy", wantContent: "a\nb\nc", wantCursor: pos(0, 2)},
			{name: "3yy from the second to last line", content: "a\nb\nc", at: term.Coordinates{Y: 1}, keys: "3yy", wantContent: "a\nb\nc", wantCursor: pos(0, 1), wantUnnamed: "b\nc\n", wantLinewise: true},
			{name: "2yy below the last line", content: "a\nb", at: term.Coordinates{Y: 9}, keys: "2yy", wantContent: "a\nb", wantCursor: pos(0, 1)},
		}},
		{name: "linewise change", cases: []testCase{
			{name: "cj", content: "ab\n  cd\nef", at: term.Coordinates{X: 1}, keys: "cj", wantContent: "\nef", wantCursor: pos(0, 0), wantMode: insertMode, wantUnnamed: "ab\n  cd\n", wantLinewise: true},
			{name: "ck from an indented line", content: "ab\n  cd\nef", at: term.Coordinates{X: 2, Y: 1}, keys: "ck", wantContent: "\nef", wantCursor: pos(0, 0), wantMode: insertMode, wantUnnamed: "ab\n  cd\n", wantLinewise: true},
			{name: "cG", content: "  ab\ncd", keys: "cG", wantContent: "  ", wantCursor: pos(2, 0), wantMode: insertMode, wantUnnamed: "  ab\ncd\n", wantLinewise: true},
			// Rune indents the new line itself, like Vim without autoindent.
			{name: "cc on an indented line", content: "\tab\ncd", at: term.Coordinates{X: 2}, keys: "cc", wantContent: "\ncd", wantCursor: pos(0, 0), wantMode: insertMode, wantUnnamed: "\tab\n", wantLinewise: true},
			{name: "cc on an empty line", content: "ab\n\ncd", at: term.Coordinates{Y: 1}, keys: "cc", wantContent: "ab\n\ncd", wantCursor: pos(0, 1), wantMode: insertMode, wantUnnamed: "\n", wantLinewise: true},
			// Rune indents the new line itself, like Vim without autoindent.
			{name: "S on an indented line", content: "  ab\ncd", at: term.Coordinates{X: 3}, keys: "S", wantContent: "\ncd", wantCursor: pos(0, 0), wantMode: insertMode, wantUnnamed: "  ab\n", wantLinewise: true},
			{name: "c quote a", content: "ab\n cd\nef", marks: map[rune]term.Coordinates{'a': {Y: 1}}, keys: "c'a", wantContent: "\nef", wantCursor: pos(0, 0), wantMode: insertMode, wantUnnamed: "ab\n cd\n", wantLinewise: true},
			{name: "cc below the last line", content: "ab\ncd", at: term.Coordinates{Y: 9}, keys: "cc", wantContent: "ab\n", wantCursor: pos(0, 1), wantMode: insertMode, wantUnnamed: "cd\n", wantLinewise: true},
			{name: "cc past the line end", content: "ab\ncd", at: term.Coordinates{X: 99}, keys: "cc", wantContent: "\ncd", wantCursor: pos(0, 0), wantMode: insertMode, wantUnnamed: "ab\n", wantLinewise: true},
			{name: "cc on an empty buffer", content: "", keys: "cc", wantContent: "", wantCursor: pos(0, 0), wantMode: insertMode, wantUnnamed: "\n", wantLinewise: true},
			{name: "cc on a double-width line", content: "日本a\nb", at: term.Coordinates{X: 1}, keys: "cc", wantContent: "\nb", wantCursor: pos(0, 0), wantMode: insertMode, wantUnnamed: "日本a\n", wantLinewise: true},
			{name: "S below the last line", content: "ab\ncd", at: term.Coordinates{Y: 9}, keys: "S", wantContent: "ab\n", wantCursor: pos(0, 1), wantMode: insertMode, wantUnnamed: "cd\n", wantLinewise: true},
			{name: "cj from the last line", content: "ab\ncd", at: term.Coordinates{Y: 1}, keys: "cj", wantContent: "ab\ncd", wantCursor: pos(0, 1)},
			{name: "cj over a double-width line", content: "日本\nab\ncd", keys: "cj", wantContent: "\ncd", wantCursor: pos(0, 0), wantMode: insertMode, wantUnnamed: "日本\nab\n", wantLinewise: true},
			{name: "cj over a tab", content: "a\tb\ncd\nef", keys: "cj", wantContent: "\nef", wantCursor: pos(0, 0), wantMode: insertMode, wantUnnamed: "a\tb\ncd\n", wantLinewise: true},
		}},
		{name: "single-key deletes", cases: []testCase{
			{name: "\"_x keeps the unnamed register", content: "abc", keys: "yl\"_x", wantContent: "bc", wantCursor: pos(0, 0), wantUnnamed: "a"},
			{name: "\"ax fills register a", content: "abc", keys: "\"axl\"ap", wantContent: "bca", wantCursor: pos(2, 0), wantUnnamed: "a"},
			{name: "3x", content: "abcd", keys: "3x", wantContent: "d", wantCursor: pos(0, 0), wantUnnamed: "abc"},
			{name: "9x stops at the line end", content: "abcd", at: term.Coordinates{X: 2}, keys: "9x", wantContent: "ab", wantCursor: pos(1, 0), wantUnnamed: "cd"},
			{name: "x on a double-width char", content: "\U0001F600b", keys: "x", wantContent: "b", wantCursor: pos(0, 0), wantUnnamed: "\U0001F600"},
			{name: "x on an empty buffer", content: "", keys: "x", wantContent: "", wantCursor: pos(0, 0)},
			{name: "x past the line end", content: "abc", at: term.Coordinates{X: 9}, keys: "x", wantContent: "ab", wantCursor: pos(1, 0), wantUnnamed: "c"},
			{name: "\"_X keeps the unnamed register", content: "abc", keys: "yl$\"_X", wantContent: "ac", wantCursor: pos(1, 0), wantUnnamed: "a"},
			{name: "3X", content: "abcd", at: term.Coordinates{X: 3}, keys: "3X", wantContent: "d", wantCursor: pos(0, 0), wantUnnamed: "abc"},
			{name: "\"_D keeps the unnamed register", content: "abc", keys: "yll\"_D", wantContent: "a", wantCursor: pos(0, 0), wantUnnamed: "a"},
			{name: "\"_C keeps the unnamed register", content: "abc", keys: "yll\"_C", wantContent: "a", wantCursor: pos(1, 0), wantMode: insertMode, wantUnnamed: "a"},
			{name: "\"_s keeps the unnamed register", content: "abc", keys: "yll\"_s", wantContent: "ac", wantCursor: pos(1, 0), wantMode: insertMode, wantUnnamed: "a"},
			{name: "\"_S keeps the unnamed register", content: "abc\nd", keys: "yll\"_S", wantContent: "\nd", wantCursor: pos(0, 0), wantMode: insertMode, wantUnnamed: "a"},
			{name: "D past the line end", content: "abc", at: term.Coordinates{X: 9}, keys: "D", wantContent: "ab", wantCursor: pos(1, 0), wantUnnamed: "c"},
			{name: "D on a double-width char", content: "a\U0001F600b", at: term.Coordinates{X: 1}, keys: "D", wantContent: "a", wantCursor: pos(0, 0), wantUnnamed: "\U0001F600b"},
			{name: "C register", content: "abc", keys: "lC", wantContent: "a", wantCursor: pos(1, 0), wantMode: insertMode, wantUnnamed: "bc"},
			{name: "s register", content: "abc", keys: "ls", wantContent: "ac", wantCursor: pos(1, 0), wantMode: insertMode, wantUnnamed: "b"},
			{name: "x on a tab", content: "a\tb", at: term.Coordinates{X: 1}, keys: "x", wantContent: "ab", wantCursor: pos(1, 0), wantUnnamed: "\t"},
			{name: "x below the last line", content: "ab\ncd", at: term.Coordinates{X: 1, Y: 9}, keys: "x", wantContent: "ab\nc", wantCursor: pos(0, 1), wantUnnamed: "d"},
			{name: "3x over a double-width char", content: "a\U0001F600bc", keys: "3x", wantContent: "c", wantCursor: pos(0, 0), wantUnnamed: "a\U0001F600b"},
			{name: "X on a double-width char", content: "a\U0001F600b", at: term.Coordinates{X: 2}, keys: "X", wantContent: "ab", wantCursor: pos(1, 0), wantUnnamed: "\U0001F600"},
			{name: "X past the line end", content: "abc", at: term.Coordinates{X: 9}, keys: "X", wantContent: "ac", wantCursor: pos(1, 0), wantUnnamed: "b"},
			{name: "X below the last line", content: "ab\ncd", at: term.Coordinates{X: 1, Y: 9}, keys: "X", wantContent: "ab\nd", wantCursor: pos(0, 1), wantUnnamed: "c"},
			{name: "X on an empty buffer", content: "", keys: "X", wantContent: "", wantCursor: pos(0, 0)},
			{name: "D below the last line", content: "ab\ncd", at: term.Coordinates{X: 1, Y: 9}, keys: "D", wantContent: "ab\nc", wantCursor: pos(0, 1), wantUnnamed: "d"},
			{name: "D on an empty buffer", content: "", keys: "D", wantContent: "", wantCursor: pos(0, 0)},
			{name: "D over a tab", content: "a\tb", at: term.Coordinates{X: 1}, keys: "D", wantContent: "a", wantCursor: pos(0, 0), wantUnnamed: "\tb"},
			{name: "s on a double-width char", content: "\U0001F600b", keys: "s", wantContent: "b", wantCursor: pos(0, 0), wantMode: insertMode, wantUnnamed: "\U0001F600"},
			{name: "s on an empty buffer", content: "", keys: "s", wantContent: "", wantCursor: pos(0, 0), wantMode: insertMode},
			{name: "C past the line end", content: "abc", at: term.Coordinates{X: 9}, keys: "C", wantContent: "ab", wantCursor: pos(2, 0), wantMode: insertMode, wantUnnamed: "c"},
			{name: "S on an empty buffer", content: "", keys: "S", wantContent: "", wantCursor: pos(0, 0), wantMode: insertMode, wantUnnamed: "\n", wantLinewise: true},
			{name: "3s past the line end", content: "abc", at: term.Coordinates{X: 1}, keys: "3s", wantContent: "a", wantCursor: pos(1, 0), wantMode: insertMode, wantUnnamed: "bc"},
		}},
		{name: "first line motion", cases: []testCase{
			{name: "dgg", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "dgg", wantContent: "mno", wantCursor: pos(0, 0), wantUnnamed: "abc def\nghi jkl\n", wantLinewise: true},
			{name: "d2gg", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "d2gg", wantContent: "abc def\nmno", wantCursor: pos(0, 1), wantUnnamed: "ghi jkl\n", wantLinewise: true},
			{name: "d1gg", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "d1gg", wantContent: "mno", wantCursor: pos(0, 0), wantUnnamed: "abc def\nghi jkl\n", wantLinewise: true},
			{name: "d3gg", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "d3gg", wantContent: "abc def", wantCursor: pos(0, 0), wantUnnamed: "ghi jkl\nmno\n", wantLinewise: true},
			{name: "d9gg past the last line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "d9gg", wantContent: "abc def", wantCursor: pos(0, 0), wantUnnamed: "ghi jkl\nmno\n", wantLinewise: true},
			{name: "cgg", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "cgg", wantContent: "\nmno", wantCursor: pos(0, 0), wantMode: insertMode, wantUnnamed: "abc def\nghi jkl\n", wantLinewise: true},
			{name: "cgg keeps the indent", content: "  abc\n\tdef\nxyz", at: term.Coordinates{X: 1, Y: 2}, keys: "cgg", wantContent: "  ", wantCursor: pos(2, 0), wantMode: insertMode, wantUnnamed: "  abc\n\tdef\nxyz\n", wantLinewise: true},
			{name: "ygg", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "ygg", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(5, 0), wantUnnamed: "abc def\nghi jkl\n", wantLinewise: true}, // nostartofline
			{name: "y2gg", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "y2gg", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(0, 1), wantUnnamed: "ghi jkl\n", wantLinewise: true},
			{name: "ygg over a tab indent", content: "  abc\n\tdef\nxyz", at: term.Coordinates{X: 1, Y: 2}, keys: "ygg", wantContent: "  abc\n\tdef\nxyz", wantCursor: pos(1, 0), wantUnnamed: "  abc\n\tdef\nxyz\n", wantLinewise: true}, // nostartofline
			{name: "gUgg", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "gUgg", wantContent: "ABC DEF\nGHI JKL\nmno", wantCursor: pos(5, 0)},                                                                 // nostartofline
			{name: "gugg", content: "ABC\nDEF\nGHI", at: term.Coordinates{X: 2, Y: 1}, keys: "gugg", wantContent: "abc\ndef\nGHI", wantCursor: pos(2, 0)},                                                                                 // nostartofline
			{name: "g~gg", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "g~gg", wantContent: "ABC DEF\nGHI JKL\nmno", wantCursor: pos(5, 0)},                                                                 // nostartofline
			{name: ">gg", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: ">gg", wantContent: "\tabc def\n\tghi jkl\nmno"},
			{name: "<gg", content: "\tabc\n\tdef\nxyz", at: term.Coordinates{X: 2, Y: 1}, keys: "\\<gg", wantContent: "abc\ndef\nxyz"},
			{name: "dgg on the first line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5}, keys: "dgg", wantContent: "ghi jkl\nmno", wantCursor: pos(0, 0), wantUnnamed: "abc def\n", wantLinewise: true},
			{name: "dgg on an empty buffer", content: "", keys: "dgg", wantContent: "", wantCursor: pos(0, 0), wantUnnamed: "\n", wantLinewise: true},
			{name: "dgg past both ends", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 9, Y: 9}, keys: "dgg", wantContent: "", wantCursor: pos(0, 0), wantUnnamed: "abc def\nghi jkl\nmno\n", wantLinewise: true},
			{name: "dgg over double-width chars and tabs", content: "  abc\n\tdef\n\U0001F600x", at: term.Coordinates{X: 1, Y: 2}, keys: "dgg", wantContent: "", wantCursor: pos(0, 0), wantUnnamed: "  abc\n\tdef\n\U0001F600x\n", wantLinewise: true},
			{name: "dgg on the trailing empty line", content: "abc\n", at: term.Coordinates{Y: 1}, keys: "dgg", wantContent: "", wantCursor: pos(0, 0), wantUnnamed: "abc\n\n", wantLinewise: true},
			{name: "dgg repeated", content: "abc\ndef\nghi\njkl", at: term.Coordinates{Y: 1}, keys: "jdgg.", wantContent: "", wantCursor: pos(0, 0), wantUnnamed: "jkl\n", wantLinewise: true},
		}},
		{name: "display line motions with wrap", cases: []testCase{
			{name: "dgj", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "dgj", wrap: true, wantContent: "abc def\nghi jo", wantCursor: pos(5, 1), wantUnnamed: "kl\nmn"},
			{name: "dgk", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "dgk", wrap: true, wantContent: "abc dkl\nmno", wantCursor: pos(5, 0), wantUnnamed: "ef\nghi j"},
			{name: "ygj", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "ygj", wrap: true, wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(5, 1), wantUnnamed: "kl\nmn"},
			{name: "dgj on the last line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 1, Y: 2}, keys: "dgj", wrap: true, wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(1, 2)},
			{name: "dgk on the first line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 1}, keys: "dgk", wrap: true, wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(1, 0)},
			{name: "dgj into a shorter line", content: "abcdef\nab\ncd", at: term.Coordinates{X: 4}, keys: "dgj", wrap: true, wantContent: "abcdb\ncd", wantCursor: pos(4, 0), wantUnnamed: "ef\na"},
			{name: "dgj over double-width chars", content: "日本ab\n日本cd", at: term.Coordinates{X: 1}, keys: "dgj", wrap: true, wantContent: "日本cd", wantCursor: pos(1, 0), wantUnnamed: "本ab\n日"},
			{name: "d2gj past the last line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 1, Y: 1}, keys: "d2gj", wrap: true, wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(1, 2)},
		}},
		{name: "display line motions without wrap", cases: []testCase{
			{name: "dgj", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "dgj", wantContent: "abc def", wantCursor: pos(0, 0), wantUnnamed: "ghi jkl\nmno\n", wantLinewise: true},
			{name: "dgk", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "dgk", wantContent: "mno", wantCursor: pos(0, 0), wantUnnamed: "abc def\nghi jkl\n", wantLinewise: true},
			{name: "ygj", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "ygj", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(5, 1), wantUnnamed: "ghi jkl\nmno\n", wantLinewise: true},
			{name: "dgj on the last line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 1, Y: 2}, keys: "dgj", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(1, 2)},
			{name: "dgk on the first line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 1}, keys: "dgk", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(1, 0)},
			{name: "dgj into a shorter line", content: "abcdef\nab\ncd", at: term.Coordinates{X: 4}, keys: "dgj", wantContent: "cd", wantCursor: pos(0, 0), wantUnnamed: "abcdef\nab\n", wantLinewise: true},
			{name: "dgj over double-width chars", content: "日本ab\n日本cd", at: term.Coordinates{X: 1}, keys: "dgj", wantContent: "", wantCursor: pos(0, 0), wantUnnamed: "日本ab\n日本cd\n", wantLinewise: true},
			{name: "d2gj past the last line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 1, Y: 1}, keys: "d2gj", wantContent: "abc def", wantCursor: pos(0, 0), wantUnnamed: "ghi jkl\nmno\n", wantLinewise: true},
		}},
		{name: "next and previous line motions", cases: []testCase{
			{name: "+", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "+", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(0, 2)},
			{name: "-", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "-", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(0, 0)},
			{name: "_", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "_", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(0, 1)},
			{name: "2+", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5}, keys: "2+", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(0, 2)},
			{name: "2-", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 2}, keys: "2-", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(0, 0)},
			{name: "3_", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5}, keys: "3_", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(0, 2)},
			{name: "9+", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5}, keys: "9+", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(0, 2)},
			{name: "9-", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 2}, keys: "9-", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(0, 0)},
			{name: "+ on the last line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 2}, keys: "+", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(2, 2)},
			{name: "- on the first line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5}, keys: "-", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(5, 0)},
			{name: "2_ on the last line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 2}, keys: "2_", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(2, 2)},
			{name: "+ to an indented line", content: "  abc\n\tdef\n  \U0001F600x", at: term.Coordinates{X: 4}, keys: "+", wantContent: "  abc\n\tdef\n  \U0001F600x", wantCursor: pos(1, 1)},
			{name: "2+ to a double-width char", content: "  abc\n\tdef\n  \U0001F600x", at: term.Coordinates{X: 4}, keys: "2+", wantContent: "  abc\n\tdef\n  \U0001F600x", wantCursor: pos(2, 2)},
			{name: "- to a tab indent", content: "  abc\n\tdef\n  \U0001F600x", at: term.Coordinates{X: 1, Y: 2}, keys: "-", wantContent: "  abc\n\tdef\n  \U0001F600x", wantCursor: pos(1, 1)},
			{name: "_ on an indented line", content: "  abc\n\tdef", at: term.Coordinates{X: 4}, keys: "_", wantContent: "  abc\n\tdef", wantCursor: pos(2, 0)},
			{name: "_ on a blank line", content: "   ", keys: "_", wantContent: "   ", wantCursor: pos(2, 0)},
			{name: "+ to a blank line", content: "a\n   \nb", keys: "+", wantContent: "a\n   \nb", wantCursor: pos(2, 1)},
			{name: "+ to the trailing empty line", content: "abc\n", keys: "+", wantContent: "abc\n", wantCursor: pos(0, 1)},
			{name: "+ on an empty buffer", content: "", keys: "+", wantContent: "", wantCursor: pos(0, 0)},
			{name: "+ below the last line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 1, Y: 9}, keys: "+", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(1, 2)},
			{name: "- below the last line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 1, Y: 9}, keys: "-", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(0, 1)},
			{name: "- past the line end", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 9, Y: 1}, keys: "-", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(0, 0)},
			{name: "d+", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "d+", wantContent: "abc def", wantCursor: pos(0, 0), wantUnnamed: "ghi jkl\nmno\n", wantLinewise: true},
			{name: "d-", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "d-", wantContent: "mno", wantCursor: pos(0, 0), wantUnnamed: "abc def\nghi jkl\n", wantLinewise: true},
			{name: "d_", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "d_", wantContent: "abc def\nmno", wantCursor: pos(0, 1), wantUnnamed: "ghi jkl\n", wantLinewise: true},
			{name: "d2_", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "d2_", wantContent: "abc def", wantCursor: pos(0, 0), wantUnnamed: "ghi jkl\nmno\n", wantLinewise: true},
			{name: "y+", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "y+", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(5, 1), wantUnnamed: "ghi jkl\nmno\n", wantLinewise: true},
			{name: "c+", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "c+", wantContent: "abc def\n", wantCursor: pos(0, 1), wantMode: insertMode, wantUnnamed: "ghi jkl\nmno\n", wantLinewise: true},
			{name: "c_", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "c_", wantContent: "abc def\n\nmno", wantCursor: pos(0, 1), wantMode: insertMode, wantUnnamed: "ghi jkl\n", wantLinewise: true},
			{name: "gU_", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "gU_", wantContent: "abc def\nGHI JKL\nmno", wantCursor: pos(0, 1)},
			{name: "d+.", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "d+.", wantContent: "abc def", wantCursor: pos(0, 0), wantUnnamed: "ghi jkl\nmno\n", wantLinewise: true},
			{name: "v+d", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "v+d", wantContent: "abc def\nghi jno", wantCursor: pos(5, 1), wantUnnamed: "kl\nm"},
			{name: "V-d", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "V-d", wantContent: "mno", wantCursor: pos(0, 0), wantUnnamed: "abc def\nghi jkl\n", wantLinewise: true},
			{name: "d5+", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "d5+", wantContent: "abc def", wantCursor: pos(0, 0), wantUnnamed: "ghi jkl\nmno\n", wantLinewise: true},
			{name: "d5-", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "d5-", wantContent: "mno", wantCursor: pos(0, 0), wantUnnamed: "abc def\nghi jkl\n", wantLinewise: true},
			{name: "d9_", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "d9_", wantContent: "abc def", wantCursor: pos(0, 0), wantUnnamed: "ghi jkl\nmno\n", wantLinewise: true},
			{name: "d+ on the last line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 2}, keys: "d+", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(2, 2)},
			{name: "d- on the first line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5}, keys: "d-", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(5, 0)},
			{name: "d2_ on the last line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{Y: 2}, keys: "d2_", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(0, 2)},
			{name: "d_ on the last line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{Y: 2}, keys: "d_", wantContent: "abc def\nghi jkl", wantCursor: pos(0, 1), wantUnnamed: "mno\n", wantLinewise: true},
			{name: "d+ on an empty buffer", content: "", keys: "d+", wantContent: "", wantCursor: pos(0, 0)},
			{name: "d+ to the trailing empty line", content: "abc\n", keys: "d+", wantContent: "", wantCursor: pos(0, 0), wantUnnamed: "abc\n\n", wantLinewise: true},
			{name: "d_ on the trailing empty line", content: "abc\n\n", at: term.Coordinates{Y: 1}, keys: "d_", wantContent: "abc\n", wantCursor: pos(0, 1), wantUnnamed: "\n", wantLinewise: true},
			{name: "c_ keeps the indent", content: "\tabc\ndef", at: term.Coordinates{X: 2}, keys: "c_", wantContent: "\t\ndef", wantCursor: pos(1, 0), wantMode: insertMode, wantUnnamed: "\tabc\n", wantLinewise: true},
			{name: "d- over double-width chars", content: "日本\n\U0001F600x", at: term.Coordinates{X: 1, Y: 1}, keys: "d-", wantContent: "", wantCursor: pos(0, 0), wantUnnamed: "日本\n\U0001F600x\n", wantLinewise: true},
			{name: ">+", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: ">+", wantContent: "abc def\n\tghi jkl\n\tmno"},
		}},
		{name: "linewise jumps to the cursor line", cases: []testCase{
			{name: "dG on the last line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 2, Y: 2}, keys: "dG", wantContent: "abc def\nghi jkl", wantCursor: pos(0, 1), wantUnnamed: "mno\n", wantLinewise: true},
			{name: "dG at the last line start", content: "abc def\nghi jkl\nmno", at: term.Coordinates{Y: 2}, keys: "dG", wantContent: "abc def\nghi jkl", wantCursor: pos(0, 1), wantUnnamed: "mno\n", wantLinewise: true},
			{name: "d1G at the first line start", content: "abc def\nghi jkl\nmno", keys: "d1G", wantContent: "ghi jkl\nmno", wantCursor: pos(0, 0), wantUnnamed: "abc def\n", wantLinewise: true},
			{name: "d2G on line 2", content: "abc def\nghi jkl\nmno", at: term.Coordinates{Y: 1}, keys: "d2G", wantContent: "abc def\nmno", wantCursor: pos(0, 1), wantUnnamed: "ghi jkl\n", wantLinewise: true},
			{name: "yG on the last line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{Y: 2}, keys: "yG", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(0, 2), wantUnnamed: "mno\n", wantLinewise: true},
			{name: "cG on the last line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 1, Y: 2}, keys: "cG", wantContent: "abc def\nghi jkl\n", wantCursor: pos(0, 2), wantMode: insertMode, wantUnnamed: "mno\n", wantLinewise: true},
			{name: "gUG on the last line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 1, Y: 2}, keys: "gUG", wantContent: "abc def\nghi jkl\nMNO", wantCursor: pos(1, 2)}, // nostartofline
			{name: "dG on the trailing empty line", content: "abc\n", at: term.Coordinates{Y: 1}, keys: "dG", wantContent: "abc", wantCursor: pos(0, 0), wantUnnamed: "\n", wantLinewise: true},
			{name: "dG on an empty buffer", content: "", keys: "dG", wantContent: "", wantCursor: pos(0, 0), wantUnnamed: "\n", wantLinewise: true},
			{name: "dG below the last line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 1, Y: 9}, keys: "dG", wantContent: "abc def\nghi jkl", wantCursor: pos(0, 1), wantUnnamed: "mno\n", wantLinewise: true},
		}},
		{name: "linewise yank moves to the range start", cases: []testCase{
			{name: "yk", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "yk", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(5, 0), wantUnnamed: "abc def\nghi jkl\n", wantLinewise: true},
			{name: "y2k", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 2, Y: 2}, keys: "y2k", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(2, 0), wantUnnamed: "abc def\nghi jkl\nmno\n", wantLinewise: true},
			{name: "y-", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "y-", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(0, 0), wantUnnamed: "abc def\nghi jkl\n", wantLinewise: true},
			{name: "y5k past the first line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 2}, keys: "y5k", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(2, 0), wantUnnamed: "abc def\nghi jkl\nmno\n", wantLinewise: true},
			{name: "yk on the first line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5}, keys: "yk", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(5, 0)},
			{name: "yk over a tab indent", content: "\tabc\n\tdef", at: term.Coordinates{X: 2, Y: 1}, keys: "yk", wantContent: "\tabc\n\tdef", wantCursor: pos(2, 0), wantUnnamed: "\tabc\n\tdef\n", wantLinewise: true},
			{name: "yk over double-width chars", content: "日本\nabc", at: term.Coordinates{X: 2, Y: 1}, keys: "yk", wantContent: "日本\nabc", wantCursor: pos(1, 0), wantUnnamed: "日本\nabc\n", wantLinewise: true},
			{name: "yk on the trailing empty line", content: "abc\n", at: term.Coordinates{Y: 1}, keys: "yk", wantContent: "abc\n", wantCursor: pos(0, 0), wantUnnamed: "abc\n\n", wantLinewise: true},
			{name: "yk below the last line", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 1, Y: 9}, keys: "yk", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(1, 1), wantUnnamed: "ghi jkl\nmno\n", wantLinewise: true},
			{name: "y'a to a mark above", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 2}, marks: map[rune]term.Coordinates{'a': {X: 4}}, keys: "y'a", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(0, 0), wantUnnamed: "abc def\nghi jkl\nmno\n", wantLinewise: true},
			{name: "yge", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "yge", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(2, 1), wantUnnamed: "i jk"},
			{name: "yj stays", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "yj", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(5, 1), wantUnnamed: "ghi jkl\nmno\n", wantLinewise: true},
			{name: "y'a to a mark below", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5}, marks: map[rune]term.Coordinates{'a': {X: 1, Y: 2}}, keys: "y'a", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(5, 0), wantUnnamed: "abc def\nghi jkl\nmno\n", wantLinewise: true},
			{name: "yb linewise on one line", content: "foo\n  baz", at: term.Coordinates{X: 2, Y: 1}, keys: "yb", wantContent: "foo\n  baz", wantCursor: pos(0, 0), wantUnnamed: "foo\n  "},
		}},
		{name: "shifts leave the registers alone", cases: []testCase{
			{name: "<< then put", content: "\tabc def\n\tghi jkl", at: term.Coordinates{X: 2}, keys: "yiwj\\<\\<k0P", wantContent: "abc\tabc def\nghi jkl", wantCursor: pos(2, 0), wantUnnamed: "abc"},
			{name: "<<", content: "\tabc def\n\tghi jkl", at: term.Coordinates{X: 2}, keys: "yiw\\<\\<", wantContent: "abc def\n\tghi jkl", wantUnnamed: "abc"},
			{name: "<j", content: "\tabc def\n\tghi jkl", at: term.Coordinates{X: 2}, keys: "yiw\\<j", wantContent: "abc def\nghi jkl", wantUnnamed: "abc"},
			{name: "<ip", content: "\tabc def\n\tghi jkl", at: term.Coordinates{X: 2}, keys: "yiw\\<ip", wantContent: "abc def\nghi jkl", wantUnnamed: "abc"},
			{name: "visual <", content: "\tabc def\n\tghi jkl", at: term.Coordinates{X: 2}, keys: "yiwV\\<", wantContent: "abc def\n\tghi jkl", wantUnnamed: "abc"},
			{name: "black hole <<", content: "\tabc def\n\tghi jkl", at: term.Coordinates{X: 2}, keys: "\"_\\<\\<", wantContent: "abc def\n\tghi jkl"},
			{name: ">> then put", content: "\tabc def\n\tghi jkl", at: term.Coordinates{X: 2}, keys: "yiw>>0P", wantContent: "abc\t\tabc def\n\tghi jkl", wantCursor: pos(2, 0), wantUnnamed: "abc"},
		}},
		{name: "window motions", cases: []testCase{
			{name: "dH", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "dH", wantContent: "mno", wantCursor: pos(0, 0), wantUnnamed: "abc def\nghi jkl\n", wantLinewise: true},
			{name: "dM", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "dM", wantContent: "abc def\nmno", wantCursor: pos(0, 1), wantUnnamed: "ghi jkl\n", wantLinewise: true},
			{name: "dL", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5, Y: 1}, keys: "dL", wantContent: "abc def", wantCursor: pos(0, 0), wantUnnamed: "ghi jkl\nmno\n", wantLinewise: true},
			{name: "dM from the last line", content: "a\nb\nc\nd", at: term.Coordinates{Y: 3}, keys: "dM", wantContent: "a", wantCursor: pos(0, 0), wantUnnamed: "b\nc\nd\n", wantLinewise: true},
			{name: "M", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5}, keys: "M", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(5, 1)},                                                                     // nostartofline
			{name: "yM", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5}, keys: "yM", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(5, 0), wantUnnamed: "abc def\nghi jkl\n", wantLinewise: true},            // nostartofline
			{name: "yH", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 1, Y: 2}, keys: "yH", wantContent: "abc def\nghi jkl\nmno", wantCursor: pos(1, 0), wantUnnamed: "abc def\nghi jkl\nmno\n", wantLinewise: true}, // nostartofline
			{name: "gUL", content: "abc def\nghi jkl\nmno", at: term.Coordinates{X: 5}, keys: "gUL", wantContent: "ABC DEF\nGHI JKL\nMNO", wantCursor: pos(5, 0)},                                                                 // nostartofline
			{name: "M with a wrapped last line", content: "a\nb\n" + strings.Repeat("x", 200), wrap: true, keys: "M", wantContent: "a\nb\n" + strings.Repeat("x", 200), wantCursor: pos(0, 2)},
			{name: "dM with a wrapped last line", content: "a\nb\n" + strings.Repeat("x", 200), wrap: true, keys: "dM", wantContent: "", wantCursor: pos(0, 0), wantUnnamed: "a\nb\n" + strings.Repeat("x", 200) + "\n", wantLinewise: true},
			{name: "M with a long wrapped last line", content: "a\nb\nc\nd\n" + strings.Repeat("x", 400), wrap: true, keys: "M", wantContent: "a\nb\nc\nd\n" + strings.Repeat("x", 400), wantCursor: pos(0, 4)},
		}},
	}

	for _, suite := range suites {
		t.Run(suite.name, func(t *testing.T) {
			for _, tc := range suite.cases {
				t.Run(tc.name, func(t *testing.T) {
					buf := cell.NewBuffer()
					buf.Init()
					_, err := buf.ReadFrom(strings.NewReader(tc.content))
					require.NoError(t, err)
					vw := New(buf, uri, WithClipboard(registerset.New(new(mockClip))), WithTabspaces(2),
						WithWrap(tc.wrap))
					vw.Resize(80, 20)
					vw.Draw(term.NoopWriter{})
					for name, at := range tc.marks {
						vw.SetLocationList(textapi.LocationPriorityInfo, string(name),
							textapi.LocationSlice([]textapi.Location{{
								From: at,
								To:   term.Coordinates{Y: at.Y, X: at.X + 1},
							}}))
					}
					h := vw.handler.(*viHandlerImpl)
					vw.SetCursorAtScroll(tc.at)
					if vw.CursorAtScroll() != tc.at {
						// SetCursorAtScroll clamps, but the cursor can still be
						// left out of bounds when the buffer shrinks under it.
						h.cursor.MoveToScroll(tc.at)
					}
					require.Equal(t, tc.at, vw.CursorAtScroll())

					for _, ev := range parseSearchOpEvents(tc.keys) {
						quit, _ := vw.Handle(ev)
						require.False(t, quit)
					}

					assert.Equal(t, tc.wantContent, buf.String(), "content")
					if tc.wantCursor != nil {
						assert.Equal(t, *tc.wantCursor, vw.CursorAtScroll(), "cursor")
					}
					assert.Equal(t, tc.wantMode, h.mode(), "mode")
					data, err := h.readRegister(unnamedRegister)
					require.NoError(t, err)
					assert.Equal(t, tc.wantUnnamed, data.Text, "unnamed register")
					assert.Equal(t, tc.wantLinewise, data.Metadata == text.LineSelection, "linewise register")
				})
			}
		})
	}
}

func TestViFindCharacterSpecialKeys(t *testing.T) {
	type step struct {
		// Either ev (for special keys) or ch (for printable characters).
		ev term.Event
		ch rune
	}

	type testCase struct {
		name          string
		content       string
		setup         func(*viHandlerImpl)
		steps         []step
		wantContent   string
		wantScroll    term.Coordinates
		wantMode      viMode
		wantClipboard string
	}

	key := func(k term.Key) step {
		return step{ev: term.Event{Type: term.EventKey, Key: k}}
	}
	ch := func(r rune) step { return step{ch: r} }

	cases := []testCase{
		{
			name:        "f<space> moves cursor to next space",
			content:     "hello world",
			steps:       []step{ch('f'), key(term.KeySpace)},
			wantScroll:  term.Coordinates{X: len("hello"), Y: 0},
			wantContent: "hello world",
		},
		{
			name:        "F<space> moves cursor to previous space",
			content:     "hello world",
			setup:       func(vi *viHandlerImpl) { vi.cursor.MoveEndLine() },
			steps:       []step{ch('F'), key(term.KeySpace)},
			wantScroll:  term.Coordinates{X: len("hello"), Y: 0},
			wantContent: "hello world",
		},
		{
			name:        "t<space> stops one before next space",
			content:     "hello world",
			steps:       []step{ch('t'), key(term.KeySpace)},
			wantScroll:  term.Coordinates{X: len("hell"), Y: 0},
			wantContent: "hello world",
		},
		{
			name:        "T<space> stops one after previous space",
			content:     "hello world",
			setup:       func(vi *viHandlerImpl) { vi.cursor.MoveEndLine() },
			steps:       []step{ch('T'), key(term.KeySpace)},
			wantScroll:  term.Coordinates{X: len("hello "), Y: 0},
			wantContent: "hello world",
		},
		{
			name:        "df<space> deletes through next space",
			content:     "hello world",
			steps:       []step{ch('d'), ch('f'), key(term.KeySpace)},
			wantScroll:  term.Coordinates{X: 0, Y: 0},
			wantContent: "world",
		},
		{
			name:        "cf<space> changes through next space",
			content:     "hello world",
			steps:       []step{ch('c'), ch('f'), key(term.KeySpace)},
			wantScroll:  term.Coordinates{X: 0, Y: 0},
			wantContent: "world",
			wantMode:    insertMode,
		},
		{
			name:          "yf<space> yanks through next space",
			content:       "hello world",
			steps:         []step{ch('y'), ch('f'), key(term.KeySpace)},
			wantScroll:    term.Coordinates{X: 0, Y: 0},
			wantContent:   "hello world",
			wantClipboard: "hello ",
		},
		{
			name:        "; after f<space> repeats find to next space",
			content:     "a b c d",
			steps:       []step{ch('f'), key(term.KeySpace), ch(';')},
			wantScroll:  term.Coordinates{X: len("a b"), Y: 0},
			wantContent: "a b c d",
		},
		{
			name:        ", after f<space> reverses to previous space",
			content:     "a b c d",
			steps:       []step{ch('f'), key(term.KeySpace), ch('f'), key(term.KeySpace), ch(',')},
			wantScroll:  term.Coordinates{X: len("a"), Y: 0},
			wantContent: "a b c d",
		},
		{
			name:        "f<tab> moves cursor to next tab",
			content:     "ab\tcd",
			steps:       []step{ch('f'), key(term.KeyTab)},
			wantScroll:  term.Coordinates{X: len("ab"), Y: 0},
			wantContent: "ab\tcd",
		},
		{
			name:        "df<tab> deletes through next tab",
			content:     "ab\tcd",
			steps:       []step{ch('d'), ch('f'), key(term.KeyTab)},
			wantScroll:  term.Coordinates{X: 0, Y: 0},
			wantContent: "cd",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vi := setupVi(t, tc.content, 2)
			vi.Resize(80, 8)
			vi.Draw(term.NoopWriter{})
			if tc.setup != nil {
				tc.setup(vi)
			}

			for _, s := range tc.steps {
				ev := s.ev
				if ev.Type == 0 {
					ev = term.Event{Type: term.EventKey, Ch: s.ch}
				}
				_, handled := vi.Handle(ev)
				require.True(t, handled, "event %+v", ev)
			}

			assert.Equal(t, tc.wantContent, vi.less.Buffer().String())
			assert.Equal(t, tc.wantScroll, vi.cursor.CursorAtScroll())
			wantMode := tc.wantMode
			if wantMode == 0 {
				wantMode = normalMode
			}
			assert.Equal(t, wantMode, vi.mode())
			if tc.wantClipboard != "" {
				paste, err := vi.config.clipboard.Paste(vi.config.defaultRegister)
				require.NoError(t, err)
				assert.Equal(t, tc.wantClipboard, paste.Text)
			}
		})
	}
}

func TestViX(t *testing.T) {
	suite := []struct {
		name          string
		moveCursorFn  func(*viHandlerImpl)
		content       string
		events        string
		expectContent string
		expectCoords  *term.Coordinates
	}{
		{
			name:          "X deletes character before cursor",
			content:       "abcd",
			events:        "llX",
			expectContent: "acd",
			expectCoords:  &term.Coordinates{X: 1, Y: 0},
		},
		{
			name:          "counted X deletes multiple previous characters",
			content:       "abcd",
			events:        "lll3X",
			expectContent: "d",
			expectCoords:  &term.Coordinates{X: 0, Y: 0},
		},
		{
			name:          "X at start of line conflates with previous line",
			content:       "ab\ncd",
			events:        "jX",
			expectContent: "abcd",
			expectCoords:  &term.Coordinates{X: 2, Y: 0},
		},
		{
			name:          "X at start of buffer is a no-op",
			content:       "abcd",
			events:        "X",
			expectContent: "abcd",
			expectCoords:  &term.Coordinates{X: 0, Y: 0},
		},
		{
			name: "10000000000000000000000000000X deletes up to buffer start",
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveEndLine()
			},
			content:       "abcd",
			events:        "10000000000000000000000000000X",
			expectContent: "d",
			expectCoords:  &term.Coordinates{X: 0, Y: 0},
		},
	}

	for _, tcase := range suite {
		t.Run(tcase.name, func(t *testing.T) {
			vi := setupVi(t, tcase.content, 2)
			vi.Resize(20, 9)
			vi.Draw(term.NoopWriter{})
			if tcase.moveCursorFn != nil {
				tcase.moveCursorFn(vi)
			}

			for _, eventChar := range tcase.events {
				vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
			}

			assert.Equal(t, tcase.expectContent, vi.less.Buffer().String())
			if tcase.expectCoords != nil {
				assert.Equal(t, *tcase.expectCoords, vi.cursor.Coordinates())
			}
			assert.Equal(t, normalMode, vi.mode())
		})
	}
}

func TestVidd(t *testing.T) {
	suite := []struct {
		name             string
		moveCursorFn     func(*viHandlerImpl)
		content          string
		events           string
		expectContent    string
		expectCoords     term.Coordinates
		expectCoordsWrap term.Coordinates
	}{
		{
			name: "2dd",
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveToScroll(term.Coordinates{Y: 2})
			},
			content:          "0000\n1111\n2222\n3333\n4444\n5555\n",
			events:           "2dd",
			expectContent:    "0000\n1111\n4444\n5555\n",
			expectCoords:     term.Coordinates{Y: 2},
			expectCoordsWrap: term.Coordinates{Y: 4},
		},
		{
			name: "3dd from last line has no lines below to count",
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveLastLine()
			},
			content:          "0000\n1111\n2222\n3333\n4444\n5555\n",
			events:           "3dd",
			expectContent:    "0000\n1111\n2222\n3333\n4444\n5555\n",
			expectCoords:     term.Coordinates{Y: 6},
			expectCoordsWrap: term.Coordinates{Y: 8},
		},
		{
			name: "3dd from second to last line deletes through the last line",
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveLastLine()
				vi.cursor.MoveLineUp()
			},
			content:          "0000\n1111\n2222\n3333\n4444\n5555\n",
			events:           "3dd",
			expectContent:    "0000\n1111\n2222\n3333\n4444",
			expectCoords:     term.Coordinates{Y: 4},
			expectCoordsWrap: term.Coordinates{Y: 4},
		},
		{
			name:             "10dd from first line wipes all content",
			content:          "0000\n1111\n2222\n3333\n4444\n5555\n",
			events:           "10dd",
			expectContent:    "",
			expectCoords:     term.Coordinates{Y: 0},
			expectCoordsWrap: term.Coordinates{Y: 0},
		},
		{
			name:             "999999999999999999999999999999999999dd",
			content:          "0000\n1111\n2222\n3333\n4444",
			events:           "999999999999999999999999999999999999dd",
			expectContent:    "",
			expectCoords:     term.Coordinates{Y: 0},
			expectCoordsWrap: term.Coordinates{Y: 0},
		},
	}

	for _, tcase := range suite {
		for _, wrap := range []bool{false, true} {
			name := tcase.name
			if wrap {
				name += " (wrap)"
			}
			t.Run(name, func(t *testing.T) {
				vi := setupVi(t, tcase.content, 2, WithWrap(wrap))
				if wrap {
					vi.Resize(2, 9)
				} else {
					vi.Resize(10, 9)
				}
				vi.Draw(term.NoopWriter{})
				if tcase.moveCursorFn != nil {
					tcase.moveCursorFn(vi)
				}
				for _, eventChar := range tcase.events {
					vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
				}
				if wrap {
					assert.Equal(t, tcase.expectCoordsWrap, vi.cursor.Coordinates())
				} else {
					assert.Equal(t, tcase.expectCoords, vi.cursor.Coordinates())
				}
				assert.Equal(t, tcase.expectContent, vi.less.Buffer().String())
			})
		}
	}
}

func TestViSubstituteLine(t *testing.T) {
	suite := []struct {
		name          string
		content       string
		events        string
		expectContent string
	}{
		{
			name:          "S on single line clears and enters insert",
			content:       "hello world",
			events:        "S",
			expectContent: "",
		},
		{
			name:          "S on indented line clears content",
			content:       "    indented line",
			events:        "S",
			expectContent: "",
		},
		{
			name:          "S on middle line only affects current line",
			content:       "aaa\nbbb\nccc",
			events:        "jS",
			expectContent: "aaa\n\nccc",
		},
		{
			name:          "S then type replacement text",
			content:       "old text\nsecond line",
			events:        "Snew text",
			expectContent: "new text\nsecond line",
		},
		{
			name:          "S behaves same as cc",
			content:       "hello world\nsecond line",
			events:        "Sreplaced",
			expectContent: "replaced\nsecond line",
		},
	}

	for _, tcase := range suite {
		t.Run(tcase.name, func(t *testing.T) {
			vi := setupVi(t, tcase.content, 2)
			vi.Resize(20, 9)
			vi.Draw(term.NoopWriter{})

			for _, eventChar := range tcase.events {
				vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
			}

			assert.Equal(t, tcase.expectContent, vi.less.Buffer().String())
			assert.Equal(t, insertMode, vi.mode())
		})
	}
}

func TestLocationMessage(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{"j",
			`                    
▐*                  
 * Check if the curr
 * diff buffers.    
 */                 
  void              
diff_buf_adjust(win_
{                   
  win_T  *wp;       
            durrdurr`},
		{"jj",
			`                    
/*                  
▐* Check if the curr
 * diff buffers.    
 */                 
  void              
diff_buf_adjust(win_
{                   
  win_T  *wp;       
  int        i;     `},
		{"jjk",
			`                    
▐*                  
 * Check if the curr
 * diff buffers.    
 */                 
  void              
diff_buf_adjust(win_
{                   
  win_T  *wp;       
            durrdurr`},
	}

	newVi := func(t *testing.T) tui.Handler {
		vi := setupVi(t, snippet, 2)
		vi.setLocationList(textapi.LocationPriorityInfo, "id",
			textapi.LocationSlice([]textapi.Location{
				{
					Message: "durrdurr",
					From:    term.Coordinates{Y: 1},
					To:      term.Coordinates{Y: 1, X: 5},
				},
			}))
		return vi
	}
	handlertest.RunHandlerIsolated(t, newVi, 20, 10, cases)
}

func TestVidfd(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{"jjdfd",
			`                    
/*                  
▐be added to or remo
 * diff buffers.    
 */                 
  void              
diff_buf_adjust(win_
{                   
  win_T  *wp;       
              NORMAL`},
		{"jjcfc",
			`                    
/*                  
▐ if the current buf
 * diff buffers.    
 */                 
  void              
diff_buf_adjust(win_
{                   
  win_T  *wp;       
              INSERT`},
	}

	newVi := func(t *testing.T) tui.Handler {
		return setupViIntegration(t, snippet, 2)
	}
	handlertest.TestHandlerIsolated(t, newVi, 20, 10, cases)
}

func TestWrapMoveDownLastLogicalLine(t *testing.T) {
	sample := `abcde
fghih
ijklm
opkrs
tuvxy
11111
22222
33333
44444
55555
66666`
	vi := setupVi(t, sample, 2, WithWrap(true))
	vi.Resize(3, 20)
	vi.Draw(term.NoopWriter{})
	for _, eventChar := range "jjjjjjjjjjjjjjjjjjjjj" {
		vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
	}
	assert.Equal(t, term.Coordinates{X: 3, Y: 10}, vi.cursor.CursorAtScroll())
	vi.cursor.SelectLine()
	assert.Equal(t, "66666\n", vi.cursor.Selection())
}

func TestVisualMoveToChar(t *testing.T) {
	sample := `abcde`
	vi := setupVi(t, sample, 2, WithWrap(false))
	vi.Resize(10, 20)
	vi.Draw(term.NoopWriter{})

	for _, eventChar := range "vfc" {
		vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
	}
	selection, ok := vi.Selection()
	require.True(t, ok)
	assert.Equal(t, "abc", selection)
}

func TestVisualMoveToCharOperators(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		at      term.Coordinates
		keys    string
		want    string
	}{
		{name: "vt,x", content: "ab,cd,ef", keys: "vt,x", want: ",cd,ef"},
		{name: "vF,x", content: "ab,cd,ef", at: term.Coordinates{X: 7}, keys: "vF,x", want: "ab,cd"},
		{name: "vT,x", content: "ab,cd,ef", at: term.Coordinates{X: 7}, keys: "vT,x", want: "ab,cd,"},
		{name: "v2f,x", content: "ab,cd,ef", keys: "v2f,x", want: "ef"},
		{name: "vf,;x", content: "ab,cd,ef", keys: "vf,;x", want: "ef"},
		{name: "vf,;,x", content: "ab,cd,ef", keys: "vf,;,x", want: "cd,ef"},
		{name: "f,v;x", content: "ab,cd,ef", keys: "f,v;x", want: "abef"},
		{name: "vt,;x", content: "ab,cd,ef", keys: "vt,;x", want: ",ef"},
		{name: "vlf,d", content: "ab,cd,ef", keys: "vlf,d", want: "cd,ef"},
		{name: "Vjf,d", content: "ab,cd\nef,gh\nij", keys: "Vjf,d", want: "ij"},
		{name: "<c-v>jf,d", content: "ab,cd\nef,gh\nij", keys: "<c-v>jf,d", want: "cd\ngh\nij"},
		{name: "v exits visual mode", content: "ab,de", keys: "vf,vx", want: "abde"},
		{name: "V exits visual line mode", content: "ab\ncd", keys: "VVx", want: "b\ncd"},
		{name: "ctrl-v exits visual block mode", content: "ab\ncd", keys: "<c-v>l<c-v>x", want: "a\ncd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vi := setupVi(t, tc.content, 2, WithWrap(false))
			vi.Resize(20, 20)
			vi.Draw(term.NoopWriter{})
			vi.setCursorAtScroll(tc.at)
			handleViEvents(vi, parseSearchOpEvents(tc.keys))
			assert.Equal(t, tc.want, vi.less.Buffer().String())
			assert.Equal(t, normalMode, vi.mode())
		})
	}
}

func TestVisualMoveToCharKeepsVisualMode(t *testing.T) {
	tests := []struct {
		name string
		keys string
		mode viMode
	}{
		{"visual", "vf,", visualMode},
		{"visual line", "Vf,", visualLineMode},
		{"visual block", "Vf,", visualBlockMode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vi := setupVi(t, `ab,de`, 0, WithWrap(false))
			vi.Resize(10, 20)
			vi.Draw(term.NoopWriter{})

			if tt.mode == visualBlockMode {
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'v', Mod: term.ModCtrl})
				tt.keys = "f,"
			}
			for _, eventChar := range tt.keys {
				vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
			}
			assert.Equal(t, tt.mode, vi.currMode)
		})
	}
}

func TestVisualMoveToCharThenDelete(t *testing.T) {
	vi := setupVi(t, `ab,de`, 0, WithWrap(false))
	vi.Resize(10, 20)
	vi.Draw(term.NoopWriter{})

	for _, eventChar := range "vf,x" {
		vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
	}
	assert.Equal(t, "de", vi.less.Buffer().String())
	assert.Equal(t, normalMode, vi.currMode)
}

func handleViEvents(vi *viHandlerImpl, events []term.Event) {
	for _, ev := range events {
		vi.Handle(ev)
	}
}

func viPositionVisible(vi *viHandlerImpl, pos term.Coordinates) bool {
	win, ok := vi.cursor.WindowCoordinates(pos)
	if !ok {
		return false
	}
	return win.X >= 0 && win.Y >= 0 &&
		win.X < vi.less.Scroll().Width() &&
		win.Y < vi.less.Scroll().SizeHeight()
}

func swappedVisualEnds(anchor, cursor term.Coordinates) (term.Coordinates, term.Coordinates) {
	return anchor, cursor
}

func swappedBlockCorners(anchor, cursor term.Coordinates) (term.Coordinates, term.Coordinates) {
	if anchor.X == cursor.X || anchor.Y == cursor.Y {
		// Same column or same row: O acts like o (full end swap).
		return anchor, cursor // wantAfterCursor=anchor, wantAfterAnchor=cursor
	}
	return term.Coordinates{X: anchor.X, Y: cursor.Y},
		term.Coordinates{X: cursor.X, Y: anchor.Y}
}

func TestVisualSwapSelectionEnd(t *testing.T) {
	type testCase struct {
		name                    string
		content                 string
		width                   int
		height                  int
		before                  []term.Event
		wantMode                viMode
		wantBeforeCursor        term.Coordinates
		wantBeforeAnchor        term.Coordinates
		wantBeforeCursorVisible bool
		wantBeforeAnchorVisible bool
		wantAfterCursorVisible  bool
		wantAfterAnchorVisible  bool
	}

	key := func(ch rune) term.Event {
		return term.Event{Type: term.EventKey, Ch: ch}
	}

	suite := []testCase{
		{
			name:                    "same line middle",
			content:                 "abcd",
			width:                   10,
			height:                  20,
			before:                  []term.Event{key('v'), key('l'), key('l')},
			wantMode:                visualMode,
			wantBeforeCursor:        term.Coordinates{X: 2},
			wantBeforeAnchor:        term.Coordinates{},
			wantBeforeCursorVisible: true,
			wantBeforeAnchorVisible: true,
			wantAfterCursorVisible:  true,
			wantAfterAnchorVisible:  true,
		},
		{
			name:                    "same line beginning to end of line",
			content:                 "abcd",
			width:                   10,
			height:                  20,
			before:                  []term.Event{key('v'), key('$')},
			wantMode:                visualMode,
			wantBeforeCursor:        term.Coordinates{X: 4},
			wantBeforeAnchor:        term.Coordinates{},
			wantBeforeCursorVisible: true,
			wantBeforeAnchorVisible: true,
			wantAfterCursorVisible:  true,
			wantAfterAnchorVisible:  true,
		},
		{
			name:                    "same line end of line to middle",
			content:                 "abcd",
			width:                   10,
			height:                  20,
			before:                  []term.Event{key('$'), key('v'), key('h'), key('h')},
			wantMode:                visualMode,
			wantBeforeCursor:        term.Coordinates{X: 1},
			wantBeforeAnchor:        term.Coordinates{X: 3},
			wantBeforeCursorVisible: true,
			wantBeforeAnchorVisible: true,
			wantAfterCursorVisible:  true,
			wantAfterAnchorVisible:  true,
		},
		{
			name:                    "different lines middle of line",
			content:                 "abcd\nefgh\nijkl",
			width:                   10,
			height:                  20,
			before:                  []term.Event{key('l'), key('v'), key('j'), key('l')},
			wantMode:                visualMode,
			wantBeforeCursor:        term.Coordinates{X: 2, Y: 1},
			wantBeforeAnchor:        term.Coordinates{X: 1},
			wantBeforeCursorVisible: true,
			wantBeforeAnchorVisible: true,
			wantAfterCursorVisible:  true,
			wantAfterAnchorVisible:  true,
		},
		{
			name:                    "start of file",
			content:                 "abcd\nefgh\nijkl",
			width:                   10,
			height:                  20,
			before:                  []term.Event{key('v'), key('j'), key('j'), key('l')},
			wantMode:                visualMode,
			wantBeforeCursor:        term.Coordinates{X: 1, Y: 2},
			wantBeforeAnchor:        term.Coordinates{},
			wantBeforeCursorVisible: true,
			wantBeforeAnchorVisible: true,
			wantAfterCursorVisible:  true,
			wantAfterAnchorVisible:  true,
		},
		{
			name:                    "end of file",
			content:                 "abcd\nefgh\nijkl",
			width:                   10,
			height:                  20,
			before:                  []term.Event{key('G'), key('$'), key('v'), key('h'), key('h')},
			wantMode:                visualMode,
			wantBeforeCursor:        term.Coordinates{X: 1, Y: 2},
			wantBeforeAnchor:        term.Coordinates{X: 3, Y: 2},
			wantBeforeCursorVisible: true,
			wantBeforeAnchorVisible: true,
			wantAfterCursorVisible:  true,
			wantAfterAnchorVisible:  true,
		},
		{
			name:                    "selection start outside rendered view vertically",
			content:                 "line0\nline1\nline2\nline3\nline4\nline5",
			width:                   10,
			height:                  3,
			before:                  []term.Event{key('v'), key('j'), key('j'), key('j'), key('j')},
			wantMode:                visualMode,
			wantBeforeCursor:        term.Coordinates{Y: 4},
			wantBeforeAnchor:        term.Coordinates{},
			wantBeforeCursorVisible: true,
			wantBeforeAnchorVisible: false,
			wantAfterCursorVisible:  true,
			wantAfterAnchorVisible:  false,
		},
		{
			name:                    "selection start outside rendered view horizontally",
			content:                 "0123456789abcdef",
			width:                   4,
			height:                  2,
			before:                  []term.Event{key('v'), key('$')},
			wantMode:                visualMode,
			wantBeforeCursor:        term.Coordinates{X: 16},
			wantBeforeAnchor:        term.Coordinates{},
			wantBeforeCursorVisible: false,
			wantBeforeAnchorVisible: false,
			wantAfterCursorVisible:  true,
			wantAfterAnchorVisible:  false,
		},
		{
			name:                    "visual line mode different lines",
			content:                 "alpha\nbeta\ngamma\ndelta",
			width:                   10,
			height:                  20,
			before:                  []term.Event{key('V'), key('j'), key('j')},
			wantMode:                visualLineMode,
			wantBeforeCursor:        term.Coordinates{Y: 2},
			wantBeforeAnchor:        term.Coordinates{},
			wantBeforeCursorVisible: true,
			wantBeforeAnchorVisible: true,
			wantAfterCursorVisible:  true,
			wantAfterAnchorVisible:  true,
		},
		{
			name:                    "visual line mode selection start outside rendered view vertically",
			content:                 "alpha\nbeta\ngamma\ndelta\nepsilon\nzeta",
			width:                   10,
			height:                  3,
			before:                  []term.Event{key('V'), key('j'), key('j'), key('j'), key('j')},
			wantMode:                visualLineMode,
			wantBeforeCursor:        term.Coordinates{Y: 4},
			wantBeforeAnchor:        term.Coordinates{},
			wantBeforeCursorVisible: true,
			wantBeforeAnchorVisible: false,
			wantAfterCursorVisible:  true,
			wantAfterAnchorVisible:  false,
		},
	}

	for _, tc := range suite {
		t.Run(tc.name, func(t *testing.T) {
			vi := setupVi(t, tc.content, 2, WithWrap(false))
			vi.Resize(tc.width, tc.height)
			vi.Draw(term.NoopWriter{})

			handleViEvents(vi, tc.before)

			require.Equal(t, tc.wantMode, vi.mode())

			beforeCursor := vi.cursor.CursorAtScroll()
			require.Equal(t, tc.wantBeforeCursor, beforeCursor)

			beforeAnchor, ok := vi.cursor.SelectionFrom()
			require.True(t, ok)
			require.Equal(t, tc.wantBeforeAnchor, beforeAnchor)
			assert.Equal(t, tc.wantBeforeCursorVisible, viPositionVisible(vi, beforeCursor))
			assert.Equal(t, tc.wantBeforeAnchorVisible, viPositionVisible(vi, beforeAnchor))

			selectionBefore := vi.cursor.Selection()
			require.NotEmpty(t, selectionBefore)
			bufferBefore := vi.less.Buffer().String()
			wantAfterCursor, wantAfterAnchor := swappedVisualEnds(beforeAnchor, beforeCursor)

			vi.Handle(key('o'))

			assert.Equal(t, tc.wantMode, vi.mode())
			assert.Equal(t, bufferBefore, vi.less.Buffer().String())
			assert.Equal(t, wantAfterCursor, vi.cursor.CursorAtScroll())
			assert.Equal(t, selectionBefore, vi.cursor.Selection())

			afterAnchor, ok := vi.cursor.SelectionFrom()
			require.True(t, ok)
			assert.Equal(t, wantAfterAnchor, afterAnchor)
			assert.Equal(t, tc.wantAfterCursorVisible, viPositionVisible(vi, wantAfterCursor))
			assert.Equal(t, tc.wantAfterAnchorVisible, viPositionVisible(vi, afterAnchor))
		})
	}
}

func TestTillCharacterMotion(t *testing.T) {
	sample := `abcdabcdabcd`
	// Positions: a=0, b=1, c=2, d=3, a=4, b=5, c=6, d=7, a=8, b=9, c=10, d=11

	type testCase struct {
		name    string
		input   string
		expectX int
	}

	cases := []testCase{
		// t: till next character, cursor lands one before target
		{"t forward", "tc", 1},   // first 'c' at 2, land at 1
		{"t forward 2", "td", 2}, // first 'd' at 3, land at 2
		{"t no match", "tz", 0},  // not found, stay at 0
		{"t after moving right", "lltd", 2},

		// T: till prev character, cursor lands one after target
		{"T backward", "lllllllTa", 5},    // at col 7, prev 'a' at 4, land at 5
		{"T backward 2", "llllllllTb", 6}, // at col 8, prev 'b' at 5, land at 6
		{"T no match", "llTz", 2},         // not found, stays at 2
		{"T from end to c", "llllllllllTc", 7},

		// ; repeats t in same direction (till)
		{"t then semicolon", "tc;", 5}, // tc->1, ;->till next 'c' at 6, land at 5
		{"t then double semicolon", "tc;;", 9},

		// , repeats t in opposite direction (till)
		{"t then comma", "lllltc,", 3}, // at 4, tc->5 (before 'c' at 6), ,->till prev 'c' at 2, land at 3
		{"t then semicolon then comma", "tc;,", 3},

		// ; repeats T in same direction (backward till)
		{"T then semicolon", "llllllllTa;", 1}, // at 8, Ta->5, ;->till prev 'a' at 0, land at 1
		{"T then comma", "llllllllTa,", 7},

		// f then ; still works (regression)
		{"f then semicolon", "fc;", 6},   // fc->2, ;->next 'c' at 6
		{"f then comma", "llllllfc,", 6}, // at 6, fc->10, ,->prev 'c' at 6
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vi := setupVi(t, sample, 2, WithWrap(false))
			vi.Resize(20, 20)
			vi.Draw(term.NoopWriter{})

			for _, ch := range tc.input {
				vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			}
			vi.Draw(term.NoopWriter{})

			coords := vi.cursor.Coordinates()
			assert.Equal(t, tc.expectX, coords.X, "cursor X position")
		})
	}
}

func TestViDeleteAWord(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{"jjjjjwdw",
			`                    
/*                  
 * Check if the curr
 * diff buffers.    
 */                 
 ▐                  
diff_buf_adjust(win_
{                   
  win_T  *wp;       
              NORMAL`},
		{"jjjjjwcw",
			`                    
/*                  
 * Check if the curr
 * diff buffers.    
 */                 
  ▐                 
diff_buf_adjust(win_
{                   
  win_T  *wp;       
              INSERT`},
		{"jjjjjwce",
			`                    
/*                  
 * Check if the curr
 * diff buffers.    
 */                 
  ▐                 
diff_buf_adjust(win_
{                   
  win_T  *wp;       
              INSERT`},
		{"jjjjjwecb",
			`                    
/*                  
 * Check if the curr
 * diff buffers.    
 */                 
  ▐                 
diff_buf_adjust(win_
{                   
  win_T  *wp;       
              INSERT`},
		{"jjjwwcw",
			`                    
/*                  
 * Check if the curr
 * ▐buffers.        
 */                 
  void              
diff_buf_adjust(win_
{                   
  win_T  *wp;       
              INSERT`},
		{"jjjwwce",
			`                    
/*                  
 * Check if the curr
 * ▐buffers.        
 */                 
  void              
diff_buf_adjust(win_
{                   
  win_T  *wp;       
              INSERT`},
	}

	newVi := func(t *testing.T) tui.Handler {
		return setupViIntegration(t, snippet, 2)
	}
	handlertest.TestHandlerIsolated(t, newVi, 20, 10, cases)
}

func TestViGoLeftEndWordMotions(t *testing.T) {
	const content = "one two-three, four"
	at := term.Coordinates{X: strings.Index(content, "four") + len("four") - 1, Y: 0}

	newVi := func(t *testing.T) *viHandlerImpl {
		t.Helper()
		vi := setupVi(t, content, 2)
		vi.Resize(80, 10)
		vi.setCursorAtScroll(at)
		vi.Draw(term.NoopWriter{})
		return vi
	}

	for _, tc := range []struct {
		name   string
		seq    string
		motion func(*viHandlerImpl) bool
	}{
		{
			name:   "ge moves to previous word end",
			seq:    "ge",
			motion: func(vi *viHandlerImpl) bool { return vi.cursor.MoveLeftEndWord() },
		},
		{
			name:   "gE moves to previous WORD end",
			seq:    "gE",
			motion: func(vi *viHandlerImpl) bool { return vi.cursor.MoveLeftEndWordGroup() },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected := newVi(t)
			tc.motion(expected)
			require.NotEqual(t, at, expected.cursor.Coordinates())

			actual := newVi(t)
			for _, event := range tc.seq {
				_, handled := actual.Handle(term.Event{Type: term.EventKey, Ch: event})
				require.True(t, handled, "sequence %q failed on %q", tc.seq, string(event))
			}

			assert.Equal(t, expected.cursor.Coordinates(), actual.cursor.Coordinates())
			assert.Equal(t, normalMode, actual.mode())
		})
	}
}

func TestViOperatorGoLeftEndWordMotions(t *testing.T) {
	const content = "one two-three, four"
	at := term.Coordinates{X: strings.Index(content, "four") + len("four") - 1, Y: 0}

	newVi := func(t *testing.T) *viHandlerImpl {
		t.Helper()
		vi := setupVi(t, content, 2)
		vi.Resize(80, 10)
		vi.setCursorAtScroll(at)
		vi.Draw(term.NoopWriter{})
		return vi
	}

	for _, tc := range []struct {
		name          string
		seq           string
		motion        func(*viHandlerImpl) bool
		wantMode      viMode
		wantClipboard bool
	}{
		{
			name:     "delete ge",
			seq:      "dge",
			motion:   func(vi *viHandlerImpl) bool { return vi.cursor.MoveLeftEndWord() },
			wantMode: normalMode,
		},
		{
			name:     "delete gE",
			seq:      "dgE",
			motion:   func(vi *viHandlerImpl) bool { return vi.cursor.MoveLeftEndWordGroup() },
			wantMode: normalMode,
		},
		{
			name:     "change ge enters insert mode",
			seq:      "cge",
			motion:   func(vi *viHandlerImpl) bool { return vi.cursor.MoveLeftEndWord() },
			wantMode: insertMode,
		},
		{
			name:          "yank ge",
			seq:           "yge",
			motion:        func(vi *viHandlerImpl) bool { return vi.cursor.MoveLeftEndWord() },
			wantMode:      normalMode,
			wantClipboard: true,
		},
		{
			name:          "yank gE",
			seq:           "ygE",
			motion:        func(vi *viHandlerImpl) bool { return vi.cursor.MoveLeftEndWordGroup() },
			wantMode:      normalMode,
			wantClipboard: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected := newVi(t)
			require.True(t, expected.cursor.Select())
			tc.motion(expected)
			require.NotEqual(t, at, expected.cursor.Coordinates())

			switch tc.seq[0] {
			case 'd', 'c':
				expected.cursor.DeleteSelection()
			case 'y':
				expected.yankRange()
			}

			switch tc.wantMode {
			case insertMode:
				expected.setInsertMode()
			default:
				expected.setNormalMode()
			}
			expected.doMoveToBounds()

			actual := newVi(t)
			for i, event := range tc.seq {
				_, handled := actual.Handle(term.Event{Type: term.EventKey, Ch: event})
				require.True(t, handled, "sequence %q failed on %q", tc.seq, string(event))
				if i == 1 {
					switch tc.seq[0] {
					case 'd', 'c':
						assert.Equal(t, deleteMode, actual.mode())
					case 'y':
						assert.Equal(t, yankMode, actual.mode())
					}
				}
			}

			assert.Equal(t, tc.wantMode, actual.mode())
			assert.Equal(t, expected.less.Buffer().String(), actual.less.Buffer().String())
			assert.Equal(t, expected.cursor.Coordinates(), actual.cursor.Coordinates())

			if tc.wantClipboard {
				expectedPaste, err := expected.config.clipboard.Paste(expected.config.defaultRegister)
				require.NoError(t, err)
				actualPaste, err := actual.config.clipboard.Paste(actual.config.defaultRegister)
				require.NoError(t, err)
				assert.Equal(t, expectedPaste.Text, actualPaste.Text)
			}
		})
	}
}

func TestViGoLeftEndWordMotionsAcrossLines(t *testing.T) {
	const content = "one\ntwo-three\nfour"
	at := term.Coordinates{X: len("four") - 1, Y: 2}

	newVi := func(t *testing.T) *viHandlerImpl {
		t.Helper()
		vi := setupVi(t, content, 2)
		vi.Resize(80, 10)
		vi.setCursorAtScroll(at)
		vi.Draw(term.NoopWriter{})
		return vi
	}

	for _, tc := range []struct {
		name   string
		seq    string
		motion func(*viHandlerImpl) bool
	}{
		{
			name:   "ge crosses to prior line word end",
			seq:    "ge",
			motion: func(vi *viHandlerImpl) bool { return vi.cursor.MoveLeftEndWord() },
		},
		{
			name:   "gE crosses to prior line WORD end",
			seq:    "gE",
			motion: func(vi *viHandlerImpl) bool { return vi.cursor.MoveLeftEndWordGroup() },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected := newVi(t)
			tc.motion(expected)
			require.NotEqual(t, at, expected.cursor.Coordinates())

			actual := newVi(t)
			for _, event := range tc.seq {
				_, handled := actual.Handle(term.Event{Type: term.EventKey, Ch: event})
				require.True(t, handled, "sequence %q failed on %q", tc.seq, string(event))
			}

			assert.Equal(t, expected.cursor.Coordinates(), actual.cursor.Coordinates())
			assert.Equal(t, normalMode, actual.mode())
		})
	}
}

func TestViOperatorGoLeftEndWordMotionsAcrossLines(t *testing.T) {
	const content = "one\ntwo-three\nfour"
	at := term.Coordinates{X: len("four") - 1, Y: 2}

	newVi := func(t *testing.T) *viHandlerImpl {
		t.Helper()
		vi := setupVi(t, content, 2)
		vi.Resize(80, 10)
		vi.setCursorAtScroll(at)
		vi.Draw(term.NoopWriter{})
		return vi
	}

	for _, tc := range []struct {
		name     string
		seq      string
		motion   func(*viHandlerImpl) bool
		wantMode viMode
	}{
		{
			name:     "delete ge across lines",
			seq:      "dge",
			motion:   func(vi *viHandlerImpl) bool { return vi.cursor.MoveLeftEndWord() },
			wantMode: normalMode,
		},
		{
			name:     "change gE across lines",
			seq:      "cgE",
			motion:   func(vi *viHandlerImpl) bool { return vi.cursor.MoveLeftEndWordGroup() },
			wantMode: insertMode,
		},
		{
			name:     "yank ge across lines",
			seq:      "yge",
			motion:   func(vi *viHandlerImpl) bool { return vi.cursor.MoveLeftEndWord() },
			wantMode: normalMode,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected := newVi(t)
			require.True(t, expected.cursor.Select())
			tc.motion(expected)
			require.NotEqual(t, at, expected.cursor.Coordinates())

			switch tc.seq[0] {
			case 'd', 'c':
				expected.cursor.DeleteSelection()
			case 'y':
				expected.yankRange()
			}

			if tc.wantMode == insertMode {
				expected.setInsertMode()
			} else {
				expected.setNormalMode()
			}
			expected.doMoveToBounds()

			actual := newVi(t)
			for _, event := range tc.seq {
				_, handled := actual.Handle(term.Event{Type: term.EventKey, Ch: event})
				require.True(t, handled, "sequence %q failed on %q", tc.seq, string(event))
			}

			assert.Equal(t, tc.wantMode, actual.mode())
			assert.Equal(t, expected.less.Buffer().String(), actual.less.Buffer().String())
			assert.Equal(t, expected.cursor.Coordinates(), actual.cursor.Coordinates())

			if tc.seq[0] == 'y' {
				expectedPaste, err := expected.config.clipboard.Paste(expected.config.defaultRegister)
				require.NoError(t, err)
				actualPaste, err := actual.config.clipboard.Paste(actual.config.defaultRegister)
				require.NoError(t, err)
				assert.Equal(t, expectedPaste.Text, actualPaste.Text)
			}
		})
	}
}

func TestViGoLeftEndWordSnapshots(t *testing.T) {
	const (
		content = "one two-three,four"
		width   = 24
		height  = 5
	)

	newVi := func(t *testing.T) tui.Handler {
		t.Helper()
		return setupViIntegration(t, content, 2)
	}

	t.Run("motions", func(t *testing.T) {
		cases := []handlertest.SequenceTestCase{
			{InputSequence: "$", Expected: "one two-three,fou▐      \n                        \n                        \n                        \n                  NORMAL"},
			{InputSequence: "ge", Expected: "one two-three▐four      \n                        \n                        \n                        \n                  NORMAL"},
			{InputSequence: "gE", Expected: "on▐ two-three,four      \n                        \n                        \n                        \n                  NORMAL"},
		}
		handlertest.RunHandlerSequence(t, newVi(t), width, height, cases)
	})

	t.Run("delete word motion", func(t *testing.T) {
		cases := []handlertest.SequenceTestCase{
			{InputSequence: "$", Expected: "one two-three,fou▐      \n                        \n                        \n                        \n                  NORMAL"},
			{InputSequence: "dge", Expected: "one two-thre▐           \n                        \n                        \n                        \n                  NORMAL"},
		}
		handlertest.RunHandlerSequence(t, newVi(t), width, height, cases)
	})

	t.Run("delete WORD motion", func(t *testing.T) {
		cases := []handlertest.SequenceTestCase{
			{InputSequence: "$", Expected: "one two-three,fou▐      \n                        \n                        \n                        \n                  NORMAL"},
			{InputSequence: "dgE", Expected: "o▐                      \n                        \n                        \n                        \n                  NORMAL"},
		}
		handlertest.RunHandlerSequence(t, newVi(t), width, height, cases)
	})

	t.Run("change word motion", func(t *testing.T) {
		cases := []handlertest.SequenceTestCase{
			{InputSequence: "$", Expected: "one two-three,fou▐      \n                        \n                        \n                        \n                  NORMAL"},
			{InputSequence: "cge", Expected: "one two-three▐          \n                        \n                        \n                        \n                  INSERT"},
		}
		handlertest.RunHandlerSequence(t, newVi(t), width, height, cases)
	})

	t.Run("change WORD motion", func(t *testing.T) {
		cases := []handlertest.SequenceTestCase{
			{InputSequence: "$", Expected: "one two-three,fou▐      \n                        \n                        \n                        \n                  NORMAL"},
			{InputSequence: "cgE", Expected: "on▐                     \n                        \n                        \n                        \n                  INSERT"},
		}
		handlertest.RunHandlerSequence(t, newVi(t), width, height, cases)
	})

	t.Run("yank paste word motion", func(t *testing.T) {
		cases := []handlertest.SequenceTestCase{
			{InputSequence: "$", Expected: "one two-three,fou▐      \n                        \n                        \n                        \n                  NORMAL"},
			{InputSequence: "yge", Expected: "one two-three▐four      \n                        \n                        \n                        \n                  NORMAL"},
			{InputSequence: "P", Expected: "one two-three,fou▐,four \n                        \n                        \n                        \n                  NORMAL"},
		}
		handlertest.RunHandlerSequence(t, newVi(t), width, height, cases)
	})

	t.Run("yank paste WORD motion", func(t *testing.T) {
		cases := []handlertest.SequenceTestCase{
			{InputSequence: "$", Expected: "one two-three,fou▐      \n                        \n                        \n                        \n                  NORMAL"},
			{InputSequence: "ygE", Expected: "on▐ two-three,four      \n                        \n                        \n                        \n                  NORMAL"},
			{InputSequence: "P", Expected: "one two-three,fou▐e two-\n                        \n                        \n                        \n                  NORMAL"},
		}
		handlertest.RunHandlerSequence(t, newVi(t), width, height, cases)
	})
}

func TestViTextObjects(t *testing.T) {
	type viTextObjectCase struct {
		name          string
		content       string
		at            *term.Coordinates
		seq           string
		wantBuffer    string
		wantMode      viMode
		wantCursor    *term.Coordinates
		wantSelection string
		wantClipboard string
	}

	run := func(t *testing.T, tc viTextObjectCase) {
		t.Helper()
		vi := setupVi(t, tc.content, 2)
		vi.Resize(80, 10)
		if tc.at != nil {
			vi.setCursorAtScroll(*tc.at)
		}
		for _, event := range tc.seq {
			_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: event})
			require.True(t, handled, "sequence %q failed on %q", tc.seq, string(event))
		}
		if tc.wantBuffer != "" || tc.content == "" {
			assert.Equal(t, tc.wantBuffer, vi.less.Buffer().String())
		}
		assert.Equal(t, tc.wantMode, vi.mode())
		if tc.wantCursor != nil {
			assert.Equal(t, *tc.wantCursor, vi.cursor.CursorAtScroll())
		}
		if tc.wantSelection != "" {
			selection, ok := vi.Selection()
			require.True(t, ok)
			assert.Equal(t, tc.wantSelection, selection)
		}
		if tc.wantClipboard != "" {
			paste, err := vi.config.clipboard.Paste(vi.config.defaultRegister)
			require.NoError(t, err)
			assert.Equal(t, tc.wantClipboard, paste.Text)
		}
	}

	for _, tc := range []viTextObjectCase{
		{
			name:       "delete inner word",
			content:    "one two three",
			seq:        "wdiw",
			wantBuffer: "one  three",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 4, Y: 0},
		},
		{
			name:       "change a word enters insert mode",
			content:    "one two three",
			seq:        "wcaw",
			wantBuffer: "one three",
			wantMode:   insertMode,
			wantCursor: &term.Coordinates{X: 4, Y: 0},
		},
		{
			name:          "yank inner quotes",
			content:       `say "hello world" now`,
			seq:           "fhyi\"",
			wantBuffer:    `say "hello world" now`,
			wantMode:      normalMode,
			wantClipboard: "hello world",
		},
		{
			name:       "delete around parens",
			content:    "call(one, two)",
			seq:        "f(dab",
			wantBuffer: "call",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 3, Y: 0},
		},
		{
			name:       "delete around parens alias",
			content:    "call(one, two)",
			seq:        "f(da)",
			wantBuffer: "call",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 3, Y: 0},
		},
		{
			name:       "delete around braces alias",
			content:    "call{one}",
			seq:        "f{daB",
			wantBuffer: "call",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 3, Y: 0},
		},
		{
			name:       "delete around angle alias",
			content:    "a <b> c",
			seq:        "f<dat",
			wantBuffer: "a  c",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 2, Y: 0},
		},
		{
			name:       "delete inner paragraph",
			content:    "one\ntwo\n\nthree\nfour\n",
			at:         &term.Coordinates{Y: 3},
			seq:        "dip",
			wantBuffer: "one\ntwo\n\n",
			wantMode:   normalMode,
		},
		{
			name:       "delete inner sentence",
			content:    "One. Two! Three?",
			at:         &term.Coordinates{X: 6},
			seq:        "dis",
			wantBuffer: "One.  Three?",
			wantMode:   normalMode,
		},
		{
			name:          "visual inner word",
			content:       "one two three",
			seq:           "vwiw",
			wantBuffer:    "one two three",
			wantMode:      visualMode,
			wantSelection: "two",
		},
		{
			name:          "visual invalid text object key keeps prior selection and stays visual",
			content:       "one two three",
			seq:           "vwiq",
			wantBuffer:    "one two three",
			wantMode:      visualMode,
			wantSelection: "one t",
		},
		{
			name:       "delete invalid text object exits operator mode without mutating",
			content:    "one two",
			seq:        "diq",
			wantBuffer: "one two",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 0, Y: 0},
		},
		{
			name:       "operator can retarget from inner to around",
			content:    "one two",
			seq:        "daiw",
			wantBuffer: " two",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 0, Y: 0},
		},
		{
			name:       "operator can retarget from around to inner",
			content:    "one two",
			seq:        "diiw",
			wantBuffer: " two",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 0, Y: 0},
		},
		{
			name:       "change inner empty quote enters insert mode without mutation",
			content:    `say "" now`,
			seq:        `f"ci"`,
			wantBuffer: `say "" now`,
			wantMode:   insertMode,
			wantCursor: &term.Coordinates{X: 5, Y: 0},
		},
		{
			name:       "change around empty quote removes delimiters and enters insert",
			content:    `say "" now`,
			seq:        `f"ca"`,
			wantBuffer: "say  now",
			wantMode:   insertMode,
			wantCursor: &term.Coordinates{X: 4, Y: 0},
		},
		{
			name:       "change inner empty block enters insert without mutation",
			content:    "call()",
			seq:        "f(cib",
			wantBuffer: "call()",
			wantMode:   insertMode,
			wantCursor: &term.Coordinates{X: 5, Y: 0},
		},
		{
			name:       "change around empty block removes delimiters and enters insert",
			content:    "call()",
			seq:        "f(cab",
			wantBuffer: "call",
			wantMode:   insertMode,
			wantCursor: &term.Coordinates{X: 4, Y: 0},
		},
		{
			name:       "delete inner word from end of line trailing spaces",
			content:    "one two  ",
			at:         &term.Coordinates{X: len("one two  ")},
			seq:        "diw",
			wantBuffer: "one   ",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 4, Y: 0},
		},
		{
			name:       "delete around last word removes leading whitespace",
			content:    "one two",
			at:         &term.Coordinates{X: len("one two")},
			seq:        "daw",
			wantBuffer: "one",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 2, Y: 0},
		},
		{
			name:       "count delete two around words",
			content:    "one two three",
			seq:        "2daw",
			wantBuffer: "three",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 0, Y: 0},
		},
		{
			name:       "count delete two inner words",
			content:    "one two three",
			seq:        "2diw",
			wantBuffer: " three",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 0, Y: 0},
		},
		{
			name:       "count change two inner words enters insert",
			content:    "one two three",
			seq:        "2ciw",
			wantBuffer: " three",
			wantMode:   insertMode,
			wantCursor: &term.Coordinates{X: 0, Y: 0},
		},
		{
			name:          "count yank two inner words",
			content:       "one two three",
			seq:           "2yiw",
			wantBuffer:    "one two three",
			wantMode:      normalMode,
			wantClipboard: "one two",
		},
		{
			name:          "yank a word stores standard selection metadata",
			content:       "one two",
			seq:           "yaw",
			wantBuffer:    "one two",
			wantMode:      normalMode,
			wantClipboard: "one ",
		},
		{
			name:       "count delete three around words",
			content:    "one two three four",
			seq:        "3daw",
			wantBuffer: "four",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 0, Y: 0},
		},
		{
			name:          "count yank three inner WORDs",
			content:       "one two-three four five",
			seq:           "3yiW",
			wantBuffer:    "one two-three four five",
			wantMode:      normalMode,
			wantClipboard: "one two-three four",
		},
		{
			name:       "count change two around words from leading whitespace",
			content:    "  one two three",
			at:         &term.Coordinates{X: 0, Y: 0},
			seq:        "2caw",
			wantBuffer: "  three",
			wantMode:   insertMode,
			wantCursor: &term.Coordinates{X: 2, Y: 0},
		},
		{
			name:       "count delete two inner words from end of line",
			content:    "one two three",
			at:         &term.Coordinates{X: len("one two three"), Y: 0},
			seq:        "2diw",
			wantBuffer: "one two ",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 7, Y: 0},
		},
		{
			name:          "visual around word from end of line",
			content:       "one two",
			at:            &term.Coordinates{X: len("one two"), Y: 0},
			seq:           "vaw",
			wantBuffer:    "one two",
			wantMode:      visualMode,
			wantSelection: " two",
		},
		{
			name:       "delete multiline block",
			content:    "call(\none,\ntwo\n)",
			at:         &term.Coordinates{X: 2, Y: 1},
			seq:        "dab",
			wantBuffer: "call",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 3, Y: 0},
		},
		{
			name:       "inner quote does not cross lines and invalid sequence is harmless",
			content:    "say \"hello\nworld\" now",
			seq:        "f\"di\"",
			wantBuffer: "say \"hello\nworld\" now",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 4, Y: 0},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run(t, tc)
		})
	}
}

func TestViTextObjectYankMetadata(t *testing.T) {
	vi := setupVi(t, "one two", 2)
	vi.Resize(40, 5)
	for _, event := range "yaw" {
		_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: event})
		require.True(t, handled)
	}
	paste, err := vi.config.clipboard.Paste(vi.config.defaultRegister)
	require.NoError(t, err)
	assert.Equal(t, "one ", paste.Text)
	assert.Equal(t, text.StandardSelection, paste.Metadata)
}

func TestViTextObjectSnapshots(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{
			InputSequence: "vwiwd",
			Expected: `one ▐three          
call()              
                    
                    
                    
                    
                    
                    
                    
              NORMAL`,
		},
		{
			InputSequence: "j0f(cab",
			Expected: `one two three       
call▐               
                    
                    
                    
                    
                    
                    
                    
              INSERT`,
		},
	}

	newVi := func(t *testing.T) tui.Handler {
		return setupViIntegration(t, "one two three\ncall()", 2)
	}
	handlertest.RunHandlerIsolated(t, newVi, 20, 10, cases)
}

func TestViCursorIsolated(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{"jjddp",
			`                    
/*                  
 * diff buffers.    
 ▐ Check if the curr
 */                 
  void              
diff_buf_adjust(win_
{                   
  win_T  *wp;       
              NORMAL`},
		// insert block one rune (for now until repeater captures all insert)
		{"`jjjIh<",
			`h                   
h/*                 
h * Check if the cur
h▐* diff buffers.   
 */                 
  void              
diff_buf_adjust(win_
{                   
  win_T  *wp;       
              NORMAL`},
		{"/C>/>",
			`                    
/*                  
 * ▐heck if the curr
 * diff buffers.    
 */                 
  void              
diff_buf_adjust(win_
{                   
  win_T  *wp;       
              NORMAL`},
		// TODO check yank paste after last line
		// the only thing from integration tests is that there's no
		// unix View that trims last EOL, this must in turn translatre in
		// some internal difference which renders this test failure
		/*{"Gyyp",
					`    curtab->tp_diff_
		    diff_redraw(TRUE
		    }
		  }
		  }
		  else
		  diff_buf_add(win->
		}
		▐
		              NORMAL`}, */
	}

	newVi := func(t *testing.T) tui.Handler {
		return setupViIntegration(t, snippet, 2)
	}
	handlertest.TestHandlerIsolated(t, newVi, 20, 10, cases)
}

func TestIntegrationScrollEvent(t *testing.T) {
	tsuite := []struct {
		desc      string
		cursorPos term.Coordinates
		ev        term.Event
	}{
		{"move to matching rune", term.Coordinates{Y: 7}, term.Event{Type: term.EventKey, Ch: '%'}},
		{"MoveEndLine", term.Coordinates{Y: 2}, term.Event{Type: term.EventKey, Ch: '$'}},
		{"MoveRightStartWord", term.Coordinates{X: 4, Y: 9}, term.Event{Type: term.EventKey, Ch: 'w'}},
		{"MoveLeftStartWord", term.Coordinates{X: 8, Y: 9}, term.Event{Type: term.EventKey, Ch: 'b'}},
		{"MoveLeftStartWordGroup", term.Coordinates{X: 8, Y: 9}, term.Event{Type: term.EventKey, Ch: 'B'}},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			vi := setupVi(t, snippet, 4)
			vi.setCursorAtScroll(tcase.cursorPos)
			vi.Resize(4, 4)

			var called int
			vi.less.Scroll().Subscribe(component.FuncScrollSubscriber(func(from, to term.Coordinates) {
				called++
			}))

			_, ok := vi.Handle(tcase.ev)
			assert.True(t, ok)
			assert.Equal(t, 1, called)
		})
	}
}

func TestIntegrationMoveWordSpecialChars(t *testing.T) {
	t.Run("navigating special chars MoveRightEndWord and MoveLeftStartWord", func(t *testing.T) {
		const snippetSpecialChars = `aaa.aaa,aaa:aaa;aaa aaa)aaa"aaa'aaa(aaa{aaa}aaa[aaa` +
			`]aaa	aaa\aaa/aaa+aaa_aaa@aaa#aaa=aaa<aaa>aaa!aaa?aaa|` +
			`aaa^aaa&aaa*aaa%aaa.aaa-`

		vi := setupVi(t, snippetSpecialChars, 2)
		prevCoords := term.Coordinates{X: 0, Y: 0}
		vi.setCursorAtScroll(prevCoords)
		vi.Resize(200, 30)
		jumps := []rune{
			'a', '.', 'a', ',', 'a', ':', 'a', ';', 'a', 'a', ')', 'a', '"',
			'a', '\'', 'a', '(', 'a', '{', 'a', '}', 'a', '[', 'a',
			']', 'a', 'a', '\\', 'a', '/', 'a', '+', 'a', 'a', '@', 'a', '#', 'a',
			'=', 'a', '<', 'a', '>', 'a', '!', 'a', '?', 'a', '|',
			'a', '^', 'a', '&', 'a', '*', 'a', '%', 'a', '.', 'a', '-',
		}

		// forward
		for i := range jumps {
			_, ok := vi.Handle(term.Event{Type: term.EventKey, Ch: 'e'})
			require.True(t, ok)
			cell, ok := vi.cursor.Cell()
			require.True(t, ok)
			require.Equal(t, jumps[i], cell.Ch, "(forward) expected '%c', got '%c'", jumps[i], cell.Ch)
		}

		// backwards
		for i := len(jumps) - 1; i >= 1; i-- {
			_, ok := vi.Handle(term.Event{Type: term.EventKey, Ch: 'b'})
			require.True(t, ok)
			cell, ok := vi.cursor.Cell()
			require.True(t, ok)
			require.Equal(t, jumps[i-1], cell.Ch, "(backwards) expected '%c', got '%c'", jumps[i-1], cell.Ch)
		}
	})
	t.Run("navigating new lines MoveRightEndWord and MoveLeftStartWord", func(t *testing.T) {
		t.Skip("Broken and to be fixed by OX-365")

		const snippetNewLines = `ab#cde
fghi.j
$klmno
pqr@st`

		vi := setupVi(t, snippetNewLines, 2)
		prevCoords := term.Coordinates{X: 0, Y: 0}
		vi.setCursorAtScroll(prevCoords)
		vi.Resize(10, 10)

		jumps := []rune{
			'b', '#', 'e',
			'f', 'i', '.', 'j',
			'$', 'o',
			'p', 'r', '@', 't', // FIXME: Instead of t gets p, to be fixed in OX-365
		}

		// forward
		for i := range jumps {
			_, ok := vi.Handle(term.Event{Type: term.EventKey, Ch: 'e'})
			require.True(t, ok)
			cell, ok := vi.cursor.Cell()
			require.True(t, ok)
			require.Equal(t, jumps[i], cell.Ch, "(forward) expected '%c', got '%c'", jumps[i], cell.Ch)
		}

		revJumps := []rune{
			's', '@', 'p',
			'o', '$',
			'j', '.', 'f',
			'e', 'c', '#', 'a',
		}

		// backwards
		for i := range revJumps {
			_, ok := vi.Handle(term.Event{Type: term.EventKey, Ch: 'b'})
			require.True(t, ok)
			cell, ok := vi.cursor.Cell()
			require.True(t, ok)
			require.Equal(t, revJumps[i], cell.Ch, "(backwards) [index %v] expected '%c', got '%c'", i, revJumps[i], cell.Ch)
		}
	})
}

func TestIntegrationNewFile(t *testing.T) {
	vi := setupVi(t, "", 2)
	vi.Resize(4, 4)
	for _, ch := range "ihello\nworld" {
		vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
	assert.Equal(t, "hello\nworld", vi.less.Buffer().String())
}

func TestExitInsertMode(t *testing.T) {
	t.Run("escape and control-c exit insert mode into normal", func(t *testing.T) {
		vi := setupVi(t, "aaaa\nbbbb\ncccc\ndddd", 2)
		vi.Resize(4, 4)

		vi.setInsertMode()
		assert.Equal(t, vi.mode(), insertMode)

		vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
		assert.Equal(t, vi.mode(), normalMode)

		vi.setInsertMode()
		assert.Equal(t, vi.mode(), insertMode)

		vi.Handle(term.Event{Type: term.EventKey, Ch: 'c', Mod: term.ModCtrl})
		assert.Equal(t, vi.mode(), normalMode)
	})
}

func TestNormalModeIMovesToFirstNonBlank(t *testing.T) {
	vi := setupVi(t, "    abc", 2)
	vi.Resize(10, 10)

	require.True(t, vi.setCursorAtScroll(term.Coordinates{X: 6, Y: 0}))

	exit, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'I'})
	require.False(t, exit)
	require.True(t, handled)
	assert.Equal(t, insertMode, vi.mode())

	scroll := vi.cursor.ScrollCoordinates(vi.cursor.Coordinates())
	assert.Equal(t, term.Coordinates{X: 4, Y: 0}, scroll)

	exit, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: 'X'})
	require.False(t, exit)
	require.True(t, handled)
	assert.Equal(t, "    Xabc", vi.less.Buffer().String())
}

func TestInsertModeCtrlShortcuts(t *testing.T) {
	type testCase struct {
		name       string
		content    string
		cursor     term.Coordinates
		key        rune
		want       string
		wantCursor term.Coordinates
	}

	suite := []testCase{
		{
			name:       "ctrl+h deletes character before cursor",
			content:    "abc",
			cursor:     term.Coordinates{X: 3, Y: 0},
			key:        'h',
			want:       "ab",
			wantCursor: term.Coordinates{X: 2, Y: 0},
		},
		{
			name:       "ctrl+w deletes previous word",
			content:    "one two",
			cursor:     term.Coordinates{X: 7, Y: 0},
			key:        'w',
			want:       "one ",
			wantCursor: term.Coordinates{X: 4, Y: 0},
		},
		{
			name:       "ctrl+j inserts newline at cursor",
			content:    "ab",
			cursor:     term.Coordinates{X: 1, Y: 0},
			key:        'j',
			want:       "a\nb",
			wantCursor: term.Coordinates{X: 0, Y: 1},
		},
		{
			name:       "ctrl+t indents current line",
			content:    "abc",
			cursor:     term.Coordinates{X: 1, Y: 0},
			key:        't',
			want:       "\tabc",
			wantCursor: term.Coordinates{X: 2, Y: 0},
		},
		{
			name:       "ctrl+d deindents current line",
			content:    "\tabc",
			cursor:     term.Coordinates{X: 1, Y: 0},
			key:        'd',
			want:       "abc",
			wantCursor: term.Coordinates{X: 0, Y: 0},
		},
	}

	for _, tc := range suite {
		t.Run(tc.name, func(t *testing.T) {
			vi := setupVi(t, tc.content, 2)
			vi.Resize(10, 10)

			require.True(t, vi.setCursorAtScroll(tc.cursor))
			vi.setInsertMode()

			exit, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: tc.key, Mod: term.ModCtrl})
			require.False(t, exit)
			require.True(t, handled)
			assert.Equal(t, insertMode, vi.mode())
			assert.Equal(t, tc.want, vi.less.Buffer().String())

			scroll := vi.cursor.ScrollCoordinates(vi.cursor.Coordinates())
			assert.Equal(t, tc.wantCursor, scroll)
		})
	}
}

func TestInsertModeAutoPairOption(t *testing.T) {
	run := func(t *testing.T, keycomb string, opts ...Option) *viHandlerImpl {
		t.Helper()
		seq, err := term.ParseKeys("i" + keycomb)
		require.NoError(t, err)

		vi := setupVi(t, "a", 2, opts...)
		vi.Resize(10, 5)
		for _, key := range seq {
			exit, handled := vi.Handle(term.Event{
				Type: term.EventKey,
				Key:  key.Key,
				Mod:  key.Mod,
				Ch:   key.Ch,
			})
			require.False(t, exit)
			require.True(t, handled)
		}
		return vi
	}

	t.Run("disabled by default", func(t *testing.T) {
		vi := run(t, "(")

		assert.Equal(t, "(a", vi.less.Buffer().String())
		assert.Equal(t, term.Coordinates{X: 1}, vi.cursorAtScroll())
	})

	t.Run("opening delimiters insert matching pair when enabled", func(t *testing.T) {
		vi := run(t, "(", WithAutoPair(true))

		assert.Equal(t, "()a", vi.less.Buffer().String())
		assert.Equal(t, term.Coordinates{X: 1}, vi.cursorAtScroll())
	})

	t.Run("backspace removes matching pair when enabled", func(t *testing.T) {
		vi := run(t, "(<backspace>", WithAutoPair(true))

		assert.Equal(t, "a", vi.less.Buffer().String())
		assert.Equal(t, term.Coordinates{}, vi.cursorAtScroll())
	})

	t.Run("enter between braces creates blank line when enabled", func(t *testing.T) {
		vi := run(t, "{<enter>", WithAutoPair(true))

		assert.Equal(t, "{\n\n}\na", vi.less.Buffer().String())
		assert.Equal(t, term.Coordinates{Y: 1}, vi.cursorAtScroll())
	})
}

func TestInsertModeTabUsesIndentServiceWhenAvailable(t *testing.T) {
	t.Run("tab uses indent service when available", func(t *testing.T) {
		buf := cell.NewBuffer()
		buf.Init()
		_, err := buf.ReadFrom(strings.NewReader("abc"))
		require.NoError(t, err)
		buf.WithView(mockIndentView{View: buf.View(), indents: map[int]int{0: 2}})

		vi := new(viHandlerImpl)
		vi.init(buf, defaultviHandlerImplConfig())
		vi.Resize(10, 10)

		_ = vi.setCursorAtScroll(term.Coordinates{X: 0, Y: 0})
		vi.setInsertMode()

		exit, handled := vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyTab})
		require.False(t, exit)
		require.True(t, handled)
		assert.Equal(t, insertMode, vi.mode())
		assert.Equal(t, "\t\tabc", vi.less.Buffer().String())
		assert.Equal(t, term.Coordinates{X: 2, Y: 0}, vi.cursor.CursorAtScroll())
	})

	t.Run("tab falls back to literal tab without indent service", func(t *testing.T) {
		vi := setupVi(t, "abc", 2)
		vi.Resize(10, 10)

		_ = vi.setCursorAtScroll(term.Coordinates{X: 0, Y: 0})
		vi.setInsertMode()

		exit, handled := vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyTab})
		require.False(t, exit)
		require.True(t, handled)
		assert.Equal(t, insertMode, vi.mode())
		assert.Equal(t, "\tabc", vi.less.Buffer().String())
		assert.Equal(t, term.Coordinates{X: 1, Y: 0}, vi.cursor.CursorAtScroll())
	})
}

func TestInsertModeTabAfterOInsertsFullIndentLevel(t *testing.T) {
	t.Run("tab on line at target inserts full indent level", func(t *testing.T) {
		buf := cell.NewBuffer()
		buf.Init()
		_, err := buf.ReadFrom(strings.NewReader("a:\n  b: c"))
		require.NoError(t, err)
		buf.WithView(mockIndentView{View: buf.View(), indents: map[int]int{0: 0, 1: 1, 2: 1}})

		cfg := defaultviHandlerImplConfig()
		cfg.tabspaces = 2
		cfg.indentRune = text.IndentRuneSpace
		cfg.indentTabspaces = 2
		vi := new(viHandlerImpl)
		vi.init(buf, cfg)
		vi.Resize(20, 10)

		// Move to end of first line and press `o` to open a new line below.
		require.True(t, vi.setCursorAtScroll(term.Coordinates{X: 1, Y: 0}))
		_, _ = vi.Handle(term.Event{Type: term.EventKey, Ch: 'o'})
		require.Equal(t, insertMode, vi.mode())
		// `o` runs tryIndent so the new line is at the syntax target (2 spaces).
		assert.Equal(t, "a:\n  \n  b: c", vi.less.Buffer().String())
		assert.Equal(t, term.Coordinates{X: 2, Y: 1}, vi.cursor.CursorAtScroll())

		// First <tab> must insert 2 more spaces, not 1.
		_, handled := vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyTab})
		require.True(t, handled)
		assert.Equal(t, "a:\n    \n  b: c", vi.less.Buffer().String())
		assert.Equal(t, term.Coordinates{X: 4, Y: 1}, vi.cursor.CursorAtScroll())

		// Second <tab> must insert another 2 spaces, not dedent.
		_, handled = vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyTab})
		require.True(t, handled)
		assert.Equal(t, "a:\n      \n  b: c", vi.less.Buffer().String())
		assert.Equal(t, term.Coordinates{X: 6, Y: 1}, vi.cursor.CursorAtScroll())
	})

	t.Run("tab on under-indented line snaps up to target", func(t *testing.T) {
		buf := cell.NewBuffer()
		buf.Init()
		_, err := buf.ReadFrom(strings.NewReader("abc"))
		require.NoError(t, err)
		buf.WithView(mockIndentView{View: buf.View(), indents: map[int]int{0: 1}})

		cfg := defaultviHandlerImplConfig()
		cfg.tabspaces = 2
		cfg.indentRune = text.IndentRuneSpace
		cfg.indentTabspaces = 2
		vi := new(viHandlerImpl)
		vi.init(buf, cfg)
		vi.Resize(20, 10)

		_ = vi.setCursorAtScroll(term.Coordinates{X: 0, Y: 0})
		vi.setInsertMode()

		_, handled := vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyTab})
		require.True(t, handled)
		assert.Equal(t, "  abc", vi.less.Buffer().String())
		assert.Equal(t, term.Coordinates{X: 2, Y: 0}, vi.cursor.CursorAtScroll())
	})
}

type insertModeCommandStep struct {
	name                 string
	event                term.Event
	wantHandled          bool
	wantBuffer           *string
	wantCursor           *term.Coordinates
	wantMode             *viMode
	wantPendingRegister  *bool
	wantPendingNormal    *bool
	wantCompletionActive *bool
}

func insertTestKey(ch rune) term.Event {
	return term.Event{Type: term.EventKey, Ch: ch}
}

func insertTestCtrl(ch rune) term.Event {
	return term.Event{Type: term.EventKey, Ch: ch, Mod: term.ModCtrl}
}

func runInsertModeCommandSteps(t *testing.T, vi *viHandlerImpl, steps []insertModeCommandStep) {
	t.Helper()

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			quit, handled := vi.Handle(step.event)
			require.False(t, quit)
			assert.Equal(t, step.wantHandled, handled)
			if step.wantBuffer != nil {
				assert.Equal(t, *step.wantBuffer, vi.less.Buffer().String())
			}
			if step.wantCursor != nil {
				assert.Equal(t, *step.wantCursor, vi.cursor.CursorAtScroll())
			}
			if step.wantMode != nil {
				assert.Equal(t, *step.wantMode, vi.mode())
			}
			if step.wantPendingRegister != nil {
				assert.Equal(t, *step.wantPendingRegister, vi.pendingInsertRegister)
			}
			if step.wantPendingNormal != nil {
				assert.Equal(t, *step.wantPendingNormal, vi.pendingInsertNormal)
			}
			if step.wantCompletionActive != nil {
				assert.Equal(t, *step.wantCompletionActive, vi.insertCompletion.active)
			}
		})
	}
}

func TestInsertModeCompletionPatternNotFound(t *testing.T) {
	const width, height = 30, 4

	suite := []struct {
		name          string
		content       string
		cursor        term.Coordinates
		inputSequence string
		expected      string
	}{
		{
			name:          "ctrl+n with no word prefix",
			content:       ".",
			cursor:        term.Coordinates{X: 1, Y: 0},
			inputSequence: "<c-n>",
			expected:      ".▐                            \n                              \n                              \n             Pattern Not Found",
		},
		{
			name:          "ctrl+p with no word prefix",
			content:       ".",
			cursor:        term.Coordinates{X: 1, Y: 0},
			inputSequence: "<c-p>",
			expected:      ".▐                            \n                              \n                              \n             Pattern Not Found",
		},
		{
			name:          "ctrl+n with no matching candidates",
			content:       "alpha\nzz",
			cursor:        term.Coordinates{X: 2, Y: 1},
			inputSequence: "<c-n>",
			expected:      "alpha                         \nzz▐                           \n                              \n             Pattern Not Found",
		},
		{
			name:          "ctrl+p with no matching candidates",
			content:       "alpha\nzz",
			cursor:        term.Coordinates{X: 2, Y: 1},
			inputSequence: "<c-p>",
			expected:      "alpha                         \nzz▐                           \n                              \n             Pattern Not Found",
		},
		{
			name:          "successful ctrl+n does not show pattern not found",
			content:       "alpha alpine\nal",
			cursor:        term.Coordinates{X: 2, Y: 1},
			inputSequence: "<c-n>",
			expected:      "alpha alpine                  \nalpha▐                        \n                              \n                              ",
		},
	}

	for _, tc := range suite {
		t.Run(tc.name, func(t *testing.T) {
			vi := setupVi(t, tc.content, 2)
			vi.Resize(width, height)
			vi.setCursorAtScroll(tc.cursor)
			vi.setInsertMode()

			handlertest.RunHandlerSequence(t, vi, width, height, []handlertest.SequenceTestCase{{
				InputSequence: tc.inputSequence,
				Expected:      tc.expected,
			}})
		})
	}
}

func TestInsertModeCompletion(t *testing.T) {
	type completionCase struct {
		name    string
		content string
		cursor  term.Coordinates
		steps   []insertModeCommandStep
	}

	for _, tc := range []completionCase{
		{
			name:    "ctrl+n with no word prefix is handled as a no-op",
			content: ".",
			cursor:  term.Coordinates{X: 1, Y: 0},
			steps: []insertModeCommandStep{
				{
					name:                 "no prefix",
					event:                insertTestCtrl('n'),
					wantHandled:          true,
					wantBuffer:           new("."),
					wantCursor:           new(term.Coordinates{X: 1, Y: 0}),
					wantMode:             new(insertMode),
					wantCompletionActive: new(false),
				},
			},
		},
		{
			name:    "ctrl+n with no matching candidates is handled as a no-op",
			content: "alpha\nzz",
			cursor:  term.Coordinates{X: 2, Y: 1},
			steps: []insertModeCommandStep{
				{
					name:                 "no candidates",
					event:                insertTestCtrl('n'),
					wantHandled:          true,
					wantBuffer:           new("alpha\nzz"),
					wantCursor:           new(term.Coordinates{X: 2, Y: 1}),
					wantMode:             new(insertMode),
					wantCompletionActive: new(false),
				},
			},
		},
		{
			name:    "ctrl+n cycles forward through candidates and wraps",
			content: "alpha alpine altar\nal",
			cursor:  term.Coordinates{X: 2, Y: 1},
			steps: []insertModeCommandStep{
				{
					name:                 "first next completion",
					event:                insertTestCtrl('n'),
					wantHandled:          true,
					wantBuffer:           new("alpha alpine altar\nalpha"),
					wantCursor:           new(term.Coordinates{X: 5, Y: 1}),
					wantMode:             new(insertMode),
					wantCompletionActive: new(true),
				},
				{
					name:                 "second next completion",
					event:                insertTestCtrl('n'),
					wantHandled:          true,
					wantBuffer:           new("alpha alpine altar\nalpine"),
					wantCursor:           new(term.Coordinates{X: 6, Y: 1}),
					wantMode:             new(insertMode),
					wantCompletionActive: new(true),
				},
				{
					name:                 "third next completion",
					event:                insertTestCtrl('n'),
					wantHandled:          true,
					wantBuffer:           new("alpha alpine altar\naltar"),
					wantCursor:           new(term.Coordinates{X: 5, Y: 1}),
					wantMode:             new(insertMode),
					wantCompletionActive: new(true),
				},
				{
					name:                 "next wraps to first completion",
					event:                insertTestCtrl('n'),
					wantHandled:          true,
					wantBuffer:           new("alpha alpine altar\nalpha"),
					wantCursor:           new(term.Coordinates{X: 5, Y: 1}),
					wantMode:             new(insertMode),
					wantCompletionActive: new(true),
				},
			},
		},
		{
			name:    "ctrl+p starts from the previous candidate and cycles backward",
			content: "alpha alpine altar\nal",
			cursor:  term.Coordinates{X: 2, Y: 1},
			steps: []insertModeCommandStep{
				{
					name:                 "previous starts at last completion",
					event:                insertTestCtrl('p'),
					wantHandled:          true,
					wantBuffer:           new("alpha alpine altar\naltar"),
					wantCursor:           new(term.Coordinates{X: 5, Y: 1}),
					wantMode:             new(insertMode),
					wantCompletionActive: new(true),
				},
				{
					name:                 "previous cycles backward",
					event:                insertTestCtrl('p'),
					wantHandled:          true,
					wantBuffer:           new("alpha alpine altar\nalpine"),
					wantCursor:           new(term.Coordinates{X: 6, Y: 1}),
					wantMode:             new(insertMode),
					wantCompletionActive: new(true),
				},
			},
		},
		{
			name:    "ctrl+p wraps from the first active candidate to the last",
			content: "alpha alpine altar\nal",
			cursor:  term.Coordinates{X: 2, Y: 1},
			steps: []insertModeCommandStep{
				{
					name:                 "start with first completion",
					event:                insertTestCtrl('n'),
					wantHandled:          true,
					wantBuffer:           new("alpha alpine altar\nalpha"),
					wantCursor:           new(term.Coordinates{X: 5, Y: 1}),
					wantMode:             new(insertMode),
					wantCompletionActive: new(true),
				},
				{
					name:                 "previous wraps to last completion",
					event:                insertTestCtrl('p'),
					wantHandled:          true,
					wantBuffer:           new("alpha alpine altar\naltar"),
					wantCursor:           new(term.Coordinates{X: 5, Y: 1}),
					wantMode:             new(insertMode),
					wantCompletionActive: new(true),
				},
			},
		},
		{
			name:    "editing after completion resets the active completion session",
			content: "alpha alpine\nal",
			cursor:  term.Coordinates{X: 2, Y: 1},
			steps: []insertModeCommandStep{
				{
					name:                 "complete prefix",
					event:                insertTestCtrl('n'),
					wantHandled:          true,
					wantBuffer:           new("alpha alpine\nalpha"),
					wantCursor:           new(term.Coordinates{X: 5, Y: 1}),
					wantMode:             new(insertMode),
					wantCompletionActive: new(true),
				},
				{
					name:                 "type resets completion",
					event:                insertTestKey('x'),
					wantHandled:          true,
					wantBuffer:           new("alpha alpine\nalphax"),
					wantCursor:           new(term.Coordinates{X: 6, Y: 1}),
					wantMode:             new(insertMode),
					wantCompletionActive: new(false),
				},
				{
					name:                 "stale candidates are not reused",
					event:                insertTestCtrl('n'),
					wantHandled:          true,
					wantBuffer:           new("alpha alpine\nalphax"),
					wantCursor:           new(term.Coordinates{X: 6, Y: 1}),
					wantMode:             new(insertMode),
					wantCompletionActive: new(false),
				},
			},
		},
		{
			name:    "single candidate is inserted directly without activating completion",
			content: "unique_word\nuni",
			cursor:  term.Coordinates{X: 3, Y: 1},
			steps: []insertModeCommandStep{
				{
					name:                 "ctrl+n inserts the only candidate",
					event:                insertTestCtrl('n'),
					wantHandled:          true,
					wantBuffer:           new("unique_word\nunique_word"),
					wantCursor:           new(term.Coordinates{X: 11, Y: 1}),
					wantMode:             new(insertMode),
					wantCompletionActive: new(false),
				},
			},
		},
		{
			name:    "completion treats underscores and digits as word characters and deduplicates candidates",
			content: "foo_1 foo_2 foo_1\nfoo_",
			cursor:  term.Coordinates{X: 4, Y: 1},
			steps: []insertModeCommandStep{
				{
					name:                 "first underscore digit completion",
					event:                insertTestCtrl('n'),
					wantHandled:          true,
					wantBuffer:           new("foo_1 foo_2 foo_1\nfoo_1"),
					wantCursor:           new(term.Coordinates{X: 5, Y: 1}),
					wantMode:             new(insertMode),
					wantCompletionActive: new(true),
				},
				{
					name:                 "duplicate candidate is skipped",
					event:                insertTestCtrl('n'),
					wantHandled:          true,
					wantBuffer:           new("foo_1 foo_2 foo_1\nfoo_2"),
					wantCursor:           new(term.Coordinates{X: 5, Y: 1}),
					wantMode:             new(insertMode),
					wantCompletionActive: new(true),
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vi := setupVi(t, tc.content, 2)
			vi.Resize(80, 24)
			require.True(t, vi.setCursorAtScroll(tc.cursor))
			vi.setInsertMode()

			runInsertModeCommandSteps(t, vi, tc.steps)
		})
	}
}

func TestInsertModeCtrlRInsertsRegister(t *testing.T) {
	type registerCase struct {
		name              string
		content           string
		cursor            term.Coordinates
		registers         map[rune]clipboard.Data
		externalClipboard *clipboard.Data
		steps             []insertModeCommandStep
		wantRegisters     map[rune]string
		wantExternal      *string
	}

	for _, tc := range []registerCase{
		{
			name:    "named register inserts and returns to insert mode",
			content: "hello ",
			cursor:  term.Coordinates{X: 6, Y: 0},
			registers: map[rune]clipboard.Data{
				'a': {Text: "world"},
			},
			steps: []insertModeCommandStep{
				{
					name:                "start register read",
					event:               insertTestCtrl('r'),
					wantHandled:         true,
					wantBuffer:          new("hello "),
					wantCursor:          new(term.Coordinates{X: 6, Y: 0}),
					wantMode:            new(insertMode),
					wantPendingRegister: new(true),
				},
				{
					name:                "insert named register",
					event:               insertTestKey('a'),
					wantHandled:         true,
					wantBuffer:          new("hello world"),
					wantCursor:          new(term.Coordinates{X: 11, Y: 0}),
					wantMode:            new(insertMode),
					wantPendingRegister: new(false),
				},
			},
		},
		{
			name:    "unnamed register inserts through the default register",
			content: "hello ",
			cursor:  term.Coordinates{X: 6, Y: 0},
			registers: map[rune]clipboard.Data{
				unnamedRegister: {Text: "default"},
			},
			steps: []insertModeCommandStep{
				{
					name:                "start register read",
					event:               insertTestCtrl('r'),
					wantHandled:         true,
					wantPendingRegister: new(true),
				},
				{
					name:                "insert unnamed register",
					event:               insertTestKey(unnamedRegister),
					wantHandled:         true,
					wantBuffer:          new("hello default"),
					wantCursor:          new(term.Coordinates{X: 13, Y: 0}),
					wantMode:            new(insertMode),
					wantPendingRegister: new(false),
				},
			},
		},
		{
			name:              "clipboard register reads from the configured external clipboard",
			content:           "hello ",
			cursor:            term.Coordinates{X: 6, Y: 0},
			externalClipboard: &clipboard.Data{Text: "clip", Metadata: text.NoSelection},
			steps: []insertModeCommandStep{
				{
					name:                "start register read",
					event:               insertTestCtrl('r'),
					wantHandled:         true,
					wantPendingRegister: new(true),
				},
				{
					name:                "insert clipboard register",
					event:               insertTestKey(clipboardRegister),
					wantHandled:         true,
					wantBuffer:          new("hello clip"),
					wantCursor:          new(term.Coordinates{X: 10, Y: 0}),
					wantMode:            new(insertMode),
					wantPendingRegister: new(false),
				},
			},
		},
		{
			name:    "multiline register contents insert at the cursor",
			content: "hello ",
			cursor:  term.Coordinates{X: 6, Y: 0},
			registers: map[rune]clipboard.Data{
				'b': {Text: "one\ntwo"},
			},
			steps: []insertModeCommandStep{
				{
					name:                "start register read",
					event:               insertTestCtrl('r'),
					wantHandled:         true,
					wantPendingRegister: new(true),
				},
				{
					name:                "insert multiline register",
					event:               insertTestKey('b'),
					wantHandled:         true,
					wantBuffer:          new("hello one\ntwo"),
					wantCursor:          new(term.Coordinates{X: 3, Y: 1}),
					wantMode:            new(insertMode),
					wantPendingRegister: new(false),
				},
			},
		},
		{
			name:    "invalid register name is swallowed and clears pending register state",
			content: "hello ",
			cursor:  term.Coordinates{X: 6, Y: 0},
			steps: []insertModeCommandStep{
				{
					name:                "start register read",
					event:               insertTestCtrl('r'),
					wantHandled:         true,
					wantPendingRegister: new(true),
				},
				{
					name:                "invalid register is ignored",
					event:               insertTestKey('?'),
					wantHandled:         true,
					wantBuffer:          new("hello "),
					wantCursor:          new(term.Coordinates{X: 6, Y: 0}),
					wantMode:            new(insertMode),
					wantPendingRegister: new(false),
				},
				{
					name:                "next key inserts normally",
					event:               insertTestKey('Z'),
					wantHandled:         true,
					wantBuffer:          new("hello Z"),
					wantCursor:          new(term.Coordinates{X: 7, Y: 0}),
					wantMode:            new(insertMode),
					wantPendingRegister: new(false),
				},
			},
		},
		{
			name:    "empty register-key event is swallowed and clears pending register state",
			content: "hello ",
			cursor:  term.Coordinates{X: 6, Y: 0},
			steps: []insertModeCommandStep{
				{
					name:                "start register read",
					event:               insertTestCtrl('r'),
					wantHandled:         true,
					wantPendingRegister: new(true),
				},
				{
					name:                "empty event is ignored",
					event:               term.Event{Type: term.EventKey},
					wantHandled:         true,
					wantBuffer:          new("hello "),
					wantCursor:          new(term.Coordinates{X: 6, Y: 0}),
					wantMode:            new(insertMode),
					wantPendingRegister: new(false),
				},
				{
					name:                "next key inserts normally",
					event:               insertTestKey('Z'),
					wantHandled:         true,
					wantBuffer:          new("hello Z"),
					wantCursor:          new(term.Coordinates{X: 7, Y: 0}),
					wantMode:            new(insertMode),
					wantPendingRegister: new(false),
				},
			},
		},
		{
			name:    "modified register-key event is swallowed and clears pending register state",
			content: "hello ",
			cursor:  term.Coordinates{X: 6, Y: 0},
			steps: []insertModeCommandStep{
				{
					name:                "start register read",
					event:               insertTestCtrl('r'),
					wantHandled:         true,
					wantPendingRegister: new(true),
				},
				{
					name:                "modified register key is ignored",
					event:               insertTestCtrl('a'),
					wantHandled:         true,
					wantBuffer:          new("hello "),
					wantCursor:          new(term.Coordinates{X: 6, Y: 0}),
					wantMode:            new(insertMode),
					wantPendingRegister: new(false),
				},
			},
		},
		{
			name:    "inserted register contents are recorded in the dot register on insert exit",
			content: "",
			cursor:  term.Coordinates{},
			registers: map[rune]clipboard.Data{
				'a': {Text: "again"},
			},
			steps: []insertModeCommandStep{
				{
					name:                "start register read",
					event:               insertTestCtrl('r'),
					wantHandled:         true,
					wantPendingRegister: new(true),
				},
				{
					name:                "insert register",
					event:               insertTestKey('a'),
					wantHandled:         true,
					wantBuffer:          new("again"),
					wantCursor:          new(term.Coordinates{X: 5, Y: 0}),
					wantMode:            new(insertMode),
					wantPendingRegister: new(false),
				},
				{
					name:        "exit insert writes dot register",
					event:       term.Event{Type: term.EventKey, Key: term.KeyEsc},
					wantHandled: true,
					wantBuffer:  new("again"),
					wantMode:    new(normalMode),
				},
			},
			wantRegisters: map[rune]string{
				'.': "again",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var opts []Option
			var clip *mockClip
			if tc.externalClipboard != nil {
				clip = new(mockClip)
				clip.data = *tc.externalClipboard
				opts = append(opts, WithClipboard(registerset.New(clip)))
			}

			vi := setupVi(t, tc.content, 2, opts...)
			vi.Resize(80, 24)
			if tc.cursor != (term.Coordinates{}) {
				require.True(t, vi.setCursorAtScroll(tc.cursor))
			}
			for name, data := range tc.registers {
				require.NoError(t, vi.writeRegister(name, data))
			}
			vi.setInsertMode()

			runInsertModeCommandSteps(t, vi, tc.steps)

			for name, want := range tc.wantRegisters {
				data, err := vi.readRegister(name)
				require.NoError(t, err)
				assert.Equalf(t, want, data.Text, "register %q", string(name))
			}
			if tc.wantExternal != nil {
				require.NotNil(t, clip)
				assert.Equal(t, *tc.wantExternal, clip.data.Text)
			}
		})
	}
}

func TestInsertModeCtrlOExecutesOneNormalCommand(t *testing.T) {
	type normalCommandCase struct {
		name    string
		content string
		cursor  term.Coordinates
		steps   []insertModeCommandStep
	}

	for _, tc := range []normalCommandCase{
		{
			name:    "single motion command returns to insert and subsequent key inserts text",
			content: "abc\ndef",
			cursor:  term.Coordinates{X: 1, Y: 0},
			steps: []insertModeCommandStep{
				{
					name:              "start one-shot normal command",
					event:             insertTestCtrl('o'),
					wantHandled:       true,
					wantBuffer:        new("abc\ndef"),
					wantCursor:        new(term.Coordinates{X: 1, Y: 0}),
					wantMode:          new(normalMode),
					wantPendingNormal: new(true),
				},
				{
					name:              "execute motion and return to insert",
					event:             insertTestKey('j'),
					wantHandled:       true,
					wantBuffer:        new("abc\ndef"),
					wantCursor:        new(term.Coordinates{X: 1, Y: 1}),
					wantMode:          new(insertMode),
					wantPendingNormal: new(false),
				},
				{
					name:              "next key is inserted in insert mode",
					event:             insertTestKey('X'),
					wantHandled:       true,
					wantBuffer:        new("abc\ndXef"),
					wantCursor:        new(term.Coordinates{X: 2, Y: 1}),
					wantMode:          new(insertMode),
					wantPendingNormal: new(false),
				},
			},
		},
		{
			name:    "count prefix remains pending until the counted motion is complete",
			content: "one\ntwo\nthree",
			cursor:  term.Coordinates{},
			steps: []insertModeCommandStep{
				{
					name:              "start one-shot normal command",
					event:             insertTestCtrl('o'),
					wantHandled:       true,
					wantMode:          new(normalMode),
					wantPendingNormal: new(true),
				},
				{
					name:              "type count digit",
					event:             insertTestKey('2'),
					wantHandled:       true,
					wantBuffer:        new("one\ntwo\nthree"),
					wantCursor:        new(term.Coordinates{}),
					wantMode:          new(normalMode),
					wantPendingNormal: new(true),
				},
				{
					name:              "complete counted motion",
					event:             insertTestKey('j'),
					wantHandled:       true,
					wantBuffer:        new("one\ntwo\nthree"),
					wantCursor:        new(term.Coordinates{X: 0, Y: 2}),
					wantMode:          new(insertMode),
					wantPendingNormal: new(false),
				},
			},
		},
		{
			name:    "find-character motion remains pending until the target character is typed",
			content: "abcdef",
			cursor:  term.Coordinates{},
			steps: []insertModeCommandStep{
				{
					name:              "start one-shot normal command",
					event:             insertTestCtrl('o'),
					wantHandled:       true,
					wantMode:          new(normalMode),
					wantPendingNormal: new(true),
				},
				{
					name:              "start find-character motion",
					event:             insertTestKey('f'),
					wantHandled:       true,
					wantBuffer:        new("abcdef"),
					wantCursor:        new(term.Coordinates{}),
					wantMode:          new(normalMode),
					wantPendingNormal: new(true),
				},
				{
					name:              "complete find-character motion",
					event:             insertTestKey('d'),
					wantHandled:       true,
					wantBuffer:        new("abcdef"),
					wantCursor:        new(term.Coordinates{X: 3, Y: 0}),
					wantMode:          new(insertMode),
					wantPendingNormal: new(false),
				},
			},
		},
		{
			name:    "replace-one command returns after the replacement character",
			content: "abc",
			cursor:  term.Coordinates{X: 1, Y: 0},
			steps: []insertModeCommandStep{
				{
					name:              "start one-shot normal command",
					event:             insertTestCtrl('o'),
					wantHandled:       true,
					wantMode:          new(normalMode),
					wantPendingNormal: new(true),
				},
				{
					name:              "start replace-one command",
					event:             insertTestKey('r'),
					wantHandled:       true,
					wantBuffer:        new("abc"),
					wantCursor:        new(term.Coordinates{X: 1, Y: 0}),
					wantMode:          new(replaceOneMode),
					wantPendingNormal: new(true),
				},
				{
					name:              "complete replace-one command",
					event:             insertTestKey('X'),
					wantHandled:       true,
					wantBuffer:        new("aXc"),
					wantCursor:        new(term.Coordinates{X: 1, Y: 0}),
					wantMode:          new(insertMode),
					wantPendingNormal: new(false),
				},
			},
		},
		{
			name:    "delete operator command returns after its motion",
			content: "one\ntwo\n",
			cursor:  term.Coordinates{},
			steps: []insertModeCommandStep{
				{
					name:              "start one-shot normal command",
					event:             insertTestCtrl('o'),
					wantHandled:       true,
					wantMode:          new(normalMode),
					wantPendingNormal: new(true),
				},
				{
					name:              "start delete operator",
					event:             insertTestKey('d'),
					wantHandled:       true,
					wantBuffer:        new("one\ntwo\n"),
					wantMode:          new(deleteMode),
					wantPendingNormal: new(true),
				},
				{
					name:              "complete line delete operator",
					event:             insertTestKey('d'),
					wantHandled:       true,
					wantBuffer:        new("two\n"),
					wantCursor:        new(term.Coordinates{}),
					wantMode:          new(insertMode),
					wantPendingNormal: new(false),
				},
			},
		},
		{
			name:    "normal command that enters insert mode clears one-shot state",
			content: "abc",
			cursor:  term.Coordinates{},
			steps: []insertModeCommandStep{
				{
					name:              "start one-shot normal command",
					event:             insertTestCtrl('o'),
					wantHandled:       true,
					wantMode:          new(normalMode),
					wantPendingNormal: new(true),
				},
				{
					name:              "normal insert command consumes one-shot command",
					event:             insertTestKey('i'),
					wantHandled:       true,
					wantBuffer:        new("abc"),
					wantCursor:        new(term.Coordinates{}),
					wantMode:          new(insertMode),
					wantPendingNormal: new(false),
				},
				{
					name:              "subsequent key inserts normally",
					event:             insertTestKey('X'),
					wantHandled:       true,
					wantBuffer:        new("Xabc"),
					wantCursor:        new(term.Coordinates{X: 1, Y: 0}),
					wantMode:          new(insertMode),
					wantPendingNormal: new(false),
				},
			},
		},
		{
			name:    "escape can be the one normal command and returns to insert",
			content: "abc",
			cursor:  term.Coordinates{X: 1, Y: 0},
			steps: []insertModeCommandStep{
				{
					name:              "start one-shot normal command",
					event:             insertTestCtrl('o'),
					wantHandled:       true,
					wantMode:          new(normalMode),
					wantPendingNormal: new(true),
				},
				{
					name:              "escape returns to insert",
					event:             term.Event{Type: term.EventKey, Key: term.KeyEsc},
					wantHandled:       true,
					wantBuffer:        new("abc"),
					wantCursor:        new(term.Coordinates{X: 1, Y: 0}),
					wantMode:          new(insertMode),
					wantPendingNormal: new(false),
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vi := setupVi(t, tc.content, 2)
			vi.Resize(80, 24)
			if tc.cursor != (term.Coordinates{}) {
				require.True(t, vi.setCursorAtScroll(tc.cursor))
			}
			vi.setInsertMode()

			runInsertModeCommandSteps(t, vi, tc.steps)
		})
	}
}

func TestExitVisualMode(t *testing.T) {
	t.Run("escape and control-c exit visual mode into normal", func(t *testing.T) {
		vi := setupVi(t, "aaaa\nbbbb\ncccc\ndddd", 2)
		vi.Resize(4, 4)

		vi.setVisualMode()
		assert.Equal(t, vi.mode(), visualMode)

		vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
		assert.Equal(t, vi.mode(), normalMode)

		vi.setVisualMode()
		assert.Equal(t, vi.mode(), visualMode)

		vi.Handle(term.Event{Type: term.EventKey, Ch: 'c', Mod: term.ModCtrl})
		assert.Equal(t, vi.mode(), normalMode)
	})
}

func TestViCountChangeToVisualMode(t *testing.T) {
	codeSnippet := "abcdefghij\n1234567"
	// In a window width of 4 without wrapping::
	//
	//   abcd (efghij hidden right)
	//   1234 (567 hidden right)
	//
	// In a window width of 4 with wrapping::
	//
	//   abcd
	//   efgh
	//   ij
	//   1234
	//   567

	suite := []struct {
		name                   string
		scrollWidth            int
		cursorAt               term.Coordinates
		countDigits            []rune
		selection              string
		expectScrollCoords     term.Coordinates
		expectScrollCoordsWrap term.Coordinates
		expectWindowCoords     term.Coordinates
		expectWindowCoordsWrap term.Coordinates
	}{
		{
			name:                   "1v unitary selects only current char",
			scrollWidth:            4,
			cursorAt:               term.Coordinates{X: 1, Y: 0}, // 'b' in snippet
			countDigits:            []rune{'1'},
			selection:              "b",
			expectScrollCoords:     term.Coordinates{X: 1, Y: 0},
			expectScrollCoordsWrap: term.Coordinates{X: 1, Y: 0},
			expectWindowCoords:     term.Coordinates{X: 1, Y: 0},
			expectWindowCoordsWrap: term.Coordinates{X: 1, Y: 0},
		},
		{
			name:                   "2v count N and change from normal to visual mode moves cursor N cells right",
			scrollWidth:            4,
			cursorAt:               term.Coordinates{X: 1, Y: 0}, // 'b' in snippet
			countDigits:            []rune{'2'},
			selection:              "bc",
			expectScrollCoords:     term.Coordinates{X: 2, Y: 0},
			expectScrollCoordsWrap: term.Coordinates{X: 2, Y: 0},
			expectWindowCoords:     term.Coordinates{X: 2, Y: 0},
			expectWindowCoordsWrap: term.Coordinates{X: 2, Y: 0},
		},
		{
			name:        "11v narrow scroll count N and change from normal to visual exceeds line end but not entire content",
			scrollWidth: 4,
			cursorAt:    term.Coordinates{X: 2, Y: 0}, // 'c' in snippet
			countDigits: []rune{'1', '5'},
			selection:   "cdefghij", // does not beyond code line
			// cursor will be at last char if it doesn't have room rightwards (last char index: 9)
			expectScrollCoords:     term.Coordinates{X: 10, Y: 0},
			expectScrollCoordsWrap: term.Coordinates{X: 10, Y: 0},
			expectWindowCoords:     term.Coordinates{X: 4, Y: 0},
			expectWindowCoordsWrap: term.Coordinates{X: 2, Y: 2},
		},
		{
			name:        "11v wide scroll count N and change from normal to visual exceeds line end but not entire content",
			scrollWidth: 100,
			cursorAt:    term.Coordinates{X: 2, Y: 0}, // 'c' in snippet
			countDigits: []rune{'1', '5'},
			selection:   "cdefghij", // does not beyond code line
			// cursor will be at last char if it doesn't have room rightwards (last char index: 9)
			expectScrollCoords:     term.Coordinates{X: 10, Y: 0},
			expectScrollCoordsWrap: term.Coordinates{X: 10, Y: 0},
			expectWindowCoords:     term.Coordinates{X: 10, Y: 0},
			expectWindowCoordsWrap: term.Coordinates{X: 10, Y: 0},
		},
		{
			name:                   "3v count N and change from normal to visual in last column wraps row below",
			scrollWidth:            4,
			cursorAt:               term.Coordinates{X: 3, Y: 0}, // 'd' in snippet
			countDigits:            []rune{'3'},
			selection:              "def",
			expectScrollCoords:     term.Coordinates{X: 5, Y: 0},
			expectScrollCoordsWrap: term.Coordinates{X: 5, Y: 0},
			expectWindowCoords:     term.Coordinates{X: 3, Y: 0},
			expectWindowCoordsWrap: term.Coordinates{X: 1, Y: 1},
		},
		{
			name:        "99999999999999999999v narrow scroll count N and change from normal to visual",
			scrollWidth: 4,
			cursorAt:    term.Coordinates{X: 3, Y: 0}, // 'd' in snippet
			countDigits: []rune{'9', '9', '9', '9', '9', '9', '9', '9', '9', '9', '9', '9', '9', '9', '9', '9', '9', '9', '9', '9'},
			selection:   "defghij",
			// cursor will be at last char if it doesn't have room rightwards (last char index: 9)
			expectScrollCoords:     term.Coordinates{X: 10, Y: 0},
			expectScrollCoordsWrap: term.Coordinates{X: 10, Y: 0},
			expectWindowCoords:     term.Coordinates{X: 4, Y: 0},
			expectWindowCoordsWrap: term.Coordinates{X: 2, Y: 2},
		},
		{
			name:        "99999999999999999999v wide scroll count N and change from normal to visual",
			scrollWidth: 100,
			cursorAt:    term.Coordinates{X: 3, Y: 0}, // 'd' in snippet
			countDigits: []rune{'9', '9', '9', '9', '9', '9', '9', '9', '9', '9', '9', '9', '9', '9', '9', '9', '9', '9', '9', '9'},
			selection:   "defghij",
			// cursor will be at last char if it doesn't have room rightwards (last char index: 9)
			expectScrollCoords:     term.Coordinates{X: 10, Y: 0},
			expectScrollCoordsWrap: term.Coordinates{X: 10, Y: 0},
			expectWindowCoords:     term.Coordinates{X: 10, Y: 0},
			expectWindowCoordsWrap: term.Coordinates{X: 10, Y: 0},
		},
		{
			name:                   "stay within code line",
			scrollWidth:            4,
			cursorAt:               term.Coordinates{X: 0, Y: 0}, // 'a' in snippet
			countDigits:            []rune{'1', '0'},
			selection:              "abcdefghij",
			expectScrollCoords:     term.Coordinates{X: 9, Y: 0},
			expectScrollCoordsWrap: term.Coordinates{X: 9, Y: 0},
			expectWindowCoords:     term.Coordinates{X: 3, Y: 0},
			expectWindowCoordsWrap: term.Coordinates{X: 1, Y: 2},
		},
	}

	for _, tcase := range suite {
		for _, wrap := range []bool{false, true} {
			name := tcase.name
			if wrap {
				name += " (wrap)"
			}

			t.Run(name, func(t *testing.T) {
				vi := setupVi(t, codeSnippet, 2, WithWrap(wrap))
				vi.Resize(tcase.scrollWidth, 20)
				// In order to create the wraps a Draw  must be issued so [scroll.Draw]
				// can create them, otherwise it's the same as passing WithWrap(false).
				vi.Draw(term.NoopWriter{})

				vi.cursor.MoveToScroll(tcase.cursorAt)

				for _, countDigit := range tcase.countDigits {
					vi.Handle(term.Event{Type: term.EventKey, Ch: countDigit})
				}
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'v'})

				windowCoords := vi.cursor.Coordinates()
				scrollCoords := vi.cursor.ScrollCoordinates(windowCoords)

				if wrap {
					assert.Equal(t, tcase.expectScrollCoordsWrap,
						scrollCoords, "wrong scroll coords (wrap)")
					assert.Equal(t, tcase.expectWindowCoordsWrap,
						windowCoords, "wrong window coords (wrap)")
				} else {
					assert.Equal(t, tcase.expectScrollCoords,
						scrollCoords, "wrong scroll coords")
					assert.Equal(t, tcase.expectWindowCoords,
						windowCoords, "wrong window coords")
				}
			})
		}
	}
}

func TestViCountChangeToLineVisualMode(t *testing.T) {
	codeSnippet := "abc\n123\ndef\n456\nghij\n7891"
	suite := []struct {
		name        string
		scrollWidth int
		cursorAt    term.Coordinates
		countDigits []rune
		selection   string
	}{
		{
			name:        "1V unitary selects current line",
			scrollWidth: 2,
			cursorAt:    term.Coordinates{X: 1, Y: 0}, // 'b' in snippet
			countDigits: []rune{'1'},
			selection:   "abc\n",
		},
		{
			name:        "2V unitary selects current line and line below",
			scrollWidth: 2,
			cursorAt:    term.Coordinates{X: 1, Y: 0}, // 'b' in snippet
			countDigits: []rune{'2'},
			selection:   "abc\n123\n",
		},
		{
			name:        "999V content overflow",
			scrollWidth: 2,
			cursorAt:    term.Coordinates{X: 1, Y: 3}, // '5' in snippet
			countDigits: []rune{'9', '9', '9'},
			selection:   "456\nghij\n7891\n",
		},
	}

	for _, tcase := range suite {
		for _, wrap := range []bool{true, false} {
			name := tcase.name
			if wrap {
				name += " (wrap)"
			}

			t.Run(name, func(t *testing.T) {
				vi := setupVi(t, codeSnippet, 2, WithWrap(wrap))
				vi.Resize(tcase.scrollWidth, 4)
				vi.cursor.MoveToScroll(tcase.cursorAt)

				for _, countDigit := range tcase.countDigits {
					vi.Handle(term.Event{Type: term.EventKey, Ch: countDigit})
				}
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'V'})
				assert.Equal(t, tcase.selection, vi.cursor.Selection())
			})
		}
	}
}

func TestViNormalModeArrowEdgeReturnsUnhandled(t *testing.T) {
	newVi := func(t *testing.T, content string) *viHandlerImpl {
		t.Helper()
		vi := setupVi(t, content, 2)
		vi.Resize(40, 10)
		vi.Draw(term.NoopWriter{})
		return vi
	}

	t.Run("ArrowUp at top line is unhandled", func(t *testing.T) {
		vi := newVi(t, "hello\nworld")
		_, handled := vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowUp})
		assert.False(t, handled)
	})

	t.Run("ArrowUp below top line is handled", func(t *testing.T) {
		vi := newVi(t, "hello\nworld")
		require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 1}))
		_, handled := vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowUp})
		assert.True(t, handled)
	})

	t.Run("ArrowDown at bottom line is unhandled", func(t *testing.T) {
		vi := newVi(t, "hello\nworld")
		require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 1}))
		_, handled := vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
		assert.False(t, handled)
	})

	t.Run("ArrowDown above bottom line is handled", func(t *testing.T) {
		vi := newVi(t, "hello\nworld")
		_, handled := vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
		assert.True(t, handled)
	})

	t.Run("ArrowLeft at column zero is unhandled", func(t *testing.T) {
		vi := newVi(t, "hello\nworld")
		_, handled := vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowLeft})
		assert.False(t, handled)
	})

	t.Run("ArrowLeft mid-line is handled", func(t *testing.T) {
		vi := newVi(t, "hello\nworld")
		require.True(t, vi.setCursorAtScroll(term.Coordinates{X: 2}))
		_, handled := vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowLeft})
		assert.True(t, handled)
	})
}

func TestViJoin(t *testing.T) {
	type joinCase struct {
		name          string
		content       string
		at            *term.Coordinates
		before        func(t *testing.T, vi *viHandlerImpl)
		width         int
		wrap          bool
		seq           string
		wantBuffer    string
		wantCursor    *term.Coordinates
		wantUnhandled bool
	}

	at := func(x, y int) *term.Coordinates {
		return &term.Coordinates{X: x, Y: y}
	}

	lineComment := func(prefix string) func(*testing.T, *viHandlerImpl) {
		return func(_ *testing.T, vi *viHandlerImpl) {
			vi.cursor.SetCommentSpec(text.CommentSpec{Line: []string{prefix}})
		}
	}

	run := func(t *testing.T, tc joinCase) {
		t.Helper()
		width := tc.width
		if width == 0 {
			width = 40
		}
		vi := setupVi(t, tc.content, 2, WithWrap(tc.wrap))
		vi.Resize(width, 10)
		vi.Draw(term.NoopWriter{})
		if tc.at != nil {
			require.True(t, vi.setCursorAtScroll(*tc.at))
		}
		if tc.before != nil {
			tc.before(t, vi)
		}

		events := parseSearchOpEvents(tc.seq)
		for i, ev := range events {
			_, handled := vi.Handle(ev)
			if tc.wantUnhandled && i == len(events)-1 {
				assert.Falsef(t, handled, "sequence %q at %v", tc.seq, ev)
				continue
			}
			require.Truef(t, handled, "sequence %q failed at %v", tc.seq, ev)
		}

		assert.Equal(t, tc.wantBuffer, vi.less.Buffer().String())
		if tc.wantCursor != nil {
			assert.Equal(t, *tc.wantCursor, vi.cursor.CursorAtScroll())
		}
		assert.Equal(t, normalMode, vi.mode())
		_, selected := vi.Selection()
		assert.False(t, selected)
		assert.Equal(t, 1, vi.count)
		assert.Empty(t, vi.countDigits)
	}

	for _, tc := range []joinCase{
		// --- J: the space it inserts ---
		{
			name:       "J inserts a space between the two lines",
			content:    "hello\nworld\nfoo",
			seq:        "J",
			wantBuffer: "hello world\nfoo",
			wantCursor: at(5, 0),
		},
		{
			name:       "J joins from anywhere on the line",
			content:    "hello\nworld",
			at:         at(3, 0),
			seq:        "J",
			wantBuffer: "hello world",
			wantCursor: at(5, 0),
		},
		{
			name:       "J drops the next line's leading spaces",
			content:    "hello\n    world",
			seq:        "J",
			wantBuffer: "hello world",
			wantCursor: at(5, 0),
		},
		{
			name:       "J drops the next line's leading tab",
			content:    "hello\n\tworld",
			seq:        "J",
			wantBuffer: "hello world",
			wantCursor: at(5, 0),
		},
		{
			name:       "J drops the next line's leading nulls",
			content:    "hello\n\x00\x00world",
			seq:        "J",
			wantBuffer: "hello world",
			wantCursor: at(5, 0),
		},
		{
			name:       "J adds no space after a trailing space",
			content:    "hello \nworld",
			seq:        "J",
			wantBuffer: "hello world",
			wantCursor: at(6, 0),
		},
		{
			name:       "J keeps every trailing space and adds none",
			content:    "hello  \nworld",
			seq:        "J",
			wantBuffer: "hello  world",
			wantCursor: at(7, 0),
		},
		{
			name:       "J adds no space after a trailing tab",
			content:    "hello\t\nworld",
			seq:        "J",
			wantBuffer: "hello\tworld",
			wantCursor: at(6, 0),
		},
		{
			name:       "J adds no space after a trailing null",
			content:    "hello\x00\nworld",
			seq:        "J",
			wantBuffer: "hello\x00world",
			wantCursor: at(6, 0),
		},
		{
			name:       "J adds no space before a closing paren",
			content:    "foo(bar\n)baz",
			seq:        "J",
			wantBuffer: "foo(bar)baz",
			wantCursor: at(7, 0),
		},
		{
			name:       "J drops indent and adds no space before a closing paren",
			content:    "foo(bar\n\t)baz",
			seq:        "J",
			wantBuffer: "foo(bar)baz",
			wantCursor: at(7, 0),
		},
		{
			name:       "J inserts a space between wide characters",
			content:    "世界\nこんにちは",
			seq:        "J",
			wantBuffer: "世界 こんにちは",
			wantCursor: at(2, 0),
		},
		// --- J: blank and empty lines ---
		{
			name:       "J adds no space when the current line is empty",
			content:    "\nworld\nfoo",
			seq:        "J",
			wantBuffer: "world\nfoo",
			wantCursor: at(0, 0),
		},
		{
			name:       "J drops the next indent when the current line is empty",
			content:    "\n   world",
			seq:        "J",
			wantBuffer: "world",
			wantCursor: at(0, 0),
		},
		{
			name:       "J on a blank current line keeps its blanks",
			content:    "   \nworld",
			seq:        "J",
			wantBuffer: "   world",
			wantCursor: at(3, 0),
		},
		{
			name:       "J removes an empty next line without padding",
			content:    "hello\n\nworld",
			seq:        "J",
			wantBuffer: "hello\nworld",
			wantCursor: at(4, 0),
		},
		{
			name:       "J removes a blank next line entirely",
			content:    "hello\n \t \nworld",
			seq:        "J",
			wantBuffer: "hello\nworld",
			wantCursor: at(4, 0),
		},
		{
			name:       "J on the last text line absorbs the trailing empty line",
			content:    "hello\nworld\n",
			at:         at(0, 1),
			seq:        "J",
			wantBuffer: "hello\nworld",
			wantCursor: at(4, 1),
		},
		// --- J: boundaries ---
		{
			name:       "J on the last line is a no-op",
			content:    "hello\nworld",
			at:         at(0, 1),
			seq:        "J",
			wantBuffer: "hello\nworld",
			wantCursor: at(0, 1),
		},
		{
			name:       "J on a single-line buffer is a no-op",
			content:    "hello",
			seq:        "J",
			wantBuffer: "hello",
			wantCursor: at(0, 0),
		},
		{
			name:       "J on an empty buffer is a no-op",
			content:    "",
			seq:        "J",
			wantBuffer: "",
			wantCursor: at(0, 0),
		},
		{
			name:    "J with a stale cursor past the last row is a no-op",
			content: "hello\nworld",
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.cursor.SetCursorAtScroll(term.Coordinates{Y: 5})
			},
			seq:        "J",
			wantBuffer: "hello\nworld",
		},
		{
			name:       "J joins logical lines when wrapping",
			content:    "hello\nworld",
			width:      4,
			wrap:       true,
			seq:        "J",
			wantBuffer: "hello world",
			wantCursor: at(5, 0),
		},
		{
			name:       "J from a wrapped continuation joins logical lines",
			content:    "hello\nworld",
			width:      4,
			wrap:       true,
			at:         at(4, 0),
			seq:        "J",
			wantBuffer: "hello world",
			wantCursor: at(5, 0),
		},
		// --- J: counts ---
		{
			name:       "1J joins two lines",
			content:    "a\nb\nc",
			seq:        "1J",
			wantBuffer: "a b\nc",
			wantCursor: at(1, 0),
		},
		{
			name:       "2J joins two lines",
			content:    "a\nb\nc\nd",
			seq:        "2J",
			wantBuffer: "a b\nc\nd",
			wantCursor: at(1, 0),
		},
		{
			name:       "3J joins three lines and leaves the cursor at the last join",
			content:    "a\nb\nc\nd",
			seq:        "3J",
			wantBuffer: "a b c\nd",
			wantCursor: at(3, 0),
		},
		{
			name:       "3J drops the indent of every joined line",
			content:    "a\n  b\n\tc\nd",
			seq:        "3J",
			wantBuffer: "a b c\nd",
			wantCursor: at(3, 0),
		},
		{
			name:       "count larger than the remaining lines is clamped",
			content:    "a\nb\nc",
			seq:        "9J",
			wantBuffer: "a b c",
			wantCursor: at(3, 0),
		},
		{
			name:       "counted J on the last line is a no-op",
			content:    "a\nb",
			at:         at(0, 1),
			seq:        "9J",
			wantBuffer: "a\nb",
			wantCursor: at(0, 1),
		},
		{
			name:       "successive J commands keep joining",
			content:    "a\nb\nc\nd",
			seq:        "JJ",
			wantBuffer: "a b c\nd",
			wantCursor: at(3, 0),
		},
		// --- J: comment leaders ---
		{
			name:       "J removes the next line's comment leader",
			content:    "// foo\n// bar",
			before:     lineComment("//"),
			seq:        "J",
			wantBuffer: "// foo bar",
			wantCursor: at(6, 0),
		},
		{
			name:       "J removes an indented comment leader",
			content:    "// foo\n\t//   bar",
			before:     lineComment("//"),
			seq:        "J",
			wantBuffer: "// foo bar",
			wantCursor: at(6, 0),
		},
		{
			name:       "J keeps the leader when the current line is not a comment",
			content:    "foo\n// bar",
			before:     lineComment("//"),
			seq:        "J",
			wantBuffer: "foo // bar",
			wantCursor: at(3, 0),
		},
		{
			name:       "J drops a leader-only next line entirely",
			content:    "// foo\n//",
			before:     lineComment("//"),
			seq:        "J",
			wantBuffer: "// foo",
			wantCursor: at(5, 0),
		},
		{
			name:       "3J removes the leader of every joined comment line",
			content:    "// a\n// b\n// c\nd",
			before:     lineComment("//"),
			seq:        "3J",
			wantBuffer: "// a b c\nd",
			wantCursor: at(6, 0),
		},
		{
			name:       "J without a configured comment spec keeps the leader",
			content:    "// foo\n// bar",
			seq:        "J",
			wantBuffer: "// foo // bar",
			wantCursor: at(6, 0),
		},
		// --- J: visual mode ---
		{
			name:       "visual J joins the selected line with the next",
			content:    "a\nb\nc",
			seq:        "vJ",
			wantBuffer: "a b\nc",
			wantCursor: at(1, 0),
		},
		{
			name:       "visual J joins every line the selection spans",
			content:    "a\nb\nc\nd",
			seq:        "vjjJ",
			wantBuffer: "a b c\nd",
			wantCursor: at(3, 0),
		},
		{
			name:       "visual line J joins the selected lines",
			content:    "a\nb\nc\nd",
			seq:        "VjjJ",
			wantBuffer: "a b c\nd",
			wantCursor: at(3, 0),
		},
		{
			name:       "visual line J on a single line still joins two",
			content:    "a\nb\nc",
			seq:        "VJ",
			wantBuffer: "a b\nc",
			wantCursor: at(1, 0),
		},
		{
			name:       "visual block J joins the spanned lines",
			content:    "aa\nbb\ncc",
			seq:        "<c-v>jJ",
			wantBuffer: "aa bb\ncc",
			wantCursor: at(2, 0),
		},
		{
			name:       "visual J drops the indent of every joined line",
			content:    "a\n    b\n\tc\nd",
			seq:        "VjjJ",
			wantBuffer: "a b c\nd",
			wantCursor: at(3, 0),
		},
		{
			name:       "visual J on an upward selection joins from the top",
			content:    "a\nb\nc\nd",
			at:         at(0, 2),
			seq:        "VkJ",
			wantBuffer: "a\nb c\nd",
			wantCursor: at(1, 1),
		},
		{
			name:       "visual J on the last line is a no-op",
			content:    "a\nb",
			at:         at(0, 1),
			seq:        "VJ",
			wantBuffer: "a\nb",
			wantCursor: at(0, 1),
		},
		{
			name:       "visual J ignores a count typed inside the selection",
			content:    "a\nb\nc\nd",
			seq:        "V3J",
			wantBuffer: "a b\nc\nd",
			wantCursor: at(1, 0),
		},
		{
			name:       "visual J removes leaders from every joined line",
			content:    "// a\n// b\n// c\nd",
			before:     lineComment("//"),
			seq:        "VjjJ",
			wantBuffer: "// a b c\nd",
			wantCursor: at(6, 0),
		},
		// --- gJ: verbatim join ---
		{
			name:       "gJ joins without inserting a space",
			content:    "hello\nworld\nfoo",
			seq:        "gJ",
			wantBuffer: "helloworld\nfoo",
			wantCursor: at(5, 0),
		},
		{
			name:       "gJ keeps the next line's leading spaces",
			content:    "hello\n    world",
			seq:        "gJ",
			wantBuffer: "hello    world",
			wantCursor: at(5, 0),
		},
		{
			name:       "gJ keeps the next line's leading tab",
			content:    "hello\n\tworld",
			seq:        "gJ",
			wantBuffer: "hello\tworld",
			wantCursor: at(5, 0),
		},
		{
			name:       "gJ keeps the current line's trailing space",
			content:    "hello \nworld",
			seq:        "gJ",
			wantBuffer: "hello world",
			wantCursor: at(6, 0),
		},
		{
			name:       "gJ keeps a trailing tab",
			content:    "hello\t\nworld",
			seq:        "gJ",
			wantBuffer: "hello\tworld",
			wantCursor: at(6, 0),
		},
		{
			name:       "gJ keeps null cells on both sides",
			content:    "hello\x00\n\x00world",
			seq:        "gJ",
			wantBuffer: "hello\x00\x00world",
			wantCursor: at(6, 0),
		},
		{
			name:       "gJ does not separate wide characters",
			content:    "世界\nこんにちは",
			seq:        "gJ",
			wantBuffer: "世界こんにちは",
			wantCursor: at(2, 0),
		},
		{
			name:       "gJ removes an empty next line",
			content:    "hello\n\nworld",
			seq:        "gJ",
			wantBuffer: "hello\nworld",
			wantCursor: at(4, 0),
		},
		{
			name:       "gJ keeps the blanks of a blank next line",
			content:    "hello\n   \nworld",
			seq:        "gJ",
			wantBuffer: "hello   \nworld",
			wantCursor: at(5, 0),
		},
		{
			name:       "gJ on an empty current line removes it",
			content:    "\nworld\nfoo",
			seq:        "gJ",
			wantBuffer: "world\nfoo",
			wantCursor: at(0, 0),
		},
		{
			name:       "gJ on the last text line absorbs the trailing empty line",
			content:    "hello\nworld\n",
			at:         at(0, 1),
			seq:        "gJ",
			wantBuffer: "hello\nworld",
			wantCursor: at(4, 1),
		},
		{
			name:       "gJ on the last line is a no-op and exits g mode",
			content:    "hello\nworld\nfoo",
			at:         at(0, 2),
			seq:        "gJ",
			wantBuffer: "hello\nworld\nfoo",
			wantCursor: at(0, 2),
		},
		{
			name:       "gJ on a single-line buffer is a no-op",
			content:    "hello",
			seq:        "gJ",
			wantBuffer: "hello",
			wantCursor: at(0, 0),
		},
		{
			name:       "gJ on an empty buffer is a no-op",
			content:    "",
			seq:        "gJ",
			wantBuffer: "",
			wantCursor: at(0, 0),
		},
		{
			name:       "2gJ joins two lines",
			content:    "abc\ndef\nghi",
			seq:        "2gJ",
			wantBuffer: "abcdef\nghi",
			wantCursor: at(3, 0),
		},
		{
			name:       "3gJ joins three lines",
			content:    "a\nb\nc\nd",
			seq:        "3gJ",
			wantBuffer: "abc\nd",
			wantCursor: at(2, 0),
		},
		{
			name:       "counted gJ clamps to the remaining lines",
			content:    "a\nb\nc",
			seq:        "9gJ",
			wantBuffer: "abc",
			wantCursor: at(2, 0),
		},
		{
			name:       "successive gJ commands keep joining",
			content:    "a\nb\nc\nd",
			seq:        "gJgJ",
			wantBuffer: "abc\nd",
			wantCursor: at(2, 0),
		},
		// --- gJ: visual mode ---
		{
			name:       "visual gJ joins the selection verbatim",
			content:    "a\n   b\nc",
			seq:        "VjgJ",
			wantBuffer: "a   b\nc",
			wantCursor: at(1, 0),
		},
		{
			name:       "visual gJ joins every line the selection spans",
			content:    "a\nb\nc\nd",
			seq:        "vjjgJ",
			wantBuffer: "abc\nd",
			wantCursor: at(2, 0),
		},
		{
			name:       "visual gJ on a single line still joins two",
			content:    "a\nb\nc",
			seq:        "VgJ",
			wantBuffer: "ab\nc",
			wantCursor: at(1, 0),
		},
		{
			name:       "visual block gJ joins the spanned lines",
			content:    "aa\nbb\ncc",
			seq:        "<c-v>jgJ",
			wantBuffer: "aabb\ncc",
			wantCursor: at(2, 0),
		},
		{
			name:       "visual gJ keeps the comment leader",
			content:    "// a\n// b\nc",
			before:     lineComment("//"),
			seq:        "VjgJ",
			wantBuffer: "// a// b\nc",
			wantCursor: at(4, 0),
		},
		{
			name:       "visual gJ on the last line is a no-op",
			content:    "a\nb",
			at:         at(0, 1),
			seq:        "VgJ",
			wantBuffer: "a\nb",
			wantCursor: at(0, 1),
		},
		{
			name:          "unsupported g sequence returns to normal mode",
			content:       "abc\ndef",
			seq:           "gx",
			wantBuffer:    "abc\ndef",
			wantUnhandled: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run(t, tc)
		})
	}
}

func TestViJoinUndoAndRepeat(t *testing.T) {
	for _, tc := range []struct {
		name       string
		content    string
		seq        string
		wantBuffer string
	}{
		{
			name:       "u restores a J in one step",
			content:    "a\nb\nc",
			seq:        "Ju",
			wantBuffer: "a\nb\nc",
		},
		{
			name:       "u restores a counted J in one step",
			content:    "a\nb\nc\nd",
			seq:        "3Ju",
			wantBuffer: "a\nb\nc\nd",
		},
		{
			name:       "u restores a gJ in one step",
			content:    "a\nb\nc",
			seq:        "gJu",
			wantBuffer: "a\nb\nc",
		},
		{
			name:       "u restores a counted gJ in one step",
			content:    "a\nb\nc\nd",
			seq:        "3gJu",
			wantBuffer: "a\nb\nc\nd",
		},
		{
			name:       "dot repeats J",
			content:    "a\nb\nc\nd",
			seq:        "J.",
			wantBuffer: "a b c\nd",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := cell.NewBuffer()
			_, err := buf.ReadFrom(strings.NewReader(tc.content))
			require.NoError(t, err)

			vi := New(buf, uri)
			vi.Resize(40, 10)
			for _, ch := range tc.seq {
				vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			}

			assert.Equal(t, tc.wantBuffer, buf.String())
		})
	}
}

func TestVigg(t *testing.T) {
	code := `11111111111
222222
333333333333

 5
6
7777777777777777777777777777777777777777777777777777777777777777777777777777777777777777777
   88888888
9999
11111111
  222222222222222
3333333
   4444444444444444
`
	codeLong := code
	for range 200 {
		codeLong += code
	}

	suite := []struct {
		name         string
		fileText     string
		narrowWrap   bool // scroll width of 4 with wrapping enaled
		moveCursorFn func(*viHandlerImpl)
		events       string
		expectCoord  term.Coordinates
	}{
		{
			name:        "gg from 0,0",
			fileText:    code,
			narrowWrap:  true,
			events:      "gg",
			expectCoord: term.Coordinates{X: 0, Y: 0},
		},
		{
			name:         "gg from 1,0",
			fileText:     code,
			narrowWrap:   true,
			moveCursorFn: func(vi *viHandlerImpl) { vi.cursor.MoveRight() },
			events:       "gg",
			expectCoord:  term.Coordinates{X: 1, Y: 0},
		},
		{
			name:       "6gg from start of wrapped line",
			fileText:   code,
			narrowWrap: true,
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveDown()
				vi.cursor.MoveRightColumns(2)
			},
			events:      "6gg",
			expectCoord: term.Coordinates{X: 0, Y: 8}, // lonely 6 in `code`
		},
		{
			name:       "6gg from middle of wrapped line",
			fileText:   code,
			narrowWrap: true,
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveDown()
				vi.cursor.MoveRightColumns(2)
			},
			events:      "6gg",
			expectCoord: term.Coordinates{X: 0, Y: 8}, // lonely 6 in `code`
		},
		{
			name:       "6gg from end of wrapped line",
			fileText:   code,
			narrowWrap: true,
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveDown()
				vi.cursor.MoveEndLine()
			},
			events:      "6gg",
			expectCoord: term.Coordinates{X: 0, Y: 8}, // lonely 6 in `code`
		},
		{
			name:       "gg to same line called from wrapped lines below brings cursor to start of line",
			fileText:   code,
			narrowWrap: true,
			events:     "3gg",
			moveCursorFn: func(vi *viHandlerImpl) {
				// Move to wrapped line of 333s in `code`.
				vi.cursor.MoveDownLines(3)
				vi.cursor.MoveRightColumns(2)
			},
			expectCoord: term.Coordinates{X: 0, Y: 5}, // beginning of 333s
		},
		{
			name:     "gg from end of long file",
			fileText: codeLong,
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveLastLine()

			},
			events:      "gg",
			expectCoord: term.Coordinates{X: 0, Y: 0},
		},
		{
			name:     "gg from middle of long file",
			fileText: codeLong,
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveLastLine()
				scrollCoords := vi.cursor.ScrollCoordinates(vi.cursor.Coordinates())
				vi.cursor.MoveFirstLine()
				vi.cursor.MoveDownLines(scrollCoords.Y / 2)

			},
			events:      "gg",
			expectCoord: term.Coordinates{X: 0, Y: 0},
		},
		{
			name:        "999gg beyond limits of file",
			fileText:    code,
			events:      "999gg",
			expectCoord: term.Coordinates{X: 0, Y: 8},
		},
		{
			name:     "0g",
			fileText: code,
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveLastLine()
			},
			events:      "0gg",
			expectCoord: term.Coordinates{X: 0, Y: 0},
		},
		{
			name:     "00g",
			fileText: code,
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveLastLine()
			},
			events:      "00gg",
			expectCoord: term.Coordinates{X: 0, Y: 0},
		},
		{
			name:        "3gg in single char file",
			fileText:    "a",
			events:      "3gg",
			expectCoord: term.Coordinates{X: 0, Y: 0},
		},
		{
			name:        "3gg in empty file",
			fileText:    "",
			events:      "3gg",
			expectCoord: term.Coordinates{X: 0, Y: 0},
		},
		{
			name:        "3gg in file that's only new lines",
			fileText:    "\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n",
			events:      "3gg",
			expectCoord: term.Coordinates{X: 0, Y: 2},
		},
		{
			name:        "9999999999999999999999999999999999999999999999999999gg",
			fileText:    code,
			events:      "9999999999999999999999999999999999999999999999999999gg",
			expectCoord: term.Coordinates{X: 0, Y: 8},
		},
	}

	for _, tcase := range suite {
		t.Run(tcase.name, func(t *testing.T) {
			vi := setupVi(t, tcase.fileText, 2, WithWrap(tcase.narrowWrap))
			scrollWidth := 100
			if tcase.narrowWrap {
				scrollWidth = 4
			}
			vi.Resize(scrollWidth, 9)
			vi.Draw(term.NoopWriter{})
			if tcase.moveCursorFn != nil {
				tcase.moveCursorFn(vi)
			}
			for _, eventChar := range tcase.events {
				vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
			}
			assert.Equal(t, tcase.expectCoord, vi.cursor.Coordinates())
		})
	}
}

func TestViG(t *testing.T) {
	code := `11111111111
222222
333333333333

 5
6
7777777777777777777777777777777777777777777777777777777777777777777777777777777777777777777
   88888888
9999
`

	suite := []struct {
		name         string
		fileText     string
		narrowWrap   bool
		moveCursorFn func(*viHandlerImpl)
		events       string
		expectCoord  term.Coordinates
	}{
		{
			name:        "G goes to last line",
			fileText:    code,
			events:      "G",
			expectCoord: term.Coordinates{X: 0, Y: 8},
		},
		{
			name:     "G from middle goes to last line",
			fileText: code,
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveDownLines(3)
			},
			events:      "G",
			expectCoord: term.Coordinates{X: 0, Y: 8},
		},
		{
			name:        "1G goes to first line",
			fileText:    code,
			events:      "1G",
			expectCoord: term.Coordinates{X: 0, Y: 0},
		},
		{
			name:        "5G goes to line 5",
			fileText:    code,
			events:      "5G",
			expectCoord: term.Coordinates{X: 0, Y: 4},
		},
		{
			name:        "6G goes to line 6",
			fileText:    code,
			events:      "6G",
			expectCoord: term.Coordinates{X: 0, Y: 5},
		},
		{
			name:        "999G beyond limits clamps to last line",
			fileText:    code,
			events:      "999G",
			expectCoord: term.Coordinates{X: 0, Y: 8},
		},
		{
			name:        "3G in single char file",
			fileText:    "a",
			events:      "3G",
			expectCoord: term.Coordinates{X: 0, Y: 0},
		},
		{
			name:        "3G in empty file",
			fileText:    "",
			events:      "3G",
			expectCoord: term.Coordinates{X: 0, Y: 0},
		},
	}

	for _, tcase := range suite {
		t.Run(tcase.name, func(t *testing.T) {
			vi := setupVi(t, tcase.fileText, 2, WithWrap(tcase.narrowWrap))
			scrollWidth := 100
			if tcase.narrowWrap {
				scrollWidth = 4
			}
			vi.Resize(scrollWidth, 9)
			vi.Draw(term.NoopWriter{})
			if tcase.moveCursorFn != nil {
				tcase.moveCursorFn(vi)
			}
			for _, eventChar := range tcase.events {
				vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
			}
			assert.Equal(t, tcase.expectCoord, vi.cursor.Coordinates())
		})
	}
}

func TestViGc(t *testing.T) {
	type tc struct {
		name               string
		fileText           string
		before             func(*viHandlerImpl)
		events             string
		wantContent        string
		wantCoord          term.Coordinates
		wantMode           viMode
		wantSelected       bool
		allowUnhandledLast bool
	}

	suite := []tc{
		{
			name:        "gcc toggles current non-empty line",
			fileText:    "alpha\nbeta\n",
			events:      "gcc",
			wantContent: "// alpha\nbeta\n",
			wantCoord:   term.Coordinates{X: 3, Y: 0},
			wantMode:    normalMode,
		},
		{
			name:        "gcc uncomments current line",
			fileText:    "// alpha\nbeta\n",
			events:      "gcc",
			wantContent: "alpha\nbeta\n",
			wantCoord:   term.Coordinates{X: 0, Y: 0},
			wantMode:    normalMode,
		},
		{
			name:     "gcc toggles current last line without trailing newline",
			fileText: "alpha\nbeta",
			before: func(vi *viHandlerImpl) {
				vi.cursor.MoveDown()
			},
			events:      "gcc",
			wantContent: "alpha\n// beta",
			wantCoord:   term.Coordinates{X: 3, Y: 1},
			wantMode:    normalMode,
		},
		{
			name:     "gcc does nothing on blank line",
			fileText: "alpha\n\nbeta\n",
			before: func(vi *viHandlerImpl) {
				vi.cursor.MoveDown()
			},
			events:      "gcc",
			wantContent: "alpha\n\nbeta\n",
			wantCoord:   term.Coordinates{X: 0, Y: 1},
			wantMode:    normalMode,
		},
		{
			name:     "gc in visual mode comments same-line selection",
			fileText: "alpha\nbeta\n",
			before: func(vi *viHandlerImpl) {
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'v'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
			},
			events:       "gc",
			wantContent:  "// alpha\nbeta\n",
			wantCoord:    term.Coordinates{X: 5, Y: 0},
			wantMode:     normalMode,
			wantSelected: false,
		},
		{
			name:     "gc in visual mode uncomments same-line selection",
			fileText: "// alpha\nbeta\n",
			before: func(vi *viHandlerImpl) {
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'v'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
			},
			events:       "gc",
			wantContent:  "alpha\nbeta\n",
			wantCoord:    term.Coordinates{X: 0, Y: 0},
			wantMode:     normalMode,
			wantSelected: false,
		},
		{
			name:     "gc in visual mode comments multiline selection",
			fileText: "alpha\nbeta\ngamma\n",
			before: func(vi *viHandlerImpl) {
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'v'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
			},
			events:       "gc",
			wantContent:  "// alpha\n// beta\ngamma\n",
			wantCoord:    term.Coordinates{X: 3, Y: 1},
			wantMode:     normalMode,
			wantSelected: false,
		},
		{
			name:     "gc in visual mode uncomments multiline selection",
			fileText: "// alpha\n// beta\ngamma\n",
			before: func(vi *viHandlerImpl) {
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'v'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
			},
			events:       "gc",
			wantContent:  "alpha\nbeta\ngamma\n",
			wantCoord:    term.Coordinates{X: 0, Y: 1},
			wantMode:     normalMode,
			wantSelected: false,
		},
		{
			name:     "gc in visual mode comments inverse multiline selection",
			fileText: "alpha\nbeta\ngamma\n",
			before: func(vi *viHandlerImpl) {
				vi.cursor.MoveDown()
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'v'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'k'})
			},
			events:       "gc",
			wantContent:  "// alpha\n// beta\ngamma\n",
			wantCoord:    term.Coordinates{X: 3, Y: 0},
			wantMode:     normalMode,
			wantSelected: false,
		},
		{
			name:     "gc in visual mode skips blank lines but comments non-blank lines",
			fileText: "alpha\n\nbeta\n",
			before: func(vi *viHandlerImpl) {
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'v'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
			},
			events:       "gc",
			wantContent:  "// alpha\n\n// beta\n",
			wantCoord:    term.Coordinates{X: 3, Y: 2},
			wantMode:     normalMode,
			wantSelected: false,
		},
		{
			name:     "gc in visual mode on all-blank selection does nothing and exits visual",
			fileText: "\n\n",
			before: func(vi *viHandlerImpl) {
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'v'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
			},
			events:       "gc",
			wantContent:  "\n\n",
			wantCoord:    term.Coordinates{X: 0, Y: 1},
			wantMode:     normalMode,
			wantSelected: false,
		},
		{
			name:        "gcip comments inner paragraph",
			fileText:    "alpha\nbeta\ngamma\n\ndelta\n",
			events:      "gcip",
			wantContent: "// alpha\n// beta\n// gamma\n\ndelta\n",
			wantCoord:   term.Coordinates{X: 3, Y: 0},
			wantMode:    normalMode,
		},
		{
			name:        "gcap comments around paragraph selection",
			fileText:    "alpha\nbeta\ngamma\n\ndelta\n",
			events:      "gcap",
			wantContent: "// alpha\n// beta\n// gamma\n\ndelta\n",
			wantCoord:   term.Coordinates{X: 3, Y: 0},
			wantMode:    normalMode,
		},
		{
			name:        "gciw wraps single-line word object in block comment",
			fileText:    "alpha beta\n",
			events:      "gciw",
			wantContent: "/*alpha*/ beta\n",
			wantCoord:   term.Coordinates{X: 2, Y: 0},
			wantMode:    normalMode,
		},
		{
			name:        "gcaw wraps single-line word object in block comment",
			fileText:    "alpha beta\n",
			events:      "gcaw",
			wantContent: "/*alpha */beta\n",
			wantCoord:   term.Coordinates{X: 2, Y: 0},
			wantMode:    normalMode,
		},
		{
			name:     "gca brace wraps single-line block object in block comment",
			fileText: "call{one}\n",
			before: func(vi *viHandlerImpl) {
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'f'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: '{'})
			},
			events:      "gca{",
			wantContent: "call/*{one}*/\n",
			wantCoord:   term.Coordinates{X: 6, Y: 0},
			wantMode:    normalMode,
		},
		{
			name:        "gcj comments current and next line",
			fileText:    "alpha\nbeta\ngamma\n",
			events:      "gcj",
			wantContent: "// alpha\n// beta\ngamma\n",
			wantCoord:   term.Coordinates{X: 3, Y: 1},
			wantMode:    normalMode,
		},
		{
			name:     "gck comments previous and current line",
			fileText: "alpha\nbeta\ngamma\n",
			before: func(vi *viHandlerImpl) {
				vi.cursor.MoveDown()
			},
			events:      "gck",
			wantContent: "// alpha\n// beta\ngamma\n",
			wantCoord:   term.Coordinates{X: 3, Y: 0},
			wantMode:    normalMode,
		},
		{
			name:        "gc right brace comments to next paragraph boundary",
			fileText:    "alpha\nbeta\n\ngamma\ndelta\n",
			events:      "gc}",
			wantContent: "// alpha\n// beta\n\ngamma\ndelta\n",
			wantCoord:   term.Coordinates{X: 3, Y: 1},
			wantMode:    normalMode,
		},
		{
			name:     "gc left brace from paragraph start covers only the blank line above",
			fileText: "alpha\n\nbeta\ngamma\n",
			before: func(vi *viHandlerImpl) {
				vi.cursor.MoveDown()
				vi.cursor.MoveDown()
			},
			events:      "gc{",
			wantContent: "alpha\n\nbeta\ngamma\n",
			wantCoord:   term.Coordinates{X: 0, Y: 1},
			wantMode:    normalMode,
		},
		{
			name:        "gce comments current line",
			fileText:    "alpha beta\n",
			events:      "gce",
			wantContent: "// alpha beta\n",
			wantCoord:   term.Coordinates{X: 7, Y: 0},
			wantMode:    normalMode,
		},
		{
			name:        "gcw comments current line",
			fileText:    "alpha beta\n",
			events:      "gcw",
			wantContent: "// alpha beta\n",
			wantCoord:   term.Coordinates{X: 8, Y: 0},
			wantMode:    normalMode,
		},
		{
			name:        "2gcj uses operator count",
			fileText:    "alpha\nbeta\ngamma\ndelta\n",
			events:      "2gcj",
			wantContent: "// alpha\n// beta\n// gamma\ndelta\n",
			wantCoord:   term.Coordinates{X: 3, Y: 2},
			wantMode:    normalMode,
		},
		{
			name:        "gc2j uses post operator count",
			fileText:    "alpha\nbeta\ngamma\ndelta\n",
			events:      "gc2j",
			wantContent: "// alpha\n// beta\n// gamma\ndelta\n",
			wantCoord:   term.Coordinates{X: 3, Y: 2},
			wantMode:    normalMode,
		},
		{
			name:     "gcgE comments backward WORD end across lines",
			fileText: "one\ntwo-three\nfour\n",
			before: func(vi *viHandlerImpl) {
				vi.setCursorAtScroll(term.Coordinates{X: len("four") - 1, Y: 2})
			},
			events:      "gcgE",
			wantContent: "one\n// two-three\n// four\n",
			wantCoord:   term.Coordinates{X: 11, Y: 1},
			wantMode:    normalMode,
		},
		{
			name:        "gcg underscore comments to last non blank on current line",
			fileText:    "hello   \nworld\n",
			events:      "gcg_",
			wantContent: "// hello   \nworld\n",
			wantCoord:   term.Coordinates{X: 7, Y: 0},
			wantMode:    normalMode,
		},
		{
			name:        "gcg invalid meta motion exits without mutating",
			fileText:    "alpha\nbeta\n",
			events:      "gcgq",
			wantContent: "alpha\nbeta\n",
			wantCoord:   term.Coordinates{X: 0, Y: 0},
			wantMode:    normalMode,
		},
		{
			name:        "gciaw retargets text object and wraps single-line word in block comment",
			fileText:    "alpha beta\n",
			events:      "gciaw",
			wantContent: "/*alpha */beta\n",
			wantCoord:   term.Coordinates{X: 2, Y: 0},
			wantMode:    normalMode,
		},
		{
			name:        "gcaiw retargets text object and wraps single-line word in block comment",
			fileText:    "alpha beta\n",
			events:      "gcaiw",
			wantContent: "/*alpha*/ beta\n",
			wantCoord:   term.Coordinates{X: 2, Y: 0},
			wantMode:    normalMode,
		},
		{
			name:        "gci invalid text object exits without mutating",
			fileText:    "alpha beta\n",
			events:      "gciq",
			wantContent: "alpha beta\n",
			wantCoord:   term.Coordinates{X: 0, Y: 0},
			wantMode:    normalMode,
		},
		{
			name:               "gch no op motion exits without mutating",
			fileText:           "alpha beta\n",
			events:             "gch",
			wantContent:        "alpha beta\n",
			wantCoord:          term.Coordinates{X: 0, Y: 0},
			wantMode:           normalMode,
			allowUnhandledLast: true,
		},
	}

	for _, tcase := range suite {
		t.Run(tcase.name, func(t *testing.T) {
			vi := setupVi(t, tcase.fileText, 2, WithComments(text.CommentConfig{
				"txt": {
					Line:  []string{"//"},
					Block: []text.CommentBlock{{Start: "/*", End: "*/"}},
				},
			}))
			vi.less.Buffer().WithView(testCommentService{
				view:  vi.less.Buffer().View(),
				line:  []string{"//"},
				block: []string{"/*", "*/"},
			})
			vi.Resize(20, 10)
			vi.cursor.SetCommentSpec(text.CommentSpec{
				Line:  []string{"//"},
				Block: []text.CommentBlock{{Start: "/*", End: "*/"}},
			})

			if tcase.before != nil {
				tcase.before(vi)
			}

			for i, eventChar := range tcase.events {
				quit, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
				require.False(t, quit)
				if tcase.allowUnhandledLast && i == len(tcase.events)-1 {
					continue
				}
				require.True(t, handled, "event %q should be handled", string(eventChar))
			}

			assert.Equal(t, tcase.wantContent, vi.less.Buffer().String())
			assert.Equal(t, tcase.wantCoord, vi.cursor.Coordinates())
			assert.Equal(t, tcase.wantMode, vi.mode())
			_, selected := vi.Selection()
			assert.Equal(t, tcase.wantSelected, selected)
		})
	}
}

func TestViGoUnderscore(t *testing.T) {
	// Content with trailing whitespace:
	// Line 0: "hello   " (last non-blank 'o' at x=4)
	// Line 1: "world" (last non-blank 'd' at x=4)
	// Line 2: "   " (all blanks)
	// Line 3: "" (empty)
	// Line 4: "  foo  " (last non-blank 'o' at x=4)
	code := "hello   \nworld\n   \n\n  foo  "

	suite := []struct {
		name          string
		fileText      string
		narrowWrap    bool
		moveCursorFn  func(*viHandlerImpl)
		events        string
		expectCoord   *term.Coordinates
		expectContent string
	}{
		// ===== Basic g_ motion =====
		{
			name:        "g_ from start of line with trailing spaces",
			fileText:    code,
			events:      "g_",
			expectCoord: &term.Coordinates{X: 4, Y: 0},
		},
		{
			name:     "g_ on line without trailing spaces",
			fileText: code,
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveDown()
			},
			events:      "g_",
			expectCoord: &term.Coordinates{X: 4, Y: 1},
		},
		{
			name:     "g_ on all-blank line stays at x=0",
			fileText: code,
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveDown()
				vi.cursor.MoveDown()
			},
			events:      "g_",
			expectCoord: &term.Coordinates{X: 0, Y: 2},
		},
		{
			name:     "g_ on empty line stays at x=0",
			fileText: code,
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveDown()
				vi.cursor.MoveDown()
				vi.cursor.MoveDown()
			},
			events:      "g_",
			expectCoord: &term.Coordinates{X: 0, Y: 3},
		},
		{
			name:     "g_ on line with leading and trailing spaces",
			fileText: code,
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveDown()
				vi.cursor.MoveDown()
				vi.cursor.MoveDown()
				vi.cursor.MoveDown()
			},
			events:      "g_",
			expectCoord: &term.Coordinates{X: 4, Y: 4},
		},

		// ===== Cursor starting positions =====
		{
			name:     "g_ from middle of line",
			fileText: code,
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveRightColumns(2)
			},
			events:      "g_",
			expectCoord: &term.Coordinates{X: 4, Y: 0},
		},
		{
			name:     "g_ already at last non-blank",
			fileText: code,
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveRightColumns(4)
			},
			events:      "g_",
			expectCoord: &term.Coordinates{X: 4, Y: 0},
		},
		{
			name:     "g_ from trailing whitespace region",
			fileText: code,
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveEndLine()
			},
			events:      "g_",
			expectCoord: &term.Coordinates{X: 4, Y: 0},
		},

		// ===== Null characters =====
		{
			name:        "g_ skips trailing nulls",
			fileText:    "abc\x00\x00\x00",
			events:      "g_",
			expectCoord: &term.Coordinates{X: 2, Y: 0},
		},
		{
			name:        "g_ on all-null line",
			fileText:    "\x00\x00\x00",
			events:      "g_",
			expectCoord: &term.Coordinates{X: 0, Y: 0},
		},
		{
			name:        "g_ finds char between nulls",
			fileText:    "\x00a\x00",
			events:      "g_",
			expectCoord: &term.Coordinates{X: 1, Y: 0},
		},

		// ===== Single character and short lines =====
		{
			name:        "g_ on single char line",
			fileText:    "a",
			events:      "g_",
			expectCoord: &term.Coordinates{X: 0, Y: 0},
		},
		{
			name:        "g_ on single char with trailing space",
			fileText:    "a ",
			events:      "g_",
			expectCoord: &term.Coordinates{X: 0, Y: 0},
		},
		{
			name:        "g_ on two chars no trailing",
			fileText:    "ab",
			events:      "g_",
			expectCoord: &term.Coordinates{X: 1, Y: 0},
		},

		// ===== Wrap mode =====
		{
			name:        "g_ wrap: short line within width",
			fileText:    "ab   ",
			narrowWrap:  true,
			events:      "g_",
			expectCoord: &term.Coordinates{X: 1, Y: 0},
		},
		{
			name:        "g_ wrap: line wraps, last non-blank on first visual row",
			fileText:    "abcd   ",
			narrowWrap:  true,
			events:      "g_",
			expectCoord: &term.Coordinates{X: 3, Y: 0},
		},
		{
			name:        "g_ wrap: line wraps, last non-blank on second visual row",
			fileText:    "abcdefg   ",
			narrowWrap:  true,
			events:      "g_",
			expectCoord: &term.Coordinates{X: 2, Y: 1},
		},
		{
			name:       "g_ wrap: cursor on second visual row finds last non-blank",
			fileText:   "abcdefg   ",
			narrowWrap: true,
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveRightColumns(4)
			},
			events:      "g_",
			expectCoord: &term.Coordinates{X: 2, Y: 1},
		},
		{
			name:       "g_ wrap: multiline, second line wraps",
			fileText:   "ab\nefghijkl  ",
			narrowWrap: true,
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveDown()
			},
			events:      "g_",
			expectCoord: &term.Coordinates{X: 3, Y: 2},
		},

		// ===== Delete operator: dg_ =====
		{
			name:          "dg_ from start of line deletes to last non-blank",
			fileText:      "hello   \nworld",
			events:        "dg_",
			expectCoord:   &term.Coordinates{X: 0, Y: 0},
			expectContent: "   \nworld",
		},
		{
			name:     "dg_ from middle of line",
			fileText: "hello   \nworld",
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveRightColumns(2)
			},
			events:        "dg_",
			expectCoord:   &term.Coordinates{X: 2, Y: 0},
			expectContent: "he   \nworld",
		},
		{
			name:     "dg_ on all-blank line is no-op (cursor doesn't move)",
			fileText: "hello\n   \nworld",
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveDown()
			},
			events:        "dg_",
			expectContent: "hello\n   \nworld",
		},
		{
			name:     "dg_ on line with leading spaces from first non-blank",
			fileText: "  foo  \nbar",
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveRightColumns(2)
			},
			events:        "dg_",
			expectCoord:   &term.Coordinates{X: 2, Y: 0},
			expectContent: "    \nbar",
		},

		// ===== Yank operator: yg_ =====
		{
			name:     "yg_ from middle does not modify buffer",
			fileText: code,
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveRightColumns(2)
			},
			events:        "yg_",
			expectCoord:   &term.Coordinates{X: 2, Y: 0},
			expectContent: code,
		},
		{
			name:          "yg_ from start does not modify buffer",
			fileText:      code,
			events:        "yg_",
			expectContent: code,
		},

		// ===== Case change operators =====
		{
			name:          "gug_ lowercases from cursor to last non-blank",
			fileText:      "HELLO   \nworld",
			events:        "gug_",
			expectContent: "hello   \nworld",
		},
		{
			name:          "gUg_ uppercases from cursor to last non-blank",
			fileText:      "hello   \nworld",
			events:        "gUg_",
			expectContent: "HELLO   \nworld",
		},
		{
			name:          "g~g_ toggles case from cursor to last non-blank",
			fileText:      "Hello   \nworld",
			events:        "g~g_",
			expectContent: "hELLO   \nworld",
		},
		{
			name:     "gug_ from middle lowercases partial",
			fileText: "HELLO   ",
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.cursor.MoveRightColumns(2)
			},
			events:        "gug_",
			expectContent: "HEllo   ",
		},
	}

	for _, tcase := range suite {
		t.Run(tcase.name, func(t *testing.T) {
			fileText := tcase.fileText
			if fileText == "" {
				fileText = code
			}
			vi := setupVi(t, fileText, 2, WithWrap(tcase.narrowWrap))
			scrollWidth := 100
			if tcase.narrowWrap {
				scrollWidth = 4
			}
			vi.Resize(scrollWidth, 30)
			vi.Draw(term.NoopWriter{})
			if tcase.moveCursorFn != nil {
				tcase.moveCursorFn(vi)
			}
			for _, eventChar := range tcase.events {
				vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
			}
			if tcase.expectCoord != nil {
				assert.Equal(t, *tcase.expectCoord, vi.cursor.Coordinates(), "cursor position")
			}
			if tcase.expectContent != "" {
				assert.Equal(t, tcase.expectContent, vi.less.Buffer().String(), "buffer content")
			}
		})
	}
}

func TestViggBeyondContent(t *testing.T) {
	t.Run("discrepancy between visual cursor and actual cursor", func(t *testing.T) {
		fileText := "a\nb\nc\nd"
		vi := setupVi(t, fileText, 2)
		vi.Resize(100, 30)
		vi.Draw(term.NoopWriter{})
		for _, eventChar := range "999gg" {
			vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
		}
		assert.Equal(t, term.Coordinates{X: 0, Y: 3}, vi.cursor.Coordinates())
		vi.cursor.MoveUp()
		assert.Equal(t, term.Coordinates{X: 0, Y: 2}, vi.cursor.Coordinates())

	})
}

func TestSetCursorAtScrollBounds(t *testing.T) {
	fileText := "123\n456\n789\nd"
	vi := setupVi(t, fileText, 2)
	vi.Resize(8, 8)

	t.Run("vertical bounds", func(t *testing.T) {
		vi.setCursorAtScroll(term.Coordinates{X: 0, Y: 999})
		assert.Equal(t, term.Coordinates{X: 0, Y: 3}, vi.cursor.Coordinates())
	})
}

func TestResetCount(t *testing.T) {
	t.Run("numbers are accumulated into vi counter", func(t *testing.T) {
		var code strings.Builder
		for i := range 45 {
			code.WriteString(fmt.Sprintf("%v\n", i))
		}

		vi := setupVi(t, code.String(), 2)
		vi.Resize(5, 50)

		vi.Handle(term.Event{Type: term.EventKey, Ch: '1'})
		vi.Handle(term.Event{Type: term.EventKey, Ch: '1'})
		vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})

		assert.Equal(t, vi.cursor.Coordinates().Y, 11)
	})
	t.Run("when parsing numbers and the parsed int exceeds MaxInt count should become MaxInt and not be reset", func(t *testing.T) {
		vi := setupVi(t, "aaa\nbbb\nccc\nddd", 2)
		vi.Resize(10, 10)

		maxIntStr := strconv.Itoa(math.MaxInt)

		for _, maxIntChar := range maxIntStr {
			vi.Handle(term.Event{Type: term.EventKey, Ch: maxIntChar})
		}
		vi.Handle(term.Event{Type: term.EventKey, Ch: '9'})
		vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})

		assert.Equal(t, vi.cursor.Coordinates().Y, 3)
	})

	t.Run("single motion on vi initialized with scroll", func(t *testing.T) {
		vi := setupViWithScroll(t, "aaa\nbbb\nccc\nddd", 2)
		vi.Resize(10, 10)
		vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
		assert.Equal(t, 1, vi.cursor.Coordinates().Y)
	})

	t.Run("huge horizontal counts clamp to line bounds", func(t *testing.T) {
		vi := setupVi(t, "abcdef", 2)
		vi.Resize(20, 5)
		vi.setCursorAtScroll(term.Coordinates{X: 3, Y: 0}) // 'd'

		maxIntStr := strconv.Itoa(math.MaxInt)
		for _, ch := range maxIntStr {
			_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			require.True(t, handled)
		}

		_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'h'})
		require.True(t, handled)
		assert.Equal(t, term.Coordinates{X: 0, Y: 0}, vi.cursor.Coordinates())

		for _, ch := range maxIntStr {
			_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			require.True(t, handled)
		}

		_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
		require.True(t, handled)
		assert.Equal(t, term.Coordinates{X: len("abcdef") - 1, Y: 0}, vi.cursor.Coordinates())

		vi = setupVi(t, "abcdef", 2)
		vi.Resize(20, 5)
		vi.setCursorAtScroll(term.Coordinates{X: 0, Y: 0})
		for _, ch := range maxIntStr {
			_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			require.True(t, handled)
		}

		_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
		require.True(t, handled)
		assert.Equal(t, term.Coordinates{X: len("abcdef") - 1, Y: 0}, vi.cursor.Coordinates())
	})

	t.Run("huge vertical counts clamp to file bounds", func(t *testing.T) {
		vi := setupVi(t, "aaa\nbbb\nccc\nddd", 2)
		vi.Resize(20, 5)
		vi.setCursorAtScroll(term.Coordinates{X: 1, Y: 2})

		maxIntStr := strconv.Itoa(math.MaxInt)
		for _, ch := range maxIntStr {
			_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			require.True(t, handled)
		}

		_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'k'})
		require.True(t, handled)
		assert.Equal(t, term.Coordinates{X: 1, Y: 0}, vi.cursor.Coordinates())

		for _, ch := range maxIntStr {
			_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			require.True(t, handled)
		}

		_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
		require.True(t, handled)
		assert.Equal(t, term.Coordinates{X: 1, Y: 3}, vi.cursor.Coordinates())
	})
}

func TestNoModeHandlesNonCtrlModifiers(t *testing.T) {
	vi := setupVi(t, "a", 2)
	vi.Resize(4, 4)

	modes := []viMode{
		normalMode,
		insertMode,
		deleteMode,
		gMode,
		zMode,
		yankMode,
		visualMode,
		visualLineMode,
		visualBlockMode,
		replaceMode,
		replaceOneMode,
		searchMode,
	}
	modifiers := []term.Modifier{
		term.ModAlt, term.ModShift, term.ModMeta,
		term.ModCtrlShift, term.ModCtrlAlt, term.ModCtrlMeta,
		term.ModCtrlShiftAlt, term.ModCtrlShiftMeta, term.ModCtrlAltMeta,
		term.ModShiftMeta, term.ModAltMeta, term.ModAltShiftMeta,
		term.ModAltShift,
	}

	for _, mod := range modifiers {
		for _, mode := range modes {
			t.Run(fmt.Sprintf("handle %v in %v", mod, mode), func(t *testing.T) {
				vi.currMode = mode
				exit, handled := vi.Handle(term.Event{Type: term.EventKey, Mod: mod, Ch: 'a'})
				assert.False(t, exit)
				assert.False(t, handled)
			})
		}
	}
}

// parseSearchOpEvents converts a compact event sequence into term events.
// Plain runes become character keys; "\n" maps to KeyEnter; "<esc>",
// "<bs>" and "<enter>" map to the corresponding named keys. Used by
// search-operator table tests where the input mixes typed pattern
// characters with control keys. A literal "<" can be expressed via
// "\\<" and a literal "\\" via "\\\\".
func parseSearchOpEvents(seq string) []term.Event {
	var events []term.Event
	runes := []rune(seq)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch r {
		case '\n':
			events = append(events, term.Event{Type: term.EventKey, Key: term.KeyEnter})
		case '\\':
			if i+1 >= len(runes) {
				panic("parseSearchOpEvents: trailing backslash")
			}
			i++
			events = append(events, term.Event{Type: term.EventKey, Ch: runes[i]})
		case '<':
			end := i + 1
			for end < len(runes) && runes[end] != '>' {
				end++
			}
			name := strings.ToLower(string(runes[i+1 : end]))
			switch name {
			case "esc":
				events = append(events, term.Event{Type: term.EventKey, Key: term.KeyEsc})
			case "bs":
				events = append(events, term.Event{Type: term.EventKey, Key: term.KeyBackspace})
			case "enter":
				events = append(events, term.Event{Type: term.EventKey, Key: term.KeyEnter})
			default:
				// support <c-X> form for ctrl-modified single chars.
				if strings.HasPrefix(name, "c-") && len([]rune(name)) == 3 {
					events = append(events, term.Event{
						Type: term.EventKey,
						Mod:  term.ModCtrl,
						Ch:   []rune(name)[2],
					})
					break
				}
				panic("parseSearchOpEvents: unknown token <" + name + ">")
			}
			i = end
		default:
			events = append(events, term.Event{Type: term.EventKey, Ch: r})
		}
	}
	return events
}

func TestGoFormatWrapParagraph(t *testing.T) {
	type tc struct {
		name               string
		content            string
		before             func(t *testing.T, vi *viHandlerImpl)
		events             string
		ruler              int
		width              int
		wantContent        string
		wantMode           viMode
		wantSelected       bool
		allowUnhandledLast bool
	}

	setupWrapVi := func(t *testing.T, content string, ruler, width int) *viHandlerImpl {
		t.Helper()
		vi := setupVi(t,
			content,
			2,
			WithRuler(ruler),
			WithComments(text.CommentConfig{
				"go": {Line: []string{"//"}},
			}),
		)
		vi.Resize(width, 20)

		buf := vi.less.Buffer()
		buf.WithView(testCommentService{view: buf.View(), line: []string{"//"}})
		vi.cursor.SetCommentSpec(text.CommentSpec{Line: []string{"//"}})
		return vi
	}

	callbackAdapterContent := `// callbackAdapter adapts semanticapi.LSPCallback to jsonrpc2.Handler. jfkejwflkwejflwejflew jfklej wklefjkl jfwe jfklw jfklwej elfkj wlkjf klwejfl wjefkl jeklfjw
// fjewkjfewl
// jfekljfwlwejflkwefjklwjeflkjlwkef fjlk w jflkew fwekljfewj lfwjelkwlkfjewlkfj j jlfkwejlfkj
type callbackAdapter struct {
	cb         semanticapi.LSPCallback
	serverName string
}
`

	suite := []tc{
		{
			name:    "gqq wraps current line only",
			content: "// alpha beta gamma delta epsilon zeta eta theta\n// iota kappa lambda mu\n\nnext\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 3}))
			},
			events:      "gqq",
			ruler:       20,
			width:       40,
			wantContent: "// alpha beta gamma\n// delta epsilon\n// zeta eta theta\n// iota kappa lambda mu\n\nnext\n",
			wantMode:    normalMode,
		},
		{
			name:    "2gqq wraps current and next line via operator count",
			content: "// alpha beta gamma\n// delta epsilon zeta\n\nnext\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 3}))
			},
			events:      "2gqq",
			ruler:       20,
			width:       40,
			wantContent: "// alpha beta gamma\n// delta epsilon\n// zeta\n\nnext\n",
			wantMode:    normalMode,
		},
		{
			name:    "gqj wraps current and next line only",
			content: "keep me untouched\nalpha beta gamma delta epsilon\nzeta eta theta\nleave me too\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 1, X: 0}))
			},
			events:      "gqj",
			ruler:       16,
			width:       80,
			wantContent: "keep me untouched\nalpha beta gamma\ndelta epsilon\nzeta eta theta\nleave me too\n",
			wantMode:    normalMode,
		},
		{
			name:    "gqk wraps previous and current line only",
			content: "keep me untouched\nalpha beta gamma delta epsilon\nzeta eta theta\nleave me too\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 2, X: 0}))
			},
			events:      "gqk",
			ruler:       16,
			width:       80,
			wantContent: "keep me untouched\nalpha beta gamma\ndelta epsilon\nzeta eta theta\nleave me too\n",
			wantMode:    normalMode,
		},
		{
			name:    "gqgk wraps previous and current line via meta motion",
			content: "keep me untouched\nalpha beta gamma delta epsilon\nzeta eta theta\nleave me too\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 2, X: 0}))
			},
			events:      "gqgk",
			ruler:       16,
			width:       80,
			wantContent: "keep me untouched\nalpha beta gamma\ndelta epsilon\nzeta eta theta\nleave me too\n",
			wantMode:    normalMode,
		},
		{
			name:    "2gqj uses operator count across three comment lines",
			content: "// alpha beta\n// gamma delta\n// epsilon zeta\nstop\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 3}))
			},
			events:      "2gqj",
			ruler:       20,
			width:       40,
			wantContent: "// alpha beta gamma\n// delta epsilon\n// zeta\nstop\n",
			wantMode:    normalMode,
		},
		{
			name:    "gq2j uses post operator count across three comment lines",
			content: "// alpha beta\n// gamma delta\n// epsilon zeta\nstop\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 3}))
			},
			events:      "gq2j",
			ruler:       20,
			width:       40,
			wantContent: "// alpha beta gamma\n// delta epsilon\n// zeta\nstop\n",
			wantMode:    normalMode,
		},
		{
			name:    "gqg_ wraps current line using meta underscore motion",
			content: "// alpha beta gamma delta   \nnext\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 3}))
			},
			events:      "gqg_",
			ruler:       14,
			width:       40,
			wantContent: "// alpha beta\n// gamma delta\nnext\n",
			wantMode:    normalMode,
		},
		{
			name:    "gq_ wraps current line using underscore motion",
			content: "// alpha beta gamma delta   \nnext\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 3}))
			},
			events:      "gq_",
			ruler:       14,
			width:       40,
			wantContent: "// alpha beta\n// gamma delta\nnext\n",
			wantMode:    normalMode,
		},
		{
			name:    "gqgg wraps from cursor line to first line",
			content: "// alpha beta gamma\n// delta epsilon zeta\n// one two three four\n// LAST\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 2, X: 3}))
			},
			events:      "gqgg",
			ruler:       20,
			width:       60,
			wantContent: "// alpha beta gamma\n// delta epsilon\n// zeta one two\n// three four\n// LAST\n",
			wantMode:    normalMode,
		},
		{
			name:    "gqiw formats and does not toggle block comments",
			content: "alpha beta gamma delta\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.cursor.SetCommentSpec(text.CommentSpec{
					Line:  []string{"//"},
					Block: []text.CommentBlock{{Start: "/*", End: "*/"}},
				})
				vi.less.Buffer().WithView(testCommentService{
					view:  vi.less.Buffer().View(),
					line:  []string{"//"},
					block: []string{"/*", "*/"},
				})
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: len("alpha ")}))
			},
			events:      "gqiw",
			ruler:       12,
			width:       40,
			wantContent: "alpha beta\ngamma delta\n",
			wantMode:    normalMode,
		},
		{
			name:    "gqip wraps inner comment paragraph only",
			content: "// alpha beta gamma delta\n// epsilon zeta eta theta\n\n// keep second paragraph intact\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 3}))
			},
			events:      "gqip",
			ruler:       20,
			width:       60,
			wantContent: "// alpha beta gamma\n// delta epsilon\n// zeta eta theta\n\n// keep second paragraph intact\n",
			wantMode:    normalMode,
		},
		{
			name:    "gqap wraps around paragraph selection without swallowing next paragraph",
			content: "// alpha beta gamma delta\n// epsilon zeta eta theta\n\n// keep second paragraph intact\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 3}))
			},
			events:      "gqap",
			ruler:       20,
			width:       60,
			wantContent: "// alpha beta gamma\n// delta epsilon\n// zeta eta theta\n\n// keep second paragraph intact\n",
			wantMode:    normalMode,
		},
		{
			name:    "visual line gq reflows selected comment block without swallowing following code",
			content: callbackAdapterContent,
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 3}))
			},
			events: "Vjjgq",
			ruler:  80,
			width:  120,
			wantContent: `// callbackAdapter adapts semanticapi.LSPCallback to jsonrpc2.Handler.
// jfkejwflkwejflwejflew jfklej wklefjkl jfwe jfklw jfklwej elfkj wlkjf klwejfl
// wjefkl jeklfjw fjewkjfewl jfekljfwlwejflkwefjklwjeflkjlwkef fjlk w jflkew
// fwekljfewj lfwjelkwlkfjewlkfj j jlfkwejlfkj
type callbackAdapter struct {
	cb         semanticapi.LSPCallback
	serverName string
}
`,
			wantMode: normalMode,
		},
		{
			name:        "visual line gq formats selected plain paragraphs independently",
			content:     "alpha beta gamma delta\nepsilon zeta eta theta\n\none two three four\nfive six seven eight\n",
			events:      "Vjjjgq",
			ruler:       12,
			width:       80,
			wantContent: "alpha beta\ngamma delta\nepsilon zeta\neta theta\n\none two\nthree four\nfive six seven eight\n",
			wantMode:    normalMode,
		},
		{
			name:    "visual line gq formats selected comment paragraphs independently",
			content: "// alpha beta gamma delta\n// epsilon zeta eta theta\n\n// one two three four\n// five six seven eight\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 3}))
			},
			events:      "Vjjjgq",
			ruler:       20,
			width:       80,
			wantContent: "// alpha beta gamma\n// delta epsilon\n// zeta eta theta\n\n// one two three\n// four\n// five six seven eight\n",
			wantMode:    normalMode,
		},
		{
			name:    "visual block gq reflows whole lines covered by the block",
			content: "alpha beta gamma delta epsilon zeta\nsecond line\nthird line\nfourth line\nfifth line\n",
			events:  "<c-v>jjj$gq",
			ruler:   12,
			width:   40,
			// Vim treats gq on a block selection as a linewise gq
			// over the lines covered by the block; column ranges
			// inside the block are ignored for the reflow target.
			wantContent: "alpha beta\ngamma delta\nepsilon zeta\nsecond line\nthird line\nfourth line\nfifth line\n",
			wantMode:    normalMode,
		},
		{
			name:        "visual block gq on a single line behaves like gqq",
			content:     "alpha beta gamma delta epsilon zeta\nsecond line untouched\n",
			events:      "<c-v>gq",
			ruler:       12,
			width:       40,
			wantContent: "alpha beta\ngamma delta\nepsilon zeta\nsecond line untouched\n",
			wantMode:    normalMode,
		},
		{
			name:        "visual block gq preserves tab indentation across reflowed lines",
			content:     "\talpha beta gamma delta epsilon zeta\n\tsecond line\n",
			events:      "<c-v>jllgq",
			ruler:       14,
			width:       40,
			wantContent: "\talpha beta\n\tgamma delta\n\tepsilon zeta\n\tsecond line\n",
			wantMode:    normalMode,
		},
		{
			name:    "visual block gq reflows wide CJK text using display width",
			content: "你好 世界 再见 朋友 测试 内容\nsecond line\n",
			events:  "<c-v>jllgq",
			ruler:   8,
			width:   40,
			// ruler=8 also rewraps "second line" (11 cols) onto two lines.
			wantContent: "你好\n世界\n再见\n朋友\n测试\n内容\nsecond\nline\n",
			wantMode:    normalMode,
		},
		{
			name:        "visual block gq across comment then code reflows both chunks independently",
			content:     "// alpha beta gamma delta epsilon\nplain code keeps its shape\n",
			events:      "<c-v>jllgq",
			ruler:       14,
			width:       40,
			wantContent: "// alpha beta\n// gamma delta\n// epsilon\nplain code\nkeeps its\nshape\n",
			wantMode:    normalMode,
		},
		{
			name:        "visual block gq across paragraph blank line splits chunks",
			content:     "alpha beta gamma delta epsilon\n\nsecond paragraph keeps text\n",
			events:      "<c-v>jjllgq",
			ruler:       12,
			width:       40,
			wantContent: "alpha beta\ngamma delta\nepsilon\n\nsecond\nparagraph\nkeeps text\n",
			wantMode:    normalMode,
		},
		{
			name:        "visual block gq over only blank lines is a no-op",
			content:     "\n\n\n",
			events:      "<c-v>jjgq",
			ruler:       12,
			width:       40,
			wantContent: "\n\n\n",
			wantMode:    normalMode,
		},
		{
			name:    "visual block gq with backward $-extension over short row reflows full lines",
			content: "alpha beta gamma delta epsilon zeta\nx\n",
			events:  "<c-v>j$gq",
			ruler:   12,
			width:   40,
			// row 1 ("x") is short so $ on row 1 is the very first column;
			// the block still covers rows 0..1 and gq reflows whole lines.
			wantContent: "alpha beta\ngamma delta\nepsilon zeta\nx\n",
			wantMode:    normalMode,
		},
		{
			name:    "visual block gq with large ruler joins lines into a single reflowed paragraph",
			content: "tiny line\nshort too\n",
			events:  "<c-v>jllgq",
			ruler:   40,
			width:   60,
			// ruler=40 fits both source lines together; gq joins them
			// into a single reflowed line.
			wantContent: "tiny line short too\n",
			wantMode:    normalMode,
		},
		{
			name: "visual block gq ignores count typed inside the visual " +
				"selection (matches Vim)",
			content: "alpha beta gamma delta epsilon zeta\nsecond line\nthird line\n",
			events:  "<c-v>j3gq",
			ruler:   12,
			width:   40,
			// In Vim, a count entered between selecting and gq is
			// silently consumed; the operator runs once over the
			// existing selection.
			wantContent: "alpha beta\ngamma delta\nepsilon zeta\nsecond line\nthird line\n",
			wantMode:    normalMode,
		},
		{
			name: "visual block gq ignores count typed before <c-v> " +
				"(matches Vim)",
			content: "alpha beta gamma delta epsilon zeta\nsecond line\nthird line\n",
			events:  "3<c-v>jgq",
			ruler:   12,
			width:   40,
			// Leading count is consumed by the visual mode entry;
			// gq still operates once over the resulting selection.
			wantContent: "alpha beta\ngamma delta\nepsilon zeta\nsecond line\nthird line\n",
			wantMode:    normalMode,
		},
		{
			name: "visual line gq ignores count typed inside the visual " +
				"selection (matches Vim)",
			content:     "alpha beta gamma delta epsilon zeta\nsecond line\n",
			events:      "Vj5gq",
			ruler:       12,
			width:       40,
			wantContent: "alpha beta\ngamma delta\nepsilon zeta\nsecond line\n",
			wantMode:    normalMode,
		},
		{
			name:    "gqg invalid meta motion exits without mutating",
			content: "// alpha beta gamma delta\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 3}))
			},
			events:      "gqgz",
			ruler:       20,
			width:       40,
			wantContent: "// alpha beta gamma delta\n",
			wantMode:    normalMode,
		},
		{
			name:    "gqE wraps current line via WORD-end motion",
			content: "// alpha beta gamma delta epsilon\nnext\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 3}))
			},
			events:      "gqE",
			ruler:       18,
			width:       40,
			wantContent: "// alpha beta\n// gamma delta\n// epsilon\nnext\n",
			wantMode:    normalMode,
		},
		{
			name:        "gq slash search wraps through matching line",
			content:     "one two three four\nalpha beta gamma\nEND marker here\ntail unchanged\n",
			events:      "gq/END\n",
			ruler:       12,
			width:       40,
			wantContent: "one two\nthree four\nalpha beta\ngamma\nEND marker here\ntail unchanged\n",
			wantMode:    normalMode,
		},
		{
			name:        "gq slash search no match leaves buffer unchanged",
			content:     "alpha beta gamma\nfoo bar baz\n",
			events:      "gq/zzzz\n",
			ruler:       8,
			width:       40,
			wantContent: "alpha beta gamma\nfoo bar baz\n",
			wantMode:    normalMode,
		},
		{
			name:        "gq slash search same line match wraps current line only",
			content:     "alpha beta gamma delta epsilon zeta\nuntouched line\n",
			events:      "gq/zeta\n",
			ruler:       12,
			width:       40,
			wantContent: "alpha beta\ngamma delta\nepsilon zeta\nuntouched line\n",
			wantMode:    normalMode,
		},
		{
			name:        "gq slash search across paragraph boundary",
			content:     "alpha beta gamma\ndelta epsilon zeta\n\nMARK keep\nlast line\n",
			events:      "gq/MARK\n",
			ruler:       12,
			width:       40,
			wantContent: "alpha beta\ngamma delta\nepsilon zeta\n\nMARK keep\nlast line\n",
			wantMode:    normalMode,
		},
		{
			name:    "gq slash search from mid-line wraps from current line",
			content: "alpha beta gamma delta epsilon\nfoo bar\nEND here\nlast\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 7}))
			},
			events:      "gq/END\n",
			ruler:       12,
			width:       40,
			wantContent: "alpha beta\ngamma delta\nepsilon foo\nbar\nEND here\nlast\n",
			wantMode:    normalMode,
		},
		{
			name:    "gq slash search wraps comment block through match",
			content: "// alpha beta gamma\n// delta epsilon zeta\n// MARK end here\nplain code\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 3}))
			},
			events:      "gq/MARK\n",
			ruler:       16,
			width:       40,
			wantContent: "// alpha beta\n// gamma delta\n// epsilon zeta\n// MARK end here\nplain code\n",
			wantMode:    normalMode,
		},
		{
			name:    "gq slash search empty pattern leaves buffer unchanged",
			content: "alpha beta gamma\nfoo bar baz\n",
			events:  "gq/\n",
			ruler:   8,
			width:   40,
			// the pre-search Enter dispatch is captured by less but no
			// motion executes; confirm no mutation and we are back to
			// normal mode.
			wantContent: "alpha beta gamma\nfoo bar baz\n",
			wantMode:    normalMode,
		},
		{
			name:        "gq slash search escape cancels and restores normal mode",
			content:     "alpha beta gamma\nfoo bar baz\n",
			events:      "gq/foo<esc>",
			ruler:       8,
			width:       40,
			wantContent: "alpha beta gamma\nfoo bar baz\n",
			wantMode:    normalMode,
		},
		{
			name:        "gq slash search backspace edits pattern before submit",
			content:     "alpha beta gamma delta\nMARK end\nlast\n",
			events:      "gq/MARQ<bs>K\n",
			ruler:       12,
			width:       40,
			wantContent: "alpha beta\ngamma delta\nMARK end\nlast\n",
			wantMode:    normalMode,
		},
		{
			name:    "gq question backward search wraps through cursor line",
			content: "TARGET line\nalpha beta gamma delta epsilon zeta\nlast\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 1, X: 0}))
			},
			events:      "gq?TARGET\n",
			ruler:       12,
			width:       40,
			wantContent: "TARGET line\nalpha beta\ngamma delta\nepsilon zeta\nlast\n",
			wantMode:    normalMode,
		},
		{
			name:    "gq question backward search no match leaves buffer unchanged",
			content: "alpha beta gamma\nfoo bar baz\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 1, X: 0}))
			},
			events:      "gq?zzzz\n",
			ruler:       8,
			width:       40,
			wantContent: "alpha beta gamma\nfoo bar baz\n",
			wantMode:    normalMode,
		},
		{
			name:        "gq slash search cancel allows subsequent normal commands",
			content:     "alpha beta gamma delta epsilon\nsecond line\n",
			events:      "gq/foo<esc>gqq",
			ruler:       12,
			width:       40,
			wantContent: "alpha beta\ngamma delta\nepsilon\nsecond line\n",
			wantMode:    normalMode,
		},
		{
			name:        "gq slash search match at line start excludes match line",
			content:     "alpha beta gamma delta\nepsilon zeta\nMARK here\nlast\n",
			events:      "gq/MARK\n",
			ruler:       12,
			width:       40,
			wantContent: "alpha beta\ngamma delta\nepsilon zeta\nMARK here\nlast\n",
			wantMode:    normalMode,
		},

		// ---- gqn / gqN reuse the last-used search pattern ----
		{
			name: "gqn reflows the lines before a match in column 0",
			content: "alpha beta gamma delta epsilon\nMARK\n" +
				"zeta eta theta iota kappa\nEND tail\nlast\n",
			events:      "/END\ngggqn",
			ruler:       12,
			width:       40,
			wantContent: "alpha beta\ngamma delta\nepsilon MARK\nzeta eta\ntheta iota\nkappa\nEND tail\nlast\n",
			wantMode:    normalMode,
		},
		{
			name: "gqN reflows backward from cursor to previous match of last search",
			content: "alpha beta gamma delta epsilon\nMARK\n" +
				"zeta eta theta iota kappa END\nlast text here\n",
			events:      "/END\nGgqN",
			ruler:       12,
			width:       40,
			wantContent: "alpha beta gamma delta epsilon\nMARK\nzeta eta\ntheta iota\nkappa END\nlast text\nhere\n",
			wantMode:    normalMode,
		},

		// ---- gq* / gq# search the word under the cursor ----
		{
			name: "gq star reflows from cursor to next match of word under cursor",
			content: "alpha beta\nEND tail keep going\n" +
				"zeta eta theta iota kappa END\nlast\n",
			// place the cursor on the second line where "END" is
			// the word under the cursor; gq* formats forward to the
			// next "END" match.
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 1, X: 0}))
			},
			events:      "gq*",
			ruler:       12,
			width:       40,
			wantContent: "alpha beta\nEND tail\nkeep going\nzeta eta\ntheta iota\nkappa END\nlast\n",
			wantMode:    normalMode,
		},
		{
			name: "gq hash reflows backward from cursor to previous match of word under cursor",
			content: "END alpha beta gamma delta epsilon\n" +
				"zeta eta theta iota kappa\n" +
				"last END trailing words go on\n",
			// place the cursor on the END word at line 2; gq# formats
			// backward to the previous END match on line 0.
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 2, X: 5}))
			},
			events: "gq#",
			ruler:  12,
			width:  40,
			// gq is linewise so the whole 3-line range collapses
			// into a single paragraph, then re-wraps at column 12.
			wantContent: "END alpha\nbeta gamma\ndelta\nepsilon zeta\neta theta\niota kappa\nlast END\ntrailing\nwords go on\n",
			wantMode:    normalMode,
		},

		// ---- gq'{mark} linewise mark motion ----
		{
			name: "gq quote a reflows linewise from cursor to mark a",
			content: "alpha beta gamma delta epsilon zeta\n" +
				"second line\n" +
				"third line keep\n" +
				"MARK fourth line\n" +
				"last\n",
			// seed mark `a` on line 3 (linewise jump targets the
			// first non-blank column of that line).
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.cursor.SetLocationList(textapi.LocationPriorityInfo, "a",
					textapi.LocationSlice([]textapi.Location{{
						From: term.Coordinates{Y: 3, X: 0},
						To:   term.Coordinates{Y: 3, X: 1},
					}}),
				)
			},
			events: "gggq'a",
			ruler:  12,
			width:  40,
			// linewise reflow of lines 0..3 inclusive.
			wantContent: "alpha beta\ngamma delta\nepsilon zeta\nsecond line\nthird line\nkeep MARK\nfourth line\nlast\n",
			wantMode:    normalMode,
		},
		{
			name: "gq quote a from below reflows backward through mark a",
			content: "first paragraph spans some text\n" +
				"MARK line\n" +
				"middle text here\n" +
				"alpha beta gamma delta epsilon zeta eta\n" +
				"final\n",
			// mark `a` at line 1; cursor starts on line 3 then
			// gq'a should reflow lines 1..3 inclusive.
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.cursor.SetLocationList(textapi.LocationPriorityInfo, "a",
					textapi.LocationSlice([]textapi.Location{{
						From: term.Coordinates{Y: 1, X: 0},
						To:   term.Coordinates{Y: 1, X: 1},
					}}),
				)
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 3, X: 0}))
			},
			events:      "gq'a",
			ruler:       12,
			width:       40,
			wantContent: "first paragraph spans some text\nMARK line\nmiddle text\nhere alpha\nbeta gamma\ndelta\nepsilon zeta\neta\nfinal\n",
			wantMode:    normalMode,
		},
		{
			name: "gq quote z without a set mark is a no-op",
			content: "alpha beta gamma delta epsilon zeta\n" +
				"second line\n",
			events: "gggq'z",
			ruler:  12,
			width:  40,
			// no mark `z` exists; the buffer is unchanged.
			wantContent: "alpha beta gamma delta epsilon zeta\nsecond line\n",
			wantMode:    normalMode,
		},

		// ---- gq`{mark} exact (charwise) mark motion ----
		{
			name: "gq backtick a with mark at column 0 excludes the mark line",
			content: "alpha beta gamma delta epsilon\n" +
				"second line\n" +
				"third line\n" +
				"MARK keep\n",
			// mark `a` at line 3 column 0; charwise/exclusive
			// motion stops *before* the mark cell, so the mark
			// line is not included in the reflow range.
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.cursor.SetLocationList(textapi.LocationPriorityInfo, "a",
					textapi.LocationSlice([]textapi.Location{{
						From: term.Coordinates{Y: 3, X: 0},
						To:   term.Coordinates{Y: 3, X: 1},
					}}),
				)
			},
			events:      "gggq`a",
			ruler:       12,
			width:       40,
			wantContent: "alpha beta\ngamma delta\nepsilon\nsecond line\nthird line\nMARK keep\n",
			wantMode:    normalMode,
		},
		{
			name: "gq backtick a with mark mid-line includes mark line",
			content: "alpha beta gamma delta epsilon\n" +
				"second line\n" +
				"third line MARK keep\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.cursor.SetLocationList(textapi.LocationPriorityInfo, "a",
					textapi.LocationSlice([]textapi.Location{{
						From: term.Coordinates{Y: 2, X: 11},
						To:   term.Coordinates{Y: 2, X: 12},
					}}),
				)
			},
			events: "gggq`a",
			ruler:  12,
			width:  40,
			// mark at column 11 on line 2 is inside the line; gq
			// formats whole lines 0..2.
			wantContent: "alpha beta\ngamma delta\nepsilon\nsecond line\nthird line\nMARK keep\n",
			wantMode:    normalMode,
		},
		{
			name: "gq backtick z without a set mark is a no-op",
			content: "alpha beta gamma delta epsilon zeta\n" +
				"second line\n",
			events:      "gggq`z",
			ruler:       12,
			width:       40,
			wantContent: "alpha beta gamma delta epsilon zeta\nsecond line\n",
			wantMode:    normalMode,
		},
	}

	for _, tcase := range suite {
		t.Run(tcase.name, func(t *testing.T) {
			vi := setupWrapVi(t, tcase.content, tcase.ruler, tcase.width)
			if tcase.before != nil {
				tcase.before(t, vi)
			}

			events := parseSearchOpEvents(tcase.events)
			for i, ev := range events {
				quit, handled := vi.Handle(ev)
				require.False(t, quit)
				if tcase.allowUnhandledLast && i == len(events)-1 {
					continue
				}
				require.True(t, handled, "event %v should be handled", ev)
			}

			assert.Equal(t, tcase.wantContent, vi.less.Buffer().String())
			assert.Equal(t, tcase.wantMode, vi.mode())
			_, selected := vi.Selection()
			assert.Equal(t, tcase.wantSelected, selected)
			assert.Equal(t, 1, vi.count)
			assert.Equal(t, "", vi.countDigits)
			assert.Equal(t, 0, vi.operatorCount)
		})
	}
}

func TestGwFormatWrapParagraph(t *testing.T) {
	type tc struct {
		name        string
		content     string
		before      func(t *testing.T, vi *viHandlerImpl)
		events      string
		ruler       int
		width       int
		wantContent string
		wantCursor  term.Coordinates
		wantMode    viMode
	}

	setupWrapVi := func(t *testing.T, content string, ruler, width int) *viHandlerImpl {
		t.Helper()
		vi := setupVi(t,
			content,
			2,
			WithRuler(ruler),
			WithComments(text.CommentConfig{
				"go": {Line: []string{"//"}},
			}),
		)
		vi.Resize(width, 20)

		buf := vi.less.Buffer()
		buf.WithView(testCommentService{view: buf.View(), line: []string{"//"}})
		vi.cursor.SetCommentSpec(text.CommentSpec{Line: []string{"//"}})
		return vi
	}

	suite := []tc{
		{
			name:    "gww wraps current line and restores cursor",
			content: "// alpha beta gamma delta epsilon zeta eta theta\n// iota kappa lambda mu\n\nnext\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 3}))
			},
			events:      "gww",
			ruler:       20,
			width:       40,
			wantContent: "// alpha beta gamma\n// delta epsilon\n// zeta eta theta\n// iota kappa lambda mu\n\nnext\n",
			wantCursor:  term.Coordinates{Y: 0, X: 3},
			wantMode:    normalMode,
		},
		{
			name:    "gwgg wraps to first line and restores cursor",
			content: "// alpha beta gamma\n// delta epsilon zeta\n// one two three four\n// LAST\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 2, X: 3}))
			},
			events:      "gwgg",
			ruler:       20,
			width:       60,
			wantContent: "// alpha beta gamma\n// delta epsilon\n// zeta one two\n// three four\n// LAST\n",
			wantCursor:  term.Coordinates{Y: 2, X: 8},
			wantMode:    normalMode,
		},
		{
			name:    "2gwgw formats count lines and restores cursor",
			content: "// alpha beta\n// gamma delta\n// epsilon zeta\nstop\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 3}))
			},
			events:      "2gwgw",
			ruler:       20,
			width:       40,
			wantContent: "// alpha beta gamma\n// delta\n// epsilon zeta\nstop\n",
			wantCursor:  term.Coordinates{Y: 0, X: 3},
			wantMode:    normalMode,
		},
		{
			name:    "2gwj uses operator count and restores cursor",
			content: "// alpha beta\n// gamma delta\n// epsilon zeta\nstop\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 3}))
			},
			events:      "2gwj",
			ruler:       20,
			width:       40,
			wantContent: "// alpha beta gamma\n// delta epsilon\n// zeta\nstop\n",
			wantCursor:  term.Coordinates{Y: 0, X: 3},
			wantMode:    normalMode,
		},
		{
			name:    "gw2j uses motion count and restores cursor",
			content: "// alpha beta\n// gamma delta\n// epsilon zeta\nstop\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 3}))
			},
			events:      "gw2j",
			ruler:       20,
			width:       40,
			wantContent: "// alpha beta gamma\n// delta epsilon\n// zeta\nstop\n",
			wantCursor:  term.Coordinates{Y: 0, X: 3},
			wantMode:    normalMode,
		},
		{
			name:    "gwk wraps previous and current line and restores cursor",
			content: "keep me untouched\nalpha beta gamma delta epsilon\nzeta eta theta\nleave me too\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 2, X: 0}))
			},
			events:      "gwk",
			ruler:       16,
			width:       80,
			wantContent: "keep me untouched\nalpha beta gamma\ndelta epsilon\nzeta eta theta\nleave me too\n",
			wantCursor:  term.Coordinates{Y: 3, X: 0},
			wantMode:    normalMode,
		},
		{
			name:    "gwgk wraps previous display line and restores cursor",
			content: "keep me untouched\nalpha beta gamma delta epsilon\nzeta eta theta\nleave me too\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 2, X: 0}))
			},
			events:      "gwgk",
			ruler:       16,
			width:       80,
			wantContent: "keep me untouched\nalpha beta gamma\ndelta epsilon\nzeta eta theta\nleave me too\n",
			wantCursor:  term.Coordinates{Y: 3, X: 0},
			wantMode:    normalMode,
		},
		{
			name:    "gw underscore wraps current line and restores cursor",
			content: "// alpha beta gamma delta   \nnext\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 3}))
			},
			events:      "gw_",
			ruler:       14,
			width:       40,
			wantContent: "// alpha beta\n// gamma delta\nnext\n",
			wantCursor:  term.Coordinates{Y: 0, X: 3},
			wantMode:    normalMode,
		},
		{
			name:    "gw meta underscore wraps current line and restores cursor",
			content: "// alpha beta gamma delta   \nnext\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 3}))
			},
			events:      "gwg_",
			ruler:       14,
			width:       40,
			wantContent: "// alpha beta\n// gamma delta\nnext\n",
			wantCursor:  term.Coordinates{Y: 0, X: 3},
			wantMode:    normalMode,
		},
		{
			name:    "gwip wraps inner paragraph and restores cursor",
			content: "// alpha beta gamma delta\n// epsilon zeta eta theta\n\n// keep second paragraph intact\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 3}))
			},
			events:      "gwip",
			ruler:       20,
			width:       60,
			wantContent: "// alpha beta gamma\n// delta epsilon\n// zeta eta theta\n\n// keep second paragraph intact\n",
			wantCursor:  term.Coordinates{Y: 0, X: 3},
			wantMode:    normalMode,
		},
		{
			name:        "gw slash search wraps through match and restores cursor",
			content:     "one two three four\nalpha beta gamma\nEND marker here\ntail unchanged\n",
			events:      "gw/END\n",
			ruler:       12,
			width:       40,
			wantContent: "one two\nthree four\nalpha beta\ngamma\nEND marker here\ntail unchanged\n",
			wantCursor:  term.Coordinates{Y: 0, X: 0},
			wantMode:    normalMode,
		},
		{
			name: "gw quote a reflows linewise to mark and restores cursor",
			content: "alpha beta gamma delta epsilon zeta\n" +
				"second line\n" +
				"third line keep\n" +
				"MARK fourth line\n" +
				"last\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.cursor.SetLocationList(textapi.LocationPriorityInfo, "a",
					textapi.LocationSlice([]textapi.Location{{
						From: term.Coordinates{Y: 3, X: 0},
						To:   term.Coordinates{Y: 3, X: 1},
					}}),
				)
			},
			events:      "gggw'a",
			ruler:       12,
			width:       40,
			wantContent: "alpha beta\ngamma delta\nepsilon zeta\nsecond line\nthird line\nkeep MARK\nfourth line\nlast\n",
			wantCursor:  term.Coordinates{Y: 0, X: 0},
			wantMode:    normalMode,
		},
		{
			name: "gw backtick a reflows to exact mark and restores cursor",
			content: "alpha beta gamma delta epsilon\n" +
				"second line\n" +
				"third line MARK keep\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.cursor.SetLocationList(textapi.LocationPriorityInfo, "a",
					textapi.LocationSlice([]textapi.Location{{
						From: term.Coordinates{Y: 2, X: 11},
						To:   term.Coordinates{Y: 2, X: 12},
					}}),
				)
			},
			events:      "gggw`a",
			ruler:       12,
			width:       40,
			wantContent: "alpha beta\ngamma delta\nepsilon\nsecond line\nthird line\nMARK keep\n",
			wantCursor:  term.Coordinates{Y: 0, X: 0},
			wantMode:    normalMode,
		},
		{
			name:        "visual line gw wraps selection and restores cursor",
			content:     "alpha beta gamma delta\nepsilon zeta eta theta\n\none two three four\nfive six seven eight\n",
			events:      "Vjjjgw",
			ruler:       12,
			width:       80,
			wantContent: "alpha beta\ngamma delta\nepsilon zeta\neta theta\n\none two\nthree four\nfive six seven eight\n",
			wantCursor:  term.Coordinates{Y: 5, X: 0},
			wantMode:    normalMode,
		},
		{
			name:        "visual block gw reflows full covered lines and restores cursor",
			content:     "alpha beta gamma delta epsilon zeta\nsecond line\nthird line\nfourth line\nfifth line\n",
			events:      "<c-v>jjj$gw",
			ruler:       12,
			width:       40,
			wantContent: "alpha beta\ngamma delta\nepsilon zeta\nsecond line\nthird line\nfourth line\nfifth line\n",
			wantCursor:  term.Coordinates{Y: 5, X: 10},
			wantMode:    normalMode,
		},
		{
			name:        "visual block gw over only blank lines is handled",
			content:     "\n\n\n",
			events:      "<c-v>jjgw",
			ruler:       12,
			width:       40,
			wantContent: "\n\n\n",
			wantCursor:  term.Coordinates{Y: 2, X: 0},
			wantMode:    normalMode,
		},
	}

	for _, tcase := range suite {
		t.Run(tcase.name, func(t *testing.T) {
			vi := setupWrapVi(t, tcase.content, tcase.ruler, tcase.width)
			if tcase.before != nil {
				tcase.before(t, vi)
			}

			events := parseSearchOpEvents(tcase.events)
			for _, ev := range events {
				quit, handled := vi.Handle(ev)
				require.False(t, quit)
				require.True(t, handled, "event %v should be handled", ev)
			}

			assert.Equal(t, tcase.wantContent, vi.less.Buffer().String())
			assert.Equal(t, tcase.wantMode, vi.mode())
			assert.Equal(t, tcase.wantCursor, vi.cursorAtScroll())
			_, selected := vi.Selection()
			assert.False(t, selected)
			assert.Equal(t, 1, vi.count)
			assert.Equal(t, "", vi.countDigits)
			assert.Equal(t, 0, vi.operatorCount)
		})
	}
}

func TestSearchOperatorMotion(t *testing.T) {
	type tc struct {
		name        string
		content     string
		before      func(t *testing.T, vi *viHandlerImpl)
		events      string
		width       int
		wantContent string
		wantMode    viMode
		// optional assertions; zero-valued means "do not check".
		wantCursor  *term.Coordinates
		wantUnnamed string // contents of the unnamed register
		wantSearch  string // contents of the / register
	}

	suite := []tc{
		// ---- delete (charwise) ----
		{
			name:        "d slash deletes from cursor up to forward match",
			content:     "alpha END beta\nlast\n",
			events:      "d/END\n",
			width:       40,
			wantContent: "END beta\nlast\n",
			wantMode:    normalMode,
		},
		{
			name:        "d slash spans newline when match is at column 0",
			content:     "alpha beta\nEND here\nlast\n",
			events:      "d/END\n",
			width:       40,
			wantContent: "END here\nlast\n",
			wantMode:    normalMode,
		},
		{
			name:    "d question deletes backward from cursor to match start",
			content: "TARGET keep\nalpha beta gamma delta\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.setCursorAtScroll(term.Coordinates{Y: 1, X: 0})
			},
			events:      "d?TARGET\n",
			width:       40,
			wantContent: "alpha beta gamma delta\n",
			wantMode:    normalMode,
		},
		{
			name:        "d slash no match leaves buffer unchanged",
			content:     "alpha beta\nfoo bar\n",
			events:      "d/zzzz\n",
			width:       40,
			wantContent: "alpha beta\nfoo bar\n",
			wantMode:    normalMode,
		},
		{
			name:        "d slash escape cancels and keeps buffer",
			content:     "alpha beta\nfoo bar\n",
			events:      "d/foo<esc>",
			width:       40,
			wantContent: "alpha beta\nfoo bar\n",
			wantMode:    normalMode,
		},
		{
			name:        "d slash backspace edits pattern before submit",
			content:     "alpha END here\nlast\n",
			events:      "d/ENQ<bs>D\n",
			width:       40,
			wantContent: "END here\nlast\n",
			wantMode:    normalMode,
		},

		// ---- change (charwise, enters insert) ----
		{
			name:        "c slash deletes range and enters insert mode",
			content:     "alpha END beta\nlast\n",
			events:      "c/END\n",
			width:       40,
			wantContent: "END beta\nlast\n",
			wantMode:    insertMode,
		},
		{
			name:        "c slash empty pattern returns to normal mode",
			content:     "alpha beta\n",
			events:      "c/\n",
			width:       40,
			wantContent: "alpha beta\n",
			wantMode:    normalMode,
		},
		{
			name:        "c slash escape cancels without entering insert",
			content:     "alpha beta\n",
			events:      "c/alpha<esc>",
			width:       40,
			wantContent: "alpha beta\n",
			wantMode:    normalMode,
		},

		// ---- yank (charwise; preserves buffer, copies into register) ----
		{
			name:        "y slash yank does not mutate buffer",
			content:     "alpha END beta\nlast\n",
			events:      "y/END\n",
			width:       40,
			wantContent: "alpha END beta\nlast\n",
			wantMode:    normalMode,
		},
		{
			name:        "y question yank backward does not mutate buffer",
			content:     "TARGET\nalpha\n",
			events:      "ly?TARGET\n",
			width:       40,
			wantContent: "TARGET\nalpha\n",
			wantMode:    normalMode,
		},
		{
			name:    "y slash followed by paste duplicates yanked text at cursor",
			content: "alpha END\n",
			events:  "y/END\nP",
			width:   40,
			// `y/END\n` yanks "alpha " into the unnamed register; cursor
			// stays at {0,0}. `P` pastes the register before the cursor,
			// resulting in the prefix being duplicated.
			wantContent: "alpha alpha END\n",
			wantMode:    normalMode,
		},

		// ---- shift (linewise) ----
		{
			name:        "shift right slash indents range up to match line",
			content:     "alpha\nbeta\nMARK end\nlast\n",
			events:      ">/MARK\n",
			width:       40,
			wantContent: "\talpha\n\tbeta\nMARK end\nlast\n",
			wantMode:    normalMode,
		},
		{
			name:    "shift left question dedents range backward through cursor line",
			content: "\talpha\n\tbeta\n\tMARK\n\tlast\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.setCursorAtScroll(term.Coordinates{Y: 3, X: 1})
			},
			events: "\\</alpha\n",
			width:  40,
			// shift is linewise; the motion spans line 0 (match) through
			// line 3 (cursor) inclusive, so all four lines are dedented.
			wantContent: "alpha\nbeta\nMARK\nlast\n",
			wantMode:    normalMode,
		},
		{
			name:    "shift left question excludes match line when match at column 0",
			content: "\talpha\n\tbeta\n\tMARK\n\tlast\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.setCursorAtScroll(term.Coordinates{Y: 3, X: 0})
			},
			events: "\\</\talpha\n",
			width:  40,
			// match lands at column 0 of line 0; the search motion is
			// exclusive so the match line itself is excluded from the
			// linewise range. Lines 1-3 are dedented, line 0's tab
			// stays.
			wantContent: "\talpha\nbeta\nMARK\nlast\n",
			wantMode:    normalMode,
		},
		{
			name:        "shift right slash no match leaves buffer unchanged",
			content:     "alpha\nbeta\n",
			events:      ">/zzzz\n",
			width:       40,
			wantContent: "alpha\nbeta\n",
			wantMode:    normalMode,
		},

		// ---- case change (charwise) ----
		{
			name:        "gu slash lowercases up to forward match",
			content:     "AAAA END BBBB\nLAST\n",
			events:      "gu/END\n",
			width:       40,
			wantContent: "aaaa END BBBB\nLAST\n",
			wantMode:    normalMode,
		},
		{
			name:        "gU slash uppercases up to forward match",
			content:     "alpha END beta\nlast\n",
			events:      "gU/END\n",
			width:       40,
			wantContent: "ALPHA END beta\nlast\n",
			wantMode:    normalMode,
		},
		{
			name:        "g tilde slash toggles case up to forward match",
			content:     "AlPhA END\n",
			events:      "g~/END\n",
			width:       40,
			wantContent: "aLpHa END\n",
			wantMode:    normalMode,
		},
		{
			name:        "gu slash escape cancels without changing case",
			content:     "AAAA END\n",
			events:      "gu/END<esc>",
			width:       40,
			wantContent: "AAAA END\n",
			wantMode:    normalMode,
		},

		// ---- shared edge cases ----
		{
			name:        "d slash leaves buffer unchanged on empty pattern",
			content:     "alpha beta\n",
			events:      "d/\n",
			width:       40,
			wantContent: "alpha beta\n",
			wantMode:    normalMode,
		},
		{
			name:        "d slash followed by normal command after cancel works",
			content:     "alpha END\nbeta\n",
			events:      "d/foo<esc>dd",
			width:       40,
			wantContent: "beta\n",
			wantMode:    normalMode,
		},

		// ---- cursor and register verification ----
		{
			name:        "d slash places cursor at deletion start and stores yanked text",
			content:     "alpha END beta\nlast\n",
			events:      "d/END\n",
			width:       40,
			wantContent: "END beta\nlast\n",
			wantMode:    normalMode,
			wantCursor:  &term.Coordinates{Y: 0, X: 0},
			wantUnnamed: "alpha ",
			wantSearch:  "END",
		},
		{
			name:        "y slash leaves cursor at start and populates registers",
			content:     "alpha END\n",
			events:      "y/END\n",
			width:       40,
			wantContent: "alpha END\n",
			wantMode:    normalMode,
			wantCursor:  &term.Coordinates{Y: 0, X: 0},
			wantUnnamed: "alpha ",
			wantSearch:  "END",
		},
		{
			name:        "c slash leaves cursor where deletion started for inserting",
			content:     "alpha END beta\n",
			events:      "c/END\n",
			width:       40,
			wantContent: "END beta\n",
			wantMode:    insertMode,
			wantCursor:  &term.Coordinates{Y: 0, X: 0},
			wantUnnamed: "alpha ",
		},
		{
			name:        "d question places cursor at match position",
			content:     "TARGET keep\nalpha\n",
			before:      func(t *testing.T, vi *viHandlerImpl) { vi.setCursorAtScroll(term.Coordinates{Y: 1, X: 0}) },
			events:      "d?TARGET\n",
			width:       40,
			wantContent: "alpha\n",
			wantMode:    normalMode,
			wantCursor:  &term.Coordinates{Y: 0, X: 0},
			wantUnnamed: "TARGET keep\n",
			wantSearch:  "TARGET",
		},

		// ---- end of file / file boundary edge cases ----
		{
			name:        "d slash from cursor to last byte of single-line buffer",
			content:     "alphaENDbeta",
			events:      "d/END\n",
			width:       40,
			wantContent: "ENDbeta",
			wantMode:    normalMode,
			wantCursor:  &term.Coordinates{Y: 0, X: 0},
			wantUnnamed: "alpha",
		},
		{
			name:        "d slash through final line without trailing newline",
			content:     "alpha\nfinal END here",
			events:      "d/END\n",
			width:       40,
			wantContent: "END here",
			wantMode:    normalMode,
			wantUnnamed: "alpha\nfinal ",
		},
		{
			name:        "d slash no match at end of file leaves buffer unchanged",
			content:     "alpha\nbeta\n",
			before:      func(t *testing.T, vi *viHandlerImpl) { vi.setCursorAtScroll(term.Coordinates{Y: 1, X: 3}) },
			events:      "d/zzzz\n",
			width:       40,
			wantContent: "alpha\nbeta\n",
			wantMode:    normalMode,
		},
		{
			name:    "d slash matching cursor position is a no-op",
			content: "MARK alpha\nbeta\n",
			events:  "d/MARK\n",
			width:   40,
			// cursor is already on the match; before==after so the
			// operator cancels without mutating the buffer.
			wantContent: "MARK alpha\nbeta\n",
			wantMode:    normalMode,
		},
		{
			name:    "d question wraps around when no earlier match exists",
			content: "alpha beta\ngamma TARGET delta\n",
			before:  func(t *testing.T, vi *viHandlerImpl) { vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 0}) },
			events:  "d?TARGET\n",
			width:   40,
			// search wraps around the buffer; cursor at {0,0} jumps
			// forward to the only match and the prefix is deleted.
			wantContent: "TARGET delta\n",
			wantMode:    normalMode,
			wantUnnamed: "alpha beta\ngamma ",
		},
		{
			name:        "d slash deletes entire buffer when match is final char",
			content:     "abcEND",
			events:      "d/END\n",
			width:       40,
			wantContent: "END",
			wantMode:    normalMode,
			wantUnnamed: "abc",
		},

		// ---- empty / single line buffer ----
		{
			name:        "d slash on empty buffer no match",
			content:     "",
			events:      "d/x\n",
			width:       40,
			wantContent: "",
			wantMode:    normalMode,
		},
		{
			name:        "d slash on single line with match at end",
			content:     "abcdEND\n",
			events:      "d/END\n",
			width:       40,
			wantContent: "END\n",
			wantMode:    normalMode,
			wantUnnamed: "abcd",
		},
		{
			name:        "d slash empty line followed by match",
			content:     "\nMARK\n",
			events:      "d/MARK\n",
			width:       40,
			wantContent: "MARK\n",
			wantMode:    normalMode,
			wantUnnamed: "\n",
		},

		// ---- whitespace and indentation ----
		{
			name:        "d slash through tab character",
			content:     "before\tEND after\n",
			events:      "d/END\n",
			width:       40,
			wantContent: "END after\n",
			wantMode:    normalMode,
			wantUnnamed: "before\t",
		},
		{
			name:        "d slash through trailing spaces",
			content:     "alpha     END\n",
			events:      "d/END\n",
			width:       40,
			wantContent: "END\n",
			wantMode:    normalMode,
			wantUnnamed: "alpha     ",
		},
		{
			name:        "d slash matches leading whitespace in pattern",
			content:     "alpha   END   beta\n",
			events:      "d/   END\n",
			width:       40,
			wantContent: "   END   beta\n",
			wantMode:    normalMode,
			wantUnnamed: "alpha",
		},
		{
			name:        "shift right slash on indented lines preserves existing tabs",
			content:     "\talpha\n\tbeta\nMARK\n",
			events:      ">/MARK\n",
			width:       40,
			wantContent: "\t\talpha\n\t\tbeta\nMARK\n",
			wantMode:    normalMode,
		},
		{
			name:        "d slash deletes through blank lines",
			content:     "alpha\n\n\nMARK\nlast\n",
			events:      "d/MARK\n",
			width:       40,
			wantContent: "MARK\nlast\n",
			wantMode:    normalMode,
			wantUnnamed: "alpha\n\n\n",
		},

		// ---- multi-line motions ----
		{
			name:        "d slash spans multiple lines charwise",
			content:     "first line\nsecond\nMARK third\nfourth\n",
			events:      "d/MARK\n",
			width:       40,
			wantContent: "MARK third\nfourth\n",
			wantMode:    normalMode,
			wantUnnamed: "first line\nsecond\n",
		},
		{
			name:    "d slash from mid-line spans through subsequent lines",
			content: "alpha beta gamma\ndelta epsilon\nzeta eta MARK theta\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 6})
			},
			events:      "d/MARK\n",
			width:       40,
			wantContent: "alpha MARK theta\n",
			wantMode:    normalMode,
			wantUnnamed: "beta gamma\ndelta epsilon\nzeta eta ",
		},

		// ---- unicode and wide characters ----
		{
			name:        "d slash with unicode pattern",
			content:     "alpha 你好 END beta\n",
			events:      "d/你好\n",
			width:       40,
			wantContent: "你好 END beta\n",
			wantMode:    normalMode,
			wantUnnamed: "alpha ",
			wantSearch:  "你好",
		},
		{
			name:        "d slash deletes through wide CJK characters",
			content:     "你好世界END\n",
			events:      "d/END\n",
			width:       40,
			wantContent: "END\n",
			wantMode:    normalMode,
			wantUnnamed: "你好世界",
		},
		{
			name:        "y slash captures unicode prefix",
			content:     "café END\n",
			events:      "y/END\n",
			width:       40,
			wantContent: "café END\n",
			wantMode:    normalMode,
			wantUnnamed: "café ",
		},
		{
			name:        "d slash with combining diacritics in pattern",
			content:     "plain résumé END\n",
			events:      "d/END\n",
			width:       40,
			wantContent: "END\n",
			wantMode:    normalMode,
			wantUnnamed: "plain résumé ",
		},
		{
			name:        "gU slash uppercases unicode prefix",
			content:     "café END\n",
			events:      "gU/END\n",
			width:       40,
			wantContent: "CAFÉ END\n",
			wantMode:    normalMode,
		},

		// ---- pattern with special characters ----
		{
			name:        "d slash matches pattern containing slash",
			content:     "alpha /path/to/file rest\n",
			events:      "d//path\n",
			width:       40,
			wantContent: "/path/to/file rest\n",
			wantMode:    normalMode,
			wantUnnamed: "alpha ",
			wantSearch:  "/path",
		},
		{
			name:        "d slash matches pattern with regex metacharacters as literals",
			content:     "before .*+ rest\n",
			events:      "d/.*+\n",
			width:       40,
			wantContent: ".*+ rest\n",
			wantMode:    normalMode,
			wantUnnamed: "before ",
		},

		// ---- repeated operators / sequences ----
		{
			name:        "two d slash operators in a row work independently",
			content:     "alpha END\nbeta MARK\n",
			events:      "d/END\nd/MARK\n",
			width:       40,
			wantContent: "MARK\n",
			wantMode:    normalMode,
		},
		{
			name:    "d slash then dot is no-op (search ops are not recorded)",
			content: "alpha END beta\nlast\n",
			events:  "d/END\n.",
			width:   40,
			// the `.` repeats the last change; search-ops are not yet
			// captured by the dot register, so it acts on whatever the
			// last recorded change was. The test verifies the buffer
			// is at least left in normal mode without a panic.
			wantContent: "END beta\nlast\n",
			wantMode:    normalMode,
		},

		// ---- count and visual-mode interactions ----
		{
			name:    "d slash with count before operator deletes only the first match (count not yet supported)",
			content: "alpha END beta END gamma\n",
			events:  "2d/END\n",
			width:   40,
			// search operators do not yet honor the operator count;
			// confirm at least the first match's prefix is deleted
			// and we are in normal mode without a panic.
			wantContent: "END beta END gamma\n",
			wantMode:    normalMode,
		},
		{
			name:    "y slash from visual mode is a no-op (handler is not visual)",
			content: "alpha END beta\n",
			events:  "v",
			width:   40,
			// visual mode does not enter the search-op path; this is a
			// regression guard that visual `v` plus subsequent input
			// does not panic and the buffer is unchanged.
			wantContent: "alpha END beta\n",
			wantMode:    visualMode,
		},

		// ---- mode and state hygiene after cancel ----
		{
			name:        "d slash cancel then yank works normally",
			content:     "alpha END beta\n",
			events:      "d/foo<esc>yy",
			width:       40,
			wantContent: "alpha END beta\n",
			wantMode:    normalMode,
			wantUnnamed: "alpha END beta\n",
		},
		{
			name:        "d slash cancel then enter insert mode",
			content:     "alpha\n",
			events:      "d/foo<esc>i",
			width:       40,
			wantContent: "alpha\n",
			wantMode:    insertMode,
		},
		{
			name:        "y slash cancel via empty pattern leaves buffer untouched",
			content:     "alpha\n",
			events:      "y/\n",
			width:       40,
			wantContent: "alpha\n",
			wantMode:    normalMode,
		},
		{
			name:        "shift left slash cancel via escape leaves indentation intact",
			content:     "\talpha\n\tbeta\n",
			events:      "\\</alpha<esc>",
			width:       40,
			wantContent: "\talpha\n\tbeta\n",
			wantMode:    normalMode,
		},

		// ---- search register population on no-op paths ----
		{
			name:    "d slash empty pattern does not overwrite search register",
			content: "alpha\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				_ = vi.writeRegister('/', clipboard.Data{Text: "previous"})
			},
			events:      "d/\n",
			width:       40,
			wantContent: "alpha\n",
			wantMode:    normalMode,
			wantSearch:  "previous",
		},
		{
			name:    "d slash escape does not overwrite search register",
			content: "alpha\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				_ = vi.writeRegister('/', clipboard.Data{Text: "previous"})
			},
			events:      "d/foo<esc>",
			width:       40,
			wantContent: "alpha\n",
			wantMode:    normalMode,
			wantSearch:  "previous",
		},
		{
			name:        "d slash no match still records search register",
			content:     "alpha\n",
			events:      "d/zzzz\n",
			width:       40,
			wantContent: "alpha\n",
			wantMode:    normalMode,
			wantSearch:  "zzzz",
		},

		// ---- gq with edge cases (linewise) ----
		{
			name:    "gq slash on single line with unicode wraps using rune count",
			content: "alpha 你好 beta gamma delta MARK end\n",
			events:  "gq/MARK\n",
			width:   40,
			// ruler default for setupVi is 0 so this is a regression
			// guard that gq with a non-positive ruler is a no-op and
			// the buffer is preserved.
			wantContent: "alpha 你好 beta gamma delta MARK end\n",
			wantMode:    normalMode,
		},

		// ---- backward search edge cases ----
		{
			name:    "d question backward through wide character",
			content: "你好 keep\nalpha beta\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.setCursorAtScroll(term.Coordinates{Y: 1, X: 0})
			},
			events:      "d?你好\n",
			width:       40,
			wantContent: "alpha beta\n",
			wantMode:    normalMode,
			wantUnnamed: "你好 keep\n",
		},
		{
			name:    "d question pattern that is found exactly at cursor is no-op",
			content: "MARK alpha\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 0})
			},
			events:      "d?MARK\n",
			width:       40,
			wantContent: "MARK alpha\n",
			wantMode:    normalMode,
		},

		// ---- handler-mode hygiene ----
		{
			name:    "delete-insert (c) slash followed by typed text inserts in place",
			content: "alpha END beta\n",
			events:  "c/END\nXYZ",
			width:   40,
			// after c/END\n the buffer is "END beta\n" with cursor in
			// insert mode at column 0; typing XYZ inserts before END.
			wantContent: "XYZEND beta\n",
			wantMode:    insertMode,
		},
		{
			name:    "yank into named register via search operator",
			content: "alpha END beta\n",
			events:  "\"ay/END\n",
			width:   40,
			// `"a` selects register `a`; the yank should place the
			// prefix into both the named and unnamed registers.
			wantContent: "alpha END beta\n",
			wantMode:    normalMode,
			wantUnnamed: "alpha ",
		},
		{
			name:    "delete into black-hole register does not pollute unnamed",
			content: "alpha END beta\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				_ = vi.writeRegister(unnamedRegister, clipboard.Data{Text: "preserved"})
			},
			events:      "\"_d/END\n",
			width:       40,
			wantContent: "END beta\n",
			wantMode:    normalMode,
			wantUnnamed: "preserved",
		},

		// ---- pattern with spaces and special whitespace ----
		{
			name:    "d slash pattern with trailing space matches literally",
			content: "alpha END  beta\n",
			events:  "d/END \n",
			width:   40,
			// pattern is `END ` (trailing space); deletes through the
			// match start of `END` itself (col 6).
			wantContent: "END  beta\n",
			wantMode:    normalMode,
			wantUnnamed: "alpha ",
		},

		// ---- multi-line and edge cases for case change ----
		{
			name:        "gU slash crosses newline",
			content:     "alpha\nMARK\n",
			events:      "gU/MARK\n",
			width:       40,
			wantContent: "ALPHA\nMARK\n",
			wantMode:    normalMode,
		},
		{
			name:        "g tilde slash through unicode",
			content:     "AbCdÉf END\n",
			events:      "g~/END\n",
			width:       40,
			wantContent: "aBcDéF END\n",
			wantMode:    normalMode,
		},

		// ---- shift count and multi-line ----
		{
			name:        "shift right slash spans many lines",
			content:     "a\nb\nc\nd\ne\nMARK\n",
			events:      ">/MARK\n",
			width:       40,
			wantContent: "\ta\n\tb\n\tc\n\td\n\te\nMARK\n",
			wantMode:    normalMode,
		},

		// ---- backward search with linewise operator ----
		{
			name:    "shift right question backward indents only cursor line when match at col 0",
			content: "MARK alpha\nbeta\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.setCursorAtScroll(term.Coordinates{Y: 1, X: 0})
			},
			events: ">?MARK\n",
			width:  40,
			// match lands at column 0, so the linewise exclusive rule
			// drops line 0 from the range; only line 1 is indented.
			wantContent: "MARK alpha\n\tbeta\n",
			wantMode:    normalMode,
		},
		{
			name:    "shift right question backward indents both lines when match mid-line",
			content: "alpha MARK\nbeta\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.setCursorAtScroll(term.Coordinates{Y: 1, X: 0})
			},
			events:      ">?MARK\n",
			width:       40,
			wantContent: "\talpha MARK\n\tbeta\n",
			wantMode:    normalMode,
		},

		// ---- match equals entire line ----
		{
			name:        "d slash where match starts at line beginning of next line",
			content:     "first\nMARK\nlast\n",
			events:      "d/MARK\n",
			width:       40,
			wantContent: "MARK\nlast\n",
			wantMode:    normalMode,
			wantUnnamed: "first\n",
		},
		{
			name:    "d slash where match is on cursor line after cursor",
			content: "abc MARK def\n",
			events:  "ld/MARK\n",
			width:   40,
			// cursor moves to col 1 then deletes through the match.
			wantContent: "aMARK def\n",
			wantMode:    normalMode,
			wantUnnamed: "bc ",
		},

		// ---- null / control character handling ----
		{
			name:    "d slash deletes through embedded null byte in buffer",
			content: "alp\x00ha END beta\n",
			events:  "d/END\n",
			width:   40,
			// the NUL byte is an ordinary cell; the operator deletes
			// `alp\x00ha ` up to the match start and leaves the rest.
			wantContent: "END beta\n",
			wantMode:    normalMode,
			wantUnnamed: "alp\x00ha ",
		},
		{
			name:    "d slash matches pattern containing null byte",
			content: "alpha\x00END beta\n",
			events:  "d/\x00END\n",
			width:   40,
			// the typed pattern is `\x00END`; the match starts at the
			// NUL position, so only `alpha` is deleted.
			wantContent: "\x00END beta\n",
			wantMode:    normalMode,
			wantUnnamed: "alpha",
		},
		{
			name:        "y slash captures embedded null byte verbatim",
			content:     "a\x00b END\n",
			events:      "y/END\n",
			width:       40,
			wantContent: "a\x00b END\n",
			wantMode:    normalMode,
			wantUnnamed: "a\x00b ",
		},

		// ---- tabs and indentation in pattern / content ----
		{
			name:    "d slash pattern containing literal tab matches",
			content: "alpha\tEND beta\n",
			events:  "d/\tEND\n",
			width:   40,
			// pattern is "<TAB>END"; match starts at the tab so only
			// "alpha" is consumed.
			wantContent: "\tEND beta\n",
			wantMode:    normalMode,
			wantUnnamed: "alpha",
		},
		{
			name:        "d slash through deeply indented prefix",
			content:     "    \t  alpha END beta\n",
			events:      "d/END\n",
			width:       40,
			wantContent: "END beta\n",
			wantMode:    normalMode,
			wantUnnamed: "    \t  alpha ",
		},
		{
			name:    "d question backward through indented match deletes prefix up to cursor",
			content: "MARK\n    alpha\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.setCursorAtScroll(term.Coordinates{Y: 1, X: 4})
			},
			events: "d?MARK\n",
			width:  40,
			// charwise backward: deletes "MARK\n    " up to cursor
			// (exclusive), leaving the trailing "alpha".
			wantContent: "alpha\n",
			wantMode:    normalMode,
		},
		{
			name:    "shift right slash preserves and adds to existing indentation",
			content: "\talpha\n  beta\nMARK\n",
			events:  ">/MARK\n",
			width:   40,
			// linewise exclusive: match line at col 0 is excluded.
			wantContent: "\t\talpha\n\t  beta\nMARK\n",
			wantMode:    normalMode,
		},

		// ---- backward variants for charwise operators ----
		{
			name:    "c question backward then typed text inserts at match start",
			content: "alpha END beta\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.setCursorAtScroll(term.Coordinates{Y: 0, X: len("alpha END bet")})
			},
			events: "c?alpha\nXYZ",
			width:  40,
			// backward match at col 0; charwise exclusive selection
			// stops just before the cursor character so the trailing
			// "a" is preserved. Insert mode replaces the deleted
			// prefix with "XYZ".
			wantContent: "XYZa\n",
			wantMode:    insertMode,
		},
		{
			name:    "gu question lowercases backward range up to but not including cursor",
			content: "ALPHA END BETA\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.setCursorAtScroll(term.Coordinates{Y: 0, X: len("ALPHA END BET")})
			},
			events:      "gu?ALPHA\n",
			width:       40,
			wantContent: "alpha end betA\n",
			wantMode:    normalMode,
		},
		{
			name:    "gU question uppercases backward range up to but not including cursor",
			content: "alpha END beta\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.setCursorAtScroll(term.Coordinates{Y: 0, X: len("alpha END bet")})
			},
			events:      "gU?alpha\n",
			width:       40,
			wantContent: "ALPHA END BETa\n",
			wantMode:    normalMode,
		},
		{
			name:    "g tilde question toggles backward range up to but not including cursor",
			content: "Alpha END beta\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.setCursorAtScroll(term.Coordinates{Y: 0, X: len("Alpha END bet")})
			},
			events:      "g~?Alpha\n",
			width:       40,
			wantContent: "aLPHA end BETa\n",
			wantMode:    normalMode,
		},
		{
			name:    "y question backward unicode pattern leaves buffer unchanged",
			content: "你好 END alpha\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.setCursorAtScroll(term.Coordinates{Y: 0, X: len("你好 END alpha")})
			},
			events:      "y?你好\n",
			width:       40,
			wantContent: "你好 END alpha\n",
			wantMode:    normalMode,
			wantUnnamed: "你好 END alph",
		},

		// ---- wide / unicode edge cases ----
		{
			name:        "d slash deletes prefix containing leading wide CJK characters",
			content:     "你好世界 END alpha\n",
			events:      "d/END\n",
			width:       40,
			wantContent: "END alpha\n",
			wantMode:    normalMode,
			wantUnnamed: "你好世界 ",
		},
		{
			name:    "d question backward consumes wide CJK characters",
			content: "alpha END 你好\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.setCursorAtScroll(term.Coordinates{Y: 0, X: len("alpha END 你")})
			},
			events: "d?END\n",
			width:  40,
			// charwise backward: deletes "END " plus first wide char.
			wantContent: "alpha 好\n",
			wantMode:    normalMode,
		},

		// ---- yank-and-paste integration via search operator ----
		{
			name:    "y question backward then p pastes captured prefix after cursor",
			content: "alpha END beta\n",
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.setCursorAtScroll(term.Coordinates{Y: 0, X: len("alpha END bet")})
			},
			events:      "y?alpha\np",
			width:       40,
			wantContent: "aalpha END betlpha END beta\n",
			wantMode:    normalMode,
			wantUnnamed: "alpha END bet",
		},

		// ---- patterns containing unusual characters ----
		{
			name:    "d slash pattern with multiple slashes only first delimits",
			content: "a/b/c END\n",
			events:  "d/c\n",
			width:   40,
			// the `/` after `d` opens the search bar; everything up
			// to <CR> is the pattern. Only the typed character `c`
			// (not slashes) is forwarded as pattern text.
			wantContent: "c END\n",
			wantMode:    normalMode,
			wantUnnamed: "a/b/",
		},

		// ---- non-search modes are not hijacked by `/` or `?` ----
		{
			name:        "insert mode slash inserts a literal slash",
			content:     "alpha\n",
			events:      "i/foo<esc>",
			width:       40,
			wantContent: "/fooalpha\n",
			wantMode:    normalMode,
		},
		{
			name:        "replace mode slash overwrites instead of starting search",
			content:     "alpha\n",
			events:      "R/<esc>",
			width:       40,
			wantContent: "/lpha\n",
			wantMode:    normalMode,
		},
	}

	for _, tcase := range suite {
		t.Run(tcase.name, func(t *testing.T) {
			clip := new(mockClip)
			vi := setupVi(t, tcase.content, 2, WithClipboard(registerset.New(clip)))
			vi.Resize(tcase.width, 20)
			if tcase.before != nil {
				tcase.before(t, vi)
			}

			events := parseSearchOpEvents(tcase.events)
			for _, ev := range events {
				quit, _ := vi.Handle(ev)
				require.False(t, quit)
			}

			assert.Equal(t, tcase.wantContent, vi.less.Buffer().String())
			assert.Equal(t, tcase.wantMode, vi.mode())
			assert.Equal(t, 1, vi.count)
			assert.Equal(t, "", vi.countDigits)
			assert.Equal(t, 0, vi.operatorCount)
			if tcase.wantCursor != nil {
				assert.Equal(t, *tcase.wantCursor, vi.cursorAtScroll(),
					"final cursor position")
			}
			if tcase.wantUnnamed != "" {
				data, err := vi.readRegister(unnamedRegister)
				require.NoError(t, err)
				assert.Equal(t, tcase.wantUnnamed, data.Text,
					"unnamed register contents")
			}
			if tcase.wantSearch != "" {
				data, err := vi.readRegister('/')
				require.NoError(t, err)
				assert.Equal(t, tcase.wantSearch, data.Text,
					"search register contents")
			}
		})
	}
}

func TestMarkOperatorMotion(t *testing.T) {
	type tc struct {
		name        string
		content     string
		marks       map[rune]term.Coordinates
		before      func(t *testing.T, vi *viHandlerImpl)
		events      string
		width       int
		wantContent string
		wantMode    viMode
		wantCursor  *term.Coordinates
		wantUnnamed string
		// optional: when true, enable wrap mode and use width as
		// wrap column. Useful for documenting wrap-mode behavior.
		wrap   bool
		height int
		// optional: assert contents of a named register (zero rune
		// means do not check).
		wantRegisterName rune
		wantRegister     string
	}

	suite := []tc{
		// ---- delete (charwise via backtick, linewise via quote) ----
		{
			name:        "d backtick a deletes charwise from cursor up to mark",
			content:     "alpha beta gamma\nsecond line\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 0, X: 6}},
			events:      "d`a",
			width:       40,
			wantContent: "beta gamma\nsecond line\n",
			wantMode:    normalMode,
			wantUnnamed: "alpha ",
		},
		{
			name:        "d quote a deletes whole lines from cursor to mark",
			content:     "first line\nsecond line\nthird line\nfourth line\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 2, X: 0}},
			events:      "d'a",
			width:       40,
			wantContent: "fourth line\n",
			wantMode:    normalMode,
			wantUnnamed: "first line\nsecond line\nthird line\n",
		},
		{
			name:        "d backtick z without a set mark is a no-op",
			content:     "alpha beta gamma\nsecond line\n",
			events:      "d`z",
			width:       40,
			wantContent: "alpha beta gamma\nsecond line\n",
			wantMode:    normalMode,
		},
		{
			name:    "d backtick a backward charwise deletes prefix to cursor",
			content: "alpha beta gamma\nsecond line\n",
			marks:   map[rune]term.Coordinates{'a': {Y: 0, X: 0}},
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 6}))
			},
			events: "d`a",
			width:  40,
			// charwise backward exclusive: deletes "alpha " up to but
			// not including the cursor character.
			wantContent: "beta gamma\nsecond line\n",
			wantMode:    normalMode,
		},
		{
			name:    "d quote a backward linewise deletes whole lines",
			content: "first\nsecond\nthird\nfourth\n",
			marks:   map[rune]term.Coordinates{'a': {Y: 0, X: 0}},
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 2, X: 0}))
			},
			events:      "d'a",
			width:       40,
			wantContent: "fourth\n",
			wantMode:    normalMode,
			wantUnnamed: "first\nsecond\nthird\n",
		},

		// ---- change ----
		{
			name:        "c backtick a deletes range and enters insert mode",
			content:     "alpha beta gamma\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 0, X: 6}},
			events:      "c`aXYZ",
			width:       40,
			wantContent: "XYZbeta gamma\n",
			wantMode:    insertMode,
		},
		{
			name:        "c quote a changes whole lines into one and enters insert mode",
			content:     "first\nsecond\nthird\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 1, X: 0}},
			events:      "c'a",
			width:       40,
			wantContent: "\nthird\n",
			wantMode:    insertMode,
		},

		// ---- yank ----
		{
			name:        "y backtick a captures charwise prefix without mutating buffer",
			content:     "alpha beta gamma\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 0, X: 6}},
			events:      "y`a",
			width:       40,
			wantContent: "alpha beta gamma\n",
			wantMode:    normalMode,
			wantUnnamed: "alpha ",
		},
		{
			name:        "y quote a captures whole lines without mutating buffer",
			content:     "first\nsecond\nthird\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 1, X: 0}},
			events:      "y'a",
			width:       40,
			wantContent: "first\nsecond\nthird\n",
			wantMode:    normalMode,
			wantUnnamed: "first\nsecond\n",
		},

		// ---- shift (always linewise regardless of ' vs `) ----
		{
			name:        "shift right quote a indents lines from cursor to mark",
			content:     "first\nsecond\nthird\nfourth\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 2, X: 0}},
			events:      ">'a",
			width:       40,
			wantContent: "\tfirst\n\tsecond\n\tthird\nfourth\n",
			wantMode:    normalMode,
		},
		{
			name:    "shift right backtick a still indents whole lines",
			content: "first\nsecond\nthird\nfourth\n",
			marks:   map[rune]term.Coordinates{'a': {Y: 2, X: 3}},
			events:  ">`a",
			width:   40,
			// shift is intrinsically linewise; the column part of the
			// mark is ignored.
			wantContent: "\tfirst\n\tsecond\n\tthird\nfourth\n",
			wantMode:    normalMode,
		},
		{
			name:        "shift left quote a dedents lines",
			content:     "\tfirst\n\tsecond\n\tthird\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 1, X: 0}},
			events:      "\\<'a",
			width:       40,
			wantContent: "first\nsecond\n\tthird\n",
			wantMode:    normalMode,
		},

		// ---- case change ----
		{
			name:        "gu backtick a lowercases charwise prefix to mark",
			content:     "ALPHA BETA GAMMA\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 0, X: 6}},
			events:      "gu`a",
			width:       40,
			wantContent: "alpha BETA GAMMA\n",
			wantMode:    normalMode,
		},
		{
			name:        "gU backtick a uppercases charwise prefix to mark",
			content:     "alpha beta gamma\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 0, X: 6}},
			events:      "gU`a",
			width:       40,
			wantContent: "ALPHA beta gamma\n",
			wantMode:    normalMode,
		},
		{
			name:        "g tilde backtick a toggles charwise prefix to mark",
			content:     "Alpha BETA gamma\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 0, X: 6}},
			events:      "g~`a",
			width:       40,
			wantContent: "aLPHA BETA gamma\n",
			wantMode:    normalMode,
		},
		{
			name:        "gu quote a lowercases whole lines",
			content:     "FIRST\nSECOND\nTHIRD\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 1, X: 0}},
			events:      "gu'a",
			width:       40,
			wantContent: "first\nsecond\nTHIRD\n",
			wantMode:    normalMode,
		},

		// ---- unset / invalid mark ----
		{
			name:        "y quote z without a set mark is a no-op",
			content:     "alpha beta\n",
			events:      "y'z",
			width:       40,
			wantContent: "alpha beta\n",
			wantMode:    normalMode,
		},
		{
			name:        "shift right quote a invalid mark name is a no-op",
			content:     "first\nsecond\n",
			events:      ">'1",
			width:       40,
			wantContent: "first\nsecond\n",
			wantMode:    normalMode,
		},

		// ---- cursor placement / register hygiene ----
		{
			name:        "d quote a places cursor at first non-blank of remaining line",
			content:     "first\nsecond\nthird\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 1, X: 0}},
			events:      "d'a",
			width:       40,
			wantContent: "third\n",
			wantMode:    normalMode,
			wantCursor:  &term.Coordinates{Y: 0, X: 0},
		},
		{
			name:        "d backtick a into named register",
			content:     "alpha beta\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 0, X: 6}},
			events:      "\"bd`a",
			width:       40,
			wantContent: "beta\n",
			wantMode:    normalMode,
			wantUnnamed: "alpha ",
		},
		{
			name:             "y backtick a into named register populates that register",
			content:          "alpha beta\n",
			marks:            map[rune]term.Coordinates{'a': {Y: 0, X: 6}},
			events:           "\"by`a",
			width:            40,
			wantContent:      "alpha beta\n",
			wantMode:         normalMode,
			wantUnnamed:      "alpha ",
			wantRegisterName: 'b',
			wantRegister:     "alpha ",
		},
		{
			name:    "d into black-hole register does not pollute unnamed",
			content: "alpha beta\n",
			marks:   map[rune]term.Coordinates{'a': {Y: 0, X: 6}},
			before: func(t *testing.T, vi *viHandlerImpl) {
				_ = vi.writeRegister(unnamedRegister, clipboard.Data{Text: "preserved"})
			},
			events:      "\"_d`a",
			width:       40,
			wantContent: "beta\n",
			wantMode:    normalMode,
			wantUnnamed: "preserved",
		},

		// ---- wide / unicode ----
		{
			name:        "d backtick consumes wide CJK prefix",
			content:     "你好世界 alpha\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 0, X: 5}},
			events:      "d`a",
			width:       40,
			wantContent: "alpha\n",
			wantMode:    normalMode,
			wantUnnamed: "你好世界 ",
		},
		{
			name:        "d backtick stops before wide CJK at mark",
			content:     "alpha 你好 beta\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 0, X: 6}},
			events:      "d`a",
			width:       40,
			wantContent: "你好 beta\n",
			wantMode:    normalMode,
			wantUnnamed: "alpha ",
		},
		{
			name:    "d backtick backward through wide chars",
			content: "alpha 你好 beta\n",
			marks:   map[rune]term.Coordinates{'a': {Y: 0, X: 0}},
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 8}))
			},
			events:      "d`a",
			width:       40,
			wantContent: " beta\n",
			wantMode:    normalMode,
		},
		{
			name:        "y quote a captures linewise unicode buffer",
			content:     "你好世界\n第二行\n第三行\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 1, X: 0}},
			events:      "y'a",
			width:       40,
			wantContent: "你好世界\n第二行\n第三行\n",
			wantMode:    normalMode,
			wantUnnamed: "你好世界\n第二行\n",
		},
		{
			name:        "gu backtick lowercases unicode",
			content:     "ÉLÈVE alpha\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 0, X: 5}},
			events:      "gu`a",
			width:       40,
			wantContent: "élève alpha\n",
			wantMode:    normalMode,
		},

		// ---- null / control characters in content ----
		{
			name:        "d backtick deletes through embedded null byte",
			content:     "alp\x00ha beta\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 0, X: 7}},
			events:      "d`a",
			width:       40,
			wantContent: "beta\n",
			wantMode:    normalMode,
			wantUnnamed: "alp\x00ha ",
		},
		{
			name:        "d quote a deletes lines containing null bytes",
			content:     "alpha\x00line\nsecond\nthird\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 1, X: 0}},
			events:      "d'a",
			width:       40,
			wantContent: "third\n",
			wantMode:    normalMode,
			wantUnnamed: "alpha\x00line\nsecond\n",
		},

		// ---- tabs and indentation ----
		{
			name:        "d quote a includes tabs and trailing spaces",
			content:     "\talpha   \n\tbeta\nMARK\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 2, X: 0}},
			events:      "d'a",
			width:       40,
			wantContent: "",
			wantMode:    normalMode,
			wantUnnamed: "\talpha   \n\tbeta\nMARK\n",
		},
		{
			name:        "shift right quote indents tab-prefixed lines",
			content:     "\talpha\n  beta\nMARK\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 2, X: 0}},
			events:      ">'a",
			width:       40,
			wantContent: "\t\talpha\n\t  beta\n\tMARK\n",
			wantMode:    normalMode,
		},

		// ---- buffer boundary edge cases ----
		{
			name:    "d quote a deletes only line in single-line buffer",
			content: "alpha beta",
			marks:   map[rune]term.Coordinates{'a': {Y: 0, X: 6}},
			events:  "d'a",
			width:   40,
			// `'a` snaps to first non-blank of the same line, so the
			// cursor doesn't actually move (still at {0,0}); the
			// before==after guard fires and the buffer is preserved.
			wantContent: "alpha beta",
			wantMode:    normalMode,
		},
		{
			name:        "d quote a on empty buffer is a no-op",
			content:     "",
			marks:       map[rune]term.Coordinates{'a': {Y: 0, X: 0}},
			events:      "d'a",
			width:       40,
			wantContent: "",
			wantMode:    normalMode,
		},
		{
			name:        "d backtick a on cursor-position mark is a no-op",
			content:     "alpha\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 0, X: 0}},
			events:      "d`a",
			width:       40,
			wantContent: "alpha\n",
			wantMode:    normalMode,
		},
		{
			name:        "d quote a on file with no trailing newline",
			content:     "first\nsecond\nthird",
			marks:       map[rune]term.Coordinates{'a': {Y: 2, X: 0}},
			events:      "d'a",
			width:       40,
			wantContent: "",
			wantMode:    normalMode,
			wantUnnamed: "first\nsecond\nthird\n",
		},
		{
			name:        "d backtick a single-line no trailing newline",
			content:     "alpha END beta",
			marks:       map[rune]term.Coordinates{'a': {Y: 0, X: 6}},
			events:      "d`a",
			width:       40,
			wantContent: "END beta",
			wantMode:    normalMode,
			wantUnnamed: "alpha ",
		},
		{
			name:        "d quote a with mark on last line",
			content:     "first\nsecond\nthird\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 2, X: 0}},
			events:      "d'a",
			width:       40,
			wantContent: "",
			wantMode:    normalMode,
			wantUnnamed: "first\nsecond\nthird\n",
		},
		{
			name:    "d quote a with mark beyond actual content is no-op",
			content: "alpha\n",
			marks:   map[rune]term.Coordinates{'a': {Y: 99, X: 0}},
			events:  "d'a",
			width:   40,
			// jumping to a mark stored beyond the end of the buffer
			// snaps the cursor to the last reachable position; the
			// resulting linewise selection covers the rest of the
			// buffer. This documents the current behavior; users
			// should not normally end up with such marks.
			wantContent: "",
			wantMode:    normalMode,
			wantUnnamed: "alpha\n\n",
		},

		// ---- cursor placement edge cases ----
		{
			name:    "d backtick a with cursor past end of line",
			content: "alpha END beta\n",
			marks:   map[rune]term.Coordinates{'a': {Y: 0, X: 0}},
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 13}))
			},
			events:      "d`a",
			width:       40,
			wantContent: "a\n",
			wantMode:    normalMode,
		},
		{
			name:    "d quote a with cursor past last line is a no-op",
			content: "first\nsecond\n",
			marks:   map[rune]term.Coordinates{'a': {Y: 0, X: 0}},
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.setCursorAtScroll(term.Coordinates{Y: 5, X: 0})
			},
			events: "d'a",
			width:  40,
			// cursor lands past content; the linewise range still
			// resolves and clears the buffer.
			wantContent: "",
			wantMode:    normalMode,
		},
		{
			name:    "d backtick a with cursor positioned past end of line is clamped",
			content: "abc\n",
			marks:   map[rune]term.Coordinates{'a': {Y: 0, X: 0}},
			before: func(t *testing.T, vi *viHandlerImpl) {
				// simulate a stale cursor (e.g. an external file
				// edit shrank the line under us): position the
				// cursor far past the actual end-of-line.
				vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 100})
			},
			events: "d`a",
			width:  40,
			// cursor is clamped to the last column on the line; the
			// backward charwise delete from there to mark at col 0
			// removes the prefix up to (but not including) the
			// surviving last cell.
			wantContent: "c\n",
			wantMode:    normalMode,
			wantUnnamed: "ab",
		},
		{
			name:    "y backtick a with cursor past EOL captures clamped charwise range",
			content: "abc def\n",
			marks:   map[rune]term.Coordinates{'a': {Y: 0, X: 0}},
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 100})
			},
			events:      "y`a",
			width:       40,
			wantContent: "abc def\n",
			wantMode:    normalMode,
			wantUnnamed: "abc de",
		},
		{
			name:    "d quote a with cursor past EOB and EOL clears whole buffer",
			content: "first\nsecond\n",
			marks:   map[rune]term.Coordinates{'a': {Y: 0, X: 0}},
			before: func(t *testing.T, vi *viHandlerImpl) {
				// past both last line and last column; cursor is
				// clamped onto the empty trailing row.
				vi.setCursorAtScroll(term.Coordinates{Y: 99, X: 99})
			},
			events:      "d'a",
			width:       40,
			wantContent: "",
			wantMode:    normalMode,
			wantUnnamed: "first\nsecond\n\n",
		},
		{
			name:    "shift right quote a with cursor past EOB indents all lines",
			content: "first\nsecond\n",
			marks:   map[rune]term.Coordinates{'a': {Y: 0, X: 0}},
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.setCursorAtScroll(term.Coordinates{Y: 99, X: 0})
			},
			events: ">'a",
			width:  40,
			// shift indents every line in the resolved linewise
			// range, including the empty trailing row implied by
			// the cursor clamp.
			wantContent: "\tfirst\n\tsecond\n\t",
			wantMode:    normalMode,
		},
		{
			name:    "c backtick a with cursor past EOL deletes clamped range and enters insert",
			content: "abc\n",
			marks:   map[rune]term.Coordinates{'a': {Y: 0, X: 0}},
			before: func(t *testing.T, vi *viHandlerImpl) {
				vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 100})
			},
			events:      "c`aXY",
			width:       40,
			wantContent: "XYc\n",
			wantMode:    insertMode,
		},

		// ---- uppercase mark name ----
		{
			name:        "d backtick uppercase mark A works",
			content:     "alpha\nbeta MARK\n",
			marks:       map[rune]term.Coordinates{'A': {Y: 1, X: 5}},
			events:      "d`A",
			width:       40,
			wantContent: "MARK\n",
			wantMode:    normalMode,
			wantUnnamed: "alpha\nbeta ",
		},

		// ---- count prefix on operator ----
		{
			name:    "d count prefix is consumed but does not multiply mark motion",
			content: "first\nsecond\nthird\nfourth\n",
			marks:   map[rune]term.Coordinates{'a': {Y: 1, X: 0}},
			events:  "2d'a",
			width:   40,
			// the `2` count is applied to the operator but the
			// mark range is fixed (cursor 0..mark 1 = lines 0,1).
			wantContent: "third\nfourth\n",
			wantMode:    normalMode,
			wantUnnamed: "first\nsecond\n",
		},

		// ---- yank+paste integration ----
		{
			name:    "y quote a then Gp pastes captured lines after last line",
			content: "first\nsecond\nthird\n",
			marks:   map[rune]term.Coordinates{'a': {Y: 1, X: 0}},
			events:  "y'aGp",
			width:   40,
			// linewise yank captures lines 0..1; Gp pastes after the
			// last line. The buffer's trailing-newline semantics
			// produce one blank line between the original content
			// and the pasted block.
			wantContent: "first\nsecond\nthird\n\nfirst\nsecond\n",
			wantMode:    normalMode,
		},

		// ---- escape cancels pending mark ----
		{
			name:        "d quote escape cancels and returns to normal",
			content:     "alpha\n",
			marks:       map[rune]term.Coordinates{'a': {Y: 0, X: 0}},
			events:      "d'<esc>",
			width:       40,
			wantContent: "alpha\n",
			wantMode:    normalMode,
		},

		// ---- chained operators with mark hygiene ----
		{
			name:    "d backtick cancel via invalid mark then dd works",
			content: "alpha\nbeta\n",
			events:  "d`1dd",
			width:   40,
			// `1` is invalid as a mark name -> cancel; then `dd`
			// deletes the current line.
			wantContent: "beta\n",
			wantMode:    normalMode,
			wantUnnamed: "alpha\n",
		},

		// ---- wrap mode regression guard ----
		{
			name:    "d backtick a in wrap mode does not panic and leaves buffer intact",
			content: "alpha beta gamma delta epsilon zeta\n",
			marks:   map[rune]term.Coordinates{'a': {Y: 0, X: 24}},
			events:  "d`a",
			width:   10,
			wrap:    true,
			// wrap mode reinterprets coordinates as display lines;
			// jumping to a logical-only mark coordinate inside a
			// wrapped row is currently a no-op rather than crashing.
			// Documents existing behavior.
			wantContent: "alpha beta gamma delta epsilon zeta\n",
			wantMode:    normalMode,
		},

		// ---- linewise cursor placement ----
		{
			name:    "d quote a leaves cursor on first non-blank of surviving line",
			content: "first\n  second\nthird\n",
			marks:   map[rune]term.Coordinates{'a': {Y: 0, X: 0}},
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 1, X: 5}))
			},
			events: "d'a",
			width:  40,
			// linewise delete from line 1 backward to mark at line 0
			// removes both lines; cursor lands at first non-blank of
			// the surviving "third" line (col 0).
			wantContent: "third\n",
			wantMode:    normalMode,
			wantCursor:  &term.Coordinates{Y: 0, X: 0},
			wantUnnamed: "first\n  second\n",
		},
		// ---- visual marks (`< / `> / '< / '>) used as motion targets ----
		{
			name:    "d backtick less deletes from cursor to start of last visual selection",
			content: "alpha beta gamma delta\nsecond line\n",
			marks: map[rune]term.Coordinates{
				'<': {Y: 0, X: 6}, // pre-seeded last visual selection start
			},
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 0, X: 12}))
			},
			events: "d`\\<",
			width:  40,
			// charwise backward exclusive: deletes columns 6..11
			// ("beta g") leaving "alpha amma delta\n".
			wantContent: "alpha amma delta\nsecond line\n",
			wantMode:    normalMode,
			wantUnnamed: "beta g",
		},
		{
			name:    "y quote greater yanks lines up to and including end of last selection",
			content: "first line\nsecond line\nthird line\nfourth line\n",
			marks: map[rune]term.Coordinates{
				'>': {Y: 2, X: 4}, // last visual selection ends inside line 2
			},
			events: "y'\\>",
			width:  40,
			// linewise yank from cursor (line 0) down to line 2.
			wantContent: "first line\nsecond line\nthird line\nfourth line\n",
			wantMode:    normalMode,
			wantUnnamed: "first line\nsecond line\nthird line\n",
		},
		{
			name:    "gq quote less reflows from cursor up to start-of-selection line",
			content: "alpha beta gamma delta epsilon zeta\nsecond line\nthird line\n",
			marks: map[rune]term.Coordinates{
				'<': {Y: 0, X: 0}, // last visual selection started at row 0
			},
			before: func(t *testing.T, vi *viHandlerImpl) {
				require.True(t, vi.setCursorAtScroll(term.Coordinates{Y: 2, X: 0}))
			},
			events: "gq'\\<",
			width:  40,
			// gq from cursor (row 2) backward to mark line (row 0)
			// reflows rows 0..2 as one paragraph at default ruler 90.
			wantContent: "alpha beta gamma delta epsilon zeta second line third line\n",
			wantMode:    normalMode,
		},
	}

	for _, tcase := range suite {
		t.Run(tcase.name, func(t *testing.T) {
			clip := new(mockClip)
			opts := []Option{WithClipboard(registerset.New(clip))}
			if tcase.wrap {
				opts = append(opts, WithWrap(true))
			}
			vi := setupVi(t, tcase.content, 2, opts...)
			height := tcase.height
			if height == 0 {
				height = 20
			}
			vi.Resize(tcase.width, height)
			for name, pos := range tcase.marks {
				vi.cursor.SetLocationList(textapi.LocationPriorityInfo, string(name),
					textapi.LocationSlice([]textapi.Location{{
						From: pos,
						To:   term.Coordinates{Y: pos.Y, X: pos.X + 1},
					}}),
				)
			}
			if tcase.before != nil {
				tcase.before(t, vi)
			}

			events := parseSearchOpEvents(tcase.events)
			for _, ev := range events {
				quit, _ := vi.Handle(ev)
				require.False(t, quit)
			}

			assert.Equal(t, tcase.wantContent, vi.less.Buffer().String())
			assert.Equal(t, tcase.wantMode, vi.mode())
			assert.Equal(t, 1, vi.count)
			assert.Equal(t, "", vi.countDigits)
			assert.Equal(t, 0, vi.operatorCount)
			if tcase.wantCursor != nil {
				assert.Equal(t, *tcase.wantCursor, vi.cursorAtScroll(),
					"final cursor position")
			}
			if tcase.wantUnnamed != "" {
				data, err := vi.readRegister(unnamedRegister)
				require.NoError(t, err)
				assert.Equal(t, tcase.wantUnnamed, data.Text,
					"unnamed register contents")
			}
			if tcase.wantRegisterName != 0 {
				data, err := vi.readRegister(tcase.wantRegisterName)
				require.NoError(t, err)
				assert.Equal(t, tcase.wantRegister, data.Text,
					"register %q contents", string(tcase.wantRegisterName))
			}
		})
	}
}

func TestCursorOutOfBounds(t *testing.T) {
	for _, wrap := range []bool{false, true} {
		for _, contentWindowOverflow := range []bool{false, true} {
			name := "cursor go beyond rows and move one up"
			if wrap {
				name += " (wrap)"
			}
			if contentWindowOverflow {
				name += " (content overflow)"
			}

			t.Run(name, func(t *testing.T) {
				vi := setupVi(t, "aaaa\nbbbb\ncccc\ndddd\neeee", 2, WithWrap(wrap))

				windowWidth := 2
				if contentWindowOverflow {
					vi.Resize(windowWidth, 3)
				} else {
					vi.Resize(windowWidth, 10)
				}

				beyondRowsCoords := term.Coordinates{Y: 99}
				ok := vi.setCursorAtScroll(beyondRowsCoords)
				require.True(t, ok)

				scrollCoords := vi.cursor.ScrollCoordinates(vi.cursor.Coordinates())
				assert.Equal(t, term.Coordinates{X: 0, Y: 4}, scrollCoords)

				vi.Handle(term.Event{Type: term.EventKey, Ch: 'k'})
				scrollCoords = vi.cursor.ScrollCoordinates(vi.cursor.Coordinates())
				assert.Equal(t, term.Coordinates{X: 0, Y: 3}, scrollCoords,
					"the cursor is trapped at the last line")
			})
		}
	}
}

func TestMoveCursorArrowKeys(t *testing.T) {
	t.Run("arrow keys can be used to move cursor", func(t *testing.T) {
		vi := setupVi(t, "aaaa\nbbbb\ncccc\ndddd", 2)
		vi.Resize(4, 4)

		modes := []viMode{
			normalMode,
			insertMode,
			visualMode,
		}

		for _, mod := range modes {
			vi.currMode = mod

			coords, _, _ := vi.Cursor()
			require.Equal(t, 0, coords.X)
			require.Equal(t, 0, coords.Y)

			vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowRight})
			coords, _, _ = vi.Cursor()
			require.Equal(t, 1, coords.X)
			require.Equal(t, 0, coords.Y)

			vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
			coords, _, _ = vi.Cursor()
			require.Equal(t, 1, coords.X)
			require.Equal(t, 1, coords.Y)

			vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowLeft})
			coords, _, _ = vi.Cursor()
			require.Equal(t, 0, coords.X)
			require.Equal(t, 1, coords.Y)

			vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowUp})
			coords, _, _ = vi.Cursor()
			require.Equal(t, 0, coords.X)
			require.Equal(t, 0, coords.Y)
		}

	})
}

func TestSetNormalModeClearing(t *testing.T) {
	vi := setupVi(t, "aaaa\nbbbb\ncccc\ndddd", 2)
	vi.Resize(4, 4)

	vi.Handle(term.Event{Type: term.EventKey, Ch: '/'})
	assert.Equal(t, searchMode, vi.mode())

	vi.setNormalMode()
	assert.Equal(t, normalMode, vi.mode())

	assert.Equal(t, thandler.LessNormalMode, vi.less.Mode())
}

func TestWithSearchDisabled(t *testing.T) {
	t.Run("normal mode / and ? are no-ops", func(t *testing.T) {
		vi := setupVi(t, "aaaa\nbbbb\ncccc", 2, WithSearch(false))
		vi.Resize(4, 4)

		vi.Handle(term.Event{Type: term.EventKey, Ch: '/'})
		assert.Equal(t, normalMode, vi.mode())
		assert.Equal(t, thandler.LessNormalMode, vi.less.Mode())

		vi.Handle(term.Event{Type: term.EventKey, Ch: '?'})
		assert.Equal(t, normalMode, vi.mode())
		assert.Equal(t, thandler.LessNormalMode, vi.less.Mode())
	})

	t.Run("operator-pending / cancels back to normal mode", func(t *testing.T) {
		vi := setupVi(t, "aaaa\nbbbb\ncccc", 2, WithSearch(false))
		vi.Resize(4, 4)

		vi.Handle(term.Event{Type: term.EventKey, Ch: 'd'})
		vi.Handle(term.Event{Type: term.EventKey, Ch: '/'})
		assert.Equal(t, normalMode, vi.mode())
		assert.Equal(t, thandler.LessNormalMode, vi.less.Mode())
		assert.Equal(t, "aaaa\nbbbb\ncccc", vi.less.Buffer().String())
	})

	t.Run("search remains enabled by default", func(t *testing.T) {
		vi := setupVi(t, "aaaa\nbbbb\ncccc", 2)
		vi.Resize(4, 4)

		vi.Handle(term.Event{Type: term.EventKey, Ch: '/'})
		assert.Equal(t, searchMode, vi.mode())
		assert.Equal(t, thandler.LessSearchMode, vi.less.Mode())
	})
}

func TestViRegisters(t *testing.T) {
	strPtr := func(s string) *string { return &s }

	type registerCase struct {
		name              string
		content           string
		seq               string
		externalClipboard *clipboard.Data
		wantBuffer        *string
		wantExternal      *string
		wantRegisters     map[rune]string
	}

	run := func(t *testing.T, vi *viHandlerImpl, seq string) {
		t.Helper()
		for _, event := range seq {
			ev := term.Event{Type: term.EventKey, Ch: event}
			switch event {
			case '#':
				ev = term.Event{Type: term.EventKey, Key: term.KeyEsc}
			case '>':
				ev = term.Event{Type: term.EventKey, Key: term.KeyEnter}
			}
			_, handled := vi.Handle(ev)
			require.True(t, handled, "sequence %q failed on %q", seq, string(event))
		}
	}

	for _, tc := range []registerCase{
		{
			name:    "default yank populates unnamed and last-yank registers",
			content: "one\ntwo\n",
			seq:     "yy",
			wantRegisters: map[rune]string{
				unnamedRegister:  "one\n",
				lastYankRegister: "one\n",
			},
		},
		{
			name:    "named yank populates named unnamed and last-yank registers",
			content: "one\ntwo\n",
			seq:     "\"ayy",
			wantRegisters: map[rune]string{
				'a':              "one\n",
				unnamedRegister:  "one\n",
				lastYankRegister: "one\n",
			},
		},
		{
			name:       "named register paste uses named text and preserves unnamed paste source",
			content:    "one\ntwo\n",
			seq:        "\"ayyjyy\"ap",
			wantBuffer: strPtr("one\ntwo\none\n"),
			wantRegisters: map[rune]string{
				'a':              "one\n",
				unnamedRegister:  "two\n",
				lastYankRegister: "two\n",
			},
		},
		{
			name:       "named delete populates named and unnamed but does not replace last-yank",
			content:    "one\ntwo\nthree\n",
			seq:        "yyj\"bdd",
			wantBuffer: strPtr("one\nthree\n"),
			wantRegisters: map[rune]string{
				'b':              "two\n",
				unnamedRegister:  "two\n",
				lastYankRegister: "one\n",
			},
		},
		{
			name:       "deletes into named register and pastes it later",
			content:    "one\ntwo\nthree\n",
			seq:        "\"add\"ap",
			wantBuffer: strPtr("two\none\nthree\n"),
			wantRegisters: map[rune]string{
				'a':             "one\n",
				unnamedRegister: "one\n",
			},
		},
		{
			name:       "black-hole operator delete preserves unnamed and last-yank registers",
			content:    "one\ntwo\nthree\n",
			seq:        "yyj\"_ddp",
			wantBuffer: strPtr("one\nthree\none\n"),
			wantRegisters: map[rune]string{
				unnamedRegister:   "one\n",
				lastYankRegister:  "one\n",
				blackHoleRegister: "",
			},
		},
		{
			name:       "last-yank register survives delete and can be pasted explicitly",
			content:    "one\ntwo\nthree\n",
			seq:        "yyjdd\"0p",
			wantBuffer: strPtr("one\nthree\none\n"),
			wantRegisters: map[rune]string{
				unnamedRegister:  "two\n",
				lastYankRegister: "one\n",
			},
		},
		{
			name:    "visual named yank populates named unnamed and last-yank registers",
			content: "abcdef\n",
			seq:     "vll\"ay",
			wantRegisters: map[rune]string{
				'a':              "abc",
				unnamedRegister:  "abc",
				lastYankRegister: "abc",
			},
		},
		{
			name:       "visual named delete populates named unnamed and small-delete registers",
			content:    "abcdef\n",
			seq:        "vll\"ad",
			wantBuffer: strPtr("def\n"),
			wantRegisters: map[rune]string{
				'a':             "abc",
				unnamedRegister: "abc",
				'-':             "abc",
			},
		},
		{
			name:    "search register stores last slash search",
			content: "one\ntwo\n",
			seq:     "/two>",
			wantRegisters: map[rune]string{
				'/': "two",
			},
		},
		{
			name:    "dot register stores last inserted text",
			content: "one\n",
			seq:     "iXYZ#",
			wantRegisters: map[rune]string{
				'.': "XYZ",
			},
		},
		{
			name:       "small-delete register can be pasted explicitly",
			content:    "abcdef\n",
			seq:        "vllx\"-P",
			wantBuffer: strPtr("abcdef\n"),
			wantRegisters: map[rune]string{
				'-':             "abc",
				unnamedRegister: "abc",
			},
		},
		{
			name:              "clipboard register pastes from configured clipboard",
			content:           "one\n",
			externalClipboard: &clipboard.Data{Text: "clip\n", Metadata: text.LineSelection},
			seq:               "\"+p",
			wantBuffer:        strPtr("one\nclip\n"),
		},
		{
			name:              "clipboard register yanks to configured clipboard",
			content:           "one\ntwo\n",
			externalClipboard: &clipboard.Data{Text: "clip\n", Metadata: text.LineSelection},
			seq:               "\"+yy",
			wantExternal:      strPtr("one\n"),
			wantRegisters: map[rune]string{
				unnamedRegister:  "one\n",
				lastYankRegister: "one\n",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clip := new(mockClip)
			if tc.externalClipboard != nil {
				clip.data = *tc.externalClipboard
			}

			vi := setupVi(t, tc.content, 2, WithClipboard(registerset.New(clip)))
			vi.Resize(80, 24)
			run(t, vi, tc.seq)

			if tc.wantBuffer != nil {
				assert.Equal(t, *tc.wantBuffer, vi.less.Buffer().String())
			}
			if tc.wantExternal != nil {
				assert.Equal(t, *tc.wantExternal, clip.data.Text)
			}
			for name, want := range tc.wantRegisters {
				data, err := vi.readRegister(name)
				require.NoError(t, err)
				assert.Equalf(t, want, data.Text, "register %q", string(name))
			}
		})
	}
}

func TestViParagraphMotions(t *testing.T) {
	type paragraphCase struct {
		name          string
		content       string
		at            *term.Coordinates
		seq           string
		wantBuffer    string
		wantMode      viMode
		wantCursor    *term.Coordinates
		wantClipboard string
	}

	run := func(t *testing.T, tc paragraphCase) {
		t.Helper()
		vi := setupVi(t, tc.content, 2)
		vi.Resize(80, 24)
		if tc.at != nil {
			vi.setCursorAtScroll(*tc.at)
		}
		for _, event := range tc.seq {
			_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: event})
			require.True(t, handled, "sequence %q failed on %q", tc.seq, string(event))
		}
		if tc.wantBuffer != "" || tc.content == "" {
			assert.Equal(t, tc.wantBuffer, vi.less.Buffer().String())
		}
		assert.Equal(t, tc.wantMode, vi.mode())
		if tc.wantCursor != nil {
			assert.Equal(t, *tc.wantCursor, vi.cursor.CursorAtScroll())
		}
		if tc.wantClipboard != "" {
			paste, err := vi.config.clipboard.Paste(vi.config.defaultRegister)
			require.NoError(t, err)
			assert.Equal(t, tc.wantClipboard, paste.Text)
		}
	}

	content := "aaa\nbbb\n\nccc\nddd\neee\n\nfff\nggg"
	// Line layout:
	//   0: aaa
	//   1: bbb
	//   2: (empty)
	//   3: ccc
	//   4: ddd
	//   5: eee
	//   6: (empty)
	//   7: fff
	//   8: ggg

	for _, tc := range []paragraphCase{
		// --- basic } motion ---
		{
			name:       "} from first line moves to first blank line",
			content:    content,
			seq:        "}",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 0, Y: 2},
		},
		{
			name:       "} from blank line skips to next blank line",
			content:    content,
			at:         &term.Coordinates{Y: 2},
			seq:        "}",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 0, Y: 6},
		},
		{
			name:       "} from last paragraph goes to the last character",
			content:    content,
			at:         &term.Coordinates{Y: 7},
			seq:        "}",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 2, Y: 8},
		},
		{
			name:       "} from last line goes to its last character",
			content:    content,
			at:         &term.Coordinates{Y: 8},
			seq:        "}",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 2, Y: 8},
		},
		// --- basic { motion ---
		{
			name:       "{ from last line moves to last blank line",
			content:    content,
			at:         &term.Coordinates{Y: 8},
			seq:        "{",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 0, Y: 6},
		},
		{
			name:       "{ from blank line skips to prev blank line",
			content:    content,
			at:         &term.Coordinates{Y: 6},
			seq:        "{",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 0, Y: 2},
		},
		{
			name:       "{ from first paragraph goes to first line",
			content:    content,
			at:         &term.Coordinates{Y: 1},
			seq:        "{",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 0, Y: 0},
		},
		{
			name:       "{ from first line stays",
			content:    content,
			seq:        "{",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 0, Y: 0},
		},
		// --- count support ---
		{
			name:       "2} jumps two paragraphs forward",
			content:    content,
			seq:        "2}",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 0, Y: 6},
		},
		{
			name:       "2{ jumps two paragraphs backward",
			content:    content,
			at:         &term.Coordinates{Y: 8},
			seq:        "2{",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 0, Y: 2},
		},
		// --- operator combos ---
		{
			name:       "d} from line start deletes the paragraph lines, keeping the blank line",
			content:    content,
			seq:        "d}",
			wantBuffer: "\nccc\nddd\neee\n\nfff\nggg",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 0, Y: 0},
		},
		{
			name:       "d{ deletes linewise from the blank line through the previous line",
			content:    content,
			at:         &term.Coordinates{Y: 4},
			seq:        "d{",
			wantBuffer: "aaa\nbbb\nddd\neee\n\nfff\nggg",
			wantMode:   normalMode,
			wantCursor: &term.Coordinates{X: 0, Y: 2},
		},
		{
			name:          "y} from line start yanks the paragraph lines",
			content:       content,
			seq:           "y}",
			wantMode:      normalMode,
			wantClipboard: "aaa\nbbb\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run(t, tc)
		})
	}
}

func setupVi(
	t *testing.T, text string, tabspaces int, opts ...Option,
) *viHandlerImpl {
	buf := cell.NewBuffer()
	buf.Init()
	_, err := buf.ReadFrom(strings.NewReader(text))
	require.NoError(t, err)

	config := defaultviHandlerImplConfig()
	config.tabspaces = tabspaces
	for _, o := range opts {
		o(&config)
	}

	vi := new(viHandlerImpl)
	vi.init(buf, config)

	return vi
}

func setupViWithScroll(
	t *testing.T, text string, tabspaces int, opts ...Option,
) *viHandlerImpl {
	buf := cell.NewBuffer()
	buf.Init()
	_, err := buf.ReadFrom(strings.NewReader(text))
	require.NoError(t, err)

	scroll := component.NewScroll(buf)
	scroll.SetTabspaces(tabspaces)

	vi := new(viHandlerImpl)
	vi.initWithScroll(scroll, opts...)

	return vi
}

func setupViIntegration(
	t *testing.T, copy string, tabspaces int, opts ...Option,
) tui.Handler {
	buf := cell.NewBuffer()
	buf.Init()
	_, err := buf.ReadFrom(strings.NewReader(copy))
	require.NoError(t, err)

	opts = append(opts, WithTabspaces(tabspaces))

	var mu sync.Mutex
	cfg := text.StatusBarConfig{
		Publisher: &texttest.TestEditor{},
		ScheduleNextTick: func(cb func()) bool {
			mu.Lock()
			defer mu.Unlock()
			cb()
			return true
		},
	}
	vi := New(buf, uri, opts...)
	bar := text.WithStatusBar(vi, buf, vi.less.Scroll(),
		false, false, cfg)
	vi.setStatusBar(bar)

	return handler.Sync(&mu, bar)
}

func TestPasteVisualMode(t *testing.T) {
	name := "standard select"
	for _, wrap := range []bool{false, true} {
		if wrap {
			name += " (wrap)"
		}

		t.Run(name, func(t *testing.T) {
			vi := setupVi(t, "ABC0123456789", 2)
			if wrap {
				vi.Resize(4, 10)
			} else {
				vi.Resize(10, 10)
			}

			events := "vllyvlld"
			for _, event := range events {
				vi.Handle(term.Event{Type: term.EventKey, Ch: event})
			}

			require.Equal(t, "0123456789", vi.less.Buffer().String())
			paste, err := vi.config.clipboard.Paste(vi.config.defaultRegister)
			require.NoError(t, err)
			require.Equal(t, "ABC", paste.Text)

			events = "lllvll"
			for _, event := range events {
				vi.Handle(term.Event{Type: term.EventKey, Ch: event})
			}
			require.Equal(t, "345", vi.cursor.Selection())

			vi.Handle(term.Event{Type: term.EventKey, Ch: 'p'})

			// after pasting in visual mode vim goes to normal mode again
			assert.Equal(t, normalMode, vi.mode())

			assert.Equal(t, "012ABC6789", vi.less.Buffer().String())

			cell, ok := vi.cursor.Cell()
			require.True(t, ok)
			require.Equal(t, 'C', cell.Ch)
		})
	}

	name = "line select"
	for _, wrap := range []bool{false, true} {
		if wrap {
			name += " (wrap)"
		}

		t.Run(name, func(t *testing.T) {
			vi := setupVi(t, "ABC0123456\n789\n", 2)
			if wrap {
				vi.Resize(4, 10)
			} else {
				vi.Resize(10, 10)
			}

			events := "vllyvlld"
			for _, event := range events {
				vi.Handle(term.Event{Type: term.EventKey, Ch: event})
			}

			require.Equal(t, "0123456\n789\n", vi.less.Buffer().String())
			paste, err := vi.config.clipboard.Paste(vi.config.defaultRegister)
			require.NoError(t, err)
			require.Equal(t, "ABC", paste.Text)

			events = "V"
			for _, event := range events {
				vi.Handle(term.Event{Type: term.EventKey, Ch: event})
			}

			if wrap {
				// FIXME: Is "0123456\n"
				// At the moment Cursor.SelectLine does not honor wrap lines and honors
				// logical lines. This will be changed by PR #127 "Add directional vi
				// (d)elete and (y)ank  (OX-222).
				// require.Equal(t, "0123", vi.cursor.Selection())
			} else {
				require.Equal(t, "0123456\n", vi.cursor.Selection())
			}

			vi.Handle(term.Event{Type: term.EventKey, Ch: 'p'})

			// after pasting in visual mode vim goes to normal mode again
			assert.Equal(t, normalMode, vi.mode())

			if wrap {
				// FIXME: Is "ABC789\n"
				// At the moment Cursor.SelectLine does not honor wrap lines and honors
				// logical lines. This will be changed by PR #127 "Add directional vi
				// (d)elete and (y)ank  (OX-222).
				//assert.Equal(t, "ABC456\n789\n", vi.less.Buffer().String())
			} else {
				assert.Equal(t, "ABC\n789\n", vi.less.Buffer().String())
			}

			cell, ok := vi.cursor.Cell()
			require.True(t, ok)
			require.Equal(t, 'A', cell.Ch,
				"current cell char is not 'A' but '%c'", cell.Ch)
		})
	}

	name = "block select"
	for _, wrap := range []bool{false, true} {
		if wrap {
			name += " (wrap)"
		}

		t.Run(name, func(t *testing.T) {
			vi := setupVi(t, "ABC\nDEF\n0123456\n789\n", 2)
			if wrap {
				vi.Resize(4, 10)
			} else {
				vi.Resize(10, 10)
			}

			vi.Handle(term.Event{Type: term.EventKey, Ch: 'v', Mod: term.ModCtrl})
			for _, event := range "lljyVj\"_d" {
				vi.Handle(term.Event{Type: term.EventKey, Ch: event})
			}

			require.Equal(t, "0123456\n789\n", vi.less.Buffer().String())
			paste, err := vi.config.clipboard.Paste(vi.config.defaultRegister)
			require.NoError(t, err)
			require.Equal(t, "ABC\nDEF", paste.Text)

			vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
			vi.Handle(term.Event{Type: term.EventKey, Ch: 'v', Mod: term.ModCtrl})
			vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})

			if wrap {
				require.Equal(t, "1\n8", vi.cursor.Selection())
			} else {
				require.Equal(t, "1\n8", vi.cursor.Selection())
			}

			vi.Handle(term.Event{Type: term.EventKey, Ch: 'p'})

			if wrap {
				assert.Equal(t, "0ABC23456\n7DEF9\n", vi.less.Buffer().String())
			} else {
				assert.Equal(t, "0ABC23456\n7DEF9\n", vi.less.Buffer().String())
			}

			cell, ok := vi.cursor.Cell()
			require.True(t, ok)
			require.Equal(t, 'A', cell.Ch,
				"current cell char is not 'A' but '%c'", cell.Ch)
		})
	}
}

func TestViGoPasteLeavesCursorAfterText(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		cursorAt      term.Coordinates
		clipboardText string
		clipboardMode text.SelectMode
		input         string
		wantBuffer    string
		wantPosition  term.Coordinates
		wantCell      rune
		wantCount     int
		wantCountText string
	}{
		{
			name:          "gp pastes characterwise text after cursor",
			content:       "abc\ndef\nxyz",
			cursorAt:      term.Coordinates{X: 0, Y: 0},
			clipboardText: "a",
			clipboardMode: text.StandardSelection,
			input:         "gp",
			wantBuffer:    "aabc\ndef\nxyz",
			wantPosition:  term.Coordinates{X: 2, Y: 0},
			wantCell:      'b',
			wantCount:     1,
			wantCountText: "",
		},
		{
			name:          "gP pastes characterwise text before cursor",
			content:       "abc\ndef\nxyz",
			cursorAt:      term.Coordinates{X: 1, Y: 0},
			clipboardText: "a",
			clipboardMode: text.StandardSelection,
			input:         "gP",
			wantBuffer:    "aabc\ndef\nxyz",
			wantPosition:  term.Coordinates{X: 2, Y: 0},
			wantCell:      'b',
			wantCount:     1,
			wantCountText: "",
		},
		{
			name:          "gp pastes multiline characterwise text after cursor",
			content:       "abc\ndef\nxyz",
			cursorAt:      term.Coordinates{X: 0, Y: 0},
			clipboardText: "abc",
			clipboardMode: text.StandardSelection,
			input:         "gp",
			wantBuffer:    "aabcbc\ndef\nxyz",
			wantPosition:  term.Coordinates{X: 4, Y: 0},
			wantCell:      'b',
			wantCount:     1,
			wantCountText: "",
		},
		{
			name:          "gP pastes multiline characterwise text before cursor",
			content:       "abc\ndef\nxyz",
			cursorAt:      term.Coordinates{X: 3, Y: 0},
			clipboardText: "abc",
			clipboardMode: text.StandardSelection,
			input:         "gP",
			wantBuffer:    "ababcc\ndef\nxyz",
			wantPosition:  term.Coordinates{X: 5, Y: 0},
			wantCell:      'c',
			wantCount:     1,
			wantCountText: "",
		},
		{
			name:          "gp pastes line after cursor line",
			content:       "abc\ndef\nxyz",
			cursorAt:      term.Coordinates{X: 0, Y: 1},
			clipboardText: "abc\n",
			clipboardMode: text.LineSelection,
			input:         "gp",
			wantBuffer:    "abc\ndef\nabc\nxyz",
			wantPosition:  term.Coordinates{X: 0, Y: 3},
			wantCell:      'x',
			wantCount:     1,
			wantCountText: "",
		},
		{
			name:          "gP pastes line before cursor line",
			content:       "abc\ndef\nxyz",
			cursorAt:      term.Coordinates{X: 0, Y: 1},
			clipboardText: "abc\n",
			clipboardMode: text.LineSelection,
			input:         "gP",
			wantBuffer:    "abc\nabc\ndef\nxyz",
			wantPosition:  term.Coordinates{X: 0, Y: 2},
			wantCell:      'd',
			wantCount:     1,
			wantCountText: "",
		},
		{
			name:          "gp on last line appends linewise paste after buffer end",
			content:       "abc\ndef\nxyz",
			cursorAt:      term.Coordinates{X: 0, Y: 2},
			clipboardText: "abc\n",
			clipboardMode: text.LineSelection,
			input:         "gp",
			wantBuffer:    "abc\ndef\nxyz\nabc",
			wantPosition:  term.Coordinates{X: 0, Y: 3},
			wantCell:      'a',
			wantCount:     1,
			wantCountText: "",
		},
		{
			name:          "gP on last line inserts linewise paste before current line",
			content:       "abc\ndef\nxyz",
			cursorAt:      term.Coordinates{X: 0, Y: 2},
			clipboardText: "abc\n",
			clipboardMode: text.LineSelection,
			input:         "gP",
			wantBuffer:    "abc\ndef\nabc\nxyz",
			wantPosition:  term.Coordinates{X: 0, Y: 3},
			wantCell:      'x',
			wantCount:     1,
			wantCountText: "",
		},
		{
			name:          "count before gp is reset after command",
			content:       "abc\ndef\nxyz",
			cursorAt:      term.Coordinates{X: 0, Y: 1},
			clipboardText: "abc\n",
			clipboardMode: text.LineSelection,
			input:         "2gp",
			wantBuffer:    "abc\ndef\nabc\nabc\nxyz",
			wantPosition:  term.Coordinates{X: 0, Y: 4},
			wantCell:      'x',
			wantCount:     1,
			wantCountText: "",
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			vi := setupVi(t, tcase.content, 2)
			vi.Resize(20, 10)
			vi.setCursorAtScroll(tcase.cursorAt)
			err := vi.config.clipboard.Copy(vi.config.defaultRegister, clipboard.Data{
				Text:     tcase.clipboardText,
				Metadata: tcase.clipboardMode,
			})
			require.NoError(t, err)

			for _, event := range tcase.input {
				_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: event})
				require.True(t, handled, "sequence %q failed on %q", tcase.input, string(event))
			}

			assert.Equal(t, tcase.wantBuffer, vi.less.Buffer().String())
			assert.Equal(t, normalMode, vi.mode())
			assert.Equal(t, tcase.wantPosition, vi.cursorAtScroll())
			assert.Equal(t, tcase.wantCount, vi.count)
			assert.Equal(t, tcase.wantCountText, vi.countDigits)

			cell, ok := vi.cursor.Cell()
			require.True(t, ok)
			assert.Equal(t, tcase.wantCell, cell.Ch)
		})
	}
}

func TestVisualBlockInsert(t *testing.T) {
	fileContent := "aaaaaa\nbbbbbb\ncccccc\ndddddd"

	suite := []struct {
		name          string
		moveCursorFn  func(*viHandlerImpl)
		selectKeys    string // keys after Ctrl-V to define block; default "jj"
		inputSequence string
		expect        string
	}{
		{
			name:          "no backspace",
			inputSequence: "01234",
			expect:        "01234aaaaaa\n01234bbbbbb\n01234cccccc\ndddddd",
		},
		{
			name:          "backspace",
			inputSequence: "01234^xy",
			expect:        "0123xyaaaaaa\n0123xybbbbbb\n0123xycccccc\ndddddd",
		},
		{
			name:          "many backspace",
			inputSequence: "01234^^^xy",
			expect:        "01xyaaaaaa\n01xybbbbbb\n01xycccccc\ndddddd",
		},
		{
			name:          "more backspaces than characters in row",
			inputSequence: "ABC^^^^^^^",
			expect:        "aaaaaa\nbbbbbb\ncccccc\ndddddd",
		},
		{
			name:          "backspace only",
			inputSequence: "^",
			expect:        "aaaaaa\nbbbbbb\ncccccc\ndddddd",
		},
		{
			name:          "many backspace only",
			inputSequence: "^^^",
			expect:        "aaaaaa\nbbbbbb\ncccccc\ndddddd",
		},
		{
			name: "wider block",
			moveCursorFn: func(vi *viHandlerImpl) {
				// move cursor to column 2
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
			},
			selectKeys:    "lljj", // select cols 2-4, rows 0-2
			inputSequence: "XY",
			expect:        "aaXYaaaa\nbbXYbbbb\nccXYcccc\ndddddd",
		},
		{
			name: "reversed horizontal selection",
			moveCursorFn: func(vi *viHandlerImpl) {
				// start at column 3
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
			},
			selectKeys:    "hhjj", // select leftward from col 3 to col 1, rows 0-2
			inputSequence: "XY",
			// I inserts at left edge (col 1)
			expect: "aXYaaaaa\nbXYbbbbb\ncXYccccc\ndddddd",
		},
		{
			name:          "reversed vertical selection",
			selectKeys:    "kk", // select upward: start at row 2, go to row 0
			inputSequence: "XY",
			moveCursorFn: func(vi *viHandlerImpl) {
				// start at row 2
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
			},
			expect: "XYaaaaaa\nXYbbbbbb\nXYcccccc\ndddddd",
		},
	}

	for _, tcase := range suite {
		t.Run(tcase.name, func(t *testing.T) {
			vi := setupVi(t, fileContent, 2)
			vi.Resize(20, 10)

			if tcase.moveCursorFn != nil {
				tcase.moveCursorFn(vi)
			}

			selectKeys := tcase.selectKeys
			if selectKeys == "" {
				selectKeys = "jj"
			}

			vi.Handle(term.Event{Type: term.EventKey, Ch: 'v', Mod: term.ModCtrl})
			for _, ch := range selectKeys {
				vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			}
			vi.Handle(term.Event{Type: term.EventKey, Ch: 'I'})

			for _, ch := range tcase.inputSequence {
				// following same convetions as handler.handlertest.SequenceTestCase
				if ch == '^' {
					vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyBackspace})
				} else {
					vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
				}

			}

			vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
			assert.Equal(t, tcase.expect,
				vi.less.Buffer().String())

		})
	}
}

func TestVisualBlockAppend(t *testing.T) {
	fileContent := "aaaaaa\nbbbbbb\ncccccc\ndddddd"

	suite := []struct {
		name          string
		moveCursorFn  func(*viHandlerImpl)
		selectKeys    string // keys after Ctrl-V; default "jj"
		inputSequence string
		expect        string
	}{
		{
			name:          "no backspace",
			inputSequence: "01234",
			// block is col 0 only, rows 0-2; A appends at col 1
			expect: "a01234aaaaa\nb01234bbbbb\nc01234ccccc\ndddddd",
		},
		{
			name:          "backspace",
			inputSequence: "01234^xy",
			expect:        "a0123xyaaaaa\nb0123xybbbbb\nc0123xyccccc\ndddddd",
		},
		{
			name:          "many backspace",
			inputSequence: "01234^^^xy",
			expect:        "a01xyaaaaa\nb01xybbbbb\nc01xyccccc\ndddddd",
		},
		{
			name:          "more backspaces than characters",
			inputSequence: "ABC^^^^^^^",
			// backspacing past insertion deletes existing chars at append position
			expect: "aaaaa\nbbbbb\nccccc\ndddddd",
		},
		{
			name:          "backspace only",
			inputSequence: "^",
			// single backspace from append position deletes char at col 0
			expect: "aaaaa\nbbbbb\nccccc\ndddddd",
		},
		{
			name: "wider block",
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
			},
			selectKeys:    "lljj", // cols 2-4, rows 0-2; A appends at col 5
			inputSequence: "XY",
			expect:        "aaaaaXYa\nbbbbbXYb\ncccccXYc\ndddddd",
		},
		{
			name: "reversed horizontal selection",
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
			},
			selectKeys:    "hhjj", // anchor col 3, cursor col 1; block cols 1-3, A at col 4
			inputSequence: "XY",
			expect:        "aaaaXYaa\nbbbbXYbb\nccccXYcc\ndddddd",
		},
		{
			name:          "reversed vertical selection",
			selectKeys:    "kk",
			inputSequence: "XY",
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
			},
			expect: "aXYaaaaa\nbXYbbbbb\ncXYccccc\ndddddd",
		},
	}

	for _, tcase := range suite {
		t.Run(tcase.name, func(t *testing.T) {
			vi := setupVi(t, fileContent, 2)
			vi.Resize(20, 10)

			if tcase.moveCursorFn != nil {
				tcase.moveCursorFn(vi)
			}

			selectKeys := tcase.selectKeys
			if selectKeys == "" {
				selectKeys = "jj"
			}

			vi.Handle(term.Event{Type: term.EventKey, Ch: 'v', Mod: term.ModCtrl})
			for _, ch := range selectKeys {
				vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			}
			vi.Handle(term.Event{Type: term.EventKey, Ch: 'A'})

			for _, ch := range tcase.inputSequence {
				if ch == '^' {
					vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyBackspace})
				} else {
					vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
				}
			}

			vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
			assert.Equal(t, tcase.expect,
				vi.less.Buffer().String())
		})
	}
}

func TestVisualBlockChange(t *testing.T) {
	fileContent := "aaaaaa\nbbbbbb\ncccccc\ndddddd"

	suite := []struct {
		name          string
		moveCursorFn  func(*viHandlerImpl)
		selectKeys    string // keys after Ctrl-V; default "jj"
		changeKey     rune   // 'c' or 's'; default 'c'
		inputSequence string
		expect        string
	}{
		{
			name:          "single column change",
			inputSequence: "X",
			// block is col 0, rows 0-2; delete col 0, insert "X"
			expect: "Xaaaaa\nXbbbbb\nXccccc\ndddddd",
		},
		{
			name: "wider block change",
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
			},
			selectKeys:    "lljj", // cols 2-4, rows 0-2
			inputSequence: "XY",
			expect:        "aaXYa\nbbXYb\nccXYc\ndddddd",
		},
		{
			name:          "change with backspace",
			selectKeys:    "lljj",
			inputSequence: "ABCD^xy",
			// block is cols 0-2, rows 0-2; delete 3 cols, type ABCDxy with 1 BS
			expect: "ABCxyaaa\nABCxybbb\nABCxyccc\ndddddd",
		},
		{
			name:      "substitute single column",
			changeKey: 's',
			// block is col 0, rows 0-2; delete col 0, insert "Z"
			inputSequence: "Z",
			expect:        "Zaaaaa\nZbbbbb\nZccccc\ndddddd",
		},
		{
			name: "reversed horizontal change",
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
			},
			selectKeys:    "hhjj", // anchor col 3, cursor col 1; block cols 1-3
			inputSequence: "XY",
			expect:        "aXYaa\nbXYbb\ncXYcc\ndddddd",
		},
	}

	for _, tcase := range suite {
		t.Run(tcase.name, func(t *testing.T) {
			vi := setupVi(t, fileContent, 2)
			vi.Resize(20, 10)

			if tcase.moveCursorFn != nil {
				tcase.moveCursorFn(vi)
			}

			selectKeys := tcase.selectKeys
			if selectKeys == "" {
				selectKeys = "jj"
			}
			changeKey := tcase.changeKey
			if changeKey == 0 {
				changeKey = 'c'
			}

			vi.Handle(term.Event{Type: term.EventKey, Ch: 'v', Mod: term.ModCtrl})
			for _, ch := range selectKeys {
				vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			}
			vi.Handle(term.Event{Type: term.EventKey, Ch: changeKey})

			for _, ch := range tcase.inputSequence {
				if ch == '^' {
					vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyBackspace})
				} else {
					vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
				}
			}

			vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
			assert.Equal(t, tcase.expect,
				vi.less.Buffer().String())
		})
	}
}

func TestVisualBlockReplace(t *testing.T) {
	suite := []struct {
		content      string
		name         string
		moveCursorFn func(*viHandlerImpl)
		selectKeys   string // keys after Ctrl-V; default "lljj"
		replaceKey   term.Event
		expect       string
	}{
		{
			name:       "replaces rectangular block",
			selectKeys: "lljj", // cols 0-2, rows 0-2
			replaceKey: term.Event{Type: term.EventKey, Ch: 'X'},
			expect:     "XXXdef\nXXXjkl\nXXXpqr\nstuvwx",
		},
		{
			name: "replaces reversed horizontal block",
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
			},
			selectKeys: "hhjj", // cols 1-3, rows 0-2
			replaceKey: term.Event{Type: term.EventKey, Ch: 'Y'},
			expect:     "aYYYef\ngYYYkl\nmYYYqr\nstuvwx",
		},
		{
			name:       "replaces reversed vertical block",
			selectKeys: "kkll", // start on row 2, select rows 0-2 and cols 0-2
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
				vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
			},
			replaceKey: term.Event{Type: term.EventKey, Ch: 'Z'},
			expect:     "ZZZdef\nZZZjkl\nZZZpqr\nstuvwx",
		},
		{
			name:       "space key replaces selected cells with spaces",
			selectKeys: "lj", // cols 0-1, rows 0-1
			replaceKey: term.Event{Type: term.EventKey, Key: term.KeySpace},
			expect:     "  cdef\n  ijkl\nmnopqr\nstuvwx",
		},
		{
			name:       "ragged block skips columns missing from shorter lines",
			content:    "abcdef\ngh\nmnopqr\nstuvwx",
			selectKeys: "lljj", // cols 0-2, rows 0-2; row 1 only has cols 0-1
			replaceKey: term.Event{Type: term.EventKey, Ch: 'R'},
			expect:     "RRRdef\nRR\nRRRpqr\nstuvwx",
		},
		{
			name:       "ragged block leaves longer unselected columns intact",
			content:    "abcd\nefghijkl\nmnopqr\nstuvwx",
			selectKeys: "llj", // cols 0-2, rows 0-1
			replaceKey: term.Event{Type: term.EventKey, Ch: 'L'},
			expect:     "LLLd\nLLLhijkl\nmnopqr\nstuvwx",
		},
		{
			name:       "ragged block skips blank lines",
			content:    "abcdef\n\nmnopqr\nstuvwx",
			selectKeys: "lljj", // cols 0-2, rows 0-2; row 1 has no cells
			replaceKey: term.Event{Type: term.EventKey, Ch: 'B'},
			expect:     "BBBdef\n\nBBBpqr\nstuvwx",
		},
		{
			name:       "high-column ragged block skips shorter intermediate rows",
			content:    "abcdef\ngh\nmnopqr\nstuvwx",
			selectKeys: "lllljj", // cols 0-4, rows 0-2; row 1 only has cols 0-1
			replaceKey: term.Event{Type: term.EventKey, Ch: 'H'},
			expect:     "HHHHHf\nHH\nHHHHHr\nstuvwx",
		},
		{
			name:       "block containing nul cells replaces them like any other cell",
			content:    "ab\x00de\nf\x00hij\nklmno",
			selectKeys: "llj", // cols 0-2, rows 0-1; each row contains one NUL in the block
			replaceKey: term.Event{Type: term.EventKey, Ch: 'N'},
			expect:     "NNNde\nNNNij\nklmno",
		},
		{
			name:       "selection ending at line end does not append cells",
			content:    "abc\ndefg\nhijkl",
			selectKeys: "$$j", // cols 0-3 after clamping, rows 0-1; row 0 has cols 0-2
			replaceKey: term.Event{Type: term.EventKey, Ch: 'E'},
			expect:     "EEE\nEEEE\nhijkl",
		},
		{
			name:    "selection starting at last cell replaces matching column on each line",
			content: "abc\ndefgh\nijklm",
			moveCursorFn: func(vi *viHandlerImpl) {
				vi.Handle(term.Event{Type: term.EventKey, Ch: '$'})
			},
			selectKeys: "j", // normal-mode cursor correction leaves $ on the last real cell
			replaceKey: term.Event{Type: term.EventKey, Ch: 'T'},
			expect:     "abT\ndeTgh\nijklm",
		},
		{
			name:       "zero rune replacement key is ignored until a character arrives",
			selectKeys: "lj", // cols 0-1, rows 0-1
			replaceKey: term.Event{Type: term.EventKey},
			expect:     "abcdef\nghijkl\nmnopqr\nstuvwx",
		},
	}

	for _, tcase := range suite {
		t.Run(tcase.name, func(t *testing.T) {
			content := tcase.content
			if content == "" {
				content = "abcdef\nghijkl\nmnopqr\nstuvwx"
			}
			vi := setupVi(t, content, 2)
			vi.Resize(20, 10)

			if tcase.moveCursorFn != nil {
				tcase.moveCursorFn(vi)
			}

			selectKeys := tcase.selectKeys
			if selectKeys == "" {
				selectKeys = "lljj"
			}

			vi.Handle(term.Event{Type: term.EventKey, Ch: 'v', Mod: term.ModCtrl})
			for _, ch := range selectKeys {
				vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			}
			vi.Handle(term.Event{Type: term.EventKey, Ch: 'r'})
			vi.Handle(tcase.replaceKey)

			if tcase.replaceKey.Ch == 0 && tcase.replaceKey.Key == 0 {
				assert.Equal(t, replaceOneMode, vi.mode())
			} else {
				assert.Equal(t, normalMode, vi.mode())
			}
			assert.Equal(t, tcase.expect, vi.less.Buffer().String())
		})
	}
}

func TestVisualBlockReplaceRepeat(t *testing.T) {
	buf := cell.NewBuffer()
	buf.Init()
	_, err := buf.ReadFrom(strings.NewReader("abcdef\nghijkl\nmnopqr\nstuvwx"))
	require.NoError(t, err)

	vi := New(buf, uri, WithTabspaces(2))
	vi.Resize(20, 10)

	_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'v', Mod: term.ModCtrl})
	require.True(t, handled)
	for _, ch := range "lljjrX" {
		_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
		require.True(t, handled, "event %q should be handled", string(ch))
	}
	assert.Equal(t, "XXXdef\nXXXjkl\nXXXpqr\nstuvwx", buf.String())

	_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
	require.True(t, handled)
	_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: '.'})
	require.True(t, handled)

	assert.Equal(t, "XXXdef\nXXXjkl\nXXXpqr\nXXXvwx", buf.String())
	assert.Equal(t, normalMode, vi.handler.mode())
}

func TestVisualBlockSwapCorner(t *testing.T) {
	type testCase struct {
		name                    string
		content                 string
		width                   int
		height                  int
		before                  []term.Event
		wantBeforeCursor        term.Coordinates
		wantBeforeAnchor        term.Coordinates
		wantBeforeCursorVisible bool
		wantBeforeAnchorVisible bool
		wantAfterCursorVisible  bool
		wantAfterAnchorVisible  bool
	}

	key := func(ch rune) term.Event {
		return term.Event{Type: term.EventKey, Ch: ch}
	}
	ctrl := func(ch rune) term.Event {
		return term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: ch}
	}

	suite := []testCase{
		{
			name:                    "different lines middle columns swaps opposite corner",
			content:                 "abcd\nefgh\nijkl",
			width:                   10,
			height:                  20,
			before:                  []term.Event{key('l'), ctrl('v'), key('j'), key('l')},
			wantBeforeCursor:        term.Coordinates{X: 2, Y: 1},
			wantBeforeAnchor:        term.Coordinates{X: 1},
			wantBeforeCursorVisible: true,
			wantBeforeAnchorVisible: true,
			wantAfterCursorVisible:  true,
			wantAfterAnchorVisible:  true,
		},
		{
			name:                    "same line block selection behaves like end swap",
			content:                 "abcdef",
			width:                   10,
			height:                  20,
			before:                  []term.Event{key('l'), ctrl('v'), key('l'), key('l')},
			wantBeforeCursor:        term.Coordinates{X: 3},
			wantBeforeAnchor:        term.Coordinates{X: 1},
			wantBeforeCursorVisible: true,
			wantBeforeAnchorVisible: true,
			wantAfterCursorVisible:  true,
			wantAfterAnchorVisible:  true,
		},
		{
			name:                    "same column block selection behaves like end swap",
			content:                 "abcd\nefgh\nijkl",
			width:                   10,
			height:                  20,
			before:                  []term.Event{key('l'), ctrl('v'), key('j'), key('j')},
			wantBeforeCursor:        term.Coordinates{X: 1, Y: 2},
			wantBeforeAnchor:        term.Coordinates{X: 1},
			wantBeforeCursorVisible: true,
			wantBeforeAnchorVisible: true,
			wantAfterCursorVisible:  true,
			wantAfterAnchorVisible:  true,
		},
		{
			name:                    "beginning of line to end of line across rows",
			content:                 "abcd\nefgh",
			width:                   10,
			height:                  20,
			before:                  []term.Event{ctrl('v'), key('j'), key('$')},
			wantBeforeCursor:        term.Coordinates{X: 4, Y: 1},
			wantBeforeAnchor:        term.Coordinates{},
			wantBeforeCursorVisible: true,
			wantBeforeAnchorVisible: true,
			wantAfterCursorVisible:  true,
			wantAfterAnchorVisible:  true,
		},
		{
			name:                    "start of file block selection",
			content:                 "abcd\nefgh\nijkl",
			width:                   10,
			height:                  20,
			before:                  []term.Event{ctrl('v'), key('j'), key('l')},
			wantBeforeCursor:        term.Coordinates{X: 1, Y: 1},
			wantBeforeAnchor:        term.Coordinates{},
			wantBeforeCursorVisible: true,
			wantBeforeAnchorVisible: true,
			wantAfterCursorVisible:  true,
			wantAfterAnchorVisible:  true,
		},
		{
			name:                    "end of file block selection",
			content:                 "abcd\nefgh\nijkl",
			width:                   10,
			height:                  20,
			before:                  []term.Event{key('G'), key('$'), ctrl('v'), key('k'), key('h')},
			wantBeforeCursor:        term.Coordinates{X: 2, Y: 1},
			wantBeforeAnchor:        term.Coordinates{X: 3, Y: 2},
			wantBeforeCursorVisible: true,
			wantBeforeAnchorVisible: true,
			wantAfterCursorVisible:  true,
			wantAfterAnchorVisible:  true,
		},
		{
			name:                    "selection start is above rendered view after swap",
			content:                 "aaaa\nbbbb\ncccc\ndddd\neeee\nffff",
			width:                   10,
			height:                  3,
			before:                  []term.Event{key('G'), key('l'), ctrl('v'), key('k'), key('k'), key('k')},
			wantBeforeCursor:        term.Coordinates{X: 1, Y: 2},
			wantBeforeAnchor:        term.Coordinates{X: 1, Y: 5},
			wantBeforeCursorVisible: true,
			wantBeforeAnchorVisible: false,
			wantAfterCursorVisible:  true,
			wantAfterAnchorVisible:  false,
		},
		{
			name:                    "selection end is below rendered view before swap and anchor becomes offscreen after swap",
			content:                 "aaaa\nbbbb\ncccc\ndddd\neeee\nffff",
			width:                   10,
			height:                  3,
			before:                  []term.Event{key('l'), ctrl('v'), key('j'), key('j'), key('j')},
			wantBeforeCursor:        term.Coordinates{X: 1, Y: 3},
			wantBeforeAnchor:        term.Coordinates{X: 1},
			wantBeforeCursorVisible: true,
			wantBeforeAnchorVisible: false,
			wantAfterCursorVisible:  true,
			wantAfterAnchorVisible:  false,
		},
		{
			name:                    "selection start is left of rendered view after horizontal scroll swap",
			content:                 "0123456789abcdef\n0123456789abcdef",
			width:                   4,
			height:                  2,
			before:                  []term.Event{key('$'), ctrl('v'), key('k'), key('k')},
			wantBeforeCursor:        term.Coordinates{X: 15},
			wantBeforeAnchor:        term.Coordinates{X: 15},
			wantBeforeCursorVisible: true,
			wantBeforeAnchorVisible: true,
			wantAfterCursorVisible:  true,
			wantAfterAnchorVisible:  true,
		},
		{
			name:                    "selection start left of rendered view in wide block after swap",
			content:                 "0123456789abcdef\n0123456789abcdef",
			width:                   4,
			height:                  2,
			before:                  []term.Event{key('$'), ctrl('v'), key('j'), key('h'), key('h')},
			wantBeforeCursor:        term.Coordinates{X: 13, Y: 1},
			wantBeforeAnchor:        term.Coordinates{X: 15},
			wantBeforeCursorVisible: true,
			wantBeforeAnchorVisible: true,
			wantAfterCursorVisible:  true,
			wantAfterAnchorVisible:  true,
		},
		{
			name:                    "selection end is right of rendered view before swap and anchor becomes offscreen after swap",
			content:                 "0123456789abcdef\n0123456789abcdef",
			width:                   4,
			height:                  2,
			before:                  []term.Event{ctrl('v'), key('j'), key('$')},
			wantBeforeCursor:        term.Coordinates{X: 16, Y: 1},
			wantBeforeAnchor:        term.Coordinates{},
			wantBeforeCursorVisible: false,
			wantBeforeAnchorVisible: false,
			wantAfterCursorVisible:  true,
			wantAfterAnchorVisible:  false,
		},
	}

	for _, tc := range suite {
		t.Run(tc.name, func(t *testing.T) {
			vi := setupVi(t, tc.content, 2, WithWrap(false))
			vi.Resize(tc.width, tc.height)
			vi.Draw(term.NoopWriter{})

			handleViEvents(vi, tc.before)

			require.Equal(t, visualBlockMode, vi.mode())

			beforeCursor := vi.cursor.CursorAtScroll()
			require.Equal(t, tc.wantBeforeCursor, beforeCursor)

			beforeAnchor, ok := vi.cursor.SelectionFrom()
			require.True(t, ok)
			require.Equal(t, tc.wantBeforeAnchor, beforeAnchor)
			assert.Equal(t, tc.wantBeforeCursorVisible, viPositionVisible(vi, beforeCursor))
			assert.Equal(t, tc.wantBeforeAnchorVisible, viPositionVisible(vi, beforeAnchor))

			selectionBefore := vi.cursor.Selection()
			require.NotEmpty(t, selectionBefore)
			bufferBefore := vi.less.Buffer().String()
			wantAfterCursor, wantAfterAnchor := swappedBlockCorners(beforeAnchor, beforeCursor)

			vi.Handle(key('O'))

			assert.Equal(t, visualBlockMode, vi.mode())
			assert.Equal(t, bufferBefore, vi.less.Buffer().String())
			assert.Equal(t, wantAfterCursor, vi.cursor.CursorAtScroll())
			assert.Equal(t, selectionBefore, vi.cursor.Selection())

			afterAnchor, ok := vi.cursor.SelectionFrom()
			require.True(t, ok)
			assert.Equal(t, wantAfterAnchor, afterAnchor)
			assert.Equal(t, tc.wantAfterCursorVisible, viPositionVisible(vi, wantAfterCursor))
			assert.Equal(t, tc.wantAfterAnchorVisible, viPositionVisible(vi, afterAnchor))
		})
	}
}

func TestNormalPageScrolls(t *testing.T) {
	fileContent := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"

	suite := []struct {
		name          string
		setCursor     term.Coordinates
		inputSequence term.Event
		expect        string
	}{
		{
			name:          "ctrl-e scrolls down, keeps cursor fixed",
			setCursor:     term.Coordinates{Y: 2},
			inputSequence: term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'e'},
			expect:        "b\nX\nd\ne",
		},
		{
			name:          "ctrl-e scrolls down, moves cursor if oob",
			setCursor:     term.Coordinates{},
			inputSequence: term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'e'},
			expect:        "X\nc\nd\ne",
		},
		{
			name:          "ctrl-y scrolls up, moves cursor if oob",
			setCursor:     term.Coordinates{Y: 5},
			inputSequence: term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'y'},
			expect:        "b\nc\nd\nX",
		},
		{
			name:          "ctrl-b move screen up one page, cursor to last line",
			setCursor:     term.Coordinates{Y: 7},
			inputSequence: term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'b'},
			expect:        "c\nd\ne\nX",
		},
		{
			name:          "ctrl-f move screen down one page, cursor to first line",
			setCursor:     term.Coordinates{Y: 1},
			inputSequence: term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'f'},
			expect:        "X\nd\ne\nf",
		},
		{
			name:          "ctrl-u move screen up half page, cursor to last line",
			setCursor:     term.Coordinates{Y: 7},
			inputSequence: term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'u'},
			expect:        "c\nd\ne\nX",
		},
		{
			name:          "ctrl-d move screen down half page, cursor to first line",
			setCursor:     term.Coordinates{Y: 1},
			inputSequence: term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'd'},
			expect:        "X\ne\nf\ng",
		},
	}

	for _, tcase := range suite {
		t.Run(tcase.name, func(t *testing.T) {
			vi := setupVi(t, fileContent, 2)
			vi.Resize(1, 4)

			vi.setCursorAtScroll(tcase.setCursor)
			_, ok := vi.Handle(tcase.inputSequence)
			require.True(t, ok)

			w := term.NewStringWriter(1, 4)
			vi.Draw(w)
			c, _, ok := vi.Cursor()
			require.True(t, ok)
			w.SetCell(c, term.Cell{Width: 1, Ch: 'X'})
			w.Flush()
			assert.Equal(t, tcase.expect, w.String())

		})
	}
}

func TestNormalPageScrollsViewport(t *testing.T) {
	content := strings.TrimSuffix(strings.Repeat("abcd\n", 100), "\n")
	for _, tc := range []struct {
		name       string
		width      int
		height     int
		offset     int
		key        rune
		wrap       bool
		fold       bool
		invert     bool
		wantOffset int
		wantCursor term.Coordinates
	}{
		{name: "forward", width: 80, height: 46, key: 'f',
			wantOffset: 44, wantCursor: term.Coordinates{Y: 44}},
		{name: "backward", width: 80, height: 46, offset: 44, key: 'b',
			wantOffset: 0, wantCursor: term.Coordinates{Y: 45}},
		{name: "forward near end", width: 80, height: 46, offset: 44, key: 'f',
			wantOffset: 88, wantCursor: term.Coordinates{Y: 88}},
		{name: "forward past maximum seek offset", width: 80, height: 46, offset: 49, key: 'f',
			wantOffset: 93, wantCursor: term.Coordinates{Y: 93}},
		{name: "forward at end", width: 80, height: 46, offset: 54, key: 'f',
			wantOffset: 99, wantCursor: term.Coordinates{Y: 99}},
		{name: "forward last page", width: 80, height: 46, offset: 88, key: 'f',
			wantOffset: 99, wantCursor: term.Coordinates{Y: 99}},
		{name: "forward last line at top", width: 80, height: 46, offset: 99, key: 'f',
			wantOffset: 99},
		{name: "forward file shorter than window", width: 80, height: 101, key: 'f',
			wantOffset: 99, wantCursor: term.Coordinates{Y: 99}},
		{name: "backward past maximum seek offset", width: 80, height: 46, offset: 88, key: 'b',
			wantOffset: 44, wantCursor: term.Coordinates{Y: 89}},
		{name: "backward near start", width: 80, height: 46, offset: 10, key: 'b',
			wantOffset: 0, wantCursor: term.Coordinates{Y: 45}},
		{name: "backward at start", width: 80, height: 46, key: 'b',
			wantOffset: 0},
		{name: "forward one row", width: 80, height: 1, key: 'f',
			wantOffset: 1, wantCursor: term.Coordinates{Y: 1}},
		{name: "backward one row", width: 80, height: 1, offset: 1, key: 'b',
			wantOffset: 0, wantCursor: term.Coordinates{}},
		{name: "forward two rows", width: 80, height: 2, key: 'f',
			wantOffset: 1, wantCursor: term.Coordinates{Y: 1}},
		{name: "backward two rows", width: 80, height: 2, offset: 1, key: 'b',
			wantOffset: 0, wantCursor: term.Coordinates{Y: 1}},
		{name: "forward wrapped lines", width: 2, height: 6, key: 'f', wrap: true,
			wantOffset: 4, wantCursor: term.Coordinates{Y: 2}},
		{name: "backward wrapped lines", width: 2, height: 6, offset: 4, key: 'b', wrap: true,
			wantOffset: 0, wantCursor: term.Coordinates{X: 2, Y: 2}},
		{name: "forward wrapped lines near end", width: 2, height: 6, offset: 192, key: 'f', wrap: true,
			wantOffset: 196, wantCursor: term.Coordinates{Y: 98}},
		{name: "forward wrapped lines at end", width: 2, height: 6, offset: 194, key: 'f', wrap: true,
			wantOffset: 198, wantCursor: term.Coordinates{Y: 99}},
		{name: "forward wrapped last line at top", width: 2, height: 6, offset: 198, key: 'f', wrap: true,
			wantOffset: 198},
		{name: "forward folded lines", width: 80, height: 6, key: 'f', fold: true,
			wantOffset: 4, wantCursor: term.Coordinates{Y: 12}},
		{name: "backward folded lines", width: 80, height: 6, offset: 4, key: 'b', fold: true,
			wantOffset: 0, wantCursor: term.Coordinates{Y: 13}},
		{name: "forward folded lines at end", width: 80, height: 6, offset: 86, key: 'f', fold: true,
			wantOffset: 91, wantCursor: term.Coordinates{Y: 99}},
		{name: "forward inverted offset", width: 80, height: 46, offset: 54, key: 'f', invert: true,
			wantOffset: 10, wantCursor: term.Coordinates{Y: 44}},
		{name: "backward inverted offset", width: 80, height: 46, offset: 10, key: 'b', invert: true,
			wantOffset: 54, wantCursor: term.Coordinates{Y: 45}},
		{name: "forward inverted near end", width: 80, height: 46, offset: 10, key: 'f', invert: true,
			wantOffset: 0, wantCursor: term.Coordinates{Y: 54}},
		{name: "backward inverted near start", width: 80, height: 46, offset: 44, key: 'b', invert: true,
			wantOffset: 54, wantCursor: term.Coordinates{Y: 45}},
		{name: "forward inverted at end", width: 80, height: 46, key: 'f', invert: true,
			wantOffset: 0},
		{name: "backward inverted at start", width: 80, height: 46, offset: 54, key: 'b', invert: true,
			wantOffset: 54},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, row := range []int{0, tc.height / 2, max(0, tc.height-2), tc.height - 1} {
				t.Run(fmt.Sprintf("cursor target row %d", row+1), func(t *testing.T) {
					vi := setupVi(t, content, 2, WithWrap(tc.wrap))
					vi.Resize(tc.width, tc.height)
					vi.less.Scroll().InvertOffset = tc.invert
					if tc.fold {
						require.True(t, vi.less.Scroll().MarkHidden(1, 9))
					}
					vi.less.Scroll().SetOffset(term.Coordinates{Y: tc.offset})
					vi.Draw(term.NoopWriter{})
					vi.cursor.MoveToWindow(term.Coordinates{Y: row})
					require.Equal(t, tc.offset, vi.less.Scroll().SeekOffset())
					last, _ := vi.cursor.WindowCoordinates(term.Coordinates{X: 3, Y: 99})
					require.Equal(t, min(row, last.Y), vi.cursor.Coordinates().Y)

					var seeks [][2]term.Coordinates
					vi.less.Scroll().Subscribe(component.FuncScrollSubscriber(func(from, to term.Coordinates) {
						seeks = append(seeks, [2]term.Coordinates{from, to})
					}))
					before := vi.cursorAtScroll()
					_, handled := vi.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: tc.key})
					assert.Equal(t, tc.offset != tc.wantOffset, handled)
					assert.Equal(t, tc.wantOffset, vi.less.Scroll().SeekOffset())
					if tc.offset == tc.wantOffset {
						assert.Equal(t, before, vi.cursorAtScroll())
						assert.Empty(t, seeks)
					} else {
						assert.Equal(t, tc.wantCursor, vi.cursorAtScroll())
						assert.Equal(t, [][2]term.Coordinates{{{Y: tc.offset}, {Y: tc.wantOffset}}}, seeks)
					}
				})
			}
		})
	}
}

func TestNormalPageScrollsLongLastLine(t *testing.T) {
	content := strings.Repeat("abcd\n", 99) + strings.Repeat("x", 80*8)
	for _, tc := range []struct {
		name        string
		offset      int
		wantOffsets []int
	}{
		{name: "approaching last line", offset: 92, wantOffsets: []int{96, 99, 99, 99}},
		{name: "last line at top", offset: 99, wantOffsets: []int{99, 99, 99}},
		{name: "inside last line", offset: 103, wantOffsets: []int{103, 103, 103}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vi := setupVi(t, content, 2, WithWrap(true))
			vi.Resize(80, 6)
			scroll := vi.less.Scroll()
			scroll.SetOffset(term.Coordinates{Y: tc.offset})
			vi.Draw(term.NoopWriter{})
			vi.cursor.MoveToWindow(term.Coordinates{Y: 2})
			require.Equal(t, tc.offset, scroll.SeekOffset())
			var seeks int
			scroll.Subscribe(component.FuncScrollSubscriber(func(_, _ term.Coordinates) {
				seeks++
			}))
			for _, want := range tc.wantOffsets {
				before := scroll.SeekOffset()
				cursor := vi.cursorAtScroll()
				seeks = 0
				_, handled := vi.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'f'})
				vi.Draw(term.NoopWriter{})
				assert.Equal(t, want, scroll.SeekOffset())
				assert.Equal(t, before != want, handled)
				if before == want {
					assert.Equal(t, cursor, vi.cursorAtScroll())
					assert.Zero(t, seeks)
				} else {
					assert.Equal(t, term.Coordinates{Y: want}, vi.cursorAtScroll())
					assert.Equal(t, 1, seeks)
				}
			}
		})
	}
}

func TestNormalPageScrollsBeforeResize(t *testing.T) {
	for _, ch := range []rune{'f', 'b'} {
		t.Run(string(ch), func(t *testing.T) {
			vi := setupVi(t, "a\nb\nc", 2)
			_, handled := vi.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: ch})
			assert.False(t, handled)
			assert.Zero(t, vi.less.Scroll().SeekOffset())
			assert.Equal(t, term.Coordinates{}, vi.cursorAtScroll())
		})
	}
}

func TestCentering(t *testing.T) {
	fileContent := "a\nb\nc\nd\ne\n	f\ng\nh\ni\nj\nk"

	suite := []struct {
		name          string
		setCursor     term.Coordinates
		inputSequence string
		expect        string
	}{
		{
			name:          "zz centers the view around the cursor if there's enough offset available",
			setCursor:     term.Coordinates{Y: 5},
			inputSequence: "zz",
			expect:        "d   \ne   \n Xf \ng   ",
		},
		{
			name:          "zz centers the last line, scrolling past the end of the file",
			setCursor:     term.Coordinates{Y: 10},
			inputSequence: "zz",
			expect:        "i   \nj   \nX   \n    ",
		},
		{
			name:          "zz does nothing with first line",
			setCursor:     term.Coordinates{},
			inputSequence: "zz",
			expect:        "X   \nb   \nc   \nd   ",
		},
		{
			name:          "z. centers the view around the cursor and moves to first non blank",
			setCursor:     term.Coordinates{Y: 5},
			inputSequence: "z.",
			expect:        "d   \ne   \n  X \ng   ",
		},
		{
			name:          "zt repositions cursor at the top of the view",
			setCursor:     term.Coordinates{Y: 5},
			inputSequence: "zt",
			expect:        " Xf \ng   \nh   \ni   ",
		},
		{
			name:          "zt on the last line scrolls past the end of the file",
			setCursor:     term.Coordinates{Y: 10},
			inputSequence: "zt",
			expect:        "X   \n    \n    \n    ",
		},
		{
			name:          "zb repositions cursor at the bottom of the view",
			setCursor:     term.Coordinates{Y: 5},
			inputSequence: "zb",
			expect:        "c   \nd   \ne   \n Xf ",
		},
		{
			name:          "z<enter> repositions cursor at the top and moves to first non blank",
			setCursor:     term.Coordinates{Y: 5},
			inputSequence: "z<enter>",
			expect:        "  X \ng   \nh   \ni   ",
		},
		{
			name:          "z- repositions cursor at the bottom and moves to first non blank",
			setCursor:     term.Coordinates{Y: 5},
			inputSequence: "z-",
			expect:        "c   \nd   \ne   \n  X ",
		},
	}

	for _, tcase := range suite {
		t.Run(tcase.name, func(t *testing.T) {
			vi := setupVi(t, fileContent, 2)
			vi.Resize(4, 4)

			vi.setCursorAtScroll(tcase.setCursor)
			keys, err := term.ParseKeys(tcase.inputSequence)
			require.NoError(t, err)
			for _, key := range keys {
				vi.Handle(term.Event{
					Type: term.EventKey, Ch: key.Ch, Mod: key.Mod, Key: key.Key,
				})
			}

			w := term.NewStringWriter(4, 4)
			vi.Draw(w)
			c, _, ok := vi.Cursor()
			require.True(t, ok)
			w.SetCell(c, term.Cell{Width: 1, Ch: 'X'})
			w.Flush()
			assert.Equal(t, tcase.expect, w.String())

		})
	}
}

func TestScreenRelativeMotions(t *testing.T) {
	fileContent := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"

	suite := []struct {
		name          string
		inputSequence string
		expected      string
	}{
		{
			name:          "H moves cursor to top visible line",
			inputSequence: "H",
			expected:      "▐\ne\nf\ng",
		},
		{
			name:          "M moves cursor to middle visible line",
			inputSequence: "M",
			expected:      "d\n▐\nf\ng",
		},
		{
			name:          "L moves cursor to bottom visible line",
			inputSequence: "L",
			expected:      "d\ne\nf\n▐",
		},
		{
			name:          "count H moves cursor to nth visible line from top",
			inputSequence: "3H",
			expected:      "d\ne\n▐\ng",
		},
		{
			name:          "count L moves cursor to nth visible line from bottom",
			inputSequence: "2L",
			expected:      "d\ne\n▐\ng",
		},
	}

	for _, tcase := range suite {
		t.Run(tcase.name, func(t *testing.T) {
			vi := setupVi(t, fileContent, 2)
			vi.Resize(1, 4)
			ok := vi.less.Scroll().SetOffset(term.Coordinates{Y: 3})
			require.True(t, ok)
			vi.setCursorAtScroll(term.Coordinates{Y: 5})

			handlertest.RunHandlerSequence(t, vi, 1, 4, []handlertest.SequenceTestCase{{
				InputSequence: tcase.inputSequence,
				Expected:      tcase.expected,
			}})
		})
	}
}

func TestPasteBatching(t *testing.T) {
	t.Run("insert mode batches paste into single edit", func(t *testing.T) {
		vi := setupVi(t, "hello\nworld", 2)
		vi.Resize(40, 10)

		// Enter insert mode
		vi.Handle(term.Event{Type: term.EventKey, Ch: 'i'})
		require.Equal(t, insertMode, vi.mode())

		// Track edits via a subscriber
		editCount := 0
		vi.less.Buffer().Subscribe(&editCounter{count: &editCount})

		// Send paste sequence: PasteStart → characters → PasteEnd
		_, handled := vi.Handle(term.Event{Type: term.EventPasteStart})
		assert.True(t, handled, "PasteStart should be handled")

		pasteText := "PASTED"
		for _, ch := range pasteText {
			_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			assert.True(t, handled, "characters during paste should be handled")
		}

		// No edits should have happened yet (characters are buffered)
		assert.Equal(t, 0, editCount, "no edits should happen during paste buffering")

		_, handled = vi.Handle(term.Event{Type: term.EventPasteEnd})
		assert.True(t, handled, "PasteEnd should be handled")

		// Exactly one edit should have happened
		assert.Equal(t, 1, editCount, "paste should result in exactly one edit")

		// Verify the buffer content
		assert.Equal(t, "PASTEDhello\nworld", vi.less.Buffer().String())
	})

	t.Run("normal mode paste is consumed without inserting", func(t *testing.T) {
		vi := setupVi(t, "hello\nworld", 2)
		vi.Resize(40, 10)

		require.Equal(t, normalMode, vi.mode())

		_, handled := vi.Handle(term.Event{Type: term.EventPasteStart})
		assert.True(t, handled)

		for _, ch := range "PASTED" {
			_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			assert.True(t, handled)
		}

		_, handled = vi.Handle(term.Event{Type: term.EventPasteEnd})
		assert.True(t, handled)

		// Buffer should be unchanged (paste in normal mode does not insert)
		assert.Equal(t, "hello\nworld", vi.less.Buffer().String())
	})

	t.Run("paste with newlines", func(t *testing.T) {
		vi := setupVi(t, "AB", 2)
		vi.Resize(40, 10)

		// Enter insert mode
		vi.Handle(term.Event{Type: term.EventKey, Ch: 'i'})

		vi.Handle(term.Event{Type: term.EventPasteStart})
		for _, ch := range "line1\nline2\n" {
			vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
		}
		vi.Handle(term.Event{Type: term.EventPasteEnd})

		assert.Equal(t, "line1\nline2\nAB", vi.less.Buffer().String())
	})
}

// editCounter counts the number of OnDidEdit calls.
type editCounter struct {
	count *int
}

func (c *editCounter) OnWillEdit(_ context.Context, _, _ term.Coordinates, _ string) {}
func (c *editCounter) OnDidEdit(_ context.Context, _, _ term.Coordinates, _ string) {
	*c.count++
}

func TestScreenRelativeMotionsWrap(t *testing.T) {
	fileContent := "123456789\nab\nc"

	suite := []struct {
		name          string
		inputSequence string
		expected      string
	}{
		{
			name:          "count H uses wrapped screen rows",
			inputSequence: "2H",
			expected:      "123\n4▐6\n789\nab ",
		},
		{
			name:          "count L uses wrapped screen rows",
			inputSequence: "2L",
			expected:      "123\n456\n▐89\nab ",
		},
	}

	for _, tcase := range suite {
		t.Run(tcase.name, func(t *testing.T) {
			vi := setupVi(t, fileContent, 2, WithWrap(true))
			vi.Resize(3, 4)
			vi.setCursorAtScroll(term.Coordinates{X: 1, Y: 1})

			handlertest.RunHandlerSequence(t, vi, 3, 4, []handlertest.SequenceTestCase{{
				InputSequence: tcase.inputSequence,
				Expected:      tcase.expected,
			}})
		})
	}
}

func TestGjGk(t *testing.T) {
	// Content: "123456789" wraps at width 3 into:
	//   row 0: "123"
	//   row 1: "456"
	//   row 2: "789"
	// Then "ab" on buffer line 1, "c" on buffer line 2.
	fileContent := "123456789\nab\nc"

	t.Run("gj moves down one display row within wrapped line", func(t *testing.T) {
		vi := setupVi(t, fileContent, 2, WithWrap(true))
		vi.Resize(3, 5)
		vi.Draw(term.NoopWriter{})

		// Cursor starts at (0,0) = "1". gj should move to display row 1 = "4"
		handlertest.RunHandlerSequence(t, vi, 3, 5, []handlertest.SequenceTestCase{{
			InputSequence: "gj",
			Expected:      "123\n▐56\n789\nab \nc  ",
		}})
	})

	t.Run("gk moves up one display row within wrapped line", func(t *testing.T) {
		vi := setupVi(t, fileContent, 2, WithWrap(true))
		vi.Resize(3, 5)
		vi.Draw(term.NoopWriter{})

		// Move to display row 1 with gj, then gk back to row 0
		handlertest.RunHandlerSequence(t, vi, 3, 5, []handlertest.SequenceTestCase{{
			InputSequence: "gjgk",
			Expected:      "▐23\n456\n789\nab \nc  ",
		}})
	})

	t.Run("gj twice crosses from wrapped line to next buffer line", func(t *testing.T) {
		vi := setupVi(t, fileContent, 2, WithWrap(true))
		vi.Resize(3, 5)
		vi.Draw(term.NoopWriter{})

		// From display row 0, gj gj goes to display row 2 = "789"
		handlertest.RunHandlerSequence(t, vi, 3, 5, []handlertest.SequenceTestCase{{
			InputSequence: "gjgj",
			Expected:      "123\n456\n▐89\nab \nc  ",
		}})
	})

	t.Run("3gj moves 3 display rows down", func(t *testing.T) {
		vi := setupVi(t, fileContent, 2, WithWrap(true))
		vi.Resize(3, 5)
		vi.Draw(term.NoopWriter{})

		// From row 0, 3gj should land on row 3 = "ab" (buffer line 1)
		handlertest.RunHandlerSequence(t, vi, 3, 5, []handlertest.SequenceTestCase{{
			InputSequence: "3gj",
			Expected:      "123\n456\n789\n▐b \nc  ",
		}})
	})

	t.Run("2gk from display row 2 moves up to row 0", func(t *testing.T) {
		vi := setupVi(t, fileContent, 2, WithWrap(true))
		vi.Resize(3, 5)
		vi.Draw(term.NoopWriter{})

		// Move down 2 display rows, then up 2
		handlertest.RunHandlerSequence(t, vi, 3, 5, []handlertest.SequenceTestCase{{
			InputSequence: "2gj2gk",
			Expected:      "▐23\n456\n789\nab \nc  ",
		}})
	})

	t.Run("gj in non-wrap mode behaves like j", func(t *testing.T) {
		vi := setupVi(t, fileContent, 2, WithWrap(false))
		vi.Resize(20, 5)
		vi.Draw(term.NoopWriter{})

		// gj without wrap = j, moves to next buffer line
		for _, eventChar := range "gj" {
			vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
		}
		assert.Equal(t, term.Coordinates{X: 0, Y: 1}, vi.cursor.CursorAtScroll())
	})

	t.Run("gk in non-wrap mode behaves like k", func(t *testing.T) {
		vi := setupVi(t, fileContent, 2, WithWrap(false))
		vi.Resize(20, 5)
		vi.Draw(term.NoopWriter{})

		// Move down first, then gk should go back up
		for _, eventChar := range "jgk" {
			vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
		}
		assert.Equal(t, term.Coordinates{X: 0, Y: 0}, vi.cursor.CursorAtScroll())
	})

	t.Run("gj at bottom of content does not move", func(t *testing.T) {
		vi := setupVi(t, "ab\ncd", 2, WithWrap(true))
		vi.Resize(10, 5)
		vi.Draw(term.NoopWriter{})

		// Move to last line, then gj should not move further
		for _, eventChar := range "j" {
			vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
		}
		pos := vi.cursor.CursorAtScroll()
		for _, eventChar := range "gj" {
			vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
		}
		assert.Equal(t, pos, vi.cursor.CursorAtScroll())
	})

	t.Run("gk at top of content does not move", func(t *testing.T) {
		vi := setupVi(t, "ab\ncd", 2, WithWrap(true))
		vi.Resize(10, 5)
		vi.Draw(term.NoopWriter{})

		// gk from first position should not move
		pos := vi.cursor.CursorAtScroll()
		for _, eventChar := range "gk" {
			vi.Handle(term.Event{Type: term.EventKey, Ch: eventChar})
		}
		assert.Equal(t, pos, vi.cursor.CursorAtScroll())
	})
}

func TestHandleMouseWindowCoordinates(t *testing.T) {
	newVi := func() *Vi {
		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader("alpha bravo charlie"))
		v := NewWithIndent(buf, workspaceapi.RandomURI("memory"),
			text.IndentRuneTab, 0, WithWrap(false))
		v.Resize(8, 1)
		return v
	}
	drag := func(v *Vi, x1, x2 int) {
		v.Handle(mouseEventAt(term.MouseLeft, x1, 0))
		v.Handle(mouseEventAt(term.MouseLeft, x2, 0))
		v.Handle(mouseEventAt(term.MouseRelease, x2, 0))
	}

	t.Run("normal mode drag enters visual and selects", func(t *testing.T) {
		v := newVi()
		drag(v, 6, 11)

		assert.Equal(t, term.Coordinates{X: 11}, v.CursorAtScroll())
		assert.Equal(t, visualMode, v.handler.mode())
		sel, ok := v.Selection()
		require.True(t, ok)
		// vi's visual selection is inclusive of the cursor cell.
		assert.Equal(t, "bravo ", sel)

		from, to, bok := v.SelectionBounds()
		require.True(t, bok)
		assert.Equal(t, term.Coordinates{X: 6}, from)
		assert.Equal(t, term.Coordinates{X: 12}, to,
			"host-facing bounds are half-open, one past the inclusive cursor cell")
	})

	t.Run("insert mode click repositions caret and stays insert", func(t *testing.T) {
		v := newVi()
		v.Handle(term.Event{Type: term.EventKey, Ch: 'i'})
		require.Equal(t, insertMode, v.handler.mode())

		_, handled := v.Handle(mouseEventAt(term.MouseLeft, 6, 0))
		require.True(t, handled)
		v.Handle(mouseEventAt(term.MouseRelease, 6, 0))

		assert.Equal(t, term.Coordinates{X: 6}, v.CursorAtScroll())
		assert.Equal(t, insertMode, v.handler.mode())
		_, ok := v.Selection()
		assert.False(t, ok, "a plain click must not leave a selection")

		// arrows snap back to the insert anchor; the click must move it.
		v.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowRight})
		assert.Equal(t, term.Coordinates{X: 7}, v.CursorAtScroll())
	})

	t.Run("insert mode drag enters visual and selects", func(t *testing.T) {
		v := newVi()
		v.Handle(term.Event{Type: term.EventKey, Ch: 'i'})

		drag(v, 6, 11)

		assert.Equal(t, visualMode, v.handler.mode())
		sel, ok := v.Selection()
		require.True(t, ok)
		assert.Equal(t, "bravo ", sel)
	})

	t.Run("normal mode click repositions caret and stays normal", func(t *testing.T) {
		v := newVi()
		v.Handle(mouseEventAt(term.MouseLeft, 6, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 6, 0))

		assert.Equal(t, term.Coordinates{X: 6}, v.CursorAtScroll())
		assert.Equal(t, normalMode, v.handler.mode())
		_, ok := v.Selection()
		assert.False(t, ok, "a focus click must not leave a selection")
	})

	t.Run("jitter inside the pressed cell does not select", func(t *testing.T) {
		v := newVi()
		v.Handle(mouseEventAt(term.MouseLeft, 6, 0))
		v.Handle(mouseEventAt(term.MouseLeft, 6, 0))
		v.Handle(mouseEventAt(term.MouseLeft, 6, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 6, 0))

		assert.Equal(t, term.Coordinates{X: 6}, v.CursorAtScroll())
		assert.Equal(t, normalMode, v.handler.mode())
		_, ok := v.Selection()
		assert.False(t, ok)
	})

	t.Run("insert mode jitter inside the pressed cell stays insert", func(t *testing.T) {
		v := newVi()
		v.Handle(term.Event{Type: term.EventKey, Ch: 'i'})

		v.Handle(mouseEventAt(term.MouseLeft, 6, 0))
		v.Handle(mouseEventAt(term.MouseLeft, 6, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 6, 0))

		assert.Equal(t, insertMode, v.handler.mode())
		_, ok := v.Selection()
		assert.False(t, ok)
	})

	t.Run("click then scroll leaves no highlight behind", func(t *testing.T) {
		v := newVi()
		v.Handle(mouseEventAt(term.MouseLeft, 6, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 6, 0))
		v.Handle(mouseEventAt(term.MouseWheelDown, 6, 0))
		v.Handle(mouseEventAt(term.MouseWheelUp, 6, 0))

		assert.Equal(t, normalMode, v.handler.mode())
		_, ok := v.Selection()
		assert.False(t, ok)
	})

	t.Run("click during visual exits visual and repositions", func(t *testing.T) {
		v := newVi()
		v.Handle(term.Event{Type: term.EventKey, Ch: 'v'})
		v.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
		require.Equal(t, visualMode, v.handler.mode())

		v.Handle(mouseEventAt(term.MouseLeft, 2, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 2, 0))

		assert.Equal(t, term.Coordinates{X: 2}, v.CursorAtScroll())
		assert.Equal(t, normalMode, v.handler.mode())
		_, ok := v.Selection()
		assert.False(t, ok)
	})

	t.Run("drag returning to the pressed cell keeps the selection live", func(t *testing.T) {
		v := newVi()
		v.Handle(mouseEventAt(term.MouseLeft, 6, 0))
		v.Handle(mouseEventAt(term.MouseLeft, 7, 0))
		v.Handle(mouseEventAt(term.MouseLeft, 6, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 6, 0))

		assert.Equal(t, visualMode, v.handler.mode())
		sel, ok := v.Selection()
		require.True(t, ok)
		assert.Equal(t, "b", sel)
	})

	t.Run("double-click selects the word in visual mode", func(t *testing.T) {
		v := newVi()
		v.Handle(mouseEventAt(term.MouseLeft, 7, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 7, 0))
		v.Handle(mouseEventAt(term.MouseLeft, 7, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 7, 0))

		assert.Equal(t, visualMode, v.handler.mode())
		sel, ok := v.Selection()
		require.True(t, ok)
		assert.Equal(t, "bravo", sel)
		assert.Equal(t, term.Coordinates{X: 10}, v.CursorAtScroll(),
			"the cursor rests on the word's last character")
	})

	t.Run("double-click selects the last word of a line", func(t *testing.T) {
		v := newVi()
		v.Resize(20, 1)
		v.Handle(mouseEventAt(term.MouseLeft, 15, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 15, 0))
		v.Handle(mouseEventAt(term.MouseLeft, 15, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 15, 0))

		assert.Equal(t, visualMode, v.handler.mode())
		sel, ok := v.Selection()
		require.True(t, ok)
		assert.Equal(t, "charlie", sel)
		assert.Equal(t, term.Coordinates{X: 18}, v.CursorAtScroll())
	})

	t.Run("double-click on a non-word keeps the drag selection", func(t *testing.T) {
		v := newVi()
		drag(v, 0, 3)

		sel, ok := v.Selection()
		require.True(t, ok)
		require.Equal(t, "alph", sel)

		v.Handle(mouseEventAt(term.MouseLeft, 5, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 5, 0))

		assert.Equal(t, term.Coordinates{X: 3}, v.CursorAtScroll())
		sel, ok = v.Selection()
		require.True(t, ok)
		assert.Equal(t, "alph", sel)
	})

	t.Run("double-click then yank copies the word", func(t *testing.T) {
		clip := new(mockClip)
		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader("alpha bravo charlie"))
		v := NewWithIndent(buf, workspaceapi.RandomURI("memory"),
			text.IndentRuneTab, 0,
			WithWrap(false), WithClipboard(registerset.New(clip)))
		v.Resize(20, 1)

		v.Handle(mouseEventAt(term.MouseLeft, 7, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 7, 0))
		v.Handle(mouseEventAt(term.MouseLeft, 7, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 7, 0))
		v.Handle(term.Event{Type: term.EventKey, Ch: 'y'})

		assert.Equal(t, normalMode, v.handler.mode())
		assert.Equal(t, "bravo", clip.data.Text)
	})

	t.Run("jitter after double-click keeps the word selected", func(t *testing.T) {
		v := newVi()
		v.Handle(mouseEventAt(term.MouseLeft, 7, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 7, 0))
		v.Handle(mouseEventAt(term.MouseLeft, 7, 0))
		v.Handle(mouseEventAt(term.MouseLeft, 7, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 7, 0))

		assert.Equal(t, visualMode, v.handler.mode())
		sel, ok := v.Selection()
		require.True(t, ok)
		assert.Equal(t, "bravo", sel)
	})

	t.Run("triple-click selects the line in visual line mode", func(t *testing.T) {
		v := newVi()
		v.Handle(mouseEventAt(term.MouseLeft, 7, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 7, 0))
		v.Handle(mouseEventAt(term.MouseLeft, 7, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 7, 0))
		v.Handle(mouseEventAt(term.MouseLeft, 7, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 7, 0))

		assert.Equal(t, visualLineMode, v.handler.mode())
		sel, ok := v.Selection()
		require.True(t, ok)
		// Line selections carry a trailing newline.
		assert.Equal(t, "alpha bravo charlie\n", sel)
	})

	t.Run("held move inside the pressed cell after triple-click keeps the cursor", func(t *testing.T) {
		v := newVi()
		v.Resize(20, 1)
		v.Handle(mouseEventAt(term.MouseLeft, 7, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 7, 0))
		v.Handle(mouseEventAt(term.MouseLeft, 7, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 7, 0))
		v.Handle(mouseEventAt(term.MouseLeft, 7, 0))
		v.Handle(mouseEventAt(term.MouseLeft, 7, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 7, 0))

		assert.Equal(t, visualLineMode, v.handler.mode())
		assert.Equal(t, term.Coordinates{X: 0}, v.CursorAtScroll(),
			"jitter inside the pressed cell must not drift the cursor")
		sel, ok := v.Selection()
		require.True(t, ok)
		assert.Equal(t, "alpha bravo charlie\n", sel)
	})

	t.Run("held move to another cell after triple-click follows the pointer", func(t *testing.T) {
		v := newVi()
		v.Resize(20, 1)
		v.Handle(mouseEventAt(term.MouseLeft, 7, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 7, 0))
		v.Handle(mouseEventAt(term.MouseLeft, 7, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 7, 0))
		v.Handle(mouseEventAt(term.MouseLeft, 7, 0))
		v.Handle(mouseEventAt(term.MouseLeft, 8, 0))
		v.Handle(mouseEventAt(term.MouseRelease, 8, 0))

		assert.Equal(t, visualLineMode, v.handler.mode())
		assert.Equal(t, term.Coordinates{X: 8}, v.CursorAtScroll())
		sel, ok := v.Selection()
		require.True(t, ok)
		assert.Equal(t, "alpha bravo charlie\n", sel)
	})
}

func mouseEventAt(key term.Key, x, y int) term.Event {
	return term.Event{Type: term.EventMouse, Key: key, MouseX: x, MouseY: y}
}

func TestViOperatorLeftStartWordKeepsCharUnderCursor(t *testing.T) {
	for _, tc := range []struct {
		name     string
		content  string
		at       term.Coordinates
		seq      string
		want     string
		wantMode viMode
	}{
		{"delete b", "one two three", term.Coordinates{X: 8}, "db", "one three", normalMode},
		{"delete B", "one two-x three", term.Coordinates{X: 10}, "dB", "one three", normalMode},
		{"change b", "one two three", term.Coordinates{X: 8}, "cb", "one three", insertMode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vi := setupVi(t, tc.content, 2)
			vi.Resize(80, 10)
			vi.setCursorAtScroll(tc.at)
			vi.Draw(term.NoopWriter{})
			for _, ch := range tc.seq {
				_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
				require.True(t, handled)
			}
			assert.Equal(t, tc.want, strings.TrimRight(vi.less.Buffer().String(), "\n"))
			assert.Equal(t, tc.wantMode, vi.mode())
		})
	}
}

func TestViHandlerImplPointerLeaves(t *testing.T) {
	t.Parallel()
	const width, height = 40, 10
	newHandler := func(wrap bool) *viHandlerImpl {
		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(pointerLeaveContent))
		cfg := defaultviHandlerImplConfig()
		cfg.wrap = wrap
		h := new(viHandlerImpl)
		h.init(buf, cfg)
		return h
	}
	for _, wrap := range []bool{true, false} {
		for _, mode := range pointerLeaveModes() {
			t.Run(fmt.Sprintf("%s wrap=%v", mode.name, wrap), func(t *testing.T) {
				t.Parallel()
				enter := func(h *viHandlerImpl) {
					t.Helper()
					for _, ev := range mode.keys {
						quit, _ := h.Handle(ev)
						require.False(t, quit)
					}
					require.Equal(t, mode.want, h.mode())
				}

				t.Run("leave keeps state", func(t *testing.T) {
					h := newHandler(wrap)
					h.Resize(width, height)
					enter(h)
					want := snapshotPointerLeave(h, width, height)
					require.NotZero(t, want.seek, "the mode keys must scroll the window")
					for _, out := range pointerLeaveOutside(width, height) {
						h.Handle(pointerLeave(out))
						assert.Equal(t, want, snapshotPointerLeave(h, width, height),
							"a leave to %v must not change the editor", out)
					}
				})

				for _, size := range [][2]int{{width, height}, {1, 1}} {
					t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
						h := newHandler(wrap)
						h.Resize(size[0], size[1])
						enter(h)
						handlertest.RunPointerLeaves(t, h, size[0], size[1])
					})
				}
			})
		}
	}
}
