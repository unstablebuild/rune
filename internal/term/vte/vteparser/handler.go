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
	"github.com/unstablebuild/rune-go-sdk/term"
)

// Handler abstracts a terminal TUI implementation.
type Handler interface {
	// SetTitle sets the terminal's window title.
	SetTitle(title string)

	// SetCursorStyle Set the cell cursor style.
	SetCursorStyle(style CursorStyle)

	// SetCursorShape sets the cell cursor shape.
	SetCursorShape(shape CursorShape)

	// Input sets the character to be displayed at the current cell.
	Input(c rune)

	// InputRun displays a run of printable UTF-8 text in a single call.
	// Each decoded codepoint carries the same semantics as Input; the
	// batch avoids per-character dispatch and locking on bulk output
	// streams. The run never ends mid-sequence.
	InputRun(run []byte)

	// Goto sets the cell cursor to the given position.
	Goto(line int, col int)

	// GotoLine sets the cell cursor to specific row.
	GotoLine(line int)

	// GotoCol sets the cell cursor to specific column.
	GotoCol(col int)

	// Insertblank inserts blank characters in current line starting from cell cursor.
	InsertBlank(count int)

	// MoveUp moves up the cell cursor `rows`.
	MoveUp(rows int)

	// MoveDown moves down the cell cursor `rows`.
	MoveDown(rows int)

	// IdentifyTerminal identifies the terminal on the pty stream.
	IdentifyTerminal(identifySecondary bool)

	// DeviceStatus writes the device status on the pty stream.
	DeviceStatus(status int)

	// MoveForward moves the cell cursor forward `cols`.
	MoveForward(cols int)

	// MoveBackward moves the cell cursor backward `cols`.
	MoveBackward(cols int)

	// MoveDownAndCR moves the cell cursor down `rows` and set to column 1.
	MoveDownAndCR(rows int)

	// MoveUpAndCR moves the cell cursor up `rows` and set to column 1.
	MoveUpAndCR(rows int)

	// PutTab sets the current cell cursor's position to be `count` number of tabs.
	PutTab()

	// Backspace deletes backward one character.
	Backspace()

	// CarriageReturn performes a carriage return.
	CarriageReturn()

	// Linefeed performs a line feed.
	Linefeed()

	// Bell ring the system bell.
	Bell()

	// Substitute subsitutes the char under the cell cursor.
	Substitute()

	// SetHorizontalTabstop sets current position as a tabstop.
	SetHorizontalTabstop()

	// ScrollUp scrolls up `rows` rows.
	ScrollUp(rows int)

	// ScrollDown scrolls down `rows` rows.
	ScrollDown(rows int)

	// InsertBlankLines insert `count` blank lines.
	InsertBlankLines(count int)

	// DeleteLines delete `count` lines.
	DeleteLines(count int)

	// EraseChars erases `count` chars in the current line following the cursor.
	//
	// Erase means resetting to the default state (default colors, no content,
	// no mode flags).
	EraseChars(count int)

	// DeleteChars deletes `count` chars.
	//
	// Deleting a character is like the delete key on the keyboard - everything
	// to the right of the deleted things is shifted left.
	DeleteChars(count int)

	// MoveBackwardTabs moves backward `count` tabs.
	MoveBackwardTabs(count int)

	// MoveForwardTabs moves forward `count` tabs.
	MoveForwardTabs(count int)

	// SaveCursorPosition saves the current cursor position.
	SaveCursorPosition()

	// RestoreCursorPosition restores the cursor position.
	RestoreCursorPosition()

	// ClearLine clears the current line.
	ClearLine(mode LineClearMode)

	// ClearScreen clears the screen.
	ClearScreen(mode ClearMode)

	// ClearTabs clears tab stops.
	ClearTabs(mode TabulationClearMode)

	// ResetState resets the terminal's state.
	ResetState()

	// ReverseIndex moves the active position to the same horizontal position on the
	// preceding line. If the active position is at the top margin, a scroll
	// down is performed.
	ReverseIndex()

	// TerminalAttribute sets a terminal attribute at the current cursor position.
	TerminalAttribute(attr Attr)

	// SetMode sets an vteparser.Mode.
	SetMode(mode Mode)

	// UnsetMode unsets an vteparser.Mode.
	UnsetMode(mode Mode)

	// ReportMode reports an vteparser.Mode back into the pty stream.
	ReportMode(mode Mode)

	// SetPrivate sets an vteparser.PrivateMode.
	SetPrivateMode(PrivateMode)

	// UnsetPrivate unsets an vteparser.PrivateMode.
	UnsetPrivateMode(PrivateMode)

	// ReportPrivateMode (DECRPM) reports an vteparser.PrivateMode back into the pty stream.
	ReportPrivateMode(PrivateMode)

	// SetScrollingRegion (DECSTBM) sets the terminal scrolling region.
	SetScrollingRegion(top, bottom int, end bool)

	// SetKeypadApplicationMode (DECKPAM) sets the keypad to applications
	// mode (ESCape instead of digits).
	SetKeypadApplicationMode()

	// UnsetKeypadApplicationMode (DECKPNM) sets the keypad to numeric
	// mode (digits instead of ESCape seq).
	UnsetKeypadApplicationMode()

	// SetActiveCharset sets one of the graphic character sets,
	// G0 to G3, as the active charset.
	//
	// 'Invoke' one of G0 to G3 in the GL area. Also referred to as shift in,
	// shift out and locking shift depending on the set being activated.
	SetActiveCharset(index CharsetIndex)

	// ConfigureCharset assigns a graphic character set to G0, G1, G2 or G3.
	//
	// 'Designate' a graphic character set as one of G0 to G3 so that it can
	// later be 'invoked' by `SetActiveCharset`.
	ConfigureCharset(index CharsetIndex, charset StandardCharset)

	// ClipboardStore stores data into the clipboard.
	ClipboardStore(register int, data []byte)

	// ClipboardLoad loads data from the clipboard.
	ClipboardLoad(register int, data string)

	// Decaln runs the decaln routine.
	Decaln()

	// PushTitle pushes a title onto the stack.
	PushTitle()

	// PopTitle pops the last title from the stack.
	PopTitle()

	// TextAreaSizePixels reports text area size in pixels.
	TextAreaSizePixels()

	// TextAreaSizeChars reports text area size in characters.
	TextAreaSizeChars()

	// CellSizePixels reports the size of a character cell in pixels.
	CellSizePixels()

	// SetHyperlink sets hyperlink.
	SetHyperlink(link *Hyperlink)

	// ReportKeyboardMode reports current keyboard mode.
	ReportKeyboardMode()

	// PushKeyboardMode pushes the keyboard mode into the keyboard mode stack.
	PushKeyboardMode(mode KeyboardMode)

	// PopKeyboardModes pops the given amount of keyboard modes
	// from the keyboard mode stack.
	PopKeyboardModes(count int)

	// SetKeyboardMode sets the [`keyboard mode`] using the given [`behavior`].
	SetKeyboardMode(mode KeyboardMode, behavior KeyboardModesApplyBehavior)

	// SetModifyOtherKeys sets XTerm's [`ModifyOtherKeys`] option.
	SetModifyOtherKeys(mode ModifyOtherKeysMode)

	// ReportModifyOtherKeys report XTerm's [`ModifyOtherKeys`] state
	// back into the pty stream.
	ReportModifyOtherKeys()

	// GraphicsCommand handles a kitty graphics protocol command. data
	// is the APC string after its leading 'G': the control data and,
	// after an optional ';', the base64 payload. It is only valid for
	// the duration of the call.
	GraphicsCommand(data []byte)
}

