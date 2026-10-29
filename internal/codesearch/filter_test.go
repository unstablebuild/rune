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

package codesearch

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCleanRoots(t *testing.T) {
	tests := []struct {
		name  string
		roots []string
		want  []string
	}{
		{name: "none", roots: nil, want: []string{"."}},
		{name: "the workspace", roots: []string{"a", "./"}, want: []string{"a", "."}},
		{name: "cleaned", roots: []string{"/a/b/", "c/../d"}, want: []string{"a/b", "d"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, cleanRoots(tt.roots))
		})
	}
}

func TestOutermost(t *testing.T) {
	tests := []struct {
		name  string
		roots []string
		want  []string
	}{
		{name: "none", roots: nil, want: nil},
		{name: "the workspace", roots: []string{"-a", "."}, want: []string{"."}},
		{name: "nested folders", roots: []string{"a/b/c", "a-b", "a/b", "a/bc"}, want: []string{"a-b", "a/b", "a/bc"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, outermost(tt.roots))
		})
	}
}

func TestUnder(t *testing.T) {
	assert.True(t, under("a/b", []string{"."}))
	assert.True(t, under("a/b", []string{"a"}))
	assert.True(t, under("a/b", []string{"a/b"}))
	assert.False(t, under("ab/c", []string{"a"}))
	assert.False(t, under("a", []string{"a/b"}))
}
