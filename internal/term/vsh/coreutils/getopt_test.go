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

package coreutils_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"unstable.build/rune/internal/term/vsh/coreutils"
)

func TestParseArgs(t *testing.T) {
	t.Parallel()
	type (
		flags  = map[byte]bool
		values = map[byte]string
		lists  = map[byte][]string
		longs  = map[string]string
	)
	tests := []struct {
		name    string
		args    []string
		spec    string
		numeric byte
		longs   []string
		want    coreutils.Options
		err     string
	}{
		{name: "nothing", spec: "rn", want: coreutils.Options{}},
		{name: "one flag", args: []string{"-r"}, spec: "rn",
			want: coreutils.Options{Flags: flags{'r': true}}},
		{name: "clustered flags", args: []string{"-rn"}, spec: "rn",
			want: coreutils.Options{Flags: flags{'r': true, 'n': true}}},
		{name: "separate flags", args: []string{"-r", "-n"}, spec: "rn",
			want: coreutils.Options{Flags: flags{'r': true, 'n': true}}},
		{name: "repeated flag", args: []string{"-rr"}, spec: "r",
			want: coreutils.Options{Flags: flags{'r': true}}},
		{name: "attached value", args: []string{"-n5"}, spec: "n:",
			want: coreutils.Options{Values: values{'n': "5"}, Lists: lists{'n': {"5"}}}},
		{name: "detached value", args: []string{"-n", "5"}, spec: "n:",
			want: coreutils.Options{Values: values{'n': "5"}, Lists: lists{'n': {"5"}}}},
		{name: "detached value that looks like an option", args: []string{"-e", "-x", "f"}, spec: "e:x",
			want: coreutils.Options{Values: values{'e': "-x"}, Lists: lists{'e': {"-x"}}, Operands: []string{"f"}}},
		{name: "detached value that is --", args: []string{"-e", "--", "f"}, spec: "e:",
			want: coreutils.Options{Values: values{'e': "--"}, Lists: lists{'e': {"--"}}, Operands: []string{"f"}}},
		{name: "flags then attached value", args: []string{"-rn5"}, spec: "rn:",
			want: coreutils.Options{Flags: flags{'r': true}, Values: values{'n': "5"}, Lists: lists{'n': {"5"}}}},
		{name: "value swallows the rest of the cluster", args: []string{"-nr"}, spec: "rn:",
			want: coreutils.Options{Values: values{'n': "r"}, Lists: lists{'n': {"r"}}}},
		{name: "flags then detached value", args: []string{"-rn", "5", "x"}, spec: "rn:",
			want: coreutils.Options{Flags: flags{'r': true}, Values: values{'n': "5"}, Lists: lists{'n': {"5"}}, Operands: []string{"x"}}},
		{name: "repeated value keeps every one and the last wins", args: []string{"-e", "a", "-eb"}, spec: "e:",
			want: coreutils.Options{Values: values{'e': "b"}, Lists: lists{'e': {"a", "b"}}}},
		{name: "empty value", args: []string{"-e", ""}, spec: "e:",
			want: coreutils.Options{Values: values{'e': ""}, Lists: lists{'e': {""}}}},
		{name: "operands", args: []string{"a", "b"}, spec: "r",
			want: coreutils.Options{Operands: []string{"a", "b"}}},
		{name: "options after operands", args: []string{"a", "-r", "b"}, spec: "r",
			want: coreutils.Options{Flags: flags{'r': true}, Operands: []string{"a", "b"}}},
		{name: "empty operand", args: []string{""}, spec: "r",
			want: coreutils.Options{Operands: []string{""}}},
		{name: "lone dash is an operand", args: []string{"-", "-r"}, spec: "r",
			want: coreutils.Options{Flags: flags{'r': true}, Operands: []string{"-"}}},
		{name: "double dash ends options", args: []string{"-r", "--", "-n", "--", "x"}, spec: "rn",
			want: coreutils.Options{Flags: flags{'r': true}, Operands: []string{"-n", "--", "x"}}},
		{name: "double dash last", args: []string{"x", "--"}, spec: "r",
			want: coreutils.Options{Operands: []string{"x"}}},
		{name: "numeric shorthand", args: []string{"-5", "f"}, spec: "n:", numeric: 'n',
			want: coreutils.Options{Values: values{'n': "5"}, Lists: lists{'n': {"5"}}, Operands: []string{"f"}}},
		{name: "numeric shorthand keeps trailing text", args: []string{"-12x"}, spec: "n:", numeric: 'n',
			want: coreutils.Options{Values: values{'n': "12x"}, Lists: lists{'n': {"12x"}}}},
		{name: "numeric shorthand then -n", args: []string{"-5", "-n", "7"}, spec: "n:", numeric: 'n',
			want: coreutils.Options{Values: values{'n': "7"}, Lists: lists{'n': {"5", "7"}}}},
		{name: "digit without numeric", args: []string{"-5"}, spec: "n:",
			err: "invalid option -- '5'"},
		{name: "long flag", args: []string{"--all"}, spec: "", longs: []string{"all"},
			want: coreutils.Options{Longs: longs{"all": ""}}},
		{name: "long attached value", args: []string{"--color=auto"}, longs: []string{"color="},
			want: coreutils.Options{Longs: longs{"color": "auto"}}},
		{name: "long attached empty value", args: []string{"--color="}, longs: []string{"color="},
			want: coreutils.Options{Longs: longs{"color": ""}}},
		{name: "long detached value", args: []string{"--color", "never", "f"}, longs: []string{"color="},
			want: coreutils.Options{Longs: longs{"color": "never"}, Operands: []string{"f"}}},
		{name: "long missing value", args: []string{"--color"}, longs: []string{"color="},
			err: "option '--color' requires an argument"},
		{name: "unknown long", args: []string{"--nope"}, spec: "n",
			err: "unrecognized option '--nope'"},
		{name: "unknown short", args: []string{"-z"}, spec: "rn",
			err: "invalid option -- 'z'"},
		{name: "unknown short in a cluster", args: []string{"-rzn"}, spec: "rn",
			err: "invalid option -- 'z'"},
		{name: "missing value", args: []string{"-r", "-n"}, spec: "rn:",
			err: "option requires an argument -- 'n'"},
		{name: "missing value at the end of a cluster", args: []string{"-rn"}, spec: "rn:",
			err: "option requires an argument -- 'n'"},
		{name: "colon is not an option", args: []string{"-:"}, spec: "n:",
			err: "invalid option -- ':'"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, usage, err := coreutils.ParseArgs("cmd", tc.args, tc.spec, tc.numeric, tc.longs...)
			assert.Equal(t, tc.err != "", usage, "usage error")
			assert.Equal(t, tc.err, errString(err))
			assert.Equal(t, normalize(tc.want), normalize(got))
		})
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// normalize makes empty and nil collections compare equal.
func normalize(o coreutils.Options) coreutils.Options {
	if len(o.Flags) == 0 {
		o.Flags = nil
	}
	if len(o.Values) == 0 {
		o.Values = nil
	}
	if len(o.Lists) == 0 {
		o.Lists = nil
	}
	if len(o.Longs) == 0 {
		o.Longs = nil
	}
	if len(o.Operands) == 0 {
		o.Operands = nil
	}
	return o
}
