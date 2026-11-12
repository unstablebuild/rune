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

package idecmd

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"mvdan.cc/sh/v3/shell"

	"unstable.build/rune/internal/text/cmdenv"
)

func TestIsPluginTarget(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		target string
		want   bool
	}{
		{"! echo hi", true},
		{"!! VAR=$(echo hi)", true},
		{"!foo", false},
		{"edit a.go", false},
		{"  !\techo hi", true},
	} {
		t.Run(tc.target, func(t *testing.T) {
			assert.Equal(t, tc.want, isPluginTarget(tc.target))
		})
	}
}

func TestExpandTargetPositional(t *testing.T) {
	t.Parallel()
	argv, ref, err := expandTarget(
		context.Background(),
		"edit $1 hellagood",
		[]string{"arg1"},
		nil,
		nil,
		false,
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"edit", "arg1", "hellagood"}, argv)
	_, used := ref[0]
	assert.True(t, used)
}

func TestExpandTargetMissingPositional(t *testing.T) {
	t.Parallel()
	_, _, err := expandTarget(
		context.Background(),
		"edit $2",
		[]string{"arg1"},
		nil,
		nil,
		false,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "$2")
}

func TestExpandTargetPluginBodyPreservesCommandSubst(t *testing.T) {
	t.Parallel()
	argv, _, err := expandTarget(
		context.Background(),
		`!! echo "$(echo hello)"`,
		nil,
		nil,
		nil,
		true,
	)
	require.NoError(t, err)
	require.Len(t, argv, 2)
	assert.Equal(t, "!!", argv[0])
	assert.Contains(t, argv[1], "$(echo hello)")
}

func TestExpandTargetChainCapture(t *testing.T) {
	t.Parallel()
	chain := NewChain()
	chain.Set("VAR", "captured")
	argv, _, err := expandTarget(
		context.Background(),
		"edit $VAR",
		nil,
		nil,
		chain,
		false,
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"edit", "captured"}, argv)
}

func TestExpandTargetDollarDollarEscape(t *testing.T) {
	t.Parallel()
	argv, _, err := expandTarget(
		cmdenv.WithCommandSubstitution(context.Background()),
		`echo $$1`,
		[]string{"arg1"},
		nil,
		nil,
		false,
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"echo", "$1"}, argv)
}

func TestExpandTargetLineParsingCommandGetsValuesAsData(t *testing.T) {
	t.Parallel()
	const url = "https://example.com/?a=1&b=2;c='x' d"
	for _, tc := range []struct {
		name   string
		target string
		want   []string
	}{
		{name: "value", target: "term browser $1", want: []string{"browser", url}},
		{name: "quoted value", target: `term browser "$1"`, want: []string{"browser", url}},
		{name: "words as written", target: "term sh -c 'echo a && echo b'", want: []string{"sh", "-c", "echo a && echo b"}},
		{name: "home", target: "term ls ~", want: []string{"ls", "/home/me"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			argv, ref, err := expandTarget(context.Background(), tc.target, []string{url}, nil, nil, true)
			require.NoError(t, err)
			require.Equal(t, "term", argv[0])
			got, err := shell.Fields(strings.Join(argv[1:], " "), func(name string) string {
				if name == "HOME" {
					return "/home/me"
				}
				return ""
			})
			require.NoError(t, err, "args: %q", argv[1:])
			assert.Equal(t, tc.want, got)
			if strings.Contains(tc.target, "$1") {
				assert.Contains(t, ref, 0)
			}
		})
	}
}
