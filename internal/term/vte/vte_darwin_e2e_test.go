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

//go:build darwin && e2e

package vte

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestE2EDarwinPtyMasterFailsLargeWriteWithEAGAIN(t *testing.T) {
	// The darwin pty master is os.NewFile over a blocking descriptor
	// that Go never registers with kqueue, so once the read stage makes
	// the open file description non-blocking a write past the tty input
	// queue fails rather than parks. That is what the write stage
	// exists for; should the pty library change this, the stage is no
	// longer what TestE2EPasteLargerThanTtyQueueArrivesWhole exercises.
	command, _ := e2eCatToFile(t)
	h := newE2ETerminal(t, e2eOptions{command: command, rawTTY: true})

	n, err := h.comp.pty.Master.Write(alphaPayload(e2ePasteSize))

	require.ErrorIs(t, err, unix.EAGAIN)
	assert.Less(t, n, e2ePasteSize)
}
