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
	"fmt"
	"slices"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/vte/vteparser"
	"unstable.build/rune/internal/term/vte/vtescreen"
	"unstable.build/rune/internal/term/vte/vtesnap"
)

// modeBindings maps each wire mode to the field that holds it. It is
// the single place the two representations meet, so a mode added to
// the emulator without a wire slot fails to compile here.
func (t *parserHandler) modeBindings() map[vtesnap.Mode]*bool {
	return map[vtesnap.Mode]*bool{
		vtesnap.ModeCursorKeys:            &t.modeCursorKeys,
		vtesnap.ModeInsert:                &t.modeInsert,
		vtesnap.ModeOrigin:                &t.modeOrigin,
		vtesnap.ModeBlinkingCursor:        &t.modeBlinkingCursor,
		vtesnap.ModeLineFeedNewLine:       &t.modeLineFeedNewLine,
		vtesnap.ModeShowCursor:            &t.modeShowCursor,
		vtesnap.ModeReportMouseClicks:     &t.modeReportMouseClicks,
		vtesnap.ModeReportCellMouseMotion: &t.modeReportCellMouseMotion,
		vtesnap.ModeReportAllMouseMotion:  &t.modeReportAllMouseMotion,
		vtesnap.ModeReportFocusInOut:      &t.modeReportFocusInOut,
		vtesnap.ModeWrap:                  &t.modeWrap,
		vtesnap.ModeUtf8Mouse:             &t.modeUtf8Mouse,
		vtesnap.ModeSgrMouse:              &t.modeSgrMouse,
		vtesnap.ModeAlternateScroll:       &t.modeAlternateScroll,
		vtesnap.ModeUrgencyHints:          &t.modeUrgencyHints,
		vtesnap.ModeBracketedPaste:        &t.modeBracketedPaste,
		vtesnap.ModeSyncUpdate:            &t.modeSyncUpdate,
		vtesnap.ModeReverseScreen:         &t.modeReverseScreen,
	}
}

