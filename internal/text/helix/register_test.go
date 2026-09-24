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

package helix

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
)

func TestRegisterNames(t *testing.T) {
	for _, tc := range []struct {
		in    rune
		want  rune
		valid bool
	}{
		{in: '"', want: '"', valid: true},
		{in: 'a', want: 'a', valid: true},
		// Uppercase names append to the lowercase register.
		{in: 'Z', want: 'z', valid: true},
		{in: '0', want: '0', valid: true},
		{in: '+', want: '+', valid: true},
		{in: '_', want: '_', valid: true},
		{in: '/', want: '/', valid: true},
		{in: '.', want: '.', valid: true},
		{in: '-', want: '-', valid: true},
		{in: '!', valid: false},
		{in: ' ', valid: false},
		{in: 0, valid: false},
	} {
		assert.Equal(t, tc.valid, validRegisterName(tc.in), "%q", tc.in)
		if tc.valid {
			assert.Equal(t, tc.want, normalizedRegisterName(tc.in), "%q", tc.in)
		}
	}
	assert.Equal(t, clipboard.DefaultRegisterID, registerNameToID(0))
	assert.Equal(t, clipboard.DefaultRegisterID, registerNameToID('"'))
}
