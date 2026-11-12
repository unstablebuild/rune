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

package shellplan

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRule(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in      string
		want    Rule
		wantStr string
		wantErr string
	}{
		// Program names and argument counts.
		{in: "git", want: Rule{Name: "git"}, wantStr: "git"},
		{in: "git *", want: Rule{Name: "git", Prefix: true}, wantStr: "git *"},
		{in: "git log", want: Rule{Name: "git", Args: []string{"log"}}, wantStr: "git log"},
		{in: "git log *", want: Rule{Name: "git", Args: []string{"log"}, Prefix: true}},
		{in: "git log --oneline -n 5", want: Rule{Name: "git", Args: []string{"log", "--oneline", "-n", "5"}}},
		{in: "mycli -- *", want: Rule{Name: "mycli", Args: []string{"--"}, Prefix: true}},
		{in: "mycli --flag=v *", want: Rule{Name: "mycli", Args: []string{"--flag=v"}, Prefix: true}},
		{in: "mycli =x", want: Rule{Name: "mycli", Args: []string{"=x"}}},
		{in: "Git log", want: Rule{Name: "Git", Args: []string{"log"}}},
		{in: "git#x", want: Rule{Name: "git#x"}},

		// Whitespace.
		{in: "  git commit *  ", want: Rule{Name: "git", Args: []string{"commit"}, Prefix: true},
			wantStr: "git commit *"},
		{in: "git\tlog\t*", want: Rule{Name: "git", Args: []string{"log"}, Prefix: true}},
		{in: "git   log    *", want: Rule{Name: "git", Args: []string{"log"}, Prefix: true}},
		{in: "git log\r\n", want: Rule{Name: "git", Args: []string{"log"}}, wantStr: "git log"},
		{in: "git log \\\n *", want: Rule{Name: "git", Args: []string{"log"}, Prefix: true}},

		// The Claude Code form.
		{in: "git add:*", want: Rule{Name: "git", Args: []string{"add"}, Prefix: true}, wantStr: "git add *"},
		{in: "git:*", want: Rule{Name: "git", Prefix: true}, wantStr: "git *"},
		{in: "git add:*  ", want: Rule{Name: "git", Args: []string{"add"}, Prefix: true}, wantStr: "git add *"},
		{in: "git add:*:*", want: Rule{Name: "git", Args: []string{"add:*"}, Prefix: true}},
		{in: "mycli a:b", want: Rule{Name: "mycli", Args: []string{"a:b"}}},
		{in: "mycli 'a:*'", want: Rule{Name: "mycli", Args: []string{`a:\*`}}},

		// Unquoted glob characters match within an argument; only a
		// final lone * matches further arguments.
		{in: "mycli get foo-*", want: Rule{Name: "mycli", Args: []string{"get", "foo-*"}}},
		{in: "mycli * x", want: Rule{Name: "mycli", Args: []string{"*", "x"}}},
		{in: "mycli * *", want: Rule{Name: "mycli", Args: []string{"*"}, Prefix: true}},
		{in: "mycli **", want: Rule{Name: "mycli", Args: []string{"**"}}},
		{in: "mycli *x*", want: Rule{Name: "mycli", Args: []string{"*x*"}}},
		{in: "mycli ? x", want: Rule{Name: "mycli", Args: []string{"?", "x"}}},
		{in: "mycli [ab] *", want: Rule{Name: "mycli", Args: []string{"[ab]"}, Prefix: true}},
		{in: "mycli [!a]", want: Rule{Name: "mycli", Args: []string{"[!a]"}}},
		{in: "mycli [^a]", want: Rule{Name: "mycli", Args: []string{"[^a]"}}},
		{in: "mycli [[:alpha:]]", want: Rule{Name: "mycli", Args: []string{"[[:alpha:]]"}}},
		{in: "mycli [ab", want: Rule{Name: "mycli", Args: []string{"[ab"}}},
		{in: "mycli }", want: Rule{Name: "mycli", Args: []string{"}"}}},

		// Quoted and escaped text matches literally.
		{in: `mycli '*' "a b" c\*`, want: Rule{Name: "mycli", Args: []string{`\*`, "a b", `c\*`}}},
		{in: `mycli "*"`, want: Rule{Name: "mycli", Args: []string{`\*`}}},
		{in: `mycli \*`, want: Rule{Name: "mycli", Args: []string{`\*`}}},
		{in: `mycli '?' '['`, want: Rule{Name: "mycli", Args: []string{`\?`, `\[`}}},
		{in: `mycli a\ b`, want: Rule{Name: "mycli", Args: []string{"a b"}}},
		{in: `mycli "a"'*'b`, want: Rule{Name: "mycli", Args: []string{`a\*b`}}},
		{in: `mycli ''`, want: Rule{Name: "mycli", Args: []string{""}}},
		{in: `mycli ""`, want: Rule{Name: "mycli", Args: []string{""}}},
		{in: `mycli '$a' \$b`, want: Rule{Name: "mycli", Args: []string{"$a", "$b"}}},
		{in: `mycli $ a$`, want: Rule{Name: "mycli", Args: []string{"$", "a$"}}},
		{in: `mycli "a\"b" "a\$b" "a\b"`, want: Rule{Name: "mycli", Args: []string{`a"b`, "a$b", `a\\b`}}},
		{in: `mycli '\'`, want: Rule{Name: "mycli", Args: []string{`\\`}}},
		{in: `mycli \\`, want: Rule{Name: "mycli", Args: []string{`\\`}}},
		{in: "mycli 'a\nb'", want: Rule{Name: "mycli", Args: []string{"a\nb"}}},
		{in: "mycli ~/x ~", want: Rule{Name: "mycli", Args: []string{"~/x", "~"}}},

		// Braces are literal unless bash would expand them.
		{in: "mycli {}", want: Rule{Name: "mycli", Args: []string{"{}"}}},
		{in: "mycli {a}", want: Rule{Name: "mycli", Args: []string{"{a}"}}},
		{in: "mycli a{b}c", want: Rule{Name: "mycli", Args: []string{"a{b}c"}}},
		{in: "mycli {", want: Rule{Name: "mycli", Args: []string{"{"}}},
		{in: `mycli \{a,b\}`, want: Rule{Name: "mycli", Args: []string{"{a,b}"}}},
		{in: `mycli '{a,b}'`, want: Rule{Name: "mycli", Args: []string{"{a,b}"}}},
		{in: "mycli {1..}", want: Rule{Name: "mycli", Args: []string{"{1..}"}}},

		// Paths are cleaned, and keep a slash.
		{in: "/usr/bin/git log", want: Rule{Name: "/usr/bin/git", Args: []string{"log"}}},
		{in: "//usr/bin/git", want: Rule{Name: "/usr/bin/git"}},
		{in: "/usr/bin/../bin/./git", want: Rule{Name: "/usr/bin/git"}},
		{in: "./bin/../mycli a", want: Rule{Name: "./mycli", Args: []string{"a"}}},
		{in: "./mycli", want: Rule{Name: "./mycli"}},
		{in: "bin/mycli", want: Rule{Name: "bin/mycli"}},
		{in: "./bin/mycli", want: Rule{Name: "bin/mycli"}},
		{in: "../bin/mycli", want: Rule{Name: "../bin/mycli"}},
		{in: `"my cli" a`, want: Rule{Name: "my cli", Args: []string{"a"}}},
		{in: `\git log`, want: Rule{Name: "git", Args: []string{"log"}}},
		{in: `'git' log`, want: Rule{Name: "git", Args: []string{"log"}}},
		{in: `'g*' x`, want: Rule{Name: "g*", Args: []string{"x"}}},

		// Not a pattern.
		{in: "", wantErr: "empty command pattern"},
		{in: "   ", wantErr: "empty command pattern"},
		{in: "\t\n", wantErr: "empty command pattern"},

		// More than one command.
		{in: "git log; rm x", wantErr: "single command"},
		{in: "git log;", wantErr: "single command"},
		{in: "git log ;", wantErr: "single command"},
		{in: "git log\n*", wantErr: "single command"},
		{in: "git log | jq", wantErr: "single command"},
		{in: "git log && rm x", wantErr: "single command"},
		{in: "git log || rm x", wantErr: "single command"},
		{in: "git log &", wantErr: "single command"},
		{in: "! git log", wantErr: "single command"},
		{in: "git log # x", wantErr: "single command"},
		{in: "time git log", wantErr: "single command"},
		{in: "coproc git", wantErr: "single command"},
		{in: "{ git; }", wantErr: "single command"},
		{in: "(git)", wantErr: "single command"},
		{in: "if true; then git; fi", wantErr: "single command"},
		{in: "for x in a; do git; done", wantErr: "single command"},
		{in: "f() { git; }", wantErr: "single command"},

		// Words whose value is only known when they run.
		{in: "git $X", wantErr: "literal words"},
		{in: "git ${X}", wantErr: "literal words"},
		{in: "git a$X", wantErr: "literal words"},
		{in: `git "$X"`, wantErr: "literal words"},
		{in: `git "a${X}b"`, wantErr: "literal words"},
		{in: "git $(id)", wantErr: "literal words"},
		{in: `git "$(id)"`, wantErr: "literal words"},
		{in: "git `id`", wantErr: "literal words"},
		{in: "git $((1))", wantErr: "literal words"},
		{in: "git $'x'", wantErr: "literal words"},
		{in: `git $"x"`, wantErr: "literal words"},
		{in: "git <(x)", wantErr: "literal words"},
		{in: "git @(a|b)", wantErr: "literal words"},
		{in: "git !(a)", wantErr: "literal words"},
		{in: "$GIT log", wantErr: "literal words"},

		// Assignments and redirections.
		{in: "X=1 git log", wantErr: "literal words"},
		{in: "git log > f", wantErr: "literal words"},
		{in: "git log 2>&1", wantErr: "literal words"},
		{in: "git < f", wantErr: "literal words"},
		{in: "git <<< x", wantErr: "single command"},

		// Brace expansions.
		{in: "git {log,show}", wantErr: "brace expansions"},
		{in: "git {1..3}", wantErr: "brace expansions"},
		{in: "git {a..c}", wantErr: "brace expansions"},
		{in: "git x{,}", wantErr: "brace expansions"},
		{in: "git {a,}", wantErr: "brace expansions"},
		{in: "{git,rm} x", wantErr: "brace expansions"},

		// Names that do not tell one program.
		{in: "g* log", wantErr: "name its program literally"},
		{in: "g?t", wantErr: "name its program literally"},
		{in: "[g]it", wantErr: "name its program literally"},
		{in: "*", wantErr: "name its program literally"},
		{in: ":*", wantErr: "right after"},
		{in: "'' x", wantErr: "must name a program"},
		{in: "~/bin/mycli *", wantErr: "absolute path"},
		{in: "~user/bin/mycli", wantErr: "absolute path"},
		{in: "~", wantErr: "absolute path"},
		{in: ".", wantErr: "must name a program"},
		{in: "..", wantErr: "must name a program"},
		{in: "/", wantErr: "must name a program"},
		{in: "./", wantErr: "must name a program"},
		{in: "git/", wantErr: "must name a program"},
		{in: "/usr/bin/", wantErr: "must name a program"},
		{in: "/usr/bin/..", wantErr: "must name a program"},

		// A detached :* is not the Claude Code form.
		{in: "git add :*", wantErr: "right after"},
		{in: "git add\t:*", wantErr: "right after"},
		{in: `git add\:*`, want: Rule{Name: "git", Args: []string{"add:*"}}},
		{in: "git 'add':*", want: Rule{Name: "git", Args: []string{"add"}, Prefix: true}},

		// Syntax errors.
		{in: "mycli '", wantErr: "does not parse"},
		{in: `mycli "`, wantErr: "does not parse"},
		{in: "git (", wantErr: "does not parse"},
		{in: "git )", wantErr: "does not parse"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got, err := ParseRule(tc.in)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				if strings.TrimSpace(tc.in) != "" {
					assert.Contains(t, err.Error(), strconv.Quote(tc.in), "the error names the pattern")
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want.Name, got.Name)
			assert.Equal(t, tc.want.Args, got.Args)
			assert.Equal(t, tc.want.Prefix, got.Prefix)
			if tc.wantStr != "" {
				assert.Equal(t, tc.wantStr, got.String())
			}
		})
	}
}

