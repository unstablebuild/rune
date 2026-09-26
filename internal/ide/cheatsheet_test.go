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
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/text"
)

func cheatsheetTestConfig() text.Config {
	cfg := text.DefaultConfig()
	cfg.CommandKeyBindings = map[term.KeyComb][][]string{
		{Ch: 'h', Mod: term.ModMeta}: {{"windowfocus", "left"}},
		{Ch: 'n', Mod: term.ModMeta}: {{"windownew"}},
		{Ch: 'd', Mod: term.ModAlt}:  {{"lsp", "definition"}},
	}
	return cfg
}

// TestCheatsheetResolvesBoundKeys verifies the key column holds the
// user's resolved chords for bound commands.
func TestCheatsheetResolvesBoundKeys(t *testing.T) {
	md, err := renderCheatsheet(cheatsheetTestConfig(), true, "vim", false)
	require.NoError(t, err)

	assert.Contains(t, md, "| `<meta-h>` | Focus the window in a direction")
	assert.Contains(t, md, "| `<meta-n>` | Open a new window with the default split orientation |")
	assert.Contains(t, md, "| `<alt-d>` | Go to definition |")

	assert.True(t, strings.HasPrefix(md, "# Rune cheatsheet"),
		"the license template comment must not render into the output")
}

// TestCheatsheetUnboundCommandsFallBack verifies commands with no bound
// key render with the command-prompt sequence and still appear.
func TestCheatsheetUnboundCommandsFallBack(t *testing.T) {
	md, err := renderCheatsheet(cheatsheetTestConfig(), true, "vim", false)
	require.NoError(t, err)

	assert.Contains(t, md, "| `: docs` | Open the documentation |")
	assert.Contains(t, md, "| `: help` | Open the docs workspace and ask the help agent |")
	assert.Contains(t, md, "| `: edit` | Open a file for editing |")
}

// TestCheatsheetUnboundCommandFallbackKeepsArgs verifies that an unbound
// command with arguments renders the full command line in the key column,
// not just the bare command name.
func TestCheatsheetUnboundCommandFallbackKeepsArgs(t *testing.T) {
	md, err := renderCheatsheet(cheatsheetTestConfig(), true, "vim", false)
	require.NoError(t, err)

	assert.Contains(t, md, "| `: tutorial start basics` | Start the basics tutorial |")
	assert.NotContains(t, md, "| `: tutorial` | Start the basics tutorial |")
}

// TestCheatsheetCloseHintFollowsWindowClose verifies the close hint names
// the configured windowclose chord, which is <alt-w> rather than <meta-w>
// on Linux, where Super belongs to the desktop.
func TestCheatsheetCloseHintFollowsWindowClose(t *testing.T) {
	cfg := cheatsheetTestConfig()
	md, err := renderCheatsheet(cfg, true, "vim", false)
	require.NoError(t, err)
	assert.Contains(t, md, "To close this cheatsheet press `: windowclose`.")

	cfg.CommandKeyBindings[term.KeyComb{Ch: 'w', Mod: term.ModAlt}] = [][]string{{"windowclose"}}
	md, err = renderCheatsheet(cfg, true, "vim", false)
	require.NoError(t, err)
	assert.Contains(t, md, "To close this cheatsheet press `<alt-w>`.")
}

// TestCheatsheetModalTips verifies the prompt-navigation rows switch on
// editor mode: modal shows the vi-style chords, standard shows arrows.
func TestCheatsheetModalTips(t *testing.T) {
	cfg := cheatsheetTestConfig()

	modal, err := renderCheatsheet(cfg, true, "vim", false)
	require.NoError(t, err)
	assert.Contains(t, modal, "`<ctrl-j>` / `<ctrl-k>`")
	assert.NotContains(t, modal, "| `<up>` / `<down>` | Move through the results |")

	standard, err := renderCheatsheet(cfg, false, "standard", false)
	require.NoError(t, err)
	assert.NotContains(t, standard, "`<ctrl-j>` / `<ctrl-k>`")
	assert.Contains(t, standard, "| `<up>` / `<down>` | Move through the results |")
}

// TestCheatsheetAutoSaveGatesWriteRow verifies the manual write row is
// shown only when auto-save is off.
func TestCheatsheetAutoSaveGatesWriteRow(t *testing.T) {
	cfg := cheatsheetTestConfig()

	off, err := renderCheatsheet(cfg, true, "vim", false)
	require.NoError(t, err)
	assert.Contains(t, off, "Flush file changes to disk")

	on, err := renderCheatsheet(cfg, true, "vim", true)
	require.NoError(t, err)
	assert.NotContains(t, on, "Flush file changes to disk")
}

// TestCheatsheetEditorSection verifies the Editor section names the active
// editor and links to its docs guide, mentioning exo only in exo mode.
func TestCheatsheetEditorSection(t *testing.T) {
	cfg := cheatsheetTestConfig()

	modal, err := renderCheatsheet(cfg, true, "vim", false)
	require.NoError(t, err)
	assert.Contains(t, modal, "**vim** editor")
	assert.Contains(t, modal, "https://docs.rune.build/learn/vim-editor")
	assert.NotContains(t, modal, "exo")

	standard, err := renderCheatsheet(cfg, false, "standard", false)
	require.NoError(t, err)
	assert.Contains(t, standard, "**standard** editor")
	assert.Contains(t, standard, "https://docs.rune.build/learn/standard-editor")
	assert.NotContains(t, standard, "exo")

	exo, err := renderCheatsheet(cfg, true, "exo", false)
	require.NoError(t, err)
	assert.Contains(t, exo, "**exo** editor")
	assert.Contains(t, exo, "https://docs.rune.build/learn/exoeditor")
}

