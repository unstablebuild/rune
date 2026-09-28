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

package ide

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/llm"
	"unstable.build/rune/internal/text"
)

func validateConfig(cfg map[string]any) (err error) {
	c := ideConfig{cfg: cfg, errors: make(map[string]error)}

	if err = validateAliases(&c, cfg); err != nil {
		return
	}

	if err = validateCommandPrompt(&c, cfg); err != nil {
		return
	}

	if err = validateExo(&c, cfg); err != nil {
		return
	}

	if err = validateQuickMenu(cfg); err != nil {
		return
	}

	if err = llm.ValidateConfig(c.llmConfig()); err != nil {
		return
	}
	if err = validateNotice(cfg); err != nil {
		return
	}
	return
}

func validateAliases(c *ideConfig, cfg map[string]any) (err error) {
	// Cycle detection only inspects alias Commands so we parse the
	// commands without building completer chains, which would require
	// a storage-backed history accessor that the validator doesn't have.
	aliases, _ := c.parseAliasCommands()
	err = text.ValidateCommandAliases(aliases)
	if err != nil {
		// void aliases but keep the rest of config intact.
		// this ensures that text.NewComponent doesn't hard error,
		// preventing user from editing using this same IDE.
		_, ok := c.cfg["command"]
		if !ok {
			panic("empty command config but detected invalid aliases")
		}
		cfg["command"].(map[string]any)[keyCommandAliases] = map[string]any{}
		err = fmt.Errorf("'command.%s' is invalid: %w", keyCommandAliases, err)
	}
	return
}

