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

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// configVersionKey marks a user config as owning its full key-binding
// list. Its absence identifies a config written before the editor
// presets took ownership, which is what the migration keys off.
const configVersionKey = "config_version"

const currentConfigVersion = 2

// editorExo mirrors the ide package's exo mode. An exo config still needs
// host bindings, so it is migrated using its fallback editor's preset.
const editorExo = "exo"

var (
	commandBlockRe     = regexp.MustCompile(`^command:\s*(#.*)?$`)
	keyBindingsBlockRe = regexp.MustCompile(`^\s*key_bindings:\s*(#.*)?$`)
	bindingEntryRe     = regexp.MustCompile(`^\s*(?:"([^"]+)"|([^\s:#][^:]*?))\s*:\s*(.*)$`)
)

// errUnsupportedConfigShape reports a config the splicer will not edit
// because it cannot guarantee a faithful, comment-preserving result.
var errUnsupportedConfigShape = errors.New("unsupported config shape")

// migrateBootstrappedConfig upgrades an existing user config in place.
// Failures are logged rather than fatal: a config this migration cannot
// safely rewrite must not stop Rune from starting.
func migrateBootstrappedConfig(configPath string) {
	if isStarlarkConfigPath(configPath) {
		log.Warnf("%s predates preset-owned key bindings and cannot be "+
			"migrated automatically; copy the bindings you want from the "+
			"shipped preset into your config", configPath)
		return
	}
	if err := migrateConfigKeyBindings(configPath); err != nil {
		log.Errorf("migrate key bindings in %s: %v", configPath, err)
	}
}

func isStarlarkConfigPath(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".star")
}

// migrateConfigKeyBindings brings a pre-preset user config up to date.
//
// Such a config only carried the deltas it wanted on top of rune.star's
// defaults, so now that rune.star ships no bindings it would come up with
// almost nothing bound. The shipped preset for the editor the user chose
// is applied over it, which also picks up any binding the preset has
// moved since. Entries the preset does not define are kept so custom
// bindings survive.
func migrateConfigKeyBindings(configPath string) error {
	src, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	cfg := map[string]any{}
	if err := yaml.Unmarshal(src, &cfg); err != nil {
		return fmt.Errorf("parse %s: %w", configPath, err)
	}
	if _, ok := cfg[configVersionKey]; ok {
		return nil
	}

	preset, err := renderPreset(presetEditorFor(cfg), true)
	if err != nil {
		return err
	}
	entries, keys, err := presetKeyBindings(preset)
	if err != nil {
		return err
	}

	out, err := applyPresetKeyBindings(src, entries, keys)
	if err != nil {
		return err
	}
	return writeFileAtomic(configPath, out)
}

// presetEditorFor resolves which preset owns a config's bindings.
// Without an explicit mode Rune runs modal, so that is the fallback.
func presetEditorFor(cfg map[string]any) string {
	editor, _ := cfg["editor"].(map[string]any)
	mode, _ := editor["mode"].(string)
	if mode == editorExo {
		exo, _ := editor["exo"].(map[string]any)
		mode, _ = exo["fallback"].(string)
	}
	switch mode {
	case editorStandard, editorModeless:
		return editorStandard
	case editorEmacs:
		return editorEmacs
	}
	return editorModal
}

// presetKeyBindings lifts a preset's key_bindings block verbatim so its
// ordering and explanatory comments carry over to the migrated config.
func presetKeyBindings(preset string) (entries []string, keys map[string]bool, err error) {
	lines := strings.Split(preset, "\n")
	cmdIdx := -1
	for i, l := range lines {
		if commandBlockRe.MatchString(l) {
			cmdIdx = i
			break
		}
	}
	if cmdIdx < 0 {
		return nil, nil, fmt.Errorf("%w: preset has no command block",
			errUnsupportedConfigShape)
	}
	_, kbIdx, kbIndent := scanCommandBlock(lines, cmdIdx)
	if kbIdx < 0 {
		return nil, nil, fmt.Errorf("%w: preset has no key_bindings block",
			errUnsupportedConfigShape)
	}

	keys = map[string]bool{}
	body := blockBody(lines, kbIdx, kbIndent)
	indent := -1
	for _, l := range body {
		if isBlankOrComment(l) {
			continue
		}
		if indent < 0 {
			indent = indentOf(l)
		}
		if k, _, ok := parseBindingEntry(l); ok {
			keys[k] = true
		}
	}
	if indent < 0 {
		return nil, nil, fmt.Errorf("%w: preset has no bindings",
			errUnsupportedConfigShape)
	}
	for _, l := range body {
		entries = append(entries, strings.TrimPrefix(l, strings.Repeat(" ", indent)))
	}
	return entries, keys, nil
}

