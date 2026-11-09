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

//go:build linux && e2e

package vte

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestE2ELinuxPtyMasterParksLargeWrite(t *testing.T) {
	// The linux pty master comes from os.OpenFile and is registered
	// with epoll, so a write past the tty input queue parks in the
	// poller even though the read stage made the master non-blocking.
	// The regression never reached linux, and the write stage must
	// deliver the same there as the master does on its own.
	command, received := e2eCatToFile(t)
	h := newE2ETerminal(t, e2eOptions{command: command, rawTTY: true})

	payload := alphaPayload(e2ePasteSize)
	n, err := h.comp.pty.Master.Write(payload)

	require.NoError(t, err)
	require.Equal(t, len(payload), n)
	requireSameBytes(t, payload, h.waitFile(t, received, len(payload)))
}
