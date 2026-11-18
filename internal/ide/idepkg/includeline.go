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

package idepkg

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
	"unstable.build/rune/internal/ide/hostenv"
)

// includeKey is the top-level key of a YAML config that lists the files it
// includes, and the name of the Starlark function that includes one.
const includeKey = "include"

// includeSpelling returns how the user config names the etc config of
// pkgID with extension ext: with $RUNE_DATADIR literal, so the same config
// serves every host.
func includeSpelling(pkgID, ext string) string {
	return "$" + hostenv.DataDirVar + "/etc/" + pkgID + ext
}

// includeSpellings returns the spellings of path a config may hold: as it
// is, and with $RUNE_DATADIR expanded with dataDir.
func includeSpellings(path, dataDir string) []string {
	expanded := hostenv.ExpandDataDir(path, dataDir)
	if expanded == path {
		return []string{path}
	}
	return []string{path, expanded}
}

// namesInclude reports whether src names path anywhere, including in a
// comment: a user who commented an include out has asked for it to stay
// off.
func namesInclude(src []byte, path, dataDir string) bool {
	for _, s := range includeSpellings(path, dataDir) {
		if bytes.Contains(src, []byte(s)) {
			return true
		}
	}
	return false
}

// addInclude returns src, the user config at configPath, with an include
// of path added, or src as it is when it already names path. A YAML config
// gets path appended to its top-level include list, or an include list
// before its first key when it has none; a Starlark one gets an
// include(path) call after its leading comments.
func addInclude(src []byte, configPath, path, dataDir string) ([]byte, error) {
	if namesInclude(src, path, dataDir) {
		return src, nil
	}
	if isStarlarkConfigPath(configPath) {
		return addStarlarkInclude(src, path), nil
	}
	return addYAMLInclude(src, path)
}

// removeInclude returns src, the user config at configPath, without the
// include of path, in either spelling of it. A commented-out include stays.
func removeInclude(src []byte, configPath, path, dataDir string) []byte {
	for _, s := range includeSpellings(path, dataDir) {
		if isStarlarkConfigPath(configPath) {
			src = removeStarlarkInclude(src, s)
		} else {
			src = removeYAMLInclude(src, s)
		}
	}
	return src
}

func isStarlarkConfigPath(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".star")
}

func addStarlarkInclude(src []byte, path string) []byte {
	line := fmt.Sprintf("%s(%q)\n", includeKey, path)
	at := afterLeadingComments(src)
	if at == len(src) {
		if len(src) > 0 && src[len(src)-1] != '\n' {
			line = "\n" + line
		}
		return append(cloneBytes(src), line...)
	}
	return insertAt(src, at, line)
}

// afterLeadingComments returns the offset of the first line of src that is
// neither blank nor a comment, or len(src) when there is none.
func afterLeadingComments(src []byte) int {
	off := 0
	for off < len(src) {
		end := bytes.IndexByte(src[off:], '\n')
		var line []byte
		if end < 0 {
			line = src[off:]
			end = len(src)
		} else {
			line = src[off : off+end]
			end = off + end + 1
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 && trimmed[0] != '#' {
			return off
		}
		off = end
	}
	return len(src)
}

func removeStarlarkInclude(src []byte, path string) []byte {
	return removeLines(src, func(trimmed string) bool {
		return trimmed == fmt.Sprintf("%s(%q)", includeKey, path) ||
			trimmed == fmt.Sprintf("%s('%s')", includeKey, path)
	})
}

func addYAMLInclude(src []byte, path string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	block := includeKey + ":\n  - " + path + "\n"
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		// Empty, or comments only.
		if len(src) > 0 && src[len(src)-1] != '\n' {
			block = "\n" + block
		}
		return append(cloneBytes(src), block...), nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config is not a mapping")
	}
	idx := findMappingKey(root, includeKey)
	if idx < 0 {
		if root.Style&yaml.FlowStyle != 0 {
			if len(root.Content) > 0 {
				return nil, fmt.Errorf("config is a flow mapping")
			}
			// `{}`: the block replaces it.
			start := offsetOf(src, root.Line, root.Column)
			end := start + bytes.IndexByte(src[start:], '}') + 1
			return replaceAt(src, start, end, strings.TrimSuffix(block, "\n")), nil
		}
		return insertAt(src, offsetOf(src, root.Content[0].Line, 1), block), nil
	}
	key, val := root.Content[idx], root.Content[idx+1]
	switch {
	case val.Kind == yaml.SequenceNode && val.Style&yaml.FlowStyle == 0:
		last := val.Content[len(val.Content)-1]
		indent := strings.Repeat(" ", val.Column-1)
		return insertAt(src, offsetOf(src, last.Line+1, 1), indent+"- "+path+"\n"), nil
	case val.Kind == yaml.ScalarNode && val.Tag == "!!null" && val.Value == "":
		// `include:` with nothing after it.
		return insertAt(src, offsetOf(src, key.Line+1, 1), "  - "+path+"\n"), nil
	case val.Kind == yaml.SequenceNode:
		return addFlowItem(src, val, path)
	default:
		return nil, fmt.Errorf("%s is not a list", includeKey)
	}
}

