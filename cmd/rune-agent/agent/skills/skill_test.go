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

package skills

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/cmd/rune-agent/agent/toolpolicy"
)

func mustPolicy(t *testing.T, allowed, denied []string) toolpolicy.Policy {
	t.Helper()
	p, err := toolpolicy.Parse(allowed, denied)
	require.NoError(t, err)
	return p
}

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		dir     string
		want    Skill
		wantErr string
	}{
		{
			name: "valid skill with body",
			input: `---
name: debug
description: Debug container agent issues
---
## Steps

1. Check logs
2. Inspect state`,
			dir: "/skills/debug",
			want: Skill{
				Name:        "debug",
				Description: "Debug container agent issues",
				Body:        "## Steps\n\n1. Check logs\n2. Inspect state",
				Dir:         "/skills/debug",
			},
		},
		{
			name: "empty body is valid",
			input: `---
name: simple
description: A simple skill
---
`,
			dir: "/skills/simple",
			want: Skill{
				Name:        "simple",
				Description: "A simple skill",
				Body:        "",
				Dir:         "/skills/simple",
			},
		},
		{
			name:    "missing frontmatter delimiters",
			input:   "no frontmatter here",
			wantErr: "missing frontmatter delimiters",
		},
		{
			name: "missing closing delimiter",
			input: `---
name: broken
description: No closing
`,
			wantErr: "missing frontmatter delimiters",
		},
		{
			name: "missing name field",
			input: `---
description: Has description but no name
---
body`,
			wantErr: "missing required field: name",
		},
		{
			name: "missing description field",
			input: `---
name: no-desc
---
body`,
			wantErr: "missing required field: description",
		},
		{
			name: "uppercase in name loads with warning",
			input: `---
name: MySkill
description: Bad name
---
body`,
			dir: "/skills/MySkill",
			want: Skill{
				Name: "MySkill", Description: "Bad name",
				Body: "body", Dir: "/skills/MySkill",
			},
		},
		{
			name: "consecutive hyphens loads with warning",
			input: `---
name: my--skill
description: Bad name
---
body`,
			dir: "/skills/my--skill",
			want: Skill{
				Name: "my--skill", Description: "Bad name",
				Body: "body", Dir: "/skills/my--skill",
			},
		},
		{
			name: "leading hyphen loads with warning",
			input: `---
name: -leading
description: Bad name
---
body`,
			dir: "/skills/-leading",
			want: Skill{
				Name: "-leading", Description: "Bad name",
				Body: "body", Dir: "/skills/-leading",
			},
		},
		{
			name: "trailing hyphen loads with warning",
			input: `---
name: trailing-
description: Bad name
---
body`,
			dir: "/skills/trailing-",
			want: Skill{
				Name: "trailing-", Description: "Bad name",
				Body: "body", Dir: "/skills/trailing-",
			},
		},
		{
			name:  "name exceeds 64 chars loads with warning",
			input: "---\nname: " + strings.Repeat("a", 65) + "\ndescription: Too long name\n---\nbody",
			dir:   "/skills/long",
			want: Skill{
				Name: strings.Repeat("a", 65), Description: "Too long name",
				Body: "body", Dir: "/skills/long",
			},
		},
		{
			name:  "long description loads with warning",
			input: "---\nname: long-desc\ndescription: " + strings.Repeat("x", 1025) + "\n---\nbody",
			dir:   "/skills/long-desc",
			want: Skill{
				Name: "long-desc", Description: strings.Repeat("x", 1025),
				Body: "body", Dir: "/skills/long-desc",
			},
		},
		{
			name: "optional frontmatter fields parsed",
			input: `---
name: extra
description: Has extra fields
license: MIT
compatibility: Requires git and docker
metadata:
  author: test-org
  version: "1.0"
allowed-tools: Bash(git:*) Read
---
body content`,
			dir: "/skills/extra",
			want: Skill{
				Name:          "extra",
				Description:   "Has extra fields",
				Body:          "body content",
				Dir:           "/skills/extra",
				License:       "MIT",
				Compatibility: "Requires git and docker",
				Metadata:      map[string]string{"author": "test-org", "version": "1.0"},
			},
		},
		{
			name: "allowed-tools string split on commas outside parentheses",
			input: `---
name: list
description: String list
type: agent
allowed-tools: read_file, bash(git commit -m 'a, b'), skill(acme-*), bash($acme-*)
---
body`,
			dir: "/skills/list",
			want: Skill{
				Name: "list", Description: "String list", Body: "body", Dir: "/skills/list",
				Tools: mustPolicy(t,
					[]string{"read_file", "bash(git commit -m 'a, b')", "skill(acme-*)", "bash($acme-*)"},
					nil).WithoutSkill("list"),
				Type: "agent",
			},
		},
		{
			name: "allowed-tools and disallowed-tools as YAML lists",
			input: `---
name: acme-triage
description: Triage issues
type: agent
allowed-tools: [read_file, "skill(acme-*)", "bash(acme issue *)", "bash($acme-notes)"]
disallowed-tools:
  - bash(acme issue delete *)
---
body`,
			dir: "/skills/acme-triage",
			want: Skill{
				Name: "acme-triage", Description: "Triage issues", Body: "body",
				Dir: "/skills/acme-triage",
				Tools: mustPolicy(t,
					[]string{"read_file", "skill(acme-*)", "bash(acme issue *)", "bash($acme-notes)"},
					[]string{"bash(acme issue delete *)"}).WithoutSkill("acme-triage"),
				Type: "agent",
			},
		},
		{
			name: "disallowed-tools without allowed-tools",
			input: `---
name: no-shell
description: No shell
type: agent
disallowed-tools: bash
---
body`,
			dir: "/skills/no-shell",
			want: Skill{
				Name: "no-shell", Description: "No shell", Body: "body", Dir: "/skills/no-shell",
				Tools: mustPolicy(t, nil, []string{"bash"}).WithoutSkill("no-shell"),
				Type:  "agent",
			},
		},
		{
			name: "an invalid allowed-tools entry is an error",
			input: `---
name: broken
description: Broken
type: agent
allowed-tools: read_file(x)
---
body`,
			dir:     "/skills/broken",
			wantErr: `"read_file(x)"`,
		},
		{
			name: "an invalid disallowed-tools entry is an error",
			input: `---
name: broken
description: Broken
type: agent
disallowed-tools: [bash($X)]
---
body`,
			dir:     "/skills/broken",
			wantErr: `"bash($X)"`,
		},
		{
			name: "name with numbers and hyphens",
			input: `---
name: test-repo-v2
description: A valid name with numbers
---
content`,
			dir: "/skills/test-repo-v2",
			want: Skill{
				Name:        "test-repo-v2",
				Description: "A valid name with numbers",
				Body:        "content",
				Dir:         "/skills/test-repo-v2",
			},
		},
		{
			name: "agent type and model fields parsed",
			input: `---
name: explore
description: Explore codebase
type: agent
model: gpt-4o
allowed-tools: read_file search_content
---
You are a researcher.`,
			dir: "/skills/explore",
			want: Skill{
				Name:        "explore",
				Description: "Explore codebase",
				Body:        "You are a researcher.",
				Dir:         "/skills/explore",
				Tools:       mustPolicy(t, []string{"read_file", "search_content"}, nil).WithoutSkill("explore"),
				Type:        "agent",
				Model:       "gpt-4o",
			},
		},
		{
			name: "plan agent skill parsed",
			input: `---
name: plan
description: Plan agent
type: agent
allowed-tools: read_file search_content ask_user_question
---
You are a planner.`,
			dir: "/skills/plan",
			want: Skill{
				Name:        "plan",
				Description: "Plan agent",
				Body:        "You are a planner.",
				Dir:         "/skills/plan",
				Tools: mustPolicy(t, []string{"read_file", "search_content", "ask_user_question"},
					nil).WithoutSkill("plan"),
				Type: "agent",
			},
		},
		{
			name: "empty type defaults to prompt skill",
			input: `---
name: prompt-skill
description: A prompt skill
---
body`,
			dir: "/skills/prompt-skill",
			want: Skill{
				Name:        "prompt-skill",
				Description: "A prompt skill",
				Body:        "body",
				Dir:         "/skills/prompt-skill",
			},
		},
		{
			name: "parent-context field parsed",
			input: `---
name: plan
description: Plan agent
type: agent
parent-context: true
---
You are a planner.`,
			dir: "/skills/plan",
			want: Skill{
				Name:          "plan",
				Description:   "Plan agent",
				Body:          "You are a planner.",
				Dir:           "/skills/plan",
				Tools:         toolpolicy.Policy{}.WithoutSkill("plan"),
				Type:          "agent",
				ParentContext: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.input), tt.dir)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseTools(t *testing.T) {
	t.Parallel()

	empty := mustPolicy(t, []string{}, nil)
	cases := []struct {
		name string
		// frontmatter follows the name, description and type of an agent
		// skill named x, whose policy is want without x itself.
		frontmatter string
		want        toolpolicy.Policy
		wantErr     string
	}{
		{name: "no tool fields", want: toolpolicy.Policy{}},

		// allowed-tools present with no entries allows nothing.
		{name: "null allowed-tools", frontmatter: "allowed-tools:\n", want: empty},
		{name: "tilde allowed-tools", frontmatter: "allowed-tools: ~\n", want: empty},
		{name: "empty string", frontmatter: "allowed-tools: \"\"\n", want: empty},
		{name: "separators only", frontmatter: "allowed-tools: ' , '\n", want: empty},
		{name: "empty list", frontmatter: "allowed-tools: []\n", want: empty},
		{name: "blank entry", frontmatter: "allowed-tools: [\"\"]\n", want: empty},

		{name: "plain string",
			frontmatter: "allowed-tools: read_file bash(git log *)\n",
			want:        mustPolicy(t, []string{"read_file", "bash(git log *)"}, nil)},
		{name: "commas",
			frontmatter: "allowed-tools: read_file, bash(git log *),find_files\n",
			want:        mustPolicy(t, []string{"read_file", "bash(git log *)", "find_files"}, nil)},
		{name: "double quoted",
			frontmatter: "allowed-tools: \"read_file bash(git commit -m 'a, b')\"\n",
			want:        mustPolicy(t, []string{"read_file", "bash(git commit -m 'a, b')"}, nil)},
		{name: "single quoted",
			frontmatter: "allowed-tools: 'bash(echo \")\") read_file'\n",
			want:        mustPolicy(t, []string{`bash(echo ")")`, "read_file"}, nil)},
		{name: "folded",
			frontmatter: "allowed-tools: >-\n  read_file\n  bash(git log *)\n",
			want:        mustPolicy(t, []string{"read_file", "bash(git log *)"}, nil)},
		{name: "literal",
			frontmatter: "allowed-tools: |\n  read_file\n  bash(git log *)\n",
			want:        mustPolicy(t, []string{"read_file", "bash(git log *)"}, nil)},
		{name: "flow list",
			frontmatter: "allowed-tools: [read_file, \"bash(git log *)\", 'skill(acme-*)', \"bash($acme-notes)\"]\n",
			want: mustPolicy(t,
				[]string{"read_file", "bash(git log *)", "skill(acme-*)", "bash($acme-notes)"}, nil)},
		{name: "block list",
			frontmatter: "allowed-tools:\n  - read_file\n  - bash(git log *)\n  - Bash(git add:*)\n",
			want:        mustPolicy(t, []string{"read_file", "bash(git log *)", "Bash(git add:*)"}, nil)},
		{name: "the Claude Code form",
			frontmatter: "allowed-tools: Bash(git:*) Read\n",
			want:        mustPolicy(t, []string{"Bash(git:*)", "Read"}, nil)},
		{name: "a tagged string", frontmatter: "allowed-tools: !!str read_file\n",
			want: mustPolicy(t, []string{"read_file"}, nil)},
		{name: "disallowed-tools only",
			frontmatter: "disallowed-tools: bash, github_*\n",
			want:        mustPolicy(t, nil, []string{"bash", "github_*"})},
		{name: "null disallowed-tools", frontmatter: "disallowed-tools:\n", want: toolpolicy.Policy{}},
		{name: "empty disallowed-tools", frontmatter: "disallowed-tools: []\n", want: toolpolicy.Policy{}},
		{name: "both",
			frontmatter: "allowed-tools: [\"bash(git *)\"]\ndisallowed-tools: [\"bash(git push *)\"]\n",
			want:        mustPolicy(t, []string{"bash(git *)"}, []string{"bash(git push *)"})},

		{name: "a mapping", frontmatter: "allowed-tools: {a: b}\n", wantErr: "allowed-tools"},
		{name: "a nested list", frontmatter: "allowed-tools: [[a]]\n", wantErr: "allowed-tools"},
		{name: "a disallowed mapping", frontmatter: "disallowed-tools: {a: b}\n", wantErr: "disallowed-tools"},
		{name: "an alias", frontmatter: "allowed-tools: [*]\n", wantErr: "parse frontmatter"},
		{name: "a colon and space in a block item",
			frontmatter: "allowed-tools:\n  - Bash(git add: *)\n", wantErr: "parse frontmatter"},
		{name: "an unquoted comma in a flow list",
			frontmatter: "allowed-tools: [bash(git commit -m 'a, b')]\n", wantErr: `"bash(git commit -m 'a"`},
		{name: "a repeated field",
			frontmatter: "allowed-tools: read_file\nallowed-tools: bash\n", wantErr: "already defined"},
		{name: "an invalid allowed entry", frontmatter: "allowed-tools: read_file(x)\n",
			wantErr: `"read_file(x)"`},
		{name: "an invalid disallowed entry", frontmatter: "disallowed-tools: [bash($X)]\n",
			wantErr: "the scripts of a skill can only be allowed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := "---\nname: x\ndescription: d\ntype: agent\n" + tc.frontmatter + "---\nbody"
			got, err := Parse([]byte(input), "/skills/x")
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want.WithoutSkill("x"), got.Tools)
		})
	}
}

