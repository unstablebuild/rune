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

package locationpicker

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler/handlertest"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
)

func TestPickerRender(t *testing.T) {
	uriA := parseURI(t, "file:///a.go")
	entries := []Entry{
		{URI: uriA, Display: "a.go:1 Foo", Range: semanticapi.Range{Start: semanticapi.Position{Line: 0}}},
		{URI: uriA, Display: "a.go:3 Bar", Range: semanticapi.Range{Start: semanticapi.Position{Line: 2}}},
	}
	fc := fileContent{"/a.go": "line0\nline1\nline2"}
	p := testPicker(entries, fc)

	runPickerSequence(t, p, 24, 8, []handlertest.SequenceTestCase{
		{InputSequence: "", Expected: " line0                  \n" +
			" line1                  \n" +
			" line2                  \n" +
			"                        \n" +
			"                        \n" +
			"                        \n" +
			" ────────────────────── \n" +
			" a.go:1 Foo             "},
	})
}

func TestPickerNavPreview(t *testing.T) {
	uriA := parseURI(t, "file:///a.go")
	uriB := parseURI(t, "file:///b.go")
	entries := []Entry{
		{URI: uriA, Display: "a.go:1 Foo", Range: semanticapi.Range{Start: semanticapi.Position{Line: 0}}},
		{URI: uriB, Display: "b.go:2 Bar", Range: semanticapi.Range{Start: semanticapi.Position{Line: 1}}},
	}
	fc := fileContent{
		"/a.go": "aline0\naline1\naline2",
		"/b.go": "bline0\nbline1\nbline2",
	}
	p := testPicker(entries, fc)
	runPickerSequence(t, p, 24, 8, []handlertest.SequenceTestCase{
		{InputSequence: "", Expected: " aline0                 \n" +
			" aline1                 \n" +
			" aline2                 \n" +
			"                        \n" +
			"                        \n" +
			"                        \n" +
			" ────────────────────── \n" +
			" a.go:1 Foo             "},
		{InputSequence: "<c-j>", Expected: " bline0                 \n" +
			" bline1                 \n" +
			" bline2                 \n" +
			"                        \n" +
			"                        \n" +
			"                        \n" +
			" ────────────────────── \n" +
			" b.go:2 Bar             "},
	})
}

func TestPickerHandle(t *testing.T) {
	tests := []struct {
		name        string
		event       term.Event
		wantExit    bool
		wantHandled bool
	}{
		{"esc exits", term.Event{Type: term.EventKey, Key: term.KeyEsc}, true, true},
		{"enter exits", term.Event{Type: term.EventKey, Key: term.KeyEnter}, true, true},
		{"up handled", term.Event{Type: term.EventKey, Key: term.KeyArrowUp}, false, true},
		{"down handled", term.Event{Type: term.EventKey, Key: term.KeyArrowDown}, false, true},
		{"non-key ignored", term.Event{Type: term.EventMouse}, false, false},
		{"unknown key ignored", term.Event{Type: term.EventKey, Key: term.KeyTab}, false, false},
		{"ctrl-j handled", term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'j'}, false, true},
		{"ctrl-k handled", term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'k'}, false, true},
		{"ctrl-n handled", term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'n'}, false, true},
		{"ctrl-p handled", term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'p'}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := []Entry{{Display: "a.go:1"}, {Display: "b.go:2"}}
			p := testPicker(entries, nil)
			exit, handled := p.Handle(tt.event)
			assert.Equal(t, tt.wantExit, exit)
			assert.Equal(t, tt.wantHandled, handled)
		})
	}
}

func TestPickerDraw(t *testing.T) {
	uriA := parseURI(t, "file:///a.go")
	entries := []Entry{
		{URI: uriA, Display: "a.go:2 Foo", Range: semanticapi.Range{Start: semanticapi.Position{Line: 1}}},
	}
	fc := fileContent{"/a.go": "line0\nline1\nline2"}
	p := testPicker(entries, fc)
	w, ht := p.Dimensions()
	sw := renderPicker(t, p, w, ht)

	rendered := sw.Cells()
	leftPad := spanHPad / 2
	cell := rendered[1*w+leftPad]
	assert.Equal(t, term.ColorYellow, cell.Bg, "target line should have preview bg")
	assert.NotEqual(t, term.ColorYellow, rendered[leftPad].Bg, "non-target line should not have preview bg")
}