// KeyboardMode is the vte keyboard mode.
type KeyboardMode uint8

const (
	// KeyboardModeNoMode No keyboard protocol mode is set.
	KeyboardModeNoMode KeyboardMode = 0b0000_0000
	// KeyboardModeDisambiguateEscCodes reports `Esc`, `alt` + `key`, `ctrl` + `key`,
	// `ctrl` + `alt` + `key`, `shift` + `alt` + `key` keys using
	// `CSI u` sequence instead of raw ones.
	KeyboardModeDisambiguateEscCodes KeyboardMode = 0b0000_0001
	// KeyboardModeReportEventTypes reports key presses, release, and repetition
	// alongside the escape. Key events that result in text are reported as
	// plain UTF-8, unless KeyboardModeReportAllKeysAsEsc is enabled.
	KeyboardModeReportEventTypes KeyboardMode = 0b0000_0010
	// KeyboardModeReportAlternateKeys reports shifted key an dbase layout key.
	KeyboardModeReportAlternateKeys KeyboardMode = 0b0000_0100
	// KeyboardModeReportAllKeysAsEsc reports every key as an escape sequence.
	KeyboardModeReportAllKeysAsEsc KeyboardMode = 0b0000_1000
	// KeyboardModeReportAssociatedText reports the text generated by the key event.
	KeyboardModeReportAssociatedText KeyboardMode = 0b0001_0000
)