// TestCheatsheetWorkspacesPrecedeWindows asserts the Workspaces section is
// rendered before the Windows section, reflecting the layout hierarchy.
func TestCheatsheetWorkspacesPrecedeWindows(t *testing.T) {
	md, err := renderCheatsheet(cheatsheetTestConfig(), true, "vim", false)
	require.NoError(t, err)

	wsIdx := strings.Index(md, "## Workspaces")
	winIdx := strings.Index(md, "## Windows")
	require.NotEqual(t, -1, wsIdx)
	require.NotEqual(t, -1, winIdx)
	assert.Less(t, wsIdx, winIdx, "Workspaces section must come before Windows")
}

// TestCheatsheetSearchSection asserts the Search section lists the
// workspace search commands sourced from the docs search guide.
func TestCheatsheetSearchSection(t *testing.T) {
	md, err := renderCheatsheet(cheatsheetTestConfig(), true, "vim", false)
	require.NoError(t, err)

	assert.Contains(t, md, "## Search")
	assert.Contains(t, md, "Fuzzy-find a file by name")
	assert.Contains(t, md, "Search file contents across the workspace")
	assert.Contains(t, md, "Jump to a function or method in the current file")
}

// TestCheatsheetJumpToastRows asserts the three jumptoast rows resolve to
// the user's `echo {prompt}jumptoast ...` prefill bindings by matching the
// per-kind capture, since the command name alone is unbound.
func TestCheatsheetJumpToastRows(t *testing.T) {
	cfg := text.DefaultConfig()
	cfg.CommandKeyBindings = map[term.KeyComb][][]string{
		{Ch: 'f', Mod: term.ModAlt}: {{"echo", "{prompt}jumptoast<space>locals.scm<space>local.definition.method|local.definition.function<space>"}},
		{Ch: 'v', Mod: term.ModAlt}: {{"echo", "{prompt}jumptoast<space>locals.scm<space>local.definition.var<space>"}},
		{Ch: 's', Mod: term.ModAlt}: {{"echo", "{prompt}jumptoast<space>locals.scm<space>local.definition.type<space>"}},
	}
	md, err := renderCheatsheet(cfg, true, "vim", false)
	require.NoError(t, err)

	assert.Contains(t, md, "| `<alt-f>` | Jump to a function or method in the current file |")
	assert.Contains(t, md, "| `<alt-v>` | Jump to a variable in the current file |")
	assert.Contains(t, md, "| `<alt-s>` | Jump to a type in the current file |")
}

// TestCheatsheetJumpToastFallback asserts the jumptoast rows fall back to
// the command-prompt sequence when no binding is present.
func TestCheatsheetJumpToastFallback(t *testing.T) {
	md, err := renderCheatsheet(text.DefaultConfig(), true, "vim", false)
	require.NoError(t, err)

	assert.Contains(t, md, "| `: jumptoast` | Jump to a function or method in the current file |")
}

// TestOpenCheatsheetLink verifies http(s) links open in the browser and are
// reported handled, while other schemes are left to the default handler.
func TestOpenCheatsheetLink(t *testing.T) {
	prev := browseURL
	t.Cleanup(func() { browseURL = prev })

	var opened []string
	browseURL = func(urls ...*url.URL) error {
		for _, u := range urls {
			opened = append(opened, u.String())
		}
		return nil
	}

	for _, scheme := range []string{"http", "https"} {
		u := &url.URL{Scheme: scheme, Host: "docs.rune.build", Path: "/learn/vim-editor"}
		assert.True(t, openCheatsheetLink(u), "%s link must be handled", scheme)
	}
	assert.Equal(t, []string{
		"http://docs.rune.build/learn/vim-editor",
		"https://docs.rune.build/learn/vim-editor",
	}, opened)

	opened = nil
	for _, scheme := range []string{"", "mailto", "file", "#anchor"} {
		u := &url.URL{Scheme: scheme, Path: "x"}
		assert.False(t, openCheatsheetLink(u), "%q link must fall through", scheme)
	}
	assert.Empty(t, opened, "non-http links must not open a browser")
}

// TestOpenCheatsheetLinkToleratesError verifies a browser failure is
// swallowed and the link is still reported handled.
func TestOpenCheatsheetLinkToleratesError(t *testing.T) {
	prev := browseURL
	t.Cleanup(func() { browseURL = prev })
	browseURL = func(...*url.URL) error { return assert.AnError }

	u := &url.URL{Scheme: "https", Host: "docs.rune.build"}
	assert.True(t, openCheatsheetLink(u))
}

// TestCheatsheetFirstWriterWins verifies that when a command is bound to
// multiple chords the cheatsheet keeps a single deterministic key and
// the template executes without error.
func TestCheatsheetFirstWriterWins(t *testing.T) {
	cfg := text.DefaultConfig()
	cfg.CommandKeyBindings = map[term.KeyComb][][]string{
		{Ch: 'n', Mod: term.ModMeta}: {{"windownew"}},
		{Ch: 'm', Mod: term.ModMeta}: {{"windownew"}},
	}
	md, err := renderCheatsheet(cfg, true, "vim", false)
	require.NoError(t, err)

	count := strings.Count(md, "| Open a new window with the default split orientation |")
	assert.Equal(t, 1, count)
	newLines := 0
	for line := range strings.SplitSeq(md, "\n") {
		if strings.Contains(line, "Open a new window") {
			newLines++
			assert.True(t,
				strings.Contains(line, "<meta-n>") || strings.Contains(line, "<meta-m>"),
				"row must use one of the bound chords: %s", line)
		}
	}
	assert.Equal(t, 1, newLines)
}