func TestPickerDrawPreviewReverseRange(t *testing.T) {
	uriA := parseURI(t, "file:///a.go")
	entries := []Entry{
		{
			URI: uriA, Display: "a.go:1 Foo",
			Range: semanticapi.Range{
				Start: semanticapi.Position{Line: 0, Character: 2},
				End:   semanticapi.Position{Line: 0, Character: 5},
			},
		},
	}
	fc := fileContent{"/a.go": "0123456789"}
	p := testPicker(entries, fc)
	w, ht := p.Dimensions()
	sw := renderPicker(t, p, w, ht)

	rendered := sw.Cells()
	leftPad := spanHPad / 2
	assert.Zero(t, rendered[leftPad+1].Attrs&term.AttrReverse, "char before range should not have AttrReverse")
	assert.NotZero(t, rendered[leftPad+2].Attrs&term.AttrReverse, "char in range should have AttrReverse")
	assert.NotZero(t, rendered[leftPad+4].Attrs&term.AttrReverse, "char in range should have AttrReverse")
	assert.Zero(t, rendered[leftPad+5].Attrs&term.AttrReverse, "char after range should not have AttrReverse")
}

func TestPickerDrawPreviewClampsTargetLine(t *testing.T) {
	uriA := parseURI(t, "file:///a.go")
	entries := []Entry{
		{URI: uriA, Display: "a.go:11 Oob", Range: semanticapi.Range{Start: semanticapi.Position{Line: 10}}},
	}
	fc := fileContent{"/a.go": "line0\nline1"}
	p := testPicker(entries, fc)
	w, ht := p.Dimensions()
	sw := renderPicker(t, p, w, ht)

	rendered := sw.Cells()
	leftPad := spanHPad / 2
	cell := rendered[1*w+leftPad]
	assert.Equal(t, term.ColorYellow, cell.Bg, "clamped target line should have preview bg")
}

func TestPickerDimensions(t *testing.T) {
	entries := []Entry{{Display: "a.go:1"}}
	p := New(entries, stubWindowManager{}, &stubFS{}, syncTick, nil, Config{}, nil)
	w, ht := p.Dimensions()
	assert.Equal(t, minPreviewWidth+spanHPad, w)
	assert.Equal(t, 1+previewContextLines+separatorHeight+spanVPad, ht)
}

func TestPickerDimensionsWideEntries(t *testing.T) {
	long := strings.Repeat("x", minPreviewWidth+20)
	entries := []Entry{{Display: long}}
	p := New(entries, stubWindowManager{}, &stubFS{}, syncTick, nil, Config{}, nil)
	w, ht := p.Dimensions()
	assert.Equal(t, utf8.RuneCountInString(long)+spanHPad, w)
	assert.Equal(t, 1+previewContextLines+separatorHeight+spanVPad, ht)
}

func TestPickerResize(t *testing.T) {
	entries := []Entry{{Display: "a.go:1"}}
	p := New(entries, stubWindowManager{}, &stubFS{}, syncTick, nil, Config{}, nil)

	w, ht := p.Dimensions()
	assert.Equal(t, minPreviewWidth+spanHPad, w)
	assert.Equal(t, 1+previewContextLines+separatorHeight+spanVPad, ht)

	p.Resize(30, 10)
	assert.Equal(t, 30-spanHPad, p.innerW)
	assert.Equal(t, 10-spanVPad, p.innerH)

	w, ht = p.Dimensions()
	assert.Equal(t, minPreviewWidth+spanHPad, w)
	assert.Equal(t, 1+previewContextLines+separatorHeight+spanVPad, ht)
}

func TestPickerCursorSelection(t *testing.T) {
	entries := []Entry{{Display: "a.go:1 Foo"}}
	p := testPicker(entries, nil)

	_, _, visible := p.Cursor()
	assert.False(t, visible)

	sel, hasSel := p.Selection()
	assert.True(t, hasSel)
	assert.Equal(t, "a.go:1 Foo", sel)
}

func TestPickerEnterCallsOnSelect(t *testing.T) {
	uriA := parseURI(t, "file:///a.go")
	entries := []Entry{{URI: uriA, Display: "a.go:1"}}
	p := testPicker(entries, nil)

	var selectedIdx int
	p.SetOnSelect(func(idx int) { selectedIdx = idx })

	exit, handled := p.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, exit)
	assert.True(t, handled)
	assert.Equal(t, 0, selectedIdx)
}