// KeyboardModesApplyBehavior encodes how to apply parsed keyboard modes.
type KeyboardModesApplyBehavior uint8

const (
	// KeyboardModesApplyBehaviorReplace replaces the active flags with the new ones.
	KeyboardModesApplyBehaviorReplace KeyboardModesApplyBehavior = iota
	// KeyboardModesApplyBehaviorUnion rerges the given flags with currently active ones.
	KeyboardModesApplyBehaviorUnion
	// KeyboardModesApplyBehaviorDifference removes the given flags from the active ones.
	KeyboardModesApplyBehaviorDifference
)

// Hyperlink is a link.
type Hyperlink struct {
	// Identifier for the given hyperlink.
	ID string
	// Resource identifier of the hyperlink.
	URI string
}

// CursorShape represents the terminal cursor shape.
type CursorShape int

const (
	// CursorShapeBlock renders the cursor as a filled block.
	CursorShapeBlock CursorShape = iota
	// CursorShapeUnderline renders the cursor as a line under the character.
	CursorShapeUnderline
	// CursorShapeBeam renders the cursor as a line next to the cursor.
	CursorShapeBeam
	// CursorShapeHollowBlock renders the cursor as a shallow block.
	CursorShapeHollowBlock
	// CursorShapeHidden renders no cursor.
	CursorShapeHidden
	// CursorShapeDefault resets the cursor to the terminal's default
	// shape. It is emitted for DECSCUSR with parameter 0 (or no
	// parameter), which per the spec means "reset to default" and must
	// not be conflated with the blinking-block shape (parameter 1).
	CursorShapeDefault
)

// CursorStyle represents the terminal cursor configuration.
type CursorStyle struct {
	Shape    CursorShape
	Blinking bool
}

// Mode represents terminal modes.
type Mode int

const (
	// ModeInsert is IRM mode: https://vt100.net/docs/vt510-rm/IRM.html.
	ModeInsert Mode = 4
	// ModeLineFeedNewLine is LNM mode: https://vt100.net/docs/vt510-rm/LNM.html
	ModeLineFeedNewLine Mode = 20
)

// NewMode maps a param to a Mode.
func NewMode(param uint16) Mode {
	// do not validate here, so we avoid having to make Mode
	// into a struct that holds the raw value as well as the named mode.
	return Mode(param)
}

// NewPrivateMode maps a param to a (private) Mode.
func NewPrivateMode(param uint16) PrivateMode {
	// do not validate here, so we avoid having to make PrivateMode
	// into a struct that holds the raw value as well as the named mode.
	return PrivateMode(param)
}

// LineClearMode is a mode of clearing a line,
// relative to the cell cursor.
type LineClearMode int

const (
	// LineClearModeRight clears right of cursor.
	LineClearModeRight LineClearMode = iota
	// LineClearModeLeft clears left of cursor.
	LineClearModeLeft
	// LineClearModeAll clears an entire line.
	LineClearModeAll
)

// ClearMode for clearing a terminal, relative to the cell cursor.
type ClearMode int

const (
	// ClearModeBelow clears below cursor.
	ClearModeBelow ClearMode = iota
	// ClearModeAbove clears above cursor.
	ClearModeAbove
	// ClearModeAll clears entire terminal.
	ClearModeAll
	// ClearModeSaved clears 'saved' lines (scrollback).
	ClearModeSaved
)

// TabulationClearMode for clearing tab stops.
type TabulationClearMode int

const (
	// TabulationClearModeCurrent clears stop under cursor.
	TabulationClearModeCurrent TabulationClearMode = iota
	// TabulationClearModeAll clears all stops.
	TabulationClearModeAll
)

