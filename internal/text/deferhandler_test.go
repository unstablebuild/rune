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

package text

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/text/streamload"
)

// deferFSReader is a minimal walkdir.Reader rooted at a real
// directory on disk. Kept local to this file so the deferred-handler
// tests do not depend on streamload's test fixtures.
type deferFSReader struct{ root string }

func (r *deferFSReader) URI(p string) (workspaceapi.URI, error) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(r.root, p)
	}
	return workspaceapi.ParseURI("file://" + p)
}

func (r *deferFSReader) OpenFile(p string, flag int, perm os.FileMode) (workspaceapi.File, error) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(r.root, p)
	}
	return os.OpenFile(p, flag, perm)
}

func (r *deferFSReader) Stat(p string) (os.FileInfo, error) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(r.root, p)
	}
	return os.Stat(p)
}

func (r *deferFSReader) ReadDir(p string) ([]os.DirEntry, error) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(r.root, p)
	}
	return os.ReadDir(p)
}

func newDeferStreamload(t *testing.T) *streamload.Handler {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	var b strings.Builder
	for i := range 8 {
		fmt.Fprintf(&b, "line%d\n", i)
	}
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o644))
	uri, err := workspaceapi.ParseURI("file://" + path)
	require.NoError(t, err)
	h, err := streamload.New(&deferFSReader{root: dir}, uri, streamload.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = h.Close() })
	return h
}

// deferFakeReal is a minimal text.Handler that records each mutating
// call in the order received. Reads return zero values unless the
// test populates a field.
type deferFakeReal struct {
	uri workspaceapi.URI

	calls []string

	cursor       term.Coordinates
	cursorSetOK  bool
	wrap         bool
	showCmdBar   bool
	defaultAttrs term.Attributes
	locLists     []deferRecordedLocList

	view   cell.View
	editor cell.Editor

	dimW, dimH int

	searchMode bool

	moveNextRet bool
	movePrevRet bool
}

type deferRecordedLocList struct {
	priority textapi.LocationPriority
	id       string
	list     LocationList
}

var _ Handler = (*deferFakeReal)(nil)

func newDeferFakeReal() *deferFakeReal {
	return &deferFakeReal{
		view:        cell.NewBuffer().View(),
		editor:      deferNoopCellEditor{},
		cursorSetOK: true,
	}
}

func (f *deferFakeReal) Resource() workspaceapi.URI { return f.uri }

func (f *deferFakeReal) SetWrap(wrap bool) {
	f.calls = append(f.calls, fmt.Sprintf("SetWrap(%v)", wrap))
	f.wrap = wrap
}

func (f *deferFakeReal) ShowCommandBar(show bool) {
	f.calls = append(f.calls, fmt.Sprintf("ShowCommandBar(%v)", show))
	f.showCmdBar = show
}

func (f *deferFakeReal) SetCursorAtScroll(pos term.Coordinates) bool {
	f.calls = append(f.calls, fmt.Sprintf("SetCursorAtScroll(%d,%d)", pos.X, pos.Y))
	f.cursor = pos
	return f.cursorSetOK
}

func (f *deferFakeReal) CursorAtScroll() term.Coordinates { return f.cursor }

func (f *deferFakeReal) SetLocationList(
	pri textapi.LocationPriority, id string, l LocationList,
) {
	f.calls = append(f.calls, fmt.Sprintf("SetLocationList(%d,%s)", pri, id))
	f.locLists = append(f.locLists, deferRecordedLocList{priority: pri, id: id, list: l})
}

func (f *deferFakeReal) LocationLists() []LocationSet { return nil }

func (f *deferFakeReal) MoveToNextLocation(string) bool { return f.moveNextRet }
func (f *deferFakeReal) MoveToPrevLocation(string) bool { return f.movePrevRet }

func (f *deferFakeReal) CellView() cell.View     { return f.view }
func (f *deferFakeReal) CellEditor() cell.Editor { return f.editor }

func (f *deferFakeReal) SetDefaultAttributes(a term.Attributes) {
	f.calls = append(f.calls, "SetDefaultAttributes")
	f.defaultAttrs = a
}

func (f *deferFakeReal) Dimensions() (width, height int) { return f.dimW, f.dimH }
func (f *deferFakeReal) IsSearchMode() bool              { return f.searchMode }
func (f *deferFakeReal) IsNormalMode() bool              { return false }

func (f *deferFakeReal) Close() error { return nil }
func (f *deferFakeReal) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return f.cursor, term.CursorStyleDefault, false
}
func (f *deferFakeReal) Selection() (string, bool)      { return "", false }
func (f *deferFakeReal) Handle(term.Event) (bool, bool) { return false, false }
func (f *deferFakeReal) Resize(int, int)                {}
func (f *deferFakeReal) Draw(term.Writer)               {}
func (f *deferFakeReal) SeekUp() bool                   { return false }
func (f *deferFakeReal) SeekDown() bool                 { return false }
func (f *deferFakeReal) SeekOffset() int                { return 0 }
func (f *deferFakeReal) MaxSeekOffset() int             { return 0 }