func TestParsePromptSkillIgnoresTools(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		frontmatter string
	}{
		{"Rune entries", "allowed-tools: read_file bash(git log *)\ndisallowed-tools: bash(git push *)\n"},
		{"Claude Code tools", "allowed-tools: Bash(git:*) Read Grep\n"},
		{"Claude Code patterns on other tools", "allowed-tools: Read(./.env) WebFetch(domain:example.com)\n"},
		{"a Claude Code variable", "allowed-tools: Bash(${CLAUDE_SKILL_DIR}/scripts/render.sh *)\n"},
		{"Claude Code wildcards", "allowed-tools: Bash(*) Bash(ls*) Bash(* --version)\n"},
		{"Claude Code skill and agent lists", "allowed-tools: Skill(review-pr *), Agent(worker, researcher)\n"},
		{"a Claude Code deny list", "disallowed-tools: AskUserQuestion Task(Explore)\n"},
		{"a mapping", "allowed-tools: {a: b}\ndisallowed-tools: {a: b}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := "---\nname: x\ndescription: d\n" + tc.frontmatter + "---\nbody"
			got, err := Parse([]byte(input), "/skills/x")
			require.NoError(t, err)
			assert.True(t, got.Tools.IsZero(), "only agent skills have tool rules")
		})
	}
}

