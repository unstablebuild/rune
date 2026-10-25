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

package ideauthorizer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOptionSpecScan(t *testing.T) {
	t.Parallel()

	spec := optionSpec{short: "ab:c?", long: []string{"flag", "value=", "maybe=?"}}
	cases := []struct {
		name     string
		args     []string
		want     []parsedOption
		wantNext int
		wantOK   bool
	}{
		{name: "no options", args: []string{"cmd", "-a"}, wantNext: 0, wantOK: true},
		{name: "cluster", args: []string{"-ab", "x", "cmd"},
			want:     []parsedOption{{name: "-a"}, {name: "-b", value: "x"}},
			wantNext: 2, wantOK: true},
		{name: "attached value", args: []string{"-bx", "cmd"},
			want: []parsedOption{{name: "-b", value: "x"}}, wantNext: 1, wantOK: true},
		{name: "optional value", args: []string{"-c", "cmd"},
			want: []parsedOption{{name: "-c"}}, wantNext: 1, wantOK: true},
		{name: "long options", args: []string{"--flag", "--value", "v", "--maybe=m", "cmd"},
			want: []parsedOption{{name: "--flag"}, {name: "--value", value: "v", at: 2},
				{name: "--maybe", value: "m", at: 3}},
			wantNext: 4, wantOK: true},
		{name: "terminator", args: []string{"-a", "--", "-b"},
			want: []parsedOption{{name: "-a"}}, wantNext: 2, wantOK: true},
		{name: "unknown short", args: []string{"-z", "cmd"}, wantNext: 0},
		{name: "unknown long", args: []string{"--zzz", "cmd"}, wantNext: 0},
		{name: "missing value", args: []string{"-b"}, wantNext: 0},
		{name: "value on a flag", args: []string{"--flag=x"}, wantNext: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts, next, ok := spec.scan(literalWords(tc.args))
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantNext, next)
			if tc.wantOK {
				assert.Equal(t, tc.want, opts)
			}
		})
	}
}

func TestOptionSpecScanStopsAtWordsThatAreNotLiteral(t *testing.T) {
	t.Parallel()

	_, next, ok := optionSpec{short: "a"}.scan([]shellWord{{text: "-a", lit: true}, {text: "$X"}})
	assert.False(t, ok)
	assert.Equal(t, 1, next)
}

func TestLookupProgramSpec(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]programKind{
		"/usr/bin/env":  kindTransparent,
		"python3.12":    kindInterpreter,
		"python":        kindInterpreter,
		"node18":        kindInterpreter,
		"bash":          kindShell,
		"./sudo":        kindEscalating,
		"zsh":           kindOpaque,
		"git":           kindInterpreter,
		"pythonista":    kindPlain,
		"/opt/bin/rgit": kindPlain,
	} {
		spec, _ := lookupProgramSpec(name)
		assert.Equal(t, want, spec.kind, name)
	}
}

func TestGitScope(t *testing.T) {
	t.Parallel()

	cases := []struct {
		args      []string
		wantScope []string
		wantCode  int
	}{
		{[]string{"status"}, []string{"status"}, -1},
		{[]string{"-C", "x", "--no-pager", "log", "-1"}, []string{"log", "-1"}, -1},
		{[]string{"--work-tree=x", "-c", "core.pager=sh", "log"},
			[]string{"-c", "core.pager=sh", "log"}, 0},
		{[]string{"--exec-path=/tmp", "status"}, []string{"--exec-path=/tmp", "status"}, 0},
		{[]string{"commit", "-C", "HEAD"}, []string{"commit", "-C", "HEAD"}, -1},
	}
	for _, tc := range cases {
		scope, code := gitScope(tc.args)
		assert.Equal(t, tc.wantScope, scope, tc.args)
		assert.Equal(t, tc.wantCode, code, tc.args)
	}
}

func TestDangerousEnv(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"PATH", "LD_PRELOAD", "LD_AUDIT", "DYLD_INSERT_LIBRARIES",
		"BASH_FUNC_ls%%", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS", "PS4", "NODE_OPTIONS"} {
		assert.True(t, dangerousEnv(name), name)
	}
	for _, name := range []string{"HOME", "LANG", "GOPATH", "TERM", "FOO"} {
		assert.False(t, dangerousEnv(name), name)
	}
}
