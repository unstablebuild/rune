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

package sounds

import "slices"

// The reserved sounds always resolve: to their built-in tones unless the
// configuration overrides them. They match the levels of Rune's
// notifications.
const (
	// Error means something failed or was refused, including input
	// that had no effect. It can play on every keystroke, so its
	// built-in tone is short.
	Error = "error"
	// Warning means something needs caution. It is what a program in a
	// terminal ringing the bell plays by default.
	Warning = "warning"
	// Info means something informational happened.
	Info = "info"
	// Success means something the user was waiting on succeeded.
	Success = "success"
)

var reserved = []string{Error, Warning, Info, Success}

// IsReserved reports whether name is one of the reserved sounds.
func IsReserved(name string) bool {
	return slices.Contains(reserved, name)
}

// Sound is a resolved sound, ready to play.
type Sound struct {
	// Name is the name that was played.
	Name string
	// Path is the audio file to play. When it is empty, the built-in
	// tone of BuiltIn plays.
	Path string
	// BuiltIn is the reserved sound whose built-in tone plays when Path
	// is empty or cannot be played. It is empty when the sound has no
	// reserved sound to fall back on, so an unplayable Path is silent.
	BuiltIn string
}

// Player plays sounds by name.
type Player interface {
	// Play starts the sound called name and returns without waiting for
	// it to be heard. It is safe for concurrent use. An unknown or
	// silenced name plays nothing, and a sound may be dropped, for
	// example while the same sound is still playing.
	Play(name string)
}

// PlayerFunc adapts a function to Player.
type PlayerFunc func(name string)

// Play calls f(name).
func (f PlayerFunc) Play(name string) { f(name) }

// Silent is a Player that plays nothing.
var Silent Player = PlayerFunc(func(string) {})

// Backend makes resolved sounds audible.
type Backend interface {
	// Play starts s and returns without waiting for it to be heard.
	// It is safe for concurrent use and may drop s, for example when
	// the same sound is still playing or no output device is available.
	Play(Sound)
}

// BackendFunc adapts a function to Output.
type BackendFunc func(Sound)

// Play calls f(s).
func (f BackendFunc) Play(s Sound) { f(s) }
