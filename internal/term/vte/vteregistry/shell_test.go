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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vteparser"
)

// fakeShell tells what a test says, and records whether the host of its
// pty was asked.
type fakeShell struct {
	complete bool
	pid      workspaceapi.Pid
	fg       int
	fgErr    error
	alt      bool
	mark     vteparser.ShellMark
	asked    bool
}

func (s *fakeShell) IsComplete() bool               { return s.complete }
func (s *fakeShell) Pid() workspaceapi.Pid          { return s.pid }
func (s *fakeShell) IsAltBuffer() bool              { return s.alt }
func (s *fakeShell) ShellMark() vteparser.ShellMark { return s.mark }

func (s *fakeShell) ForegroundProcessGroup() (int, error) {
	s.asked = true
	return s.fg, s.fgErr
}

func TestShellLiveness(t *testing.T) {
	t.Parallel()
	const pid, job = 4242, 4343
	var (
		promptStart  = vteparser.ShellMarkPromptStart
		promptEnd    = vteparser.ShellMarkPromptEnd
		commandStart = vteparser.ShellMarkCommandStart
		commandEnd   = vteparser.ShellMarkCommandEnd
	)
	cases := []struct {
		desc      string
		shell     fakeShell
		want      vte.Liveness
		wantAsked bool
	}{
		{desc: "a shell that stopped reading its pty is idle without asking",
			shell: fakeShell{complete: true, pid: pid, fg: job, mark: commandStart},
			want:  vte.LivenessIdle},
		{desc: "a shell not started yet is unknown without asking",
			want: vte.LivenessUnknown},
		{desc: "a shell without integration nor a host that tells is unknown",
			shell: fakeShell{pid: pid, fgErr: errors.ErrUnsupported},
			want:  vte.LivenessUnknown, wantAsked: true},
		{desc: "the shell in the foreground is idle",
			shell: fakeShell{pid: pid, fg: pid}, want: vte.LivenessIdle, wantAsked: true},
		{desc: "a job in the foreground is running",
			shell: fakeShell{pid: pid, fg: job}, want: vte.LivenessRunning, wantAsked: true},
		// A program such as ssh relays the marks of another shell.
		{desc: "a job in the foreground outranks a prompt",
			shell: fakeShell{pid: pid, fg: job, mark: promptEnd},
			want:  vte.LivenessRunning, wantAsked: true},
		// A shell without job control runs its jobs in its own group.
		{desc: "a command's output outranks the shell in the foreground",
			shell: fakeShell{pid: pid, fg: pid, mark: commandStart},
			want:  vte.LivenessRunning, wantAsked: true},
		{desc: "the alternate screen outranks the shell in the foreground",
			shell: fakeShell{pid: pid, fg: pid, alt: true},
			want:  vte.LivenessRunning, wantAsked: true},
		// Some shells mark their prompts but not their commands.
		{desc: "the alternate screen outranks a prompt",
			shell: fakeShell{pid: pid, fgErr: errors.ErrUnsupported, alt: true, mark: promptStart},
			want:  vte.LivenessRunning, wantAsked: true},
		{desc: "a prompt with the shell in the foreground is idle",
			shell: fakeShell{pid: pid, fg: pid, mark: promptStart},
			want:  vte.LivenessIdle, wantAsked: true},
		{desc: "a command's output is running where the host cannot tell",
			shell: fakeShell{pid: pid, fgErr: errors.ErrUnsupported, mark: commandStart},
			want:  vte.LivenessRunning, wantAsked: true},
		{desc: "a prompt is idle where the host cannot tell",
			shell: fakeShell{pid: pid, fgErr: errors.ErrUnsupported, mark: promptStart},
			want:  vte.LivenessIdle, wantAsked: true},
		{desc: "a prompt's end is idle",
			shell: fakeShell{pid: pid, fgErr: errors.ErrUnsupported, mark: promptEnd},
			want:  vte.LivenessIdle, wantAsked: true},
		{desc: "a command's end is idle",
			shell: fakeShell{pid: pid, fgErr: errors.ErrUnsupported, mark: commandEnd},
			want:  vte.LivenessIdle, wantAsked: true},
		{desc: "a host that fails tells nothing",
			shell: fakeShell{pid: pid, fgErr: errors.New("transport dropped"), mark: commandStart},
			want:  vte.LivenessRunning, wantAsked: true},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			s := tc.shell
			assert.Equal(t, tc.want, shellLiveness(&s))
			assert.Equal(t, tc.wantAsked, s.asked, "whether the host of the pty was asked")
		})
	}
}
