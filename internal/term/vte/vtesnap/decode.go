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
	"errors"
	"fmt"
	"io"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// ScreenState is one decoded screen: its state plus the live rows the
// PAGE records carried.
type ScreenState struct {
	Screen
	Rows [][]term.Cell
}

// Snapshot is a decoded terminal, complete through READY. Scrollback
// is not part of it: transports carry it separately, as pages encoded
// by [EncodePage].
type Snapshot struct {
	Terminal     Terminal
	Primary      ScreenState
	Alternate    ScreenState
	Continuation []byte
}

// Decoder reads one snapshot from a stream of records.
type Decoder struct {
	r     *RecordReader
	ready bool
}

// NewDecoder returns a decoder over r and validates the envelope.
func NewDecoder(r io.Reader) (*Decoder, error) {
	rr := NewRecordReader(r)
	if err := rr.Envelope(); err != nil {
		return nil, err
	}
	return &Decoder{r: rr}, nil
}

// Ready reads records through the READY marker and returns the
// terminal they describe.
func (d *Decoder) Ready() (*Snapshot, error) {
	if d.ready {
		return nil, errors.New("vtesnap: Ready called twice")
	}

	var snap Snapshot
	var current *ScreenState
	seen := map[Tag]bool{}

	for {
		tag, payload, err := d.r.Next()
		if err != nil {
			return nil, truncated(err)
		}

		switch tag {
		case TagTerminal:
			if seen[TagTerminal] {
				return nil, fmt.Errorf("%w: duplicate TERMINAL", ErrMalformed)
			}
			if snap.Terminal, err = decodeTerminal(payload); err != nil {
				return nil, err
			}

		case TagScreen:
			screen, err := decodeScreen(payload)
			if err != nil {
				return nil, err
			}
			current = snap.screen(screen.Kind)
			if current.Screen.Rows != 0 || len(current.Rows) != 0 {
				return nil, fmt.Errorf("%w: duplicate %s SCREEN",
					ErrMalformed, screen.Kind)
			}
			current.Screen = screen

		case TagPage:
			page, err := decodePage(payload)
			if err != nil {
				return nil, err
			}
			if current == nil || page.Kind != current.Kind {
				return nil, fmt.Errorf("%w: PAGE outside its SCREEN",
					ErrMalformed)
			}
			current.Rows = append(current.Rows, page.Rows...)

		case TagContinuation:
			snap.Continuation = append([]byte(nil), payload...)

		case TagReady:
			if !seen[TagTerminal] {
				return nil, fmt.Errorf("%w: READY before TERMINAL", ErrMalformed)
			}
			if err := snap.validate(); err != nil {
				return nil, err
			}
			d.ready = true
			return &snap, nil

		default:
			return nil, fmt.Errorf("%w: %s before READY", ErrMalformed, tag)
		}
		seen[tag] = true
	}
}

// truncated distinguishes a stream that stopped from one that ended.
// A snapshot ends at READY, so records simply running out before it
// means the producer died mid-snapshot.
func truncated(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

func (s *Snapshot) screen(kind ScreenKind) *ScreenState {
	if kind == ScreenAlternate {
		return &s.Alternate
	}
	return &s.Primary
}

func (s *Snapshot) validate() error {
	for _, screen := range []*ScreenState{&s.Primary, &s.Alternate} {
		if len(screen.Rows) != screen.Screen.Rows {
			return fmt.Errorf("%w: %s SCREEN declared %d rows, got %d",
				ErrMalformed, screen.Kind, screen.Screen.Rows, len(screen.Rows))
		}
	}
	return nil
}