func TestParseAgentSkillNames(t *testing.T) {
	t.Parallel()

	// Names are loaded leniently, so the self-restriction must hold for
	// names that are not valid patterns, and not hide other skills.
	cases := []struct {
		name     string
		neighbor string
	}{
		{name: "Plan", neighbor: "planner"},
		{name: "acme-*", neighbor: "acme-notes"},
		{name: "a[", neighbor: "a"},
		{name: "my skill", neighbor: "my"},
		{name: "a?", neighbor: "ab"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := "---\nname: \"" + tc.name + "\"\ndescription: d\ntype: agent\n---\nbody"
			got, err := Parse([]byte(input), "/skills/x")
			require.NoError(t, err)
			assert.False(t, got.Tools.AllowsSkill(tc.name))
			assert.False(t, got.Tools.AllowsAgent(tc.name))
			assert.True(t, got.Tools.AllowsSkill(tc.neighbor))
			assert.True(t, got.Tools.AllowsAgent(tc.neighbor))
		})
	}
}

// repoSkillsDir returns the absolute path to the repo's skills/ directory.
func repoSkillsDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "skills")
}

func TestPlanSkillFile(t *testing.T) {
	skillDir := filepath.Join(repoSkillsDir(t), "plan")
	data, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	require.NoError(t, err, "skills/plan/SKILL.md must exist")

	skill, err := Parse(data, skillDir)
	require.NoError(t, err)

	assert.Equal(t, "plan", skill.Name)
	assert.Equal(t, "agent", skill.Type)

	// Must include ask_user_question and exit_plan_mode.
	assert.True(t, skill.Tools.NamesTool("ask_user_question"), "plan skill must include ask_user_question")
	assert.True(t, skill.Tools.NamesTool("exit_plan_mode"), "plan skill must include exit_plan_mode")

	// Must include core read-only tools plus semantic tools.
	for _, name := range []string{
		"read_file", "search_content", "find_files",
		"find_definition", "find_implementations", "outline_file",
		"search_symbols", "describe_symbol", "check_file_errors",
		"compact",
	} {
		assert.True(t, skill.Tools.AllowsTool(name), "plan skill must include %s", name)
	}

	// Must NOT include write tools.
	for _, name := range []string{"apply_patch", "bash"} {
		assert.False(t, skill.Tools.AllowsTool(name), "plan skill must not include %s", name)
	}
	assert.False(t, skill.Tools.AllowsSkill("plan"), "plan skill must not invoke itself")
}

func TestPlanSkillParentContext(t *testing.T) {
	skillDir := filepath.Join(repoSkillsDir(t), "plan")
	data, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	require.NoError(t, err)

	skill, err := Parse(data, skillDir)
	require.NoError(t, err)
	assert.True(t, skill.ParentContext)
}
