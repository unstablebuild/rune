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
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/term/graphemecluster"
)

// HandlerWithLogging wraps a Handler and adds trace logging.
func HandlerWithLogging(keyClass string, h Handler) Handler {
	return loggingHandler{keyClass: keyClass, h: h}
}

type loggingHandler struct {
	h        Handler
	keyClass string
}

func (h loggingHandler) SetTitle(title string) {
	h.log("SetTitle %s", title)
	h.h.SetTitle(title)
}

func (h loggingHandler) SetCursorStyle(style CursorStyle) {
	h.log("SetCursorStyle %v", style)
	h.h.SetCursorStyle(style)

}

func (h loggingHandler) SetCursorShape(shape CursorShape) {
	h.log("SetCursorShape %v", shape)
	h.h.SetCursorShape(shape)

}

func (h loggingHandler) Input(c rune) {
	h.log("Input '%c', width=%d", c, graphemecluster.StringWidth(string(c)))
	h.h.Input(c)

}

func (h loggingHandler) InputRun(run []byte) {
	h.log("InputRun %q", run)
	h.h.InputRun(run)

}

func (h loggingHandler) Goto(line int, col int) {
	h.log("Goto line=%d col=%d", line, col)
	h.h.Goto(line, col)

}

func (h loggingHandler) GotoLine(line int) {
	h.log("GotoLine %d", line)
	h.h.GotoLine(line)

}

func (h loggingHandler) GotoCol(col int) {
	h.log("GotoCol %d", col)
	h.h.GotoCol(col)

}

func (h loggingHandler) InsertBlank(count int) {
	h.log("InsertBlank %d", count)
	h.h.InsertBlank(count)

}

func (h loggingHandler) MoveUp(rows int) {
	h.log("MoveUp %d", rows)
	h.h.MoveUp(rows)

}

func (h loggingHandler) MoveDown(rows int) {
	h.log("MoveDown %d", rows)
	h.h.MoveDown(rows)

}

func (h loggingHandler) IdentifyTerminal(identifySecondary bool) {
	h.log("IdentifyTerminal %t", identifySecondary)
	h.h.IdentifyTerminal(identifySecondary)

}

func (h loggingHandler) DeviceStatus(status int) {
	h.log("DeviceStatus %d", status)
	h.h.DeviceStatus(status)

}

func (h loggingHandler) MoveForward(cols int) {
	h.log("MoveForward %d", cols)
	h.h.MoveForward(cols)

}

func (h loggingHandler) MoveBackward(cols int) {
	h.log("MoveBackward %d", cols)
	h.h.MoveBackward(cols)

}

func (h loggingHandler) MoveDownAndCR(row int) {
	h.log("MoveDownAndCR %d", row)
	h.h.MoveDownAndCR(row)

}

func (h loggingHandler) MoveUpAndCR(row int) {
	h.log("MoveUpAndCR %d", row)
	h.h.MoveUpAndCR(row)

}

func (h loggingHandler) PutTab() {
	h.log("PutTab")
	h.h.PutTab()

}

func (h loggingHandler) Backspace() {
	h.log("Backspace")
	h.h.Backspace()

}

func (h loggingHandler) CarriageReturn() {
	h.log("CarriageReturn")
	h.h.CarriageReturn()

}

func (h loggingHandler) Linefeed() {
	h.log("Linefeed")
	h.h.Linefeed()

}

func (h loggingHandler) Bell() {
	h.log("Bell ")
	h.h.Bell()

}

func (h loggingHandler) Substitute() {
	h.log("Substitute ")
	h.h.Substitute()

}

func (h loggingHandler) SetHorizontalTabstop() {
	h.log("SetHorizontalTabstop ")
	h.h.SetHorizontalTabstop()

}

func (h loggingHandler) ScrollUp(rows int) {
	h.log("ScrollUp %d", rows)
	h.h.ScrollUp(rows)

}

func (h loggingHandler) ScrollDown(rows int) {
	h.log("ScrollDown %d", rows)
	h.h.ScrollDown(rows)

}

func (h loggingHandler) InsertBlankLines(count int) {
	h.log("InsertBlankLines %d", count)
	h.h.InsertBlankLines(count)

}

func (h loggingHandler) DeleteLines(count int) {
	h.log("DeleteLines %d", count)
	h.h.DeleteLines(count)

}

func (h loggingHandler) EraseChars(count int) {
	h.log("EraseChars %d", count)
	h.h.EraseChars(count)

}

func (h loggingHandler) DeleteChars(count int) {
	h.log("DeleteChars %d", count)
	h.h.DeleteChars(count)

}

func (h loggingHandler) MoveBackwardTabs(count int) {
	h.log("MoveBackwardTabs %d", count)
	h.h.MoveBackwardTabs(count)

}

func (h loggingHandler) MoveForwardTabs(count int) {
	h.log("MoveForwardTabs %d", count)
	h.h.MoveForwardTabs(count)

}

func (h loggingHandler) SaveCursorPosition() {
	h.log("SaveCursorPosition ")
	h.h.SaveCursorPosition()

}

func (h loggingHandler) RestoreCursorPosition() {
	h.log("RestoreCursorPosition ")
	h.h.RestoreCursorPosition()

}

func (h loggingHandler) ClearLine(mode LineClearMode) {
	h.log("ClearLine %v", mode)
	h.h.ClearLine(mode)

}

