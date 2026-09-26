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

//go:build !windows

package procattr

import (
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewGroup(t *testing.T) {
	assert.Equal(t, &syscall.SysProcAttr{Setpgid: true}, NewGroup())
}

func TestNewSession(t *testing.T) {
	for _, tc := range []struct {
		name            string
		setsid, setctty bool
		want            *syscall.SysProcAttr
	}{
		{"session and tty", true, true, &syscall.SysProcAttr{Setsid: true, Setctty: true}},
		{"session only", true, false, &syscall.SysProcAttr{Setsid: true}},
		{"tty only", false, true, &syscall.SysProcAttr{Setctty: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, NewSession(tc.setsid, tc.setctty))
		})
	}
}

func TestLeadsGroup(t *testing.T) {
	for _, tc := range []struct {
		name string
		attr *syscall.SysProcAttr
		want bool
	}{
		{"nil", nil, false},
		{"no group", &syscall.SysProcAttr{}, false},
		{"new group", &syscall.SysProcAttr{Setpgid: true}, true},
		{"joins existing group", &syscall.SysProcAttr{Setpgid: true, Pgid: 42}, false},
		{"NewGroup", NewGroup(), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, LeadsGroup(tc.attr))
		})
	}
}

func TestKillGroupAfterExitReportsProcessDone(t *testing.T) {
	cmd := exec.Command("true")
	cmd.SysProcAttr = NewGroup()
	require.NoError(t, cmd.Run())

	assert.ErrorIs(t, KillGroup(cmd.Process), os.ErrProcessDone)
}