// addFlowItem appends path to the flow sequence seq, `[...]`, in src.
func addFlowItem(src []byte, seq *yaml.Node, path string) ([]byte, error) {
	open := offsetOf(src, seq.Line, seq.Column)
	if open >= len(src) || src[open] != '[' {
		return nil, fmt.Errorf("%s list not found at line %d", includeKey, seq.Line)
	}
	closing := flowSequenceEnd(src, open)
	if closing < 0 {
		return nil, fmt.Errorf("%s list at line %d is not closed", includeKey, seq.Line)
	}
	inner := bytes.TrimSpace(src[open+1 : closing])
	item := path
	switch {
	case len(inner) == 0:
	case inner[len(inner)-1] == ',':
		item = " " + path
	default:
		item = ", " + path
	}
	return insertAt(src, closing, item), nil
}

// flowSequenceEnd returns the offset of the `]` closing the flow sequence
// opening at open, or -1. Quoted strings and comments are skipped.
func flowSequenceEnd(src []byte, open int) int {
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth == 0 {
				return i
			}
		case '\'', '"':
			quote := src[i]
			for i++; i < len(src) && src[i] != quote; i++ {
				if quote == '"' && src[i] == '\\' {
					i++
				}
			}
		case '#':
			if i == 0 || src[i-1] == ' ' || src[i-1] == '\t' || src[i-1] == '\n' {
				for i < len(src) && src[i] != '\n' {
					i++
				}
			}
		}
	}
	return -1
}

func removeYAMLInclude(src []byte, path string) []byte {
	src = removeLines(src, func(trimmed string) bool {
		return trimmed == "- "+path || trimmed == `- "`+path+`"` || trimmed == "- '"+path+"'"
	})
	quoted := regexp.QuoteMeta(path)
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`,\s*` + quoted + `(\s*[,\]])`),
		regexp.MustCompile(`(\[\s*)` + quoted + `\s*,\s*`),
		regexp.MustCompile(`(\[\s*)` + quoted + `(\s*\])`),
	} {
		src = re.ReplaceAll(src, []byte("$1$2"))
	}
	return src
}

// removeLines returns src without the lines whose trimmed text drop
// reports.
func removeLines(src []byte, drop func(trimmed string) bool) []byte {
	var out []byte
	for off := 0; off < len(src); {
		end := bytes.IndexByte(src[off:], '\n')
		if end < 0 {
			end = len(src)
		} else {
			end = off + end + 1
		}
		line := src[off:end]
		if !drop(strings.TrimSpace(string(line))) {
			out = append(out, line...)
		}
		off = end
	}
	return out
}

// offsetOf returns the byte offset of the 1-based line and column in src,
// or len(src) when src has no such line.
func offsetOf(src []byte, line, column int) int {
	off := 0
	for l := 1; l < line; l++ {
		next := bytes.IndexByte(src[off:], '\n')
		if next < 0 {
			return len(src)
		}
		off += next + 1
	}
	return min(off+column-1, len(src))
}

func insertAt(src []byte, at int, text string) []byte {
	return replaceAt(src, at, at, text)
}

func replaceAt(src []byte, start, end int, text string) []byte {
	out := make([]byte, 0, len(src)+len(text))
	out = append(out, src[:start]...)
	out = append(out, text...)
	return append(out, src[end:]...)
}

func cloneBytes(src []byte) []byte {
	return append([]byte(nil), src...)
}

// findMappingKey returns the index of the key node in a mapping's Content
// slice, or -1 if not found.
func findMappingKey(mapping *yaml.Node, key string) int {
	for i := 0; i < len(mapping.Content)-1; i += 2 {
		if mapping.Content[i].Value == key {
			return i
		}
	}
	return -1
}
