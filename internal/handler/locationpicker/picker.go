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
	"io"
	"log/slog"
	"os"
	"unicode/utf8"

	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
)

// Entry is a single row in the location picker.
type Entry struct {
	URI     workspaceapi.URI
	Range   semanticapi.Range
	Display string
}

// Config configures the appearance of a picker.
type Config struct {
	ListTextAttr  term.Attributes
	ListFocusAttr term.Attributes
	PreviewAttr   term.Attributes
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		ListFocusAttr: term.Attributes{Fg: term.ColorPurple, Attrs: term.AttrBold},
	}
}

// Picker is a floating list + preview UI for code locations.
type Picker struct {
	entries          []Entry
	list             *component.FocusList
	fs               workspaceapi.FileSystem
	scheduleNextTick func(func()) bool
	parser           syntaxapi.Parser
	log              *slog.Logger

	// onSelect, when set, is called with the focused entry index on
	// Enter.
	onSelect func(int)

	previewCells [][]term.Cell // cell matrix for the currently previewed file
	prevURI      string        // URI of the currently loaded preview

	maxEntryW int // widest entry display in rune count
	innerW    int // inner content width from Resize (excludes padding)
	innerH    int // inner content height from Resize (excludes padding)
	previewH  int // computed preview height from Resize
	listH     int // computed list height from Resize

	span        *component.Span
	previewAttr term.Attributes
}

// New allocates a new location picker.
func New(
	entries []Entry,
	fs workspaceapi.FileSystem, scheduleNextTick func(func()) bool,
	parser syntaxapi.Parser, cfg Config, log *slog.Logger,
) *Picker {
	list := &component.FocusList{}
	list.InitWithAttr(cfg.ListTextAttr, cfg.ListFocusAttr)
	maxEntryW := 0
	for _, e := range entries {
		list.PushBack(component.NewResponsiveString(e.Display, component.StringResponsiveConfig{}))
		if w := utf8.RuneCountInString(e.Display); w > maxEntryW {
			maxEntryW = w
		}
	}
	if log == nil {
		log = slog.Default()
	}
	handler := &Picker{
		entries:          entries,
		list:             list,
		fs:               fs,
		scheduleNextTick: scheduleNextTick,
		parser:           parser,
		log:              log,
		maxEntryW:        maxEntryW,
		previewAttr:      cfg.PreviewAttr,
	}
	handler.span = component.NewSpan(&pickerInner{handler}, component.SpanConfig{
		PadHorizontal:    spanHPad,
		PadVertical:      spanVPad,
		ContentAlignment: component.AlignmentCentered,
	})
	handler.loadPreview()
	return handler
}

// SetOnSelect configures the callback invoked when Enter is pressed.
func (l *Picker) SetOnSelect(fn func(int)) {
	l.onSelect = fn
}

// Entries returns the picker entries.
func (l *Picker) Entries() []Entry {
	return l.entries
}

// Handle processes keyboard navigation and selection events.
func (l *Picker) Handle(ev term.Event) (exit, handled bool) {
	if ev.Type != term.EventKey {
		return false, false
	}
	switch ev.Key {
	case term.KeyEsc:
		return true, true
	case term.KeyEnter:
		idx := l.list.FocusOffset()
		if idx < len(l.entries) && l.onSelect != nil {
			l.onSelect(idx)
		}
		return true, true
	case term.KeyArrowUp:
		l.list.FocusUp()
		l.loadPreview()
		return false, true
	case term.KeyArrowDown:
		l.list.FocusDown()
		l.loadPreview()
		return false, true
	}
	if ev.Mod == term.ModCtrl {
		switch ev.Ch {
		case 'j', 'n':
			l.list.FocusDown()
			l.loadPreview()
			return false, true
		case 'k', 'p':
			l.list.FocusUp()
			l.loadPreview()
			return false, true
		}
	}
	return false, false
}

// Draw renders the picker list and preview.
func (l *Picker) Draw(w term.Writer) {
	l.span.Draw(w)
}

// Dimensions returns the picker's preferred floating-window size.
func (l *Picker) Dimensions() (int, int) {
	return l.span.Dimensions()
}

// Cursor returns the picker cursor state.
func (l *Picker) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, term.CursorStyleDefault, false
}

