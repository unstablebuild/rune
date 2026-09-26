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

//go:build windows

package procattr

import (
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTaskkillArgs(t *testing.T) {
	for _, tc := range []struct {
		pid  int
		want []string
	}{
		{1234, []string{"/T", "/F", "/PID", "1234"}},
		{4, []string{"/T", "/F", "/PID", "4"}},
	} {
		assert.Equal(t, tc.want, taskkillArgs(tc.pid))
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
		{"NewGroup", NewGroup(), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, LeadsGroup(tc.attr))
		})
	}
}

func TestNewSessionIsNil(t *testing.T) {
	assert.Nil(t, NewSession(true, true))
}
