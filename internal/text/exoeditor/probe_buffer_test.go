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

package exoeditor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vteprobe"
)

// bufferOf builds an editable *cell.Buffer (with an undoer so Version
// advances on edits) seeded with content.
func bufferOf(tb testing.TB, content string) *cell.Buffer {
	tb.Helper()
	buf := cell.NewBuffer()
	_, err := buf.ReadFrom(strings.NewReader(content))
	require.NoError(tb, err)
	return buf
}

// fakeComponent is a componentSnapshotter that returns a test-controlled
// rendered grid and cursor, so the real refreshProbe path (Snapshot
// decode, Active, Infer) runs without a pty-backed vte handler. err lets
// a test exercise the snapshot-failure branch.
type fakeComponent struct {
	cells  [][]term.Cell
	cursor term.Coordinates
	err    error
	// version is returned verbatim by Version. Tests bump it to mimic a
	// changed rendered grid and defeat refreshProbe's version guard.
	version uint64
	pid     workspaceapi.Pid
}

func (c *fakeComponent) Snapshot() (vte.Snapshot, error) {
	if c.err != nil {
		return vte.Snapshot{}, c.err
	}
	return vte.Snapshot{Primary: vte.ScreenSnapshot{Cells: c.cells, Cursor: c.cursor}}, nil
}

// Version reports the test-controlled grid revision. refreshProbe
// compares it to decide whether to re-run Infer.
func (c *fakeComponent) Version() uint64 { return c.version }

// SnapshotInto mirrors Component.SnapshotInto: it copies the fixed test
// grid into dst, reusing its capacity, so the reuse path refreshProbe
// takes in production is exercised here too.
func (c *fakeComponent) SnapshotInto(dst [][]term.Cell) (vte.Snapshot, error) {
	if c.err != nil {
		return vte.Snapshot{}, c.err
	}
	cells := term.CopyCells(dst, c.cells)
	return vte.Snapshot{
		Version: c.version,
		Primary: vte.ScreenSnapshot{Cells: cells, Cursor: c.cursor},
	}, nil
}

// DrawSnapshot mirrors Component.DrawSnapshot: it returns the same
// snapshot SnapshotInto would and stamps it with the current version,
// so the Draw path's single-lock paint+probe can be exercised without a
// pty. The paint to w is a no-op because the buffer tests assert on the
// probe, not the rendered grid.
func (c *fakeComponent) DrawSnapshot(
	_ term.Writer, dst [][]term.Cell,
) (vte.Snapshot, error) {
	return c.SnapshotInto(dst)
}

func (c *fakeComponent) Pid() workspaceapi.Pid { return c.pid }

// handlerForBufferTest builds an editorHandler around buf the way
// newHandler does for the parts relevant to cell snapshots and probe
// refresh: it seeds the snapshot, subscribes to buffer edits, installs a
// real probe, and points the component at a fake snapshotter the caller
// drives. The full newHandler is not used because it spawns a PTY-backed
// vte handler a unit test cannot drive.
func handlerForBufferTest(
	tb testing.TB, buf *cell.Buffer,
) (*editorHandler, *fakeComponent) {
	tb.Helper()
	// vte.Component starts its version at 1; mirror that so the guard in
	// refreshProbe falls through on the first call exactly as in
	// production (inferredVersion is the 0 zero value).
	comp := &fakeComponent{version: 1}
	h := &editorHandler{
		buf:       buf,
		probe:     vteprobe.New([]int{8, 4, 2}, 0.6, 8<<20),
		component: comp,
	}
	h.snapshotBufferCells()
	h.bufSub = &bufCellWatcher{h: h}
	buf.Subscribe(h.bufSub)
	tb.Cleanup(func() { buf.Unsubscribe(h.bufSub) })
	return h, comp
}