// deferEmptyList satisfies LocationList with zero entries.
type deferEmptyList struct{}

func (deferEmptyList) Current() (textapi.Location, bool) { return textapi.Location{}, false }
func (deferEmptyList) Next() (textapi.Location, bool)    { return textapi.Location{}, false }
func (deferEmptyList) Prev() (textapi.Location, bool)    { return textapi.Location{}, false }

func TestDeferHandlerSetCursorAtScrollQueuedUntilSwap(t *testing.T) {
	sh := newDeferStreamload(t)
	d := newDeferHandler(sh)

	ok := d.SetCursorAtScroll(term.Coordinates{X: 3, Y: 5})
	assert.True(t, ok,
		"pre-swap SetCursorAtScroll must report success so callers do not fall back")

	real := newDeferFakeReal()
	d.Swap(real)
	assert.Equal(t, []string{"SetCursorAtScroll(3,5)"}, real.calls)
	assert.Equal(t, term.Coordinates{X: 3, Y: 5}, real.cursor)
}

func TestDeferHandlerLastSetCursorWinsBeforeSwap(t *testing.T) {
	sh := newDeferStreamload(t)
	d := newDeferHandler(sh)

	d.SetCursorAtScroll(term.Coordinates{X: 1, Y: 1})
	d.SetCursorAtScroll(term.Coordinates{X: 2, Y: 2})
	d.SetCursorAtScroll(term.Coordinates{X: 7, Y: 9})

	real := newDeferFakeReal()
	d.Swap(real)
	assert.Equal(t, []string{"SetCursorAtScroll(7,9)"}, real.calls)
}

func TestDeferHandlerSetLocationListReplayedInOrder(t *testing.T) {
	sh := newDeferStreamload(t)
	d := newDeferHandler(sh)

	d.SetLocationList(textapi.LocationPriority(1), "lsp", deferEmptyList{})
	d.SetLocationList(textapi.LocationPriority(2), "syntax", deferEmptyList{})
	d.SetLocationList(textapi.LocationPriority(3), "search", deferEmptyList{})

	real := newDeferFakeReal()
	d.Swap(real)
	require.Len(t, real.locLists, 3)
	assert.Equal(t, "lsp", real.locLists[0].id)
	assert.Equal(t, "syntax", real.locLists[1].id)
	assert.Equal(t, "search", real.locLists[2].id)
}

func TestDeferHandlerSetDefaultAttributesReplayed(t *testing.T) {
	sh := newDeferStreamload(t)
	d := newDeferHandler(sh)

	d.SetDefaultAttributes(term.Attributes{})

	real := newDeferFakeReal()
	d.Swap(real)
	assert.Contains(t, real.calls, "SetDefaultAttributes")
}

func TestDeferHandlerSetWrapAndShowCommandBarReplayed(t *testing.T) {
	sh := newDeferStreamload(t)
	d := newDeferHandler(sh)

	d.SetWrap(true)
	d.ShowCommandBar(false)
	d.SetCursorAtScroll(term.Coordinates{X: 0, Y: 4})

	real := newDeferFakeReal()
	d.Swap(real)
	// Replay order: defaultAttrs -> wrap -> showCommandBar -> locationLists -> cursor.
	assert.Equal(t, []string{
		"SetWrap(true)",
		"ShowCommandBar(false)",
		"SetCursorAtScroll(0,4)",
	}, real.calls)
}

// deferRecordingEditor records every Edit so tests can assert whether
// pre-Swap edits reach the real handler.
type deferRecordingEditor struct {
	edits []string
}

func (e *deferRecordingEditor) Edit(
	_ context.Context, start, end term.Coordinates, str string,
) (from, to term.Coordinates, old string) {
	e.edits = append(e.edits,
		fmt.Sprintf("Edit(%d,%d-%d,%d,%q)", start.X, start.Y, end.X, end.Y, str))
	return start, end, ""
}

// TestDeferHandlerEditBeforeSwapReachesRealHandler reproduces the
// dropped-edit bug behind gopls go.mod vuln upgrades not landing: a
// server-driven workspace/applyEdit force-opens the closed file during
// its async streaming load, so the edit is applied through the
// deferHandler before Swap. Edits issued pre-Swap must be queued and
// replayed on the real handler; today CellEditor returns a no-op editor
// and they are silently dropped.
func TestDeferHandlerEditBeforeSwapReachesRealHandler(t *testing.T) {
	sh := newDeferStreamload(t)
	d := newDeferHandler(sh)

	_, _, _ = d.CellEditor().Edit(
		context.Background(),
		term.Coordinates{X: 12, Y: 4},
		term.Coordinates{X: 18, Y: 4},
		"v0.3.8",
	)

	real := newDeferFakeReal()
	rec := &deferRecordingEditor{}
	real.editor = rec
	d.Swap(real)

	assert.Equal(t,
		[]string{`Edit(12,4-18,4,"v0.3.8")`}, rec.edits,
		"pre-Swap edit must be replayed on the real handler, not dropped")
}

