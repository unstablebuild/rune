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

package apiclient

import (
	"fmt"
	"runtime"

	"golang.org/x/sys/unix"
)

func uname() (sysinfo, error) {
	var data unix.Utsname
	if err := unix.Uname(&data); err != nil {
		return sysinfo{}, fmt.Errorf("uname: %v", err)
	}
	return sysinfo{
		Name:    utsnameToString(data.Sysname),
		Node:    utsnameToString(data.Nodename),
		Release: utsnameToString(data.Release),
		Version: utsnameToString(data.Version),
		Machine: utsnameToString(data.Machine),
		OS:      runtime.GOOS,
	}, nil
}
