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
	"bytes"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// C0 set of 7-bit control characters (from ANSI X3.4-1977).
const (
	c0NUL  = 0x00 // Null filler, terminal should ignore this character.
	c0SOH  = 0x01 // Start of Header.
	c0STX  = 0x02 // Start of Text, implied end of header.
	c0ETX  = 0x03 // End of Text, causes some terminal to respond with ACK or NAK.
	c0EOT  = 0x04 // End of Transmission.
	c0ENQ  = 0x05 // Enquiry, causes terminal to send ANSWER-BACK ID.
	c0ACK  = 0x06 // Acknowledge, usually sent by terminal in response to ETX.
	c0BEL  = 0x07 // Bell, triggers the bell, buzzer, or beeper on the terminal.
	c0BS   = 0x08 // Backspace, can be used to define overstruck characters.
	c0HT   = 0x09 // Horizontal Tabulation, move to next predetermined position.
	c0LF   = 0x0A // Linefeed, move to same position on next line (see also NL).
	c0VT   = 0x0B // Vertical Tabulation, move to next predetermined line.
	c0FF   = 0x0C // Form Feed, move to next form or page.
	c0CR   = 0x0D // Carriage Return, move to first character of current line.
	c0SO   = 0x0E // Shift Out, switch to G1 (other half of character set).
	c0SI   = 0x0F // Shift In, switch to G0 (normal half of character set).
	c0DLE  = 0x10 // Data Link Escape, interpret next control character specially.
	c0XON  = 0x11 // (DC1) Terminal is allowed to resume transmitting.
	c0DC2  = 0x12 // Device Control 2, causes ASR-33 to activate paper-tape reader.
	c0XOFF = 0x13 // (DC2) Terminal must pause and refrain from transmitting.
	c0DC4  = 0x14 // Device Control 4, causes ASR-33 to deactivate paper-tape reader.
	c0NAK  = 0x15 // Negative Acknowledge, used sometimes with ETX and ACK.
	c0SYN  = 0x16 // Synchronous Idle, used to maintain timing in Sync communication.
	c0ETB  = 0x17 // End of Transmission block.
	c0CAN  = 0x18 // Cancel (makes VT100 abort current escape sequence if any).
	c0EM   = 0x19 // End of Medium.
	c0SUB  = 0x1A // Substitute (VT100 uses this to display parity errors).
	c0ESC  = 0x1B // Prefix to an escape sequence.
	c0FS   = 0x1C // File Separator.
	c0GS   = 0x1D // Group Separator.
	c0RS   = 0x1E // Record Separator (sent by VT132 in block-transfer mode).
	c0US   = 0x1F // Unit Separator.
	c0DEL  = 0x7F // Delete, should be ignored by terminal.
)

type driver struct {
	handler Handler
	state   *parserState
	// Scratch buffers for SGR dispatch. Neither escapes the dispatch
	// call and the parse stage is single-threaded, so reusing them keeps
	// dense attribute streams allocation-free.
	sgrAttrs      []Attr
	sgrHeadParams []uint16
}

func newDriver(state *parserState, h Handler) *driver {
	ret := new(driver)
	ret.init(state, h)
	return ret
}

func (p *driver) init(state *parserState, h Handler) {
	p.handler = h
	p.state = state
}

func (p *driver) Print(r rune) {
	p.handler.Input(r)
	p.state.precedingChar = r
}

func (p *driver) Execute(ch byte) {
	switch ch {
	case c0HT:
		p.handler.PutTab()
	case c0BS:
		p.handler.Backspace()
	case c0CR:
		p.handler.CarriageReturn()
	case c0LF, c0VT, c0FF:
		p.handler.Linefeed()
	case c0BEL:
		p.handler.Bell()
	case c0SUB:
		p.handler.Substitute()
	case c0SI:
		p.handler.SetActiveCharset(CharsetIndexG0)
	case c0SO:
		p.handler.SetActiveCharset(CharsetIndexG1)
	default:
		p.log(log.DebugLevel, "unhandled execute byte=%02x", ch)
	}
}

func (p *driver) Hook(params [][]uint16, intermediates []byte, ignore bool, action rune) {
	p.log(log.DebugLevel, "unhandled hook params=%v, ints: %v, ignore: %v, action: %v",
		params, intermediates, ignore, action)
}