func TestPickerHighlightApplied(t *testing.T) {
	uriA := parseURI(t, "file:///a.go")
	entries := []Entry{
		{URI: uriA, Display: "a.go:2 Foo", Range: semanticapi.Range{Start: semanticapi.Position{Line: 1}}},
	}
	fc := fileContent{"/a.go": "func main() {}\nfoo bar baz"}
	p := testPicker(entries, fc)

	for x := 0; x < 4 && x < len(p.previewCells[0]); x++ {
		p.previewCells[0][x].SetAttributes(term.Attributes{Fg: term.ColorGreen})
	}

	w, ht := p.Dimensions()
	sw := renderPicker(t, p, w, ht)

	rendered := sw.Cells()
	leftPad := spanHPad / 2
	assert.Equal(t, term.ColorGreen, rendered[leftPad+0].Fg, "highlighted char should have green fg")
	assert.NotEqual(t, term.ColorGreen, rendered[leftPad+5].Fg, "non-highlighted char should not have green fg")
}

func TestPickerHighlightUnionWithTargetLine(t *testing.T) {
	uriA := parseURI(t, "file:///a.go")
	entries := []Entry{
		{
			URI: uriA, Display: "a.go:1 Foo",
			Range: semanticapi.Range{
				Start: semanticapi.Position{Line: 0, Character: 1},
				End:   semanticapi.Position{Line: 0, Character: 3},
			},
		},
	}
	fc := fileContent{"/a.go": "func main"}
	p := testPicker(entries, fc)

	for x := 0; x < 4 && x < len(p.previewCells[0]); x++ {
		p.previewCells[0][x].SetAttributes(term.Attributes{Fg: term.ColorRed})
	}

	w, ht := p.Dimensions()
	sw := renderPicker(t, p, w, ht)

	rendered := sw.Cells()
	leftPad := spanHPad / 2
	cell0 := rendered[leftPad+0]
	assert.Equal(t, term.ColorYellow, cell0.Bg, "target line cell should have preview bg")
	assert.Equal(t, term.ColorRed, cell0.Fg, "target line cell should keep syntax fg")
	assert.Zero(t, cell0.Attrs&term.AttrReverse, "cell outside reference range should not have reverse")

	cell2 := rendered[leftPad+2]
	assert.Equal(t, term.ColorYellow, cell2.Bg, "ref range cell should have preview bg")
	assert.Equal(t, term.ColorRed, cell2.Fg, "ref range cell should keep syntax fg")
	assert.NotZero(t, cell2.Attrs&term.AttrReverse, "ref range cell should have reverse")
}

func TestPickerHighlightMultipleRangesPerLine(t *testing.T) {
	uriA := parseURI(t, "file:///a.go")
	entries := []Entry{
		{URI: uriA, Display: "a.go:2 x", Range: semanticapi.Range{Start: semanticapi.Position{Line: 1}}},
	}
	fc := fileContent{"/a.go": "func main\nreturn"}
	p := testPicker(entries, fc)

	for x := 0; x < 4 && x < len(p.previewCells[0]); x++ {
		p.previewCells[0][x].SetAttributes(term.Attributes{Fg: term.ColorBlue})
	}
	for x := 5; x < 9 && x < len(p.previewCells[0]); x++ {
		p.previewCells[0][x].SetAttributes(term.Attributes{Fg: term.ColorRed})
	}

	w, ht := p.Dimensions()
	sw := renderPicker(t, p, w, ht)

	rendered := sw.Cells()
	leftPad := spanHPad / 2
	assert.Equal(t, term.ColorBlue, rendered[leftPad+0].Fg, "first range should be blue")
	assert.Equal(t, term.ColorRed, rendered[leftPad+5].Fg, "second range should be red")
}

func TestPickerNilParserNoHighlights(t *testing.T) {
	uriA := parseURI(t, "file:///a.go")
	entries := []Entry{
		{URI: uriA, Display: "a.go:1 Foo", Range: semanticapi.Range{Start: semanticapi.Position{Line: 0}}},
	}
	fc := fileContent{"/a.go": "line0\nline1"}
	p := testPicker(entries, fc)

	require.NotNil(t, p.previewCells, "previewCells should be set from file content")

	w, ht := p.Dimensions()
	sw := renderPicker(t, p, w, ht)
	assert.Contains(t, sw.String(), "line0")
}

