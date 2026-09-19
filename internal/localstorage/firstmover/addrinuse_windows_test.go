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

package firstmover

import (
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/sys/windows"
)

func TestIsAddrInUse(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"EADDRINUSE", syscall.EADDRINUSE, true},
		{"WSAEADDRINUSE", windows.WSAEADDRINUSE, true},
		{"WSAENETDOWN", windows.WSAENETDOWN, false},
		{"wrapped bind error", &net.OpError{Op: "listen", Net: "unix",
			Err: os.NewSyscallError("bind", windows.WSAEADDRINUSE)}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isAddrInUse(tc.err))
		})
	}
}