// SnapshotThroughReady encodes everything a replica needs to render
// this terminal: the envelope, terminal state, both screens with their
// live rows, the continuation and the READY marker. Scrollback is not
// included; SnapshotHistory returns it separately so a transport can
// deliver it after the screen is already usable.
//
// continuation must be the replay-safe bytes for the exact stream
// position this snapshot is cut at, as produced by the parser's
// continuation tracker.
func (t *Component) SnapshotThroughReady(continuation []byte) ([]byte, error) {
	if err := vteparser.ValidateContinuation(continuation); err != nil {
		return nil, fmt.Errorf("vte: snapshot continuation: %w", err)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	ph := t.parserHandler
	var buf bytes.Buffer
	e, err := vtesnap.NewEncoder(&buf)
	if err != nil {
		return nil, err
	}
	if err := e.Terminal(ph.terminalRecord()); err != nil {
		return nil, err
	}

	primary, _ := ph.primaryRows()
	if err := e.Screen(
		screenRecord(vtesnap.ScreenPrimary, &ph.sync.primBuf.AltBuffer),
		primary,
	); err != nil {
		return nil, err
	}
	if err := e.Screen(
		screenRecord(vtesnap.ScreenAlternate, ph.sync.altBuf),
		ph.sync.altBuf.Cells.RawCells(),
	); err != nil {
		return nil, err
	}
	if err := e.Continuation(continuation); err != nil {
		return nil, err
	}
	if err := e.Ready(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// SnapshotHistory returns a copy of the primary screen's scrollback,
// oldest row first. The rows are cloned so the caller may stream them
// without holding the emulator still.
func (t *Component) SnapshotHistory() [][]term.Cell {
	t.mu.Lock()
	defer t.mu.Unlock()

	_, history := t.parserHandler.primaryRows()
	return term.CloneCells(history)
}

// primaryRows splits the primary buffer into the rows currently on
// screen and the scrollback above them. Both share the buffer's
// storage, so callers must hold the component lock or clone.
func (t *parserHandler) primaryRows() (live, history [][]term.Cell) {
	rows := t.sync.primBuf.Cells.RawCells()
	if t.height <= 0 {
		// A terminal nobody has sized yet has no screen to scroll
		// out of, so none of its rows are scrollback.
		return rows, nil
	}
	split := max(len(rows)-t.height, 0)
	return rows[split:], rows[:split]
}

func (t *parserHandler) terminalRecord() vtesnap.Terminal {
	var modes vtesnap.Modes
	for mode, field := range t.modeBindings() {
		modes = modes.Set(mode, *field)
	}
	rec := vtesnap.Terminal{
		Width:           t.width,
		Height:          t.height,
		Title:           t.title,
		TitleStack:      t.titles,
		Tabstops:        t.tabs.tabs,
		Modes:           modes,
		CursorStyle:     t.cursorStyle,
		CursorHidden:    t.cursorHidden,
		PendingWrap:     t.shouldWrap,
		CurrentCharset:  uint8(t.currentCharset),
		UseAlternate:    t.useAlt,
		UsedAlternate:   t.usedAlt,
		MaxScrollback:   t.maxScrollLength,
		MinWidth:        t.minWidth,
		ModifyOtherKeys: uint8(t.keyboard.encoding().modifyOtherKeys),
	}
	if cell, ok := t.graphics.cell(); ok {
		rec.CellPixelWidth, rec.CellPixelHeight = cell.Width, cell.Height
	}
	for i := range t.keyboard.kitty {
		stack := &t.keyboard.kitty[i]
		for _, flags := range stack.entries[:stack.depth] {
			rec.KittyKeyboard[i] = append(rec.KittyKeyboard[i], uint8(flags))
		}
	}
	return rec
}

func screenRecord(kind vtesnap.ScreenKind, b *vtescreen.AltBuffer) vtesnap.Screen {
	return vtesnap.Screen{
		Kind:                   kind,
		Width:                  b.Width(),
		Height:                 b.Height(),
		TopScrollableRegion:    b.TopScrollableRegion(),
		BottomScrollableRegion: b.BottomScrollableRegion(),
		Cursor:                 cursorRecord(b.Cursor()),
		SavedCursor:            cursorRecord(b.SavedCursor()),
	}
}

func cursorRecord(c vtescreen.CursorState) vtesnap.Cursor {
	var charsets []vtesnap.Charset
	for index, standard := range c.Charsets {
		charsets = append(charsets, vtesnap.Charset{
			Index:    uint8(index),
			Standard: uint8(standard),
		})
	}
	// Map iteration order is unspecified; a stable order keeps the
	// encoding a pure function of the emulator state.
	slices.SortFunc(charsets, func(a, b vtesnap.Charset) int {
		return int(a.Index) - int(b.Index)
	})
	return vtesnap.Cursor{
		Position:   c.Position(),
		Attributes: c.Attributes(),
		Hidden:     c.Hidden(),
		Charsets:   charsets,
	}
}

// ApplySnapshot replaces this emulator's entire state with a decoded
// snapshot, parser included: the bytes that follow must be the
// snapshot's continuation and then the stream from the position it
// was cut at. The pty and the running process are untouched: only the
// interpretation of the byte stream is replaced, which is what a
// replica needs on attach and again after a transport loss.
//
// It must not run concurrently with the emulator's own parse loop:
// before Run, or from the read it is blocked in.
func (t *Component) ApplySnapshot(snap *vtesnap.Snapshot) error {
	// A terminal nobody has sized yet is replicated unsized: giving it
	// dimensions the emulator it replicates does not have would make
	// the two interpret the same bytes differently.
	width := max(snap.Terminal.Width, 0)
	height := max(snap.Terminal.Height, 0)

	t.parser.Reset()
	t.mu.Lock()
	// Force the resize below through the full path even when the
	// dimensions already match what this component last saw.
	t.width = 0
	t.height = 0
	t.mu.Unlock()

	// Size the buffers before installing content: resizing the
	// alternate screen clears it, so restoring first would throw the
	// snapshot away.
	if width > 0 && height > 0 {
		if err := t.Resize(width, height); err != nil {
			return err
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	ph := t.parserHandler
	ph.applyTerminal(snap.Terminal)
	applyScreen(ph.sync.primBuf, snap.Primary, width, height)
	applyScreen(ph.sync.altBuf, snap.Alternate, width, height)
	if snap.Terminal.UseAlternate {
		ph.sync.buf = ph.sync.altBuf
	} else {
		ph.sync.buf = ph.sync.primBuf
	}

	t.scroll.InitPerformance(&ph.sync.primBuf.Cells)
	t.scroll.InvertOffset = true
	t.scroll.SetTabspaces(1)
	if width > 0 && height > 0 {
		t.scroll.Resize(width, height)
		t.scroll.ClampOffset()
	}
	// A replica cut inside a synchronized update has no frame from
	// before it, so it holds the one it joined with.
	t.syncUpdateLocked(ph.modeSyncUpdate)
	t.version.Add(1)
	return nil
}

// PrependHistory puts scrollback rows above the primary screen's
// existing rows. Pages arrive newest first, so each call prepends
// immediately before whatever is already there.
func (t *Component) PrependHistory(rows [][]term.Cell) {
	if len(rows) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	cells := t.parserHandler.sync.primBuf.Cells.RawCells()
	combined := make([][]term.Cell, 0, len(rows)+len(cells))
	combined = append(combined, rows...)
	combined = append(combined, cells...)
	t.parserHandler.sync.primBuf.Cells.ResetCells(combined)
	t.version.Add(1)
}

func (t *parserHandler) applyTerminal(rec vtesnap.Terminal) {
	t.title = rec.Title
	t.titles = rec.TitleStack
	t.tabs.tabs = rec.Tabstops
	for mode, field := range t.modeBindings() {
		*field = rec.Modes.Get(mode)
	}
	t.cursorStyle = rec.CursorStyle
	t.cursorHidden = rec.CursorHidden
	t.shouldWrap = rec.PendingWrap
	t.currentCharset = vteparser.CharsetIndex(rec.CurrentCharset)
	t.useAlt = rec.UseAlternate
	t.usedAlt = rec.UsedAlternate
	t.width = rec.Width
	t.height = rec.Height
	t.keyboard.reset()
	for i, stack := range rec.KittyKeyboard {
		for _, flags := range stack {
			t.keyboard.kitty[i].push(vteparser.KeyboardMode(flags) & definedKittyFlags)
		}
	}
	t.keyboard.activate(t.useAlt)
	t.keyboard.setModifyOtherKeys(vteparser.ModifyOtherKeysMode(rec.ModifyOtherKeys))
}

// restorableScreen is the state-replacement surface both screen
// buffers share. The primary buffer overrides Restore so its rows keep
// the capacity of its minimum width, while the size itself is the
// snapshot's, applied by SetSizeExact; so the interface must be
// satisfied by the concrete buffer rather than its embedded AltBuffer.
type restorableScreen interface {
	Restore(cells [][]term.Cell, cursor term.Coordinates, width, height int)
	SetCursor(vtescreen.CursorState)
	SetSavedCursor(vtescreen.CursorState)
	SetScrollableRegionExact(top, bottom int)
	SetSizeExact(width, height int)
}

func applyScreen(b restorableScreen, s vtesnap.ScreenState, width, height int) {
	b.Restore(s.Rows, nonNegative(s.Cursor.Position), width, height)
	b.SetSizeExact(s.Width, s.Height)
	b.SetCursor(cursorState(s.Cursor))
	b.SetSavedCursor(cursorState(s.SavedCursor))
	b.SetScrollableRegionExact(s.TopScrollableRegion, s.BottomScrollableRegion)
}

// nonNegative keeps a position from a corrupt snapshot off negative
// coordinates, which no screen operation accepts.
func nonNegative(c term.Coordinates) term.Coordinates {
	return term.Coordinates{X: max(c.X, 0), Y: max(c.Y, 0)}
}

func cursorState(c vtesnap.Cursor) vtescreen.CursorState {
	charsets := make(map[vteparser.CharsetIndex]vteparser.StandardCharset,
		len(c.Charsets))
	for _, cs := range c.Charsets {
		charsets[vteparser.CharsetIndex(cs.Index)] =
			vteparser.StandardCharset(cs.Standard)
	}
	return vtescreen.NewCursorState(nonNegative(c.Position), c.Attributes, c.Hidden, charsets)
}
