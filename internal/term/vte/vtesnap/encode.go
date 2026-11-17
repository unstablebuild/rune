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
	"io"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// DefaultPageRows is the number of rows an encoder puts in one page
// when the caller does not choose. Small enough that a client can
// prepend history incrementally, large enough that framing overhead
// stays negligible.
const DefaultPageRows = 64

// Encoder writes one snapshot as a stream of records.
//
// Records must be produced in snapshot order. The encoder enforces
// that order so a partially written blob is never mistaken for a valid
// one: Terminal first, then screens with their live pages, then the
// continuation, then Ready, which ends the snapshot.
type Encoder struct {
	w        *RecordWriter
	pageRows int
	stage    stage
}

type stage uint8

const (
	stageInit stage = iota
	stageTerminal
	stageScreens
	stageContinuation
	stageReady
)

// NewEncoder returns an encoder writing to w. It writes the envelope
// immediately.
func NewEncoder(w io.Writer) (*Encoder, error) {
	rw := NewRecordWriter(w)
	if err := rw.Envelope(); err != nil {
		return nil, err
	}
	return &Encoder{w: rw, pageRows: DefaultPageRows}, nil
}

// SetPageRows overrides how many rows one page record carries.
func (e *Encoder) SetPageRows(rows int) {
	if rows > 0 {
		e.pageRows = rows
	}
}

// Terminal writes the TERMINAL record.
func (e *Encoder) Terminal(t Terminal) error {
	if err := e.expect(stageInit, "TERMINAL"); err != nil {
		return err
	}
	e.stage = stageTerminal
	return e.w.Record(TagTerminal, t.encode())
}

// Screen writes one SCREEN record followed by the PAGE records holding
// rows, which must be the screen's live rows top to bottom.
func (e *Encoder) Screen(s Screen, rows [][]term.Cell) error {
	if e.stage != stageTerminal && e.stage != stageScreens {
		return e.orderErr("SCREEN")
	}
	e.stage = stageScreens
	s.Rows = len(rows)
	if err := e.w.Record(TagScreen, s.encode()); err != nil {
		return err
	}
	return e.pages(s.Kind, rows)
}

// Continuation writes the CONTINUATION record. Empty bytes mean the
// stream was cut at a ground state and needs no replay.
func (e *Encoder) Continuation(b []byte) error {
	if e.stage != stageTerminal && e.stage != stageScreens {
		return e.orderErr("CONTINUATION")
	}
	e.stage = stageContinuation
	return e.w.Record(TagContinuation, b)
}

// Ready writes the READY marker, which ends the snapshot.
func (e *Encoder) Ready() error {
	if err := e.expect(stageContinuation, "READY"); err != nil {
		return err
	}
	e.stage = stageReady
	return e.w.Record(TagReady, nil)
}

func (e *Encoder) pages(kind ScreenKind, rows [][]term.Cell) error {
	for start := 0; start < len(rows); start += e.pageRows {
		end := min(start+e.pageRows, len(rows))
		page := Page{Kind: kind, Rows: rows[start:end]}
		if err := e.w.Record(TagPage, page.encode()); err != nil {
			return err
		}
	}
	return nil
}

func (e *Encoder) expect(s stage, what string) error {
	if e.stage != s {
		return e.orderErr(what)
	}
	return nil
}

func (e *Encoder) orderErr(what string) error {
	return fmt.Errorf("vtesnap: %s record out of order", what)
}