func (p *driver) Put(ch byte) {
	p.log(log.DebugLevel, "unhandled put byte=%v", ch)
}

func (p *driver) Unhook() {
	p.log(log.DebugLevel, "unhandled unhook")
}

func (p *driver) APCDispatch(data []byte) {
	if len(data) > 0 && data[0] == 'G' {
		p.handler.GraphicsCommand(data[1:])
		return
	}
	p.log(log.DebugLevel, "unhandled apc len=%d", len(data))
}

func (p *driver) OSCDispatch(params [][]byte, bellTerminated bool) {
	terminator := "\x1b\\"
	if bellTerminated {
		terminator = "\x07"
	}

	if len(params) == 0 || len(params[0]) == 0 {
		return
	}

	switch string(params[0]) {
	case "0", "2":
		if len(params) >= 2 {
			var title strings.Builder
			for _, x := range params[1:] {
				title.WriteString(string(x))
				title.WriteByte(';')
			}
			titleStr := strings.Trim(title.String(), ";")
			p.handler.SetTitle(titleStr)
			return
		}
		p.logUnhandledOSC(params)

	case "4":
		/* unsupported setting color value of indexed color, or responding to dynamic color query */
	case "8":
		if len(params) > 2 {
			linkParams := params[1]
			var uri strings.Builder

			uri.WriteString(string(params[2]))
			for _, param := range params[3:] {
				uri.WriteByte(';')
				uri.WriteString(string(param))
			}

			if uri.Len() == 0 {
				p.handler.SetHyperlink(nil)
				return
			}

			var id string
			for kv := range bytes.SplitSeq(linkParams, []byte(":")) {
				if bytes.HasPrefix(kv, []byte("id=")) {
					id = string(kv[3:])
					break
				}
			}

			p.handler.SetHyperlink(&Hyperlink{ID: id, URI: uri.String()})
		}

	case "10", "11", "12":
		/* unsupported setting color value of indexed color, or responding to dynamic color query */
	case "22":
		/* unsupported setting of cursor icon */
	case "50":
		if len(params) >= 2 && len(params[1]) >= 13 &&
			bytes.HasPrefix(params[1], []byte("CursorShape=")) {
			var shape CursorShape
			switch params[1][12] {
			case '0':
				shape = CursorShapeBlock
			case '1':
				shape = CursorShapeBeam
			case '2':
				shape = CursorShapeUnderline
			default:
				p.logUnhandledOSC(params)
				return
			}
			p.handler.SetCursorShape(shape)
			return
		}
		p.logUnhandledOSC(params)

	case "52":
		if len(params) < 3 {
			p.logUnhandledOSC(params)
			return
		}

		register := int('c')
		if len(params[1]) != 0 {
			register = int(params[1][0])
		}
		switch string(params[2]) {
		case "?":
			p.handler.ClipboardLoad(register, terminator)
		default:
			p.handler.ClipboardStore(register, params[2])
		}

	case "104", "110", "111", "112":
	/* unsupported color CSI dispatch */
	default:
		p.logUnhandledOSC(params)
	}
}

