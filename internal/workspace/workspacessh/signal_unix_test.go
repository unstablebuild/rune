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

package workspacessh

import (
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/crypto/ssh"
)

func TestSigMapForwardsUserSignals(t *testing.T) {
	for _, tc := range []struct {
		sig  syscall.Signal
		want ssh.Signal
	}{
		{syscall.SIGUSR1, "USR1"},
		{syscall.SIGUSR2, "USR2"},
		{syscall.SIGTERM, "TERM"},
		{syscall.SIGKILL, "KILL"},
	} {
		t.Run(tc.sig.String(), func(t *testing.T) {
			got, ok := sigMap[tc.sig]
			assert.True(t, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}
