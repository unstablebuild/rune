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

package hostenv

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPosixFragment(t *testing.T) {
	vars := []envVar{
		{"QUOTE", "it's"},
		{"SPACES", "a b  c"},
		{"DOLLAR", "$HOME/$(x)`y`"},
		{"NEWLINE", "one\ntwo"},
		{"BAD-NAME", "skipped"},
	}
	got := string(posixFragment(vars, []string{"/d/bin", "/d/py bin"}, []string{"/opt/it's"}))

	assert.Equal(t, fragmentHeader+
		"export QUOTE='it'\\''s'\n"+
		"export SPACES='a b  c'\n"+
		"export DOLLAR='$HOME/$(x)`y`'\n"+
		"export NEWLINE='one\ntwo'\n"+
		posixPathFunctions+
		"__rune_path_prepend '/d/py bin'\n"+
		"__rune_path_prepend '/d/bin'\n"+
		"__rune_path_append '/opt/it'\\''s'\n"+
		"export PATH\n"+
		"unset -f __rune_path_prepend __rune_path_append\n"+
		"unset __rune_rest __rune_out __rune_entry\n", got)
}

func TestFishFragment(t *testing.T) {
	vars := []envVar{
		{"QUOTE", "it's"},
		{"BACKSLASH", `a\b`},
		{"DOLLAR", "$HOME (x)"},
		{"NEWLINE", "one\ntwo"},
		{"BAD-NAME", "skipped"},
	}
	got := string(fishFragment(vars, []string{"/d/bin", "/d/py bin"}, []string{"/opt/x"}))

	assert.Equal(t, fragmentHeader+
		"set -gx QUOTE 'it\\'s'\n"+
		"set -gx BACKSLASH 'a\\\\b'\n"+
		"set -gx DOLLAR '$HOME (x)'\n"+
		"set -gx NEWLINE 'one\ntwo'\n"+
		"begin\n"+
		"    for __rune_dir in '/d/py bin' '/d/bin'\n"+
		"        set -l __rune_rest\n"+
		"        for __rune_entry in $PATH\n"+
		"            test \"$__rune_entry\" = \"$__rune_dir\"; or set -a __rune_rest $__rune_entry\n"+
		"        end\n"+
		"        set -gx PATH $__rune_dir $__rune_rest\n"+
		"    end\n"+
		"    for __rune_dir in '/opt/x'\n"+
		"        contains -- $__rune_dir $PATH; or set -gx -a PATH $__rune_dir\n"+
		"    end\n"+
		"end\n", got)
}