// Selection returns the display string of the focused entry.
func (l *Picker) Selection() (string, bool) {
	idx := l.list.FocusOffset()
	if idx >= len(l.entries) {
		return "", false
	}
	return l.entries[idx].Display, true
}

// Resize resizes the picker to the given dimensions.
func (l *Picker) Resize(w, h int) {
	l.span.Resize(w, h)
}

// Close satisfies browserapi.Handler. The window is left to its owner,
// since it may already show other content.
func (l *Picker) Close() error {
	return nil
}

const (
	previewContextLines = 21 // +/- 10 lines around the reference
	minPreviewWidth     = 80
	maxListHeight       = 15
	separatorHeight     = 1
	spanHPad            = 2
	spanVPad            = 0
)

func (l *Picker) drawPreview(w term.Writer) {
	idx := l.list.FocusOffset()
	if idx >= len(l.entries) || l.previewCells == nil {
		return
	}
	entry := l.entries[idx]
	targetLine := int(entry.Range.Start.Line)
	if targetLine >= len(l.previewCells) {
		targetLine = len(l.previewCells) - 1
		if targetLine < 0 {
			targetLine = 0
		}
	}
	startLine := targetLine - l.previewH/2
	if startLine < 0 {
		startLine = 0
	}
	if startLine+l.previewH > len(l.previewCells) {
		startLine = len(l.previewCells) - l.previewH
		if startLine < 0 {
			startLine = 0
		}
	}
	startChar := int(entry.Range.Start.Character)
	endChar := int(entry.Range.End.Character)
	for row := range l.previewH {
		srcLine := startLine + row
		if srcLine >= len(l.previewCells) {
			break
		}
		isTarget := srcLine == targetLine
		cells := l.previewCells[srcLine]
		for x := range l.innerW {
			var cell term.Cell
			if x < len(cells) {
				cell = cells[x]
			} else {
				cell = term.Cell{Ch: ' ', Width: 1}
			}
			if isTarget {
				cell.SetAttributes(term.AttributesUnion(cell.Attributes(), l.previewAttr))
				if x >= startChar && x < endChar {
					cell.Attrs |= term.AttrReverse
				}
			}
			w.SetCell(term.Coordinates{X: x, Y: row}, cell)
		}
	}
}

func (l *Picker) drawSeparator(w term.Writer) {
	ch := component.FrameCharSetDefault().HorizontalTop
	attr := term.Attributes{Fg: term.ColorGray}
	y := l.previewH
	for x := range l.innerW {
		w.SetCell(term.Coordinates{X: x, Y: y}, term.NewCell(ch, 1, attr))
	}
}

func (l *Picker) loadPreview() {
	idx := l.list.FocusOffset()
	if idx >= len(l.entries) {
		return
	}
	entry := l.entries[idx]
	if entry.URI.String() == l.prevURI {
		return
	}
	f, err := l.fs.OpenFile(entry.URI.Path(), os.O_RDONLY, 0)
	if err != nil {
		l.previewCells = nil
		l.prevURI = ""
		return
	}
	defer f.Close() //nolint:errcheck
	data, err := io.ReadAll(f)
	if err != nil {
		l.previewCells = nil
		l.prevURI = ""
		return
	}
	content := string(data)
	l.previewCells = term.StringToCells(content)
	l.prevURI = entry.URI.String()
	if l.parser != nil {
		baseCells := term.CloneCells(l.previewCells)
		fileURI := entry.URI
		go debug.CapturePanicReport(func() {
			l.loadHighlights(fileURI, content, baseCells)
		})
	}
}

func (l *Picker) loadHighlights(
	uri workspaceapi.URI, content string, baseCells [][]term.Cell,
) {
	iter, err := l.parser.Highlight(uri, content)
	if err != nil {
		return
	}
	defer func() { _ = iter.Close() }()
	highlighted := term.CloneCells(baseCells)
	for {
		loc, ok := iter.Next(context.Background())
		if !ok {
			break
		}
		y := loc.From.Y
		if y < 0 || y >= len(highlighted) {
			continue
		}
		row := highlighted[y]
		for x := loc.From.X; x < loc.To.X && x < len(row); x++ {
			row[x].SetAttributes(loc.Attr)
		}
	}
	if err := iter.Err(); err != nil {
		l.log.Warn("highlight iteration", "uri", uri, "err", err)
		return
	}
	l.scheduleNextTick(func() {
		l.previewCells = highlighted
	})
}