// applyPresetKeyBindings rewrites the config's key_bindings block with
// the preset's, appending the entries the preset does not define so the
// user's own bindings survive. Empty values are dropped: they only ever
// existed to unbind a rune.star default that no longer exists.
func applyPresetKeyBindings(
	src []byte, entries []string, keys map[string]bool,
) ([]byte, error) {
	lines := strings.Split(strings.TrimSuffix(string(src), "\n"), "\n")

	cmdIdx := -1
	for i, l := range lines {
		if commandBlockRe.MatchString(l) {
			cmdIdx = i
			break
		}
	}

	var block []string
	switch {
	case cmdIdx < 0:
		if hasTopLevelKey(lines, "command") {
			return nil, fmt.Errorf("%w: inline command mapping",
				errUnsupportedConfigShape)
		}
		block = append([]string{"command:", "  key_bindings:"},
			indented(entries, 4)...)
		lines = append(lines, block...)
	default:
		childIndent, kbIdx, kbIndent := scanCommandBlock(lines, cmdIdx)
		if kbIdx < 0 {
			block = append([]string{
				strings.Repeat(" ", childIndent) + "key_bindings:"},
				indented(entries, childIndent+2)...)
			lines = insertAt(lines, cmdIdx+1, block)
			break
		}
		body := blockBody(lines, kbIdx, kbIndent)
		block = append(indented(entries, kbIndent+2),
			keptBindings(body, keys, kbIndent+2)...)
		lines = replaceRange(lines, kbIdx+1, kbIdx+1+len(body), block)
	}

	stamp := []string{
		fmt.Sprintf("%s: %d", configVersionKey, currentConfigVersion),
		"",
	}
	lines = append(stamp, lines...)
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

// keptBindings returns the config's own entries that the preset does not
// define, so a rebinding the user added is not lost to the migration.
func keptBindings(body []string, presetKeys map[string]bool, indent int) []string {
	var kept []string
	for _, l := range body {
		k, v, ok := parseBindingEntry(l)
		if !ok || presetKeys[k] || v == `""` || v == "''" || v == "" {
			continue
		}
		kept = append(kept, fmt.Sprintf("%s%q: %s",
			strings.Repeat(" ", indent), k, v))
	}
	if len(kept) == 0 {
		return nil
	}
	return append([]string{
		strings.Repeat(" ", indent) + "# Kept from your previous configuration.",
	}, kept...)
}

// blockBody returns the lines belonging to the block opened at idx,
// trimmed of trailing blank and comment lines.
func blockBody(lines []string, idx, indent int) []string {
	end := idx + 1
	for i := idx + 1; i < len(lines); i++ {
		if isBlankOrComment(lines[i]) {
			continue
		}
		if indentOf(lines[i]) <= indent {
			break
		}
		end = i + 1
	}
	return lines[idx+1 : end]
}

func parseBindingEntry(l string) (key, value string, ok bool) {
	if isBlankOrComment(l) {
		return "", "", false
	}
	m := bindingEntryRe.FindStringSubmatch(l)
	if m == nil {
		return "", "", false
	}
	key = m[1]
	if key == "" {
		key = strings.TrimSpace(m[2])
	}
	return key, strings.TrimSpace(m[3]), true
}

func scanCommandBlock(lines []string, cmdIdx int) (childIndent, kbIdx, kbIndent int) {
	childIndent, kbIdx = -1, -1
	for i := cmdIdx + 1; i < len(lines); i++ {
		l := lines[i]
		if isBlankOrComment(l) {
			continue
		}
		indent := indentOf(l)
		if indent == 0 {
			break
		}
		if childIndent < 0 {
			childIndent = indent
		}
		if indent == childIndent && keyBindingsBlockRe.MatchString(l) {
			return childIndent, i, indent
		}
	}
	if childIndent < 0 {
		childIndent = 2
	}
	return childIndent, kbIdx, kbIndent
}

func indented(lines []string, indent int) []string {
	pad := strings.Repeat(" ", indent)
	out := make([]string, len(lines))
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			out[i] = l
			continue
		}
		out[i] = pad + l
	}
	return out
}

func insertAt(lines []string, at int, block []string) []string {
	return replaceRange(lines, at, at, block)
}

func replaceRange(lines []string, from, to int, block []string) []string {
	out := make([]string, 0, len(lines)-(to-from)+len(block))
	out = append(out, lines[:from]...)
	out = append(out, block...)
	return append(out, lines[to:]...)
}

func hasTopLevelKey(lines []string, key string) bool {
	for _, l := range lines {
		if indentOf(l) == 0 && strings.HasPrefix(l, key+":") {
			return true
		}
	}
	return false
}

func isBlankOrComment(l string) bool {
	t := strings.TrimSpace(l)
	return t == "" || strings.HasPrefix(t, "#")
}

func indentOf(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
