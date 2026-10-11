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
	_ "embed"
	"fmt"
	"strings"
	"text/template"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/idetutorial/starlarktutorial"
	"unstable.build/rune/internal/text"
)

//go:embed cheatsheet.md.tmpl
var cheatsheetTmpl string

// cheatsheetWidth is the fixed width of the cheatsheet floating window.
// The content is known and authored to fit, so a static width keeps the
// table columns tight instead of stretching to the markdown component's
// natural ideal width.
const cheatsheetWidth = 100

// cheatsheetData is the template context for cheatsheet.md.tmpl.
type cheatsheetData struct {
	CommandKey string
	Modal      bool
	EditorMode string
	AutoSave   bool
}

// renderCheatsheet fills cheatsheet.md.tmpl from the resolved key
// bindings and editor mode, producing a keys-first Markdown cheatsheet.
// The `key` template func resolves a command line to the user's bound
// chord, falling back to the command-prompt sequence when unbound.
func renderCheatsheet(cfg text.Config, modal bool, editorMode string, autoSave bool) (string, error) {
	commandKey := starlarktutorial.PrettyKeySpec(cfg.CommandEvent.String())
	lookup := commandKeyLookup(cfg.CommandKeyBindings)

	key := func(cmd string, args ...string) string {
		line := strings.Join(append([]string{cmd}, args...), " ")
		if k, ok := lookup[line]; ok {
			return k
		}
		if len(args) > 0 {
			if k, ok := lookup[cmd]; ok {
				return k
			}
		}
		return commandKey + " " + line
	}

	// jumptoast bindings are prompt-prefill `echo {prompt}jumptoast ...`
	// macros, so the command-line lookup cannot match them by name. Match
	// the binding by its `local.definition.<kind>` capture instead and
	// fall back to the command-prompt sequence when no binding is found.
	jumpKey := func(kind string) string {
		capture := "local.definition." + kind
		for line, k := range lookup {
			if strings.Contains(line, "jumptoast") && strings.Contains(line, capture) {
				return k
			}
		}
		return commandKey + " jumptoast"
	}

	tmpl, err := template.New("cheatsheet").Funcs(template.FuncMap{
		"key":     key,
		"jumpkey": jumpKey,
	}).Parse(cheatsheetTmpl)
	if err != nil {
		return "", fmt.Errorf("parse cheatsheet template: %w", err)
	}

	var b strings.Builder
	if err := tmpl.Execute(&b, cheatsheetData{
		CommandKey: commandKey,
		Modal:      modal,
		EditorMode: editorMode,
		AutoSave:   autoSave,
	}); err != nil {
		return "", fmt.Errorf("execute cheatsheet template: %w", err)
	}
	return b.String(), nil
}

// commandKeyLookup inverts resolved key bindings into a command-line to
// pretty-key map. Each command line in a binding maps to that binding's
// key; the first writer wins so earlier bindings stay stable.
func commandKeyLookup(bindings map[term.KeyComb][][]string) map[string]string {
	lookup := make(map[string]string)
	for kc, cmds := range bindings {
		key := starlarktutorial.PrettyKeySpec(kc.String())
		for _, cmdAndArgs := range cmds {
			line := strings.Join(cmdAndArgs, " ")
			if _, ok := lookup[line]; !ok {
				lookup[line] = key
			}
		}
	}
	return lookup
}
