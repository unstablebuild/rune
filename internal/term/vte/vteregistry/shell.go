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

package vteregistry

import (
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vteparser"
)

// shell is what a terminal tells about the shell it runs.
type shell interface {
	IsComplete() bool
	Pid() workspaceapi.Pid
	IsAltBuffer() bool
	ShellMark() vteparser.ShellMark
	ForegroundProcessGroup() (int, error)
}

// shellLiveness tells whether s runs a command or waits at its prompt.
// A job the shell put in the foreground outranks its marks, as a
// program such as ssh relays the marks of another shell. The marks and
// the alternate screen outrank the shell being in the foreground, as a
// shell without job control runs its jobs in its own process group.
func shellLiveness(s shell) vte.Liveness {
	if s.IsComplete() {
		return vte.LivenessIdle
	}
	pid := s.Pid()
	if pid == 0 {
		return vte.LivenessUnknown
	}
	fg, err := s.ForegroundProcessGroup()
	shellForeground := err == nil && fg == int(pid)
	if err == nil && !shellForeground {
		return vte.LivenessRunning
	}
	if s.IsAltBuffer() {
		return vte.LivenessRunning
	}
	switch s.ShellMark() {
	case vteparser.ShellMarkCommandStart:
		return vte.LivenessRunning
	case vteparser.ShellMarkPromptStart, vteparser.ShellMarkPromptEnd, vteparser.ShellMarkCommandEnd:
		return vte.LivenessIdle
	}
	if shellForeground {
		return vte.LivenessIdle
	}
	return vte.LivenessUnknown
}