// gutterGrid renders rows of the form " N text" padded to width, the
// shape an editor with a line-number gutter produces. It mirrors the
// fixtures vteprobe's own tests use so the probe aligns confidently.
func gutterGrid(lines []string, width int) [][]term.Cell {
	cells := make([][]term.Cell, len(lines))
	for i, s := range lines {
		row := []rune(s)
		out := make([]term.Cell, 0, width)
		for _, r := range row {
			out = append(out, term.Cell{Ch: r, Width: 1})
		}
		for len(out) < width {
			out = append(out, term.Cell{Ch: ' ', Width: 1})
		}
		cells[i] = out
	}
	return cells
}

func bufferCellLinesForTest(cells [][]term.Cell) []string {
	if len(cells) == 0 {
		return nil
	}
	return strings.Split(term.CellsToString(cells), "\n")
}

func TestBufferCellsFollowsBufferNotDisk(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "code.go")
	// Disk starts out diverged from the buffer mirror on purpose.
	require.NoError(t, os.WriteFile(path, []byte("DISK\nDISK\nDISK\n"), 0o600))

	buf := bufferOf(t, "foo\nbar\nbaz\n")
	h, _ := handlerForBufferTest(t, buf)

	// Cells reflect the buffer, not the diverged disk content.
	assert.Equal(t, []string{"foo", "bar", "baz"}, bufferCellLinesForTest(h.bufferCells()))

	// Mutate disk again without touching the buffer: lines are stable.
	require.NoError(t, os.WriteFile(path, []byte("OTHER\n"), 0o600))
	assert.Equal(t, []string{"foo", "bar", "baz"}, bufferCellLinesForTest(h.bufferCells()))

	// Edit the buffer: the version advances and lines now follow it.
	beforeV := buf.Version()
	buf.Edit(context.Background(),
		term.Coordinates{X: 0, Y: 1}, term.Coordinates{X: 3, Y: 1}, "BAR")
	require.NotEqual(t, beforeV, buf.Version(),
		"editing the buffer must advance its Version")
	assert.Equal(t, []string{"foo", "BAR", "baz"}, bufferCellLinesForTest(h.bufferCells()))
}

func TestBufferCellsSnapshotStableBetweenEdits(t *testing.T) {
	t.Parallel()

	buf := bufferOf(t, "foo\nbar\n")
	h, _ := handlerForBufferTest(t, buf)

	first := h.bufferCells()
	again := h.bufferCells()
	// No edit -> same backing slice (no re-split, no re-store).
	assert.Equal(t, &first[0][0], &again[0][0],
		"unchanged buffer must reuse the cached snapshot")

	buf.Edit(context.Background(),
		term.Coordinates{X: 0, Y: 0}, term.Coordinates{X: 3, Y: 0}, "FOO")
	updated := h.bufferCells()
	assert.Equal(t, []string{"FOO", "bar"}, bufferCellLinesForTest(updated))
}

func TestBufferCellsReuseRetiredProbeSnapshot(t *testing.T) {
	t.Parallel()

	buf := bufferOf(t, "foo\nbar\n")
	h, comp := handlerForBufferTest(t, buf)
	comp.cells = gutterGrid([]string{" 1 foo", " 2 bar"}, 30)
	comp.cursor = term.Coordinates{X: 3, Y: 0}

	h.refreshProbe()
	firstProbe := h.lastProbe.Load()
	require.NotNil(t, firstProbe)
	firstPublished := firstProbe.FileLines

	comp.cells = gutterGrid([]string{" 1 FOO", " 2 bar"}, 30)
	buf.Edit(context.Background(),
		term.Coordinates{X: 0, Y: 0}, term.Coordinates{X: 3, Y: 0}, "FOO")
	secondProbe := h.lastProbe.Load()
	require.NotNil(t, secondProbe)
	secondPublished := secondProbe.FileLines

	comp.cells = gutterGrid([]string{" 1 FOO", " 2 BAR"}, 30)
	buf.Edit(context.Background(),
		term.Coordinates{X: 0, Y: 1}, term.Coordinates{X: 3, Y: 1}, "BAR")
	thirdProbe := h.lastProbe.Load()
	require.NotNil(t, thirdProbe)

	assert.Equal(t, []string{"FOO", "BAR"}, bufferCellLinesForTest(firstPublished),
		"the retired first snapshot storage may be recycled")
	assert.Equal(t, []string{"FOO", "bar"}, bufferCellLinesForTest(secondPublished),
		"the second snapshot stays immutable while it is current")
	assert.True(t, cellMatricesAlias(firstPublished, thirdProbe.FileLines),
		"retired probe cell storage should be recycled for later snapshots")
	assert.False(t, cellMatricesAlias(secondPublished, thirdProbe.FileLines),
		"currently published probe cells must not be overwritten")
}