func (p *driver) CSIDispatch(
	params [][]uint16, intermediates []byte,
	hasIgnoredIntermediates bool, action rune,
) {
	if hasIgnoredIntermediates || len(intermediates) > 2 {
		p.logUnhandledCSI(params, intermediates, action)
		return
	}

	handler := p.handler
	nextParamOr := func(defaultValue int) int {
		if len(params) == 0 {
			return defaultValue
		}
		head := params[0]
		params = params[1:]
		if head[0] == 0 {
			return defaultValue
		}
		ret := head[0]
		return int(ret)
	}

	// TODO consider doing the same as ESC where we keep a tight control over intermediates.

	switch action {
	case '@':
		handler.InsertBlank(nextParamOr(1))
	case 'A':
		handler.MoveUp(nextParamOr(1))
	case 'B', 'e':
		handler.MoveDown(nextParamOr(1))
	case 'b':
		if c := p.state.precedingChar; c != 0 {
			count := nextParamOr(1)
			for range count {
				handler.Input(c)
			}
		} else {
			p.log(log.TraceLevel, "tried to repeat with no preceding char")
		}
	case 'C', 'a':
		handler.MoveForward(nextParamOr(1))
	case 'c':
		if nextParamOr(0) == 0 {
			identifySecondary := len(intermediates) != 0 && intermediates[0] == '>'
			handler.IdentifyTerminal(identifySecondary)
		} else {
			p.logUnhandledCSI(params, intermediates, action)
		}
	case 'D':
		handler.MoveBackward(nextParamOr(1))
	case 'd':
		handler.GotoLine(nextParamOr(1) - 1)
	case 'E':
		handler.MoveDownAndCR(nextParamOr(1))
	case 'F':
		handler.MoveUpAndCR(nextParamOr(1))
	case 'G', '`':
		handler.GotoCol(nextParamOr(1) - 1)
	case 'g':
		mode := nextParamOr(0)
		switch mode {
		case 0:
			handler.ClearTabs(TabulationClearModeCurrent)
		case 3:
			handler.ClearTabs(TabulationClearModeAll)
		default:
			p.logUnhandledCSI(params, intermediates, action)
		}
	case 'H', 'f':
		y := nextParamOr(1)
		x := nextParamOr(1)
		handler.Goto(y-1, x-1)
	case 'h':
		if len(intermediates) == 1 && intermediates[0] == '?' {
			for _, param := range params {
				if len(param) == 0 {
					p.logUnexpectedParamsLength(params, intermediates, action)
				} else {
					if PrivateMode(param[0]) == PrivateModeSyncUpdate {
						p.state.syncState.timeout.SetTimeout(syncUpdateTimeout)
					}
					p.handler.SetPrivateMode(NewPrivateMode(param[0]))
				}
			}
		} else if len(intermediates) == 0 {
			for _, param := range params {
				if len(param) == 0 {
					p.logUnexpectedParamsLength(params, intermediates, action)
				} else {
					p.handler.SetMode(NewMode(param[0]))
				}
			}
		} else {
			p.logUnhandledCSI(params, intermediates, action)
		}
	case 'I':
		handler.MoveForwardTabs(nextParamOr(1))
	case 'J':
		mode := nextParamOr(0)
		switch mode {
		case 0:
			handler.ClearScreen(ClearModeBelow)
		case 1:
			handler.ClearScreen(ClearModeAbove)
		case 2:
			handler.ClearScreen(ClearModeAll)
		case 3:
			handler.ClearScreen(ClearModeSaved)
		default:
			p.logUnhandledCSI(params, intermediates, action)
		}
	case 'K':
		mode := nextParamOr(0)
		switch mode {
		case 0:
			handler.ClearLine(LineClearModeRight)
		case 1:
			handler.ClearLine(LineClearModeLeft)
		case 2:
			handler.ClearLine(LineClearModeAll)
		default:
			p.logUnhandledCSI(params, intermediates, action)
		}
	case 'L':
		handler.InsertBlankLines(nextParamOr(1))
	case 'l':
		if len(intermediates) == 1 && intermediates[0] == '?' {
			for _, param := range params {
				if len(param) == 0 {
					p.logUnexpectedParamsLength(params, intermediates, action)
				} else {
					p.handler.UnsetPrivateMode(NewPrivateMode(param[0]))
				}
			}
		} else if len(intermediates) == 0 {
			for _, param := range params {
				if len(param) == 0 {
					p.logUnexpectedParamsLength(params, intermediates, action)
				} else {
					p.handler.UnsetMode(NewMode(param[0]))
				}
			}
		} else {
			p.logUnhandledCSI(params, intermediates, action)
		}
	case 'M':
		handler.DeleteLines(nextParamOr(1))
	case 'm':
		if len(intermediates) == 1 && intermediates[0] == '>' {
			if nextParamOr(1) == 4 {
				switch nextParamOr(0) {
				case 0:
					p.handler.SetModifyOtherKeys(ModifyOtherKeysReset)
				case 1:
					p.handler.SetModifyOtherKeys(ModifyOtherKeysEnableExceptWellDefined)
				case 2:
					p.handler.SetModifyOtherKeys(ModifyOtherKeysEnableAll)
				default:
					p.logUnhandledCSI(params, intermediates, action)
				}
			} else {
				p.logUnhandledCSI(params, intermediates, action)
			}
		} else if len(intermediates) == 1 && intermediates[0] == '?' {
			if nextParamOr(0) == 4 {
				p.handler.ReportModifyOtherKeys()
			} else {
				p.logUnhandledCSI(params, intermediates, action)
			}
		} else if len(intermediates) == 0 {
			if len(params) == 0 {
				handler.TerminalAttribute(Attr{Type: ResetAttr})
			} else {
				for _, attr := range p.attrsFromSgrParameters(params) {
					handler.TerminalAttribute(attr)
				}
			}
		} else {
			p.logUnhandledCSI(params, intermediates, action)
		}
	case 'n':
		handler.DeviceStatus(nextParamOr(0))
	case 'P':
		handler.DeleteChars(nextParamOr(1))
	case 'p':
		switch {
		case len(intermediates) == 1 && intermediates[0] == '$':
			p.handler.ReportMode(NewMode(uint16(nextParamOr(0))))
		case len(intermediates) == 2 && intermediates[0] == '?' && intermediates[1] == '$':
			p.handler.ReportPrivateMode(NewPrivateMode(uint16(nextParamOr(0))))
		default:
			p.logUnhandledCSI(params, intermediates, action)
		}
	case 'q':
		if len(intermediates) == 1 && intermediates[0] == ' ' {
			// DECSCUSR (CSI Ps SP q) -- Set Cursor Style.
			i := nextParamOr(0)
			var shape CursorShape
			switch i {
			case 0:
				// Reset to the terminal default shape. Distinct from
				// the blinking-block shape (param 1).
				shape = CursorShapeDefault
			case 1, 2:
				shape = CursorShapeBlock
			case 3, 4:
				shape = CursorShapeUnderline
			case 5, 6:
				shape = CursorShapeBeam
			default:
				p.logUnhandledCSI(params, intermediates, action)
			}
			blinking := i%2 == 1
			handler.SetCursorStyle(CursorStyle{Shape: shape, Blinking: blinking})
		} else {
			p.logUnhandledCSI(params, intermediates, action)
		}
	case 'r':
		top := nextParamOr(1)
		var bottom int
		var end bool
		if len(params) == 1 && len(params[0]) == 1 && params[0][0] != 0 {
			bottom = int(params[0][0])
		} else {
			end = true
		}
		handler.SetScrollingRegion(top, bottom, end)
	case 'S':
		handler.ScrollUp(nextParamOr(1))
	case 's':
		handler.SaveCursorPosition()
	case 'T':
		handler.ScrollDown(nextParamOr(1))
	case 't':
		param := int(nextParamOr(1))
		switch param {
		case 14:
			handler.TextAreaSizePixels()
		case 16:
			handler.CellSizePixels()
		case 18:
			handler.TextAreaSizeChars()
		case 22:
			handler.PushTitle()
		case 23:
			handler.PopTitle()
		default:
			p.logUnhandledCSI(params, intermediates, action)
		}
	case 'u':
		if len(intermediates) == 0 {
			handler.RestoreCursorPosition()
		} else {
			switch intermediates[0] {
			case '?':
				handler.ReportKeyboardMode()
			case '=':
				mode := NewKeyboardMode(uint8(nextParamOr(0)))
				var behavior KeyboardModesApplyBehavior
				switch nextParamOr(1) {
				case 3:
					behavior = KeyboardModesApplyBehaviorDifference
				case 2:
					behavior = KeyboardModesApplyBehaviorUnion
				default:
					behavior = KeyboardModesApplyBehaviorReplace
				}
				handler.SetKeyboardMode(mode, behavior)
			case '>':
				handler.PushKeyboardMode(NewKeyboardMode(uint8(nextParamOr(0))))
			case '<':
				// default is 1
				handler.PopKeyboardModes(nextParamOr(1))
			}
		}
	case 'X':
		handler.EraseChars(nextParamOr(1))
	case 'Z':
		handler.MoveBackwardTabs(nextParamOr(1))
	default:
		p.logUnhandledCSI(params, intermediates, action)
	}
}
func (p *driver) configureCharset(charset StandardCharset, intermediates []byte, b byte) {
	index := CharsetIndex(0)
	switch string(intermediates) {
	case "(":
		index = CharsetIndexG0
	case ")":
		index = CharsetIndexG1
	case "*":
		index = CharsetIndexG2
	case "+":
		index = CharsetIndexG3
	default:
		p.logUnhandledESC(intermediates, b)
		return
	}
	p.handler.ConfigureCharset(index, charset)
}

