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

package vtesnap

import (
	"fmt"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// ScreenKind names one of the two screens a terminal keeps.
type ScreenKind uint8

// The screens a snapshot can carry.
const (
	// ScreenPrimary is the scrollback-backed screen.
	ScreenPrimary ScreenKind = 0

	// ScreenAlternate is the full-screen program's screen.
	ScreenAlternate ScreenKind = 1
)

func (k ScreenKind) String() string {
	if k == ScreenAlternate {
		return "alternate"
	}
	return "primary"
}

// Mode is a terminal mode carried in the TERMINAL record's bitmap.
// The values are wire constants and must never be renumbered.
type Mode uint8

// Modes tracked by a snapshot.
const (
	ModeCursorKeys Mode = iota
	ModeInsert
	ModeOrigin
	ModeBlinkingCursor
	ModeLineFeedNewLine
	ModeShowCursor
	ModeReportMouseClicks
	ModeReportCellMouseMotion
	ModeReportAllMouseMotion
	ModeReportFocusInOut
	ModeWrap
	ModeUtf8Mouse
	ModeSgrMouse
	ModeAlternateScroll
	ModeUrgencyHints
	ModeBracketedPaste
	ModeSyncUpdate
	ModeReverseScreen
	modeCount
)

// ModeCount is one past the highest defined mode.
const ModeCount = modeCount

// Modes is a set of terminal modes packed into a bitmap.
type Modes uint32

// Get reports whether m is set.
func (s Modes) Get(m Mode) bool { return s&(1<<m) != 0 }

// Set returns the set with m turned on or off.
func (s Modes) Set(m Mode, on bool) Modes {
	if on {
		return s | 1<<m
	}
	return s &^ (1 << m)
}

// Charset binds one charset slot to a standard charset. Both values
// are the emulator's own enumerations, carried verbatim.
type Charset struct {
	Index    uint8
	Standard uint8
}

// Cursor is a cursor position together with the state that travels
// with it across a save/restore.
type Cursor struct {
	Position   term.Coordinates
	Attributes term.Attributes
	Hidden     bool
	Charsets   []Charset
}

// Terminal is the TERMINAL record: terminal-wide state that is not
// owned by either screen.
type Terminal struct {
	Width       int
	Height      int
	Title       string
	TitleStack  []string
	Tabstops    []bool
	Modes       Modes
	CursorStyle term.CursorStyle
	// CursorHidden is DECTCEM's effect on the drawn cursor, which the
	// emulator tracks separately from the per-screen cursor state.
	CursorHidden bool
	// PendingWrap is DEC's deferred wrap: the cursor sits past the
	// last column and the next glyph starts a new row.
	PendingWrap    bool
	CurrentCharset uint8
	UseAlternate   bool
	UsedAlternate  bool
	MaxScrollback  int
	MinWidth       int
	// KittyKeyboard holds the kitty keyboard protocol flag stacks of
	// the primary and alternate screens, oldest entry first. A replica
	// encodes its keys from them, so losing them would hand a program
	// keys in an encoding it did not ask for.
	KittyKeyboard [2][]uint8
	// ModifyOtherKeys is xterm's modifyOtherKeys level.
	ModifyOtherKeys uint8
	// CellPixelWidth and CellPixelHeight are the size of a cell in
	// pixels, zero when the terminal has none. Placing an image moves
	// the cursor by the cells the image covers, so a replica interprets
	// the stream with this cell size rather than its own display's.
	CellPixelWidth  int
	CellPixelHeight int
}

// Screen is the SCREEN record: one screen's state, followed by the
// PAGE records holding its live rows.
type Screen struct {
	Kind                   ScreenKind
	Width                  int
	Height                 int
	TopScrollableRegion    int
	BottomScrollableRegion int
	Cursor                 Cursor
	SavedCursor            Cursor
	// Rows is the total number of rows the PAGE records that follow
	// add up to, so a decoder can size its grid before reading them.
	Rows int
}

// Page is the PAGE record: a contiguous run of a screen's live rows,
// ordered top to bottom from row zero.
type Page struct {
	Kind ScreenKind
	Rows [][]term.Cell
}

func (t *Terminal) encode() []byte {
	var e enc
	e.i32(t.Width)
	e.i32(t.Height)
	e.str(t.Title)
	e.u32(uint32(len(t.TitleStack)))
	for _, title := range t.TitleStack {
		e.str(title)
	}
	e.u32(uint32(len(t.Tabstops)))
	for _, stop := range t.Tabstops {
		e.bool(stop)
	}
	e.u32(uint32(t.Modes))
	e.u8(uint8(t.CursorStyle))
	e.bool(t.CursorHidden)
	e.bool(t.PendingWrap)
	e.u8(t.CurrentCharset)
	e.bool(t.UseAlternate)
	e.bool(t.UsedAlternate)
	e.i32(t.MaxScrollback)
	e.i32(t.MinWidth)
	for _, stack := range t.KittyKeyboard {
		e.u32(uint32(len(stack)))
		for _, flags := range stack {
			e.u8(flags)
		}
	}
	e.u8(t.ModifyOtherKeys)
	e.i32(t.CellPixelWidth)
	e.i32(t.CellPixelHeight)
	return e.b
}

func decodeTerminal(payload []byte) (Terminal, error) {
	d := dec{b: payload}
	var t Terminal
	t.Width = d.i32()
	t.Height = d.i32()
	t.Title = d.str()
	if n := d.count(4); n > 0 {
		t.TitleStack = make([]string, n)
		for i := range t.TitleStack {
			t.TitleStack[i] = d.str()
		}
	}
	if n := d.count(1); n > 0 {
		t.Tabstops = make([]bool, n)
		for i := range t.Tabstops {
			t.Tabstops[i] = d.bool()
		}
	}
	t.Modes = Modes(d.u32())
	t.CursorStyle = term.CursorStyle(d.u8())
	t.CursorHidden = d.bool()
	t.PendingWrap = d.bool()
	t.CurrentCharset = d.u8()
	t.UseAlternate = d.bool()
	t.UsedAlternate = d.bool()
	t.MaxScrollback = d.i32()
	t.MinWidth = d.i32()
	for i := range t.KittyKeyboard {
		if n := d.count(1); n > 0 {
			t.KittyKeyboard[i] = make([]uint8, n)
			for j := range t.KittyKeyboard[i] {
				t.KittyKeyboard[i][j] = d.u8()
			}
		}
	}
	t.ModifyOtherKeys = d.u8()
	t.CellPixelWidth = d.i32()
	t.CellPixelHeight = d.i32()
	return t, d.done()
}

func (c *Cursor) encodeInto(e *enc) {
	e.i32(c.Position.X)
	e.i32(c.Position.Y)
	e.u32(uint32(c.Attributes.Fg))
	e.u32(uint32(c.Attributes.Bg))
	e.u16(uint16(c.Attributes.Attrs))
	e.u32(uint32(c.Attributes.Underline))
	e.bool(c.Hidden)
	e.u32(uint32(len(c.Charsets)))
	for _, cs := range c.Charsets {
		e.u8(cs.Index)
		e.u8(cs.Standard)
	}
}

func decodeCursorInto(d *dec) Cursor {
	var c Cursor
	c.Position.X = d.i32()
	c.Position.Y = d.i32()
	c.Attributes.Fg = term.Color(d.u32())
	c.Attributes.Bg = term.Color(d.u32())
	c.Attributes.Attrs = term.AttrMask(d.u16())
	c.Attributes.Underline = term.Color(d.u32())
	c.Hidden = d.bool()
	if n := d.count(2); n > 0 {
		c.Charsets = make([]Charset, n)
		for i := range c.Charsets {
			c.Charsets[i].Index = d.u8()
			c.Charsets[i].Standard = d.u8()
		}
	}
	return c
}

func (s *Screen) encode() []byte {
	var e enc
	e.u8(uint8(s.Kind))
	e.i32(s.Width)
	e.i32(s.Height)
	e.i32(s.TopScrollableRegion)
	e.i32(s.BottomScrollableRegion)
	s.Cursor.encodeInto(&e)
	s.SavedCursor.encodeInto(&e)
	e.i32(s.Rows)
	return e.b
}

func decodeScreen(payload []byte) (Screen, error) {
	d := dec{b: payload}
	var s Screen
	s.Kind = ScreenKind(d.u8())
	s.Width = d.i32()
	s.Height = d.i32()
	s.TopScrollableRegion = d.i32()
	s.BottomScrollableRegion = d.i32()
	s.Cursor = decodeCursorInto(&d)
	s.SavedCursor = decodeCursorInto(&d)
	s.Rows = d.i32()
	if err := d.done(); err != nil {
		return Screen{}, err
	}
	if err := s.Kind.validate(); err != nil {
		return Screen{}, err
	}
	return s, nil
}

func (k ScreenKind) validate() error {
	if k != ScreenPrimary && k != ScreenAlternate {
		return fmt.Errorf("%w: unknown screen kind %d", ErrMalformed, uint8(k))
	}
	return nil
}
