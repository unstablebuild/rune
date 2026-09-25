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

// List of constants used by the scanner.
const (
	MaxIntermediates = 2
	MaxOSCParams     = 16
	MaxOSCRaw        = 1024
	// MaxAPCRaw bounds an APC string. kitty accepts 256 KiB per escape
	// and graphics clients chunk at 4 KiB, so this leaves ample room
	// while keeping a runaway sequence from growing without bound.
	MaxAPCRaw = 1024 * 1024
	// MaxParams represents the max number of parameters
	// passed to the Perform ifc.
	MaxParams = 32
)

// State represents the scanner state.
type State uint8

// List of scanner states.
const (
	Anywhere State = iota
	CsiEntry
	CsiIgnore
	CsiIntermediate
	CsiParam
	DcsEntry
	DcsIgnore
	DcsIntermediate
	DcsParam
	DcsPassthrough
	Escape
	EscapeIntermediate
	Ground
	OSCString
	SosPmApcString
	Utf8
)

// Action represents the scanner action.
type Action int

// List of scanner actions.
const (
	None Action = iota
	Clear
	Collect
	CSIDispatch
	ESCDispatch
	Execute
	Hook
	Ignore
	OSCEnd
	OSCPut
	OSCStart
	Param
	Print
	Put
	Unhook
	BeginUtf8
)

// unpack unpacks a uint8 into a State and Action.
func unpack(delta uint8) (State, Action) {
	return State(delta & 0x0f), Action(delta >> 4)
}

// nolint:unused
// pack packs a State and Action into a uint8.
func pack(state State, action Action) uint8 {
	return uint8(action)<<4 | uint8(state)
}
