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

package starlarktutorial

import (
	"fmt"
	"strings"

	"unstable.build/rune/internal/handler/command"
)

// buildWaitCommandHint composes the markdown body of a wait_command
// screen. A step that declared text is rendered as written: the copy
// is expected to say what to run and how. A step without text gets a
// generated hint naming the command, the key bound to it and its
// manual when lookup returns one.
func buildWaitCommandHint(
	r *request, cmdKey string, lookup CommandManualLookup,
	keyForCommand func(cmd string, args []string) string,
) string {
	if r == nil {
		return ""
	}
	if r.text != "" {
		return expandCmdTemplate(r.text, cmdKey)
	}
	cmdName := r.command
	var b strings.Builder
	fmt.Fprintf(&b, "Run `%s` from the command prompt (`%s`)", cmdName, cmdKey)
	if boundKey := waitCommandBoundKey(cmdName, keyForCommand); boundKey != "" {
		fmt.Fprintf(&b, ", or press `%s`", boundKey)
	}
	b.WriteString(".\n\n")
	if lookup != nil {
		if man, ok := lookup(commandName(cmdName)); ok {
			b.WriteString(renderCommandManual(man))
		}
	}
	return b.String()
}

// waitCommandBoundKey resolves the pretty key spec bound to the
// awaited command (the first token of cmd is the command name and the
// rest are arguments). Returns "" when no resolver is wired or the
// command has no binding.
func waitCommandBoundKey(
	cmd string, keyForCommand func(name string, args []string) string,
) string {
	if keyForCommand == nil {
		return ""
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	return keyForCommand(fields[0], fields[1:])
}

// commandName is the command name of an awaited, possibly argument-
// qualified wait_command spec ("! git log" -> "!").
func commandName(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return cmd
	}
	return fields[0]
}

// buildWaitShellHint composes the markdown body of a wait_shell
// screen: the step's own text, or a generated line asking the user to
// run the expected command inside Rune's console.
func buildWaitShellHint(r *request, cmdKey string) string {
	if r == nil {
		return ""
	}
	if r.text != "" {
		return expandCmdTemplate(r.text, cmdKey)
	}
	return fmt.Sprintf("Run `%s` in Rune's console.\n",
		strings.Join(r.shellArgs, " "))
}

// renderCommandManual returns a compact, markdown summary of man
// suitable for embedding in the wait_command screen. The
// layout intentionally mirrors what handler/command renders in its
// own manual overlay so the user sees the same shape they would in
// the live prompt.
func renderCommandManual(man command.Manual) string {
	var b strings.Builder
	if man.Synopsis != "" {
		fmt.Fprintf(&b, "**Usage:** `%s %s`\n\n", man.Name, man.Synopsis)
	} else {
		fmt.Fprintf(&b, "**Usage:** `%s`\n\n", man.Name)
	}
	if len(man.AliasOf) > 0 {
		if len(man.AliasOf) == 1 {
			fmt.Fprintf(&b, "Alias of `%s`.\n\n", man.AliasOf[0])
		} else {
			b.WriteString("Alias of the following sequence of commands:\n\n")
			for _, c := range man.AliasOf {
				fmt.Fprintf(&b, "- `%s`\n", c)
			}
			b.WriteString("\n")
		}
	}
	if man.Summary != "" {
		b.WriteString(man.Summary)
		b.WriteString("\n")
	}
	if len(man.Commands) > 0 {
		b.WriteString("\n**Subcommands:**\n\n")
		for _, sub := range man.Commands {
			if sub.Summary != "" {
				fmt.Fprintf(&b, "- `%s` — %s\n", sub.Name, sub.Summary)
			} else {
				fmt.Fprintf(&b, "- `%s`\n", sub.Name)
			}
		}
	}
	return b.String()
}

// expandCmdTemplate replaces every <cmd> token in s with the
// already-prettified command-key display string.
func expandCmdTemplate(s, cmdKey string) string {
	return strings.ReplaceAll(s, "<cmd>", cmdKey)
}

// prettyKeySpecs maps a rendered key spec to a friendlier form for
// tutorial copy. It is display-only and never affects key matching.
var prettyKeySpecs = map[string]string{"<shift-;>": ":"}

// PrettyKeySpec rewrites a rendered key spec to its tutorial-friendly
// form (e.g. "<shift-;>" becomes ":"). Unmapped specs pass through
// unchanged. Used only for display in tutorial copy.
func PrettyKeySpec(s string) string {
	if p, ok := prettyKeySpecs[s]; ok {
		return p
	}
	return s
}