func TestPickerLoadHighlights(t *testing.T) {
	uriA := parseURI(t, "file:///a.go")
	entries := []Entry{
		{URI: uriA, Display: "a.go:1 Foo", Range: semanticapi.Range{Start: semanticapi.Position{Line: 0}}},
	}
	fc := fileContent{"/a.go": "line0\nline1\nline2"}
	p := testPicker(entries, fc)

	parser := &stubParser{
		highlightFn: func(_ workspaceapi.URI, _ string) (iterator.Iterator[textapi.Location], error) {
			return iterator.FromSlice([]textapi.Location{
				{
					From: term.Coordinates{X: 0, Y: 0},
					To:   term.Coordinates{X: 5, Y: 0},
					Attr: term.Attributes{Fg: term.ColorGreen},
				},
				{
					From: term.Coordinates{X: 0, Y: 1},
					To:   term.Coordinates{X: 5, Y: 1},
					Attr: term.Attributes{Fg: term.ColorBlue},
				},
			}), nil
		},
	}
	p.parser = parser
	content := "line0\nline1\nline2"
	baseCells := term.CloneCells(p.previewCells)
	p.loadHighlights(uriA, content, baseCells)

	require.NotNil(t, p.previewCells)
	require.Len(t, p.previewCells, 3)
	for x := 0; x < 5; x++ {
		assert.Equal(t, term.ColorGreen, p.previewCells[0][x].Fg, "line 0 char %d", x)
	}
	for x := 0; x < 5; x++ {
		assert.Equal(t, term.ColorBlue, p.previewCells[1][x].Fg, "line 1 char %d", x)
	}
	for _, c := range p.previewCells[2] {
		assert.Equal(t, term.ColorDefault, c.Fg, "line 2 should have default fg")
	}
}

func TestPickerLoadHighlightsErrorIgnored(t *testing.T) {
	uriA := parseURI(t, "file:///a.go")
	entries := []Entry{
		{URI: uriA, Display: "a.go:1 Foo", Range: semanticapi.Range{Start: semanticapi.Position{Line: 0}}},
	}
	fc := fileContent{"/a.go": "line0"}
	p := testPicker(entries, fc)

	before := term.CloneCells(p.previewCells)

	parser := &stubParser{
		highlightFn: func(_ workspaceapi.URI, _ string) (iterator.Iterator[textapi.Location], error) {
			return nil, errors.New("highlight unavailable")
		},
	}
	p.parser = parser
	baseCells := term.CloneCells(p.previewCells)
	p.loadHighlights(uriA, "line0", baseCells)

	assert.Equal(t, before, p.previewCells, "previewCells should be unchanged when parser returns error")
}

func TestPickerLoadHighlightsOutOfBoundsIgnored(t *testing.T) {
	uriA := parseURI(t, "file:///a.go")
	entries := []Entry{
		{URI: uriA, Display: "a.go:1 Foo", Range: semanticapi.Range{Start: semanticapi.Position{Line: 0}}},
	}
	fc := fileContent{"/a.go": "line0"}
	p := testPicker(entries, fc)

	before := term.CloneCells(p.previewCells)

	parser := &stubParser{
		highlightFn: func(_ workspaceapi.URI, _ string) (iterator.Iterator[textapi.Location], error) {
			return iterator.FromSlice([]textapi.Location{
				{
					From: term.Coordinates{X: 0, Y: 99},
					To:   term.Coordinates{X: 5, Y: 99},
					Attr: term.Attributes{Fg: term.ColorGreen},
				},
			}), nil
		},
	}
	p.parser = parser
	baseCells := term.CloneCells(p.previewCells)
	p.loadHighlights(uriA, "line0", baseCells)

	require.NotNil(t, p.previewCells)
	for i, row := range p.previewCells {
		for j, c := range row {
			assert.Equal(t, before[i][j].Attributes(), c.Attributes(),
				"cell [%d][%d] attrs should be unchanged for out-of-bounds highlight", i, j)
		}
	}
}

var _ browserapi.Floating = (*Picker)(nil)

type fileContent map[string]string

type stubWindowManager struct{}

func (stubWindowManager) Focus() (browserapi.Window, error) { return nil, nil }
func (stubWindowManager) Split(browserapi.Orientation, browserapi.Window, browserapi.Handler) (browserapi.Window, error) {
	return nil, nil
}
func (stubWindowManager) Floating(browserapi.Floating, browserapi.FloatingConfig) (browserapi.Window, error) {
	return nil, nil
}
func (stubWindowManager) Bar(browserapi.BarConfig, tui.Handler) error { return nil }
func (stubWindowManager) Tab(workspaceapi.URI, rune, string, browserapi.Handler) (browserapi.Handler, error) {
	return nil, nil
}
func (stubWindowManager) SetWindowContent(browserapi.Window, browserapi.Handler) error { return nil }
func (stubWindowManager) CloseWindow(browserapi.Window) error                          { return nil }
func (stubWindowManager) SetTabName(workspaceapi.URI, string) error                    { return nil }

func (stubWindowManager) SetTabActivity(workspaceapi.URI, bool) error { return nil }