func TestRefreshProbeFollowsBufferAcrossDiskMutation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "code.go")
	require.NoError(t, os.WriteFile(path, []byte("DISK\nDISK\nDISK\n"), 0o600))

	buf := bufferOf(t, "foo\nbar\nbaz\n")
	h, comp := handlerForBufferTest(t, buf)

	// Rendered gutter view of the buffer; cursor on the body of line 2.
	comp.cells = gutterGrid([]string{" 1 foo", " 2 bar", " 3 baz"}, 30)
	comp.cursor = term.Coordinates{X: 3, Y: 1}

	h.refreshProbe()
	got := h.lastProbe.Load()
	require.NotNil(t, got)
	assert.Equal(t, term.Coordinates{X: 0, Y: 1}, got.CursorAtScroll)

	// Mutate disk out-of-band; the buffer mirror is untouched, so a
	// refresh against the same grid yields the same result.
	require.NoError(t, os.WriteFile(path, []byte("x\ny\n"), 0o600))
	h.refreshProbe()
	got2 := h.lastProbe.Load()
	require.NotNil(t, got2)
	assert.Equal(t, term.Coordinates{X: 0, Y: 1}, got2.CursorAtScroll,
		"disk mutation must not affect inference driven by the buffer")

	// Reload the buffer (the watcher's OnDidEdit) with a matching grid
	// update. The edit alone must refresh lastProbe — no extra
	// refreshProbe call — so the overlay follows the buffer.
	comp.cells = gutterGrid([]string{" 1 foo", " 2 BAR", " 3 baz"}, 30)
	comp.cursor = term.Coordinates{X: 5, Y: 1}
	buf.Edit(context.Background(),
		term.Coordinates{X: 0, Y: 1}, term.Coordinates{X: 3, Y: 1}, "BAR")
	got3 := h.lastProbe.Load()
	require.NotNil(t, got3)
	// Body offset 5-3 = 2 -> rune index 2 = 'R' on "BAR" (0-based).
	assert.Equal(t, term.Coordinates{X: 2, Y: 1}, got3.CursorAtScroll,
		"reload must refresh the cached probe without an extra interrupt")
}

func TestRefreshProbeKeepsLastResultOnSnapshotError(t *testing.T) {
	t.Parallel()

	buf := bufferOf(t, "foo\nbar\nbaz\n")
	h, comp := handlerForBufferTest(t, buf)

	comp.cells = gutterGrid([]string{" 1 foo", " 2 bar", " 3 baz"}, 30)
	comp.cursor = term.Coordinates{X: 3, Y: 1}
	h.refreshProbe()
	good := h.lastProbe.Load()
	require.NotNil(t, good)

	comp.err = errors.New("snapshot failed")
	// Bump the grid version so refreshProbe gets past its change guard
	// and actually attempts (and fails) the snapshot.
	comp.version++
	h.refreshProbe()
	assert.Same(t, good, h.lastProbe.Load(),
		"a snapshot error must keep the last good probe")
}

// highlightedRows returns the sorted, de-duplicated screen rows the
// overlay touched via UnionAttributes.
func highlightedRows(calls []writerCall) []int {
	seen := map[int]bool{}
	var rows []int
	for _, c := range calls {
		if !seen[c.pos.Y] {
			seen[c.pos.Y] = true
			rows = append(rows, c.pos.Y)
		}
	}
	sort.Ints(rows)
	return rows
}

