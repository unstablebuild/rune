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
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// loadDir scans dir for subdirectories containing SKILL.md using the
// registry's FileSystem. Returns all successfully parsed skills and
// any parsing/validation errors encountered.
func (r *SkillRegistry) loadDir(dir string) ([]Skill, []SkillError) {
	entries, err := r.fs.ReadDir(dir)
	if err != nil {
		slog.Debug("skills: cannot read directory", "dir", dir, "error", err)
		return nil, nil
	}

	var result []Skill
	var errs []SkillError
	for _, e := range entries {
		if !e.IsDir() {
			if e.Type()&os.ModeSymlink == 0 {
				continue
			}
			info, err := r.fs.Stat(filepath.Join(dir, e.Name()))
			if err != nil || !info.IsDir() {
				continue
			}
		}
		skillPath := filepath.Join(dir, e.Name(), "SKILL.md")
		data, err := r.readFile(skillPath)
		if err != nil {
			continue // no SKILL.md in this subdirectory
		}
		skillDir := filepath.Join(dir, e.Name())
		skill, err := Parse(data, skillDir)
		if err != nil {
			slog.Warn("skills: failed to parse",
				"path", skillPath, "error", err)
			errs = append(errs, SkillError{Path: skillPath, Err: err})
			continue
		}
		result = append(result, skill)
	}
	return result, errs
}

// readFile reads the entire contents of path via r.fs.
func (r *SkillRegistry) readFile(path string) ([]byte, error) {
	f, err := r.fs.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}
