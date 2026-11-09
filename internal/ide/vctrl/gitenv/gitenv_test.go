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

package gitenv

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEnviron(t *testing.T) {
	stripped := []string{
		"GIT_DIR",
		"GIT_WORK_TREE",
		"GIT_INDEX_FILE",
		"GIT_COMMON_DIR",
		"GIT_CONFIG_PARAMETERS",
		"GIT_CONFIG_COUNT",
		"GIT_CONFIG_KEY_0",
		"GIT_CONFIG_VALUE_0",
		"GIT_QUARANTINE_PATH",
		"GIT_CEILING_DIRECTORIES",
	}
	kept := []string{
		"GIT_SSH_COMMAND",
		"GIT_AUTHOR_NAME",
		"GIT_CONFIG_GLOBAL",
		"GIT_TERMINAL_PROMPT",
		"SOME_OTHER_VAR",
	}
	for _, name := range append(append([]string{}, stripped...), kept...) {
		t.Setenv(name, "value-of-"+name)
	}

	got := map[string]bool{}
	for _, kv := range Environ() {
		got[kv] = true
	}

	for _, name := range stripped {
		assert.False(t, got[name+"=value-of-"+name],
			"%s must be stripped: it pins git to the caller's repository", name)
	}
	for _, name := range kept {
		assert.True(t, got[name+"=value-of-"+name],
			"%s must be preserved: it is user-level configuration", name)
	}
}