func (h loggingHandler) ClearScreen(mode ClearMode) {
	h.log("ClearScreen %v", mode)
	h.h.ClearScreen(mode)

}

func (h loggingHandler) ClearTabs(mode TabulationClearMode) {
	h.log("ClearTabs %v", mode)
	h.h.ClearTabs(mode)

}

func (h loggingHandler) ResetState() {
	h.log("ResetState ")
	h.h.ResetState()

}

func (h loggingHandler) ReverseIndex() {
	h.log("ReverseIndex ")
	h.h.ReverseIndex()

}

func (h loggingHandler) TerminalAttribute(attr Attr) {
	h.log("TerminalAttribute %v", attr)
	h.h.TerminalAttribute(attr)

}

func (h loggingHandler) SetMode(mode Mode) {
	h.log("SetMode %v", mode)
	h.h.SetMode(mode)

}

func (h loggingHandler) UnsetMode(mode Mode) {
	h.log("UnsetMode %v", mode)
	h.h.UnsetMode(mode)

}

func (h loggingHandler) ReportMode(mode Mode) {
	h.log("ReportMode %v", mode)
	h.h.ReportMode(mode)

}

func (h loggingHandler) SetPrivateMode(mode PrivateMode) {
	h.log("SetPrivateMode %v", mode)
	h.h.SetPrivateMode(mode)

}

func (h loggingHandler) UnsetPrivateMode(mode PrivateMode) {
	h.log("UnsetPrivateMode %v", mode)
	h.h.UnsetPrivateMode(mode)

}

func (h loggingHandler) ReportPrivateMode(mode PrivateMode) {
	h.log("ReportPrivateMode %v", mode)
	h.h.ReportPrivateMode(mode)

}

func (h loggingHandler) SetScrollingRegion(top, bottom int, end bool) {
	h.log("SetScrollingRegion top=%d, bottom=%d, end=%t", top, bottom, end)
	h.h.SetScrollingRegion(top, bottom, end)

}

func (h loggingHandler) SetKeypadApplicationMode() {
	h.log("SetKeypadApplicationMode ")
	h.h.SetKeypadApplicationMode()

}

func (h loggingHandler) UnsetKeypadApplicationMode() {
	h.log("UnsetKeypadApplicationMode ")
	h.h.UnsetKeypadApplicationMode()

}

func (h loggingHandler) SetActiveCharset(index CharsetIndex) {
	h.log("SetActiveCharset %v", index)
	h.h.SetActiveCharset(index)

}

func (h loggingHandler) ConfigureCharset(index CharsetIndex, charset StandardCharset) {
	h.log("ConfigureCharset index=%v charset=%v", index, charset)

}

func (h loggingHandler) ClipboardStore(register int, data []byte) {
	h.log("ClipboardStore %d data=%d", register, len(data))
}

func (h loggingHandler) ClipboardLoad(register int, data string) {
	h.log("ClipboardLoad %d data=%d", register, len(data))

}

func (h loggingHandler) Decaln() {
	h.log("Decaln ")

}

func (h loggingHandler) PushTitle() {
	h.log("PushTitle ")
	h.h.PushTitle()

}

func (h loggingHandler) PopTitle() {
	h.log("PopTitle ")
	h.h.PopTitle()

}

func (h loggingHandler) TextAreaSizePixels() {
	h.log("TextAreaSizePixels ")
	h.h.TextAreaSizePixels()

}

func (h loggingHandler) TextAreaSizeChars() {
	h.log("TextAreaSizeChars ")
	h.h.TextAreaSizeChars()

}

func (h loggingHandler) CellSizePixels() {
	h.log("CellSizePixels ")
	h.h.CellSizePixels()
}

func (h loggingHandler) SetHyperlink(link *Hyperlink) {
	h.log("SetHyperlink ")
	h.h.SetHyperlink(link)

}

func (h loggingHandler) ReportKeyboardMode() {
	h.log("ReportKeyboardMode ")
	h.h.ReportKeyboardMode()

}

func (h loggingHandler) PushKeyboardMode(mode KeyboardMode) {
	h.log("PushKeyboardMode %v", mode)
	h.h.PushKeyboardMode(mode)

}

func (h loggingHandler) PopKeyboardModes(count int) {
	h.log("PopKeyboardModes %d", count)
	h.h.PopKeyboardModes(count)

}

func (h loggingHandler) SetKeyboardMode(
	mode KeyboardMode, behavior KeyboardModesApplyBehavior,
) {
	h.log("SetKeyboardMode %v", mode)
	h.h.SetKeyboardMode(mode, behavior)
}

func (h loggingHandler) SetModifyOtherKeys(mode ModifyOtherKeysMode) {
	h.log("SetModifyOtherKeys %v", mode)
	h.h.SetModifyOtherKeys(mode)

}

func (h loggingHandler) ReportModifyOtherKeys() {
	h.log("ReportModifyOtherKeys: ")
	h.h.ReportModifyOtherKeys()
}

func (h loggingHandler) GraphicsCommand(data []byte) {
	h.log("GraphicsCommand len(data)=%d", len(data))
	h.h.GraphicsCommand(data)
}

func (h loggingHandler) log(line string, params ...any) {
	log.WithField(logging.KeyClass, h.keyClass).
		Tracef(line, params...)
}