func (p *driver) ESCDispatch(intermediates []byte, ignore bool, b byte) {
	if len(intermediates) == 0 {
		switch b {
		case 'D':
			p.handler.Linefeed()
		case 'E':
			p.handler.Linefeed()
			p.handler.CarriageReturn()
		case 'H':
			p.handler.SetHorizontalTabstop()
		case 'M':
			p.handler.ReverseIndex()
		case 'Z':
			p.handler.IdentifyTerminal(false)
		case 'c':
			p.handler.ResetState()
		case '7':
			p.handler.SaveCursorPosition()
		case '8':
			p.handler.RestoreCursorPosition()
		case '=':
			p.handler.SetKeypadApplicationMode()
		case '>':
			p.handler.UnsetKeypadApplicationMode()
		case '\\':
		// string terminator, do nothing (parser handles as string terminator).
		default:
			p.logUnhandledESC(intermediates, b)
		}
		return
	}

	switch b {
	case 'B':
		p.configureCharset(StandardCharsetASCII, intermediates, b)
	case '0':
		p.configureCharset(StandardCharsetSpecialCharacterAndLineDrawing, intermediates, b)
	case '8':
		if len(intermediates) == 1 && intermediates[0] == '#' {
			p.handler.Decaln()
		} else {
			p.logUnhandledESC(intermediates, b)
		}
	default:
		p.logUnhandledESC(intermediates, b)
	}
}

