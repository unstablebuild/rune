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

package vteparser

import (
	"unicode/utf8"

	log "github.com/sirupsen/logrus"
	"unstable.build/rune/internal/term/vte/vtescanner"
)

// parserState represents the internal state for the VTE parser.
type parserState struct {
	// Last processed character for repetition.
	precedingChar rune
}

// Parser wraps a Parser to ultimately call methods on a Handler.
type Parser struct {
	state   parserState
	scanner *vtescanner.Scanner
	handler Handler
}

// NewParser returns a new Parser instance.
func NewParser(handler Handler) *Parser {
	ret := new(Parser)
	ret.Init(handler)
	return ret
}

// Init initializes this parser with the given Handler.
func (p *Parser) Init(handler Handler) {
	p.handler = handler
	p.state = parserState{}
	var driver vtescanner.Driver = newDriver(&p.state, handler)
	if log.IsLevelEnabled(log.TraceLevel) {
		driver = vtescanner.WithLoggingDriver(driver)
	}
	p.scanner = vtescanner.NewScanner(driver)
}

// Advance processes a new byte from the PTY.
func (p *Parser) Advance(ch byte) {
	p.scanner.Advance(ch)
}

// AdvanceBytes processes a batch of bytes from the PTY. Runs of
// printable text encountered while the scanner is in ground state are
// delivered to the handler in one InputRun call instead of per-byte
// dispatch, which dominates bulk output streams.
func (p *Parser) AdvanceBytes(buf []byte) {
	for len(buf) > 0 {
		if n := p.scanner.GroundRun(buf); n > 0 {
			p.handler.InputRun(buf[:n])
			p.state.precedingChar, _ = utf8.DecodeLastRune(buf[:n])
			buf = buf[n:]
			continue
		}
		p.scanner.Advance(buf[0])
		buf = buf[1:]
	}
}