// Attr is a terminal cell attributes.
type Attr struct {
	Type  AttrType
	Color term.Color
}

// AttrType encodes the type of attribute in an Attr.
type AttrType uint8

const (
	// ResetAttr clears all special abilities.
	ResetAttr AttrType = iota
	// BoldAttr sets the text as bold text.
	BoldAttr
	// DimAttr dims the color.
	DimAttr
	// ItalicAttr sets the text as italic text.
	ItalicAttr
	// UnderlineAttr sets the text with an underline.
	UnderlineAttr
	// DoubleUnderlineAttr sets the text with a double underline.
	DoubleUnderlineAttr
	// UndercurlAttr sets the text with an undercurl line.
	UndercurlAttr
	// DottedUnderlineAttr sets the text with a dotted underline.
	DottedUnderlineAttr
	// DashedUnderlineAttr sets the text with a dashed underline.
	DashedUnderlineAttr
	// BlinkSlowAttr sets the cursor to blink slowly.
	BlinkSlowAttr
	// BlinkFastAttr sets the cursor to blink fast.
	BlinkFastAttr
	// ReverseAttr inverts colors.
	ReverseAttr
	// HiddenAttr hides all colors and attributes.
	HiddenAttr
	// StrikeAttr strikes out the text.
	StrikeAttr
	// CancelBoldAttr cancels bold.
	CancelBoldAttr
	// CancelBoldDimAttr cancels bold and dim.
	CancelBoldDimAttr
	// CancelItalicAttr cancels italic.
	CancelItalicAttr
	// CancelUnderlineAttr cancels all underlines.
	CancelUnderlineAttr
	// CancelBlinkAttr cancels blink.
	CancelBlinkAttr
	// CancelReverseAttr cancels inversion.
	CancelReverseAttr
	// CancelHiddenAttr cancels text hiding.
	CancelHiddenAttr
	// CancelStrikeAttr cancels strikeout.
	CancelStrikeAttr
	// ForegroundAttr sets the foreground color.
	ForegroundAttr
	// BackgroundAttr sets the background color.
	BackgroundAttr
	// UnderlineColorAttr sets the underline color attributes.
	UnderlineColorAttr
)

// PrivateMode defines named private DEC modes as constants.
type PrivateMode int16

// List of named private DEC modes.
const (
	PrivateModeCursorKeys                    PrivateMode = 1
	PrivateModeColumnMode                    PrivateMode = 3
	PrivateModeScreen                        PrivateMode = 5
	PrivateModeOrigin                        PrivateMode = 6
	PrivateModeLineWrap                      PrivateMode = 7
	PrivateModeBlinkingCursor                PrivateMode = 12
	PrivateModeShowCursor                    PrivateMode = 25
	PrivateModeReportMouseClicks             PrivateMode = 1000
	PrivateModeReportCellMouseMotion         PrivateMode = 1002
	PrivateModeReportAllMouseMotion          PrivateMode = 1003
	PrivateModeReportFocusInOut              PrivateMode = 1004
	PrivateModeUtf8Mouse                     PrivateMode = 1005
	PrivateModeSgrMouse                      PrivateMode = 1006
	PrivateModeAlternateScroll               PrivateMode = 1007
	PrivateModeUrgencyHints                  PrivateMode = 1042
	PrivateModeSwapScreenAndSetRestoreCursor PrivateMode = 1049
	PrivateModeBracketedPaste                PrivateMode = 2004
	PrivateModeSyncUpdate                    PrivateMode = 2026
)

// ModifyOtherKeysMode represents one of Xterm's `ModifyOtherKeys` option.
type ModifyOtherKeysMode uint8

// List of ModifyOtherKeysMode.
const (
	ModifyOtherKeysReset ModifyOtherKeysMode = iota
	ModifyOtherKeysEnableExceptWellDefined
	ModifyOtherKeysEnableAll
)

// CharsetIndex represents the index identifider of a StandardCharset.
type CharsetIndex int

// List of charset indexes
const (
	CharsetIndexG0 CharsetIndex = iota
	CharsetIndexG1
	CharsetIndexG2
	CharsetIndexG3
)

// NewKeyboardMode wraps param into a Keyboard mode without any validation.
func NewKeyboardMode(param uint8) KeyboardMode {
	return KeyboardMode(param)
}