// validateQuickMenu drops malformed `gui.quick_menu` entries from the
// raw config so nothing downstream — including the native bar's cgo
// bridge — can observe them, and reports what was rejected.
func validateQuickMenu(cfg map[string]any) error {
	guiCfg, ok := cfg[keyGUI].(map[string]any)
	if !ok {
		return nil
	}
	if _, ok := guiCfg[keyQuickMenu]; !ok {
		return nil
	}
	errs := make(map[string]error)
	buttons := parseQuickMenuButtons(config.MapConfig(guiCfg), errs)
	if len(errs) == 0 {
		return nil
	}
	guiCfg[keyQuickMenu] = quickMenuEntries(buttons)
	keys := make([]string, 0, len(errs))
	for key := range errs {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	joined := make([]error, 0, len(keys))
	for _, key := range keys {
		joined = append(joined, fmt.Errorf("'%s' is invalid: %w", key, errs[key]))
	}
	return errors.Join(joined...)
}

// quickMenuEntries renders buttons back into the raw config shape so a
// later parse of the neutralised tree yields exactly the valid subset.
func quickMenuEntries(buttons []QuickMenuButton) []any {
	entries := make([]any, 0, len(buttons))
	for _, button := range buttons {
		entries = append(entries, map[string]any{
			"symbol":  button.Symbol,
			"title":   button.Title,
			"command": strings.Join(button.Command, " "),
		})
	}
	return entries
}

func validateCommandPrompt(c *ideConfig, cfg map[string]any) (err error) {
	commandKey := c.commandKey()
	editorMode := c.editorMode()

	switch editorMode {
	case editorModeVim, editorModeHelix:
		/* no validation needed */
	case editorModeStandard, editorModeEmacs:
		// <c-space> parses/dispatches with Ch == ' ', so treat it as a
		// modified key to avoid a false positive.
		isCtrlSpace := commandKey.Ch == ' ' &&
			commandKey.Key == term.KeySpace && commandKey.Mod == term.ModCtrl
		isIncompatible := !isCtrlSpace && commandKey.Ch != 0 && commandKey.Mod == 0

		if isIncompatible {
			// <s-m-p> stays clear of the emacs control chords (e.g.
			// <c-space> set-mark) so the prompt is always reachable.
			cfg["command"].(map[string]any)[keyCommandKey] = "<s-m-p>"
			return fmt.Errorf("command key must use ctrl, alt, or meta modifiers in " +
				"standard or emacs editor mode otherwise you wouldn't be able to activate it," +
				" falling back to <s-m-p>")
		}
	}
	return
}

// validateExo checks that editor.exo.command is well-formed when the
// editor mode is "exo". On failure it rewrites editor.mode back to
// "vim" so the IDE still boots, and returns a descriptive error.
// editor.exo.goto and editor.exo.quit are required and validated
// the same way; an empty or unparseable value falls back to "vim".
func validateExo(c *ideConfig, cfg map[string]any) (err error) {
	if c.editorMode() != editorModeExo {
		return
	}
	command := c.exoCommand()
	if command == "" || !strings.Contains(command, "{file}") {
		// Rewrite editor.mode back to vim so the IDE boots.
		if ed, ok := cfg["editor"].(map[string]any); ok {
			ed["mode"] = editorModeVim
		}
		return fmt.Errorf("editor.exo.command is required and must contain " +
			"{file} when editor.mode = \"exo\"; falling back to \"vim\"")
	}
	gotoTpl := c.exoGoto()
	if gotoTpl == "" {
		if ed, ok := cfg["editor"].(map[string]any); ok {
			ed["mode"] = editorModeVim
		}
		return fmt.Errorf("editor.exo.goto is required when " +
			"editor.mode = \"exo\"; falling back to \"vim\"")
	}
	if perr := parseGotoTemplate(gotoTpl); perr != nil {
		if ed, ok := cfg["editor"].(map[string]any); ok {
			ed["mode"] = editorModeVim
		}
		return fmt.Errorf("editor.exo.goto is invalid: %w; "+
			"falling back to \"vim\"", perr)
	}
	quitTpl := c.exoQuit()
	if quitTpl == "" {
		if ed, ok := cfg["editor"].(map[string]any); ok {
			ed["mode"] = editorModeVim
		}
		return fmt.Errorf("editor.exo.quit is required when " +
			"editor.mode = \"exo\"; falling back to \"vim\"")
	}
	if _, perr := term.ParseKeys(quitTpl); perr != nil {
		if ed, ok := cfg["editor"].(map[string]any); ok {
			ed["mode"] = editorModeVim
		}
		return fmt.Errorf("editor.exo.quit is invalid: %w; "+
			"falling back to \"vim\"", perr)
	}
	if err = validateExoFallback(c, cfg); err != nil {
		return
	}
	return
}

// validateExoFallback ensures editor.exo.fallback, when set, resolves
// to a valid fallback via normalizeEditorFallback. Unrecognised values
// are rewritten to the canonical default so the IDE still boots. An
// absent fallback key is treated as valid and uses the default.
func validateExoFallback(c *ideConfig, cfg map[string]any) error {
	exo, ok := c.exo()
	if !ok {
		return nil
	}
	raw, err := exo.GetString("fallback")
	if err != nil {
		if err == config.ErrNotFound {
			return nil
		}
		return fmt.Errorf("editor.exo.fallback: %w", err)
	}
	if raw == "" {
		return nil
	}
	if _, ok := normalizeEditorFallback(raw); ok {
		return nil
	}
	if ed, ok := cfg["editor"].(map[string]any); ok {
		if b, ok := ed["exo"].(map[string]any); ok {
			b["fallback"] = editorFallbackStandard
		}
	}
	return fmt.Errorf("editor.exo.fallback must be %q, %q, %q, or %q; got %q, "+
		"falling back to %q",
		editorFallbackVim, editorFallbackHelix, editorFallbackStandard,
		editorFallbackEmacs, raw, editorFallbackStandard)
}

// parseGotoTemplate splits tpl around {line}/{col} placeholders and
// validates that each literal segment parses with term.ParseKeys.
// Returns nil if the template is well-formed.
func parseGotoTemplate(tpl string) error {
	segments := splitGotoTemplate(tpl)
	for _, seg := range segments {
		if seg.placeholder != "" {
			continue
		}
		if seg.literal == "" {
			continue
		}
		if _, perr := term.ParseKeys(seg.literal); perr != nil {
			return fmt.Errorf("segment %q: %w", seg.literal, perr)
		}
	}
	return nil
}

// gotoSegment is either a literal key sequence string or a {line}/{col}
// placeholder.
type gotoSegment struct {
	literal     string
	placeholder string // "line" or "col"
}

// validateNotice rewrites an invalid workspace.notice.show back to
// "once" so the IDE still boots when the user supplies an
// unrecognised value.
func validateNotice(cfg map[string]any) error {
	ws, ok := cfg["workspace"].(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := ws["notice"]
	if !ok {
		return nil
	}
	notice, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("workspace.notice must be a mapping, got %T", raw)
	}
	for k := range notice {
		switch k {
		case "path", "literal", "show":
		default:
			return fmt.Errorf("workspace.notice has unknown key %q "+
				"(expected path, literal, show)", k)
		}
	}
	if v, present := notice["show"]; present {
		s, ok := v.(string)
		if !ok {
			notice["show"] = "once"
			return fmt.Errorf("workspace.notice.show must be a string, "+
				"got %T; falling back to %q", v, "once")
		}
		switch s {
		case "", "once", "always":
		default:
			notice["show"] = "once"
			return fmt.Errorf("workspace.notice.show must be %q or %q; "+
				"got %q, falling back to %q", "once", "always", s, "once")
		}
	}
	return nil
}

// splitGotoTemplate splits tpl into literal/placeholder segments.
// Unknown {...} placeholders are treated as literal text.
func splitGotoTemplate(tpl string) []gotoSegment {
	var out []gotoSegment
	for tpl != "" {
		lineIdx := strings.Index(tpl, "{line}")
		colIdx := strings.Index(tpl, "{col}")
		var idx int
		var name string
		var width int
		switch {
		case lineIdx == -1 && colIdx == -1:
			out = append(out, gotoSegment{literal: tpl})
			return out
		case lineIdx == -1:
			idx, name, width = colIdx, "col", len("{col}")
		case colIdx == -1:
			idx, name, width = lineIdx, "line", len("{line}")
		case lineIdx < colIdx:
			idx, name, width = lineIdx, "line", len("{line}")
		default:
			idx, name, width = colIdx, "col", len("{col}")
		}
		if idx > 0 {
			out = append(out, gotoSegment{literal: tpl[:idx]})
		}
		out = append(out, gotoSegment{placeholder: name})
		tpl = tpl[idx+width:]
	}
	return out
}