func (p *driver) attrsFromSgrParameters(params [][]uint16) []Attr {
	attrs := p.sgrAttrs[:0]

	for i := 0; i < len(params); i++ {
		param := params[i]
		if len(param) == 0 {
			p.log(log.DebugLevel,
				"unhandled sgr parameter in CSI dispatch: params=%v", params)
			continue
		}

		if len(param) > 1 {
			switch param[0] {
			case 4:
				if len(param) != 2 {
					p.logUnhandledAttribute(params)
					continue
				}
				switch param[1] {
				case 0:
					attrs = append(attrs, Attr{Type: CancelUnderlineAttr})
				case 2:
					attrs = append(attrs, Attr{Type: DoubleUnderlineAttr})
				case 3:
					attrs = append(attrs, Attr{Type: UndercurlAttr})
				case 4:
					attrs = append(attrs, Attr{Type: DottedUnderlineAttr})
				case 5:
					attrs = append(attrs, Attr{Type: DashedUnderlineAttr})
				default:
					attrs = append(attrs, Attr{Type: UnderlineAttr})
				}
			case 38, 48, 58:
				attr := sgrColorAttr(param[0])
				color, ok := parseColonSGRColor(param[1:])
				if !ok {
					p.logUnhandledAttribute(params)
					continue
				}
				attrs = append(attrs, Attr{Type: attr, Color: color})
			default:
				p.logUnhandledAttribute(params)
			}
			continue
		}

		switch param[0] {
		case 0:
			attrs = append(attrs, Attr{Type: ResetAttr})
		case 1:
			attrs = append(attrs, Attr{Type: BoldAttr})
		case 2:
			attrs = append(attrs, Attr{Type: DimAttr})
		case 3:
			attrs = append(attrs, Attr{Type: ItalicAttr})
		case 4:
			attrs = append(attrs, Attr{Type: UnderlineAttr})
		case 5:
			attrs = append(attrs, Attr{Type: BlinkSlowAttr})
		case 6:
			attrs = append(attrs, Attr{Type: BlinkFastAttr})
		case 7:
			attrs = append(attrs, Attr{Type: ReverseAttr})
		case 8:
			attrs = append(attrs, Attr{Type: HiddenAttr})
		case 9:
			attrs = append(attrs, Attr{Type: StrikeAttr})
		case 21:
			attrs = append(attrs, Attr{Type: CancelBoldAttr})
		case 22:
			attrs = append(attrs, Attr{Type: CancelBoldDimAttr})
		case 23:
			attrs = append(attrs, Attr{Type: CancelItalicAttr})
		case 24:
			attrs = append(attrs, Attr{Type: CancelUnderlineAttr})
		case 25:
			attrs = append(attrs, Attr{Type: CancelBlinkAttr})
		case 27:
			attrs = append(attrs, Attr{Type: CancelReverseAttr})
		case 28:
			attrs = append(attrs, Attr{Type: CancelHiddenAttr})
		case 29:
			attrs = append(attrs, Attr{Type: CancelStrikeAttr})
		case 30:
			attrs = append(attrs, Attr{Type: ForegroundAttr, Color: term.ColorBlack})
		case 31:
			attrs = append(attrs, Attr{Type: ForegroundAttr, Color: term.ColorMaroon})
		case 32:
			attrs = append(attrs, Attr{Type: ForegroundAttr, Color: term.ColorGreen})
		case 33:
			attrs = append(attrs, Attr{Type: ForegroundAttr, Color: term.ColorOlive})
		case 34:
			attrs = append(attrs, Attr{Type: ForegroundAttr, Color: term.ColorNavy})
		case 35:
			attrs = append(attrs, Attr{Type: ForegroundAttr, Color: term.ColorPurple})
		case 36:
			attrs = append(attrs, Attr{Type: ForegroundAttr, Color: term.ColorTeal})
		case 37:
			attrs = append(attrs, Attr{Type: ForegroundAttr, Color: term.ColorSilver})
		case 38, 48, 58:
			attr := sgrColorAttr(param[0])
			n, color, ok := parseSGRColor(p.headParams(params[i+1:]))
			i += n
			if ok {
				attrs = append(attrs, Attr{Type: attr, Color: color})
				continue
			}
			p.logUnhandledAttribute(params)
		case 39:
			attrs = append(attrs, Attr{Type: ForegroundAttr, Color: term.ColorDefault})
		case 40:
			attrs = append(attrs, Attr{Type: BackgroundAttr, Color: term.ColorBlack})
		case 41:
			attrs = append(attrs, Attr{Type: BackgroundAttr, Color: term.ColorMaroon})
		case 42:
			attrs = append(attrs, Attr{Type: BackgroundAttr, Color: term.ColorGreen})
		case 43:
			attrs = append(attrs, Attr{Type: BackgroundAttr, Color: term.ColorOlive})
		case 44:
			attrs = append(attrs, Attr{Type: BackgroundAttr, Color: term.ColorNavy})
		case 45:
			attrs = append(attrs, Attr{Type: BackgroundAttr, Color: term.ColorPurple})
		case 46:
			attrs = append(attrs, Attr{Type: BackgroundAttr, Color: term.ColorTeal})
		case 47:
			attrs = append(attrs, Attr{Type: BackgroundAttr, Color: term.ColorSilver})
		case 49:
			attrs = append(attrs, Attr{Type: BackgroundAttr, Color: term.ColorDefault})
		case 59:
			attrs = append(attrs, Attr{Type: UnderlineColorAttr, Color: term.ColorDefault})
		case 90:
			attrs = append(attrs, Attr{Type: ForegroundAttr, Color: term.ColorGray})
		case 91:
			attrs = append(attrs, Attr{Type: ForegroundAttr, Color: term.ColorRed})
		case 92:
			attrs = append(attrs, Attr{Type: ForegroundAttr, Color: term.ColorLime})
		case 93:
			attrs = append(attrs, Attr{Type: ForegroundAttr, Color: term.ColorYellow})
		case 94:
			attrs = append(attrs, Attr{Type: ForegroundAttr, Color: term.ColorBlue})
		case 95:
			attrs = append(attrs, Attr{Type: ForegroundAttr, Color: term.ColorFuchsia})
		case 96:
			attrs = append(attrs, Attr{Type: ForegroundAttr, Color: term.ColorAqua})
		case 97:
			attrs = append(attrs, Attr{Type: ForegroundAttr, Color: term.ColorWhite})
		case 100:
			attrs = append(attrs, Attr{Type: BackgroundAttr, Color: term.ColorGray})
		case 101:
			attrs = append(attrs, Attr{Type: BackgroundAttr, Color: term.ColorRed})
		case 102:
			attrs = append(attrs, Attr{Type: BackgroundAttr, Color: term.ColorLime})
		case 103:
			attrs = append(attrs, Attr{Type: BackgroundAttr, Color: term.ColorYellow})
		case 104:
			attrs = append(attrs, Attr{Type: BackgroundAttr, Color: term.ColorBlue})
		case 105:
			attrs = append(attrs, Attr{Type: BackgroundAttr, Color: term.ColorFuchsia})
		case 106:
			attrs = append(attrs, Attr{Type: BackgroundAttr, Color: term.ColorAqua})
		case 107:
			attrs = append(attrs, Attr{Type: BackgroundAttr, Color: term.ColorWhite})
		default:
			p.logUnhandledAttribute(params)
		}
	}

	p.sgrAttrs = attrs
	return attrs
}

