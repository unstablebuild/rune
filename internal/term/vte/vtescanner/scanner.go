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

package vtescanner

import (
	"math"
	"unicode/utf8"

	"unstable.build/rune/internal/term/vte/utf8parser"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
)

// Scanner represents the VT100 scanner. based on  Paul Williams' ANSI
// parser state machine. See https://vt100.net/emu/dec_ansi_parser for more details.
type Scanner struct {
	utf8            utf8parser.Parser
	driver          Driver
	state           State
	intermediates   []byte
	intermediateIdx uint8
	params          params
	param           uint16
	oscRaw          []byte
	oscParams       [MaxOSCParams][2]uint16
	oscNumParams    int
	apcRaw          []byte
	apcOverflow     bool
	ignoring        bool
}

// NewScanner creates a new Scanner.
func NewScanner(driver Driver) *Scanner {
	ret := new(Scanner)
	ret.Init(driver)
	return ret
}

// Init initializes this scanner with the given Driver.
func (p *Scanner) Init(driver Driver) {
	p.utf8.Init(utf8Receiver{p, driver})
	p.driver = driver
	p.state = Ground
	p.intermediates = make([]byte, MaxIntermediates)
	p.oscRaw = make([]byte, 0, MaxOSCRaw)
}

// Advance advances the scanner's state.
func (p *Scanner) Advance(ch byte) {
	if p.state == Utf8 {
		p.processUtf8(ch)
		return
	}

	// The generated table ignores BEL inside a control string, but
	// kitty accepts it as a terminator alongside ST (kitty
	// vt-parser.c:419-441, :466-468). Without this an APC terminated
	// with BEL swallows everything up to the next ST.
	if p.state == SosPmApcString && ch == 0x07 {
		p.apcEnd()
		p.state = Ground
		return
	}

	change := tableStateChanges[uint8(Anywhere)][uint8(ch)]
	if change == 0 {
		change = tableStateChanges[uint8(p.state)][uint8(ch)]
	}

	state, action := unpack(change)
	p.performStateChange(State(state), Action(action), ch)
}

// GroundRun reports how many leading bytes of buf the scanner would
// print without leaving ground state. Bytes 0x20..0x7E take the Print
// action directly, and a complete well-formed UTF-8 sequence takes the
// same path via BeginUtf8 and returns to ground once decoded, so both
// can be handed to the handler in one batch. DEL (0x7F) is excluded;
// although the table prints it, handlers treat it as a zero-width
// control.
//
// A malformed sequence ends the run so the byte-at-a-time path applies
// the scanner's own error handling. So does a sequence truncated by the
// end of buf, which that path buffers until the rest arrives.
func (p *Scanner) GroundRun(buf []byte) int {
	if p.state != Ground {
		return 0
	}
	for i := 0; i < len(buf); {
		switch ch := buf[i]; {
		case ch >= 0x20 && ch <= 0x7E:
			i++
		case ch < utf8.RuneSelf:
			return i
		default:
			// DecodeRune reports (RuneError, 1) for both malformed and
			// truncated input; a literally encoded U+FFFD is 3 bytes
			// and stays in the run.
			r, size := utf8.DecodeRune(buf[i:])
			if r == utf8.RuneError && size <= 1 {
				return i
			}
			i += size
		}
	}
	return len(buf)
}

type utf8Receiver struct {
	*Scanner
	Driver
}

func (u utf8Receiver) Codepoint(c rune) {
	u.Driver.Print(c)
	u.Scanner.state = Ground
}

func (u utf8Receiver) InvalidSequence() {
	u.Driver.Print('�')
	u.Scanner.state = Ground
}

// ProcessUtf8 processes UTF-8 characters.
func (p *Scanner) processUtf8(ch byte) {
	// p.log(log.TraceLevel, "process utf8: byte: %c", ch)
	p.utf8.Advance(ch)
}

// DriverStateChange performs the state change.
func (p *Scanner) performStateChange(state State, action Action, ch byte) {
	// p.log(log.TraceLevel, "state change: %v to %v, action: %v, ch: %c", p.state, state, action, ch)
	if state == Anywhere {
		// The table ignores every byte of an SOS/PM/APC string; the
		// scanner collects them so APC payloads can be dispatched.
		if p.state == SosPmApcString {
			p.apcPut(ch)
			return
		}
		p.performAction(action, ch)
		return
	}

	switch p.state {
	case DcsPassthrough:
		p.performAction(Unhook, ch)
	case OSCString:
		p.performAction(OSCEnd, ch)
	case SosPmApcString:
		p.apcEnd()
	}

	if action != None {
		p.performAction(action, ch)
	}

	switch state {
	case CsiEntry, DcsEntry, Escape:
		p.performAction(Clear, ch)
	case DcsPassthrough:
		p.performAction(Hook, ch)
	case OSCString:
		p.performAction(OSCStart, ch)
	case SosPmApcString:
		p.apcStart(ch)
	}

	p.state = state

}

