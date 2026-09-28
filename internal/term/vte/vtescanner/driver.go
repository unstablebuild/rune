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

// Driver is an interface that abstracts behavioural actions requested by a Scanner.
type Driver interface {
	// Print draws a character to the screen and updates states.
	Print(rune)

	// Execute executes a C0 or C1 control function.
	Execute(byte)

	// Hook is invoked when a final character arrives in the first part of a device control string.
	// The control function should be determined from the private marker, final character, and
	// execute with a parameter list. A handler should be selected for remaining characters in the
	// string; the handler function should subsequently be called by Put for every character in
	// the control string.
	// The ignore flag indicates that more than two intermediates arrived and
	// subsequent characters were ignored.
	Hook(params [][]uint16, intermediates []byte, ignore bool, action rune)

	// Put passes bytes as part of a device control string to the handler chosen in Hook.
	// C0 controls will also be passed to the handler.
	Put(byte)

	// Unhook is called when a device control string is terminated.
	// The previously selected handler should be notified that the DCS has terminated.
	Unhook()

	// OSCDispatch dispatches an operating system command.
	OSCDispatch(params [][]byte, bellTerminated bool)

	// CSIDispatch is called when a final character has arrived for a CSI sequence.
	// The ignore flag indicates that either more than two intermediates arrived
	// or the number of parameters exceeded the maximum supported length,
	// and subsequent characters were ignored.
	CSIDispatch(params [][]uint16, intermediates []byte, ignore bool, action rune)

	// ESCDispatch is called when the final character of an escape sequence has arrived.
	// The ignore flag indicates that more than two intermediates arrived and
	// subsequent characters were ignored.
	ESCDispatch(intermediates []byte, ignore bool, ch byte)

	// APCDispatch is called when an application program command string
	// (ESC _ ... ST) has been terminated. data excludes the introducer
	// and terminator and is only valid for the duration of the call.
	// Strings longer than MaxAPCRaw are dropped without dispatch.
	APCDispatch(data []byte)
}
