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

package workspacessh

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWarningRoundTrip(t *testing.T) {
	var b strings.Builder
	require.NoError(t, WriteWarning(&b, "install shell dotfiles: disk full"))
	assert.True(t, strings.HasSuffix(b.String(), "\n"), "one record per line")

	line := []byte(strings.TrimRight(b.String(), "\n"))
	msg, ok := ParseWarningLine(line)
	require.True(t, ok)
	assert.Equal(t, "install shell dotfiles: disk full", msg)
	assert.False(t, parseServerReadyLine(line))
}

func TestServerReadyRoundTrip(t *testing.T) {
	line, err := encodeServerReady()
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(line, "\n"), "one record per line")

	trimmed := []byte(strings.TrimRight(line, "\n"))
	assert.True(t, parseServerReadyLine(trimmed))
	_, ok := ParseWarningLine(trimmed)
	assert.False(t, ok)
}

func TestParseStderrRecordRejectsOtherLines(t *testing.T) {
	for _, tc := range []struct {
		desc string
		line string
	}{
		{"plain text", "ready to serve"},
		{"unrelated json", `{"level":"warning","msg":"hello"}`},
		{"wrong sentinel", `{"rune":"other","msg":"hello"}`},
		{"older provisioning record", `{"rune":"provision","phase":"done"}`},
		{"empty", ""},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			assert.False(t, parseServerReadyLine([]byte(tc.line)))
			_, ok := ParseWarningLine([]byte(tc.line))
			assert.False(t, ok)
		})
	}
}
