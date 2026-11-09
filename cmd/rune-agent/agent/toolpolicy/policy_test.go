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

package toolpolicy

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"unstable.build/rune/internal/term/shellplan"
)

func mustParse(t *testing.T, allowed, denied []string) Policy {
	t.Helper()
	p, err := Parse(allowed, denied)
	require.NoError(t, err)
	return p
}

func mustRules(t *testing.T, patterns ...string) []shellplan.Rule {
	t.Helper()
	rules := make([]shellplan.Rule, len(patterns))
	for i, p := range patterns {
		r, err := shellplan.ParseRule(p)
		require.NoError(t, err)
		rules[i] = r
	}
	return rules
}

func TestSplitList(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want []string
	}{
		{in: "", want: nil},
		{in: "   ", want: nil},
		{in: ",,,", want: nil},
		{in: " , \t, ", want: nil},
		{in: "read_file", want: []string{"read_file"}},
		{in: "read_file search_content", want: []string{"read_file", "search_content"}},
		{in: "read_file, search_content,find_files", want: []string{"read_file", "search_content", "find_files"}},
		{in: "a,,b", want: []string{"a", "b"}},
		{in: ",a,", want: []string{"a"}},
		{in: "read_file\n\tfind_files", want: []string{"read_file", "find_files"}},
		{in: "a\r\nb", want: []string{"a", "b"}},
		{in: "a\u00a0b", want: []string{"a", "b"}},
		{in: "a;b", want: []string{"a;b"}},
		{in: "  Bash(git log *)  Read ", want: []string{"Bash(git log *)", "Read"}},
		{in: "bash(git log *),read_file", want: []string{"bash(git log *)", "read_file"}},
		{in: "skill(a b) agent(c, d)", want: []string{"skill(a b)", "agent(c, d)"}},

		// Parentheses nest, and quotes and backslashes inside them hide
		// parentheses, commas and spaces.
		{in: "bash(echo (x)) read_file", want: []string{"bash(echo (x))", "read_file"}},
		{in: "bash(git commit -m 'a, b'),skill(acme-*)", want: []string{"bash(git commit -m 'a, b')", "skill(acme-*)"}},
		{in: `bash(echo ")") read_file`, want: []string{`bash(echo ")")`, "read_file"}},
		{in: `bash(echo ')') read_file`, want: []string{`bash(echo ')')`, "read_file"}},
		{in: `bash(echo "a\")b") x`, want: []string{`bash(echo "a\")b")`, "x"}},
		{in: `bash(echo 'a\') x`, want: []string{`bash(echo 'a\')`, "x"}},
		{in: `bash(a\)) b`, want: []string{`bash(a\))`, "b"}},
		{in: "bash(git log --format='%h (%an)') read_file",
			want: []string{"bash(git log --format='%h (%an)')", "read_file"}},

		// Quotes and backslashes outside parentheses are part of names.
		{in: `'read_file' "x" a\ b`, want: []string{"'read_file'", `"x"`, `a\`, "b"}},

		// Unbalanced text keeps going to the end, so that it fails to
		// parse rather than splitting into entries that would.
		{in: "bash(git log read_file", want: []string{"bash(git log read_file"}},
		{in: "bash(git log 'x) read_file", want: []string{"bash(git log 'x) read_file"}},
		{in: "a) b", want: []string{"a)", "b"}},
		{in: "bash (git log *)", want: []string{"bash", "(git log *)"}},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, SplitList(tc.in), tc.in)
	}
}

func TestListUnmarshalYAML(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		in      string
		want    List
		wantErr string
	}{
		{name: "plain string", in: "tools: read_file bash(git log *)", want: List{"read_file", "bash(git log *)"}},
		{name: "commas", in: "tools: read_file,bash(git log *), find_files",
			want: List{"read_file", "bash(git log *)", "find_files"}},
		{name: "double quoted", in: `tools: "read_file, bash(git commit -m 'a, b')"`,
			want: List{"read_file", "bash(git commit -m 'a, b')"}},
		{name: "single quoted", in: `tools: 'bash(git commit -m "a, b")'`,
			want: List{`bash(git commit -m "a, b")`}},
		{name: "folded", in: "tools: >-\n  read_file\n  bash(git log *)\n", want: List{"read_file", "bash(git log *)"}},
		{name: "literal", in: "tools: |\n  read_file\n  bash(git log *)\n", want: List{"read_file", "bash(git log *)"}},
		{name: "tagged", in: "tools: !!str read_file", want: List{"read_file"}},
		{name: "number", in: "tools: 5", want: List{"5"}},
		{name: "flow", in: `tools: [read_file, "bash(git log *)", 'skill(acme-*)']`,
			want: List{"read_file", "bash(git log *)", "skill(acme-*)"}},
		{name: "block", in: "tools:\n  - read_file\n  - bash(git log *)\n  - Bash(git add:*)\n",
			want: List{"read_file", "bash(git log *)", "Bash(git add:*)"}},
		{name: "items are not split", in: `tools: ["read_file find_files", "a,b"]`,
			want: List{"read_file find_files", "a,b"}},
		{name: "scalars of other types", in: "tools: [1, true]", want: List{"1", "true"}},
		{name: "an unquoted comma in a flow list splits the entry",
			in: "tools: [bash(git commit -m 'a, b')]", want: List{"bash(git commit -m 'a", "b')"}},

		// An empty value is an empty list, not a missing one.
		{name: "empty string", in: "tools: ''", want: List{}},
		{name: "separators only", in: "tools: ' , '", want: List{}},
		{name: "empty flow list", in: "tools: []", want: List{}},
		{name: "blank entry", in: `tools: [""]`, want: List{""}},
		{name: "null", in: "tools:", want: nil},
		{name: "tilde", in: "tools: ~", want: nil},

		{name: "mapping", in: "tools: {a: b}", wantErr: "expected a string or a list of strings"},
		{name: "nested list", in: "tools: [[a]]", wantErr: "cannot unmarshal"},
		{name: "mapping item", in: "tools:\n  - Bash(git add: *)\n", wantErr: "yaml"},
		{name: "alias", in: "tools: [*]", wantErr: "yaml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var v struct {
				Tools List `yaml:"tools"`
			}
			err := yaml.Unmarshal([]byte(tc.in), &v)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, v.Tools)
		})
	}
}

func TestParse(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		allowed []string
		denied  []string
		// want is the single layer of the policy, or nil for the zero
		// Policy.
		want *layer
	}{
		{name: "nothing", want: nil},
		{name: "an empty deny list", denied: []string{}, want: nil},
		{name: "blank denies", denied: []string{"", " \t"}, want: nil},
		{name: "an empty allow list allows nothing", allowed: []string{}, want: &layer{allowed: true}},
		{name: "blank allows allow nothing", allowed: []string{"", " \t"}, want: &layer{allowed: true}},
		{name: "tool names",
			allowed: []string{"read_file", " find_files ", "github_*", "*", "1"},
			want:    &layer{allowed: true, allowNames: []string{"read_file", "find_files", "github_*", "*", "1"}}},
		{name: "duplicates",
			allowed: []string{"read_file", "read_file"},
			want:    &layer{allowed: true, allowNames: []string{"read_file", "read_file"}}},
		{name: "names keep their case",
			allowed: []string{"Bash", "SKILL", "Read_File"},
			want:    &layer{allowed: true, allowNames: []string{"Bash", "SKILL", "Read_File"}}},
		{name: "command patterns",
			allowed: []string{"bash(git log *)", "Bash(git add:*)", "BASH( jq * )", "bash (mycli a)"},
			want: &layer{allowed: true,
				allowCmds: mustRules(t, "git log *", "git add:*", "jq *", "mycli a")}},
		{name: "command patterns with quoted parentheses",
			allowed: []string{`bash(echo ")")`, "bash(git log --format='%h (%an)')", `bash(a\))`},
			want: &layer{allowed: true,
				allowCmds: mustRules(t, `echo ")"`, "git log --format='%h (%an)'", `a\)`)}},
		{name: "skill scripts",
			allowed: []string{"bash($acme-notes)", "bash($acme-*)", "BASH( $x )", "bash($a.b_c:d)"},
			want:    &layer{allowed: true, allowScripts: []string{"acme-notes", "acme-*", "x", "a.b_c:d"}}},
		{name: "skill patterns keep their case",
			allowed: []string{"skill(acme-*)", "Skill(Other)", "skill([ab]?)"},
			want:    &layer{allowed: true, allowSkills: []string{"acme-*", "Other", "[ab]?"}}},
		{name: "agent patterns are lowercased",
			allowed: []string{"agent(Explore)", "AGENT(plan-*)"},
			want:    &layer{allowed: true, allowAgents: []string{"explore", "plan-*"}}},
		{name: "a * pattern allows the whole tool",
			allowed: []string{"skill(*)", "Agent( * )"},
			want:    &layer{allowed: true, allowNames: []string{"skill", "Agent"}}},
		{name: "denies",
			denied: []string{"bash", "github_*", "bash(rm *)", "skill(x)", "skill(*)", "agent(Default)", "agent(*)"},
			want: &layer{denyNames: []string{"bash", "github_*"}, denyCmds: mustRules(t, "rm *"),
				denySkills: []string{"x", "*"}, denyAgents: []string{"default", "*"}}},
		{name: "allows and denies",
			allowed: []string{"read_file", "bash(git *)"}, denied: []string{"bash(git push *)"},
			want: &layer{allowed: true, allowNames: []string{"read_file"},
				allowCmds: mustRules(t, "git *"), denyCmds: mustRules(t, "git push *")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, err := Parse(tc.allowed, tc.denied)
			require.NoError(t, err)
			if tc.want == nil {
				assert.True(t, p.IsZero())
				return
			}
			require.Len(t, p.layers, 1)
			assert.Equal(t, *tc.want, p.layers[0])
		})
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()

	const (
		allowedOnly = 1 << iota
		deniedOnly
		both = allowedOnly | deniedOnly
	)
	cases := []struct {
		entry   string
		lists   int
		wantErr string
	}{
		// Patterns on tools that take none.
		{entry: "read_file(x)", lists: both, wantErr: "only bash, skill and agent take a pattern"},
		{entry: "Read(x)", lists: both, wantErr: "only bash, skill and agent take a pattern"},
		{entry: "exec_command(git *)", lists: both, wantErr: "only bash, skill and agent take a pattern"},
		{entry: "run_command(git *)", lists: both, wantErr: "only bash, skill and agent take a pattern"},
		{entry: "github_*(x)", lists: both, wantErr: "only bash, skill and agent take a pattern"},
		{entry: "skill)x(y)", lists: both, wantErr: "only bash, skill and agent take a pattern"},

		// Parentheses.
		{entry: "bash(", lists: both, wantErr: "unbalanced parentheses"},
		{entry: "bash)", lists: both, wantErr: "unbalanced parentheses"},
		{entry: "bash(git", lists: both, wantErr: "unbalanced parentheses"},
		{entry: "read_file)", lists: both, wantErr: "unbalanced parentheses"},
		{entry: ")(", lists: both, wantErr: "unbalanced parentheses"},
		{entry: "bash(x) y", lists: both, wantErr: "unbalanced parentheses"},
		{entry: "bash(x)y", lists: both, wantErr: "unbalanced parentheses"},
		{entry: "(git)", lists: both, wantErr: "no tool name"},
		{entry: "(git log *)", lists: both, wantErr: "no tool name"},
		{entry: "bash()", lists: both, wantErr: "empty pattern"},
		{entry: "bash( )", lists: both, wantErr: "empty pattern"},
		{entry: "skill()", lists: both, wantErr: "empty pattern"},
		{entry: "agent(\t)", lists: both, wantErr: "empty pattern"},

		// Command patterns.
		{entry: "bash(git $X)", lists: both, wantErr: "literal words"},
		{entry: "bash(git; rm x)", lists: both, wantErr: "single command"},
		{entry: "bash(git log;)", lists: both, wantErr: "single command"},
		{entry: "bash(git log | jq)", lists: both, wantErr: "single command"},
		{entry: "bash(a)(b)", lists: both, wantErr: "does not parse"},
		{entry: "bash(echo (x)", lists: both, wantErr: "does not parse"},
		{entry: "bash((x))", lists: both, wantErr: "single command"},
		{entry: "bash(g* x)", lists: both, wantErr: "name its program literally"},
		{entry: "bash(~/bin/x)", lists: both, wantErr: "absolute path"},
		{entry: "bash(bin/)", lists: both, wantErr: "must name a program"},
		{entry: "bash(git log > f)", lists: both, wantErr: "literal words"},
		{entry: "bash(git add :*)", lists: both, wantErr: "right after"},
		{entry: "bash(git {log,show})", lists: both, wantErr: "brace expansions"},
		{entry: "bash(git commit -m 'a)", lists: both, wantErr: "does not parse"},

		// Skill and agent patterns.
		{entry: "skill([)", lists: both, wantErr: "syntax error in pattern"},
		{entry: "agent([)", lists: both, wantErr: "syntax error in pattern"},
		{entry: "skill(a b)", lists: both, wantErr: "is not a skill name pattern"},
		{entry: "skill(a/b)", lists: both, wantErr: "is not a skill name pattern"},
		{entry: `skill("a")`, lists: both, wantErr: "is not a skill name pattern"},
		{entry: "skill(a,b)", lists: both, wantErr: "is not a skill name pattern"},
		{entry: "skill(a)b)", lists: both, wantErr: "is not a skill name pattern"},
		{entry: "skill(a(b))", lists: both, wantErr: "is not a skill name pattern"},
		{entry: "skill($x)", lists: both, wantErr: "is not a skill name pattern"},
		{entry: `skill(a\*)`, lists: both, wantErr: "is not a skill name pattern"},
		{entry: "agent(a b)", lists: both, wantErr: "is not a sub-agent type pattern"},
		{entry: "agent(a;b)", lists: both, wantErr: "is not a sub-agent type pattern"},

		// Skill scripts.
		{entry: "bash($)", lists: allowedOnly, wantErr: "write $ and a skill name"},
		{entry: "bash($ acme)", lists: allowedOnly, wantErr: "write $ and a skill name"},
		{entry: "bash($acme notes)", lists: allowedOnly, wantErr: "write $ and a skill name"},
		{entry: "bash($acme *)", lists: allowedOnly, wantErr: "write $ and a skill name"},
		{entry: "bash($acme/scripts)", lists: allowedOnly, wantErr: "write $ and a skill name"},
		{entry: "bash($[)", lists: allowedOnly, wantErr: "write $ and a skill name"},
		{entry: "bash($$x)", lists: allowedOnly, wantErr: "write $ and a skill name"},
		{entry: "bash(${x})", lists: allowedOnly, wantErr: "write $ and a skill name"},
		{entry: "bash($a,b)", lists: allowedOnly, wantErr: "write $ and a skill name"},
		{entry: "bash($'acme')", lists: allowedOnly, wantErr: "write $ and a skill name"},
		{entry: "bash($acme-notes)", lists: deniedOnly, wantErr: "the scripts of a skill can only be allowed"},
		{entry: "Bash($*)", lists: deniedOnly, wantErr: "the scripts of a skill can only be allowed"},

		// Tool names.
		{entry: "[", lists: both, wantErr: "syntax error in pattern"},
		{entry: "a[", lists: both, wantErr: "syntax error in pattern"},
		{entry: "read file", lists: both, wantErr: "is not a tool name pattern"},
		{entry: `"read_file"`, lists: both, wantErr: "is not a tool name pattern"},
		{entry: "read_file,", lists: both, wantErr: "is not a tool name pattern"},
		{entry: `read\_file`, lists: both, wantErr: "is not a tool name pattern"},
		{entry: "a/b", lists: both, wantErr: "is not a tool name pattern"},
	}
	for _, tc := range cases {
		t.Run(tc.entry, func(t *testing.T) {
			t.Parallel()
			check := func(allowed, denied []string) {
				t.Helper()
				_, err := Parse(allowed, denied)
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.Contains(t, err.Error(), strconv.Quote(tc.entry), "the error names the entry")
			}
			if tc.lists&allowedOnly != 0 {
				check([]string{"read_file", tc.entry}, nil)
			}
			if tc.lists&deniedOnly != 0 {
				check(nil, []string{tc.entry})
			}
		})
	}
}

func TestAllowsTool(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		allowed []string
		denied  []string
		want    map[string]bool
	}{
		{name: "zero policy allows everything",
			want: map[string]bool{"read_file": true, "bash": true, "github_issue": true}},
		{name: "an empty allow list allows nothing",
			allowed: []string{},
			want:    map[string]bool{"read_file": false, "bash": false, "skill": false, "agent": false}},
		{name: "blank allows allow nothing",
			allowed: []string{" "},
			want:    map[string]bool{"read_file": false, "bash": false}},
		{name: "exact names",
			allowed: []string{"read_file", "find_files"},
			want:    map[string]bool{"read_file": true, "find_files": true, "bash": false, "skill": false}},
		{name: "globs on MCP tools",
			allowed: []string{"github_*"},
			want:    map[string]bool{"github_issue": true, "github_pr_list": true, "gitlab_issue": false}},
		{name: "glob classes",
			allowed: []string{"find_?iles", "[rw]*_file"},
			want: map[string]bool{"find_files": true, "find_xfiles": false, "read_file": true,
				"write_file": true, "apply_patch": false}},
		{name: "* allows every tool",
			allowed: []string{"*"},
			want: map[string]bool{"read_file": true, "bash": true, "exec_command": true, "skill": true,
				"agent": true, "github_issue": true}},
		{name: "tool names are case-sensitive",
			allowed: []string{"Read_File"},
			want:    map[string]bool{"read_file": false, "Read_File": true}},
		{name: "bash, skill and agent are not",
			allowed: []string{"bAsH", "SKILL", "Agent"},
			want: map[string]bool{"bash": true, "exec_command": true, "run_command": true,
				"skill": true, "agent": true}},
		{name: "bash stands for every shell tool",
			allowed: []string{"Bash"},
			want:    map[string]bool{"bash": true, "exec_command": true, "run_command": true, "read_file": false}},
		{name: "a shell tool by name is only that tool",
			allowed: []string{"exec_command"},
			want:    map[string]bool{"exec_command": true, "bash": false, "run_command": false}},
		{name: "patterns imply their tool",
			allowed: []string{"bash(git log *)", "skill(acme-*)", "agent(explore)"},
			want: map[string]bool{"bash": true, "exec_command": true, "run_command": true,
				"skill": true, "agent": true, "read_file": false}},
		{name: "skill scripts imply the shell",
			allowed: []string{"bash($acme-notes)"},
			want:    map[string]bool{"bash": true, "exec_command": true, "skill": false}},
		{name: "no allow list removes what is denied",
			denied: []string{"bash", "github_*"},
			want: map[string]bool{"bash": false, "exec_command": false, "run_command": false,
				"github_issue": false, "read_file": true}},
		{name: "a denied name in any case",
			denied: []string{"BASH", "Skill"},
			want:   map[string]bool{"bash": false, "exec_command": false, "skill": false, "read_file": true}},
		{name: "deny wins over allow",
			allowed: []string{"github_*"}, denied: []string{"github_delete_repo"},
			want: map[string]bool{"github_issue": true, "github_delete_repo": false}},
		{name: "a denied tool loses its patterns",
			allowed: []string{"bash(git *)", "skill(acme-*)", "agent(explore)"},
			denied:  []string{"bash", "skill", "agent"},
			want:    map[string]bool{"bash": false, "exec_command": false, "skill": false, "agent": false}},
		{name: "a denied shell tool by name",
			allowed: []string{"bash"}, denied: []string{"exec_command"},
			want: map[string]bool{"bash": true, "run_command": true, "exec_command": false}},
		{name: "* denies everything",
			allowed: []string{"read_file", "bash"}, denied: []string{"*"},
			want: map[string]bool{"read_file": false, "bash": false}},
		{name: "patterned denies never remove the tool",
			denied: []string{"bash(rm *)", "skill(acme-*)", "agent(default)", "skill(*)", "agent(*)"},
			want:   map[string]bool{"bash": true, "skill": true, "agent": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := mustParse(t, tc.allowed, tc.denied)
			for tool, want := range tc.want {
				assert.Equal(t, want, p.AllowsTool(tool), tool)
			}
		})
	}
}

func TestNamesTool(t *testing.T) {
	t.Parallel()

	p := mustParse(t, []string{"ask_user_question", "github_*"}, nil)
	assert.True(t, p.NamesTool("ask_user_question"))
	assert.False(t, p.NamesTool("github_issue"), "a glob does not name a tool")
	assert.False(t, mustParse(t, nil, []string{"bash"}).NamesTool("read_file"))
	assert.False(t, Policy{}.NamesTool("read_file"))
	assert.False(t, Intersect(p, mustParse(t, nil, []string{"ask_user_question"})).
		NamesTool("ask_user_question"))
}

func TestAllowsSkillAndAgent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		allowed    []string
		denied     []string
		skills     map[string]bool
		agents     map[string]bool
		constrains bool
	}{
		{name: "zero policy",
			skills: map[string]bool{"acme-notes": true}, agents: map[string]bool{"default": true}},
		{name: "plain entries allow all",
			allowed: []string{"skill", "agent"},
			skills:  map[string]bool{"acme-notes": true}, agents: map[string]bool{"default": true}},
		{name: "plain entries in any case",
			allowed: []string{"Skill", "AGENT"},
			skills:  map[string]bool{"acme-notes": true}, agents: map[string]bool{"default": true}},
		{name: "* patterns allow all",
			allowed: []string{"skill(*)", "agent(*)"},
			skills:  map[string]bool{"acme-notes": true}, agents: map[string]bool{"default": true}},
		{name: "patterns",
			allowed:    []string{"skill(acme-*)", "agent(explore)"},
			skills:     map[string]bool{"acme-notes": true, "acme-": true, "other": false, "xacme-notes": false},
			agents:     map[string]bool{"explore": true, "Explore": true, "default": false, "plan": false},
			constrains: true},
		{name: "glob classes",
			allowed:    []string{"skill(acme-?)", "skill([bc]*)", "agent(pl[a-z]n)"},
			skills:     map[string]bool{"acme-x": true, "acme-xy": false, "beta": true, "delta": false},
			agents:     map[string]bool{"plan": true, "pl4n": false},
			constrains: true},
		{name: "skill names are case-sensitive",
			allowed: []string{"skill(acme-*)"},
			skills:  map[string]bool{"acme-notes": true, "Acme-notes": false}, constrains: true},
		{name: "agent types are not",
			allowed: []string{"AGENT(EXPLORE)"},
			agents:  map[string]bool{"explore": true, "Explore": true, "EXPLORE": true}, constrains: true},
		{name: "several patterns",
			allowed: []string{"skill(a)", "skill(b)", "agent(x)", "agent(y)"},
			skills:  map[string]bool{"a": true, "b": true, "c": false},
			agents:  map[string]bool{"x": true, "y": true, "z": false}, constrains: true},
		{name: "an allow list without them",
			allowed: []string{"read_file"},
			skills:  map[string]bool{"acme-notes": false}, agents: map[string]bool{"explore": false},
			constrains: true},
		{name: "an empty allow list",
			allowed: []string{},
			skills:  map[string]bool{"acme-notes": false}, agents: map[string]bool{"explore": false},
			constrains: true},
		{name: "deny without an allow list",
			denied: []string{"skill(acme-secret)", "agent(DEFAULT)"},
			skills: map[string]bool{"acme-notes": true, "acme-secret": false},
			agents: map[string]bool{"explore": true, "default": false, "Default": false}, constrains: true},
		{name: "deny wins over allow",
			allowed: []string{"skill", "agent(*)"}, denied: []string{"skill(acme-*)", "agent(default)"},
			skills: map[string]bool{"acme-notes": false, "other": true},
			agents: map[string]bool{"explore": true, "default": false}, constrains: true},
		{name: "denied * patterns hide everything",
			denied: []string{"skill(*)", "agent(*)"},
			skills: map[string]bool{"acme-notes": false}, agents: map[string]bool{"explore": false},
			constrains: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := mustParse(t, tc.allowed, tc.denied)
			for name, want := range tc.skills {
				assert.Equal(t, want, p.AllowsSkill(name), "skill %s", name)
			}
			for id, want := range tc.agents {
				assert.Equal(t, want, p.AllowsAgent(id), "agent %s", id)
			}
			assert.Equal(t, tc.constrains, p.ConstrainsAgents())
		})
	}
}

func TestWithoutSkill(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// neighbor is a skill and agent type that stays allowed.
		neighbor string
	}{
		{name: "plan", neighbor: "planner"},
		{name: "Plan", neighbor: "planner"},
		{name: "acme-*", neighbor: "acme-notes"},
		{name: "a?", neighbor: "ab"},
		{name: "[ab]", neighbor: "a"},
		{name: "a[", neighbor: "a"},
		{name: `a\b`, neighbor: "ab"},
		{name: "my skill", neighbor: "my"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base := mustParse(t, []string{"skill", "agent", "bash"}, nil)
			for _, p := range []Policy{{}, base} {
				got := p.WithoutSkill(tc.name)
				assert.False(t, got.AllowsSkill(tc.name), "the skill itself")
				assert.False(t, got.AllowsAgent(tc.name), "the agent type itself")
				assert.True(t, got.AllowsSkill(tc.neighbor), "another skill")
				assert.True(t, got.AllowsAgent(tc.neighbor), "another agent type")
				assert.True(t, got.ConstrainsAgents())
				assert.True(t, got.AllowsTool("skill"))
				assert.True(t, got.AllowsTool("agent"))
				assert.True(t, got.AllowsTool("bash"))
				assert.Empty(t, got.CommandHelp())
				assert.True(t, p.IsZero() || p.AllowsSkill(tc.name), "p is left alone")
			}
		})
	}
	assert.False(t, Policy{}.WithoutSkill("Plan").AllowsAgent("PLAN"), "agent types compare in any case")
	assert.True(t, Policy{}.WithoutSkill("Plan").AllowsSkill("plan"), "skill names compare exactly")
}

func TestIntersect(t *testing.T) {
	t.Parallel()

	parent := mustParse(t, []string{"read_file", "bash(git *)", "skill"}, nil)
	child := mustParse(t, []string{"read_file", "find_files", "bash(git log *)", "bash(jq *)"}, nil)
	p := Intersect(parent, Policy{}, child)

	assert.True(t, Intersect().IsZero())
	assert.True(t, Intersect(Policy{}, Policy{}).IsZero())
	assert.True(t, p.AllowsTool("read_file"))
	assert.False(t, p.AllowsTool("find_files"), "the parent does not allow it")
	assert.False(t, p.AllowsTool("skill"), "the child does not allow it")
	assert.True(t, p.AllowsTool("bash"))
	bash := func(cmd string) shellplan.Plan { return shellplan.Command("bash", []string{"-c", cmd}, nil, nil) }
	assert.NoError(t, p.CheckCommand(bash("git log -1")))
	assert.Error(t, p.CheckCommand(bash("git push")), "the child does not allow it")
	assert.Error(t, p.CheckCommand(bash("jq .")), "the parent does not allow it")
}

func TestCheckCommand(t *testing.T) {
	t.Parallel()

	const dir = "/skills/acme-notes"
	skills := map[string]string{"acme-notes": dir, "other": "/skills/other", "rel": "relative/rel"}
	cases := []struct {
		cmd     string
		allowed []string
		denied  []string
		wantErr string
	}{
		{cmd: "mycli a b", allowed: []string{"bash(mycli a *)"}},
		{cmd: "mycli a; rm x", allowed: []string{"bash(mycli a *)"}, wantErr: `"rm x" matches no allowed pattern`},
		{cmd: "mycli a | jq .", allowed: []string{"bash(mycli a *)"}, wantErr: `"jq ." matches no allowed pattern`},
		{cmd: "mycli a | jq .", allowed: []string{"bash(mycli a *)", "bash(jq *)"}},
		{cmd: "echo $(curl x)", allowed: []string{"bash(mycli *)"}, wantErr: `"curl x"`},
		{cmd: "bash -c 'rm x'", allowed: []string{"bash(mycli *)"}, wantErr: `"rm x"`},
		{cmd: "eval x", allowed: []string{"bash(mycli *)"}, wantErr: "eval runs code that cannot be followed"},
		{cmd: "eval x", denied: []string{"bash(rm *)"}, wantErr: "eval runs code that cannot be followed"},
		{cmd: "$CMD a", allowed: []string{"bash(mycli *)"}, wantErr: "$CMD is not known until it runs"},
		{cmd: "$CMD a", denied: []string{"bash(rm *)"}, wantErr: "$CMD is not known until it runs"},
		{cmd: "timeout 5 mycli a", allowed: []string{"bash(mycli a *)"}},
		{cmd: "sudo mycli a", allowed: []string{"bash(mycli a *)"}, wantErr: `"sudo mycli a"`},
		{cmd: "sudo mycli a", allowed: []string{"bash(sudo mycli *)"}},
		{cmd: "mycli a > f", allowed: []string{"bash(mycli a *)"}, wantErr: "redirects output to the file f"},
		{cmd: "mycli a > $F", allowed: []string{"bash(mycli a *)"}, wantErr: "redirects output to the file $F"},
		{cmd: "mycli a > f", allowed: []string{"bash", "bash(mycli a *)"}},
		{cmd: "mycli a > f", denied: []string{"bash(rm *)"}},
		{cmd: "mycli a 2>/dev/null >/dev/stderr 2>&1", allowed: []string{"bash(mycli a *)"}},
		{cmd: "git -C d commit -m x", allowed: []string{"bash(git commit *)"}},
		{cmd: "./mycli a", allowed: []string{"bash(mycli *)"}, wantErr: `"./mycli a"`},
		{cmd: "./mycli a", allowed: []string{"bash(./mycli *)"}},
		{cmd: "cd x && ./mycli a", allowed: []string{"bash(./mycli *)"}, wantErr: `"./mycli a"`},
		{cmd: "cd x && mycli a", allowed: []string{"bash(mycli a *)"}},
		{cmd: "echo hi; printf x; test -f a; pwd; read l; type ls", allowed: []string{"bash(mycli *)"}},
		{cmd: "kill 1", allowed: []string{"bash(mycli *)"}, wantErr: `"kill 1"`},
		{cmd: "kill 1", allowed: []string{"bash(kill *)"}},
		{cmd: dir + "/scripts/run x", allowed: []string{"bash(mycli *)", "bash($acme-notes)"}},
		{cmd: dir + "/scripts/run x", allowed: []string{"bash(mycli *)"}, wantErr: dir + "/scripts/run x"},
		{cmd: dir + "/scripts/sub/run", allowed: []string{"bash($acme-notes)"}},
		{cmd: dir + "/scripts/run | mycli a", allowed: []string{"bash($acme-notes)"}, wantErr: `"mycli a"`},
		{cmd: "echo hi", allowed: []string{"bash($acme-notes)"}},
		{cmd: dir + "/scripts/../x", allowed: []string{"bash($acme-notes)"},
			wantErr: dir + "/scripts/../x"},
		{cmd: dir + "/scripts-other/run", allowed: []string{"bash($acme-notes)"},
			wantErr: "scripts-other"},
		{cmd: "/skills/other/scripts/run", allowed: []string{"bash($acme-notes)"}, wantErr: "other"},
		{cmd: "/skills/other/scripts/run", allowed: []string{"bash($*)"}},
		{cmd: dir + "/scripts/run", allowed: []string{"bash($missing)"}, wantErr: dir + "/scripts/run"},
		{cmd: "relative/rel/scripts/run", allowed: []string{"bash($rel)"}, wantErr: "relative/rel/scripts/run"},
		{cmd: "mycli issue delete 1", allowed: []string{"bash(mycli issue *)"},
			denied:  []string{"bash(mycli issue delete *)"},
			wantErr: `"mycli issue delete 1" matches the denied pattern "mycli issue delete *"`},
		{cmd: "mycli issue list", allowed: []string{"bash(mycli issue *)"},
			denied: []string{"bash(mycli issue delete *)"}},
		{cmd: "sudo rm -rf x", denied: []string{"bash(rm *)"}, wantErr: "denied pattern"},
		{cmd: "rm x", allowed: []string{"bash"}, denied: []string{"bash(rm *)"}, wantErr: "denied pattern"},
		{cmd: "ls", denied: []string{"bash(bash *)"}},
		{cmd: "rm x", allowed: []string{"read_file"}},
		{cmd: "rm x"},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			t.Parallel()
			p := mustParse(t, tc.allowed, tc.denied).WithSkillDirs(skills)
			err := p.CheckCommand(shellplan.Command("bash", []string{"-c", tc.cmd}, nil, nil))
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}

	t.Run("the rejection lists the allowed patterns", func(t *testing.T) {
		t.Parallel()
		p := mustParse(t, []string{"bash(git log *)", "Bash(jq:*)"}, []string{"bash(git log -p *)"})
		err := p.CheckCommand(shellplan.Command("bash", []string{"-c", "git push"}, nil, nil))
		require.Error(t, err)
		assert.Equal(t, "command rejected: \"git push\" matches no allowed pattern. "+
			"Every program a command runs must match one of `git log *`, `jq *`. "+
			"Programs matching `git log -p *` may not run.", err.Error())

		denyOnly := mustParse(t, []string{"bash"}, []string{"bash(git push *)"})
		err = denyOnly.CheckCommand(shellplan.Command("bash", []string{"-c", "git push"}, nil, nil))
		require.Error(t, err)
		assert.Equal(t, "command rejected: \"git push\" matches the denied pattern \"git push *\". "+
			"Programs matching `git push *` may not run.", err.Error())
	})

	t.Run("a model-chosen interpreter is checked", func(t *testing.T) {
		t.Parallel()
		p := mustParse(t, []string{"bash(mycli *)"}, nil)
		assert.Error(t, p.CheckCommand(shellplan.Command("python3", []string{"-c", "print(1)"}, nil, nil)))
		denied := mustParse(t, nil, []string{"bash(python3 *)"})
		assert.Error(t, denied.CheckCommand(shellplan.Command("python3", []string{"-c", "print(1)"}, nil, nil)))
	})
}

func TestWithSkillDirs(t *testing.T) {
	t.Parallel()

	skills := map[string]string{
		"acme-notes": "/skills/acme-notes",
		"acme-other": "/skills/acme-other/",
		"Upper":      "/skills/Upper",
		"dots":       "/skills/x/../dots",
		"rel":        "relative/rel",
	}
	const notes = "/skills/acme-notes/scripts"
	cases := []struct {
		allowed []string
		cmd     string
		want    bool
	}{
		{allowed: []string{"bash($acme-notes)"}, cmd: notes + "/run", want: true},
		{allowed: []string{"bash($acme-notes)"}, cmd: notes + "/run --flag $X", want: true},
		{allowed: []string{"bash($acme-notes)"}, cmd: notes + "/sub/run", want: true},
		{allowed: []string{"bash($acme-notes)"}, cmd: notes + "/./run", want: true},
		{allowed: []string{"bash($acme-notes)"}, cmd: "exec " + notes + "/run", want: true},
		{allowed: []string{"bash($acme-notes)"}, cmd: "timeout 5 " + notes + "/run", want: true},
		{allowed: []string{"bash($acme-notes)"}, cmd: "cd /tmp && " + notes + "/run", want: true},
		{allowed: []string{"bash($acme-notes)"}, cmd: notes + "/run 2>/dev/null", want: true},
		{allowed: []string{"bash($acme-notes)"}, cmd: "echo hi", want: true},
		{allowed: []string{"bash($acme-notes)"}, cmd: notes, want: false},
		{allowed: []string{"bash($acme-notes)"}, cmd: "/skills/acme-notes/run", want: false},
		{allowed: []string{"bash($acme-notes)"}, cmd: notes + "/../run", want: false},
		{allowed: []string{"bash($acme-notes)"}, cmd: notes + "-x/run", want: false},
		{allowed: []string{"bash($acme-notes)"}, cmd: "/skills/acme-other/scripts/run", want: false},
		{allowed: []string{"bash($acme-notes)"}, cmd: "scripts/run", want: false},
		{allowed: []string{"bash($acme-notes)"}, cmd: "cd /skills/acme-notes && ./scripts/run", want: false},
		{allowed: []string{"bash($acme-notes)"}, cmd: "bash " + notes + "/run", want: false},
		{allowed: []string{"bash($acme-notes)"}, cmd: "python3 " + notes + "/x.py", want: false},
		{allowed: []string{"bash($acme-notes)"}, cmd: notes + "/run | jq .", want: false},
		{allowed: []string{"bash($acme-notes)"}, cmd: notes + "/run > out", want: false},
		{allowed: []string{"bash($acme-notes)"}, cmd: "git log", want: false},
		{allowed: []string{"bash($acme-notes)", "bash(jq *)"}, cmd: notes + "/run | jq .", want: true},
		{allowed: []string{"bash($acme-*)"}, cmd: "/skills/acme-other/scripts/run", want: true},
		{allowed: []string{"bash($acme-*)"}, cmd: "/skills/Upper/scripts/run", want: false},
		{allowed: []string{"bash($*)"}, cmd: "/skills/Upper/scripts/run", want: true},
		{allowed: []string{"bash($*)"}, cmd: "relative/rel/scripts/run", want: false},
		{allowed: []string{"bash($upper)"}, cmd: "/skills/Upper/scripts/run", want: false},
		{allowed: []string{"bash($dots)"}, cmd: "/skills/dots/scripts/run", want: true},
		{allowed: []string{"bash($rel)"}, cmd: "relative/rel/scripts/run", want: false},
		{allowed: []string{"bash($missing)"}, cmd: notes + "/run", want: false},
		{allowed: []string{"bash", "bash($acme-notes)"}, cmd: "git push > out", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			t.Parallel()
			p := mustParse(t, tc.allowed, nil).WithSkillDirs(skills)
			err := p.CheckCommand(shellplan.Command("bash", []string{"-c", tc.cmd}, nil, nil))
			if tc.want {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}

	t.Run("resolution", func(t *testing.T) {
		t.Parallel()
		plan := shellplan.Command("bash", []string{"-c", notes + "/run"}, nil, nil)
		unresolved := mustParse(t, []string{"bash(jq *)", "bash($acme-notes)"}, nil)
		child := unresolved.WithSkillDirs(skills)

		assert.NoError(t, child.CheckCommand(plan))
		assert.Error(t, unresolved.CheckCommand(plan), "unresolved entries allow nothing")
		assert.Equal(t, child, child.WithSkillDirs(skills), "resolving again changes nothing")
		assert.Error(t, child.WithSkillDirs(map[string]string{"acme-notes": "/elsewhere"}).CheckCommand(plan),
			"resolving again replaces the directories")
		assert.Error(t, child.WithSkillDirs(nil).CheckCommand(plan))
		assert.True(t, Policy{}.WithSkillDirs(skills).IsZero())
		plain := mustParse(t, []string{"bash(jq *)"}, nil)
		assert.Equal(t, plain, plain.WithSkillDirs(skills), "a policy without skill scripts")

		parent := mustParse(t, []string{"bash(git *)"}, nil).WithSkillDirs(skills)
		assert.Error(t, Intersect(parent, child).CheckCommand(plan), "a parent limits the scripts of its children")
		parent = mustParse(t, []string{"bash($acme-*)"}, nil).WithSkillDirs(skills)
		assert.NoError(t, Intersect(parent, child).CheckCommand(plan))
		assert.NoError(t, Intersect(parent, unresolved).WithSkillDirs(skills).CheckCommand(plan),
			"resolving an intersection resolves every layer")
	})
}

func TestCommandHelp(t *testing.T) {
	t.Parallel()

	assert.Empty(t, Policy{}.CommandHelp())
	assert.Empty(t, mustParse(t, []string{"bash", "read_file"}, nil).CommandHelp())
	assert.Empty(t, mustParse(t, []string{"bash", "bash(git *)"}, nil).CommandHelp())

	help := mustParse(t, []string{"bash(git log *)"}, []string{"bash(git log -p *)"}).
		CommandHelp()
	assert.Equal(t, "Commands are restricted: every program a command runs must match one of "+
		"`git log *`. Programs matching `git log -p *` may not run. Commands whose programs Rune cannot tell, "+
		"such as eval or a program name in a variable, are rejected. "+
		"Redirections may not write files, except /dev/null.", help)

	skills := map[string]string{"acme": "/skills/acme"}
	scripts := mustParse(t, []string{"bash(git log *)", "bash($acme)"}, nil).WithSkillDirs(skills)
	assert.Contains(t, scripts.CommandHelp(), "Commands are restricted: every program a command runs must "+
		"match one of `git log *` or be inside `/skills/acme/scripts`, given by its absolute path.")
	scriptsOnly := mustParse(t, []string{"bash($acme)"}, nil).WithSkillDirs(skills)
	assert.Contains(t, scriptsOnly.CommandHelp(), "Commands are restricted: every program a command runs must "+
		"be inside `/skills/acme/scripts`, given by its absolute path.")
	both := Intersect(mustParse(t, []string{"bash(git *)"}, nil), mustParse(t, []string{"bash(git log *)"}, nil))
	assert.Contains(t, both.CommandHelp(), "must match one of `git *`. Also, every program a command runs "+
		"must match one of `git log *`.")

	deny := mustParse(t, nil, []string{"bash(rm *)"}).CommandHelp()
	assert.Contains(t, deny, "Programs matching `rm *` may not run.")
	assert.NotContains(t, deny, "Redirections")
}
