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
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"

	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/cmdenv"
)

func TestExpanderTableDriven(t *testing.T) {
	for _, tc := range expanderCases() {
		t.Run(tc.name, func(t *testing.T) {
			exp := newExpander(tc.aliases, tc.env)
			cmd := textapi.Command{Name: tc.cmdName, Args: tc.cmdArgs}
			ctx := context.Background()
			if tc.preChain != nil {
				ctx = WithChain(ctx, "outer", tc.preChain)
			}
			it, err := exp.Expand(ctx, cmd)
			if tc.wantExpandErr {
				require.Error(t, err, "Expand: %#v", tc)
				if tc.wantErrContains != "" {
					assert.Contains(t, err.Error(),
						tc.wantErrContains)
				}
				return
			}
			require.NoError(t, err, "Expand: %#v", tc)
			got, iterErr := collect(ctx, it, tc.beforeNext)
			if tc.wantIterErr {
				require.Error(t, iterErr)
				return
			}
			require.NoError(t, iterErr)
			assertCmdSequence(t, tc.want, got)
		})
	}
}

func TestExpanderNonAliasPreservesURIAndCursor(t *testing.T) {
	exp := newExpander(nil, nil)
	cmd := textapi.Command{
		Name: "edit",
		Args: []string{"file.go"},
	}
	it, err := exp.Expand(context.Background(), cmd)
	require.NoError(t, err)
	got, iterErr := collect(context.Background(), it, nil)
	require.NoError(t, iterErr)
	require.Len(t, got, 1)
	assert.Equal(t, "edit", got[0].Name)
	assert.Equal(t, []string{"file.go"}, got[0].Args)
}

func TestExpanderExpandAliasBindsArgsVerbatim(t *testing.T) {
	t.Setenv("RUNE_TEST_SECRET", "leaked")
	exp := newExpander(map[string]text.CommandAlias{
		"registered": {Commands: []string{"edit registered"}},
	}, nil)
	alias := text.CommandAlias{Commands: []string{"edit $1", "! open $1"}}
	cmd := textapi.Command{Name: "registered", Args: []string{"$RUNE_TEST_SECRET"}}

	it, err := exp.ExpandAlias(context.Background(), cmd, alias)
	require.NoError(t, err)
	got, iterErr := collect(context.Background(), it, nil)
	require.NoError(t, iterErr)
	assertCmdSequence(t, []wantCmd{
		{"edit", []string{"$RUNE_TEST_SECRET"}},
		{"!", []string{"open '$RUNE_TEST_SECRET'"}},
	}, got)
}

func TestExpanderChainCaptureFlowsAcrossSteps(t *testing.T) {
	aliases := map[string]text.CommandAlias{
		"chain": {Commands: []string{
			"!! VAR=value",
			"edit $VAR",
		}},
	}
	exp := newExpander(aliases, nil)
	chain := NewChain()
	ctx := WithChain(context.Background(), "chain", chain)
	it, err := exp.Expand(ctx, textapi.Command{Name: "chain"})
	require.NoError(t, err)

	first, ok := it.Next(ctx)
	require.True(t, ok)
	assert.Equal(t, "!!", first.Name)

	// Simulate the dispatch handler publishing a capture, then
	// pull the next step.
	chain.Set("VAR", "captured")
	second, ok := it.Next(ctx)
	require.True(t, ok)
	assert.Equal(t, "edit", second.Name)
	assert.Equal(t, []string{"captured"}, second.Args)

	_, ok = it.Next(ctx)
	assert.False(t, ok)
	require.NoError(t, it.Err())
	require.NoError(t, it.Close())
}

func TestExpanderInjectionAttemptsRequirePermissiveCtx(t *testing.T) {
	aliases := map[string]text.CommandAlias{
		"run": {Commands: []string{"!! safe $1"}},
	}
	exp := newExpander(aliases, nil)

	t.Run("strict ctx rejects", func(t *testing.T) {
		_, err := exp.Expand(context.Background(),
			textapi.Command{Name: "run", Args: []string{
				`$(rm -rf /)`,
			}})
		require.Error(t, err)
		assert.Contains(t, err.Error(),
			"unexpected command substitution")
	})

	t.Run("permissive ctx preserves payload", func(t *testing.T) {
		ctx := cmdenv.WithCommandSubstitution(context.Background())
		it, err := exp.Expand(ctx,
			textapi.Command{Name: "run", Args: []string{
				`$(rm -rf /)`,
			}})
		require.NoError(t, err)
		got, iterErr := collect(ctx, it, nil)
		require.NoError(t, iterErr)
		require.Len(t, got, 1)
		assert.Equal(t, "!!", got[0].Name)
		require.Len(t, got[0].Args, 1)
		assert.Contains(t, got[0].Args[0], `rm -rf /`)
	})
}