func TestDrawOverlayFollowsScrolledGrid(t *testing.T) {
	t.Parallel()

	buf := bufferOf(t, "foo\nbar\nbaz\n")
	h, comp := handlerForBufferTest(t, buf)
	h.experimentalHighlights = true
	h.locations = locationStoreForTest(t)
	h.scheduleNextTick = func(fn func()) bool { fn(); return true }

	// Highlight the whole of file line 2 ("bar").
	h.locations.SetLocationList(0, "loc", sliceList([]textapi.Location{{
		From: term.Coordinates{X: 0, Y: 1},
		To:   term.Coordinates{X: 3, Y: 1},
		Attr: term.Attributes{Attrs: term.AttrUnderline},
	}}))

	// First frame: line 2 is rendered on screen row 1.
	comp.cells = gutterGrid([]string{" 1 foo", " 2 bar", " 3 baz"}, 30)
	comp.cursor = term.Coordinates{X: 3, Y: 1}
	w1 := &recordingWriter{}
	h.Draw(w1)
	require.Equal(t, []int{1}, highlightedRows(w1.calls),
		"line 2 highlight must land on its screen row")

	// The embedded editor scrolls up one line: line 2 is now on screen
	// row 0. The parser advancing bumps the grid version.
	comp.cells = gutterGrid([]string{" 2 bar", " 3 baz", " 4 qux"}, 30)
	comp.cursor = term.Coordinates{X: 3, Y: 0}
	comp.version++
	w2 := &recordingWriter{}
	h.Draw(w2)
	assert.Equal(t, []int{0}, highlightedRows(w2.calls),
		"after scroll the highlight must follow line 2 to its new row, "+
			"not stay on the row from the previous grid")
}

// BenchmarkRefreshProbe measures the steady-state cost of one
// refreshProbe on the exo handler: a component snapshot, the buffer-cell
// load, and the vteprobe alignment over a realistic gutter-rendered
// screen. The fixture is built once outside the timing loop and the
// probe slab is reused across calls, mirroring how refreshProbe runs on
// every grid mutation.
func BenchmarkRefreshProbe(b *testing.B) {
	const lines = 48

	var content strings.Builder
	rows := make([]string, 0, lines)
	for i := range lines {
		body := fmt.Sprintf("x%d := compute(%d) + offset", i, i)
		fmt.Fprintf(&content, "%s\n", body)
		rows = append(rows, fmt.Sprintf(" %d %s", i+1, body))
	}

	buf := bufferOf(b, content.String())
	h, comp := handlerForBufferTest(b, buf)
	comp.cells = gutterGrid(rows, 80)
	comp.cursor = term.Coordinates{X: 6, Y: lines / 2}

	h.refreshProbe()
	if h.lastProbe.Load() == nil {
		b.Fatal("warmup refreshProbe did not produce a result")
	}

	b.ReportAllocs()
	for b.Loop() {
		h.refreshProbe()
	}
}

// BenchmarkSnapshotBufferCells measures the per-edit cost of
// recomputing the cell snapshot, which runs on the buffer-owning
// goroutine on every edit.
func BenchmarkSnapshotBufferCells(b *testing.B) {
	const lines = 48

	var content strings.Builder
	for i := range lines {
		fmt.Fprintf(&content, "x%d := compute(%d) + offset\n", i, i)
	}
	buf := bufferOf(b, content.String())
	h, _ := handlerForBufferTest(b, buf)

	b.ReportAllocs()
	for b.Loop() {
		h.snapshotBufferCells()
	}
}

func TestSnapshotFileCellsMatchesViewSplit(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{"trailing newline", "foo\nbar\nbaz\n", []string{"foo", "bar", "baz"}},
		{"no trailing newline", "foo\nbar", []string{"foo", "bar"}},
		{"single line newline", "foo\n", []string{"foo"}},
		{"interior blank", "foo\n\nbar\n", []string{"foo", "", "bar"}},
		{"trailing blank line", "a\n\n", []string{"a", ""}},
		{"empty", "", nil},
		{"lone newline", "\n", []string{""}},
		{"single char", "x", []string{"x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			buf := bufferOf(t, tc.content)
			assert.Equal(t, tc.want, bufferCellLinesForTest(snapshotFileCells(buf.View().RawCells())))
		})
	}
}
