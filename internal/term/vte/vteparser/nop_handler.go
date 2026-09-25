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

// NopHandler returns an implementation of Handler that does nothing.
func NopHandler() Handler {
	return nopHandler{}
}

type nopHandler struct{}

func (h nopHandler) SetTitle(title string) {

}

func (h nopHandler) SetCursorStyle(style CursorStyle) {

}

func (h nopHandler) SetCursorShape(shape CursorShape) {

}

func (h nopHandler) Input(c rune) {

}

func (h nopHandler) InputRun(run []byte) {

}

func (h nopHandler) Goto(line int, col int) {

}

func (h nopHandler) GotoLine(line int) {

}

func (h nopHandler) GotoCol(col int) {

}

func (h nopHandler) InsertBlank(count int) {

}

func (h nopHandler) MoveUp(rows int) {

}

func (h nopHandler) MoveDown(rows int) {

}

func (h nopHandler) IdentifyTerminal(identifySecondary bool) {

}

func (h nopHandler) DeviceStatus(status int) {

}

func (h nopHandler) MoveForward(cols int) {

}

func (h nopHandler) MoveBackward(cols int) {

}

func (h nopHandler) MoveDownAndCR(row int) {

}

func (h nopHandler) MoveUpAndCR(row int) {

}

func (h nopHandler) PutTab() {

}

func (h nopHandler) Backspace() {

}

func (h nopHandler) CarriageReturn() {

}

func (h nopHandler) Linefeed() {

}

func (h nopHandler) Bell() {

}

func (h nopHandler) Substitute() {

}

func (h nopHandler) Newline() {

}

func (h nopHandler) SetHorizontalTabstop() {

}

func (h nopHandler) ScrollUp(rows int) {

}

func (h nopHandler) ScrollDown(rows int) {

}

func (h nopHandler) InsertBlankLines(count int) {

}

func (h nopHandler) DeleteLines(count int) {

}

func (h nopHandler) EraseChars(count int) {

}

func (h nopHandler) DeleteChars(count int) {

}

func (h nopHandler) MoveBackwardTabs(count int) {

}

func (h nopHandler) MoveForwardTabs(count int) {

}

func (h nopHandler) SaveCursorPosition() {

}

func (h nopHandler) RestoreCursorPosition() {

}

func (h nopHandler) ClearLine(mode LineClearMode) {

}

func (h nopHandler) ClearScreen(mode ClearMode) {

}

func (h nopHandler) ClearTabs(mode TabulationClearMode) {

}

func (h nopHandler) ResetState() {

}

func (h nopHandler) ReverseIndex() {

}

func (h nopHandler) TerminalAttribute(attr Attr) {

}

func (h nopHandler) SetMode(mode Mode) {

}

func (h nopHandler) UnsetMode(mode Mode) {

}

func (h nopHandler) ReportMode(mode Mode) {

}

func (h nopHandler) SetPrivateMode(mode PrivateMode) {

}

func (h nopHandler) UnsetPrivateMode(mode PrivateMode) {

}

func (h nopHandler) ReportPrivateMode(mode PrivateMode) {

}

func (h nopHandler) SetScrollingRegion(top, bottom int, end bool) {

}

func (h nopHandler) SetKeypadApplicationMode() {

}

func (h nopHandler) UnsetKeypadApplicationMode() {

}

func (h nopHandler) SetActiveCharset(index CharsetIndex) {

}

func (h nopHandler) ConfigureCharset(index CharsetIndex, charset StandardCharset) {

}

func (h nopHandler) ClipboardStore(register int, data []byte) {

}

func (h nopHandler) ClipboardLoad(register int, data string) {

}

func (h nopHandler) Decaln() {

}

func (h nopHandler) PushTitle() {

}

func (h nopHandler) PopTitle() {

}

func (h nopHandler) TextAreaSizePixels() {

}

func (h nopHandler) TextAreaSizeChars() {

}

func (h nopHandler) CellSizePixels() {

}

func (h nopHandler) SetHyperlink(link *Hyperlink) {

}

func (h nopHandler) ReportKeyboardMode() {

}

func (h nopHandler) PushKeyboardMode(mode KeyboardMode) {

}

func (h nopHandler) PopKeyboardModes(count int) {

}

func (h nopHandler) SetKeyboardMode(
	mode KeyboardMode, behavior KeyboardModesApplyBehavior,
) {

}

func (h nopHandler) SetModifyOtherKeys(mode ModifyOtherKeysMode) {

}

func (h nopHandler) ReportModifyOtherKeys() {

}

func (h nopHandler) GraphicsCommand(data []byte) {

}