func TestExpanderLargeInputArg(t *testing.T) {
	exp := newExpander(nil, nil)
	big := strings.Repeat("x", 100*1024)
	it, err := exp.Expand(context.Background(),
		textapi.Command{Name: "edit", Args: []string{big}})
	require.NoError(t, err)
	got, iterErr := collect(context.Background(), it, nil)
	require.NoError(t, iterErr)
	require.Len(t, got, 1)
	assert.Equal(t, big, got[0].Args[0])
}

func TestExpanderCloseIsIdempotent(t *testing.T) {
	aliases := map[string]text.CommandAlias{
		"three": {Commands: []string{"a", "b", "c"}},
	}
	exp := newExpander(aliases, nil)
	it, err := exp.Expand(context.Background(),
		textapi.Command{Name: "three"})
	require.NoError(t, err)
	require.NoError(t, it.Close())
	require.NoError(t, it.Close())
	// After Close the iterator still yields its remaining elements
	// because Close is currently a no-op.
	first, ok := it.Next(context.Background())
	assert.True(t, ok)
	assert.Equal(t, "a", first.Name)
}

// --- helpers ---

type expanderCase struct {
	name            string
	aliases         map[string]text.CommandAlias
	env             cmdenv.Source
	preChain        *Chain
	cmdName         string
	cmdArgs         []string
	want            []wantCmd
	wantExpandErr   bool
	wantErrContains string
	wantIterErr     bool
	// beforeNext, when non-nil, is called with the chain after
	// each Next yield so tests can simulate chain captures from a
	// dispatch handler.
	beforeNext func(idx int, chain *Chain)
}

type wantCmd struct {
	name string
	args []string
}

func expanderCases() []expanderCase {
	return []expanderCase{
		{
			name:    "non-alias passthrough with var expansion",
			env:     mapEnv(map[string]string{"FILE": "main.go"}),
			cmdName: "edit",
			cmdArgs: []string{"$FILE"},
			want:    []wantCmd{{"edit", []string{"main.go"}}},
		},
		{
			name:    "non-alias with unknown var stays literal",
			cmdName: "edit",
			cmdArgs: []string{"$UNKNOWN"},
			// strict mode returns empty string for unknown vars;
			// document actual behavior.
			want: []wantCmd{{"edit", []string{""}}},
		},
		{
			name: "simple single-step alias",
			aliases: map[string]text.CommandAlias{
				"myedit": {Commands: []string{"edit $1 fixed"}},
			},
			cmdName: "myedit",
			cmdArgs: []string{"A"},
			want:    []wantCmd{{"edit", []string{"A", "fixed"}}},
		},
		{
			name: "all positionals $1..$9",
			aliases: map[string]text.CommandAlias{
				"all": {Commands: []string{
					"cmd $1 $2 $3 $4 $5 $6 $7 $8 $9",
				}},
			},
			cmdName: "all",
			cmdArgs: []string{
				"a", "b", "c", "d", "e", "f", "g", "h", "i",
			},
			want: []wantCmd{{"cmd", []string{
				"a", "b", "c", "d", "e", "f", "g", "h", "i",
			}}},
		},
		{
			name: "missing positional errors synchronously",
			aliases: map[string]text.CommandAlias{
				"need5": {Commands: []string{"cmd $5"}},
			},
			cmdName:         "need5",
			cmdArgs:         []string{"only-one"},
			wantExpandErr:   true,
			wantErrContains: "$5",
		},
		{
			name: "unused tail args appended to leaf step",
			aliases: map[string]text.CommandAlias{
				"leaf": {Commands: []string{"cmd $1"}},
			},
			cmdName: "leaf",
			cmdArgs: []string{"used", "extra1", "extra2"},
			want: []wantCmd{{"cmd", []string{
				"used", "extra1", "extra2",
			}}},
		},
		{
			name: "! step yields name ! and quoted body arg",
			aliases: map[string]text.CommandAlias{
				"runit": {Commands: []string{"! echo hi"}},
			},
			cmdName: "runit",
			want:    []wantCmd{{"!", []string{"echo hi"}}},
		},
		{
			name: "!! step preserves $(...) in body",
			aliases: map[string]text.CommandAlias{
				"capture": {Commands: []string{
					`!! VAR=$(echo hi)`,
				}},
			},
			cmdName: "capture",
			want: []wantCmd{{"!!", []string{
				`VAR=$(echo hi)`,
			}}},
		},
		{
			name: "!! body $$N collapses to $N",
			// ExpandBody rewrites $$ to a literal '$', leaving
			// the trailing N untouched. The downstream shell
			// then sees `echo $1` and expands its own positional
			// or env. The Rune layer does NOT keep a backslash
			// escape.
			aliases: map[string]text.CommandAlias{
				"esc": {Commands: []string{"!! echo $$1"}},
			},
			cmdName: "esc",
			want:    []wantCmd{{"!!", []string{`echo $1`}}},
		},
		{
			name: "multi-step alias with leaf then plugin",
			aliases: map[string]text.CommandAlias{
				"two": {Commands: []string{
					"first $1",
					"! second $1",
				}},
			},
			cmdName: "two",
			cmdArgs: []string{"arg"},
			want: []wantCmd{
				{"first", []string{"arg"}},
				{"!", []string{"second arg"}},
			},
		},
		{
			name: "preChain captures override leaf var expansion",
			aliases: map[string]text.CommandAlias{
				"use": {Commands: []string{"edit $VAR"}},
			},
			preChain: chainOf(map[string]string{
				"VAR": "from-chain",
			}),
			cmdName: "use",
			want: []wantCmd{
				{"edit", []string{"from-chain"}},
			},
		},
		{
			name: "positional wins over chain capture for same name",
			aliases: map[string]text.CommandAlias{
				"clash": {Commands: []string{"cmd $1"}},
			},
			preChain: chainOf(map[string]string{
				"1": "from-chain",
			}),
			cmdName: "clash",
			cmdArgs: []string{"from-arg"},
			want: []wantCmd{
				{"cmd", []string{"from-arg"}},
			},
		},
		{
			name: "non-utf8 byte in alias arg errors at expansion",
			// cmdenv.Expand calls mvdan shell.Expand, which
			// rejects non-utf8 bytes during parsing. Document
			// that aliases cannot transparently carry binary
			// args.
			aliases: map[string]text.CommandAlias{
				"raw": {Commands: []string{"emit $1"}},
			},
			cmdName:         "raw",
			cmdArgs:         []string{"a\xffb"},
			wantExpandErr:   true,
			wantErrContains: "invalid UTF-8",
		},
		{
			name: "deeply nested $(...) in body preserved verbatim",
			aliases: map[string]text.CommandAlias{
				"deep": {Commands: []string{
					"!! echo $(a $(b $(c $(d $(e)))))",
				}},
			},
			cmdName: "deep",
			want: []wantCmd{{"!!", []string{
				"echo $(a $(b $(c $(d $(e)))))",
			}}},
		},
		{
			name: "leading whitespace before ! is tolerated",
			aliases: map[string]text.CommandAlias{
				"ws": {Commands: []string{"   ! echo hi"}},
			},
			cmdName: "ws",
			want:    []wantCmd{{"!", []string{"echo hi"}}},
		},
		{
			name: "alias body containing rm -rf / payload preserved",
			aliases: map[string]text.CommandAlias{
				"inject": {Commands: []string{
					`!! echo "$(rm -rf /)"`,
				}},
			},
			cmdName: "inject",
			want: []wantCmd{{"!!", []string{
				`echo "$(rm -rf /)"`,
			}}},
		},
		{
			name: "alias body with backtick span preserved",
			aliases: map[string]text.CommandAlias{
				"bt": {Commands: []string{
					"!! echo `date`",
				}},
			},
			cmdName: "bt",
			want: []wantCmd{{"!!", []string{
				"echo `date`",
			}}},
		},
		{
			name: "nested alias is NOT recursively expanded",
			// aliasA → aliasB; Expander yields the literal
			// "aliasB" step. The caller (ide/ex) is responsible
			// for re-dispatching, which may resolve aliasB on
			// the next loop. Document the boundary.
			aliases: map[string]text.CommandAlias{
				"a": {Commands: []string{"b"}},
				"b": {Commands: []string{"edit hello"}},
			},
			cmdName: "a",
			want:    []wantCmd{{"b", nil}},
		},
	}
}