func (p *driver) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "parser.driver").
		Logf(level, msg, args...)
}

func (p *driver) logUnhandledCSI(
	params [][]uint16, intermediates []byte, action rune,
) {
	p.log(log.DebugLevel, "Unhandled CSI action=%q, params=%v, intermediates=%v",
		action, params, intermediates)
}

func (p *driver) logUnexpectedParamsLength(
	params [][]uint16, intermediates []byte, action rune,
) {
	p.log(log.DebugLevel, "Unexpected params length in CSI action=%q, "+
		"params=%v, intermediates=%v", action, params, intermediates)
}

func (p *driver) logUnhandledESC(
	intermediates []byte, b byte,
) {
	p.log(log.DebugLevel, "Unhandled ESC ints=%v, byte=%c", intermediates, b)
}

func (p *driver) logUnhandledOSC(params [][]byte) {
	var buf strings.Builder
	for _, items := range params {
		buf.WriteByte('[')
		for _, item := range items {
			buf.WriteString(strconv.QuoteRune(rune(item)))
		}
		buf.WriteString("],")
	}
	p.log(log.DebugLevel, "unhandled osc_dispatch: [%s]", buf.String())
}

func (p *driver) logUnhandledAttribute(params [][]uint16) {
	p.log(log.DebugLevel, "Unhandled Attribute in CSI dispatch: params=%v", params)
}