type stubFS struct {
	openFileFn func(string, int, os.FileMode) (workspaceapi.File, error)
}

func (s *stubFS) URI(path string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("file://" + path)
}
func (s *stubFS) OpenFile(path string, flag int, mode os.FileMode) (workspaceapi.File, error) {
	if s.openFileFn != nil {
		return s.openFileFn(path, flag, mode)
	}
	return nil, os.ErrNotExist
}
func (s *stubFS) Remove(string) error                   { return nil }
func (s *stubFS) Stat(string) (os.FileInfo, error)      { return nil, os.ErrNotExist }
func (s *stubFS) ReadDir(string) ([]os.DirEntry, error) { return nil, nil }
func (s *stubFS) MkdirAll(string, os.FileMode) error    { return nil }

func testFS(content fileContent) *stubFS {
	return &stubFS{
		openFileFn: func(path string, _ int, _ os.FileMode) (workspaceapi.File, error) {
			text, ok := content[path]
			if !ok {
				return nil, os.ErrNotExist
			}
			return newStringFile(text), nil
		},
	}
}

type stringFile struct{ *strings.Reader }

func newStringFile(s string) *stringFile         { return &stringFile{strings.NewReader(s)} }
func (f *stringFile) Close() error               { return nil }
func (f *stringFile) Name() string               { return "" }
func (f *stringFile) Stat() (os.FileInfo, error) { return nil, nil }
func (f *stringFile) Sync() error                { return nil }
func (f *stringFile) Truncate(int64) error       { return nil }
func (f *stringFile) Fd() uintptr                { return 0 }
func (f *stringFile) Write([]byte) (int, error)  { return 0, os.ErrPermission }
func (f *stringFile) WriteAt([]byte, int64) (int, error) {
	return 0, os.ErrPermission
}

type stubParser struct {
	highlightFn func(workspaceapi.URI, string) (iterator.Iterator[textapi.Location], error)
}

func (p *stubParser) Search(string, []string, ...string) (iterator.Iterator[syntaxapi.Result], error) {
	return iterator.Empty[syntaxapi.Result](), nil
}
func (p *stubParser) ResolveSymbol(context.Context, string, syntaxapi.Progress) (
	iterator.Iterator[syntaxapi.Match], error,
) {
	return iterator.Empty[syntaxapi.Match](), nil
}
func (p *stubParser) ListReferencedSymbols(context.Context) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}
func (p *stubParser) SearchNode(syntaxapi.NodeCaptureName, ...string) (iterator.Iterator[syntaxapi.Result], error) {
	return iterator.Empty[syntaxapi.Result](), nil
}
func (p *stubParser) Query(workspaceapi.URI, string, []string) (iterator.Iterator[syntaxapi.Result], error) {
	return iterator.Empty[syntaxapi.Result](), nil
}
func (p *stubParser) QueryNode(workspaceapi.URI, syntaxapi.NodeCaptureName) (iterator.Iterator[syntaxapi.Result], error) {
	return iterator.Empty[syntaxapi.Result](), nil
}
func (p *stubParser) Highlight(uri workspaceapi.URI, content string) (iterator.Iterator[textapi.Location], error) {
	if p.highlightFn != nil {
		return p.highlightFn(uri, content)
	}
	return iterator.Empty[textapi.Location](), nil
}

var syncTick = func(fn func()) bool { fn(); return true }

func parseURI(t *testing.T, raw string) workspaceapi.URI {
	t.Helper()
	uri, err := workspaceapi.ParseURI(raw)
	require.NoError(t, err)
	return uri
}

func testPicker(entries []Entry, fc fileContent) *Picker {
	cfg := Config{PreviewAttr: term.Attributes{Bg: term.ColorYellow}}
	return New(entries, stubWindowManager{}, testFS(fc), syncTick, nil, cfg, nil)
}

func renderPicker(t *testing.T, p *Picker, w, ht int) *term.StringWriter {
	t.Helper()
	p.Resize(w, ht)
	expected := handlertest.DrawHandler(p, w, ht)
	cases := []handlertest.SequenceTestCase{{InputSequence: "", Expected: expected}}
	handlertest.RunHandlerSequence(t, p, w, ht, cases)
	writer := term.NewStringWriter(w, ht)
	handlertest.RunHandlerSequenceWriter(t, writer, p, w, ht, cases)
	return writer
}

func runPickerSequence(
	t *testing.T, p *Picker, w, ht int, cases []handlertest.SequenceTestCase,
) {
	t.Helper()
	handlertest.RunHandlerSequence(t, p, w, ht, cases)
}