func TestRuleString(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "git log *", Rule{Name: "git", Args: []string{"log"}, Prefix: true}.String())
	assert.Equal(t, "git", Rule{Name: "git"}.String())
	r, err := ParseRule(`  mycli  'a b'  * `)
	require.NoError(t, err)
	assert.Equal(t, `mycli  'a b'  *`, r.String(), "a parsed rule keeps its source")
}

func TestRuleMatch(t *testing.T) {
	t.Parallel()

	// last is the last program the plan of script runs.
	last := func(t *testing.T, script string) Program {
		plan := Command("bash", []string{"-c", script}, nil, nil)
		require.False(t, plan.Opaque, plan.Reason)
		return plan.Programs[len(plan.Programs)-1]
	}
	cases := []struct {
		rule   string
		script string
		want   bool
	}{
		// Argument counts.
		{rule: "mycli a *", script: "mycli a", want: true},
		{rule: "mycli a *", script: "mycli a b c", want: true},
		{rule: "mycli a *", script: "mycli b", want: false},
		{rule: "mycli a *", script: "mycli", want: false},
		{rule: "mycli a", script: "mycli a", want: true},
		{rule: "mycli a", script: "mycli a b", want: false},
		{rule: "mycli a", script: "mycli", want: false},
		{rule: "mycli a b", script: "mycli a b", want: true},
		{rule: "mycli a b", script: "mycli a b c", want: false},
		{rule: "mycli", script: "mycli", want: true},
		{rule: "mycli", script: "mycli a", want: false},
		{rule: "mycli *", script: "mycli", want: true},
		{rule: "mycli *", script: "mycli a b c d e f", want: true},
		{rule: "mycli * x", script: "mycli y x", want: true},
		{rule: "mycli * x", script: "mycli x", want: false},
		{rule: "mycli * x", script: "mycli y x z", want: false},

		// Arguments known only when the command runs.
		{rule: "mycli a *", script: "mycli a $X", want: true},
		{rule: "mycli a", script: "mycli a $X", want: false},
		{rule: "mycli a *", script: "mycli $X a", want: false},
		{rule: "mycli *", script: "mycli $X", want: true},
		{rule: "mycli", script: "mycli $X", want: false},
		{rule: "mycli a *", script: `mycli a"$X"`, want: false},
		{rule: "mycli a *", script: "mycli a$(id)", want: false},
		{rule: "mycli *.txt", script: "mycli *.txt", want: false},
		{rule: "mycli *", script: "mycli *.txt", want: true},
		{rule: "mycli a *", script: "mycli {a,b}", want: false},

		// Argument patterns match whole arguments.
		{rule: "mycli get foo-*", script: "mycli get foo-bar", want: true},
		{rule: "mycli get foo-*", script: "mycli get foo-", want: true},
		{rule: "mycli get foo-*", script: "mycli get bar", want: false},
		{rule: "mycli get foo-*", script: "mycli get xfoo-bar", want: false},
		{rule: "mycli a", script: "mycli ab", want: false},
		{rule: "mycli b", script: "mycli ab", want: false},
		{rule: "mycli *.txt", script: "mycli a.txt", want: true},
		{rule: "mycli *.txt", script: "mycli a.txtx", want: false},
		{rule: "mycli a.b", script: "mycli axb", want: false},
		{rule: "mycli a*", script: "mycli a/b", want: true},
		{rule: "mycli **", script: "mycli a/b", want: true},
		{rule: "mycli -*", script: "mycli --force", want: true},
		{rule: "mycli ? x", script: "mycli a x", want: true},
		{rule: "mycli ? x", script: "mycli ab x", want: false},
		{rule: "mycli ?", script: "mycli é", want: true},
		{rule: "mycli ?", script: "mycli ''", want: false},
		{rule: "mycli [ab] *", script: "mycli b c", want: true},
		{rule: "mycli [ab] *", script: "mycli c", want: false},
		{rule: "mycli [!a]", script: "mycli b", want: true},
		{rule: "mycli [!a]", script: "mycli a", want: false},
		{rule: "mycli [^a]", script: "mycli a", want: false},
		{rule: "mycli [[:digit:]]", script: "mycli 1", want: true},
		{rule: "mycli [[:digit:]]", script: "mycli a", want: false},
		{rule: "mycli [ab", script: "mycli '[ab'", want: true},
		{rule: "mycli a", script: "mycli A", want: false},
		{rule: "mycli a*", script: "mycli $'a\\nb'", want: false},

		// Quoted rule text matches literally.
		{rule: "mycli get 'foo-*'", script: "mycli get foo-bar", want: false},
		{rule: "mycli get 'foo-*'", script: "mycli get 'foo-*'", want: true},
		{rule: "mycli get 'foo-*'", script: `mycli get "foo-*"`, want: true},
		{rule: "mycli get 'foo-*'", script: `mycli get foo-\*`, want: true},
		{rule: "mycli 'a b'", script: `mycli "a b"`, want: true},
		{rule: "mycli 'a b'", script: "mycli a b", want: false},
		{rule: "mycli ''", script: "mycli ''", want: true},
		{rule: "mycli ''", script: "mycli", want: false},
		{rule: "mycli *", script: "mycli ''", want: true},
		{rule: "mycli {}", script: "mycli {}", want: true},
		{rule: `mycli \{a,b\}`, script: "mycli '{a,b}'", want: true},

		// Program names.
		{rule: "mycli *", script: "./mycli", want: false},
		{rule: "mycli *", script: "/usr/bin/mycli", want: false},
		{rule: "mycli *", script: "bin/mycli", want: false},
		{rule: "Mycli a", script: "mycli a", want: false},
		{rule: "mycli a *", script: `"mycli" a`, want: true},
		{rule: "mycli a *", script: `\mycli a`, want: true},
		{rule: "'my cli' *", script: `"my cli" a`, want: true},
		{rule: "./mycli *", script: "./mycli a", want: true},
		{rule: "./mycli *", script: "././mycli a", want: true},
		{rule: "./mycli *", script: "mycli a", want: false},
		{rule: "./mycli *", script: "/mycli a", want: false},
		{rule: "bin/mycli *", script: "./bin/mycli x", want: true},
		{rule: "./bin/mycli *", script: "bin/mycli x", want: true},
		{rule: "./bin/mycli *", script: "bin/../bin/mycli x", want: true},
		{rule: "../bin/mycli *", script: "../bin/mycli x", want: true},
		{rule: "../bin/mycli *", script: "bin/mycli x", want: false},
		{rule: "/opt/x/mycli *", script: "/opt/x/../x/mycli a", want: true},
		{rule: "/opt/x/mycli *", script: "/opt/x/./mycli", want: true},
		{rule: "/opt/x/mycli *", script: "/opt//x/mycli", want: true},
		{rule: "/opt/x/mycli *", script: "mycli", want: false},
		{rule: "/opt/x/mycli *", script: "/opt/y/mycli", want: false},
		{rule: "/opt/x/mycli *", script: "opt/x/mycli", want: false},

		// Programs other programs run.
		{rule: "mycli a *", script: "A=1 mycli a", want: true},
		{rule: "mycli a *", script: "command mycli a", want: true},
		{rule: "mycli a *", script: "exec mycli a", want: true},
		{rule: "mycli a *", script: "nice mycli a b", want: true},
		{rule: "mycli a *", script: "timeout 5 mycli a", want: true},
		{rule: "mycli a *", script: "echo $(mycli a)", want: true},
		{rule: "mycli a *", script: "bash -c 'mycli a'", want: true},
		{rule: "mycli a *", script: "echo x | xargs mycli a", want: true},
		{rule: "mycli a", script: "echo x | xargs mycli a", want: false},
		{rule: "mycli *", script: `find . -exec mycli {} \;`, want: true},
		{rule: "mycli a *", script: `find . -exec mycli {} \;`, want: false},

		// git's global options are skipped unless they change what runs.
		{rule: "git commit *", script: "git -C d commit -m x", want: true},
		{rule: "git commit *", script: "git --no-pager commit", want: true},
		{rule: "git commit *", script: "git -C d log", want: false},
		{rule: "git log", script: "git -C d log", want: true},
		{rule: "git *", script: "git -C d", want: true},
		{rule: "git commit *", script: "git -c a=b commit", want: false},
		{rule: "git log *", script: "git -c 'alias.log=!rm -rf x' log", want: false},
		{rule: "git commit *", script: "git -C $D commit", want: false},
		{rule: "git commit *", script: `git commit -m "$M"`, want: true},
		{rule: "git commit", script: `git commit -m "$M"`, want: false},
		{rule: "git add:*", script: "git add .", want: true},
		{rule: "kill *", script: "kill 1", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.rule+" ~ "+tc.script, func(t *testing.T) {
			t.Parallel()
			r, err := ParseRule(tc.rule)
			require.NoError(t, err)
			assert.Equal(t, tc.want, r.Match(last(t, tc.script)))
		})
	}

	t.Run("a program whose name is not known matches no rule", func(t *testing.T) {
		t.Parallel()
		r, err := ParseRule("mycli *")
		require.NoError(t, err)
		assert.False(t, r.Match(Program{Name: "mycli", Opaque: true}))
	})
}