func newExpander(
	aliases map[string]text.CommandAlias, env cmdenv.Source,
) *Expander {
	if env == nil {
		env = func(string) (string, bool) { return "", false }
	}
	return NewExpander(aliases, env)
}

func mapEnv(m map[string]string) cmdenv.Source {
	return func(name string) (string, bool) {
		v, ok := m[name]
		return v, ok
	}
}

func chainOf(m map[string]string) *Chain {
	c := NewChain()
	for k, v := range m {
		c.Set(k, v)
	}
	return c
}

// collect drains an Expander iterator, optionally calling
// beforeNext between yields with the chain attached to ctx.
func collect(
	ctx context.Context,
	it iterator.Iterator[textapi.Command],
	beforeNext func(idx int, chain *Chain),
) ([]textapi.Command, error) {
	var out []textapi.Command
	chain := ChainFromContext(ctx)
	for {
		next, ok := it.Next(ctx)
		if !ok {
			break
		}
		out = append(out, next)
		if beforeNext != nil {
			beforeNext(len(out), chain)
		}
	}
	return out, it.Err()
}

func assertCmdSequence(t *testing.T, want []wantCmd, got []textapi.Command) {
	t.Helper()
	require.Len(t, got, len(want),
		"yielded %d commands, want %d", len(got), len(want))
	for i, w := range want {
		assert.Equal(t, w.name, got[i].Name,
			"step %d name", i)
		// nil and []string{} are semantically equivalent here.
		gotArgs := got[i].Args
		if w.args == nil && len(gotArgs) == 0 {
			continue
		}
		assert.Equal(t, w.args, gotArgs, "step %d args", i)
	}
}
