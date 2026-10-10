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

package vctrl

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestProtectedMatcherHomeAware(t *testing.T) {
	t.Parallel()
	home := filepath.Join("/Users", "tester")
	library := filepath.Join(home, "Library")
	m := protectedMatcher{base: home, roots: []string{library}}

	cases := []struct {
		relpath string
		want    bool
	}{
		{"Library", true},
		{"Library/Containers", true},
		{"Library/Application Support/SomeApp", true},
		{library, true},
		{filepath.Join(library, "Caches"), true},
		{"Documents", false},
		{"Desktop/screenshot.png", false},
		{"Downloads", false},
		{filepath.Join(home, "Documents"), false},
		{"src/Library", false},
		{"LibraryNotReally", false},
		{"Projects/Documents", false},
		{filepath.Join(home, "DownloadsX"), false},
		{filepath.Join(home, "Projects"), false},
	}
	for _, tc := range cases {
		assert.Equalf(t, tc.want, m.MatchRelPath(tc.relpath, true),
			"MatchRelPath(%q)", tc.relpath)
	}
}

func TestProtectedDirMatcherNoRoots(t *testing.T) {
	t.Parallel()
	m := protectedMatcher{base: "/work", roots: nil}
	assert.False(t, m.MatchRelPath("Library", true))
	assert.False(t, m.MatchRelPath("anything", false))
}