// TestDeferHandlerEditReplayedBeforeCursor pins the replay order:
// queued edits mutate the buffer before the cursor is restored, so the
// cursor lands on the post-edit content.
func TestDeferHandlerEditReplayedBeforeCursor(t *testing.T) {
	sh := newDeferStreamload(t)
	d := newDeferHandler(sh)

	_, _, _ = d.CellEditor().Edit(
		context.Background(),
		term.Coordinates{X: 0, Y: 0}, term.Coordinates{X: 0, Y: 0}, "x",
	)
	d.SetCursorAtScroll(term.Coordinates{X: 1, Y: 0})

	real := newDeferFakeReal()
	rec := &deferRecordingEditor{}
	real.editor = rec
	d.Swap(real)

	require.Len(t, rec.edits, 1)
	assert.Equal(t, []string{"SetCursorAtScroll(1,0)"}, real.calls)
}

func TestDeferHandlerReadsReturnSafeDefaultsBeforeSwap(t *testing.T) {
	sh := newDeferStreamload(t)
	sh.Resize(80, 24)
	d := newDeferHandler(sh)

	assert.Equal(t, term.Coordinates{}, d.CursorAtScroll(),
		"CursorAtScroll must return {0,0} when no pending cursor")
	assert.Nil(t, d.LocationLists())
	assert.False(t, d.MoveToNextLocation("anything"))
	assert.False(t, d.MoveToPrevLocation("anything"))
	view := d.CellView()
	require.NotNil(t, view)
	if view.Rows() > 0 {
		assert.Equal(t, 0, view.Columns(0))
	}
	editor := d.CellEditor()
	require.NotNil(t, editor)
	from, to, old := editor.Edit(
		context.Background(), term.Coordinates{}, term.Coordinates{}, "x")
	assert.Equal(t, term.Coordinates{}, from)
	assert.Equal(t, term.Coordinates{}, to)
	assert.Equal(t, "", old)

	w, h := d.Dimensions()
	assert.Equal(t, 80, w)
	assert.Equal(t, 24, h)
}

func TestDeferHandlerCursorAtScrollReturnsPendingBeforeSwap(t *testing.T) {
	sh := newDeferStreamload(t)
	d := newDeferHandler(sh)

	d.SetCursorAtScroll(term.Coordinates{X: 2, Y: 6})
	assert.Equal(t, term.Coordinates{X: 2, Y: 6}, d.CursorAtScroll())
}

func TestDeferHandlerResourceReportsStreamloadURI(t *testing.T) {
	sh := newDeferStreamload(t)
	d := newDeferHandler(sh)

	assert.Equal(t, sh.URI(), d.Resource())
}

func TestDeferHandlerForwardsBrowserapiToStreamload(t *testing.T) {
	sh := newDeferStreamload(t)
	d := newDeferHandler(sh)

	d.Resize(100, 40)
	w, h := d.Dimensions()
	assert.Equal(t, 100, w)
	assert.Equal(t, 40, h)

	assert.Equal(t, 0, d.SeekOffset())
}

func TestDeferHandlerPostSwapCallsForwardDirectly(t *testing.T) {
	sh := newDeferStreamload(t)
	d := newDeferHandler(sh)

	real := newDeferFakeReal()
	d.Swap(real)

	d.SetWrap(true)
	d.ShowCommandBar(true)
	ok := d.SetCursorAtScroll(term.Coordinates{X: 1, Y: 2})
	d.SetLocationList(textapi.LocationPriority(0), "post", deferEmptyList{})
	d.SetDefaultAttributes(term.Attributes{})

	assert.True(t, ok)
	assert.Equal(t, []string{
		"SetWrap(true)",
		"ShowCommandBar(true)",
		"SetCursorAtScroll(1,2)",
		"SetLocationList(0,post)",
		"SetDefaultAttributes",
	}, real.calls)
}

func TestDeferHandlerSwapNilPanics(t *testing.T) {
	sh := newDeferStreamload(t)
	d := newDeferHandler(sh)
	assert.Panics(t, func() { d.Swap(nil) })
}

func TestDeferHandlerNewNilPanics(t *testing.T) {
	assert.Panics(t, func() { _ = newDeferHandler(nil) })
}

func TestDeferHandlerIsSearchModeForwardsToStreamloadBeforeSwap(t *testing.T) {
	sh := newDeferStreamload(t)
	d := newDeferHandler(sh)

	assert.False(t, d.IsSearchMode(),
		"IsSearchMode must reflect the streamload handler before Swap")

	real := newDeferFakeReal()
	real.searchMode = true
	d.Swap(real)
	assert.True(t, d.IsSearchMode(),
		"IsSearchMode must reflect the real handler after Swap")
}