func sgrColorAttr(param uint16) AttrType {
	switch param {
	case 38:
		return ForegroundAttr
	case 48:
		return BackgroundAttr
	default:
		return UnderlineColorAttr
	}
}

func parseColonSGRColor(params []uint16) (term.Color, bool) {
	if len(params) == 2 && params[0] == 5 && params[1] <= 255 {
		return term.PaletteColor(int(params[1])), true
	}
	if len(params) == 4 && params[0] == 2 {
		return newSGRRGB(params[1:])
	}
	if len(params) == 5 && params[0] == 2 && params[1] == 0 {
		return newSGRRGB(params[2:])
	}
	return 0, false
}

func newSGRRGB(params []uint16) (term.Color, bool) {
	if len(params) != 3 || params[0] > 255 || params[1] > 255 || params[2] > 255 {
		return 0, false
	}
	return term.NewRGBColor(int32(params[0]), int32(params[1]), int32(params[2])), true
}

// parse a color
func parseSGRColor(params []uint16) (int, term.Color, bool) {
	if len(params) == 0 {
		return 0, 0, false
	}
	switch params[0] {
	case 2:
		n := min(4, len(params))
		if len(params) < 4 {
			return n, 0, false
		}
		color, ok := newSGRRGB(params[1:4])
		return 4, color, ok
	case 5:
		n := min(2, len(params))
		if len(params) < 2 || params[1] > 255 {
			return n, 0, false
		}
		return 2, term.PaletteColor(int(params[1])), true
	default:
		return 1, 0, false
	}
}

// headParams flattens params to their leading value. The result aliases
// a scratch buffer that the next call overwrites.
func (p *driver) headParams(params [][]uint16) []uint16 {
	ret := p.sgrHeadParams[:0]
	for _, param := range params {
		if len(param) != 0 {
			ret = append(ret, param[0])
		}
	}
	p.sgrHeadParams = ret
	return ret
}