// apcStart begins collecting a control string. Only APC (ESC _) is
// dispatched; SOS (ESC X) and PM (ESC ^) are collected and discarded
// as before.
func (p *Scanner) apcStart(introducer byte) {
	p.apcRaw = p.apcRaw[:0]
	p.apcOverflow = introducer != '_'
}

func (p *Scanner) apcPut(ch byte) {
	if p.apcOverflow {
		return
	}
	if len(p.apcRaw) >= MaxAPCRaw {
		p.apcOverflow = true
		p.apcRaw = p.apcRaw[:0]
		return
	}
	p.apcRaw = append(p.apcRaw, ch)
}

func (p *Scanner) apcEnd() {
	if p.apcOverflow {
		p.apcOverflow = false
		return
	}
	p.driver.APCDispatch(p.apcRaw)
	// Release a large payload's storage rather than pinning it for the
	// scanner's lifetime.
	if cap(p.apcRaw) > 64*1024 {
		p.apcRaw = nil
	} else {
		p.apcRaw = p.apcRaw[:0]
	}
}

// DriverAction performs the specified action.
func (p *Scanner) performAction(action Action, ch byte) {
	switch action {
	case Print:
		p.driver.Print(rune(ch))
	case Execute:
		p.driver.Execute(ch)
	case Hook:
		if p.params.isFull() {
			p.ignoring = true
		} else {
			p.params.push(p.param)
		}
		intermediates := p.intermediates[:p.intermediateIdx]
		p.driver.Hook(p.params.slice(), intermediates, p.ignoring, rune(ch))
	case Put:
		p.driver.Put(ch)
	case OSCStart:
		p.oscRaw = p.oscRaw[:0]
		p.oscNumParams = 0
	case OSCPut:
		if ch != ';' {
			p.oscRaw = append(p.oscRaw, ch)
			return
		}
		idx := len(p.oscRaw)
		paramIdx := p.oscNumParams
		switch paramIdx {
		case MaxOSCParams:
			return
		case 0:
			// first param is special
			p.oscParams[0] = [2]uint16{0, uint16(idx)}
		default:
			// the rest depend on previous indexing
			prev := p.oscParams[paramIdx-1]
			p.oscParams[paramIdx] = [2]uint16{prev[1], uint16(idx)}
		}
		p.oscNumParams++
	case OSCEnd:
		paramIdx := p.oscNumParams
		idx := len(p.oscRaw)
		switch paramIdx {
		case MaxOSCParams:
		case 0:
			p.oscParams[0] = [2]uint16{0, uint16(idx)}
			p.oscNumParams++
		default:
			prev := p.oscParams[paramIdx-1]
			p.oscParams[paramIdx] = [2]uint16{prev[1], uint16(idx)}
			p.oscNumParams++
		}
		p.oscDispatch(ch)
	case Unhook:
		p.driver.Unhook()
	case CSIDispatch:
		if p.params.isFull() {
			p.ignoring = true
		} else {
			p.params.push(p.param)
		}
		intermediates := p.intermediates[:p.intermediateIdx]
		p.driver.CSIDispatch(p.params.slice(), intermediates,
			p.ignoring, rune(ch))
	case ESCDispatch:
		intermediates := p.intermediates[:p.intermediateIdx]
		p.driver.ESCDispatch(intermediates, p.ignoring, ch)
	case Collect:
		if p.intermediateIdx == MaxIntermediates {
			p.ignoring = true
			return
		}
		p.intermediates[p.intermediateIdx] = ch
		p.intermediateIdx++
	case Param:
		if p.params.isFull() {
			p.ignoring = true
			return
		}
		switch ch {
		case ':':
			p.params.extend(p.param)
			p.param = 0
		case ';':
			p.params.push(p.param)
			p.param = 0
		default:
			if int(p.param)*10 > math.MaxUint16 {
				p.param = math.MaxUint16
			} else {
				p.param *= 10
			}
			add := uint16(ch - '0')
			if int(p.param)+int(add) > math.MaxUint16 {
				p.param = math.MaxUint16
			} else {
				p.param += add
			}
		}
	case Clear:
		p.intermediateIdx = 0
		p.ignoring = false
		p.param = 0
		p.params.reset()
	case BeginUtf8:
		p.processUtf8(ch)
	case Ignore, None: /* do nothing */
	default:
		p.log(log.WarnLevel, "unknown action: %v", action)
	}
}

func (p *Scanner) oscDispatch(ch byte) {
	var slices [MaxOSCParams][]byte

	for i, indices := range p.oscParams[:p.oscNumParams] {
		slices[i] = p.oscRaw[indices[0]:indices[1]]
	}

	p.driver.OSCDispatch(slices[:p.oscNumParams], ch == 0x07)
}

func (p *Scanner) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "scanner.Scanner").
		Logf(level, msg, args...)
}
