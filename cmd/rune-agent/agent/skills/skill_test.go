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
)

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
				AllowedTools:  "Bash(git:*) Read",
			},
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
				Name:         "explore",
				Description:  "Explore codebase",
				Body:         "You are a researcher.",
				Dir:          "/skills/explore",
				AllowedTools: "read_file search_content",
				Type:         "agent",
				Model:        "gpt-4o",
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
				Name:         "plan",
				Description:  "Plan agent",
				Body:         "You are a planner.",
				Dir:          "/skills/plan",
				AllowedTools: "read_file search_content ask_user_question",
				Type:         "agent",
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

	tools := strings.Fields(skill.AllowedTools)
	toolSet := make(map[string]bool, len(tools))
	for _, name := range tools {
		toolSet[name] = true
	}

	// Must include ask_user_question and exit_plan_mode.
	assert.True(t, toolSet["ask_user_question"], "plan skill must include ask_user_question")
	assert.True(t, toolSet["exit_plan_mode"], "plan skill must include exit_plan_mode")

	// Must include core read-only tools plus semantic tools.
	for _, name := range []string{
		"read_file", "search_content", "find_files",
		"find_definition", "find_implementations", "outline_file",
		"search_symbols", "describe_symbol", "check_file_errors",
		"compact",
	} {
		assert.True(t, toolSet[name], "plan skill must include %s", name)
	}

	// Must NOT include write tools.
	for _, name := range []string{"apply_patch"} {
		assert.False(t, toolSet[name], "plan skill must not include %s", name)
	}
}

func TestPlanSkillParentContext(t *testing.T) {
	skillDir := filepath.Join(repoSkillsDir(t), "plan")
	data, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	require.NoError(t, err)

	skill, err := Parse(data, skillDir)
	require.NoError(t, err)
	assert.True(t, skill.ParentContext)
}
