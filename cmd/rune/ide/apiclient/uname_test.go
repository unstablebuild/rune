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

package apiclient

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUtsnameToString(t *testing.T) {
	full := [65]byte{}
	for i := range full {
		full[i] = 'x'
	}
	tests := []struct {
		name  string
		field []byte
		want  string
	}{
		{name: "terminated", field: []byte("Linux\x00\x00\x00"), want: "Linux"},
		{name: "darwin length", field: append([]byte("Darwin"), make([]byte, 250)...), want: "Darwin"},
		{name: "empty", field: make([]byte, 65), want: ""},
		{name: "no terminator", field: full[:], want: string(full[:])},
		{name: "stops at first NUL", field: []byte("a\x00b\x00"), want: "a"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, utsnameToString(tc.field))
		})
	}
}

func TestUnameReportsRuntimeOS(t *testing.T) {
	info, err := uname()
	require.NoError(t, err)
	assert.Equal(t, runtime.GOOS, info.OS)
	assert.NotEmpty(t, info.Name)
	assert.NotEmpty(t, info.Machine)
}
