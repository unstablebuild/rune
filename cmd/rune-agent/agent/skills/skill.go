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
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
	"unstable.build/rune/cmd/rune-agent/agent/toolpolicy"
)

// Skill holds a parsed SKILL.md.
type Skill struct {
	Name          string            // from frontmatter (required)
	Description   string            // from frontmatter (required)
	Body          string            // markdown below frontmatter
	Dir           string            // absolute path to skill directory
	License       string            // optional
	Compatibility string            // optional
	Metadata      map[string]string // optional
	// Tools restricts the tools of the sub-agent of an agent skill, as
	// its allowed-tools and denied-tools say. An allowed-tools field
	// without entries, even a null one, allows no tools. An agent skill
	// also may not use or spawn itself.
	Tools toolpolicy.Policy
	Type  string // "" for prompt-skill, "agent" for agent-skill
	Model string // optional model override for agent-type skills
	// ParentContext, when true, opts an agent skill into receiving the
	// parent dialogue's prior user/assistant messages as initial
	// context when the skill is invoked via a slash command. Default
	// false keeps the sub-agent isolated.
	ParentContext bool
}

type frontmatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license"`
	Compatibility string            `yaml:"compatibility"`
	Metadata      map[string]string `yaml:"metadata"`
	AllowedTools  yaml.Node         `yaml:"allowed-tools"`
	DeniedTools   toolpolicy.List   `yaml:"denied-tools"`
	Type          string            `yaml:"type"`
	Model         string            `yaml:"model"`
	ParentContext bool              `yaml:"parent-context"`
}

var nameRegexp = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Parse splits YAML frontmatter from body, validates fields, and returns a Skill.
// Validation is lenient: name format issues are logged as warnings but still load.
// Only missing name, missing description, unparseable YAML, an
// allowed-tools or denied-tools value that is not a string or a list of
// strings, or an entry in them that cannot be parsed cause errors.
func Parse(data []byte, dir string) (Skill, error) {
	fm, body, err := splitFrontmatter(data)
	if err != nil {
		return Skill{}, err
	}

	var meta frontmatter
	if err := yaml.Unmarshal(fm, &meta); err != nil {
		return Skill{}, fmt.Errorf("parse frontmatter: %w", err)
	}

	if meta.Name == "" {
		return Skill{}, fmt.Errorf("missing required field: name")
	}
	if meta.Description == "" {
		return Skill{}, fmt.Errorf("missing required field: description")
	}

	// Lenient validation: warn but load anyway.
	warnName(meta.Name, dir)

	// A present allowed-tools restricts the agent, so that a field left
	// empty fails closed.
	var allowed toolpolicy.List
	if !meta.AllowedTools.IsZero() {
		if err := meta.AllowedTools.Decode(&allowed); err != nil {
			return Skill{}, fmt.Errorf("parse frontmatter: allowed-tools: %w", err)
		}
		if allowed == nil {
			allowed = toolpolicy.List{}
		}
	}
	tools, err := toolpolicy.Parse(allowed, meta.DeniedTools)
	if err != nil {
		return Skill{}, err
	}
	if meta.Type == "agent" {
		tools = tools.WithoutSkill(meta.Name)
	}

	return Skill{
		Name:          meta.Name,
		Description:   meta.Description,
		Body:          body,
		Dir:           dir,
		License:       meta.License,
		Compatibility: meta.Compatibility,
		Metadata:      meta.Metadata,
		Tools:         tools,
		Type:          meta.Type,
		Model:         meta.Model,
		ParentContext: meta.ParentContext,
	}, nil
}

// warnName logs warnings for name issues without blocking loading.
func warnName(name, dir string) {
	log := slog.With("name", name, "dir", dir)
	if len(name) > 64 {
		log.Warn("skill name exceeds 64 characters")
	}
	if !nameRegexp.MatchString(name) {
		log.Warn("skill name does not match recommended format (lowercase alphanumeric with single hyphens)")
	}
	if strings.Contains(name, "--") {
		log.Warn("skill name contains consecutive hyphens")
	}
	if dirBase := filepath.Base(dir); dir != "" && dirBase != name {
		log.Warn("skill name does not match parent directory", "directory", dirBase)
	}
}

func splitFrontmatter(data []byte) (fm []byte, body string, err error) {
	s := string(data)
	if !strings.HasPrefix(s, "---") {
		return nil, "", fmt.Errorf("missing frontmatter delimiters")
	}
	rest := s[3:]
	fmStr, after, ok := strings.Cut(rest, "\n---")
	if !ok {
		return nil, "", fmt.Errorf("missing frontmatter delimiters")
	}
	// Strip leading newline from body
	after = strings.TrimPrefix(after, "\n")
	return []byte(fmStr), after, nil
}
